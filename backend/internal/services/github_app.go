package services

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/forgelab/backend/internal/config"
	"github.com/forgelab/backend/internal/models"
)

var (
	ErrGitHubAppNotConfigured   = errors.New("github app is not configured on the server")
	ErrGitHubInstallationFailed = errors.New("failed to retrieve or verify github app installation")
)

// GitHubAppManager handles GitHub App authentication, installation lifecycle, and token minting.
type GitHubAppManager struct {
	cfg        config.GitHubAppConfig
	db         *pgxpool.Pool
	httpClient *http.Client

	rsaKeyOnce sync.Once
	rsaKey     *rsa.PrivateKey
	rsaKeyErr  error

	// tokenCache caches installation access tokens: installation_id -> cachedToken
	tokenMu    sync.RWMutex
	tokenCache map[int64]*cachedInstallationToken
}

type cachedInstallationToken struct {
	token     string
	expiresAt time.Time
}

// NewGitHubAppManager creates a new GitHubAppManager.
func NewGitHubAppManager(cfg config.GitHubAppConfig, db *pgxpool.Pool, httpClient *http.Client) *GitHubAppManager {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &GitHubAppManager{
		cfg:        cfg,
		db:         db,
		httpClient: httpClient,
		tokenCache: make(map[int64]*cachedInstallationToken),
	}
}

// IsConfigured returns true if GitHub App credentials are set.
func (m *GitHubAppManager) IsConfigured() bool {
	return m.cfg.IsConfigured()
}

// AppSlug returns the configured GitHub App slug.
func (m *GitHubAppManager) AppSlug() string {
	return m.cfg.Slug
}

// getPrivateKey parses and caches the RSA private key.
func (m *GitHubAppManager) getPrivateKey() (*rsa.PrivateKey, error) {
	m.rsaKeyOnce.Do(func() {
		var pemBytes []byte
		if strings.TrimSpace(m.cfg.PrivateKey) != "" {
			pemBytes = []byte(m.cfg.PrivateKey)
		} else if strings.TrimSpace(m.cfg.PrivateKeyPath) != "" {
			b, err := os.ReadFile(m.cfg.PrivateKeyPath)
			if err != nil {
				m.rsaKeyErr = fmt.Errorf("failed to read github app private key file: %w", err)
				return
			}
			pemBytes = b
		} else {
			m.rsaKeyErr = ErrGitHubAppNotConfigured
			return
		}

		key, err := jwt.ParseRSAPrivateKeyFromPEM(pemBytes)
		if err != nil {
			m.rsaKeyErr = fmt.Errorf("failed to parse github app rsa private key: %w", err)
			return
		}
		m.rsaKey = key
	})
	return m.rsaKey, m.rsaKeyErr
}

// GenerateAppJWT creates a signed RS256 JWT for authenticating as the GitHub App.
func (m *GitHubAppManager) GenerateAppJWT() (string, error) {
	if !m.IsConfigured() {
		return "", ErrGitHubAppNotConfigured
	}
	key, err := m.getPrivateKey()
	if err != nil {
		return "", err
	}

	now := time.Now()
	claims := jwt.MapClaims{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": strings.TrimSpace(m.cfg.AppID),
	}

	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	return tok.SignedString(key)
}

