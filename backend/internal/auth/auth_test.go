package auth_test

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/forgelab/backend/internal/auth"
)

func TestJWTTokenGenerationAndValidation(t *testing.T) {
	secret := "test-secret-key-32-bytes-long!!"
	manager := auth.NewJWTManager(secret, 15*time.Minute, 7*24*time.Hour)

	userID := uuid.New()
	email := "developer@forgelab.local"

	tokenStr, err := manager.GenerateAccessToken(userID, email)
	if err != nil {
		t.Fatalf("failed to generate access token: %v", err)
	}

	claims, err := manager.ValidateAccessToken(tokenStr)
	if err != nil {
		t.Fatalf("failed to validate valid access token: %v", err)
	}

	if claims.UserID != userID {
		t.Errorf("expected UserID %s, got %s", userID, claims.UserID)
	}

	if claims.Subject != userID.String() {
		t.Errorf("expected Subject %s, got %s", userID.String(), claims.Subject)
	}

	if claims.Email != email {
		t.Errorf("expected Email %s, got %s", email, claims.Email)
	}

	if claims.Issuer != "forgelab" {
		t.Errorf("expected Issuer 'forgelab', got %s", claims.Issuer)
	}
}

func TestRejectUnapprovedSigningAlgorithm(t *testing.T) {
	secret := "test-secret-key-32-bytes-long!!"
	manager := auth.NewJWTManager(secret, 15*time.Minute, 7*24*time.Hour)

	// Create token signed with None algorithm or wrong key
	claims := auth.Claims{
		UserID: uuid.New(),
		Email:  "test@example.com",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: "forgelab",
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodNone, claims)
	tokenStr, _ := token.SignedString(jwt.UnsafeAllowNoneSignatureType)

	_, err := manager.ValidateAccessToken(tokenStr)
	if err == nil {
		t.Fatalf("expected error when validating token signed with 'none' alg, but got nil")
	}
}

func TestRejectExpiredToken(t *testing.T) {
	secret := "test-secret-key-32-bytes-long!!"
	// Expired immediately
	manager := auth.NewJWTManager(secret, -1*time.Minute, 7*24*time.Hour)

	tokenStr, err := manager.GenerateAccessToken(uuid.New(), "expired@forgelab.local")
	if err != nil {
		t.Fatalf("failed to generate token: %v", err)
	}

	_, err = manager.ValidateAccessToken(tokenStr)
	if err != auth.ErrExpiredToken {
		t.Fatalf("expected ErrExpiredToken, got %v", err)
	}
}
