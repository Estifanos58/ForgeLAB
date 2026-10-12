package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
)

var (
	ErrInvalidKeySize = errors.New("encryption key must be exactly 32 bytes for AES-256")
	ErrWeakKey        = errors.New("encryption key is too weak or uses a known default")
)

// Encryptor handles AES-256-GCM encryption and decryption.
type Encryptor struct {
	key []byte
}

// ParseKey extracts a 32-byte AES-256 key from either a raw 32-byte string or a base64 encoded string.
// Returns ErrInvalidKeySize if the key does not decode to or equal exactly 32 bytes.
func ParseKey(keyStr string) ([]byte, error) {
	if keyStr == "" {
		return nil, ErrInvalidKeySize
	}

	// Try standard base64 decoding first
	if keyBytes, err := base64.StdEncoding.DecodeString(keyStr); err == nil && len(keyBytes) == 32 {
		return keyBytes, nil
	}
	// Try URL / raw base64 decoding
	if keyBytes, err := base64.RawStdEncoding.DecodeString(keyStr); err == nil && len(keyBytes) == 32 {
		return keyBytes, nil
	}

	// Try raw byte string
	rawBytes := []byte(keyStr)
	if len(rawBytes) == 32 {
		return rawBytes, nil
	}

	return nil, ErrInvalidKeySize
}

// ValidateProductionKey ensures the key is valid 32-bytes and not a known default or low-entropy key.
func ValidateProductionKey(keyStr string) error {
	keyBytes, err := ParseKey(keyStr)
	if err != nil {
		return err
	}

	lowerKey := strings.ToLower(keyStr)
	decodedStr := strings.ToLower(string(keyBytes))

	if keyStr == "dGhpcy1pcy1hLWRldi1rZXktY2hhbmdlLWluLXByb2Q=" ||
		decodedStr == "this-is-a-dev-key-change-in-prod" ||
		decodedStr == "default-forgelab-encryption-key!" ||
		decodedStr == "12345678901234567890123456789012" ||
		decodedStr == "abcdefghijklmnopqrstuvwxyz123456" ||
		strings.Contains(decodedStr, "change-in-prod") ||
		strings.Contains(decodedStr, "default-forgelab") ||
		strings.Contains(decodedStr, "dev-key") ||
		strings.Contains(lowerKey, "change-in-prod") ||
		strings.Contains(lowerKey, "default-forgelab") ||
		strings.Contains(lowerKey, "dev-key") {
		return fmt.Errorf("%w: cannot use development/default encryption key in production", ErrWeakKey)
	}

	// Check byte diversity (low entropy protection)
	uniqueBytes := make(map[byte]struct{})
	for _, b := range keyBytes {
		uniqueBytes[b] = struct{}{}
	}
	if len(uniqueBytes) <= 4 {
		return fmt.Errorf("%w: key has insufficient entropy", ErrWeakKey)
	}

	return nil
}

// NewEncryptor creates a new Encryptor given a base64 encoded or raw 32-byte string key.
// Returns ErrInvalidKeySize if the key does not decode to or equal exactly 32 bytes.
func NewEncryptor(keyStr string) (*Encryptor, error) {
	keyBytes, err := ParseKey(keyStr)
	if err != nil {
		return nil, err
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
