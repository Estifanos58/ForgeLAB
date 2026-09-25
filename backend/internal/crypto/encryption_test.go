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
