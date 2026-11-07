package config

import (
	"os"
	"strings"
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

	// 4. Missing allowed source roots
	os.Setenv("DATABASE_URL", "postgres://forgelab:strong_prod_pass_123@db:5432/forgelab")
	os.Unsetenv("FORGELAB_ALLOWED_SOURCE_ROOTS")
	_, err = Load()
	if err == nil {
		t.Errorf("expected error for missing FORGELAB_ALLOWED_SOURCE_ROOTS in production mode, got nil")
	}

	// 5. Valid production configuration
	os.Setenv("FORGELAB_ALLOWED_SOURCE_ROOTS", "/var/forgelab/sources")
	os.Setenv("FRONTEND_URL", "https://app.forgelab.com")
	os.Setenv("CORS_ALLOWED_ORIGINS", "https://app.forgelab.com")
	defer os.Unsetenv("FORGELAB_ALLOWED_SOURCE_ROOTS")
	defer os.Unsetenv("FRONTEND_URL")
	defer os.Unsetenv("CORS_ALLOWED_ORIGINS")
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

func TestConfigLoadStagingRejectsWeakSecretsAndInsecureSettings(t *testing.T) {
	os.Setenv("APP_ENV", "staging")
	defer os.Unsetenv("APP_ENV")

	// 1. Staging must reject short JWT_SECRET
	os.Setenv("JWT_SECRET", "short-secret")
	defer os.Unsetenv("JWT_SECRET")
	_, err := Load()
	if err == nil {
		t.Errorf("expected error for short JWT_SECRET in staging mode, got nil")
	}

	// 2. Staging must reject default db password
	os.Setenv("JWT_SECRET", "this-is-a-valid-production-jwt-secret-with-more-than-32-chars")
	os.Setenv("FORGELAB_ENCRYPTION_KEY", "MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE=")
	defer os.Unsetenv("FORGELAB_ENCRYPTION_KEY")
	os.Setenv("DATABASE_URL", "postgres://forgelab:forgelab_dev_password@db:5432/forgelab")
	defer os.Unsetenv("DATABASE_URL")
	_, err = Load()
	if err == nil {
		t.Errorf("expected error for default db password in staging mode, got nil")
	}

	// 3. Staging must reject insecure cookies
	os.Setenv("DATABASE_URL", "postgres://forgelab:staging_secure_pass_456@db:5432/forgelab")
	os.Setenv("COOKIE_SECURE", "false")
	defer os.Unsetenv("COOKIE_SECURE")
	_, err = Load()
	if err == nil {
		t.Errorf("expected error for COOKIE_SECURE=false in staging mode, got nil")
	}

	// 4. Staging must reject unsafe source roots (e.g. root filesystem)
	os.Setenv("COOKIE_SECURE", "true")
	os.Setenv("RATE_LIMIT_TRUSTED_PROXIES", "10.0.0.1")
	defer os.Unsetenv("RATE_LIMIT_TRUSTED_PROXIES")
	os.Setenv("FRONTEND_URL", "https://staging.forgelab.internal")
	os.Setenv("CORS_ALLOWED_ORIGINS", "https://staging.forgelab.internal")
	defer os.Unsetenv("FRONTEND_URL")
	defer os.Unsetenv("CORS_ALLOWED_ORIGINS")
	os.Setenv("FORGELAB_ALLOWED_SOURCE_ROOTS", "/")
	defer os.Unsetenv("FORGELAB_ALLOWED_SOURCE_ROOTS")
	_, err = Load()
	if err == nil {
		t.Errorf("expected error for root filesystem in FORGELAB_ALLOWED_SOURCE_ROOTS in staging mode, got nil")
	}

	// 5. Staging must reject sensitive system directories
	os.Setenv("FORGELAB_ALLOWED_SOURCE_ROOTS", "/etc")
	_, err = Load()
	if err == nil {
		t.Errorf("expected error for /etc in FORGELAB_ALLOWED_SOURCE_ROOTS in staging mode, got nil")
	}

	// 6. Valid staging configuration succeeds and excludes loopback CORS origins by default
	os.Setenv("FORGELAB_ALLOWED_SOURCE_ROOTS", "/opt/forgelab/sources")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("expected successful staging configuration load, got error: %v", err)
	}
	if cfg.App.Environment != "staging" {
		t.Errorf("expected environment 'staging', got '%s'", cfg.App.Environment)
	}

	origins := cfg.App.AllowedOriginsList()
	for _, o := range origins {
		if o == "http://localhost:3000" || o == "http://127.0.0.1:3000" {
			t.Errorf("staging configuration must not automatically include loopback origin %s", o)
		}
	}
}

