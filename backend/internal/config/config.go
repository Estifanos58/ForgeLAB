package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
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
	if a.Environment != "production" {
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
	MaxConcurrentBuilds int // global cap on concurrent Docker builds/deploys
	QueueWorkerCount    int // number of queue worker goroutines
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
	isProd := envMode == "production" || envMode == "prod"

	dbURL := getEnv("DATABASE_URL", "postgres://forgelab:forgelab_dev_password@localhost:5432/forgelab?sslmode=disable")
	encKey := getEnv("FORGELAB_ENCRYPTION_KEY", "dGhpcy1pcy1hLWRldi1rZXktY2hhbmdlLWluLXByb2Q=")

	if isProd {
		if jwtSecret == "dev-jwt-secret-change-in-production" || jwtSecret == "your-super-secret-jwt-key" ||
			jwtSecret == "example-jwt-secret" || len(jwtSecret) < 32 {
			return nil, fmt.Errorf("production configuration error: JWT_SECRET must be a secure random secret of at least 32 characters in production mode")
		}
		if encKey == "" || encKey == "dGhpcy1pcy1hLWRldi1rZXktY2hhbmdlLWluLXByb2Q=" {
			return nil, fmt.Errorf("production configuration error: default development FORGELAB_ENCRYPTION_KEY is not allowed in production mode")
		}
		if strings.Contains(dbURL, "forgelab_dev_password") {
			return nil, fmt.Errorf("production configuration error: default development database password (forgelab_dev_password) is not allowed in production mode")
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
			LocalBuildMode:      getEnv("FORGELAB_LOCAL_BUILD_MODE", "direct"),
			MaxConcurrentBuilds: parseIntEnv("FORGELAB_MAX_DOCKER_BUILDS", 4),
			QueueWorkerCount:    parseIntEnv("FORGELAB_QUEUE_WORKERS", 4),
		},
		Log: LogConfig{
			Level:  getEnv("LOG_LEVEL", "debug"),
			Format: getEnv("LOG_FORMAT", "text"),
		},
		App: AppConfig{
			Environment:        envMode,
			FrontendURL:        getEnv("FRONTEND_URL", "http://localhost:3000"),
			CORSAllowedOrigins: getEnv("CORS_ALLOWED_ORIGINS", "http://localhost:3000"),
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
