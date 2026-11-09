package services_test

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/services"
	"github.com/forgelab/backend/internal/tunnel"
)

type mockShareProvider struct {
	mu           sync.Mutex
	started      map[string]*tunnel.ShareEndpoint
	stopped      map[string]bool
	statusStates map[string]string
}

func newMockShareProvider() *mockShareProvider {
	return &mockShareProvider{
		started:      make(map[string]*tunnel.ShareEndpoint),
		stopped:      make(map[string]bool),
		statusStates: make(map[string]string),
	}
}

func (m *mockShareProvider) Name() string {
	return models.ProviderCloudflare
}

func (m *mockShareProvider) IsConfigured() bool {
	return true
}

func (m *mockShareProvider) CheckPrerequisites() (bool, string, error) {
	return true, "", nil
}

func (m *mockShareProvider) StartShare(ctx context.Context, share *models.PublicShare, localHandler http.Handler) (*tunnel.ShareEndpoint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ep := &tunnel.ShareEndpoint{
		PublicURL:  "https://" + share.ShareToken + ".trycloudflare.com",
		PublicHost: share.ShareToken + ".trycloudflare.com",
		LocalPort:  12345,
		State:      models.TunnelStateOnline,
	}
	m.started[share.ShareToken] = ep
	m.statusStates[share.ShareToken] = models.TunnelStateOnline
	return ep, nil
}

func (m *mockShareProvider) StopShare(ctx context.Context, shareToken string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopped[shareToken] = true
	m.statusStates[shareToken] = models.TunnelStateDisconnected
	return nil
}

func (m *mockShareProvider) GetShareStatus(ctx context.Context, shareToken string) (string, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, exists := m.statusStates[shareToken]
	if !exists {
		return models.TunnelStateNotShared, "", nil
	}
	return state, "", nil
}

func (m *mockShareProvider) Close() error {
	return nil
}

func TestShareLifecycle_ReplacementAndCleanup(t *testing.T) {
	pool, _ := connectTestDB(t)
	if pool == nil {
		t.Skip("skipping test: database not available")
	}

	ctx := context.Background()
	projectSvc := services.NewProjectService(pool, nil, nil, nil)
	serviceSvc := services.NewServiceService(pool)
	shareSvc := services.NewShareService(pool, projectSvc, serviceSvc, true, "https://preview.local", "127.0.0.1:8443")
	mockProv := newMockShareProvider()
	shareSvc.SetProvider(mockProv)

	// Create test user and project with running service
	userID := uuid.New()
	_, err := pool.Exec(ctx, `
		INSERT INTO users (id, email, password_hash, display_name, role)
		VALUES ($1, $2, 'hash', 'Share Tester', 'user')
		ON CONFLICT DO NOTHING
	`, userID, "share-tester-"+userID.String()[:8]+"@example.com")
	require.NoError(t, err)

	projectID := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO projects (id, owner_id, name, slug, status, created_at, updated_at)
		VALUES ($1, $2, 'share-proj', $3, 'running', NOW(), NOW())
	`, projectID, userID, "share-proj-"+projectID.String()[:8])
	require.NoError(t, err)

	serviceID := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO services (id, project_id, name, role, port, status, created_at, updated_at)
		VALUES ($1, $2, 'web', 'frontend', 3000, 'running', NOW(), NOW())
	`, serviceID, projectID)
	require.NoError(t, err)

	t.Run("CreateShare replaces existing share and stops previous tunnel", func(t *testing.T) {
		// 1. Create initial share
		share1, err := shareSvc.CreateShare(ctx, projectID, userID, models.CreateShareInput{
			PrimaryServiceID: serviceID,
		})
		require.NoError(t, err)
		require.NotNil(t, share1)
		assert.Equal(t, models.ShareStatusActive, share1.Status)

		// 2. Create replacement share for the same project
		share2, err := shareSvc.CreateShare(ctx, projectID, userID, models.CreateShareInput{
			PrimaryServiceID: serviceID,
		})
		require.NoError(t, err)
		require.NotNil(t, share2)
		assert.NotEqual(t, share1.ShareToken, share2.ShareToken)

		// Verify share1 provider tunnel was stopped
		mockProv.mu.Lock()
		wasStopped := mockProv.stopped[share1.ShareToken]
		mockProv.mu.Unlock()
		assert.True(t, wasStopped, "previous share tunnel must be stopped upon replacement")

		// Verify database state: only 1 active share exists for this project
		var activeCount int
		err = pool.QueryRow(ctx, "SELECT COUNT(*) FROM public_shares WHERE project_id = $1 AND status = 'active'", projectID).Scan(&activeCount)
		require.NoError(t, err)
		assert.Equal(t, 1, activeCount, "strictly one active share should exist per project")

		// Verify share1 status in DB is now revoked
		var share1Status string
		err = pool.QueryRow(ctx, "SELECT status FROM public_shares WHERE id = $1", share1.ID).Scan(&share1Status)
		require.NoError(t, err)
		assert.Equal(t, models.ShareStatusRevoked, share1Status)
	})

	t.Run("CleanupExpiredShares marks expired shares and stops provider tunnels", func(t *testing.T) {
		// Insert an expired active share directly into DB
		expiredToken := "share-expired-" + uuid.New().String()[:12]
		expiredHost := expiredToken + ".trycloudflare.com"
		expiredAt := time.Now().Add(-10 * time.Minute)

		proj2ID := uuid.New()
		_, err := pool.Exec(ctx, `
			INSERT INTO projects (id, owner_id, name, slug, status, created_at, updated_at)
			VALUES ($1, $2, 'expired-proj', $3, 'running', NOW(), NOW())
		`, proj2ID, userID, "expired-proj-"+proj2ID.String()[:8])
		require.NoError(t, err)

		svc2ID := uuid.New()
		_, err = pool.Exec(ctx, `
			INSERT INTO services (id, project_id, name, role, port, status, created_at, updated_at)
			VALUES ($1, $2, 'api', 'backend', 8080, 'running', NOW(), NOW())
		`, svc2ID, proj2ID)
		require.NoError(t, err)

		_, err = pool.Exec(ctx, `
			INSERT INTO public_shares (
				project_id, owner_id, share_token, public_url, public_host,
				primary_service_id, route_mappings, provider, status, expires_at
			) VALUES (
				$1, $2, $3, $4, $5, $6, '[]', 'cloudflare', 'active', $7
			)
		`, proj2ID, userID, expiredToken, "https://"+expiredHost, expiredHost, svc2ID, expiredAt)
		require.NoError(t, err)

		// Run CleanupExpiredShares
		cleaned, err := shareSvc.CleanupExpiredShares(ctx)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, cleaned, 1)

		// Verify provider StopShare was called for the expired token
		mockProv.mu.Lock()
		expiredStopped := mockProv.stopped[expiredToken]
		mockProv.mu.Unlock()
		assert.True(t, expiredStopped, "CleanupExpiredShares must stop active tunnel for expired shares")

		// Verify status in DB was updated to expired
		var status string
		err = pool.QueryRow(ctx, "SELECT status FROM public_shares WHERE share_token = $1", expiredToken).Scan(&status)
		require.NoError(t, err)
		assert.Equal(t, models.ShareStatusExpired, status)
	})
}
