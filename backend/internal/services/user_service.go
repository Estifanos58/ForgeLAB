package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/forgelab/backend/internal/auth"
	"github.com/forgelab/backend/internal/models"
)

var (
	ErrUserExists       = errors.New("user with this email already exists")
	ErrUserNotFound     = errors.New("user not found")
	ErrInvalidPassword  = errors.New("invalid password")
	ErrTokenRevoked     = errors.New("refresh token has been revoked")
	ErrTokenExpired     = errors.New("refresh token has expired")
	ErrTokenNotFound    = errors.New("refresh token not found")
)

// UserService handles user-related business logic.
type UserService struct {
	db         *pgxpool.Pool
	jwtManager *auth.JWTManager
}

// NewUserService creates a new UserService.
func NewUserService(db *pgxpool.Pool, jwtManager *auth.JWTManager) *UserService {
	return &UserService{
		db:         db,
		jwtManager: jwtManager,
	}
}

// RegisterInput holds the data needed to register a new user.
type RegisterInput struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
}

// LoginInput holds the data needed to log in.
type LoginInput struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// AuthTokens holds the access and refresh tokens returned on login/register.
type AuthTokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"` // seconds
}

// Register creates a new user account.
func (s *UserService) Register(ctx context.Context, input RegisterInput) (*models.User, *AuthTokens, error) {
	// Check if user already exists
	var exists bool
	err := s.db.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM users WHERE email = $1)",
		input.Email,
	).Scan(&exists)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to check user existence: %w", err)
	}
	if exists {
		return nil, nil, ErrUserExists
	}

	// Hash the password
	passwordHash, err := auth.HashPassword(input.Password)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to hash password: %w", err)
	}

	// Create the user
	user := &models.User{
		ID:           uuid.New(),
		Email:        input.Email,
		PasswordHash: passwordHash,
		DisplayName:  input.DisplayName,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	_, err = s.db.Exec(ctx,
		`INSERT INTO users (id, email, password_hash, display_name, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		user.ID, user.Email, user.PasswordHash, user.DisplayName, user.CreatedAt, user.UpdatedAt,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create user: %w", err)
	}

	// Generate tokens
	tokens, err := s.generateTokens(ctx, user)
	if err != nil {
		return nil, nil, err
	}

	slog.Info("user registered", "user_id", user.ID, "email", user.Email)
	return user, tokens, nil
}

// Login authenticates a user and returns tokens.
func (s *UserService) Login(ctx context.Context, input LoginInput) (*models.User, *AuthTokens, error) {
	// Find user by email
	user := &models.User{}
	err := s.db.QueryRow(ctx,
		`SELECT id, email, password_hash, display_name, created_at, updated_at
		 FROM users WHERE email = $1`,
		input.Email,
	).Scan(&user.ID, &user.Email, &user.PasswordHash, &user.DisplayName, &user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, ErrInvalidPassword
		}
		return nil, nil, fmt.Errorf("failed to find user: %w", err)
	}

	// Verify password
	if err := auth.CheckPassword(input.Password, user.PasswordHash); err != nil {
		return nil, nil, ErrInvalidPassword
	}

	// Generate tokens
	tokens, err := s.generateTokens(ctx, user)
	if err != nil {
		return nil, nil, err
	}

	slog.Info("user logged in", "user_id", user.ID, "email", user.Email)
	return user, tokens, nil
}

// RefreshTokens validates a refresh token and issues new tokens.
func (s *UserService) RefreshTokens(ctx context.Context, refreshToken string) (*AuthTokens, error) {
	tokenHash := auth.HashToken(refreshToken)

	// Find the refresh token
	var rt models.RefreshToken
	var userEmail string
	err := s.db.QueryRow(ctx,
		`SELECT rt.id, rt.user_id, rt.expires_at, rt.revoked, u.email
		 FROM refresh_tokens rt
		 JOIN users u ON u.id = rt.user_id
		 WHERE rt.token_hash = $1`,
		tokenHash,
	).Scan(&rt.ID, &rt.UserID, &rt.ExpiresAt, &rt.Revoked, &userEmail)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrTokenNotFound
		}
		return nil, fmt.Errorf("failed to find refresh token: %w", err)
	}

	if rt.Revoked {
		return nil, ErrTokenRevoked
	}

	if time.Now().After(rt.ExpiresAt) {
		return nil, ErrTokenExpired
	}

	// Revoke the old token (single-use rotation)
	_, err = s.db.Exec(ctx,
		"UPDATE refresh_tokens SET revoked = true WHERE id = $1",
		rt.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to revoke old token: %w", err)
	}

	// Generate new tokens
	user := &models.User{ID: rt.UserID, Email: userEmail}
	tokens, err := s.generateTokens(ctx, user)
	if err != nil {
		return nil, err
	}

	return tokens, nil
}

// GetUserByID retrieves a user by their ID.
func (s *UserService) GetUserByID(ctx context.Context, userID uuid.UUID) (*models.User, error) {
	user := &models.User{}
	err := s.db.QueryRow(ctx,
		`SELECT id, email, display_name, created_at, updated_at
		 FROM users WHERE id = $1`,
		userID,
	).Scan(&user.ID, &user.Email, &user.DisplayName, &user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("failed to get user: %w", err)
	}
	return user, nil
}

// generateTokens creates access and refresh tokens for a user.
func (s *UserService) generateTokens(ctx context.Context, user *models.User) (*AuthTokens, error) {
	accessToken, err := s.jwtManager.GenerateAccessToken(user.ID, user.Email)
	if err != nil {
		return nil, fmt.Errorf("failed to generate access token: %w", err)
	}

	refreshToken := s.jwtManager.GenerateRefreshToken()
	tokenHash := auth.HashToken(refreshToken)

	// Store the refresh token (hashed)
	_, err = s.db.Exec(ctx,
		`INSERT INTO refresh_tokens (id, user_id, token_hash, expires_at, created_at)
		 VALUES ($1, $2, $3, $4, $5)`,
		uuid.New(),
		user.ID,
		tokenHash,
		time.Now().Add(s.jwtManager.RefreshTokenExpiry()),
		time.Now(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to store refresh token: %w", err)
	}

	return &AuthTokens{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    int(s.jwtManager.RefreshTokenExpiry().Seconds()),
	}, nil
}
