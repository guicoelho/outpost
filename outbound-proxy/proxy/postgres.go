package proxy

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"

	"github.com/jackc/pgproto3/v2"

	"outbound-proxy/config"
)

// StartPostgresProxy starts a PostgreSQL TCP proxy listener for one managed tool.
func StartPostgresProxy(ctx context.Context, tool config.ManagedTool, listenAddr string, logger *log.Logger) (net.Listener, error) {
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", listenAddr, err)
	}

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				logger.Printf("action=error tool=%s phase=accept err=%v", tool.Name, err)
				continue
			}

			go handlePostgresClient(ctx, conn, tool, logger)
		}
	}()

	return ln, nil
}

func handlePostgresClient(ctx context.Context, clientConn net.Conn, tool config.ManagedTool, logger *log.Logger) {
	defer clientConn.Close()

	username, password, err := extractPostgresCredentials(tool)
	if err != nil {
		logger.Printf("action=blocked tool=%s reason=%v", tool.Name, err)
		return
	}

	clientBackend := pgproto3.NewBackend(pgproto3.NewChunkReader(clientConn), clientConn)
	startup, err := receiveClientStartup(clientBackend, clientConn)
	if err != nil {
		logger.Printf("action=error tool=%s phase=startup err=%v", tool.Name, err)
		return
	}

	rawConn, err := net.Dial("tcp", tool.Match)
	if err != nil {
		logger.Printf("action=error tool=%s phase=dial destination=%s err=%v", tool.Name, tool.Match, err)
		return
	}

	upstreamConn, err := upgradeToTLS(rawConn, tool.Match)
	if err != nil {
		rawConn.Close()
		logger.Printf("action=error tool=%s phase=tls err=%v", tool.Name, err)
		return
	}
	defer upstreamConn.Close()

	params := make(map[string]string, len(startup.Parameters)+1)
	for k, v := range startup.Parameters {
		params[k] = v
	}
	params["user"] = username

	newStartup := &pgproto3.StartupMessage{
		ProtocolVersion: startup.ProtocolVersion,
		Parameters:      params,
	}
	if err := writeMessage(upstreamConn, newStartup); err != nil {
		logger.Printf("action=error tool=%s phase=send_startup err=%v", tool.Name, err)
		return
	}

	upstreamFrontend := pgproto3.NewFrontend(pgproto3.NewChunkReader(upstreamConn), upstreamConn)

	if err := runAuthHandshake(upstreamConn, upstreamFrontend, clientConn, username, password); err != nil {
		logger.Printf("action=blocked tool=%s reason=%v", tool.Name, err)
		return
	}

	logger.Printf("action=managed tool=%s destination=%s", tool.Name, tool.Match)
	relayBidirectional(ctx, clientConn, upstreamConn)
}

// upgradeToTLS negotiates SSL with the upstream PostgreSQL server.
func upgradeToTLS(conn net.Conn, target string) (net.Conn, error) {
	// PostgreSQL SSLRequest: 4-byte length (8) + 4-byte code (80877103)
	sslRequest := [8]byte{0, 0, 0, 8, 0x04, 0xD2, 0x16, 0x2F}
	if _, err := conn.Write(sslRequest[:]); err != nil {
		return nil, fmt.Errorf("write ssl request: %w", err)
	}

	resp := make([]byte, 1)
	if _, err := io.ReadFull(conn, resp); err != nil {
		return nil, fmt.Errorf("read ssl response: %w", err)
	}

	if resp[0] != 'S' {
		return conn, nil
	}

	host, _, err := net.SplitHostPort(target)
	if err != nil {
		host = target
	}
	tlsConn := tls.Client(conn, &tls.Config{ServerName: host})
	if err := tlsConn.Handshake(); err != nil {
		return nil, fmt.Errorf("tls handshake: %w", err)
	}
	return tlsConn, nil
}

func receiveClientStartup(backend *pgproto3.Backend, clientConn net.Conn) (*pgproto3.StartupMessage, error) {
	for {
		msg, err := backend.ReceiveStartupMessage()
		if err != nil {
			return nil, err
		}

		switch m := msg.(type) {
		case *pgproto3.SSLRequest:
			if _, err := clientConn.Write([]byte("N")); err != nil {
				return nil, err
			}
		case *pgproto3.StartupMessage:
			return m, nil
		default:
			return nil, fmt.Errorf("unsupported startup message %T", msg)
		}
	}
}

