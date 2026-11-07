package services

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/forgelab/backend/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
)

// GetStatus checks whether the user has granted repository-access OAuth permissions or installed GitHub App.
func (s *GitHubService) GetStatus(ctx context.Context, userID uuid.UUID) (*GitHubStatus, error) {
	if s.statusGetter != nil {
		return s.statusGetter(ctx, userID)
	}

	appConfigured := s.appManager != nil && s.appManager.IsConfigured()
	appSlug := ""
	if s.appManager != nil {
		appSlug = s.appManager.AppSlug()
	}

	// 1. Check if user has active GitHub App installations
	var installations []models.GitHubAppInstallation
	if s.appManager != nil {
		if insts, err := s.appManager.ListUserInstallations(ctx, userID); err == nil && len(insts) > 0 {
			installations = insts
		}
	}

	if len(installations) > 0 {
		return &GitHubStatus{
			Connected:     true,
			Username:      installations[0].AccountLogin,
			NeedsReauth:   false,
			AuthType:      "app",
			AppConfigured: appConfigured,
			AppSlug:       appSlug,
			Installations: installations,
		}, nil
	}

	if s.db == nil {
		return &GitHubStatus{
			Connected:     false,
			AuthType:      "none",
			AppConfigured: appConfigured,
			AppSlug:       appSlug,
		}, nil
	}

	// 2. Check if user has connected OAuth or PAT in github_integrations
	var (
		encryptedToken []byte
		username       string
		scope          string
		authTypeCol    string
		updatedAt      time.Time
	)

	err := s.db.QueryRow(ctx,
		`SELECT encrypted_access_token, github_username, scope, COALESCE(auth_type, 'oauth'), updated_at
		 FROM github_integrations WHERE user_id = $1`,
		userID,
	).Scan(&encryptedToken, &username, &scope, &authTypeCol, &updatedAt)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return &GitHubStatus{
				Connected:     false,
				AuthType:      "none",
				AppConfigured: appConfigured,
				AppSlug:       appSlug,
			}, nil
		}
		return nil, fmt.Errorf("failed to query github integration: %w", err)
	}

	// Verify token can be decrypted
	plainToken, err := s.encryptor.Decrypt(encryptedToken)
	if err != nil {
		slog.Error("failed to decrypt github access token", "user_id", userID, "error", err)
		return &GitHubStatus{
			Connected:     false,
			AuthType:      "none",
			AppConfigured: appConfigured,
			AppSlug:       appSlug,
		}, nil
	}

	plainTokenStr := string(plainToken)
	scopes := parseOAuthScopes(scope)
	hasRepoScope := false
	authType := authTypeCol

	// Detect token capabilities: GitHub App user tokens (ghu_) and PATs
	if strings.HasPrefix(plainTokenStr, "ghu_") || authType == "app" {
		authType = "app"
		hasRepoScope = true
	} else if strings.HasPrefix(plainTokenStr, "github_pat_") {
		authType = "pat"
		for _, sc := range scopes {
			if sc == "repo" || sc == "fine-grained" {
				hasRepoScope = true
				break
			}
		}
	} else if strings.HasPrefix(plainTokenStr, "ghp_") || authType == "pat" {
		authType = "pat"
		for _, sc := range scopes {
			if sc == "repo" {
				hasRepoScope = true
				break
			}
		}
	} else {
		for _, sc := range scopes {
			if sc == "repo" {
				hasRepoScope = true
				break
			}
		}
	}

	if hasRepoScope && len(scopes) == 0 {
		if authType == "app" {
			scopes = []string{"app", "repo"}
		} else {
			scopes = []string{"pat", "repo"}
		}
	}

	reconnectGuidance := ""
	if appConfigured {
		reconnectGuidance = "GitHub App is available on this server. Install the GitHub App for fine-grained repository access without personal OAuth tokens."
	}

	return &GitHubStatus{
		Connected:         true,
		Username:          username,
		Scopes:            scopes,
		NeedsReauth:       !hasRepoScope,
		AuthType:          authType,
		UpdatedAt:         &updatedAt,
		AppConfigured:     appConfigured,
		AppSlug:           appSlug,
		ReconnectGuidance: reconnectGuidance,
	}, nil
}

