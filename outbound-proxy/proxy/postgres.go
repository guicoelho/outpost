package proxy

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
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

	upstreamConn, err := net.Dial("tcp", tool.Match)
	if err != nil {
		logger.Printf("action=error tool=%s phase=dial destination=%s err=%v", tool.Name, tool.Match, err)
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
			return fmt.Errorf("SASL authentication is not supported by this proxy")
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