// GetInstallationDetails fetches metadata about an installation from GitHub API.
func (m *GitHubAppManager) GetInstallationDetails(ctx context.Context, installationID int64) (*models.GitHubAppInstallation, error) {
	jwtToken, err := m.GenerateAppJWT()
	if err != nil {
		return nil, err
	}

	apiURL := fmt.Sprintf("https://api.github.com/app/installations/%d", installationID)
	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+jwtToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "ForgeLAB-App")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to call github app installations api: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("github api returned status %d for installation %d: %s", resp.StatusCode, installationID, string(body))
	}

	var raw struct {
		ID      int64 `json:"id"`
		Account struct {
			Login     string `json:"login"`
			ID        int64  `json:"id"`
			Type      string `json:"type"`
			AvatarURL string `json:"avatar_url"`
		} `json:"account"`
		RepositorySelection string          `json:"repository_selection"`
		Permissions         json.RawMessage `json:"permissions"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("failed to decode installation details: %w", err)
	}

	return &models.GitHubAppInstallation{
		ID:                  raw.ID,
		AccountLogin:        raw.Account.Login,
		AccountID:           raw.Account.ID,
		AccountType:         raw.Account.Type,
		AccountAvatarURL:    raw.Account.AvatarURL,
		RepositorySelection: raw.RepositorySelection,
		Permissions:         raw.Permissions,
		CreatedAt:           time.Now(),
		UpdatedAt:           time.Now(),
	}, nil
}

// GetInstallationToken returns a valid short-lived installation access token (cached if unexpired).
func (m *GitHubAppManager) GetInstallationToken(ctx context.Context, installationID int64) (string, error) {
	m.tokenMu.RLock()
	cached, ok := m.tokenCache[installationID]
	if ok && time.Now().Before(cached.expiresAt) {
		tok := cached.token
		m.tokenMu.RUnlock()
		return tok, nil
	}
	m.tokenMu.RUnlock()

	jwtToken, err := m.GenerateAppJWT()
	if err != nil {
		return "", err
	}

	apiURL := fmt.Sprintf("https://api.github.com/app/installations/%d/access_tokens", installationID)
	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, strings.NewReader("{}"))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+jwtToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "ForgeLAB-App")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to mint installation access token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("failed to mint installation token (status %d): %s", resp.StatusCode, string(body))
	}

	var tokenResp struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", fmt.Errorf("failed to decode installation token response: %w", err)
	}

	// Cache with safety margin (5 minutes before expiration)
	safeExpiry := tokenResp.ExpiresAt.Add(-5 * time.Minute)
	if safeExpiry.Before(time.Now()) {
		safeExpiry = tokenResp.ExpiresAt
	}

	m.tokenMu.Lock()
	m.tokenCache[installationID] = &cachedInstallationToken{
		token:     tokenResp.Token,
		expiresAt: safeExpiry,
	}
	m.tokenMu.Unlock()

	return tokenResp.Token, nil
}

// SaveInstallation persists or updates an authorized installation for a user.
func (m *GitHubAppManager) SaveInstallation(ctx context.Context, userID uuid.UUID, inst *models.GitHubAppInstallation) error {
	if m.db == nil {
		return nil
	}

	permJSON := inst.Permissions
	if len(permJSON) == 0 {
		permJSON = json.RawMessage("{}")
	}

	query := `
		INSERT INTO github_app_installations (
			user_id, installation_id, account_login, account_id, account_type,
			account_avatar_url, repository_selection, permissions, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NOW(), NOW())
		ON CONFLICT (user_id, installation_id) DO UPDATE SET
			account_login = EXCLUDED.account_login,
			account_id = EXCLUDED.account_id,
			account_type = EXCLUDED.account_type,
			account_avatar_url = EXCLUDED.account_avatar_url,
			repository_selection = EXCLUDED.repository_selection,
			permissions = EXCLUDED.permissions,
			updated_at = NOW()`

	_, err := m.db.Exec(ctx, query,
		userID, inst.ID, inst.AccountLogin, inst.AccountID, inst.AccountType,
		inst.AccountAvatarURL, inst.RepositorySelection, permJSON,
	)
	if err != nil {
		return fmt.Errorf("failed to save github app installation: %w", err)
	}
	return nil
}

// ListUserInstallations loads all GitHub App installations linked to the user.
func (m *GitHubAppManager) ListUserInstallations(ctx context.Context, userID uuid.UUID) ([]models.GitHubAppInstallation, error) {
	if m.db == nil {
		return nil, nil
	}

	query := `
		SELECT installation_id, account_login, account_id, account_type,
		       account_avatar_url, repository_selection, permissions, created_at, updated_at
		FROM github_app_installations
		WHERE user_id = $1
		ORDER BY account_login ASC`

	rows, err := m.db.Query(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to query github app installations: %w", err)
	}
	defer rows.Close()

	var result []models.GitHubAppInstallation
	for rows.Next() {
		var inst models.GitHubAppInstallation
		var perms []byte
		if err := rows.Scan(
			&inst.ID, &inst.AccountLogin, &inst.AccountID, &inst.AccountType,
			&inst.AccountAvatarURL, &inst.RepositorySelection, &perms,
			&inst.CreatedAt, &inst.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan installation row: %w", err)
		}
		if len(perms) > 0 {
			inst.Permissions = perms
		} else {
			inst.Permissions = json.RawMessage("{}")
		}
		result = append(result, inst)
	}
	return result, nil
}

// DeleteInstallation removes an installation record for a user.
func (m *GitHubAppManager) DeleteInstallation(ctx context.Context, userID uuid.UUID, installationID int64) error {
	if m.db == nil {
		return nil
	}
	_, err := m.db.Exec(ctx, "DELETE FROM github_app_installations WHERE user_id = $1 AND installation_id = $2", userID, installationID)
	return err
}

// ListInstallationRepositories lists repositories granted to an installation.
func (m *GitHubAppManager) ListInstallationRepositories(ctx context.Context, installationID int64, page, perPage int) ([]GitHubRepo, bool, error) {
	token, err := m.GetInstallationToken(ctx, installationID)
	if err != nil {
		return nil, false, err
	}

	if page < 1 {
		page = 1
	}
	if perPage < 1 || perPage > 100 {
		perPage = 30
	}

	apiURL := fmt.Sprintf("https://api.github.com/installation/repositories?page=%d&per_page=%d", page, perPage)
	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "ForgeLAB-App")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("failed to fetch installation repositories: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("github api returned status %d for installation repositories", resp.StatusCode)
	}

	var raw struct {
		TotalCount   int `json:"total_count"`
		Repositories []struct {
			ID            int64  `json:"id"`
			Name          string `json:"name"`
			FullName      string `json:"full_name"`
			Private       bool   `json:"private"`
			DefaultBranch string `json:"default_branch"`
			Description   string `json:"description"`
			HTMLURL       string `json:"html_url"`
			UpdatedAt     string `json:"updated_at"`
			Owner         struct {
				Login string `json:"login"`
			} `json:"owner"`
		} `json:"repositories"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, false, fmt.Errorf("failed to decode installation repositories: %w", err)
	}

	repos := make([]GitHubRepo, len(raw.Repositories))
	for i, r := range raw.Repositories {
		repos[i] = GitHubRepo{
			ID:            r.ID,
			Name:          r.Name,
			FullName:      r.FullName,
			Owner:         r.Owner.Login,
			Private:       r.Private,
			DefaultBranch: r.DefaultBranch,
			Description:   r.Description,
			HTMLURL:       r.HTMLURL,
			UpdatedAt:     r.UpdatedAt,
		}
	}

	hasMore := strings.Contains(resp.Header.Get("Link"), `rel="next"`) || (page*perPage < raw.TotalCount)
	return repos, hasMore, nil
}

