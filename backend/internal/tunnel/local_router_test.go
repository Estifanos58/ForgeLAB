package tunnel

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/models"
)

type mockShareLookup struct {
	shares map[string]*models.PublicShare
}

func (m *mockShareLookup) GetShareByToken(ctx context.Context, token string) (*models.PublicShare, error) {
	if s, ok := m.shares[token]; ok {
		return s, nil
	}
	return nil, fmt.Errorf("not found")
}

type mockServiceLookup struct {
	services map[uuid.UUID]*models.Service
}

func (m *mockServiceLookup) GetService(ctx context.Context, id uuid.UUID) (*models.Service, error) {
	if s, ok := m.services[id]; ok {
		return s, nil
	}
	return nil, fmt.Errorf("not found")
}

type mockUpstreamResolver struct {
	targets map[uuid.UUID]string
}

func (m *mockUpstreamResolver) ResolveUpstream(ctx context.Context, s *models.Service) (string, error) {
	if t, ok := m.targets[s.ID]; ok {
		return t, nil
	}
	return "", fmt.Errorf("no upstream")
}

func TestLocalRouter_RoutingAndSecurity(t *testing.T) {
	// 1. Setup mock upstream HTTP servers
	frontendHit := false
	frontendServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		frontendHit = true
		assert.Equal(t, "https", r.Header.Get("X-Forwarded-Proto"))
		assert.Equal(t, "share-abc.preview.local", r.Header.Get("X-Forwarded-Host"))
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<h1>Frontend Application</h1>"))
	}))
	defer frontendServer.Close()

	backendHit := false
	backendServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendHit = true
		assert.Equal(t, "/api/data", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"items": [1, 2, 3]}`))
	}))
	defer backendServer.Close()

	projectID := uuid.New()
	frontendID := uuid.New()
	backendID := uuid.New()
	stoppedID := uuid.New()
	otherProjectSvcID := uuid.New()

	share := &models.PublicShare{
		ID:               uuid.New(),
		ProjectID:        projectID,
		OwnerID:          uuid.New(),
		ShareToken:       "share-abc",
		PublicHost:       "share-abc.preview.local",
		PublicURL:        "https://share-abc.preview.local",
		PrimaryServiceID: frontendID,
		RouteMappings: []models.RouteMapping{
			{PathPrefix: "/api", ServiceID: backendID},
			{PathPrefix: "/", ServiceID: frontendID},
		},
		Status:    models.ShareStatusActive,
		CreatedAt: time.Now(),
	}

	shareLookup := &mockShareLookup{
		shares: map[string]*models.PublicShare{"share-abc": share},
	}
	serviceLookup := &mockServiceLookup{
		services: map[uuid.UUID]*models.Service{
			frontendID: {
				ID:        frontendID,
				ProjectID: projectID,
				Name:      "web-frontend",
				Role:      models.RoleFrontend,
				Status:    models.ServiceStatusRunning,
			},
			backendID: {
				ID:        backendID,
				ProjectID: projectID,
				Name:      "api-backend",
				Role:      models.RoleBackend,
				Status:    models.ServiceStatusRunning,
			},
			stoppedID: {
				ID:        stoppedID,
				ProjectID: projectID,
				Name:      "stopped-worker",
				Role:      models.RoleWorker,
				Status:    models.ServiceStatusStopped,
			},
			otherProjectSvcID: {
				ID:        otherProjectSvcID,
				ProjectID: uuid.New(), // Different project!
				Name:      "evil-service",
				Status:    models.ServiceStatusRunning,
			},
		},
	}
	upstreamResolver := &mockUpstreamResolver{
		targets: map[uuid.UUID]string{
			frontendID:        frontendServer.URL,
			backendID:         backendServer.URL,
			otherProjectSvcID: "http://127.0.0.1:9999",
		},
	}

	router := NewLocalRouter(shareLookup, serviceLookup, upstreamResolver)

	t.Run("Routes to frontend root", func(t *testing.T) {
		frontendHit = false
		clientConn, serverConn := net.Pipe()

		go router.HandleStream(serverConn)

		req := "GET / HTTP/1.1\r\nHost: share-abc.preview.local\r\n\r\n"
		_, err := clientConn.Write([]byte(req))
		require.NoError(t, err)

		resp, err := http.ReadResponse(bufio.NewReader(clientConn), nil)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		body, _ := io.ReadAll(resp.Body)
		assert.Contains(t, string(body), "Frontend Application")
		assert.True(t, frontendHit)
	})

	t.Run("Routes to backend on /api prefix", func(t *testing.T) {
		backendHit = false
		clientConn, serverConn := net.Pipe()

		go router.HandleStream(serverConn)

		req := "GET /api/data HTTP/1.1\r\nHost: share-abc.preview.local\r\n\r\n"
		_, err := clientConn.Write([]byte(req))
		require.NoError(t, err)

		resp, err := http.ReadResponse(bufio.NewReader(clientConn), nil)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		body, _ := io.ReadAll(resp.Body)
		assert.Contains(t, string(body), `{"items": [1, 2, 3]}`)
		assert.True(t, backendHit)
	})

	t.Run("Rejects path traversal", func(t *testing.T) {
		clientConn, serverConn := net.Pipe()

		go router.HandleStream(serverConn)

		req := "GET /api/../../../etc/passwd HTTP/1.1\r\nHost: share-abc.preview.local\r\n\r\n"
		_, err := clientConn.Write([]byte(req))
		require.NoError(t, err)

		resp, err := http.ReadResponse(bufio.NewReader(clientConn), nil)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("Rejects unknown or inactive share", func(t *testing.T) {
		clientConn, serverConn := net.Pipe()

		go router.HandleStream(serverConn)

		req := "GET / HTTP/1.1\r\nHost: share-unknown.preview.local\r\n\r\n"
		_, err := clientConn.Write([]byte(req))
		require.NoError(t, err)

		resp, err := http.ReadResponse(bufio.NewReader(clientConn), nil)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("Returns 503 for stopped service", func(t *testing.T) {
		share.RouteMappings = append([]models.RouteMapping{{PathPrefix: "/stopped", ServiceID: stoppedID}}, share.RouteMappings...)
		defer func() {
			share.RouteMappings = share.RouteMappings[1:]
		}()

		clientConn, serverConn := net.Pipe()

		go router.HandleStream(serverConn)

		req := "GET /stopped HTTP/1.1\r\nHost: share-abc.preview.local\r\n\r\n"
		_, err := clientConn.Write([]byte(req))
		require.NoError(t, err)

		resp, err := http.ReadResponse(bufio.NewReader(clientConn), nil)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
		body, _ := io.ReadAll(resp.Body)
		assert.Contains(t, string(body), "stopped")
	})

	t.Run("Enforces cross-project boundary", func(t *testing.T) {
		share.RouteMappings = append([]models.RouteMapping{{PathPrefix: "/evil", ServiceID: otherProjectSvcID}}, share.RouteMappings...)
		defer func() {
			share.RouteMappings = share.RouteMappings[1:]
		}()

		clientConn, serverConn := net.Pipe()

		go router.HandleStream(serverConn)

		req := "GET /evil HTTP/1.1\r\nHost: share-abc.preview.local\r\n\r\n"
		_, err := clientConn.Write([]byte(req))
		require.NoError(t, err)

		resp, err := http.ReadResponse(bufio.NewReader(clientConn), nil)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	})

	t.Run("Blocks internal management plane ports", func(t *testing.T) {
		assert.True(t, router.isForbiddenUpstream("http://127.0.0.1:8080/api/projects"))
		assert.True(t, router.isForbiddenUpstream("http://localhost:5432"))
		assert.True(t, router.isForbiddenUpstream("http://127.0.0.1:6379"))
		assert.True(t, router.isForbiddenUpstream("http://127.0.0.1:4142/api/agent/status"))
		assert.True(t, router.isForbiddenUpstream("http://forgelab-backend:8080/api/projects"))
		assert.False(t, router.isForbiddenUpstream("http://127.0.0.1:10001"))
		assert.False(t, router.isForbiddenUpstream("http://172.19.0.5:8080"))
		assert.False(t, router.isForbiddenUpstream("http://10.0.1.25:8080"))
	})
}
