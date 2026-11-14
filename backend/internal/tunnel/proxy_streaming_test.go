package tunnel

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/models"
)

func TestShareScopedRouter_StreamingAndIdleTimeouts(t *testing.T) {
	t.Run("Streams SSE chunks with immediate flushes", func(t *testing.T) {
		streamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			flusher, ok := w.(http.Flusher)
			require.True(t, ok)

			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)
			flusher.Flush()

			for i := 1; i <= 3; i++ {
				_, _ = fmt.Fprintf(w, "data: event-%d\n\n", i)
				flusher.Flush()
				time.Sleep(10 * time.Millisecond)
			}
		}))
		defer streamServer.Close()

		projectID := uuid.New()
		serviceID := uuid.New()
		share := &models.PublicShare{
			ID:               uuid.New(),
			ProjectID:        projectID,
			PrimaryServiceID: serviceID,
			Status:           models.ShareStatusActive,
		}

		serviceLookup := &mockServiceLookup{
			services: map[uuid.UUID]*models.Service{
				serviceID: {
					ID:        serviceID,
					ProjectID: projectID,
					Name:      "sse-service",
					Status:    models.ServiceStatusRunning,
				},
			},
		}

		upstreamResolver := &mockUpstreamResolver{
			targets: map[uuid.UUID]string{
				serviceID: streamServer.URL,
			},
		}

		router := NewShareScopedRouterWithOptions(share, serviceLookup, upstreamResolver, RouterOptions{
			ResponseHeaderTimeout: 5 * time.Second,
			StreamIdleTimeout:     1 * time.Second,
		})

		req := httptest.NewRequest(http.MethodGet, "/events", nil)
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		resp := rec.Result()
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Contains(t, string(body), "data: event-1")
		assert.Contains(t, string(body), "data: event-2")
		assert.Contains(t, string(body), "data: event-3")
	})

	t.Run("Terminates upstream request when client context is cancelled", func(t *testing.T) {
		var upstreamCancelled atomic.Bool
		started := make(chan struct{})

		streamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			flusher, ok := w.(http.Flusher)
			require.True(t, ok)

			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher.Flush()
			close(started)

			select {
			case <-r.Context().Done():
				upstreamCancelled.Store(true)
			case <-time.After(3 * time.Second):
				// Stalled without cancellation
			}
		}))
		defer streamServer.Close()

		projectID := uuid.New()
		serviceID := uuid.New()
		share := &models.PublicShare{
			ID:               uuid.New(),
			ProjectID:        projectID,
			PrimaryServiceID: serviceID,
			Status:           models.ShareStatusActive,
		}

		serviceLookup := &mockServiceLookup{
			services: map[uuid.UUID]*models.Service{
				serviceID: {
					ID:        serviceID,
					ProjectID: projectID,
					Name:      "sse-service",
					Status:    models.ServiceStatusRunning,
				},
			},
		}

		upstreamResolver := &mockUpstreamResolver{
			targets: map[uuid.UUID]string{
				serviceID: streamServer.URL,
			},
		}

		router := NewShareScopedRouter(share, serviceLookup, upstreamResolver)

		clientCtx, cancelClient := context.WithCancel(context.Background())
		req := httptest.NewRequest(http.MethodGet, "/events", nil).WithContext(clientCtx)
		rec := httptest.NewRecorder()

		done := make(chan struct{})
		go func() {
			router.ServeHTTP(rec, req)
			close(done)
		}()

		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for upstream server to start")
		}

		// Cancel client request context
		cancelClient()

		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for router to return after client cancellation")
		}

		// Verify upstream context was cancelled
		assert.Eventually(t, func() bool {
			return upstreamCancelled.Load()
		}, 1*time.Second, 50*time.Millisecond)
	})

	t.Run("Terminates hung stream when idle timeout is exceeded", func(t *testing.T) {
		streamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			flusher, ok := w.(http.Flusher)
			require.True(t, ok)

			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("initial chunk\n"))
			flusher.Flush()

			// Hang without sending more data
			time.Sleep(1500 * time.Millisecond)
		}))
		defer streamServer.Close()

		projectID := uuid.New()
		serviceID := uuid.New()
		share := &models.PublicShare{
			ID:               uuid.New(),
			ProjectID:        projectID,
			PrimaryServiceID: serviceID,
			Status:           models.ShareStatusActive,
		}

		serviceLookup := &mockServiceLookup{
			services: map[uuid.UUID]*models.Service{
				serviceID: {
					ID:        serviceID,
					ProjectID: projectID,
					Name:      "hang-service",
					Status:    models.ServiceStatusRunning,
				},
			},
		}

		upstreamResolver := &mockUpstreamResolver{
			targets: map[uuid.UUID]string{
				serviceID: streamServer.URL,
			},
		}

		// Configure stream idle timeout
		router := NewShareScopedRouterWithOptions(share, serviceLookup, upstreamResolver, RouterOptions{
			ResponseHeaderTimeout: 5 * time.Second,
			StreamIdleTimeout:     250 * time.Millisecond,
		})

		req := httptest.NewRequest(http.MethodGet, "/data", nil)
		rec := httptest.NewRecorder()

		start := time.Now()
		router.ServeHTTP(rec, req)
		elapsed := time.Since(start)

		// Must complete well before the server's 1.5-second sleep finishes
		assert.Less(t, elapsed, 1200*time.Millisecond)
		body := rec.Body.String()
		assert.Contains(t, body, "initial chunk")
	})
}

func TestLocalRouter_StreamingAndIdleTimeouts(t *testing.T) {
	t.Run("Streams response across net.Conn stream without 60s limitation", func(t *testing.T) {
		streamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("stream-data-ok"))
		}))
		defer streamServer.Close()

		projectID := uuid.New()
		serviceID := uuid.New()
		share := &models.PublicShare{
			ID:               uuid.New(),
			ProjectID:        projectID,
			ShareToken:       "share-stream",
			PublicHost:       "share-stream.local",
			PrimaryServiceID: serviceID,
			Status:           models.ShareStatusActive,
		}

		shareLookup := &mockShareLookup{
			shares: map[string]*models.PublicShare{"share-stream": share},
		}
		serviceLookup := &mockServiceLookup{
			services: map[uuid.UUID]*models.Service{
				serviceID: {
					ID:        serviceID,
					ProjectID: projectID,
					Name:      "stream-service",
					Status:    models.ServiceStatusRunning,
				},
			},
		}
		upstreamResolver := &mockUpstreamResolver{
			targets: map[uuid.UUID]string{
				serviceID: streamServer.URL,
			},
		}

		router := NewLocalRouter(shareLookup, serviceLookup, upstreamResolver)

		clientConn, serverConn := net.Pipe()
		go router.HandleStream(serverConn)

		req := "GET / HTTP/1.1\r\nHost: share-stream.local\r\n\r\n"
		_, err := clientConn.Write([]byte(req))
		require.NoError(t, err)

		resp, err := http.ReadResponse(bufio.NewReader(clientConn), nil)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Equal(t, "stream-data-ok", strings.TrimSpace(string(body)))
	})
}
