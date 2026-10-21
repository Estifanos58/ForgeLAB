package tunnel

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/yamux"

	"github.com/forgelab/backend/internal/models"
)

// ActiveTokenProvider fetches active share tokens on initial connection or reconnect.
type ActiveTokenProvider interface {
	ListActiveShareTokens(ctx context.Context) ([]string, error)
}

// TunnelManagerConfig holds settings for the outbound tunnel client.
type TunnelManagerConfig struct {
	Enabled          bool
	GatewayTunnelURL string
	GatewayAuthToken string
	TLSSkipVerify    bool
	InstallationID   string
}

// TunnelManager maintains the outbound authenticated Yamux tunnel to the public gateway.
type TunnelManager struct {
	config        TunnelManagerConfig
	localRouter   *LocalRouter
	tokenProvider ActiveTokenProvider

	mu            sync.RWMutex
	state         string // not_shared, connecting, online, disconnected, unavailable
	session       *yamux.Session
	controlStream net.Conn
	activeTokens  map[string]bool
	cancel        context.CancelFunc
	stopped       bool
	lastError     error
	connectedAt   *time.Time

	ackMu       sync.Mutex
	pendingAcks map[string]chan ControlResponse
}

// NewTunnelManager creates a new TunnelManager.
func NewTunnelManager(
	cfg TunnelManagerConfig,
	localRouter *LocalRouter,
	tokenProvider ActiveTokenProvider,
) *TunnelManager {
	initialState := models.TunnelStateNotShared
	if !cfg.Enabled || strings.TrimSpace(cfg.GatewayTunnelURL) == "" {
		initialState = models.TunnelStateUnavailable
	}

	return &TunnelManager{
		config:        cfg,
		localRouter:   localRouter,
		tokenProvider: tokenProvider,
		state:         initialState,
		activeTokens:  make(map[string]bool),
		pendingAcks:   make(map[string]chan ControlResponse),
	}
}

// Start initiates the background outbound connection loop if sharing is enabled.
func (tm *TunnelManager) Start(ctx context.Context) {
	if !tm.config.Enabled || strings.TrimSpace(tm.config.GatewayTunnelURL) == "" {
		slog.Info("public tunnel manager disabled or gateway unconfigured")
		return
	}

	connCtx, cancel := context.WithCancel(ctx)
	tm.cancel = cancel

	go tm.runConnectionLoop(connCtx)
}

// Stop closes active sessions and terminates the reconnection loop.
func (tm *TunnelManager) Stop() {
	tm.mu.Lock()
	tm.stopped = true
	if tm.cancel != nil {
		tm.cancel()
	}
	if tm.session != nil {
		_ = tm.session.Close()
	}
	tm.state = models.TunnelStateUnavailable
	tm.mu.Unlock()
}

// RegisterShare registers a newly created share with the gateway and waits for acknowledgment.
func (tm *TunnelManager) RegisterShare(shareToken string) error {
	tm.mu.RLock()
	stream := tm.controlStream
	connected := tm.session != nil && !tm.session.IsClosed() && stream != nil
	tm.mu.RUnlock()

	if !connected {
		return errors.New("cannot register share: outbound tunnel to public gateway is not connected")
	}

	ackCh := make(chan ControlResponse, 1)
	tm.ackMu.Lock()
	if tm.pendingAcks == nil {
		tm.pendingAcks = make(map[string]chan ControlResponse)
	}
	tm.pendingAcks[shareToken] = ackCh
	tm.ackMu.Unlock()

	defer func() {
		tm.ackMu.Lock()
		delete(tm.pendingAcks, shareToken)
		tm.ackMu.Unlock()
	}()

	msg := ControlMessage{
		Action:     ControlActionRegister,
		ShareToken: shareToken,
		Timestamp:  time.Now().Unix(),
	}
	data, _ := json.Marshal(msg)
	data = append(data, '\n')

	tm.mu.Lock()
	if tm.controlStream == nil {
		tm.mu.Unlock()
		return errors.New("tunnel control stream disconnected during registration")
	}
	_, err := tm.controlStream.Write(data)
	tm.mu.Unlock()
	if err != nil {
		return fmt.Errorf("failed to send registration message to gateway: %w", err)
	}

	// Bounded timeout waiting for gateway acknowledgment
	select {
	case resp := <-ackCh:
		if resp.Status != StatusSuccess {
			return fmt.Errorf("gateway registration rejected: %s", resp.Error)
		}
		tm.mu.Lock()
		tm.activeTokens[shareToken] = true
		tm.mu.Unlock()
		return nil
	case <-time.After(5 * time.Second):
		return errors.New("timed out waiting for gateway registration acknowledgment")
	}
}