type GitHubRepoStateRecord struct {
	UserID  string    `json:"user_id"`
	Nonce   string    `json:"nonce"`
	Expires time.Time `json:"expires"`
}

var consumeGitHubRepoStateScript = redis.NewScript(`
	local val = redis.call('GET', KEYS[1])
	if val then
		redis.call('DEL', KEYS[1])
	end
	return val
`)

// GetConnectURL initiates repository-permission authorization flow.
func (s *GitHubService) GetConnectURL(ctx context.Context, userID uuid.UUID) (authURL string, nonce string, err error) {
	if !s.githubCfg.IsConfigured() {
		return "", "", ErrProviderNotConfigured
	}

	stateBytes := make([]byte, 32)
	if _, err := rand.Read(stateBytes); err != nil {
		return "", "", fmt.Errorf("failed to generate random state: %w", err)
	}
	state := hex.EncodeToString(stateBytes)

	nonceBytes := make([]byte, 32)
	if _, err := rand.Read(nonceBytes); err != nil {
		return "", "", fmt.Errorf("failed to generate random nonce: %w", err)
	}
	nonce = hex.EncodeToString(nonceBytes)

	record := GitHubRepoStateRecord{
		UserID:  userID.String(),
		Nonce:   nonce,
		Expires: time.Now().Add(15 * time.Minute),
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return "", "", err
	}

	key := "forgelab:github_repo_state:" + state

	if s.redis != nil {
		if err := s.redis.Set(ctx, key, string(payload), 15*time.Minute).Err(); err != nil {
			return "", "", fmt.Errorf("failed to store github repo state in redis: %w", err)
		}
	} else {
		s.fallbackMem.Store(key, record)
	}

	params := url.Values{}
	params.Set("client_id", s.githubCfg.ClientID)
	redirectURL := s.githubCfg.RepoRedirectURL
	if redirectURL == "" {
		redirectURL = "http://localhost:3000/api/integrations/github/callback"
	}
	params.Set("redirect_uri", redirectURL)
	params.Set("scope", "repo read:user user:email")
	params.Set("prompt", "consent")
	params.Set("state", state)

	return "https://github.com/login/oauth/authorize?" + params.Encode(), nonce, nil
}

