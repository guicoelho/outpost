package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"outbound-proxy/certs"
	"outbound-proxy/config"
	"outbound-proxy/manifest"
	"outbound-proxy/proxy"
)

func main() {
	configPath := flag.String("config", os.Getenv("CONFIG_PATH"), "Path to proxy config YAML")
	httpAddr := flag.String("http-addr", ":3128", "HTTP proxy listen address")
	flag.Parse()

	logger := log.New(os.Stdout, "", log.LstdFlags|log.LUTC)

	cfg, err := config.Load(strings.TrimSpace(*configPath))
	if err != nil {
		logger.Fatalf("load config: %v", err)
	}

	caCert, caKey, err := certs.EnsureCA()
	if err != nil {
		logger.Fatalf("ensure CA: %v", err)
	}

	if data, err := manifest.Generate(cfg); err != nil {
		logger.Printf("action=warning component=manifest err=%v", err)
	} else if err := os.WriteFile("/ca/tools.json", data, 0644); err != nil {
		logger.Printf("action=warning component=manifest err=%v", err)
	} else {
		logger.Printf("action=ready component=manifest path=/ca/tools.json")
	}

	httpServer, err := proxy.NewHTTPServer(cfg, caCert, caKey, *httpAddr)
	if err != nil {
		logger.Fatalf("create http proxy: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Printf("action=start component=http-proxy listen=%s", *httpAddr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("http proxy failed: %w", err)
		}
	}()

	var listeners []net.Listener
	nextPort := 5432
	for _, tool := range cfg.ManagedTools {
		if !strings.EqualFold(tool.Protocol, "postgres") {
			continue
		}
		localPort := tool.LocalPort
		if localPort == 0 {
			localPort = nextPort
			nextPort++
		}

		listenAddr := fmt.Sprintf(":%d", localPort)
		ln, err := proxy.StartPostgresProxy(ctx, tool, listenAddr, logger)
		if err != nil {
			logger.Fatalf("start postgres proxy for %s: %v", tool.Name, err)
		}
		listeners = append(listeners, ln)
		logger.Printf("action=start component=postgres-proxy tool=%s listen=%s target=%s", tool.Name, listenAddr, tool.Match)
	}

	logger.Printf("action=ready user=%s managed_tools=%d blocked_rules=%d", cfg.User, len(cfg.ManagedTools), len(cfg.Blocked))

	select {
	case <-ctx.Done():
		logger.Printf("action=shutdown signal=received")
	case err := <-errCh:
		logger.Printf("action=shutdown reason=%v", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for _, ln := range listeners {
		_ = ln.Close()
	}

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Printf("action=error component=http-proxy phase=shutdown err=%v", err)
	}
}
