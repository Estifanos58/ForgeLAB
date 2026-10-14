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

var (
	Version   = "1.0.0"
	CommitSHA = "dev"
	BuildTime = "unknown"
)

func main() {
	versionFlag := flag.Bool("version", false, "Print version and exit")
	hostFlag := flag.String("host", "127.0.0.1", "Local agent listening host address (defaults to secure loopback 127.0.0.1; specify 0.0.0.0 or network IP for explicit Docker/network access)")
	port := flag.Int("port", 4142, "Local agent listening port")
	backendURL := flag.String("backend", "http://localhost:8080", "ForgeLAB backend URL")
	agentIDFlag := flag.String("agent-id", "", "Unique agent identifier")
	allowedRootsFlag := flag.String("allowed-roots", "", "Comma-separated list of allowed source directory roots")
	flag.Parse()

	if *versionFlag {
		fmt.Printf("forgelab-agent v%s (commit: %s, built: %s)\n", Version, CommitSHA, BuildTime)
		return
	}

	listenHost := strings.TrimSpace(*hostFlag)
	if envHost := os.Getenv("FORGELAB_AGENT_HOST_BIND"); envHost != "" {
		listenHost = strings.TrimSpace(envHost)
	}
	if listenHost == "" {
		listenHost = "127.0.0.1"
	}

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

	agentID := strings.TrimSpace(*agentIDFlag)
	if agentID == "" {
		agentID = os.Getenv("FORGELAB_AGENT_ID")
	}

	server := agent.NewAgentServer(agent.AgentServerConfig{
		AgentID:      agentID,
		Version:      Version,
		CommitSHA:    CommitSHA,
		Port:         *port,
		AllowedRoots: allowedRoots,
		BackendURL:   *backendURL,
	})

	addr := fmt.Sprintf("%s:%d", listenHost, *port)
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
		fmt.Printf("   ForgeLAB Local Agent v%s (commit: %s, built: %s)\n", Version, CommitSHA, BuildTime)
		fmt.Printf("   Agent ID: %s\n", server.AgentID())
		fmt.Printf("   Ready on http://%s\n", addr)
		fmt.Printf("   Connected backend: %s\n", *backendURL)
		fmt.Printf("====================================================\n")
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errStr := strings.ToLower(err.Error())
			if strings.Contains(errStr, "bind") || strings.Contains(errStr, "only one usage of each socket address") || strings.Contains(errStr, "address already in use") {
				fmt.Fprintf(os.Stderr, "\n[ForgeLAB] Startup Error: Port %d is already in use by another process.\n  Action: Terminate the process using port %d or specify a different port via -port <number>.\n\n", *port, *port)
			} else {
				slog.Error("agent server failed", "error", err)
			}
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