func runAuthHandshake(upstreamConn net.Conn, frontend *pgproto3.Frontend, clientConn net.Conn, username, password string) error {
	for {
		msg, err := frontend.Receive()
		if err != nil {
			return err
		}

		switch m := msg.(type) {
		case *pgproto3.AuthenticationCleartextPassword:
			pwMsg := &pgproto3.PasswordMessage{Password: password}
			if err := writeMessage(upstreamConn, pwMsg); err != nil {
				return err
			}
		case *pgproto3.AuthenticationMD5Password:
			pwMsg := &pgproto3.PasswordMessage{Password: md5Password(password, username, m.Salt[:])}
			if err := writeMessage(upstreamConn, pwMsg); err != nil {
				return err
			}
		case *pgproto3.AuthenticationSASL:
			if err := performSCRAM(upstreamConn, frontend, username, password, m.AuthMechanisms); err != nil {
				return err
			}
		case *pgproto3.ErrorResponse:
			_ = writeMessage(clientConn, m)
			return fmt.Errorf("postgres error: %s", m.Message)
		default:
			if err := writeMessage(clientConn, msg); err != nil {
				return err
			}
			if _, ok := msg.(*pgproto3.ReadyForQuery); ok {
				return nil
			}
		}
	}
}

func performSCRAM(upstreamConn net.Conn, frontend *pgproto3.Frontend, username, password string, mechanisms []string) error {
	supported := false
	for _, m := range mechanisms {
		if m == "SCRAM-SHA-256" {
			supported = true
			break
		}
	}
	if !supported {
		return fmt.Errorf("server does not offer SCRAM-SHA-256, offered: %v", mechanisms)
	}

	// Generate client nonce.
	nonceBytes := make([]byte, 18)
	if _, err := rand.Read(nonceBytes); err != nil {
		return fmt.Errorf("generate nonce: %w", err)
	}
	clientNonce := base64.StdEncoding.EncodeToString(nonceBytes)

	// Build client-first-message.
	clientFirstBare := fmt.Sprintf("n=%s,r=%s", scramUsername(username), clientNonce)
	clientFirstMsg := "n,," + clientFirstBare

	saslInit := &pgproto3.SASLInitialResponse{
		AuthMechanism: "SCRAM-SHA-256",
		Data:          []byte(clientFirstMsg),
	}
	if err := writeMessage(upstreamConn, saslInit); err != nil {
		return fmt.Errorf("send sasl initial: %w", err)
	}

	// Receive server-first-message.
	msg, err := frontend.Receive()
	if err != nil {
		return fmt.Errorf("receive sasl continue: %w", err)
	}
	saslContinue, ok := msg.(*pgproto3.AuthenticationSASLContinue)
	if !ok {
		if errResp, ok := msg.(*pgproto3.ErrorResponse); ok {
			return fmt.Errorf("postgres error during SASL: %s", errResp.Message)
		}
		return fmt.Errorf("expected AuthenticationSASLContinue, got %T", msg)
	}

	serverFirstMsg := string(saslContinue.Data)
	serverNonce, salt, iterations, err := parseServerFirst(serverFirstMsg)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(serverNonce, clientNonce) {
		return fmt.Errorf("server nonce does not start with client nonce")
	}

	// Derive keys.
	saltedPassword := pbkdf2SHA256([]byte(password), salt, iterations)
	clientKey := hmacSHA256(saltedPassword, []byte("Client Key"))
	storedKey := sha256Sum(clientKey)

	clientFinalNoProof := fmt.Sprintf("c=biws,r=%s", serverNonce)
	authMessage := clientFirstBare + "," + serverFirstMsg + "," + clientFinalNoProof

	clientSignature := hmacSHA256(storedKey, []byte(authMessage))
	clientProof := xorBytes(clientKey, clientSignature)

	serverKey := hmacSHA256(saltedPassword, []byte("Server Key"))
	expectedServerSig := hmacSHA256(serverKey, []byte(authMessage))

	clientFinalMsg := clientFinalNoProof + ",p=" + base64.StdEncoding.EncodeToString(clientProof)

	saslResp := &pgproto3.SASLResponse{Data: []byte(clientFinalMsg)}
	if err := writeMessage(upstreamConn, saslResp); err != nil {
		return fmt.Errorf("send sasl response: %w", err)
	}

	// Receive server-final-message.
	msg, err = frontend.Receive()
	if err != nil {
		return fmt.Errorf("receive sasl final: %w", err)
	}
	saslFinal, ok := msg.(*pgproto3.AuthenticationSASLFinal)
	if !ok {
		if errResp, ok := msg.(*pgproto3.ErrorResponse); ok {
			return fmt.Errorf("postgres error during SASL final: %s", errResp.Message)
		}
		return fmt.Errorf("expected AuthenticationSASLFinal, got %T", msg)
	}

	serverFinalMsg := string(saslFinal.Data)
	if !strings.HasPrefix(serverFinalMsg, "v=") {
		return fmt.Errorf("invalid server-final-message: %s", serverFinalMsg)
	}
	serverSig, err := base64.StdEncoding.DecodeString(serverFinalMsg[2:])
	if err != nil {
		return fmt.Errorf("decode server signature: %w", err)
	}
	if !hmac.Equal(serverSig, expectedServerSig) {
		return fmt.Errorf("server signature verification failed")
	}

	return nil
}

