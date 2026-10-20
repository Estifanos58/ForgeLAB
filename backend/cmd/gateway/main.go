package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
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
	httpAddr := flag.String("http", getEnv("GATEWAY_HTTP_ADDR", ":8082"), "Ingress HTTP listen address")
	tunnelAddr := flag.String("tunnel", getEnv("GATEWAY_TUNNEL_ADDR", ":8443"), "Tunnel listener address")
	baseDomain := flag.String("domain", getEnv("GATEWAY_BASE_DOMAIN", "preview.forgelab.local"), "Base share domain (e.g. preview.example.com)")
	authSecret := flag.String("secret", getEnv("GATEWAY_AUTH_SECRET", ""), "Tunnel authentication secret")
	tlsCert := flag.String("tls-cert", getEnv("GATEWAY_TLS_CERT", ""), "TLS certificate file path")
	tlsKey := flag.String("tls-key", getEnv("GATEWAY_TLS_KEY", ""), "TLS private key file path")
	flag.Parse()

	if *versionFlag {
		fmt.Printf("forgelab-gateway v%s (commit: %s, built: %s)\n", Version, CommitSHA, BuildTime)
		return
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	fmt.Printf("====================================================\n")
	fmt.Printf("   ForgeLAB Public Gateway v%s (commit: %s, built: %s)\n", Version, CommitSHA, BuildTime)
	fmt.Printf("   HTTP Ingress:     %s\n", *httpAddr)
	fmt.Printf("   Tunnel Listener:  %s\n", *tunnelAddr)
	fmt.Printf("   Base Domain:      *.%s\n", *baseDomain)
	fmt.Printf("====================================================\n")

	server := gateway.NewServer(gateway.Config{
		HTTPAddr:    *httpAddr,
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

func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}
