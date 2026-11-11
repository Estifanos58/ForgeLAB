package services

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/forgelab/backend/internal/auth"
	"github.com/forgelab/backend/internal/crypto"
	"github.com/forgelab/backend/internal/models"
)

var (
	ErrUserExists      = errors.New("user with this email already exists")
	ErrUserNotFound    = errors.New("user not found")
	ErrInvalidPassword = errors.New("invalid password")
	ErrTokenRevoked    = errors.New("refresh token has been revoked")
	ErrTokenExpired    = errors.New("refresh token has expired")
	ErrTokenNotFound   = errors.New("refresh token not found")
)

type cachedRotation struct {
	tokens    *AuthTokens
	expiresAt time.Time
}

// UserService handles user-related business logic.
type UserService struct {
	db            *pgxpool.Pool
	jwtManager    *auth.JWTManager
	redis         *redis.Client
	encryptor     *crypto.Encryptor
	rotationCache sync.Map
	gracePeriod   time.Duration
}

// NewUserService creates a new UserService.
func NewUserService(db *pgxpool.Pool, jwtManager *auth.JWTManager) *UserService {
	return &UserService{
		db:          db,
		jwtManager:  jwtManager,
		gracePeriod: 30 * time.Second,
	}
}

// SetEncryptor attaches an encryptor to protect refresh credentials cached in Redis.
func (s *UserService) SetEncryptor(enc *crypto.Encryptor) {
	s.encryptor = enc
}

// SetRedisClient attaches a Redis client for distributed token rotation caching.
func (s *UserService) SetRedisClient(r *redis.Client) {
	s.redis = r
}

// SetGracePeriod configures the concurrency grace window for simultaneous refresh calls.
func (s *UserService) SetGracePeriod(d time.Duration) {
	s.gracePeriod = d
}

// RefreshTokenExpiry returns the configured refresh token duration.
func (s *UserService) RefreshTokenExpiry() time.Duration {
	if s != nil && s.jwtManager != nil {
		return s.jwtManager.RefreshTokenExpiry()
	}
	return 30 * 24 * time.Hour
}

// AccessTokenExpiry returns the configured access token duration.
func (s *UserService) AccessTokenExpiry() time.Duration {
	if s != nil && s.jwtManager != nil {
		return s.jwtManager.AccessTokenExpiry()
	}
	return 24 * time.Hour
}

// JWTManager returns the underlying JWT manager instance.
func (s *UserService) JWTManager() *auth.JWTManager {
	if s != nil {
		return s.jwtManager
	}
	return nil
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

// EnsureGuestUser ensures the single shared guest user record exists in the database.
func (s *UserService) EnsureGuestUser(ctx context.Context) error {
	return s.EnsureGuestUserWithID(ctx, models.GuestUserID)
}

// EnsureGuestUserWithID ensures a specific guest user record exists in the database.
func (s *UserService) EnsureGuestUserWithID(ctx context.Context, guestID uuid.UUID) error {
	if s.db == nil || guestID == uuid.Nil {
		return nil
	}
	models.RegisterGuestID(guestID)
	email := models.MakeGuestUserEmail(guestID)
	_, err := s.db.Exec(ctx, `
		INSERT INTO users (id, email, password_hash, display_name, created_at, updated_at)
		VALUES ($1, $2, $3, $4, NOW(), NOW())
		ON CONFLICT (id) DO UPDATE SET updated_at = NOW()
	`, guestID, email, "$2a$12$disabled.guest.account.cannot.login.directly.hash", "Guest")
	if err != nil {
		return fmt.Errorf("failed to ensure guest user identity %s: %w", guestID, err)
	}
	return nil
}

// Register creates a new user account.
func (s *UserService) Register(ctx context.Context, input RegisterInput) (*models.User, *AuthTokens, error) {
	input.Email = strings.ToLower(strings.TrimSpace(input.Email))
	if input.Email == models.GuestUserEmail || strings.HasSuffix(input.Email, "@forgelab.local") {
		return nil, nil, ErrUserExists
	}

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
		PasswordHash: &passwordHash,
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
	input.Email = strings.ToLower(strings.TrimSpace(input.Email))
	if input.Email == models.GuestUserEmail {
		return nil, nil, ErrInvalidPassword
	}

	// Find user by email
	user := &models.User{}
	var passwordHash *string
	err := s.db.QueryRow(ctx,
		`SELECT id, email, password_hash, display_name, created_at, updated_at
		 FROM users WHERE email = $1`,
		input.Email,
	).Scan(&user.ID, &user.Email, &passwordHash, &user.DisplayName, &user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, ErrInvalidPassword
		}
		return nil, nil, fmt.Errorf("failed to find user: %w", err)
	}

	if models.IsGuest(user.ID) {
		return nil, nil, ErrInvalidPassword
	}

	if passwordHash == nil || *passwordHash == "" {
		return nil, nil, ErrInvalidPassword
	}

	// Verify password
	if err := auth.CheckPassword(input.Password, *passwordHash); err != nil {
		return nil, nil, ErrInvalidPassword
	}
	user.PasswordHash = passwordHash

	// Generate tokens
	tokens, err := s.generateTokens(ctx, user)
	if err != nil {
		return nil, nil, err
	}

	slog.Info("user logged in", "user_id", user.ID, "email", user.Email)
	return user, tokens, nil
}