func TestConfigValidation_DatabaseURL(t *testing.T) {
	// 1. Invalid scheme
	if err := validateDatabaseURL("mysql://user:pass@localhost:3306/db", false, "production"); err == nil {
		t.Errorf("expected error for non-postgres scheme, got nil")
	}

	// 2. Missing host
	if err := validateDatabaseURL("postgres://user:pass@/dbname", false, "production"); err == nil {
		t.Errorf("expected error for missing host in database URL, got nil")
	}

	// 3. Missing password in non-dev
	if err := validateDatabaseURL("postgres://user@localhost:5432/dbname", false, "production"); err == nil {
		t.Errorf("expected error for missing password in production database URL, got nil")
	}

	// 4. Default / weak password in non-dev
	for _, weakPass := range []string{"forgelab_dev_password", "postgres", "password", "admin", "123456", "root"} {
		url := "postgres://forgelab:" + weakPass + "@db:5432/forgelab"
		if err := validateDatabaseURL(url, false, "production"); err == nil {
			t.Errorf("expected error for weak password %q in production, got nil", weakPass)
		}
	}

	// 5. Dev mode allows default password and postgresql:// scheme
	devURL := "postgresql://forgelab:forgelab_dev_password@localhost:5432/forgelab"
	if err := validateDatabaseURL(devURL, true, "development"); err != nil {
		t.Errorf("expected success for default password in development mode, got: %v", err)
	}
}

func TestConfigValidation_RedisURL(t *testing.T) {
	// Valid redis URLs
	if err := validateRedisURL("redis://localhost:6379/0"); err != nil {
		t.Errorf("expected redis:// to be valid, got: %v", err)
	}
	if err := validateRedisURL("rediss://secure-redis:6380/1"); err != nil {
		t.Errorf("expected rediss:// to be valid, got: %v", err)
	}

	// Invalid scheme
	if err := validateRedisURL("http://localhost:6379"); err == nil {
		t.Errorf("expected error for http:// scheme in redis URL, got nil")
	}

	// Missing host
	if err := validateRedisURL("redis:///0"); err == nil {
		t.Errorf("expected error for missing host in redis URL, got nil")
	}
}

func TestConfigValidation_JWTSecret(t *testing.T) {
	// Dev mode allows short secrets
	if err := validateJWTSecret("dev-short-secret", true, "development"); err != nil {
		t.Errorf("expected short secret to pass in development, got: %v", err)
	}

	// Non-dev mode rejects short secrets
	if err := validateJWTSecret("short", false, "production"); err == nil {
		t.Errorf("expected error for short JWT secret in production, got nil")
	}

	// Non-dev mode rejects predictable passwords/substrings
	predictableSecrets := []string{
		"this-is-a-long-secret-with-password-inside!",
		"forgelab-super-secret-key-that-is-long-enough",
		"a-very-long-secret-containing-123456-suffix!",
	}
	for _, sec := range predictableSecrets {
		if err := validateJWTSecret(sec, false, "production"); err == nil {
			t.Errorf("expected error for predictable JWT secret %q, got nil", sec)
		}
	}

	// Non-dev mode rejects low character variety
	lowEntropy := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := validateJWTSecret(lowEntropy, false, "production"); err == nil {
		t.Errorf("expected error for low entropy secret %q, got nil", lowEntropy)
	}

	// Non-dev mode accepts strong secret
	strong := "kX9#mP2$vL8@zQ5*wE4!rT7^yU1&iO3("
	if err := validateJWTSecret(strong, false, "production"); err != nil {
		t.Errorf("expected strong JWT secret to pass, got: %v", err)
	}
}

func TestConfigValidation_TrustedProxies(t *testing.T) {
	// Valid IPv4, IPv6, CIDR
	valid := []string{"127.0.0.1", "10.0.0.1", "192.168.1.0/24", "::1", "fe80::/10"}
	if err := validateTrustedProxies(valid, false, "production"); err != nil {
		t.Errorf("expected valid proxies to pass, got: %v", err)
	}

	// Invalid proxy format
	invalid := []string{"10.0.0.1", "invalid-ip-or-hostname"}
	if err := validateTrustedProxies(invalid, false, "production"); err == nil {
		t.Errorf("expected error for invalid proxy IP in production, got nil")
	}
	if err := validateTrustedProxies(invalid, true, "development"); err == nil {
		t.Errorf("expected error for invalid proxy IP in development, got nil")
	}
}

