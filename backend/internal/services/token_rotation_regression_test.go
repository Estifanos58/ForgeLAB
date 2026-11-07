package services_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/auth"
	"github.com/forgelab/backend/internal/services"
)

func TestUserService_AtomicRefreshTokenRotation(t *testing.T) {
	pool, _ := connectTestDB(t)
	if pool == nil {
		t.Skip("skipping test: database not available")
	}

	jwtManager := auth.NewJWTManager("test-secret-key-32-chars-long-here!", 15*time.Minute, 7*24*time.Hour)
	userSvc := services.NewUserService(pool, jwtManager)
	ctx := context.Background()

	// Create test user
	testEmail := "token-rotation-" + uuid.New().String()[:8] + "@example.com"
	user, origTokens, err := userSvc.Register(ctx, services.RegisterInput{
		Email:       testEmail,
		Password:    "SuperPassword123!",
		DisplayName: "Token Tester",
	})
	require.NoError(t, err)
	require.NotEmpty(t, origTokens.RefreshToken)

	t.Run("successful atomic rotation", func(t *testing.T) {
		newTokens, err := userSvc.RefreshTokens(ctx, origTokens.RefreshToken)
		require.NoError(t, err)
		assert.NotEmpty(t, newTokens.AccessToken)
		assert.NotEmpty(t, newTokens.RefreshToken)
		assert.NotEqual(t, origTokens.RefreshToken, newTokens.RefreshToken)

		// Verify old token is now revoked
		var isRevoked bool
		oldHash := auth.HashToken(origTokens.RefreshToken)
		err = pool.QueryRow(ctx, "SELECT revoked FROM refresh_tokens WHERE token_hash = $1", oldHash).Scan(&isRevoked)
		require.NoError(t, err)
		assert.True(t, isRevoked, "old token must be marked revoked")

		// Verify new token exists and is not revoked
		newHash := auth.HashToken(newTokens.RefreshToken)
		err = pool.QueryRow(ctx, "SELECT revoked FROM refresh_tokens WHERE token_hash = $1", newHash).Scan(&isRevoked)
		require.NoError(t, err)
		assert.False(t, isRevoked, "new token must NOT be revoked")

		// Test replaying old token returns ErrTokenRevoked
		_, err = userSvc.RefreshTokens(ctx, origTokens.RefreshToken)
		assert.ErrorIs(t, err, services.ErrTokenRevoked, "replaying old token must return ErrTokenRevoked")

		// Successive rotation with new token works
		newerTokens, err := userSvc.RefreshTokens(ctx, newTokens.RefreshToken)
		require.NoError(t, err)
		assert.NotEmpty(t, newerTokens.RefreshToken)
		assert.NotEqual(t, newTokens.RefreshToken, newerTokens.RefreshToken)
	})

	t.Run("expired refresh token returns ErrTokenExpired", func(t *testing.T) {
		expiredToken := jwtManager.GenerateRefreshToken()
		expiredHash := auth.HashToken(expiredToken)
		_, err := pool.Exec(ctx,
			`INSERT INTO refresh_tokens (id, user_id, token_hash, revoked, expires_at, created_at)
			 VALUES ($1, $2, $3, false, $4, $5)`,
			uuid.New(), user.ID, expiredHash, time.Now().Add(-1*time.Hour), time.Now().Add(-2*time.Hour),
		)
		require.NoError(t, err)

		_, err = userSvc.RefreshTokens(ctx, expiredToken)
		assert.ErrorIs(t, err, services.ErrTokenExpired, "expired token must return ErrTokenExpired")
	})

	t.Run("non-existent refresh token returns ErrTokenNotFound", func(t *testing.T) {
		fakeToken := jwtManager.GenerateRefreshToken()
		_, err := userSvc.RefreshTokens(ctx, fakeToken)
		assert.ErrorIs(t, err, services.ErrTokenNotFound, "non-existent token must return ErrTokenNotFound")
	})

	t.Run("concurrent refresh requests claim token exactly once", func(t *testing.T) {
		// Log in to obtain fresh token
		_, loginTokens, err := userSvc.Login(ctx, services.LoginInput{
			Email:    testEmail,
			Password: "SuperPassword123!",
		})
		require.NoError(t, err)

		concurrency := 8
		var wg sync.WaitGroup
		wg.Add(concurrency)

		successCount := 0
		revokedCount := 0
		var mu sync.Mutex

		for i := 0; i < concurrency; i++ {
			go func() {
				defer wg.Done()
				_, rErr := userSvc.RefreshTokens(ctx, loginTokens.RefreshToken)
				mu.Lock()
				defer mu.Unlock()
				if rErr == nil {
					successCount++
				} else if rErr == services.ErrTokenRevoked {
					revokedCount++
				}
			}()
		}

		wg.Wait()

		assert.Equal(t, 1, successCount, "exactly one concurrent request must successfully claim the token")
		assert.Equal(t, concurrency-1, revokedCount, "all other concurrent requests must see ErrTokenRevoked")
	})
}