func (s *UserService) cacheRotation(ctx context.Context, oldTokenHash string, tokens *AuthTokens) {
	if s.gracePeriod <= 0 || tokens == nil {
		return
	}
	grace := s.gracePeriod
	now := time.Now()

	s.rotationCache.Store(oldTokenHash, &cachedRotation{
		tokens:    tokens,
		expiresAt: now.Add(grace),
	})

	if s.redis != nil {
		// Fail closed: never store plaintext credentials in Redis when encryption is unconfigured or fails.
		if s.encryptor == nil {
			slog.Warn("skipping redis refresh rotation cache: encryptor not configured (failing closed)")
			return
		}
		payload, err := json.Marshal(tokens)
		if err != nil {
			slog.Warn("skipping redis refresh rotation cache: failed to marshal tokens", "error", err)
			return
		}
		encrypted, encErr := s.encryptor.Encrypt(payload)
		if encErr != nil || len(encrypted) == 0 {
			slog.Warn("skipping redis refresh rotation cache: encryption failed (failing closed)", "error", encErr)
			return
		}
		encoded := "enc:v1:" + base64.StdEncoding.EncodeToString(encrypted)
		if setErr := s.redis.Set(ctx, "forgelab:refresh_rotation:"+oldTokenHash, encoded, grace).Err(); setErr != nil {
			slog.Warn("failed to cache encrypted refresh rotation in redis", "error", setErr)
		}
	}
}

func (s *UserService) getCachedRotation(ctx context.Context, oldTokenHash string) *AuthTokens {
	if s.gracePeriod <= 0 {
		return nil
	}
	now := time.Now()

	if val, ok := s.rotationCache.Load(oldTokenHash); ok {
		if entry, ok := val.(*cachedRotation); ok && entry != nil {
			if now.Before(entry.expiresAt) {
				return entry.tokens
			}
			s.rotationCache.Delete(oldTokenHash)
		}
	}

	if s.redis != nil {
		// Fail closed: never read or trust cached refresh tokens from Redis without active decryption.
		if s.encryptor == nil {
			slog.Warn("ignoring redis refresh rotation cache: encryptor not configured (failing closed)")
			return nil
		}
		rawVal, err := s.redis.Get(ctx, "forgelab:refresh_rotation:"+oldTokenHash).Result()
		if err == nil && len(rawVal) > 0 {
			const encPrefix = "enc:v1:"
			if !strings.HasPrefix(rawVal, encPrefix) {
				slog.Warn("rejecting unencrypted refresh rotation cache entry in redis (failing closed)")
				return nil
			}
			cipherBytes, decB64Err := base64.StdEncoding.DecodeString(strings.TrimPrefix(rawVal, encPrefix))
			if decB64Err != nil {
				slog.Warn("rejecting malformed refresh rotation cache entry in redis (failing closed)", "error", decB64Err)
				return nil
			}
			decrypted, decErr := s.encryptor.Decrypt(cipherBytes)
			if decErr != nil {
				slog.Warn("rejecting undecryptable refresh rotation cache entry in redis (failing closed)", "error", decErr)
				return nil
			}
			var tokens AuthTokens
			if err := json.NewDecoder(strings.NewReader(string(decrypted))).Decode(&tokens); err == nil && tokens.RefreshToken != "" {
				s.rotationCache.Store(oldTokenHash, &cachedRotation{
					tokens:    &tokens,
					expiresAt: now.Add(15 * time.Second),
				})
				return &tokens
			}
		}
	}

	return nil
}

// PruneExpiredRefreshTokens deletes expired or revoked refresh tokens from the database.
func (s *UserService) PruneExpiredRefreshTokens(ctx context.Context) (int64, error) {
	if s.db == nil {
		return 0, nil
	}
	tag, err := s.db.Exec(ctx, `
		DELETE FROM refresh_tokens
		WHERE expires_at < NOW()
		   OR (revoked = TRUE AND created_at < NOW() - INTERVAL '7 days')
	`)
	if err != nil {
		return 0, fmt.Errorf("failed to prune expired refresh tokens: %w", err)
	}
	return tag.RowsAffected(), nil
}