func TestConfigValidation_FrontendAndCORS(t *testing.T) {
	// Missing frontend URL in production
	if err := validateFrontendAndCORS("", "https://app.forgelab.com", "production"); err == nil {
		t.Errorf("expected error for missing FRONTEND_URL in production, got nil")
	}

	// Trailing slash in frontend URL
	if err := validateFrontendAndCORS("https://app.forgelab.com/", "https://app.forgelab.com", "production"); err == nil {
		t.Errorf("expected error for trailing slash in FRONTEND_URL, got nil")
	}

	// Missing CORS origins in production
	if err := validateFrontendAndCORS("https://app.forgelab.com", "", "production"); err == nil {
		t.Errorf("expected error for missing CORS_ALLOWED_ORIGINS in production, got nil")
	}

	// Wildcard CORS in production
	if err := validateFrontendAndCORS("https://app.forgelab.com", "*", "production"); err == nil {
		t.Errorf("expected error for wildcard CORS in production, got nil")
	}

	// Localhost CORS in production
	if err := validateFrontendAndCORS("https://app.forgelab.com", "http://localhost:3000", "production"); err == nil {
		t.Errorf("expected error for localhost CORS in production, got nil")
	}

	// Valid production frontend and CORS origins
	if err := validateFrontendAndCORS("https://app.forgelab.com", "https://app.forgelab.com,https://api.forgelab.com", "production"); err != nil {
		t.Errorf("expected valid frontend and CORS origins to pass in production, got: %v", err)
	}
}

func TestConfigGuestModeDefaults(t *testing.T) {
	// Helper to set standard prod vars to allow config.Load to pass
	setupProdEnv := func() {
		os.Setenv("APP_ENV", "production")
		os.Setenv("COOKIE_SECURE", "true")
		os.Setenv("RATE_LIMIT_TRUSTED_PROXIES", "127.0.0.1")
		os.Setenv("JWT_SECRET", "this-is-a-valid-production-jwt-secret-with-more-than-32-chars")
		os.Setenv("FORGELAB_ENCRYPTION_KEY", "MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE=")
		os.Setenv("DATABASE_URL", "postgres://forgelab:strong_prod_pass_123@db:5432/forgelab")
		os.Setenv("FORGELAB_ALLOWED_SOURCE_ROOTS", "/var/forgelab/sources")
		os.Setenv("FRONTEND_URL", "https://app.forgelab.com")
		os.Setenv("CORS_ALLOWED_ORIGINS", "https://app.forgelab.com")
	}

	cleanupEnv := func() {
		os.Unsetenv("APP_ENV")
		os.Unsetenv("COOKIE_SECURE")
		os.Unsetenv("RATE_LIMIT_TRUSTED_PROXIES")
		os.Unsetenv("JWT_SECRET")
		os.Unsetenv("FORGELAB_ENCRYPTION_KEY")
		os.Unsetenv("DATABASE_URL")
		os.Unsetenv("FORGELAB_ALLOWED_SOURCE_ROOTS")
		os.Unsetenv("FRONTEND_URL")
		os.Unsetenv("CORS_ALLOWED_ORIGINS")
		os.Unsetenv("FORGELAB_ALLOW_GUEST_MODE")
		os.Unsetenv("ALLOW_GUEST_MODE")
	}

	t.Run("development defaults to enabled", func(t *testing.T) {
		cleanupEnv()
		defer cleanupEnv()
		os.Setenv("APP_ENV", "development")
		os.Setenv("JWT_SECRET", "dev-secret-that-is-short")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !cfg.App.AllowGuestMode {
			t.Errorf("expected AllowGuestMode to be true in development by default")
		}
	})

	t.Run("production defaults to disabled", func(t *testing.T) {
		cleanupEnv()
		defer cleanupEnv()
		setupProdEnv()

		cfg, err := Load()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.App.AllowGuestMode {
			t.Errorf("expected AllowGuestMode to be false in production by default")
		}
	})

	t.Run("staging defaults to disabled", func(t *testing.T) {
		cleanupEnv()
		defer cleanupEnv()
		setupProdEnv()
		os.Setenv("APP_ENV", "staging")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.App.AllowGuestMode {
			t.Errorf("expected AllowGuestMode to be false in staging by default")
		}
	})

	t.Run("production explicitly enabled", func(t *testing.T) {
		cleanupEnv()
		defer cleanupEnv()
		setupProdEnv()
		os.Setenv("FORGELAB_ALLOW_GUEST_MODE", "true")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !cfg.App.AllowGuestMode {
			t.Errorf("expected AllowGuestMode to be true when explicitly configured")
		}
	})

	t.Run("development explicitly disabled", func(t *testing.T) {
		cleanupEnv()
		defer cleanupEnv()
		os.Setenv("APP_ENV", "development")
		os.Setenv("JWT_SECRET", "dev-secret-that-is-short")
		os.Setenv("FORGELAB_ALLOW_GUEST_MODE", "false")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.App.AllowGuestMode {
			t.Errorf("expected AllowGuestMode to be false when explicitly disabled in dev")
		}
	})

	t.Run("empty string falls back to development default", func(t *testing.T) {
		cleanupEnv()
		defer cleanupEnv()
		os.Setenv("APP_ENV", "development")
		os.Setenv("JWT_SECRET", "dev-secret-that-is-short")
		os.Setenv("FORGELAB_ALLOW_GUEST_MODE", "")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !cfg.App.AllowGuestMode {
			t.Errorf("expected AllowGuestMode to be true in development when env var is empty string")
		}
	})
}

