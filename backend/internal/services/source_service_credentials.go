package services

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// EncryptToken encrypts a plaintext token using the service encryptor.
func (s *SourceService) EncryptToken(token string) ([]byte, error) {
	if token == "" {
		return nil, nil
	}
	if s.encryptor == nil {
		return nil, errors.New("encryption service not initialized")
	}
	return s.encryptor.Encrypt([]byte(token))
}

// GetDecryptedAgentToken securely retrieves and decrypts the stored agent session token for a source.
// It verifies source ownership, ensures credentials exist, and returns an explicit authorization error
// if the session token is missing, expired, or decryption fails.
func (s *SourceService) GetDecryptedAgentToken(ctx context.Context, ownerID, sourceID uuid.UUID) (string, error) {
	if sourceID == uuid.Nil {
		return "", ErrSourceDirNotFound
	}
	src, err := s.GetSource(ctx, sourceID, ownerID)
	if err != nil {
		return "", fmt.Errorf("failed to retrieve agent source %s: %w", sourceID, err)
	}
	if src.OwnerID != ownerID {
		return "", ErrUnauthorizedSource
	}
	if len(src.EncryptedSessionToken) == 0 {
		return "", errors.New("agent session credential missing or expired; please re-select project folder")
	}
	if s.encryptor == nil {
		return "", errors.New("encryption service not available")
	}
	dec, err := s.encryptor.Decrypt(src.EncryptedSessionToken)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt agent session credential: %w", err)
	}
	tok := strings.TrimSpace(string(dec))
	if tok == "" {
		return "", errors.New("agent session credential is empty; please re-select project folder")
	}
	return tok, nil
}

// GetAgentSessionToken is an alias for GetDecryptedAgentToken.
func (s *SourceService) GetAgentSessionToken(ctx context.Context, ownerID, sourceID uuid.UUID) (string, error) {
	return s.GetDecryptedAgentToken(ctx, ownerID, sourceID)
}