// HandleCallback completes repository-permission authorization flow.
func (s *GitHubService) HandleCallback(ctx context.Context, code, state, nonce string) (uuid.UUID, error) {
	if !s.githubCfg.IsConfigured() {
		return uuid.Nil, ErrProviderNotConfigured
	}
	if state == "" || nonce == "" {
		return uuid.Nil, ErrInvalidOAuthState
	}

	key := "forgelab:github_repo_state:" + state
	var record GitHubRepoStateRecord

	if s.redis != nil {
		res, err := consumeGitHubRepoStateScript.Run(ctx, s.redis, []string{key}).Result()
		if err != nil {
			if errors.Is(err, redis.Nil) {
				return uuid.Nil, ErrInvalidOAuthState
			}
			return uuid.Nil, fmt.Errorf("failed to query github repo state: %w", err)
		}
		rawStr, ok := res.(string)
		if !ok || rawStr == "" {
			return uuid.Nil, ErrInvalidOAuthState
		}
		if err := json.Unmarshal([]byte(rawStr), &record); err != nil {
			return uuid.Nil, ErrInvalidOAuthState
		}
	} else {
		raw, ok := s.fallbackMem.LoadAndDelete(key)
		if !ok {
			return uuid.Nil, ErrInvalidOAuthState
		}
		rec, ok := raw.(GitHubRepoStateRecord)
		if !ok || time.Now().After(rec.Expires) {
			return uuid.Nil, ErrInvalidOAuthState
		}
		record = rec
	}

	if record.Nonce != nonce {
		return uuid.Nil, ErrInvalidOAuthState
	}

	userID, err := uuid.Parse(record.UserID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid user id in state: %w", err)
	}

	redirectURL := s.githubCfg.RepoRedirectURL
	if redirectURL == "" {
		redirectURL = "http://localhost:3000/api/integrations/github/callback"
	}

	// 1. Exchange code for access token
	tokenBody, _ := json.Marshal(map[string]string{
		"client_id":     s.githubCfg.ClientID,
		"client_secret": s.githubCfg.ClientSecret,
		"code":          code,
		"redirect_uri":  redirectURL,
	})

	req, err := http.NewRequestWithContext(ctx, "POST", "https://github.com/login/oauth/access_token", strings.NewReader(string(tokenBody)))
	if err != nil {
		return uuid.Nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return uuid.Nil, fmt.Errorf("github token exchange failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return uuid.Nil, fmt.Errorf("%w: GitHub token endpoint returned HTTP %d", ErrCodeExchangeFailed, resp.StatusCode)
	}

	var tokenRes struct {
		AccessToken      string `json:"access_token"`
		Scope            string `json:"scope"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
		ErrorURI         string `json:"error_uri"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenRes); err != nil {
		return uuid.Nil, fmt.Errorf("%w: failed to decode GitHub token response: %v", ErrCodeExchangeFailed, err)
	}
	if tokenRes.Error != "" {
		return uuid.Nil, fmt.Errorf("%w: GitHub returned '%s' (%s). Verify that GITHUB_REPO_REDIRECT_URL in .env matches the Authorization callback URL in your GitHub OAuth App settings (expected callback: %s)",
			ErrCodeExchangeFailed, tokenRes.Error, tokenRes.ErrorDescription, redirectURL)
	}
	if tokenRes.AccessToken == "" {
		return uuid.Nil, fmt.Errorf("%w: empty access token returned by GitHub", ErrCodeExchangeFailed)
	}

	// 2. Fetch authenticated GitHub user identity
	userReq, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/user", nil)
	if err != nil {
		return uuid.Nil, err
	}
	userReq.Header.Set("Authorization", "Bearer "+tokenRes.AccessToken)
	userReq.Header.Set("Accept", "application/vnd.github+json")
	userReq.Header.Set("User-Agent", "ForgeLAB-App")

	userResp, err := s.httpClient.Do(userReq)
	if err != nil {
		return uuid.Nil, fmt.Errorf("fetching github user profile failed: %w", err)
	}
	defer userResp.Body.Close()

	if userResp.StatusCode != http.StatusOK {
		return uuid.Nil, ErrUserInfoFailed
	}

	var ghUser struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	}
	if err := json.NewDecoder(userResp.Body).Decode(&ghUser); err != nil || ghUser.Login == "" {
		return uuid.Nil, ErrUserInfoFailed
	}

	// 3. Encrypt access token at rest using AES-256-GCM
	encryptedToken, err := s.encryptor.Encrypt([]byte(tokenRes.AccessToken))
	if err != nil {
		return uuid.Nil, fmt.Errorf("failed to encrypt github token: %w", err)
	}

	ghUserIDStr := fmt.Sprintf("%d", ghUser.ID)
	now := time.Now()

	authType := "oauth"
	storedScope := strings.TrimSpace(tokenRes.Scope)
	if storedScope == "" {
		storedScope = strings.TrimSpace(userResp.Header.Get("X-OAuth-Scopes"))
	}

	if strings.HasPrefix(tokenRes.AccessToken, "ghu_") {
		authType = "app"
		if storedScope == "" {
			storedScope = "repo read:user user:email"
		}
	} else if strings.HasPrefix(tokenRes.AccessToken, "github_pat_") || strings.HasPrefix(tokenRes.AccessToken, "ghp_") {
		authType = "pat"
		if storedScope == "" {
			storedScope = "pat repo"
		}
	} else if storedScope == "" {
		// Probe whether OAuth token has private repository access.
		// Note: accessing /user/repos without visibility=private will succeed (HTTP 200) even for
		// tokens with only read:user or public_repo permissions. We specifically test private visibility.
		repoCheckReq, _ := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/user/repos?visibility=private&per_page=1", nil)
		repoCheckReq.Header.Set("Authorization", "Bearer "+tokenRes.AccessToken)
		repoCheckReq.Header.Set("Accept", "application/vnd.github+json")
		repoCheckReq.Header.Set("User-Agent", "ForgeLAB-App")
		if repoCheckResp, checkErr := s.httpClient.Do(repoCheckReq); checkErr == nil {
			if repoCheckResp.StatusCode == http.StatusOK {
				if sc := strings.TrimSpace(repoCheckResp.Header.Get("X-OAuth-Scopes")); sc != "" {
					storedScope = sc
				} else {
					storedScope = "repo read:user user:email"
				}
			} else {
				if sc := strings.TrimSpace(repoCheckResp.Header.Get("X-OAuth-Scopes")); sc != "" {
					storedScope = sc
				} else {
					storedScope = "read:user user:email"
				}
			}
			repoCheckResp.Body.Close()
		}
	}

	// 4. Upsert into github_integrations
	_, err = s.db.Exec(ctx,
		`INSERT INTO github_integrations (user_id, encrypted_access_token, github_user_id, github_username, scope, auth_type, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 ON CONFLICT (user_id) DO UPDATE SET
		   encrypted_access_token = EXCLUDED.encrypted_access_token,
		   github_user_id = EXCLUDED.github_user_id,
		   github_username = EXCLUDED.github_username,
		   scope = EXCLUDED.scope,
		   auth_type = EXCLUDED.auth_type,
		   updated_at = EXCLUDED.updated_at`,
		userID, encryptedToken, ghUserIDStr, ghUser.Login, storedScope, authType, now, now,
	)
	if err != nil {
		return uuid.Nil, fmt.Errorf("failed to save github integration: %w", err)
	}

	slog.Info("github repository integration authorized", "user_id", userID, "github_login", ghUser.Login)
	return userID, nil
}

