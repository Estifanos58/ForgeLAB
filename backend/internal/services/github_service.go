package services

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/forgelab/backend/internal/config"
	"github.com/forgelab/backend/internal/crypto"
	"github.com/forgelab/backend/internal/models"
)

var (
	ErrGitHubNotConnected = errors.New("github repository access is not authorized")
	ErrGitHubNeedsReauth  = errors.New("github connection requires re-authorization for repository access")
	ErrGitHubSSORequired  = errors.New("github organization saml sso authorization required")
	ErrGitHubRateLimited  = errors.New("github api rate limit exceeded")
	ErrGitHubRepoNotFound = errors.New("github repository not found or access denied")

	ErrGitHubArchiveTooLarge   = errors.New("github repository archive exceeds maximum allowed size (100MB)")
	ErrGitHubTooManyFiles      = errors.New("github repository archive exceeds maximum allowed file count (10,000)")
	ErrGitHubFileTooLarge      = errors.New("github repository archive contains a file exceeding maximum single file size (50MB)")
	ErrGitHubMaliciousArchive  = errors.New("malicious github repository archive detected")
	ErrGitHubExtractionTimeout = errors.New("github repository archive extraction timed out")
)

const (
	MaxGitHubCompressedBytes   int64         = 100 * 1024 * 1024 // 100MB compressed
	MaxGitHubUncompressedBytes int64         = 100 * 1024 * 1024 // 100MB total uncompressed
	MaxGitHubArchiveFiles      int           = 10000             // 10,000 files
	MaxGitHubSingleFileBytes   int64         = 50 * 1024 * 1024  // 50MB single file
	GitHubExtractionTimeout    time.Duration = 3 * time.Minute
)

// parseOAuthScopes splits comma- and/or whitespace-separated OAuth scopes reliably.
func parseOAuthScopes(scopeStr string) []string {
	fields := strings.FieldsFunc(scopeStr, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	})
	var result []string
	seen := make(map[string]bool)
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f != "" && !seen[f] {
			seen[f] = true
			result = append(result, f)
		}
	}
	return result
}

type GitHubRepo struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	Owner         string `json:"owner"`
	Private       bool   `json:"private"`
	DefaultBranch string `json:"default_branch"`
	Description   string `json:"description"`
	HTMLURL       string `json:"html_url"`
	UpdatedAt     string `json:"updated_at"`
}

type GitHubBranch struct {
	Name      string `json:"name"`
	CommitSHA string `json:"commit_sha"`
}

type GitHubStatus struct {
	Connected         bool                           `json:"connected"`
	Username          string                         `json:"username,omitempty"`
	Scopes            []string                       `json:"scopes,omitempty"`
	NeedsReauth       bool                           `json:"needs_reauth"`
	AuthType          string                         `json:"auth_type,omitempty"` // "app", "oauth", "pat", or "none"
	UpdatedAt         *time.Time                     `json:"updated_at,omitempty"`
	AppConfigured     bool                           `json:"app_configured"`
	AppSlug           string                         `json:"app_slug,omitempty"`
	Installations     []models.GitHubAppInstallation `json:"installations,omitempty"`
	ReconnectGuidance string                         `json:"reconnect_guidance,omitempty"`
}

type GitHubOrg struct {
	Login     string `json:"login"`
	AvatarURL string `json:"avatar_url"`
}

type GitHubService struct {
	db           *pgxpool.Pool
	encryptor    *crypto.Encryptor
	githubCfg    config.OAuthConfig
	appManager   *GitHubAppManager
	redis        *redis.Client
	httpClient   *http.Client
	fallbackMem  sync.Map
	tokenGetter  func(ctx context.Context, userID uuid.UUID) (string, string, error)
	statusGetter func(ctx context.Context, userID uuid.UUID) (*GitHubStatus, error)
}

func (s *GitHubService) SetHTTPClient(c *http.Client) {
	s.httpClient = c
}

func (s *GitHubService) SetGitHubAppManager(mgr *GitHubAppManager) {
	s.appManager = mgr
}

func (s *GitHubService) SetTokenGetterForTest(fn func(ctx context.Context, userID uuid.UUID) (string, string, error)) {
	s.tokenGetter = fn
}

func (s *GitHubService) SetStatusGetterForTest(fn func(ctx context.Context, userID uuid.UUID) (*GitHubStatus, error)) {
	s.statusGetter = fn
}

func NewGitHubService(
	db *pgxpool.Pool,
	encryptor *crypto.Encryptor,
	githubCfg config.OAuthConfig,
	redisClient *redis.Client,
) *GitHubService {
	return &GitHubService{
		db:         db,
		encryptor:  encryptor,
		githubCfg:  githubCfg,
		redis:      redisClient,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}
