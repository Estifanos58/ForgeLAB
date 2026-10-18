package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/forgelab/backend/internal/crypto"
)

// Config holds all application configuration.
type Config struct {
	Server     ServerConfig
	Database   DatabaseConfig
	Redis      RedisConfig
	JWT        JWTConfig
	Encryption EncryptionConfig
	Docker     DockerConfig
	Log        LogConfig
	App        AppConfig
	Google     OAuthConfig
	GitHub     OAuthConfig
	GitHubApp  GitHubAppConfig
	RateLimit  RateLimitConfig
}


// RateLimitConfig holds throttling limits for sensitive API endpoints and WebSockets.
type RateLimitConfig struct {
	Enabled             bool
	AuthLimit           int // requests per minute
	SourceLimit         int // requests per minute
	DeployLimit         int // requests per minute
	WSConnLimit         int // requests per minute
	WSSubscriptionLimit int // requests per minute
	TrustedProxies      []string
}

// AppConfig holds application, CORS, and cookie settings.
type AppConfig struct {
	Environment        string
	FrontendURL        string
	CORSAllowedOrigins string
	CookieSecure       bool
	AllowGuestMode     bool
}

// AllowedOriginsList returns a slice of allowed origins parsed from CORSAllowedOrigins and FrontendURL.
func (a AppConfig) AllowedOriginsList() []string {
	origins := make(map[string]bool)
	if a.FrontendURL != "" {
		origins[strings.TrimRight(strings.TrimSpace(a.FrontendURL), "/")] = true
	}
	if a.CORSAllowedOrigins != "" {
		for _, o := range strings.Split(a.CORSAllowedOrigins, ",") {
			trimmed := strings.TrimRight(strings.TrimSpace(o), "/")
			if trimmed != "" {
				origins[trimmed] = true
			}
		}
	}
	// Always include loopback origins and local agent in local development
	envLower := strings.ToLower(a.Environment)
	isDev := envLower == "development" || envLower == "dev" || envLower == "local" || envLower == "test" || envLower == ""
	if isDev {
		origins["http://localhost:3000"] = true
		origins["http://127.0.0.1:3000"] = true
		origins["http://localhost:8080"] = true
		origins["http://127.0.0.1:8080"] = true
		origins["http://localhost:4142"] = true
		origins["http://127.0.0.1:4142"] = true
	}

	result := make([]string, 0, len(origins))
	for o := range origins {
		result = append(result, o)
	}
	return result
}

// OAuthConfig holds OAuth provider settings.
type OAuthConfig struct {
	ClientID        string
	ClientSecret    string
	RedirectURL     string
	RepoRedirectURL string
}

// IsConfigured returns true if the OAuth provider has non-placeholder credentials.
func (o OAuthConfig) IsConfigured() bool {
	if o.ClientID == "" || o.ClientSecret == "" {
		return false
	}
	// Check for example/placeholder values
	if o.ClientID == "example-google-client-id" || o.ClientID == "example-github-client-id" ||
		o.ClientSecret == "example-google-client-secret" || o.ClientSecret == "example-github-client-secret" {
		return false
	}
	return true
}

// GitHubAppConfig holds GitHub App settings for repository access and installation tokens.
type GitHubAppConfig struct {
	AppID          string
	PrivateKey     string
	PrivateKeyPath string
	Slug           string
	WebhookSecret  string
	ClientID       string
	ClientSecret   string
}

// IsConfigured returns true if the GitHub App has an AppID and PrivateKey configured.
func (g GitHubAppConfig) IsConfigured() bool {
	if strings.TrimSpace(g.AppID) == "" {
		return false
	}
	if g.AppID == "example-github-app-id" {
		return false
	}
	return strings.TrimSpace(g.PrivateKey) != "" || strings.TrimSpace(g.PrivateKeyPath) != ""
}


// ServerConfig holds HTTP server settings.
type ServerConfig struct {
	Host string
	Port int
}

// DatabaseConfig holds PostgreSQL connection settings.
type DatabaseConfig struct {
	URL string
}

// RedisConfig holds Redis connection settings.
type RedisConfig struct {
	URL string
}

// JWTConfig holds JWT authentication settings.
type JWTConfig struct {
	Secret             string
	AccessTokenExpiry  time.Duration
	RefreshTokenExpiry time.Duration
}