// DeregisterShare unregisters a revoked or expired share from the gateway.
func (tm *TunnelManager) DeregisterShare(shareToken string) error {
	tm.mu.Lock()
	delete(tm.activeTokens, shareToken)
	stream := tm.controlStream
	tm.mu.Unlock()

	if stream == nil {
		return nil
	}

	ackCh := make(chan ControlResponse, 1)
	tm.ackMu.Lock()
	if tm.pendingAcks == nil {
		tm.pendingAcks = make(map[string]chan ControlResponse)
	}
	tm.pendingAcks[shareToken] = ackCh
	tm.ackMu.Unlock()

	defer func() {
		tm.ackMu.Lock()
		delete(tm.pendingAcks, shareToken)
		tm.ackMu.Unlock()
	}()

	msg := ControlMessage{
		Action:     ControlActionDeregister,
		ShareToken: shareToken,
		Timestamp:  time.Now().Unix(),
	}
	data, _ := json.Marshal(msg)
	data = append(data, '\n')

	tm.mu.Lock()
	if tm.controlStream == nil {
		tm.mu.Unlock()
		return nil
	}
	_, err := tm.controlStream.Write(data)
	tm.mu.Unlock()
	if err != nil {
		return err
	}

	select {
	case <-ackCh:
		return nil
	case <-time.After(3 * time.Second):
		return nil // best-effort deregistration ack
	}
}

// IsConnected returns true if the tunnel has an active Yamux session.
func (tm *TunnelManager) IsConnected() bool {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	return tm.session != nil && !tm.session.IsClosed()
}

// GetTunnelState returns the current state of the tunnel.
func (tm *TunnelManager) GetTunnelState() string {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	return tm.state
}

