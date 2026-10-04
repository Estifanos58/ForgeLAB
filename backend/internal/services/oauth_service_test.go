package services_test

import (
	"context"
	"sync"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/config"
	"github.com/forgelab/backend/internal/services"
)

func setupTestRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	t.Cleanup(func() {
		client.Close()
		mr.Close()
	})
	return mr, client
}

func TestOAuthService_StateAndNonceValidation(t *testing.T) {
	ctx := context.Background()

	t.Run("InMemory_SuccessAndReplay", func(t *testing.T) {
		svc := services.NewOAuthService(config.OAuthConfig{}, config.OAuthConfig{}, nil)

		// 1. Invalid nonce must consume/fail
		state1, _, err := svc.GenerateState(ctx, "google")
		require.NoError(t, err)
		err = svc.ValidateState(ctx, "google", state1, "wrong-nonce")
		assert.ErrorIs(t, err, services.ErrInvalidOAuthState)

		// 2. Correct nonce succeeds on fresh state
		state2, nonce2, err := svc.GenerateState(ctx, "google")
		require.NoError(t, err)
		err = svc.ValidateState(ctx, "google", state2, nonce2)
		assert.NoError(t, err)

		// 3. Replay with same state fails
		err = svc.ValidateState(ctx, "google", state2, nonce2)
		assert.ErrorIs(t, err, services.ErrInvalidOAuthState)
	})

	t.Run("Redis_SuccessAndReplay", func(t *testing.T) {
		_, client := setupTestRedis(t)
		svc := services.NewOAuthService(config.OAuthConfig{}, config.OAuthConfig{}, client)

		// 1. Wrong provider fails
		state1, nonce1, err := svc.GenerateState(ctx, "github")
		require.NoError(t, err)
		err = svc.ValidateState(ctx, "google", state1, nonce1)
		assert.ErrorIs(t, err, services.ErrInvalidOAuthState)

		// 2. Correct provider and nonce succeeds on fresh state
		state2, nonce2, err := svc.GenerateState(ctx, "github")
		require.NoError(t, err)
		err = svc.ValidateState(ctx, "github", state2, nonce2)
		assert.NoError(t, err)

		// 3. Replay with same state immediately fails
		err = svc.ValidateState(ctx, "github", state2, nonce2)
		assert.ErrorIs(t, err, services.ErrInvalidOAuthState)
	})

	t.Run("Redis_ConcurrentDoubleConsumptionRace", func(t *testing.T) {
		_, client := setupTestRedis(t)
		svc := services.NewOAuthService(config.OAuthConfig{}, config.OAuthConfig{}, client)

		state, nonce, err := svc.GenerateState(ctx, "google")
		require.NoError(t, err)

		concurrency := 10
		var wg sync.WaitGroup
		successCount := 0
		var mu sync.Mutex

		for i := 0; i < concurrency; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				err := svc.ValidateState(ctx, "google", state, nonce)
				if err == nil {
					mu.Lock()
					successCount++
					mu.Unlock()
				}
			}()
		}

		wg.Wait()

		// Exactly one worker must succeed; all other concurrent attempts must be rejected!
		assert.Equal(t, 1, successCount, "expected exactly 1 successful consumption among concurrent requests")
	})
}
