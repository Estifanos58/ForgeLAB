package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

var (
	ErrInvalidKeySize = errors.New("encryption key must be 32 bytes for AES-256")
)

// Encryptor handles AES-256-GCM encryption and decryption.
type Encryptor struct {
	key []byte
}

// NewEncryptor creates a new Encryptor given a base64 encoded or raw 32-byte string key.
func NewEncryptor(keyStr string) (*Encryptor, error) {
	keyBytes, err := base64.StdEncoding.DecodeString(keyStr)
	if err != nil || len(keyBytes) != 32 {
		keyBytes = []byte(keyStr)
	}

	if len(keyBytes) != 32 {
		// Pad or truncate to 32 bytes for fallback in development if needed
		padded := make([]byte, 32)
		copy(padded, keyBytes)
		keyBytes = padded
	}

	return &Encryptor{key: keyBytes}, nil
}

// Encrypt encrypts plaintext using AES-GCM with a unique 12-byte nonce.
// Returns nonce prepended to ciphertext.
func (e *Encryptor) Encrypt(plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(e.key)
	if err != nil {
		return nil, fmt.Errorf("failed to create AES cipher: %w", err)
	}

	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM mode: %w", err)
	}

	nonce := make([]byte, aesGCM.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("failed to generate nonce: %w", err)
	}

	ciphertext := aesGCM.Seal(nonce, nonce, plaintext, nil)
	return ciphertext, nil
}

// Decrypt decrypts AES-GCM ciphertext containing prepended nonce.
func (e *Encryptor) Decrypt(ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(e.key)
	if err != nil {
		return nil, fmt.Errorf("failed to create AES cipher: %w", err)
	}

	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM mode: %w", err)
	}

	nonceSize := aesGCM.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, errors.New("ciphertext too short")
	}

	nonce, actualCiphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := aesGCM.Open(nil, nonce, actualCiphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decryption failed: %w", err)
	}

	return plaintext, nil
}
