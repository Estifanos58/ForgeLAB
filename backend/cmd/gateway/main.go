package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/forgelab/backend/internal/gateway"
)

var (
	Version   = "1.0.0"
	CommitSHA = "dev"
	BuildTime = "unknown"
)

func main() {
	versionFlag := flag.Bool("version", false, "Print version and exit")
	httpAddr := flag.String("http", getEnv("GATEWAY_HTTP_ADDR", getEnv("GATEWAY_INGRESS_PORT", getEnv("GATEWAY_HTTP_PORT", ":8082"))), "Ingress HTTP listen address")
	httpsAddr := flag.String("https", getEnv("GATEWAY_HTTPS_ADDR", getEnv("GATEWAY_HTTPS_PORT", "")), "Ingress HTTPS listen address (optional, requires TLS cert & key)")
	tunnelAddr := flag.String("tunnel", getEnv("GATEWAY_TUNNEL_ADDR", getEnv("GATEWAY_TUNNEL_PORT", ":8443")), "Tunnel listener address")
	baseDomain := flag.String("domain", getEnv("GATEWAY_BASE_DOMAIN", "preview.forgelab.local"), "Base share domain (e.g. preview.example.com)")
	authSecret := flag.String("secret", getEnv("GATEWAY_AUTH_SECRET", getEnv("GATEWAY_AUTH_TOKEN", "")), "Tunnel authentication secret")
	tlsCert := flag.String("tls-cert", getEnv("GATEWAY_TLS_CERT", ""), "TLS certificate file path")
	tlsKey := flag.String("tls-key", getEnv("GATEWAY_TLS_KEY", ""), "TLS private key file path")
	flag.Parse()

	// Normalize port-only values (e.g. "8082" -> ":8082")
	*httpAddr = normalizeAddr(*httpAddr)
	if *httpsAddr != "" {
		*httpsAddr = normalizeAddr(*httpsAddr)
	}
	*tunnelAddr = normalizeAddr(*tunnelAddr)

	if *versionFlag {
		fmt.Printf("forgelab-gateway v%s (commit: %s, built: %s)\n", Version, CommitSHA, BuildTime)
		return
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if strings.TrimSpace(*authSecret) == "" {
		slog.Error("gateway configuration error: GATEWAY_AUTH_SECRET (or GATEWAY_AUTH_TOKEN) is required and must not be empty")
		os.Exit(1)
	}

	fmt.Printf("====================================================\n")
	fmt.Printf("   ForgeLAB Public Gateway v%s (commit: %s, built: %s)\n", Version, CommitSHA, BuildTime)
	fmt.Printf("   HTTP Ingress:     %s\n", *httpAddr)
	if *httpsAddr != "" {
		fmt.Printf("   HTTPS Ingress:    %s\n", *httpsAddr)
	}
	fmt.Printf("   Tunnel Listener:  %s\n", *tunnelAddr)
	fmt.Printf("   Base Domain:      *.%s\n", *baseDomain)
	fmt.Printf("====================================================\n")

	server := gateway.NewServer(gateway.Config{
		HTTPAddr:    *httpAddr,
		HTTPSAddr:   *httpsAddr,
		TunnelAddr:  *tunnelAddr,
		BaseDomain:  *baseDomain,
		AuthSecret:  *authSecret,
		TLSCertPath: *tlsCert,
		TLSKeyPath:  *tlsKey,
		TLSEnabled:  *tlsCert != "" && *tlsKey != "",
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := server.Start(ctx); err != nil {
		slog.Error("gateway server error", "error", err)
		os.Exit(1)
	}

	slog.Info("gateway server shutdown gracefully")
}

func normalizeAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr != "" && !strings.Contains(addr, ":") {
		return ":" + addr
	}
	return addr
}

func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}
