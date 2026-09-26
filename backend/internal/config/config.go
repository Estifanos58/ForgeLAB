package config

import (
	"fmt"
	"os"
	"strconv"
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
}

// AppConfig holds application, CORS, and cookie settings.
type AppConfig struct {
	FrontendURL        string
	CORSAllowedOrigins string
	CookieSecure       bool
}

// OAuthConfig holds OAuth provider settings.
type OAuthConfig struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string
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
	Host               string
	WorkDir            string
	AllowedSourceRoots string
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

	cfg := &Config{
		Server: ServerConfig{
			Host: getEnv("SERVER_HOST", "0.0.0.0"),
			Port: port,
		},
		Database: DatabaseConfig{
			URL: getEnv("DATABASE_URL", "postgres://forgelab:forgelab_dev_password@localhost:5432/forgelab?sslmode=disable"),
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
			Key: getEnv("FORGELAB_ENCRYPTION_KEY", "dGhpcy1pcy1hLWRldi1rZXktY2hhbmdlLWluLXByb2Q="),
		},
		Docker: DockerConfig{
			Host:               getEnv("DOCKER_HOST", ""),
			WorkDir:            getEnv("FORGELAB_WORK_DIR", "./data/builds"),
			AllowedSourceRoots: getEnv("FORGELAB_ALLOWED_SOURCE_ROOTS", ""),
		},
		Log: LogConfig{
			Level:  getEnv("LOG_LEVEL", "debug"),
			Format: getEnv("LOG_FORMAT", "text"),
		},
		App: AppConfig{
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
			ClientID:     getEnv("GITHUB_CLIENT_ID", ""),
			ClientSecret: getEnv("GITHUB_CLIENT_SECRET", ""),
			RedirectURL:  getEnv("GITHUB_REDIRECT_URL", "http://localhost:3000/api/auth/github/callback"),
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
