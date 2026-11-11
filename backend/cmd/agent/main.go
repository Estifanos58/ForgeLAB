package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
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
	stopCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(runWithContext(stopCtx, os.Args[1:], os.Stdout, os.Stderr))
}

func runWithContext(stopCtx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("forgelab-agent", flag.ContinueOnError)
	fs.SetOutput(stderr)

	versionFlag := fs.Bool("version", false, "Print version and exit")
	hostFlag := fs.String("host", "127.0.0.1", "Local agent listening host address (defaults to secure loopback 127.0.0.1; specify 0.0.0.0 or network IP for explicit Docker/network access)")
	port := fs.Int("port", 4142, "Local agent listening port (use 0 for ephemeral OS-assigned port)")
	backendURL := fs.String("backend", "http://localhost:8080", "ForgeLAB backend URL")
	agentIDFlag := fs.String("agent-id", "", "Unique agent identifier")
	allowedRootsFlag := fs.String("allowed-roots", "", "Comma-separated list of allowed source directory roots")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *versionFlag {
		fmt.Fprintf(stdout, "forgelab-agent v%s (commit: %s, built: %s)\n", Version, CommitSHA, BuildTime)
		return 0
	}

	if *port < 0 || *port > 65535 {
		fmt.Fprintf(stderr, "\n[ForgeLAB] Startup Error: Invalid port %d (must be between 0 and 65535).\n\n", *port)
		return 1
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

	sessionInfo := agent.InspectInteractiveSession()
	if !sessionInfo.Interactive {
		slog.Warn("local agent running in non-interactive session; native folder picker unavailable",
			"session_id", sessionInfo.SessionID,
			"window_station", sessionInfo.WindowStation,
			"reason", sessionInfo.Reason,
		)
		fmt.Fprintf(stderr, "\n[ForgeLAB] WARNING: Non-interactive desktop session detected (Session %d, WindowStation %q).\n  %s\n  Native OS folder picker dialogs will be unavailable unless launched from the interactive logged-in user session (e.g. .\\start-agent.ps1).\n\n",
			sessionInfo.SessionID, sessionInfo.WindowStation, sessionInfo.Reason)
	}

	// Bind HTTP listener synchronously before announcing readiness
	addr := net.JoinHostPort(listenHost, strconv.Itoa(*port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		errStr := strings.ToLower(err.Error())
		if strings.Contains(errStr, "only one usage of each socket address") || strings.Contains(errStr, "address already in use") || strings.Contains(errStr, "bind") {
			fmt.Fprintf(stderr, "\n[ForgeLAB] Startup Error: Unable to bind %s (port %d is already in use or restricted).\n  Details: %v\n  Action: Terminate the process using port %d or specify a different port via -port <number>.\n\n", addr, *port, err, *port)
		} else {
			fmt.Fprintf(stderr, "\n[ForgeLAB] Startup Error: Failed to listen on %s: %v\n\n", addr, err)
		}
		return 1
	}

	boundAddr := ln.Addr().String()

	server := agent.NewAgentServer(agent.AgentServerConfig{
		AgentID:      agentID,
		Version:      Version,
		CommitSHA:    CommitSHA,
		Host:         listenHost,
		Port:         *port,
		AllowedRoots: allowedRoots,
		BackendURL:   *backendURL,
	})
	defer server.Close()
	server.SetBoundAddr(boundAddr)

	httpServer := &http.Server{
		Addr:         boundAddr,
		Handler:      server.Router(),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 5 * time.Minute, // Allow long tar stream transfers
	}

	serveErrCh := make(chan error, 1)
	go func() {
		serveErrCh <- httpServer.Serve(ln)
	}()

	// Announce readiness strictly after net.Listen has bound the socket
	fmt.Fprintf(stdout, "====================================================\n")
	fmt.Fprintf(stdout, "   ForgeLAB Local Agent v%s (commit: %s, built: %s)\n", Version, CommitSHA, BuildTime)
	fmt.Fprintf(stdout, "   Agent ID: %s\n", server.AgentID())
	if sessionInfo.WindowStation != "" {
		fmt.Fprintf(stdout, "   Desktop Session: interactive=%t (session %d, winsta %s)\n", sessionInfo.Interactive, sessionInfo.SessionID, sessionInfo.WindowStation)
	} else {
		fmt.Fprintf(stdout, "   Desktop Session: interactive=%t (session %d)\n", sessionInfo.Interactive, sessionInfo.SessionID)
	}
	fmt.Fprintf(stdout, "   Ready on http://%s\n", boundAddr)
	fmt.Fprintf(stdout, "   Connected backend: %s\n", *backendURL)
	fmt.Fprintf(stdout, "====================================================\n")

	select {
	case <-stopCtx.Done():
		fmt.Fprintf(stdout, "\nShutting down ForgeLAB Local Agent...\n")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
		if err := <-serveErrCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("agent server error during shutdown", "error", err)
			return 1
		}
		fmt.Fprintf(stdout, "ForgeLAB Local Agent stopped cleanly.\n")
		return 0
	case err := <-serveErrCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("agent server failed", "error", err)
			fmt.Fprintf(stderr, "\n[ForgeLAB] Runtime Error: Agent HTTP server stopped unexpectedly: %v\n\n", err)
			return 1
		}
		return 0
	}
}