// runConnectionLoop continually maintains the outbound connection with exponential backoff.
func (tm *TunnelManager) runConnectionLoop(ctx context.Context) {
	backoff := 1 * time.Second
	maxBackoff := 30 * time.Second

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		tm.mu.Lock()
		if tm.stopped {
			tm.mu.Unlock()
			return
		}
		tm.state = models.TunnelStateConnecting
		tm.mu.Unlock()

		err := tm.connectAndServe(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("public tunnel connection error", "gateway", tm.config.GatewayTunnelURL, "error", err)
			tm.mu.Lock()
			tm.state = models.TunnelStateDisconnected
			tm.lastError = err
			tm.mu.Unlock()
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

// connectAndServe dials the gateway, completes authentication, and accepts visitor streams.
func (tm *TunnelManager) connectAndServe(ctx context.Context) error {
	targetAddr := tm.config.GatewayTunnelURL
	targetAddr = strings.TrimPrefix(targetAddr, "tls://")
	targetAddr = strings.TrimPrefix(targetAddr, "tcp://")

	var rawConn net.Conn
	var err error

	dialer := &net.Dialer{Timeout: 10 * time.Second}

	if tm.config.TLSSkipVerify || strings.HasSuffix(targetAddr, ":8443") || strings.HasPrefix(tm.config.GatewayTunnelURL, "tls://") {
		// Use TLS
		host, _, splitErr := net.SplitHostPort(targetAddr)
		if splitErr != nil {
			host = targetAddr
		}
		tlsConfig := &tls.Config{
			ServerName:         host,
			InsecureSkipVerify: tm.config.TLSSkipVerify, // true only for development
			MinVersion:         tls.VersionTLS12,
		}
		rawConn, err = tls.DialWithDialer(dialer, "tcp", targetAddr, tlsConfig)
	} else {
		// Try TLS first, falling back to plaintext TCP if TLS handshake fails and running locally
		host, _, splitErr := net.SplitHostPort(targetAddr)
		if splitErr != nil {
			host = targetAddr
		}
		tlsConfig := &tls.Config{
			ServerName: host,
			MinVersion: tls.VersionTLS12,
		}
		rawConn, err = tls.DialWithDialer(dialer, "tcp", targetAddr, tlsConfig)
		if err != nil {
			// Plaintext TCP fallback
			rawConn, err = dialer.DialContext(ctx, "tcp", targetAddr)
		}
	}

	if err != nil {
		return fmt.Errorf("failed to dial gateway %s: %w", targetAddr, err)
	}
	defer rawConn.Close()

	// 1. Send Handshake Request
	handshakeReq := HandshakeRequest{
		ProtocolVersion: CurrentProtocolVersion,
		AuthToken:       tm.config.GatewayAuthToken,
		InstallationID:  tm.config.InstallationID,
	}
	reqData, _ := json.Marshal(handshakeReq)
	reqData = append(reqData, '\n')
	if _, err := rawConn.Write(reqData); err != nil {
		return fmt.Errorf("failed to send handshake to gateway: %w", err)
	}

	// 2. Read Handshake Response
	rawConn.SetReadDeadline(time.Now().Add(10 * time.Second))
	reader := json.NewDecoder(rawConn)
	var handshakeResp HandshakeResponse
	if err := reader.Decode(&handshakeResp); err != nil {
		return fmt.Errorf("failed to read handshake response from gateway: %w", err)
	}
	rawConn.SetReadDeadline(time.Time{})

	if handshakeResp.Status != StatusSuccess {
		return fmt.Errorf("gateway rejected authentication: %s", handshakeResp.Error)
	}

	slog.Info("authenticated with public gateway", "gateway_version", handshakeResp.GatewayVersion, "base_domain", handshakeResp.BaseDomain)

	// 3. Establish Yamux session (ForgeLAB acts as Yamux Server receiving visitor streams from Gateway client)
	yamuxCfg := yamux.DefaultConfig()
	yamuxCfg.EnableKeepAlive = true
	yamuxCfg.KeepAliveInterval = 15 * time.Second
	yamuxCfg.ConnectionWriteTimeout = 10 * time.Second

	session, err := yamux.Server(rawConn, yamuxCfg)
	if err != nil {
		return fmt.Errorf("failed to initialize multiplexed yamux session: %w", err)
	}
	defer session.Close()

	// 4. Open Control Stream
	controlStream, err := session.OpenStream()
	if err != nil {
		return fmt.Errorf("failed to open control stream on tunnel session: %w", err)
	}
	defer controlStream.Close()

	tm.mu.Lock()
	tm.session = session
	tm.controlStream = controlStream
	tm.state = models.TunnelStateOnline
	tm.lastError = nil
	now := time.Now()
	tm.connectedAt = &now

	// Sync all active tokens from database and local cache
	tokensToRegister := make([]string, 0, len(tm.activeTokens))
	for t := range tm.activeTokens {
		tokensToRegister = append(tokensToRegister, t)
	}
	tm.mu.Unlock()

	if tm.tokenProvider != nil {
		if dbTokens, err := tm.tokenProvider.ListActiveShareTokens(ctx); err == nil {
			for _, t := range dbTokens {
				tm.mu.Lock()
				tm.activeTokens[t] = true
				tm.mu.Unlock()
				tokensToRegister = append(tokensToRegister, t)
			}
		}
	}

	// Register tokens with gateway
	for _, token := range tokensToRegister {
		msg := ControlMessage{
			Action:     ControlActionRegister,
			ShareToken: token,
			Timestamp:  time.Now().Unix(),
		}
		data, _ := json.Marshal(msg)
		data = append(data, '\n')
		_, _ = controlStream.Write(data)
	}

	slog.Info("tunnel session online and registered active shares", "count", len(tokensToRegister))

	// Keep control stream reader alive for gateway control responses & heartbeats
	go func() {
		dec := json.NewDecoder(controlStream)
		for {
			var resp ControlResponse
			if err := dec.Decode(&resp); err != nil {
				// Signal any pending acks that the control stream closed
				tm.ackMu.Lock()
				for token, ch := range tm.pendingAcks {
					select {
					case ch <- ControlResponse{Status: StatusError, ShareToken: token, Error: "control stream disconnected"}:
					default:
					}
				}
				tm.ackMu.Unlock()
				return
			}

			// Route response to pending waiter
			tm.ackMu.Lock()
			if ch, ok := tm.pendingAcks[resp.ShareToken]; ok {
				select {
				case ch <- resp:
				default:
				}
			}
			tm.ackMu.Unlock()
		}
	}()

	// 5. Accept visitor streams and dispatch to local router
	for {
		stream, err := session.AcceptStream()
		if err != nil {
			if errors.Is(err, io.EOF) || session.IsClosed() {
				return nil
			}
			return fmt.Errorf("tunnel stream accept error: %w", err)
		}

		go tm.localRouter.HandleStream(stream)
	}
}
