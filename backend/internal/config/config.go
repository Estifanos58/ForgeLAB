package config

import (
	"fmt"
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
	// Always include loopback origins in local development
	envLower := strings.ToLower(a.Environment)
	isDev := envLower == "development" || envLower == "dev" || envLower == "local" || envLower == "test" || envLower == ""
	if isDev {
		origins["http://localhost:3000"] = true
		origins["http://127.0.0.1:3000"] = true
		origins["http://localhost:8080"] = true
		origins["http://127.0.0.1:8080"] = true
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

// LogConfig holds logging settings.
type LogConfig struct {
	Level  string
	Format string
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
	if jwtSecret == "" {
		return nil, fmt.Errorf("JWT_SECRET is required")
	}

	envMode := strings.ToLower(getEnv("APP_ENV", getEnv("FORGELAB_ENV", getEnv("ENV", "development"))))
	isDev := envMode == "development" || envMode == "dev" || envMode == "local" || envMode == "test"

	dbURL := getEnv("DATABASE_URL", "postgres://forgelab:forgelab_dev_password@localhost:5432/forgelab?sslmode=disable")
	encKey := getEnv("FORGELAB_ENCRYPTION_KEY", "dGhpcy1pcy1hLWRldi1rZXktY2hhbmdlLWluLXByb2Q=")

	// Validate encryption key length in all environments
	if _, err := crypto.ParseKey(encKey); err != nil {
		return nil, fmt.Errorf("invalid FORGELAB_ENCRYPTION_KEY: %w", err)
	}

	if !isDev {
		if _, ok := os.LookupEnv("DATABASE_URL"); !ok || strings.Contains(dbURL, "forgelab_dev_password") {
			return nil, fmt.Errorf("%s configuration error: default development database password (forgelab_dev_password) is not allowed in %s mode; DATABASE_URL must be explicitly configured", envMode, envMode)
		}
		if _, ok := os.LookupEnv("FORGELAB_ENCRYPTION_KEY"); !ok || encKey == "dGhpcy1pcy1hLWRldi1rZXktY2hhbmdlLWluLXByb2Q=" {
			return nil, fmt.Errorf("%s configuration error: default development encryption key is not allowed in %s mode; FORGELAB_ENCRYPTION_KEY must be explicitly configured", envMode, envMode)
		}
		if jwtSecret == "dev-jwt-secret-change-in-production" || jwtSecret == "your-super-secret-jwt-key" ||
			jwtSecret == "example-jwt-secret" || len(jwtSecret) < 32 {
			return nil, fmt.Errorf("%s configuration error: JWT_SECRET must be a secure random secret of at least 32 characters in %s mode", envMode, envMode)
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
		trustedProxies := parseStringSlice(getEnv("RATE_LIMIT_TRUSTED_PROXIES", ""))
		if len(trustedProxies) == 0 {
			return nil, fmt.Errorf("%s configuration error: RATE_LIMIT_TRUSTED_PROXIES must be configured with at least one trusted reverse proxy IP/CIDR in %s mode", envMode, envMode)
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

	defaultFrontend := "http://localhost:3000"
	defaultCORS := "http://localhost:3000"
	if !isDev {
		defaultFrontend = ""
		defaultCORS = ""
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
			Level:  getEnv("LOG_LEVEL", "debug"),
			Format: getEnv("LOG_FORMAT", "text"),
		},
		App: AppConfig{
			Environment:        envMode,
			FrontendURL:        getEnv("FRONTEND_URL", defaultFrontend),
			CORSAllowedOrigins: getEnv("CORS_ALLOWED_ORIGINS", defaultCORS),
			CookieSecure:       getEnv("COOKIE_SECURE", "false") == "true",
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
		RateLimit: RateLimitConfig{
			Enabled:             getEnv("RATE_LIMIT_ENABLED", "true") != "false",
			AuthLimit:           parseIntEnv("RATE_LIMIT_AUTH_PER_MINUTE", 30),
			SourceLimit:         parseIntEnv("RATE_LIMIT_SOURCE_PER_MINUTE", 60),
			DeployLimit:         parseIntEnv("RATE_LIMIT_DEPLOY_PER_MINUTE", 60),
			WSConnLimit:         parseIntEnv("RATE_LIMIT_WS_CONN_PER_MINUTE", 120),
			WSSubscriptionLimit: parseIntEnv("RATE_LIMIT_WS_SUB_PER_MINUTE", 120),
			TrustedProxies:      parseStringSlice(getEnv("RATE_LIMIT_TRUSTED_PROXIES", "")),
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