func parseServerFirst(msg string) (nonce string, salt []byte, iterations int, err error) {
	for _, part := range strings.Split(msg, ",") {
		switch {
		case strings.HasPrefix(part, "r="):
			nonce = part[2:]
		case strings.HasPrefix(part, "s="):
			salt, err = base64.StdEncoding.DecodeString(part[2:])
			if err != nil {
				return "", nil, 0, fmt.Errorf("decode salt: %w", err)
			}
		case strings.HasPrefix(part, "i="):
			iterations, err = strconv.Atoi(part[2:])
			if err != nil {
				return "", nil, 0, fmt.Errorf("parse iterations: %w", err)
			}
		}
	}
	if nonce == "" || salt == nil || iterations == 0 {
		return "", nil, 0, fmt.Errorf("incomplete server-first-message: %s", msg)
	}
	return nonce, salt, iterations, nil
}

// scramUsername escapes '=' and ',' per RFC 5802.
func scramUsername(username string) string {
	username = strings.ReplaceAll(username, "=", "=3D")
	username = strings.ReplaceAll(username, ",", "=2C")
	return username
}

// pbkdf2SHA256 derives a key using PBKDF2 with HMAC-SHA-256 (single block).
func pbkdf2SHA256(password, salt []byte, iterations int) []byte {
	mac := hmac.New(sha256.New, password)
	mac.Write(salt)
	mac.Write([]byte{0, 0, 0, 1})
	u := mac.Sum(nil)

	result := make([]byte, sha256.Size)
	copy(result, u)

	for i := 1; i < iterations; i++ {
		mac.Reset()
		mac.Write(u)
		u = mac.Sum(nil)
		for j := range result {
			result[j] ^= u[j]
		}
	}
	return result
}

func hmacSHA256(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}

func sha256Sum(data []byte) []byte {
	h := sha256.Sum256(data)
	return h[:]
}

func xorBytes(a, b []byte) []byte {
	out := make([]byte, len(a))
	for i := range a {
		out[i] = a[i] ^ b[i]
	}
	return out
}

func relayBidirectional(ctx context.Context, a, b net.Conn) {
	var once sync.Once
	closeBoth := func() {
		once.Do(func() {
			_ = a.Close()
			_ = b.Close()
		})
	}

	errCh := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(a, b)
		errCh <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(b, a)
		errCh <- struct{}{}
	}()

	select {
	case <-ctx.Done():
		closeBoth()
	case <-errCh:
		closeBoth()
	}
}

func extractPostgresCredentials(tool config.ManagedTool) (string, string, error) {
	username := strings.TrimSpace(tool.Credentials.Username)
	password := strings.TrimSpace(tool.Credentials.Password)

	if username != "" && password != "" {
		return username, password, nil
	}

	if strings.TrimSpace(tool.Credentials.Ref) == "" {
		return "", "", fmt.Errorf("missing postgres credentials for tool %s (set credentials.username/password or credentials.ref=user:password)", tool.Name)
	}

	parts := strings.SplitN(tool.Credentials.Ref, ":", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", fmt.Errorf("invalid credentials.ref for tool %s: expected user:password", tool.Name)
	}

	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), nil
}

func md5Password(password, user string, salt []byte) string {
	inner := md5.Sum([]byte(password + user))
	innerHex := hex.EncodeToString(inner[:])
	outer := md5.New()
	_, _ = outer.Write([]byte(innerHex))
	_, _ = outer.Write(salt)
	return "md5" + hex.EncodeToString(outer.Sum(nil))
}

type pgEncodable interface {
	Encode(dst []byte) ([]byte, error)
}

func writeMessage(conn net.Conn, msg pgEncodable) error {
	buf, err := msg.Encode(nil)
	if err != nil {
		return err
	}
	_, err = conn.Write(buf)
	return err
}