// RefreshTokens validates a refresh token and issues new tokens atomically in a single PostgreSQL transaction.
// It implements a concurrency grace window to allow simultaneous requests from independent browser tabs
// or components to receive fresh rotated tokens without losing sessions or hitting false replay errors.
func (s *UserService) RefreshTokens(ctx context.Context, refreshToken string) (*AuthTokens, error) {
	cleanToken := strings.TrimSpace(refreshToken)
	if cleanToken == "" {
		return nil, ErrTokenNotFound
	}
	tokenHash := auth.HashToken(cleanToken)
	now := time.Now()

	// Fast-path: Check if this token was already rotated within the concurrency grace window
	if cached := s.getCachedRotation(ctx, tokenHash); cached != nil {
		return cached, nil
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Step 1 & 2: Atomically validate and claim the existing refresh token, revoking it in the same transaction
	var rtID uuid.UUID
	var userID uuid.UUID
	var userEmail string
	err = tx.QueryRow(ctx,
		`UPDATE refresh_tokens rt
		 SET revoked = true
		 FROM users u
		 WHERE rt.token_hash = $1
		   AND rt.user_id = u.id
		   AND rt.revoked = false
		   AND rt.expires_at > $2
		 RETURNING rt.id, rt.user_id, u.email`,
		tokenHash, now,
	).Scan(&rtID, &userID, &userEmail)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Rollback(ctx)

			// If grace period is active, spin briefly checking rotation cache:
			// a concurrent winning transaction is committing and caching replacement tokens.
			if s.gracePeriod > 0 {
				for attempt := 0; attempt < 10; attempt++ {
					if cached := s.getCachedRotation(ctx, tokenHash); cached != nil {
						return cached, nil
					}
					time.Sleep(time.Duration(15*(attempt+1)) * time.Millisecond)
				}
				if cached := s.getCachedRotation(ctx, tokenHash); cached != nil {
					return cached, nil
				}
			}

			// Token couldn't be claimed and is not in rotation cache.
			// Query token state to determine exact failure cause.
			var isRevoked bool
			var expiresAt time.Time
			checkErr := s.db.QueryRow(ctx,
				"SELECT revoked, expires_at FROM refresh_tokens WHERE token_hash = $1",
				tokenHash,
			).Scan(&isRevoked, &expiresAt)
			if checkErr != nil {
				if errors.Is(checkErr, pgx.ErrNoRows) {
					return nil, ErrTokenNotFound
				}
				return nil, fmt.Errorf("failed to check refresh token state: %w", checkErr)
			}
			if isRevoked {
				return nil, ErrTokenRevoked
			}
			if now.After(expiresAt) {
				return nil, ErrTokenExpired
			}
			return nil, ErrTokenNotFound
		}
		return nil, fmt.Errorf("failed to refresh token: %w", err)
	}

	// Step 3: Generate replacement access token and new refresh token
	accessToken, err := s.jwtManager.GenerateAccessToken(userID, userEmail)
	if err != nil {
		return nil, fmt.Errorf("failed to generate access token: %w", err)
	}

	newRefreshToken := s.jwtManager.GenerateRefreshToken()
	newTokenHash := auth.HashToken(newRefreshToken)

	// Persist the replacement token hash in the SAME transaction
	_, err = tx.Exec(ctx,
		`INSERT INTO refresh_tokens (id, user_id, token_hash, expires_at, created_at)
		 VALUES ($1, $2, $3, $4, $5)`,
		uuid.New(),
		userID,
		newTokenHash,
		now.Add(s.jwtManager.RefreshTokenExpiry()),
		now,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to store replacement refresh token: %w", err)
	}

	// Step 4: Commit both changes together
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("failed to commit refresh token rotation: %w", err)
	}

	tokens := &AuthTokens{
		AccessToken:  accessToken,
		RefreshToken: newRefreshToken,
		ExpiresIn:    int(s.jwtManager.AccessTokenExpiry().Seconds()),
	}

	// Record in rotation cache with grace period for concurrent requests
	s.cacheRotation(ctx, tokenHash, tokens)

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
		ExpiresIn:    int(s.jwtManager.AccessTokenExpiry().Seconds()),
	}, nil
}

// RevokeRefreshToken revokes a refresh token in the database.
func (s *UserService) RevokeRefreshToken(ctx context.Context, refreshToken string) error {
	if refreshToken == "" {
		return nil
	}
	tokenHash := auth.HashToken(refreshToken)
	_, err := s.db.Exec(ctx, "UPDATE refresh_tokens SET revoked = true WHERE token_hash = $1", tokenHash)
	return err
}