// Disconnect revokes repository integration by removing the encrypted credential.
func (s *GitHubService) Disconnect(ctx context.Context, userID uuid.UUID) error {
	if s.db == nil {
		return nil
	}
	_, err := s.db.Exec(ctx, "DELETE FROM github_integrations WHERE user_id = $1", userID)
	if err != nil {
		return fmt.Errorf("failed to delete github integration: %w", err)
	}
	slog.Info("github repository integration disconnected", "user_id", userID)
	return nil
}

// ConnectWithPAT verifies and encrypts a GitHub Personal Access Token (classic or fine-grained) for the user.
func (s *GitHubService) ConnectWithPAT(ctx context.Context, userID uuid.UUID, patToken string) (*GitHubStatus, error) {
	cleanToken := strings.TrimSpace(patToken)
	if cleanToken == "" {
		return nil, errors.New("personal access token cannot be empty")
	}

	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/user", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cleanToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "ForgeLAB-App")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to verify GitHub token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, errors.New("invalid or expired GitHub token. Please verify token permissions.")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}

	var ghUser struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ghUser); err != nil || ghUser.Login == "" {
		return nil, errors.New("failed to retrieve GitHub user profile with token")
	}

	// Verify actual private repository access to ensure the token has sufficient permissions
	repoReq, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/user/repos?visibility=private&per_page=1", nil)
	if err != nil {
		return nil, err
	}
	repoReq.Header.Set("Authorization", "Bearer "+cleanToken)
	repoReq.Header.Set("Accept", "application/vnd.github+json")
	repoReq.Header.Set("User-Agent", "ForgeLAB-App")

	repoResp, err := s.httpClient.Do(repoReq)
	if err != nil {
		return nil, fmt.Errorf("failed to verify GitHub repository access: %w", err)
	}
	defer repoResp.Body.Close()

	if repoResp.StatusCode == http.StatusUnauthorized || repoResp.StatusCode == http.StatusForbidden || repoResp.StatusCode == http.StatusUnprocessableEntity {
		return nil, errors.New("invalid GitHub token permissions: private repository access is not authorized. Please grant repository permissions ('repo' scope for classic tokens or 'Contents' read access for fine-grained tokens).")
	}
	if repoResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned status %d when verifying repository access", repoResp.StatusCode)
	}

	scopeHeader := resp.Header.Get("X-OAuth-Scopes")
	if scopeHeader == "" {
		scopeHeader = repoResp.Header.Get("X-OAuth-Scopes")
	}
	var savedScope string
	if scopeHeader != "" {
		savedScope = scopeHeader + ",pat"
	} else {
		savedScope = "pat,fine-grained,repo"
	}

	encryptedToken, err := s.encryptor.Encrypt([]byte(cleanToken))
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt GitHub token: %w", err)
	}

	now := time.Now()
	ghUserIDStr := fmt.Sprintf("%d", ghUser.ID)

	_, err = s.db.Exec(ctx,
		`INSERT INTO github_integrations (user_id, encrypted_access_token, github_user_id, github_username, scope, auth_type, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 ON CONFLICT (user_id) DO UPDATE SET
		   encrypted_access_token = EXCLUDED.encrypted_access_token,
		   github_user_id = EXCLUDED.github_user_id,
		   github_username = EXCLUDED.github_username,
		   scope = EXCLUDED.scope,
		   auth_type = EXCLUDED.auth_type,
		   updated_at = EXCLUDED.updated_at`,
		userID, encryptedToken, ghUserIDStr, ghUser.Login, savedScope, "pat", now, now,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to save GitHub integration: %w", err)
	}

	slog.Info("github personal access token connected successfully", "user_id", userID, "username", ghUser.Login)
	return &GitHubStatus{
		Connected:   true,
		Username:    ghUser.Login,
		Scopes:      parseOAuthScopes(savedScope),
		NeedsReauth: false,
		AuthType:    "pat",
		UpdatedAt:   &now,
	}, nil
}