// EncryptionConfig holds secret encryption settings.
type EncryptionConfig struct {
	Key string
}

// DockerConfig holds Docker-related settings.
type DockerConfig struct {
	Host                string
	WorkDir             string
	SourcesDir          string
	AllowedSourceRoots  string
	HostSourceRoot      string
	ContainerSourceRoot string
	LocalBuildMode      string
	MaxConcurrentBuilds int           // global cap on concurrent Docker builds/deploys
	QueueWorkerCount    int           // number of queue worker goroutines
	PruneInterval       time.Duration // interval between image & workspace GC runs
	PruneFilterLabel    string        // optional label filter for scoped image pruning
}

// LogConfig holds logging and log retention settings.
type LogConfig struct {
	Level                string
	Format               string
	RetentionDays        int           // number of days to keep logs (0 = disable age-based retention)
	MaxLogsPerDeployment int           // maximum logs kept per deployment (0 = unlimited)
	CleanupInterval      time.Duration // interval between log retention cleanup runs
}

// Load reads configuration from environment variables.
func Load() (*Config, error) {
	port, err := strconv.Atoi(getEnv("SERVER_PORT", "8080"))
	if err != nil {
		return nil, fmt.Errorf("invalid SERVER_PORT: %w", err)
	}

	accessExpiry, err := strconv.Atoi(getEnv("JWT_ACCESS_TOKEN_EXPIRY", "15"))
	if err != nil {
		return nil, fmt.Errorf("invalid JWT_ACCESS_TOKEN_EXPIRY: %w", err)
	}

	refreshExpiry, err := strconv.Atoi(getEnv("JWT_REFRESH_TOKEN_EXPIRY", "7"))
	if err != nil {
		return nil, fmt.Errorf("invalid JWT_REFRESH_TOKEN_EXPIRY: %w", err)
	}

	jwtSecret := getEnv("JWT_SECRET", "")
	envMode := strings.ToLower(getEnv("APP_ENV", getEnv("FORGELAB_ENV", getEnv("ENV", "development"))))
	isDev := envMode == "development" || envMode == "dev" || envMode == "local" || envMode == "test"

	// 1. Validate JWT Secret
	if err := validateJWTSecret(jwtSecret, isDev, envMode); err != nil {
		return nil, err
	}

	// 2. Validate Database URL & credentials
	if !isDev {
		if _, ok := os.LookupEnv("DATABASE_URL"); !ok {
			return nil, fmt.Errorf("%s configuration error: default development database password (forgelab_dev_password) is not allowed in %s mode; DATABASE_URL must be explicitly configured", envMode, envMode)
		}
	}
	dbURL := getEnv("DATABASE_URL", "postgres://forgelab:forgelab_dev_password@localhost:5432/forgelab?sslmode=disable")
	if err := validateDatabaseURL(dbURL, isDev, envMode); err != nil {
		return nil, err
	}

	// 3. Validate Redis URL if configured
	redisURL := getEnv("REDIS_URL", "redis://localhost:6379/0")
	if err := validateRedisURL(redisURL); err != nil {
		return nil, err
	}

	// 4. Validate Encryption Key
	encKey := getEnv("FORGELAB_ENCRYPTION_KEY", "dGhpcy1pcy1hLWRldi1rZXktY2hhbmdlLWluLXByb2Q=")
	if _, err := crypto.ParseKey(encKey); err != nil {
		return nil, fmt.Errorf("invalid FORGELAB_ENCRYPTION_KEY: %w", err)
	}

	// 5. Validate Trusted Proxies
	rawTrustedProxies := getEnv("RATE_LIMIT_TRUSTED_PROXIES", "")
	if isDev && strings.TrimSpace(rawTrustedProxies) == "" {
		rawTrustedProxies = "127.0.0.1,::1"
	}
	trustedProxies := parseStringSlice(rawTrustedProxies)
	if err := validateTrustedProxies(trustedProxies, isDev, envMode); err != nil {
		return nil, err
	}

	defaultFrontend := "http://localhost:3000"
	defaultCORS := "http://localhost:3000"
	if !isDev {
		defaultFrontend = ""
		defaultCORS = ""
	}

	frontendURL := getEnv("FRONTEND_URL", defaultFrontend)
	corsOrigins := getEnv("CORS_ALLOWED_ORIGINS", defaultCORS)

	if !isDev {
		if _, ok := os.LookupEnv("FORGELAB_ENCRYPTION_KEY"); !ok || encKey == "dGhpcy1pcy1hLWRldi1rZXktY2hhbmdlLWluLXByb2Q=" {
			return nil, fmt.Errorf("%s configuration error: default development encryption key is not allowed in %s mode; FORGELAB_ENCRYPTION_KEY must be explicitly configured", envMode, envMode)
		}
		if err := crypto.ValidateProductionKey(encKey); err != nil {
			return nil, fmt.Errorf("%s configuration error: %w", envMode, err)
		}
		if getEnv("COOKIE_SECURE", "false") != "true" {
			return nil, fmt.Errorf("%s configuration error: COOKIE_SECURE must be true in %s mode to enforce secure HttpOnly cookies", envMode, envMode)
		}
		logLevel := strings.ToLower(getEnv("LOG_LEVEL", "info"))
		if logLevel == "debug" {
			return nil, fmt.Errorf("%s configuration error: LOG_LEVEL cannot be 'debug' in %s mode to prevent sensitive data leakage", envMode, envMode)
		}
		if err := validateFrontendAndCORS(frontendURL, corsOrigins, envMode); err != nil {
			return nil, err
		}
		allowedRoots := parseStringSlice(getEnv("FORGELAB_ALLOWED_SOURCE_ROOTS", ""))
		if len(allowedRoots) == 0 {
			return nil, fmt.Errorf("%s configuration error: FORGELAB_ALLOWED_SOURCE_ROOTS must be configured with at least one explicit allowed source directory in %s mode", envMode, envMode)
		}
		for _, root := range allowedRoots {
			cleanSlash := strings.ToLower(filepath.ToSlash(filepath.Clean(root)))
			if cleanSlash == "/" || cleanSlash == "." || cleanSlash == ".." || strings.HasPrefix(cleanSlash, "../") {
				return nil, fmt.Errorf("%s configuration error: unsafe source root %q in FORGELAB_ALLOWED_SOURCE_ROOTS (cannot allow entire root filesystem or relative path)", envMode, root)
			}
			if len(cleanSlash) <= 3 && strings.HasSuffix(cleanSlash, ":/") {
				return nil, fmt.Errorf("%s configuration error: unsafe source root %q in FORGELAB_ALLOWED_SOURCE_ROOTS (cannot allow entire drive root)", envMode, root)
			}
			if cleanSlash == "/var" || strings.HasPrefix(cleanSlash, "/var/run") {
				return nil, fmt.Errorf("%s configuration error: unsafe source root %q in FORGELAB_ALLOWED_SOURCE_ROOTS (system directory access forbidden)", envMode, root)
			}
			for _, sysDir := range []string{"/etc", "/usr", "/sys", "/proc", "/dev", "/boot", "/bin", "/sbin", "/root", "c:/windows", "c:/program files", "c:/program files (x86)", "c:/system volume information", "c:/recovery"} {
				if cleanSlash == sysDir || strings.HasPrefix(cleanSlash, sysDir+"/") {
					return nil, fmt.Errorf("%s configuration error: unsafe source root %q in FORGELAB_ALLOWED_SOURCE_ROOTS (system directory access forbidden)", envMode, root)
				}
			}
		}
	}

	cfg := &Config{
		Server: ServerConfig{
			Host: getEnv("SERVER_HOST", "0.0.0.0"),
			Port: port,
		},
		Database: DatabaseConfig{
			URL: dbURL,
		},
		Redis: RedisConfig{
			URL: getEnv("REDIS_URL", "redis://localhost:6379/0"),
		},
		JWT: JWTConfig{
			Secret:             jwtSecret,
			AccessTokenExpiry:  time.Duration(accessExpiry) * time.Minute,
			RefreshTokenExpiry: time.Duration(refreshExpiry) * 24 * time.Hour,
		},
		Encryption: EncryptionConfig{
			Key: encKey,
		},
		Docker: DockerConfig{
			Host:                getEnv("DOCKER_HOST", ""),
			WorkDir:             getEnv("FORGELAB_WORK_DIR", "./data/builds"),
			SourcesDir:          getEnv("FORGELAB_SOURCES_DIR", "./data/sources"),
			AllowedSourceRoots:  getEnv("FORGELAB_ALLOWED_SOURCE_ROOTS", ""),
			HostSourceRoot:      getEnv("FORGELAB_HOST_SOURCE_ROOT", ""),
			ContainerSourceRoot: getEnv("FORGELAB_CONTAINER_SOURCE_ROOT", "/host-projects"),
			LocalBuildMode:      getEnv("FORGELAB_LOCAL_BUILD_MODE", "snapshot"),
			MaxConcurrentBuilds: parseIntEnv("FORGELAB_MAX_CONCURRENT_BUILDS", parseIntEnv("FORGELAB_MAX_DOCKER_BUILDS", defaultMaxConcurrentBuilds())),
			QueueWorkerCount:    parseIntEnv("FORGELAB_QUEUE_WORKERS", 4),
			PruneInterval:       parseDurationEnv("FORGELAB_IMAGE_PRUNE_INTERVAL", parseDurationEnv("FORGELAB_PRUNE_INTERVAL", 1*time.Hour)),
			PruneFilterLabel:    getEnv("FORGELAB_PRUNE_FILTER_LABEL", ""),
		},
		Log: LogConfig{
			Level:                getEnv("LOG_LEVEL", "debug"),
			Format:               getEnv("LOG_FORMAT", "text"),
			RetentionDays:        parseIntEnv("FORGELAB_LOG_RETENTION_DAYS", 30),
			MaxLogsPerDeployment: parseIntEnv("FORGELAB_LOG_MAX_PER_DEPLOYMENT", 10000),
			CleanupInterval:      parseDurationEnv("FORGELAB_LOG_CLEANUP_INTERVAL", 1*time.Hour),
		},
		App: AppConfig{
			Environment:        envMode,
			FrontendURL:        frontendURL,
			CORSAllowedOrigins: corsOrigins,
			CookieSecure:       getEnv("COOKIE_SECURE", "false") == "true",
			AllowGuestMode: func() bool {
				if guestEnv, ok := os.LookupEnv("FORGELAB_ALLOW_GUEST_MODE"); ok {
					guestEnvLower := strings.ToLower(strings.TrimSpace(guestEnv))
					return guestEnvLower == "true" || guestEnvLower == "1" || guestEnvLower == "yes"
				}
				// Default: enabled for local/single-user development, restricted for public/remote installations
				return isDev
			}(),
		},
		Google: OAuthConfig{
			ClientID:     getEnv("GOOGLE_CLIENT_ID", ""),
			ClientSecret: getEnv("GOOGLE_CLIENT_SECRET", ""),
			RedirectURL:  getEnv("GOOGLE_REDIRECT_URL", "http://localhost:3000/api/auth/google/callback"),
		},
		GitHub: OAuthConfig{
			ClientID:        getEnv("GITHUB_CLIENT_ID", ""),
			ClientSecret:    getEnv("GITHUB_CLIENT_SECRET", ""),
			RedirectURL:     getEnv("GITHUB_REDIRECT_URL", "http://localhost:3000/api/auth/github/callback"),
			RepoRedirectURL: getEnv("GITHUB_REPO_REDIRECT_URL", getEnv("FRONTEND_URL", "http://localhost:3000")+"/api/integrations/github/callback"),
		},
		GitHubApp: GitHubAppConfig{
			AppID:          getEnv("GITHUB_APP_ID", ""),
			PrivateKey:     getEnv("GITHUB_APP_PRIVATE_KEY", ""),
			PrivateKeyPath: getEnv("GITHUB_APP_PRIVATE_KEY_PATH", ""),
			Slug:           getEnv("GITHUB_APP_SLUG", ""),
			WebhookSecret:  getEnv("GITHUB_APP_WEBHOOK_SECRET", ""),
			ClientID:       getEnv("GITHUB_APP_CLIENT_ID", ""),
			ClientSecret:   getEnv("GITHUB_APP_CLIENT_SECRET", ""),
		},

		RateLimit: RateLimitConfig{
			Enabled:             getEnv("RATE_LIMIT_ENABLED", "true") != "false",
			AuthLimit:           parseIntEnv("RATE_LIMIT_AUTH_PER_MINUTE", 30),
			SourceLimit:         parseIntEnv("RATE_LIMIT_SOURCE_PER_MINUTE", 60),
			DeployLimit:         parseIntEnv("RATE_LIMIT_DEPLOY_PER_MINUTE", 60),
			WSConnLimit:         parseIntEnv("RATE_LIMIT_WS_CONN_PER_MINUTE", 120),
			WSSubscriptionLimit: parseIntEnv("RATE_LIMIT_WS_SUB_PER_MINUTE", 120),
			TrustedProxies:      trustedProxies,
		},
	}

	return cfg, nil
}