// FindOrCreateOAuthUser identifies or creates a ForgeLab user from an external OAuth identity.
// Safe account linking rules:
// 1. Existing provider identity (provider, subject) -> return linked user
// 2. New provider identity + verified matching email -> safely associate with existing user
// 3. Otherwise -> create new ForgeLab user (with nullable password_hash) and link identity
// 4. Never silently merge accounts on unverified email
func (s *UserService) FindOrCreateOAuthUser(
	ctx context.Context,
	provider string,
	subject string,
	email string,
	displayName string,
	emailVerified bool,
) (*models.User, *AuthTokens, error) {
	email = strings.ToLower(strings.TrimSpace(email))

	// 1. Check if external identity already exists
	var userID uuid.UUID
	err := s.db.QueryRow(ctx,
		`SELECT user_id FROM auth_identities WHERE provider = $1 AND provider_subject = $2`,
		provider, subject,
	).Scan(&userID)

	if err == nil {
		// Identity exists! Fetch the user
		user, err := s.GetUserByID(ctx, userID)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to load user for existing identity: %w", err)
		}
		// Update auth_identities updated_at and provider_email if changed
		_, _ = s.db.Exec(ctx,
			`UPDATE auth_identities SET provider_email = $1, email_verified = $2, updated_at = NOW()
			 WHERE provider = $3 AND provider_subject = $4`,
			email, emailVerified, provider, subject,
		)
		tokens, err := s.generateTokens(ctx, user)
		if err != nil {
			return nil, nil, err
		}
		return user, tokens, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, fmt.Errorf("failed to query auth_identities: %w", err)
	}

	// 2. Identity does NOT exist yet. Check safe account linking rules:
	// If provider email is verified and non-empty, check if a ForgeLAB user with matching email exists.
	if email != "" && emailVerified {
		var existingUser models.User
		var pwdHash *string
		err = s.db.QueryRow(ctx,
			`SELECT id, email, password_hash, display_name, created_at, updated_at
			 FROM users WHERE email = $1`,
			email,
		).Scan(&existingUser.ID, &existingUser.Email, &pwdHash, &existingUser.DisplayName, &existingUser.CreatedAt, &existingUser.UpdatedAt)

		if err == nil {
			existingUser.PasswordHash = pwdHash
			// Safely associate with existing user
			_, err = s.db.Exec(ctx,
				`INSERT INTO auth_identities (id, user_id, provider, provider_subject, provider_email, email_verified, created_at, updated_at)
				 VALUES ($1, $2, $3, $4, $5, $6, NOW(), NOW())`,
				uuid.New(), existingUser.ID, provider, subject, email, emailVerified,
			)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to link identity to existing account: %w", err)
			}

			tokens, err := s.generateTokens(ctx, &existingUser)
			if err != nil {
				return nil, nil, err
			}
			slog.Info("linked oauth identity to existing user", "user_id", existingUser.ID, "provider", provider, "email", email)
			return &existingUser, tokens, nil
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, fmt.Errorf("failed to query user by email: %w", err)
		}
	} else if email != "" && !emailVerified {
		// Never silently merge on unverified email!
		var count int
		_ = s.db.QueryRow(ctx, "SELECT count(1) FROM users WHERE email = $1", email).Scan(&count)
		if count > 0 {
			return nil, nil, errors.New("cannot link account: OAuth provider email is unverified and matches existing account")
		}
	}

	// 3. Create a new ForgeLAB user and link identity
	newUser := &models.User{
		ID:          uuid.New(),
		Email:       email,
		DisplayName: displayName,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to start transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx,
		`INSERT INTO users (id, email, password_hash, display_name, created_at, updated_at)
		 VALUES ($1, $2, NULL, $3, $4, $5)`,
		newUser.ID, newUser.Email, newUser.DisplayName, newUser.CreatedAt, newUser.UpdatedAt,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to insert new user: %w", err)
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO auth_identities (id, user_id, provider, provider_subject, provider_email, email_verified, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, NOW(), NOW())`,
		uuid.New(), newUser.ID, provider, subject, email, emailVerified,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to insert auth_identity: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, nil, fmt.Errorf("failed to commit user creation: %w", err)
	}

	tokens, err := s.generateTokens(ctx, newUser)
	if err != nil {
		return nil, nil, err
	}

	slog.Info("created new user via oauth", "user_id", newUser.ID, "provider", provider, "email", email)
	return newUser, tokens, nil
}
