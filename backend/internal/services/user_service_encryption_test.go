package services

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/crypto"
)

func TestUserService_RedisRefreshCache_FailClosed(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	enc, err := crypto.NewEncryptor("dGhpcy1pcy1hLWRldi1rZXktY2hhbmdlLWluLXByb2Q=")
	require.NoError(t, err)
	wrongEnc, err := crypto.NewEncryptor("another-32-byte-secret-for-test!")
	require.NoError(t, err)

	sampleTokens := &AuthTokens{
		AccessToken:  "access-secret-123",
		RefreshToken: "refresh-secret-456",
	}

	t.Run("caches encrypted payload when encryptor is configured", func(t *testing.T) {
		svc := NewUserService(nil, nil)
		svc.SetRedisClient(rdb)
		svc.SetEncryptor(enc)
		svc.SetGracePeriod(30 * time.Second)

		hash := "hash-encrypted-ok"
		svc.cacheRotation(ctx, hash, sampleTokens)

		raw, err := rdb.Get(ctx, "forgelab:refresh_rotation:"+hash).Result()
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(raw, "enc:v1:"), "Redis entry must carry enc:v1: ciphertext prefix")
		assert.NotContains(t, raw, sampleTokens.RefreshToken)
		assert.NotContains(t, raw, sampleTokens.AccessToken)

		// Evict in-memory cache so getCachedRotation reads from Redis
		svc.rotationCache.Delete(hash)
		got := svc.getCachedRotation(ctx, hash)
		require.NotNil(t, got)
		assert.Equal(t, sampleTokens.AccessToken, got.AccessToken)
		assert.Equal(t, sampleTokens.RefreshToken, got.RefreshToken)
	})

	t.Run("skips Redis write and refuses Redis read when encryptor is nil", func(t *testing.T) {
		svc := NewUserService(nil, nil)
		svc.SetRedisClient(rdb)
		svc.SetGracePeriod(30 * time.Second)

		hash := "hash-no-encryptor"
		svc.cacheRotation(ctx, hash, sampleTokens)

		_, err := rdb.Get(ctx, "forgelab:refresh_rotation:"+hash).Result()
		assert.ErrorIs(t, err, redis.Nil, "must fail closed and never store plaintext in Redis when encryptor is nil")

		// Even if plaintext JSON is inserted out-of-band into Redis, fail closed
		plain, _ := json.Marshal(sampleTokens)
		require.NoError(t, rdb.Set(ctx, "forgelab:refresh_rotation:"+hash, string(plain), time.Minute).Err())
		svc.rotationCache.Delete(hash)

		assert.Nil(t, svc.getCachedRotation(ctx, hash), "must refuse reading Redis entry when encryptor is nil")
	})

	t.Run("rejects plaintext JSON injected into Redis when encryptor is configured", func(t *testing.T) {
		svc := NewUserService(nil, nil)
		svc.SetRedisClient(rdb)
		svc.SetEncryptor(enc)
		svc.SetGracePeriod(30 * time.Second)

		hash := "hash-plaintext-injected"
		plain, _ := json.Marshal(sampleTokens)
		require.NoError(t, rdb.Set(ctx, "forgelab:refresh_rotation:"+hash, string(plain), time.Minute).Err())

		assert.Nil(t, svc.getCachedRotation(ctx, hash), "must reject unencrypted JSON in Redis")
	})

	t.Run("rejects ciphertext encrypted with a wrong key", func(t *testing.T) {
		svc := NewUserService(nil, nil)
		svc.SetRedisClient(rdb)
		svc.SetEncryptor(enc)
		svc.SetGracePeriod(30 * time.Second)

		payload, _ := json.Marshal(sampleTokens)
		wrongCipher, err := wrongEnc.Encrypt(payload)
		require.NoError(t, err)

		hash := "hash-wrong-key"
		encodedWrong := "enc:v1:" + base64.StdEncoding.EncodeToString(wrongCipher)
		require.NoError(t, rdb.Set(ctx, "forgelab:refresh_rotation:"+hash, encodedWrong, time.Minute).Err())

		assert.Nil(t, svc.getCachedRotation(ctx, hash), "must reject ciphertext from mismatched encryption key")
	})

	t.Run("rejects malformed base64 even with enc:v1 prefix", func(t *testing.T) {
		svc := NewUserService(nil, nil)
		svc.SetRedisClient(rdb)
		svc.SetEncryptor(enc)
		svc.SetGracePeriod(30 * time.Second)

		hash := "hash-bad-b64"
		require.NoError(t, rdb.Set(ctx, "forgelab:refresh_rotation:"+hash, "enc:v1:!!!not-base64!!!", time.Minute).Err())
		assert.Nil(t, svc.getCachedRotation(ctx, hash), "must fail closed on malformed base64 ciphertext")
	})
}
