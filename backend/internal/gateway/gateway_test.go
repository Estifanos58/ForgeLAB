package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/tunnel"
)

func TestGateway_AuthenticationAndRouting(t *testing.T) {
	// 1. Create Gateway Server
	gw := NewServer(Config{
		BaseDomain: "preview.test",
		AuthSecret: "super-secret-token",
	})

	t.Run("Rejects invalid auth secret on tunnel handshake", func(t *testing.T) {
		serverConn, clientConn := net.Pipe()
		defer serverConn.Close()
		defer clientConn.Close()

		go gw.handleTunnelConnection(context.Background(), serverConn)

		// Send handshake with wrong secret
		handshake := tunnel.HandshakeRequest{
			ProtocolVersion: "1.0.0",
			AuthToken:       "wrong-token",
		}
		data, _ := json.Marshal(handshake)
		data = append(data, '\n')
		_, err := clientConn.Write(data)
		require.NoError(t, err)

		dec := json.NewDecoder(clientConn)
		var resp tunnel.HandshakeResponse
		err = dec.Decode(&resp)
		require.NoError(t, err)

		assert.Equal(t, tunnel.StatusError, resp.Status)
	})

	t.Run("Authenticates valid tunnel connection and routes visitor requests", func(t *testing.T) {
		serverConn, clientConn := net.Pipe()
		defer serverConn.Close()
		defer clientConn.Close()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		go gw.handleTunnelConnection(ctx, serverConn)

		// Send handshake with valid secret
		handshake := tunnel.HandshakeRequest{
			ProtocolVersion: "1.0.0",
			AuthToken:       "super-secret-token",
		}
		data, _ := json.Marshal(handshake)
		data = append(data, '\n')
		_, err := clientConn.Write(data)
		require.NoError(t, err)

		dec := json.NewDecoder(clientConn)
		var resp tunnel.HandshakeResponse
		err = dec.Decode(&resp)
		require.NoError(t, err)
		assert.Equal(t, tunnel.StatusSuccess, resp.Status)

		// Initialize client-side Yamux Server
		session, err := yamux.Server(clientConn, yamux.DefaultConfig())
		require.NoError(t, err)
		defer session.Close()

		// Open control stream from client to gateway
		controlStream, err := session.OpenStream()
		require.NoError(t, err)
		defer controlStream.Close()

		// Register share "share-1234"
		regMsg := tunnel.ControlMessage{
			Action:     tunnel.ControlActionRegister,
			ShareToken: "share-1234",
			Timestamp:  time.Now().Unix(),
		}
		regData, _ := json.Marshal(regMsg)
		regData = append(regData, '\n')
		_, err = controlStream.Write(regData)
		require.NoError(t, err)

		// Wait briefly for gateway to process registration
		time.Sleep(50 * time.Millisecond)

		// Accept stream on client side and handle simulated visitor request
		go func() {
			stream, acceptErr := session.AcceptStream()
			if acceptErr != nil {
				return
			}
			defer stream.Close()

			reader := bufio.NewReader(stream)
			req, readErr := http.ReadRequest(reader)
			if readErr != nil {
				return
			}

			assert.Equal(t, "share-1234", req.Header.Get(tunnel.HeaderForgeLabShareToken))
			assert.Equal(t, "/test-path", req.URL.Path)

			resp := "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: 13\r\n\r\nHello Gateway"
			_, _ = stream.Write([]byte(resp))
		}()

		// Send visitor HTTP request to gateway HTTPHandler
		handler := gw.HTTPHandler()

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://share-1234.preview.test/test-path", nil)
		req.Host = "share-1234.preview.test"

		handler.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "Hello Gateway", rec.Body.String())
	})

	t.Run("Returns 503 for unregistered or offline share", func(t *testing.T) {
		handler := gw.HTTPHandler()

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://share-offline.preview.test/test-path", nil)
		req.Host = "share-offline.preview.test"

		handler.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
		assert.Contains(t, rec.Body.String(), "Share Offline")
	})

	t.Run("Rejects invalid hostnames not matching base domain", func(t *testing.T) {
		handler := gw.HTTPHandler()

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://evil.com/test-path", nil)
		req.Host = "evil.com"

		handler.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("Rejects malformed share token in subdomain", func(t *testing.T) {
		handler := gw.HTTPHandler()

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://bad!token$.preview.test/test-path", nil)
		req.Host = "bad!token$.preview.test"

		handler.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
}

func TestGateway_ProtectionControls(t *testing.T) {
	t.Run("Enforces per-share rate limits", func(t *testing.T) {
		gw := NewServer(Config{
			BaseDomain:        "preview.test",
			AuthSecret:        "secret",
			RateLimitPerShare: 3,
			RateLimitWindow:   time.Minute,
		})
		handler := gw.HTTPHandler()

		for i := 1; i <= 3; i++ {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "http://share-test.preview.test/path", nil)
			req.Host = "share-test.preview.test"
			req.RemoteAddr = "192.168.1.100:1234"
			handler.ServeHTTP(rec, req)
			// Offline share returns 503, but not 429 rate limit
			assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
		}

		// 4th request must be rate limited with HTTP 429
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://share-test.preview.test/path", nil)
		req.Host = "share-test.preview.test"
		req.RemoteAddr = "192.168.1.100:1234"
		handler.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusTooManyRequests, rec.Code)
		assert.Contains(t, rec.Body.String(), "Rate Limit Exceeded")
		assert.Equal(t, "5", rec.Header().Get("Retry-After"))
	})

	t.Run("Enforces per-IP rate limits across shares", func(t *testing.T) {
		gw := NewServer(Config{
			BaseDomain:        "preview.test",
			AuthSecret:        "secret",
			RateLimitPerShare: 100,
			RateLimitPerIP:    2,
			RateLimitWindow:   time.Minute,
		})
		handler := gw.HTTPHandler()

		// 2 requests from same client IP
		for i := 1; i <= 2; i++ {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "http://share-ip.preview.test/path", nil)
			req.Host = "share-ip.preview.test"
			req.RemoteAddr = "10.0.0.50:54321"
			handler.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
		}

		// 3rd request from same client IP exceeds IP limit
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://share-different.preview.test/path", nil)
		req.Host = "share-different.preview.test"
		req.RemoteAddr = "10.0.0.50:54321"
		handler.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusTooManyRequests, rec.Code)
		assert.Contains(t, rec.Body.String(), "Rate Limit Exceeded")
	})

	t.Run("Enforces per-share connection limits", func(t *testing.T) {
		gw := NewServer(Config{
			BaseDomain:             "preview.test",
			AuthSecret:             "secret",
			MaxConnectionsPerShare: 2,
			RateLimitPerShare:      100,
		})

		// Acquire 2 slots
		ok1, _ := gw.acquireConnection("share-conn")
		assert.True(t, ok1)
		ok2, _ := gw.acquireConnection("share-conn")
		assert.True(t, ok2)
		assert.Equal(t, 2, gw.GetActiveShareConnections("share-conn"))

		// 3rd connection exceeds limit
		handler := gw.HTTPHandler()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://share-conn.preview.test/path", nil)
		req.Host = "share-conn.preview.test"
		handler.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusTooManyRequests, rec.Code)
		assert.Contains(t, rec.Body.String(), "Connection Limit Reached")

		// Release 1 slot
		gw.releaseConnection("share-conn")
		assert.Equal(t, 1, gw.GetActiveShareConnections("share-conn"))

		// Now connection can be acquired
		ok3, _ := gw.acquireConnection("share-conn")
		assert.True(t, ok3)
		gw.releaseConnection("share-conn")
		gw.releaseConnection("share-conn")
		assert.Equal(t, 0, gw.GetActiveShareConnections("share-conn"))
	})
}
