package services_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/auth"
	"github.com/forgelab/backend/internal/crypto"
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

		// Verify within grace period, replaying old token safely returns the cached rotated token
		replayTokens, err := userSvc.RefreshTokens(ctx, origTokens.RefreshToken)
		require.NoError(t, err, "within grace period, replaying old token must succeed safely")
		assert.Equal(t, newTokens.RefreshToken, replayTokens.RefreshToken)

		// Test replaying old token returns ErrTokenRevoked when grace period expires
		userSvc.SetGracePeriod(0)
		_, err = userSvc.RefreshTokens(ctx, origTokens.RefreshToken)
		assert.ErrorIs(t, err, services.ErrTokenRevoked, "replaying old token after grace expiry must return ErrTokenRevoked")
		userSvc.SetGracePeriod(30 * time.Second)

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

	t.Run("concurrent refresh requests claim token safely within grace window", func(t *testing.T) {
		// Log in to obtain fresh token
		_, loginTokens, err := userSvc.Login(ctx, services.LoginInput{
			Email:    testEmail,
			Password: "SuperPassword123!",
		})
		require.NoError(t, err)

		concurrency := 8
		var wg sync.WaitGroup
		wg.Add(concurrency)

		successTokens := make([]*services.AuthTokens, concurrency)
		errs := make([]error, concurrency)

		for i := 0; i < concurrency; i++ {
			idx := i
			go func() {
				defer wg.Done()
				toks, rErr := userSvc.RefreshTokens(ctx, loginTokens.RefreshToken)
				successTokens[idx] = toks
				errs[idx] = rErr
			}()
		}

		wg.Wait()

		// All concurrent requests within the grace window must succeed
		for i := 0; i < concurrency; i++ {
			assert.NoError(t, errs[i], "concurrent request %d should succeed within grace window", i)
			require.NotNil(t, successTokens[i], "concurrent request %d should return tokens", i)
			assert.NotEmpty(t, successTokens[i].AccessToken)
			assert.NotEmpty(t, successTokens[i].RefreshToken)
		}

		// All concurrent callers receive the same fresh rotated tokens
		for i := 1; i < concurrency; i++ {
			assert.Equal(t, successTokens[0].RefreshToken, successTokens[i].RefreshToken, "concurrent callers must receive the same rotated refresh token")
		}
	})

	t.Run("replaying token after grace window expired returns ErrTokenRevoked", func(t *testing.T) {
		userSvc.SetGracePeriod(0)
		defer userSvc.SetGracePeriod(30 * time.Second)

		_, loginTokens, err := userSvc.Login(ctx, services.LoginInput{
			Email:    testEmail,
			Password: "SuperPassword123!",
		})
		require.NoError(t, err)

		// First refresh succeeds and rotates the token
		newTokens, err := userSvc.RefreshTokens(ctx, loginTokens.RefreshToken)
		require.NoError(t, err)
		assert.NotEmpty(t, newTokens.RefreshToken)

		// Immediate second refresh with old token when grace period is 0 must fail with ErrTokenRevoked
		_, err = userSvc.RefreshTokens(ctx, loginTokens.RefreshToken)
		assert.ErrorIs(t, err, services.ErrTokenRevoked, "replaying token after grace period expires must return ErrTokenRevoked")
	})
}

func TestUserService_RedisCachedRotationEncryption(t *testing.T) {
	pool, _ := connectTestDB(t)
	if pool == nil {
		t.Skip("skipping test: database not available")
	}

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	encKey := "dGhpcy1pcy1hLWRldi1rZXktY2hhbmdlLWluLXByb2Q=" // 32-byte valid key
	enc, err := crypto.NewEncryptor(encKey)
	require.NoError(t, err)

	jwtManager := auth.NewJWTManager("test-secret-key-32-chars-long-here!", 15*time.Minute, 7*24*time.Hour)
	userSvc := services.NewUserService(pool, jwtManager)
	userSvc.SetRedisClient(rdb)
	userSvc.SetEncryptor(enc)

	ctx := context.Background()
	testEmail := "encrypted-cache-" + uuid.New().String()[:8] + "@example.com"
	_, origTokens, err := userSvc.Register(ctx, services.RegisterInput{
		Email:       testEmail,
		Password:    "SuperPassword123!",
		DisplayName: "Encrypted Tester",
	})
	require.NoError(t, err)

	// Perform token refresh, which triggers cacheRotation into Redis
	newTokens, err := userSvc.RefreshTokens(ctx, origTokens.RefreshToken)
	require.NoError(t, err)
	require.NotEmpty(t, newTokens.RefreshToken)

	// Check the raw key in Redis
	oldHash := auth.HashToken(origTokens.RefreshToken)
	redisKey := "forgelab:refresh_rotation:" + oldHash
	rawVal, err := rdb.Get(ctx, redisKey).Result()
	require.NoError(t, err)

	// Value in Redis must be encrypted with AES-256-GCM prefix
	assert.True(t, strings.HasPrefix(rawVal, "enc:v1:"), "cached rotation credentials must be encrypted with AES-256-GCM")
	assert.False(t, strings.Contains(rawVal, newTokens.RefreshToken), "cached value in Redis must NOT contain plaintext refresh token")
	assert.False(t, strings.Contains(rawVal, newTokens.AccessToken), "cached value in Redis must NOT contain plaintext access token")

	// Replaying old token decrypts cache and returns matching tokens
	cachedTokens, err := userSvc.RefreshTokens(ctx, origTokens.RefreshToken)
	require.NoError(t, err)
	assert.Equal(t, newTokens.RefreshToken, cachedTokens.RefreshToken)
	assert.Equal(t, newTokens.AccessToken, cachedTokens.AccessToken)

	// Fail-closed requirement: if unencrypted plaintext JSON is present in Redis, it MUST be rejected.
	plainOldToken := "legacy-plain-token-" + uuid.New().String()
	plainOldHash := auth.HashToken(plainOldToken)
	plainKey := "forgelab:refresh_rotation:" + plainOldHash
	expectedTokens := &services.AuthTokens{
		AccessToken:  "legacy-access-token",
		RefreshToken: "legacy-refresh-token",
	}
	plainJSON, _ := json.Marshal(expectedTokens)
	require.NoError(t, rdb.Set(ctx, plainKey, string(plainJSON), time.Minute).Err())

	// Create valid user record so refresh_tokens foreign key constraint succeeds
	uID := uuid.New()
	_, err = pool.Exec(ctx,
		`INSERT INTO users (id, email, password_hash, display_name)
		 VALUES ($1, $2, 'hash', 'Legacy Rejection Tester')`,
		uID, "legacy-reject-"+uID.String()[:8]+"@example.com",
	)
	require.NoError(t, err)
	defer func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id = $1", uID)
	}()

	_, err = pool.Exec(ctx,
		`INSERT INTO refresh_tokens (id, user_id, token_hash, revoked, expires_at, created_at)
		 VALUES ($1, $2, $3, true, $4, $5)`,
		uuid.New(), uID, plainOldHash, time.Now().Add(time.Hour), time.Now(),
	)
	require.NoError(t, err)

	_, err = userSvc.RefreshTokens(ctx, plainOldToken)
	assert.ErrorIs(t, err, services.ErrTokenRevoked, "plaintext JSON in Redis must fail closed and be rejected")
}