// FindTokenForRepo searches the user's GitHub App installations for one that has access to owner/repo.
// Returns the installation access token and the installation ID if found.
func (m *GitHubAppManager) FindTokenForRepo(ctx context.Context, userID uuid.UUID, owner, repo string) (string, int64, error) {
	installations, err := m.ListUserInstallations(ctx, userID)
	if err != nil || len(installations) == 0 {
		return "", 0, ErrGitHubNotConnected
	}

	// Try installations matching account login first
	matchingInstallations := make([]models.GitHubAppInstallation, 0, len(installations))
	for _, inst := range installations {
		if strings.EqualFold(inst.AccountLogin, owner) {
			matchingInstallations = append([]models.GitHubAppInstallation{inst}, matchingInstallations...)
		} else {
			matchingInstallations = append(matchingInstallations, inst)
		}
	}

	for _, inst := range matchingInstallations {
		tok, err := m.GetInstallationToken(ctx, inst.ID)
		if err != nil {
			continue
		}

		// Verify repo access with this installation token
		apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s", url.PathEscape(owner), url.PathEscape(repo))
		req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
		if err != nil {
			continue
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("User-Agent", "ForgeLAB-App")

		resp, err := m.httpClient.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return tok, inst.ID, nil
			}
		}
	}

	return "", 0, ErrGitHubRepoNotFound
}
