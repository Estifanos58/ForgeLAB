package crypto_test

import (
	"bytes"
	"testing"

	"github.com/forgelab/backend/internal/crypto"
)

func TestAESGCMEncryptionAndDecryption(t *testing.T) {
	key := "dGhpcy1pcy1hLWRldi1rZXktY2hhbmdlLWluLXByb2Q=" // 32 bytes base64
	encryptor, err := crypto.NewEncryptor(key)
	if err != nil {
		t.Fatalf("failed to create encryptor: %v", err)
	}

	secretText := "postgres://forgelab:supersecret@localhost:5432/db"

	encrypted, err := encryptor.Encrypt([]byte(secretText))
	if err != nil {
		t.Fatalf("failed to encrypt: %v", err)
	}

	decrypted, err := encryptor.Decrypt(encrypted)
	if err != nil {
		t.Fatalf("failed to decrypt: %v", err)
	}

	if string(decrypted) != secretText {
		t.Errorf("expected decrypted string '%s', got '%s'", secretText, string(decrypted))
	}
}

func TestNonceUniqueness(t *testing.T) {
	key := "dGhpcy1pcy1hLWRldi1rZXktY2hhbmdlLWluLXByb2Q="
	encryptor, _ := crypto.NewEncryptor(key)

	secretText := "my-secret-token"

	enc1, _ := encryptor.Encrypt([]byte(secretText))
	enc2, _ := encryptor.Encrypt([]byte(secretText))

	if bytes.Equal(enc1, enc2) {
		t.Errorf("two encryptions of the same plaintext must produce different ciphertexts due to unique nonces!")
	}
}

func TestKeyValidation_RejectsInvalidKeySize(t *testing.T) {
	invalidKeys := []struct {
		name string
		key  string
	}{
		{"empty key", ""},
		{"short raw string", "too-short-key"},
		{"31 bytes raw string", "1234567890123456789012345678901"},
		{"33 bytes raw string", "123456789012345678901234567890123"},
		{"short base64", "c2hvcnQ="},                                       // decodes to "short" (5 bytes)
		{"invalid base64 non-32", "dGhpcy1pcy1ub3QtMzItYnl0ZXM="},        // decodes to 19 bytes
	}

	for _, tc := range invalidKeys {
		t.Run(tc.name, func(t *testing.T) {
			_, err := crypto.NewEncryptor(tc.key)
			if err == nil {
				t.Fatalf("expected error for key %q, got nil", tc.key)
			}
			if err != crypto.ErrInvalidKeySize {
				t.Fatalf("expected ErrInvalidKeySize, got: %v", err)
			}
		})
	}
}

func TestKeyValidation_PreservesAccessToExistingEncryptedData(t *testing.T) {
	// 32-byte raw key
	rawKey := "default-forgelab-encryption-key!"
	encRaw, err := crypto.NewEncryptor(rawKey)
	if err != nil {
		t.Fatalf("failed to create encryptor with raw 32-byte key: %v", err)
	}

	plaintext := "existing-database-credential-12345"
	ciphertext, err := encRaw.Encrypt([]byte(plaintext))
	if err != nil {
		t.Fatalf("failed to encrypt: %v", err)
	}

	// Recreate encryptor and verify decryption
	encRaw2, err := crypto.NewEncryptor(rawKey)
	if err != nil {
		t.Fatalf("failed to recreate encryptor: %v", err)
	}
	decrypted, err := encRaw2.Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("failed to decrypt existing data: %v", err)
	}
	if string(decrypted) != plaintext {
		t.Fatalf("expected %s, got %s", plaintext, string(decrypted))
	}
}

func TestValidateProductionKey_RejectsWeakAndKeys(t *testing.T) {
	weakKeys := []struct {
		name string
		key  string
	}{
		{"default dev base64 key", "dGhpcy1pcy1hLWRldi1rZXktY2hhbmdlLWluLXByb2Q="},
		{"default dev raw key", "this-is-a-dev-key-change-in-prod"},
		{"fallback dev key", "default-forgelab-encryption-key!"},
		{"sequential repetitive key", "12345678901234567890123456789012"},
		{"all zero bytes base64", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="},
		{"low entropy key", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	}

	for _, tc := range weakKeys {
		t.Run(tc.name, func(t *testing.T) {
			err := crypto.ValidateProductionKey(tc.key)
			if err == nil {
				t.Fatalf("expected production key validation error for %s, got nil", tc.name)
			}
		})
	}

	// Valid production key: 32 random bytes base64 encoded
	validProdKey := "MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE="
	if err := crypto.ValidateProductionKey(validProdKey); err != nil {
		t.Fatalf("expected valid production key to pass, got: %v", err)
	}
}
