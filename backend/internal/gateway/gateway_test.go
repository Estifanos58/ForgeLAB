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
