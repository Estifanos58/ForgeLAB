package gateway

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/yamux"

	"github.com/forgelab/backend/internal/tunnel"
)

var validShareTokenRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{3,62}$`)

// Config holds configuration for the Public Gateway server.
type Config struct {
	HTTPAddr       string // Ingress HTTP listen address (e.g. ":80" or ":8082")
	HTTPSAddr      string // Ingress HTTPS listen address (e.g. ":443", optional)
	TunnelAddr     string // Tunnel listener address (e.g. ":8443")
	BaseDomain     string // Base public share domain (e.g. "preview.example.com")
	AuthSecret     string // Required secret for authenticating ForgeLAB tunnel connections
	TLSCertPath    string // Optional TLS certificate file for HTTPS / TLS tunnel
	TLSKeyPath     string // Optional TLS private key file
	TLSEnabled     bool   // Whether TLS is terminated by gateway (false if behind reverse proxy/ALB)
	MaxConnections int
}

// Server represents the internet-facing public gateway.
type Server struct {
	config Config

	mu           sync.RWMutex
	routes       map[string]*yamux.Session          // shareToken -> active yamux session
	sessionShares map[*yamux.Session]map[string]bool // session -> registered shareTokens
	closed       bool
}

// NewServer creates a new Gateway Server.
func NewServer(cfg Config) *Server {
	if cfg.BaseDomain == "" {
		cfg.BaseDomain = "preview.forgelab.local"
	}
	cfg.BaseDomain = strings.ToLower(strings.TrimPrefix(cfg.BaseDomain, "."))

	return &Server{
		config:        cfg,
		routes:        make(map[string]*yamux.Session),
		sessionShares: make(map[*yamux.Session]map[string]bool),
	}
}

// Start begins listening for both tunnel connections and public visitor HTTP traffic.
func (s *Server) Start(ctx context.Context) error {
	// 1. Start Tunnel Listener
	tunnelListener, err := s.createTunnelListener()
	if err != nil {
		return fmt.Errorf("failed to start tunnel listener on %s: %w", s.config.TunnelAddr, err)
	}
	defer tunnelListener.Close()

	slog.Info("gateway tunnel listener ready", "addr", s.config.TunnelAddr, "base_domain", s.config.BaseDomain)
	go s.serveTunnelConnections(ctx, tunnelListener)

	// 2. Start HTTP Ingress Listener
	httpServer := &http.Server{
		Addr:              s.config.HTTPAddr,
		Handler:           s.HTTPHandler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	slog.Info("gateway public http ingress ready", "addr", s.config.HTTPAddr)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("gateway http server error: %w", err)
	}

	return nil
}

func (s *Server) createTunnelListener() (net.Listener, error) {
	if s.config.TLSEnabled && s.config.TLSCertPath != "" && s.config.TLSKeyPath != "" {
		cert, err := tls.LoadX509KeyPair(s.config.TLSCertPath, s.config.TLSKeyPath)
		if err != nil {
			return nil, fmt.Errorf("failed to load TLS certificate for tunnel listener: %w", err)
		}
		tlsCfg := &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}
		return tls.Listen("tcp", s.config.TunnelAddr, tlsCfg)
	}
	return net.Listen("tcp", s.config.TunnelAddr)
}

// serveTunnelConnections accepts incoming TCP/TLS tunnel connections from ForgeLAB installations.
func (s *Server) serveTunnelConnections(ctx context.Context, l net.Listener) {
	for {
		conn, err := l.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				slog.Debug("tunnel listener accept error", "error", err)
				time.Sleep(100 * time.Millisecond)
				continue
			}
		}

		go s.handleTunnelConnection(ctx, conn)
	}
}

// handleTunnelConnection authenticates a client connection and manages its session.
func (s *Server) handleTunnelConnection(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	// 1. Read Handshake Request
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	reader := json.NewDecoder(conn)
	var req tunnel.HandshakeRequest
	if err := reader.Decode(&req); err != nil {
		slog.Warn("gateway handshake decode error", "error", err)
		return
	}
	_ = conn.SetReadDeadline(time.Time{})

	// 2. Authenticate token
	if s.config.AuthSecret != "" && req.AuthToken != s.config.AuthSecret {
		slog.Warn("gateway authentication failed: invalid auth token", "remote", conn.RemoteAddr().String())
		resp := tunnel.HandshakeResponse{
			Status: tunnel.StatusError,
			Error:  "invalid or unauthorized auth_token",
		}
		data, _ := json.Marshal(resp)
		data = append(data, '\n')
		_, _ = conn.Write(data)
		return
	}

	// 3. Send successful handshake response
	resp := tunnel.HandshakeResponse{
		Status:         tunnel.StatusSuccess,
		BaseDomain:     s.config.BaseDomain,
		GatewayVersion: "1.0.0",
	}
	data, _ := json.Marshal(resp)
	data = append(data, '\n')
	if _, err := conn.Write(data); err != nil {
		return
	}

	// 4. Initialize Yamux Client session
	yamuxCfg := yamux.DefaultConfig()
	yamuxCfg.EnableKeepAlive = true
	yamuxCfg.KeepAliveInterval = 15 * time.Second
	yamuxCfg.ConnectionWriteTimeout = 10 * time.Second

	session, err := yamux.Client(conn, yamuxCfg)
	if err != nil {
		slog.Warn("failed to initialize yamux client on gateway", "error", err)
		return
	}
	defer func() {
		session.Close()
		s.unregisterSession(session)
	}()

	// 5. Accept control stream from client
	controlStream, err := session.AcceptStream()
	if err != nil {
		slog.Warn("failed to accept control stream from client", "error", err)
		return
	}
	defer controlStream.Close()

	slog.Info("tunnel client connected and authenticated", "remote", conn.RemoteAddr().String())

	// 6. Process control messages (registration / deregistration of shares)
	ctrlDec := json.NewDecoder(controlStream)
	for {
		var msg tunnel.ControlMessage
		if err := ctrlDec.Decode(&msg); err != nil {
			if !errors.Is(err, io.EOF) {
				slog.Debug("control stream decode error", "error", err)
			}
			return
		}

		switch msg.Action {
		case tunnel.ControlActionRegister:
			if msg.ShareToken != "" && validShareTokenRegex.MatchString(msg.ShareToken) {
				s.registerShare(msg.ShareToken, session)
				slog.Info("registered public share route", "share_token", msg.ShareToken)
				ack, _ := json.Marshal(tunnel.ControlResponse{Status: tunnel.StatusSuccess, ShareToken: msg.ShareToken})
				ack = append(ack, '\n')
				_, _ = controlStream.Write(ack)
			}
		case tunnel.ControlActionDeregister:
			if msg.ShareToken != "" {
				s.deregisterShare(msg.ShareToken)
				slog.Info("deregistered public share route", "share_token", msg.ShareToken)
				ack, _ := json.Marshal(tunnel.ControlResponse{Status: tunnel.StatusSuccess, ShareToken: msg.ShareToken})
				ack = append(ack, '\n')
				_, _ = controlStream.Write(ack)
			}
		}
	}
}

func (s *Server) registerShare(token string, session *yamux.Session) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.routes[token] = session
	if s.sessionShares[session] == nil {
		s.sessionShares[session] = make(map[string]bool)
	}
	s.sessionShares[session][token] = true
}

func (s *Server) deregisterShare(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if sess, ok := s.routes[token]; ok {
		delete(s.routes, token)
		if s.sessionShares[sess] != nil {
			delete(s.sessionShares[sess], token)
		}
	}
}

func (s *Server) unregisterSession(session *yamux.Session) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if tokens, ok := s.sessionShares[session]; ok {
		for token := range tokens {
			delete(s.routes, token)
		}
		delete(s.sessionShares, session)
	}
}

func (s *Server) getSession(token string) *yamux.Session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess := s.routes[token]
	if sess != nil && sess.IsClosed() {
		return nil
	}
	return sess
}

// HTTPHandler returns the http.Handler for visitor requests.
func (s *Server) HTTPHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. Extract and validate share host
		rawHost := r.Host
		if colon := strings.Index(rawHost, ":"); colon != -1 {
			rawHost = rawHost[:colon]
		}
		rawHost = strings.ToLower(strings.TrimSpace(rawHost))

		// Check domain suffix
		expectedSuffix := "." + s.config.BaseDomain
		if !strings.HasSuffix(rawHost, expectedSuffix) {
			s.renderError(w, http.StatusNotFound, "Host Not Found",
				fmt.Sprintf("The requested host '%s' is not managed by this ForgeLAB gateway.", rawHost))
			return
		}

		shareToken := strings.TrimSuffix(rawHost, expectedSuffix)
		if !validShareTokenRegex.MatchString(shareToken) {
			s.renderError(w, http.StatusBadRequest, "Invalid Hostname", "Malformed share identifier.")
			return
		}

		// 2. Find active tunnel session
		session := s.getSession(shareToken)
		if session == nil {
			s.renderError(w, http.StatusServiceUnavailable, "Share Offline",
				"The application host for this share is currently offline, disconnected, or the link has expired.")
			return
		}

		// 3. Open Yamux stream to ForgeLAB
		stream, err := session.OpenStream()
		if err != nil {
			s.renderError(w, http.StatusBadGateway, "Gateway Error", "Failed to establish tunnel stream to application host.")
			return
		}
		defer stream.Close()

		// Set forwarding identification headers
		r.Header.Set(tunnel.HeaderForgeLabShareToken, shareToken)
		clientIP := r.RemoteAddr
		if host, _, err := net.SplitHostPort(clientIP); err == nil {
			clientIP = host
		}
		r.Header.Set(tunnel.HeaderForgeLabClientIP, clientIP)

		// 4. Handle WebSocket Upgrade
		isWebSocket := strings.EqualFold(r.Header.Get("Upgrade"), "websocket") ||
			strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade")

		if isWebSocket {
			s.handleWebSocketRelay(w, r, stream)
			return
		}

		// 5. Standard HTTP Relay
		s.handleHTTPRelay(w, r, stream)
	})
}

// handleHTTPRelay forwards ordinary HTTP requests and streams responses.
func (s *Server) handleHTTPRelay(w http.ResponseWriter, r *http.Request, stream net.Conn) {
	// Write request into tunnel stream
	if err := r.Write(stream); err != nil {
		s.renderError(w, http.StatusBadGateway, "Gateway Error", "Failed to forward request through tunnel.")
		return
	}

	// Read response from stream
	reader := bufio.NewReader(stream)
	resp, err := http.ReadResponse(reader, r)
	if err != nil {
		s.renderError(w, http.StatusBadGateway, "Gateway Error", "Failed to read response from application host.")
		return
	}
	defer resp.Body.Close()

	// Copy headers
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}

	w.WriteHeader(resp.StatusCode)

	// Stream body back to visitor with flusher support
	flusher, hasFlusher := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			_, writeErr := w.Write(buf[:n])
			if writeErr != nil {
				return
			}
			if hasFlusher {
				flusher.Flush()
			}
		}
		if readErr != nil {
			return
		}
	}
}

// handleWebSocketRelay handles bidirectional WebSocket protocol switching over the tunnel stream.
func (s *Server) handleWebSocketRelay(w http.ResponseWriter, r *http.Request, stream net.Conn) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		s.renderError(w, http.StatusInternalServerError, "Upgrade Not Supported", "Webserver does not support connection hijacking.")
		return
	}

	// Forward handshake request into stream
	if err := r.Write(stream); err != nil {
		s.renderError(w, http.StatusBadGateway, "Gateway Error", "Failed to forward WebSocket handshake.")
		return
	}

	// Hijack client connection
	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		s.renderError(w, http.StatusInternalServerError, "Hijack Failed", "Could not hijack visitor connection.")
		return
	}
	defer clientConn.Close()

	// Bidirectional stream pipe
	var wg sync.WaitGroup
	wg.Add(2)

	pipe := func(dst io.Writer, src io.Reader, cancelClose io.Closer) {
		defer wg.Done()
		_, _ = io.Copy(dst, src)
		_ = cancelClose.Close()
	}

	go pipe(stream, clientConn, stream)
	go pipe(clientConn, stream, clientConn)

	wg.Wait()
}

func (s *Server) renderError(w http.ResponseWriter, statusCode int, title, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(statusCode)
	html := fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
  <meta charset="utf-8">
  <title>%d %s — ForgeLAB Public Gateway</title>
  <style>
    body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; background: #0a0a0a; color: #ededed; display: flex; align-items: center; justify-content: center; height: 100vh; margin: 0; }
    .card { background: #171717; border: 1px solid #262626; border-radius: 8px; padding: 32px; max-width: 480px; width: 90%%; box-shadow: 0 4px 20px rgba(0,0,0,0.5); }
    h1 { font-size: 20px; font-weight: 600; margin: 0 0 12px; color: #ffffff; }
    p { font-size: 14px; line-height: 1.5; color: #a1a1aa; margin: 0 0 20px; }
    .badge { display: inline-block; font-size: 11px; font-family: monospace; background: #27272a; color: #f43f5e; padding: 2px 8px; border-radius: 4px; margin-bottom: 12px; }
    .footer { font-size: 12px; color: #52525b; border-top: 1px solid #262626; pt: 16px; margin-top: 16px; }
  </style>
</head>
<body>
  <div class="card">
    <div class="badge">HTTP %d</div>
    <h1>%s</h1>
    <p>%s</p>
    <div class="footer">ForgeLAB Public Gateway</div>
  </div>
</body>
</html>`, statusCode, title, statusCode, title, message)
	_, _ = w.Write([]byte(html))
}
