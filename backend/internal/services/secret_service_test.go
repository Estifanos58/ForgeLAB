package services

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/crypto"
	"github.com/forgelab/backend/internal/models"
)

func TestSecretClassificationHeuristics(t *testing.T) {
	secretKeys := []string{
		"DATABASE_URL",
		"JWT_SECRET",
		"API_KEY",
		"STRIPE_APIKEY",
		"AWS_ACCESS_KEY",
		"AUTH_TOKEN",
		"USER_PASSWORD",
		"DB_PASS",
		"SSL_CERTIFICATE",
		"PRIVATE_KEY",
		"CLIENT_SECRET",
	}

	for _, k := range secretKeys {
		assert.True(t, IsSecretKey(k), "key %q should be classified as secret", k)
	}

	publicKeys := []string{
		"NEXT_PUBLIC_API_URL",
		"NEXT_PUBLIC_APP_NAME",
		"VITE_SITE_TITLE",
		"PUBLIC_ASSET_URL",
		"PORT",
		"NODE_ENV",
	}

	for _, k := range publicKeys {
		assert.False(t, IsSecretKey(k), "key %q should be classified as public", k)
	}
}

func TestSecretService_Encryptor_EncryptionAtRest(t *testing.T) {
	encKey := "dGhpcy1pcy1hLWRldi1rZXktY2hhbmdlLWluLXByb2Q="
	encryptor, err := crypto.NewEncryptor(encKey)
	require.NoError(t, err)

	plain := "my-secret-password-123"
	encrypted, err := encryptor.Encrypt([]byte(plain))
	require.NoError(t, err)
	assert.NotEqual(t, plain, string(encrypted))

	decrypted, err := encryptor.Decrypt(encrypted)
	require.NoError(t, err)
	assert.Equal(t, plain, string(decrypted))
}

func TestSecretService_EnvSnapshot_EncryptionTamperingScopeAndBuildSeparation(t *testing.T) {
	keyA := "dGhpcy1pcy1hLWRldi1rZXktY2hhbmdlLWluLXByb2Q="
	keyB := "YW5vdGhlci0zMi1ieXRlLWVuY3J5cHRpb24ta2V5ISE=" // base64("another-32-byte-encryption-key!!") (32 bytes)
	encA, err := crypto.NewEncryptor(keyA)
	require.NoError(t, err)
	encB, err := crypto.NewEncryptor(keyB)
	require.NoError(t, err)

	svcA := NewSecretService(nil, encA, nil)
	svcB := NewSecretService(nil, encB, nil)

	entries := []EnvSnapshotEntry{
		{Key: "DATABASE_URL", Value: "postgres://user:supersecret@db:5432/app", Scope: models.EnvScopeRuntime, IsSecret: true},
		{Key: "NPM_TOKEN", Value: "npm_secret_token_999", Scope: models.EnvScopeBuild, IsSecret: true},
		{Key: "API_SIGNING_KEY", Value: "signing_secret_both", Scope: models.EnvScopeBoth, IsSecret: true},
		{Key: "NEXT_PUBLIC_API_URL", Value: "https://api.example.com", Scope: models.EnvScopeBuild, IsSecret: false},
		{Key: "LOG_LEVEL", Value: "info", Scope: models.EnvScopeRuntime, IsSecret: false},
	}
	rawJSON, err := json.Marshal(entries)
	require.NoError(t, err)

	ciphertext, err := encA.Encrypt(rawJSON)
	require.NoError(t, err)

	// 1. Runtime scope decryption returns runtime + both entries and redacts secrets only
	runtimeMap, runtimeSecrets, err := svcA.GetEnvMapFromSnapshot(ciphertext, models.EnvScopeRuntime)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"DATABASE_URL":    "postgres://user:supersecret@db:5432/app",
		"API_SIGNING_KEY": "signing_secret_both",
		"LOG_LEVEL":       "info",
	}, runtimeMap)
	assert.ElementsMatch(t, []string{"postgres://user:supersecret@db:5432/app", "signing_secret_both"}, runtimeSecrets)

	// 2. Build variable separation never places secrets in normalArgs (Docker build-args)
	normalArgs, secretVars, allSecrets, err := svcA.GetBuildVariables(context.Background(), uuid.New(), nil, ciphertext)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"NEXT_PUBLIC_API_URL": "https://api.example.com"}, normalArgs)
	assert.Equal(t, map[string]string{
		"NPM_TOKEN":       "npm_secret_token_999",
		"API_SIGNING_KEY": "signing_secret_both",
	}, secretVars)
	assert.ElementsMatch(t, []string{
		"postgres://user:supersecret@db:5432/app",
		"npm_secret_token_999",
		"signing_secret_both",
	}, allSecrets)

	// 3. Wrong key fails closed
	_, _, err = svcB.GetEnvMapFromSnapshot(ciphertext, models.EnvScopeRuntime)
	require.Error(t, err)

	// 4. Tampered ciphertext fails authentication tag verification
	tampered := append([]byte(nil), ciphertext...)
	tampered[len(tampered)-1] ^= 0xff
	_, _, err = svcA.GetEnvMapFromSnapshot(tampered, models.EnvScopeRuntime)
	require.Error(t, err)

	// 5. Encrypted malformed JSON fails cleanly
	badJSONCipher, err := encA.Encrypt([]byte("{not-json"))
	require.NoError(t, err)
	_, _, err = svcA.GetEnvMapFromSnapshot(badJSONCipher, models.EnvScopeRuntime)
	require.Error(t, err)
}
