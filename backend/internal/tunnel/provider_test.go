package tunnel

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/models"
)

func TestCloudflareProvider_Prerequisites(t *testing.T) {
	t.Run("Fails when binary does not exist in PATH", func(t *testing.T) {
		p := NewCloudflareProvider(true, "nonexistent-cloudflared-binary-xyz123")
		ready, guide, err := p.CheckPrerequisites()
		assert.False(t, ready)
		assert.Error(t, err)
		assert.Contains(t, guide, "cloudflared is required")
		assert.Contains(t, guide, "Installation Instructions")
	})
}

func TestCloudflareProvider_MockTunnelLifecycle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping shell script test on Windows")
	}

	// Create a temporary mock cloudflared script
	tmpDir := t.TempDir()
	mockCloudflaredPath := filepath.Join(tmpDir, "cloudflared")
	script := `#!/bin/sh
echo "2026-10-21T08:00:00Z INF Thank you for using Cloudflare Tunnel."
echo "2026-10-21T08:00:01Z INF Your quick Tunnel has been created! Visit it at:"
echo "2026-10-21T08:00:01Z INF https://test-quick-tunnel-1234.trycloudflare.com"
echo "2026-10-21T08:00:02Z INF Ready to serve connections"
# Keep running until killed
while true; do
  sleep 1
done
`
	err := os.WriteFile(mockCloudflaredPath, []byte(script), 0755)
	require.NoError(t, err)

	provider := NewCloudflareProvider(true, mockCloudflaredPath)
	defer provider.Close()

	// Target mock backend
	backendServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("mock backend response"))
	}))
	defer backendServer.Close()

	share := &models.PublicShare{
		ID:         uuid.New(),
		ProjectID:  uuid.New(),
		ShareToken: "share-mock-test-123",
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("handler ok"))
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	endpoint, err := provider.StartShare(ctx, share, handler)
	require.NoError(t, err)
	require.NotNil(t, endpoint)

	assert.Equal(t, "https://test-quick-tunnel-1234.trycloudflare.com", endpoint.PublicURL)
	assert.Equal(t, "test-quick-tunnel-1234.trycloudflare.com", endpoint.PublicHost)
	assert.True(t, endpoint.LocalPort > 0)
	assert.Equal(t, models.TunnelStateOnline, endpoint.State)

	// Check status
	state, reason, err := provider.GetShareStatus(context.Background(), share.ShareToken)
	require.NoError(t, err)
	assert.Equal(t, models.TunnelStateOnline, state)
	assert.Empty(t, reason)

	// Verify local HTTP port is actually listening and responsive
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", endpoint.LocalPort))
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, "handler ok", string(body))

	// Stop share
	err = provider.StopShare(context.Background(), share.ShareToken)
	require.NoError(t, err)

	// After stop, local port must no longer accept connections
	time.Sleep(100 * time.Millisecond)
	_, err = http.Get(fmt.Sprintf("http://127.0.0.1:%d/", endpoint.LocalPort))
	assert.Error(t, err)
}

func TestCloudflareProvider_ConfigConflictDetection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping shell script test on Windows")
	}

	tmpDir := t.TempDir()
	mockCloudflaredPath := filepath.Join(tmpDir, "cloudflared-conflict")
	script := `#!/bin/sh
echo "Cannot use --url with a configuration file /home/user/.cloudflared/config.yml"
exit 1
`
	err := os.WriteFile(mockCloudflaredPath, []byte(script), 0755)
	require.NoError(t, err)

	provider := NewCloudflareProvider(true, mockCloudflaredPath)
	defer provider.Close()

	share := &models.PublicShare{
		ID:         uuid.New(),
		ShareToken: "share-conflict-test",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = provider.StartShare(ctx, share, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pre-existing cloudflared configuration file")
}

func TestGatewayProvider_PrerequisitesAndRouting(t *testing.T) {
	t.Run("Rejects placeholder hostnames", func(t *testing.T) {
		p := NewGatewayProvider(nil, true, "https://example.com", "gateway.example.com:8443", "test-secret-123456")
		ready, _, err := p.CheckPrerequisites()
		assert.False(t, ready)
		assert.ErrorIs(t, err, ErrInvalidGatewayURL)
	})

	t.Run("Rejects unconfigured gateway", func(t *testing.T) {
		p := NewGatewayProvider(nil, true, "https://preview.domain.com", "", "")
		ready, _, err := p.CheckPrerequisites()
		assert.False(t, ready)
		assert.Error(t, err)
	})

	t.Run("Accepts valid configuration", func(t *testing.T) {
		p := NewGatewayProvider(nil, true, "https://preview.mydomain.io", "tunnel.mydomain.io:8443", "strong-secret-key-12345")
		ready, _, err := p.CheckPrerequisites()
		assert.True(t, ready)
		assert.NoError(t, err)
	})
}

func TestShareScopedRouter_Execution(t *testing.T) {
	projectID := uuid.New()
	frontendID := uuid.New()
	backendID := uuid.New()

	frontendHit := false
	frontendServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		frontendHit = true
		assert.Equal(t, "https", r.Header.Get("X-Forwarded-Proto"))
		assert.Equal(t, "app-123.preview.mydomain.io", r.Header.Get("X-Forwarded-Host"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Frontend Home"))
	}))
	defer frontendServer.Close()

	backendHit := false
	backendServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendHit = true
		assert.Equal(t, "/api/users", r.URL.Path)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[{"id": 1, "name": "Alice"}]`))
	}))
	defer backendServer.Close()

	share := &models.PublicShare{
		ID:               uuid.New(),
		ProjectID:        projectID,
		ShareToken:       "share-scoped-test",
		PublicURL:        "https://app-123.preview.mydomain.io",
		PublicHost:       "app-123.preview.mydomain.io",
		PrimaryServiceID: frontendID,
		RouteMappings: []models.RouteMapping{
			{PathPrefix: "/api", ServiceID: backendID},
			{PathPrefix: "/", ServiceID: frontendID},
		},
		Status: models.ShareStatusActive,
	}

	serviceLookup := &mockServiceLookup{
		services: map[uuid.UUID]*models.Service{
			frontendID: {
				ID:        frontendID,
				ProjectID: projectID,
				Name:      "web",
				Role:      models.RoleFrontend,
				Status:    models.ServiceStatusRunning,
			},
			backendID: {
				ID:        backendID,
				ProjectID: projectID,
				Name:      "api",
				Role:      models.RoleBackend,
				Status:    models.ServiceStatusRunning,
			},
		},
	}

	upstreamResolver := &mockUpstreamResolver{
		targets: map[uuid.UUID]string{
			frontendID: frontendServer.URL,
			backendID:  backendServer.URL,
		},
	}

	router := NewShareScopedRouter(share, serviceLookup, upstreamResolver)

	t.Run("Routes root to frontend", func(t *testing.T) {
		frontendHit = false
		req := httptest.NewRequest(http.MethodGet, "https://app-123.preview.mydomain.io/", nil)
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), "Frontend Home")
		assert.True(t, frontendHit)
	})

	t.Run("Routes /api to backend", func(t *testing.T) {
		backendHit = false
		req := httptest.NewRequest(http.MethodGet, "https://app-123.preview.mydomain.io/api/users", nil)
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), "Alice")
		assert.True(t, backendHit)
	})

	t.Run("Blocks path traversal attacks", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "https://app-123.preview.mydomain.io/api/../../etc/passwd", nil)
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "Path traversal is strictly prohibited")
	})

	t.Run("Blocks encoded path traversal", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "https://app-123.preview.mydomain.io/api/%2e%2e/secret", nil)
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
}
