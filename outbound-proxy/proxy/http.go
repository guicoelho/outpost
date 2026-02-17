package proxy

import (
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/elazarl/goproxy"

	"outbound-proxy/config"
)

// NewHTTPServer builds the outbound HTTP/HTTPS proxy server.
func NewHTTPServer(cfg *config.Config, ca *x509.Certificate, key *rsa.PrivateKey, addr string) (*http.Server, error) {
	if addr == "" {
		addr = ":3128"
	}

	logger := log.New(os.Stdout, "", log.LstdFlags|log.LUTC)
	rateLimiter := NewRateLimiter()

	tlsCert := tls.Certificate{
		Certificate: [][]byte{ca.Raw},
		PrivateKey:  key,
		Leaf:        ca,
	}

	mitmAction := &goproxy.ConnectAction{
		Action:    goproxy.ConnectMitm,
		TLSConfig: goproxy.TLSConfigFromCA(&tlsCert),
	}

	p := goproxy.NewProxyHttpServer()
	p.Verbose = false

	p.OnRequest().HandleConnect(goproxy.FuncHttpsHandler(func(host string, ctx *goproxy.ProxyCtx) (*goproxy.ConnectAction, string) {
		destHost, destHostPort := normalizeDest(host, "https")

		if isBlocked(destHost, destHostPort, cfg.Blocked) {
			logger.Printf("action=blocked destination=%s", host)
			return goproxy.RejectConnect, host
		}

		if _, ok := matchManagedHTTPTool(cfg.ManagedTools, destHost, destHostPort); ok {
			return mitmAction, host
		}

		logger.Printf("action=pass destination=%s", host)
		return goproxy.OkConnect, host
	}))

	p.OnRequest().DoFunc(func(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
		host, hostPort := requestDestination(req)

		if isBlocked(host, hostPort, cfg.Blocked) {
			logger.Printf("action=blocked destination=%s", hostPort)
			return req, goproxy.NewResponse(req, goproxy.ContentTypeText, http.StatusForbidden, "destination blocked by policy")
		}

		tool, managed := matchManagedHTTPTool(cfg.ManagedTools, host, hostPort)
		if !managed {
			logger.Printf("action=pass destination=%s", hostPort)
			return req, nil
		}

		if err := CheckMethod(req.Method, tool.Policy.Methods); err != nil {
			status, msg := statusAndMessage(err)
			logger.Printf("action=blocked destination=%s reason=%s", hostPort, msg)
			return req, goproxy.NewResponse(req, goproxy.ContentTypeText, status, msg)
		}

		if err := CheckPath(req.URL.Path, tool.Policy.Paths); err != nil {
			status, msg := statusAndMessage(err)
			logger.Printf("action=blocked destination=%s reason=%s", hostPort, msg)
			return req, goproxy.NewResponse(req, goproxy.ContentTypeText, status, msg)
		}

		if err := rateLimiter.Check(tool.Name, tool.Policy.RateLimit); err != nil {
			status, msg := statusAndMessage(err)
			logger.Printf("action=blocked destination=%s reason=%s", hostPort, msg)
			return req, goproxy.NewResponse(req, goproxy.ContentTypeText, status, msg)
		}

		hdrName := strings.TrimSpace(tool.Credentials.HeaderName)
		hdrValue := strings.TrimSpace(tool.Credentials.HeaderValue)
		if hdrName != "" && hdrValue != "" {
			req.Header.Set(hdrName, hdrValue)
		}

		logger.Printf("action=managed tool=%s destination=%s", tool.Name, hostPort)
		return req, nil
	})

	return &http.Server{
		Addr:    addr,
		Handler: p,
	}, nil
}

func requestDestination(req *http.Request) (string, string) {
	target := req.URL.Host
	if target == "" {
		target = req.Host
	}

	scheme := req.URL.Scheme
	if scheme == "" {
		scheme = "http"
	}

	return normalizeDest(target, scheme)
}

func normalizeDest(target, scheme string) (string, string) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", ""
	}

	if !strings.Contains(target, ":") {
		if strings.EqualFold(scheme, "https") {
			target = net.JoinHostPort(target, "443")
		} else {
			target = net.JoinHostPort(target, "80")
		}
	}

	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return strings.ToLower(target), strings.ToLower(target)
	}

	host = strings.Trim(host, "[]")
	host = strings.ToLower(host)
	return host, net.JoinHostPort(host, port)
}

func matchManagedHTTPTool(tools []config.ManagedTool, host, hostPort string) (*config.ManagedTool, bool) {
	for i := range tools {
		tool := &tools[i]
		if strings.EqualFold(tool.Protocol, "postgres") {
			continue
		}
		if hostMatches(tool.Match, host, hostPort) {
			return tool, true
		}
	}
	return nil, false
}

func isBlocked(host, hostPort string, blocked []string) bool {
	for _, pattern := range blocked {
		if hostMatches(pattern, host, hostPort) {
			return true
		}
	}
	return false
}

func hostMatches(pattern, host, hostPort string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	if pattern == "" {
		return false
	}

	if strings.Contains(pattern, ":") {
		ok, _ := doublestar.Match(pattern, hostPort)
		return ok
	}

	ok, _ := doublestar.Match(pattern, host)
	if ok {
		return true
	}

	// Convenience: allow plain domain match for subdomains.
	return host == pattern || strings.HasSuffix(host, "."+pattern)
}

func statusAndMessage(err error) (int, string) {
	var pErr *PolicyError
	if errors.As(err, &pErr) {
		return pErr.StatusCode, pErr.Message
	}
	return http.StatusForbidden, fmt.Sprintf("policy check failed: %v", err)
}