func TestDockerComposeGuestModeDefault(t *testing.T) {
	candidates := []string{
		"../../../docker-compose.yml",
		"../../docker-compose.yml",
		"../docker-compose.yml",
		"docker-compose.yml",
	}
	var content []byte
	var found bool
	for _, path := range candidates {
		var err error
		content, err = os.ReadFile(path)
		if err == nil {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("could not locate docker-compose.yml")
	}
	text := string(content)
	if strings.Contains(text, "${FORGELAB_ALLOW_GUEST_MODE:-false}") {
		t.Errorf("docker-compose.yml must not default FORGELAB_ALLOW_GUEST_MODE to false for local deployments")
	}
	if !strings.Contains(text, "FORGELAB_ALLOW_GUEST_MODE: ${FORGELAB_ALLOW_GUEST_MODE:-true}") &&
		!strings.Contains(text, "FORGELAB_ALLOW_GUEST_MODE=${FORGELAB_ALLOW_GUEST_MODE:-true}") {
		t.Errorf("docker-compose.yml must default FORGELAB_ALLOW_GUEST_MODE to true")
	}
}

func TestConfigPublicSharing(t *testing.T) {
	setupProdEnv := func() {
		os.Setenv("APP_ENV", "production")
		os.Setenv("COOKIE_SECURE", "true")
		os.Setenv("RATE_LIMIT_TRUSTED_PROXIES", "127.0.0.1")
		os.Setenv("JWT_SECRET", "this-is-a-valid-production-jwt-secret-with-more-than-32-chars")
		os.Setenv("FORGELAB_ENCRYPTION_KEY", "MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE=")
		os.Setenv("DATABASE_URL", "postgres://forgelab:strong_prod_pass_123@db:5432/forgelab")
		os.Setenv("FORGELAB_ALLOWED_SOURCE_ROOTS", "/var/forgelab/sources")
		os.Setenv("FRONTEND_URL", "https://app.forgelab.com")
		os.Setenv("CORS_ALLOWED_ORIGINS", "https://app.forgelab.com")
	}

	cleanupEnv := func() {
		os.Unsetenv("APP_ENV")
		os.Unsetenv("COOKIE_SECURE")
		os.Unsetenv("RATE_LIMIT_TRUSTED_PROXIES")
		os.Unsetenv("JWT_SECRET")
		os.Unsetenv("FORGELAB_ENCRYPTION_KEY")
		os.Unsetenv("DATABASE_URL")
		os.Unsetenv("FORGELAB_ALLOWED_SOURCE_ROOTS")
		os.Unsetenv("FRONTEND_URL")
		os.Unsetenv("CORS_ALLOWED_ORIGINS")
		os.Unsetenv("FORGELAB_ALLOW_GUEST_MODE")
		os.Unsetenv("FORGELAB_SHARING_ENABLED")
		os.Unsetenv("FORGELAB_SHARING_PROVIDER")
		os.Unsetenv("GATEWAY_TUNNEL_URL")
		os.Unsetenv("GATEWAY_PUBLIC_URL")
		os.Unsetenv("GATEWAY_AUTH_TOKEN")
		os.Unsetenv("GATEWAY_AUTH_SECRET")
	}

	t.Run("cloudflare provider does not require gateway credentials in production", func(t *testing.T) {
		cleanupEnv()
		defer cleanupEnv()
		setupProdEnv()
		os.Setenv("FORGELAB_SHARING_ENABLED", "true")
		os.Setenv("FORGELAB_SHARING_PROVIDER", "cloudflare")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("expected cloudflare sharing to succeed without gateway credentials in production, got error: %v", err)
		}
		if !cfg.Share.Enabled {
			t.Errorf("expected Share.Enabled to be true")
		}
		if cfg.Share.Provider != "cloudflare" {
			t.Errorf("expected Share.Provider to be 'cloudflare', got: %s", cfg.Share.Provider)
		}
	})

	t.Run("default provider is cloudflare when gateway credentials are absent", func(t *testing.T) {
		cleanupEnv()
		defer cleanupEnv()
		setupProdEnv()
		os.Setenv("FORGELAB_SHARING_ENABLED", "true")
		os.Unsetenv("FORGELAB_SHARING_PROVIDER")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("expected default sharing provider to resolve to cloudflare and succeed, got error: %v", err)
		}
		if cfg.Share.Provider != "cloudflare" {
			t.Errorf("expected Share.Provider to default to 'cloudflare', got: %s", cfg.Share.Provider)
		}
	})

	t.Run("gateway provider requires tunnel url in production", func(t *testing.T) {
		cleanupEnv()
		defer cleanupEnv()
		setupProdEnv()
		os.Setenv("FORGELAB_SHARING_ENABLED", "true")
		os.Setenv("FORGELAB_SHARING_PROVIDER", "gateway")
		os.Setenv("GATEWAY_AUTH_TOKEN", "valid-auth-token")
		os.Unsetenv("GATEWAY_TUNNEL_URL")

		_, err := Load()
		if err == nil {
			t.Fatalf("expected error when gateway provider is missing GATEWAY_TUNNEL_URL in production")
		}
		if !strings.Contains(err.Error(), "GATEWAY_TUNNEL_URL") {
			t.Errorf("expected error message to mention GATEWAY_TUNNEL_URL, got: %v", err)
		}
	})

	t.Run("gateway provider requires auth token in production", func(t *testing.T) {
		cleanupEnv()
		defer cleanupEnv()
		setupProdEnv()
		os.Setenv("FORGELAB_SHARING_ENABLED", "true")
		os.Setenv("FORGELAB_SHARING_PROVIDER", "gateway")
		os.Setenv("GATEWAY_TUNNEL_URL", "tunnel.example.com:8443")
		os.Unsetenv("GATEWAY_AUTH_TOKEN")
		os.Unsetenv("GATEWAY_AUTH_SECRET")

		_, err := Load()
		if err == nil {
			t.Fatalf("expected error when gateway provider is missing GATEWAY_AUTH_TOKEN in production")
		}
		if !strings.Contains(err.Error(), "GATEWAY_AUTH_TOKEN") {
			t.Errorf("expected error message to mention GATEWAY_AUTH_TOKEN, got: %v", err)
		}
	})

	t.Run("gateway provider succeeds with valid credentials in production", func(t *testing.T) {
		cleanupEnv()
		defer cleanupEnv()
		setupProdEnv()
		os.Setenv("FORGELAB_SHARING_ENABLED", "true")
		os.Setenv("FORGELAB_SHARING_PROVIDER", "gateway")
		os.Setenv("GATEWAY_TUNNEL_URL", "tunnel.example.com:8443")
		os.Setenv("GATEWAY_AUTH_TOKEN", "my-secret-token")
		os.Setenv("GATEWAY_PUBLIC_URL", "https://preview.example.com")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("expected gateway sharing to succeed with valid credentials in production, got: %v", err)
		}
		if !cfg.Share.Enabled {
			t.Errorf("expected Share.Enabled to be true")
		}
		if cfg.Share.Provider != "gateway" {
			t.Errorf("expected Share.Provider to be 'gateway', got: %s", cfg.Share.Provider)
		}
		if cfg.Share.GatewayTunnelURL != "tunnel.example.com:8443" {
			t.Errorf("unexpected GatewayTunnelURL: %s", cfg.Share.GatewayTunnelURL)
		}
		if cfg.Share.GatewayAuthToken != "my-secret-token" {
			t.Errorf("unexpected GatewayAuthToken: %s", cfg.Share.GatewayAuthToken)
		}
	})

	t.Run("unsupported sharing provider is rejected with clear error", func(t *testing.T) {
		cleanupEnv()
		defer cleanupEnv()
		setupProdEnv()
		os.Setenv("FORGELAB_SHARING_ENABLED", "true")
		os.Setenv("FORGELAB_SHARING_PROVIDER", "ngrok")

		_, err := Load()
		if err == nil {
			t.Fatalf("expected error for unsupported sharing provider 'ngrok'")
		}
		if !strings.Contains(err.Error(), "unsupported public sharing provider") || !strings.Contains(err.Error(), "ngrok") {
			t.Errorf("expected unsupported provider error mentioning ngrok, got: %v", err)
		}
	})
}
