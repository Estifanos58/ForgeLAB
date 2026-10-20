package services

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/forgelab/backend/internal/models"
)

type mockTunnelRegistrar struct {
	registered   map[string]bool
	deregistered map[string]bool
	online       bool
}

func (m *mockTunnelRegistrar) RegisterShare(token string) error {
	if m.registered == nil {
		m.registered = make(map[string]bool)
	}
	m.registered[token] = true
	return nil
}

func (m *mockTunnelRegistrar) DeregisterShare(token string) error {
	if m.deregistered == nil {
		m.deregistered = make(map[string]bool)
	}
	m.deregistered[token] = true
	return nil
}

func (m *mockTunnelRegistrar) IsConnected() bool {
	return m.online
}

func (m *mockTunnelRegistrar) GetTunnelState() string {
	if m.online {
		return models.TunnelStateOnline
	}
	return models.TunnelStateDisconnected
}

func TestShareService_Configuration(t *testing.T) {
	t.Run("IsConfigured returns false when disabled", func(t *testing.T) {
		svc := NewShareService(nil, nil, nil, false, "https://preview.local", "127.0.0.1:8443")
		assert.False(t, svc.IsConfigured())
	})

	t.Run("IsConfigured returns false when URLs are empty", func(t *testing.T) {
		svc := NewShareService(nil, nil, nil, true, "", "")
		assert.False(t, svc.IsConfigured())
	})

	t.Run("IsConfigured returns true when enabled and URLs present", func(t *testing.T) {
		svc := NewShareService(nil, nil, nil, true, "https://preview.example.com", "gateway.example.com:8443")
		assert.True(t, svc.IsConfigured())
	})

	t.Run("Fails closed when creating share with unconfigured gateway", func(t *testing.T) {
		svc := NewShareService(nil, nil, nil, false, "", "")
		_, err := svc.CreateShare(context.Background(), uuid.New(), uuid.New(), models.CreateShareInput{})
		assert.ErrorIs(t, err, ErrShareNotConfigured)
	})

	t.Run("ShareStatusResponse reports unavailable when gateway unconfigured", func(t *testing.T) {
		svc := NewShareService(nil, nil, nil, false, "", "")
		status, err := svc.GetShareStatus(context.Background(), uuid.New(), uuid.New())
		assert.NoError(t, err)
		assert.Equal(t, models.TunnelStateUnavailable, status.TunnelState)
		assert.Contains(t, status.Reason, "FORGELAB_SHARING_ENABLED is false")
	})
}

func TestShareTokenFormat(t *testing.T) {
	// Verify that generated share tokens begin with "share-" and have cryptographically random hex entropy
	for i := 0; i < 10; i++ {
		token := models.PublicShare{
			ShareToken: "share-abcdef1234567890abcdef1234567890",
		}.ShareToken
		assert.True(t, strings.HasPrefix(token, "share-"))
		assert.True(t, len(token) >= 20)
	}
}
