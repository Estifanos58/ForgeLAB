package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/forgelab/backend/internal/agent"
)

func main() {
	port := flag.Int("port", 4142, "Local agent listening port")
	backendURL := flag.String("backend", "http://localhost:8080", "ForgeLAB backend URL")
	allowedRootsFlag := flag.String("allowed-roots", "", "Comma-separated list of allowed source directory roots")
	flag.Parse()

	var allowedRoots []string
	if *allowedRootsFlag != "" {
		for _, r := range strings.Split(*allowedRootsFlag, ",") {
			if trimmed := strings.TrimSpace(r); trimmed != "" {
				allowedRoots = append(allowedRoots, trimmed)
			}
		}
	} else if envRoots := os.Getenv("FORGELAB_AGENT_ALLOWED_ROOTS"); envRoots != "" {
		for _, r := range strings.Split(envRoots, ",") {
			if trimmed := strings.TrimSpace(r); trimmed != "" {
				allowedRoots = append(allowedRoots, trimmed)
			}
		}
	}

	server := agent.NewAgentServer(agent.AgentServerConfig{
		Port:         *port,
		AllowedRoots: allowedRoots,
		BackendURL:   *backendURL,
	})

	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	httpServer := &http.Server{
		Addr:         addr,
		Handler:      server.Router(),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 5 * time.Minute, // Allow long tar stream transfers
	}

	// Graceful shutdown context
	stopCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		fmt.Printf("====================================================\n")
		fmt.Printf("   ForgeLAB Local Agent v1.0.0\n")
		fmt.Printf("   Ready on http://%s\n", addr)
		fmt.Printf("   Connected backend: %s\n", *backendURL)
		fmt.Printf("====================================================\n")
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("agent server failed", "error", err)
			os.Exit(1)
		}
	}()

	<-stopCtx.Done()
	fmt.Printf("\nShutting down ForgeLAB Local Agent...\n")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)
	fmt.Printf("ForgeLAB Local Agent stopped cleanly.\n")
}
