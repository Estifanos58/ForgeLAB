package config

import (
	"os"
	"testing"
)

func TestConfigLoadDevelopment(t *testing.T) {
	os.Setenv("JWT_SECRET", "dev-secret-that-is-short")
	os.Setenv("APP_ENV", "development")
	defer os.Unsetenv("JWT_SECRET")
	defer os.Unsetenv("APP_ENV")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("expected successful load in development, got error: %v", err)
	}
	if cfg.App.Environment != "development" {
		t.Errorf("expected environment 'development', got '%s'", cfg.App.Environment)
	}

	origins := cfg.App.AllowedOriginsList()
	if len(origins) == 0 {
		t.Errorf("expected non-empty allowed origins list")
	}
}

func TestConfigLoadProductionRejectsDefaultSecrets(t *testing.T) {
	os.Setenv("APP_ENV", "production")
	os.Setenv("COOKIE_SECURE", "true")
	os.Setenv("RATE_LIMIT_TRUSTED_PROXIES", "127.0.0.1")
	defer os.Unsetenv("APP_ENV")
	defer os.Unsetenv("COOKIE_SECURE")
	defer os.Unsetenv("RATE_LIMIT_TRUSTED_PROXIES")

	// 1. Insecure / short JWT_SECRET
	os.Setenv("JWT_SECRET", "short-secret")
	_, err := Load()
	if err == nil {
		t.Errorf("expected error for short JWT_SECRET in production mode, got nil")
	}

	// 2. Default encryption key
	os.Setenv("JWT_SECRET", "this-is-a-valid-production-jwt-secret-with-more-than-32-chars")
	os.Setenv("FORGELAB_ENCRYPTION_KEY", "dGhpcy1pcy1hLWRldi1rZXktY2hhbmdlLWluLXByb2Q=")
	defer os.Unsetenv("FORGELAB_ENCRYPTION_KEY")
	_, err = Load()
	if err == nil {
		t.Errorf("expected error for default encryption key in production mode, got nil")
	}

	// 3. Default database password
	os.Setenv("FORGELAB_ENCRYPTION_KEY", "MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE=")
	os.Setenv("DATABASE_URL", "postgres://forgelab:forgelab_dev_password@db:5432/forgelab")
	defer os.Unsetenv("DATABASE_URL")
	_, err = Load()
	if err == nil {
		t.Errorf("expected error for default db password in production mode, got nil")
	}

	// 4. Valid production configuration
	os.Setenv("DATABASE_URL", "postgres://forgelab:strong_prod_pass_123@db:5432/forgelab")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("expected successful load with strong secrets in production mode, got: %v", err)
	}
	if cfg.App.Environment != "production" {
		t.Errorf("expected environment 'production', got '%s'", cfg.App.Environment)
	}
}

func TestConfigLoadRejectsInvalidEncryptionKeyLength(t *testing.T) {
	os.Setenv("JWT_SECRET", "dev-secret-that-is-short")
	os.Setenv("APP_ENV", "development")
	os.Setenv("FORGELAB_ENCRYPTION_KEY", "invalid-short-key")
	defer os.Unsetenv("JWT_SECRET")
	defer os.Unsetenv("APP_ENV")
	defer os.Unsetenv("FORGELAB_ENCRYPTION_KEY")

	_, err := Load()
	if err == nil {
		t.Fatalf("expected error for invalid key length in development mode, got nil")
	}
}

func TestConfigBuildConcurrency(t *testing.T) {
	os.Setenv("JWT_SECRET", "dev-secret-that-is-short")
	os.Setenv("APP_ENV", "development")
	defer os.Unsetenv("JWT_SECRET")
	defer os.Unsetenv("APP_ENV")

	// Default conservative value
	cfg, err := Load()
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}
	if cfg.Docker.MaxConcurrentBuilds < 1 {
		t.Errorf("expected MaxConcurrentBuilds >= 1, got %d", cfg.Docker.MaxConcurrentBuilds)
	}

	// Explicit override via FORGELAB_MAX_CONCURRENT_BUILDS
	os.Setenv("FORGELAB_MAX_CONCURRENT_BUILDS", "5")
	defer os.Unsetenv("FORGELAB_MAX_CONCURRENT_BUILDS")

	cfgOverride, err := Load()
	if err != nil {
		t.Fatalf("failed to load config with override: %v", err)
	}
	if cfgOverride.Docker.MaxConcurrentBuilds != 5 {
		t.Errorf("expected MaxConcurrentBuilds = 5, got %d", cfgOverride.Docker.MaxConcurrentBuilds)
	}
}
