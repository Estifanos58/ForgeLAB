package services

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/crypto"
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