// getDecryptedTokenAndScope retrieves and decrypts the GitHub access token along with authorized OAuth scopes.
func (s *GitHubService) getDecryptedTokenAndScope(ctx context.Context, userID uuid.UUID) (string, string, error) {
	if s.tokenGetter != nil {
		return s.tokenGetter(ctx, userID)
	}
	if s.db == nil {
		return "", "", ErrGitHubNotConnected
	}
	var (
		encryptedToken []byte
		scope          string
		authType       string
	)
	err := s.db.QueryRow(ctx, "SELECT encrypted_access_token, scope, COALESCE(auth_type, 'oauth') FROM github_integrations WHERE user_id = $1", userID).Scan(&encryptedToken, &scope, &authType)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", "", ErrGitHubNotConnected
		}
		return "", "", fmt.Errorf("failed to query github token: %w", err)
	}

	plainToken, err := s.encryptor.Decrypt(encryptedToken)
	if err != nil {
		return "", "", fmt.Errorf("failed to decrypt github token: %w", err)
	}
	tokenStr := string(plainToken)

	// If token is a GitHub App user token (ghu_) or fine-grained PAT, or auth_type is app,
	// ensure scope reflects repo capability so downstream callers do not falsely reject it.
	if strings.HasPrefix(tokenStr, "ghu_") || strings.HasPrefix(tokenStr, "github_pat_") || authType == "app" {
		scopes := parseOAuthScopes(scope)
		hasRepo := false
		for _, sc := range scopes {
			if sc == "repo" {
				hasRepo = true
				break
			}
		}
		if !hasRepo {
			if scope == "" {
				scope = "repo"
			} else {
				scope = "repo " + scope
			}
		}
	}

	return tokenStr, scope, nil
}

// getDecryptedToken retrieves and decrypts the GitHub access token for a user.
func (s *GitHubService) getDecryptedToken(ctx context.Context, userID uuid.UUID) (string, error) {
	tok, _, err := s.getDecryptedTokenAndScope(ctx, userID)
	return tok, err
}

// getDecryptedTokenForRepo finds the best access token for a given owner/repo:
// 1. Checks if a GitHub App installation for this user has access to owner/repo.
// 2. Falls back to user's decrypted OAuth or PAT token.
// 3. Returns empty string with nil error if user has no credentials (enabling public repository access).
func (s *GitHubService) getDecryptedTokenForRepo(ctx context.Context, userID uuid.UUID, owner, repo string) (string, error) {
	if s.tokenGetter != nil {
		tok, _, err := s.tokenGetter(ctx, userID)
		return tok, err
	}

	// 1. Try GitHub App installation token
	if s.appManager != nil && s.appManager.IsConfigured() {
		tok, _, err := s.appManager.FindTokenForRepo(ctx, userID, owner, repo)
		if err == nil && tok != "" {
			return tok, nil
		}
	}

	// 2. Try user-level OAuth token or PAT
	tok, err := s.getDecryptedToken(ctx, userID)
	if err == nil && tok != "" {
		return tok, nil
	}

	// 3. Return empty string with nil error (allow public access)
	return "", nil
}