// Addr returns the server listen address.
func (s ServerConfig) Addr() string {
	return fmt.Sprintf("%s:%d", s.Host, s.Port)
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func parseIntEnv(key string, fallback int) int {
	if value, ok := os.LookupEnv(key); ok {
		if n, err := strconv.Atoi(value); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

func parseDurationEnv(key string, fallback time.Duration) time.Duration {
	if value, ok := os.LookupEnv(key); ok {
		if d, err := time.ParseDuration(value); err == nil && d > 0 {
			return d
		}
	}
	return fallback
}

func parseStringSlice(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var res []string
	for _, p := range strings.Split(s, ",") {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			res = append(res, trimmed)
		}
	}
	return res
}

func defaultMaxConcurrentBuilds() int {
	cpus := runtime.NumCPU()
	if cpus <= 2 {
		return 1
	}
	if cpus <= 4 {
		return 2
	}
	n := cpus / 2
	if n > 4 {
		return 4
	}
	return n
}

func validateDatabaseURL(rawURL string, isDev bool, envMode string) error {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid DATABASE_URL: %w", err)
	}

	scheme := strings.ToLower(u.Scheme)
	if scheme != "postgres" && scheme != "postgresql" {
		return fmt.Errorf("invalid DATABASE_URL scheme %q (must be postgres:// or postgresql://)", u.Scheme)
	}

	if u.Host == "" {
		return fmt.Errorf("invalid DATABASE_URL: host and port must be specified")
	}

	if !isDev {
		if u.User == nil || u.User.Username() == "" {
			return fmt.Errorf("%s configuration error: DATABASE_URL must specify a database username in %s mode", envMode, envMode)
		}
		pass, hasPass := u.User.Password()
		if !hasPass || pass == "" {
			return fmt.Errorf("%s configuration error: DATABASE_URL must specify a password in %s mode", envMode, envMode)
		}
		lowerPass := strings.ToLower(pass)
		if strings.Contains(rawURL, "forgelab_dev_password") ||
			lowerPass == "password" || lowerPass == "postgres" ||
			lowerPass == "admin" || lowerPass == "root" ||
			lowerPass == "123456" || lowerPass == "test" {
			return fmt.Errorf("%s configuration error: default development database password (forgelab_dev_password) is not allowed in %s mode; DATABASE_URL must be explicitly configured", envMode, envMode)
		}
	}
	return nil
}

func validateRedisURL(rawURL string) error {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid REDIS_URL: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "redis" && scheme != "rediss" {
		return fmt.Errorf("invalid REDIS_URL scheme %q (must be redis:// or rediss://)", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("invalid REDIS_URL: host must be specified")
	}
	return nil
}

func validateJWTSecret(secret string, isDev bool, envMode string) error {
	if strings.TrimSpace(secret) == "" {
		return fmt.Errorf("JWT_SECRET is required")
	}
	if !isDev {
		if len(secret) < 32 {
			return fmt.Errorf("%s configuration error: JWT_SECRET must be a secure random secret of at least 32 characters in %s mode", envMode, envMode)
		}
		lower := strings.ToLower(secret)
		if secret == "dev-jwt-secret-change-in-production" ||
			secret == "your-super-secret-jwt-key" ||
			secret == "example-jwt-secret" ||
			strings.Contains(lower, "change-in-prod") ||
			strings.Contains(lower, "changeme") ||
			strings.Contains(lower, "default-jwt") ||
			strings.Contains(lower, "example-jwt") ||
			strings.Contains(lower, "super-secret") ||
			strings.Contains(lower, "password") ||
			strings.Contains(lower, "forgelab") ||
			strings.Contains(lower, "123456") ||
			strings.Contains(lower, "1234567890") ||
			strings.Contains(lower, "abcdefgh") ||
			strings.Contains(lower, "qwerty") ||
			strings.Contains(lower, "admin") {
			return fmt.Errorf("%s configuration error: JWT_SECRET must be a secure random secret of at least 32 characters in %s mode", envMode, envMode)
		}
		uniqueChars := make(map[rune]struct{})
		for _, r := range secret {
			uniqueChars[r] = struct{}{}
		}
		if len(uniqueChars) < 8 {
			return fmt.Errorf("%s configuration error: JWT_SECRET has insufficient entropy (must contain at least 8 unique characters) in %s mode", envMode, envMode)
		}
	}
	return nil
}

func validateTrustedProxies(proxies []string, isDev bool, envMode string) error {
	if !isDev && len(proxies) == 0 {
		return fmt.Errorf("%s configuration error: RATE_LIMIT_TRUSTED_PROXIES must be configured with at least one trusted reverse proxy IP/CIDR in %s mode", envMode, envMode)
	}
	for _, p := range proxies {
		trimmed := strings.TrimSpace(p)
		if trimmed == "" {
			continue
		}
		if net.ParseIP(trimmed) == nil {
			if _, _, err := net.ParseCIDR(trimmed); err != nil {
				if !isDev {
					return fmt.Errorf("%s configuration error: invalid trusted proxy IP address or CIDR %q", envMode, trimmed)
				}
				return fmt.Errorf("invalid trusted proxy IP address or CIDR %q in RATE_LIMIT_TRUSTED_PROXIES", trimmed)
			}
		}
	}
	return nil
}

func validateFrontendAndCORS(frontendURL, corsAllowed, envMode string) error {
	trimmedFE := strings.TrimSpace(frontendURL)
	if trimmedFE == "" {
		return fmt.Errorf("%s configuration error: FRONTEND_URL must be explicitly configured in %s mode", envMode, envMode)
	}
	if strings.HasSuffix(trimmedFE, "/") {
		return fmt.Errorf("%s configuration error: FRONTEND_URL %q must not contain a trailing slash", envMode, trimmedFE)
	}
	u, err := url.Parse(trimmedFE)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("%s configuration error: FRONTEND_URL %q is not a valid absolute http/https URL", envMode, trimmedFE)
	}
	if (envMode == "production" || envMode == "staging") && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1") {
		return fmt.Errorf("%s configuration error: FRONTEND_URL %q cannot use loopback/localhost in %s mode", envMode, trimmedFE, envMode)
	}

	trimmedCORS := strings.TrimSpace(corsAllowed)
	if trimmedCORS == "" {
		return fmt.Errorf("%s configuration error: CORS_ALLOWED_ORIGINS must be explicitly configured in %s mode", envMode, envMode)
	}
	origins := strings.Split(trimmedCORS, ",")
	validCount := 0
	for _, o := range origins {
		oTrimmed := strings.TrimSpace(o)
		if oTrimmed == "" {
			continue
		}
		validCount++
		if oTrimmed == "*" {
			return fmt.Errorf("%s configuration error: wildcard origin '*' is not allowed in CORS_ALLOWED_ORIGINS in %s mode", envMode, envMode)
		}
		if strings.HasSuffix(oTrimmed, "/") {
			return fmt.Errorf("%s configuration error: CORS origin %q must not contain trailing slash", envMode, oTrimmed)
		}
		oURL, err := url.Parse(oTrimmed)
		if err != nil || oURL.Host == "" || (oURL.Scheme != "http" && oURL.Scheme != "https") {
			return fmt.Errorf("%s configuration error: invalid browser origin %q in CORS_ALLOWED_ORIGINS", envMode, oTrimmed)
		}
		if (envMode == "production" || envMode == "staging") && (oURL.Hostname() == "localhost" || oURL.Hostname() == "127.0.0.1" || oURL.Hostname() == "::1") {
			return fmt.Errorf("%s configuration error: CORS origin %q cannot use loopback/localhost in %s mode", envMode, oTrimmed, envMode)
		}
	}
	if validCount == 0 {
		return fmt.Errorf("%s configuration error: CORS_ALLOWED_ORIGINS must contain at least one valid origin in %s mode", envMode, envMode)
	}
	return nil
}
