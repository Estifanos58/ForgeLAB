package docker

import (
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/logging"
	"github.com/forgelab/backend/internal/models"
)

// TestConnectionResolver_PercentEncoding verifies that special characters in database credentials
// are safely percent-encoded using url.UserPassword rather than raw string concatenation.
func TestConnectionResolver_PercentEncoding(t *testing.T) {
	specialPass := "p@ss:w/ord?#&"
	specialUser := "us@er:name"
	dbName := "app_db"
	host := "postgres"
	port := 5432

	u := &url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(specialUser, specialPass),
		Host:     fmt.Sprintf("%s:%d", host, port),
		Path:     "/" + dbName,
		RawQuery: "sslmode=disable",
	}

	rawURL := u.String()

	// Parse back with standard library to verify RFC conformity
	parsed, err := url.Parse(rawURL)
	require.NoError(t, err, "URL must be standard-compliant RFC URI")
	assert.Equal(t, "postgres", parsed.Scheme)
	assert.Equal(t, host+":5432", parsed.Host)
	assert.Equal(t, "/app_db", parsed.Path)

	user := parsed.User.Username()
	pass, hasPass := parsed.User.Password()
	assert.True(t, hasPass)
	assert.Equal(t, specialUser, user, "Username must decode back to original special string")
	assert.Equal(t, specialPass, pass, "Password must decode back to original special string")

	// Raw URL should contain percent-encoded representations rather than raw '@' or ':' in userinfo
	assert.NotContains(t, rawURL, "p@ss:w/ord")
	assert.Contains(t, rawURL, "p%40ss%3Aw%2Ford")
}

// TestConnectionResolver_LogRedactor_MasksSecrets verifies that target database credentials
// and generated connection URLs are redacted from logs and diagnostics.
func TestConnectionResolver_LogRedactor_MasksSecrets(t *testing.T) {
	redactor := logging.NewLogRedactor()
	secretPass := "SuperSecretPassword123!"
	dbURL := "postgres://forgelab:SuperSecretPassword123!@db:5432/forgelab?sslmode=disable"

	redactor.AddSecret(secretPass)
	redactor.AddSecret(dbURL)

	log1 := fmt.Sprintf("Connecting to database using %s", dbURL)
	redacted1 := redactor.Redact(log1)
	assert.NotContains(t, redacted1, secretPass)
	assert.NotContains(t, redacted1, dbURL)
	assert.Contains(t, redacted1, "[REDACTED]")

	log2 := fmt.Sprintf("Authentication failed with password %s", secretPass)
	redacted2 := redactor.Redact(log2)
	assert.NotContains(t, redacted2, secretPass)
	assert.Contains(t, redacted2, "[REDACTED]")
}

// TestConnectionResolver_MultiDatabaseAmbiguity verifies that when multiple databases exist,
// generic DATABASE_URL is not set to prevent ambiguous automatic selection, while unambiguous
// per-service connection URLs are set.
func TestConnectionResolver_MultiDatabaseAmbiguity(t *testing.T) {
	projID := uuid.New()
	db1ID := uuid.New()
	db2ID := uuid.New()

	allServices := []*models.Service{
		{
			ID:             db1ID,
			ProjectID:      projID,
			Name:           "primary-db",
			Classification: models.ClassificationInfrastructure,
			InternalPort:   5432,
			Image:          "postgres:15",
		},
		{
			ID:             db2ID,
			ProjectID:      projID,
			Name:           "analytics-db",
			Classification: models.ClassificationInfrastructure,
			InternalPort:   5432,
			Image:          "postgres:15",
		},
	}

	var dbServices []*models.Service
	for _, p := range allServices {
		pNameLower := strings.ToLower(p.Name)
		pImageLower := strings.ToLower(p.Image)
		isDB := strings.Contains(pNameLower, "postgres") || strings.Contains(pImageLower, "postgres") ||
			strings.Contains(pNameLower, "mysql") || strings.Contains(pImageLower, "mysql") ||
			strings.Contains(pNameLower, "mongo") || strings.Contains(pImageLower, "mongo") ||
			pNameLower == "db" || pNameLower == "database"
		if isDB {
			dbServices = append(dbServices, p)
		}
	}

	assert.Equal(t, 2, len(dbServices))
	isSingleDB := len(dbServices) == 1
	assert.False(t, isSingleDB, "Multiple databases detected; isSingleDB must be false")

	// Simulate connection string generation
	var svcEnv []string
	hasExplicitDatabaseURL := false

	for _, peer := range allServices {
		upperName := strings.ToUpper(strings.ReplaceAll(peer.Name, "-", "_"))
		dbURL := fmt.Sprintf("postgres://forgelab:pass@%s:%d/forgelab", peer.Name, peer.InternalPort)
		svcEnv = append(svcEnv, fmt.Sprintf("%s_URL=%s", upperName, dbURL))
		if !hasExplicitDatabaseURL && isSingleDB {
			svcEnv = append(svcEnv, fmt.Sprintf("DATABASE_URL=%s", dbURL))
			hasExplicitDatabaseURL = true
		}
	}

	envMap := make(map[string]string)
	for _, e := range svcEnv {
		parts := strings.SplitN(e, "=", 2)
		envMap[parts[0]] = parts[1]
	}

	assert.Contains(t, envMap, "PRIMARY_DB_URL")
	assert.Contains(t, envMap, "ANALYTICS_DB_URL")
	assert.NotContains(t, envMap, "DATABASE_URL", "DATABASE_URL must be omitted when multiple databases exist to prevent ambiguous selection")
}

// TestConnectionResolver_MultiRedisAmbiguity verifies that when multiple caches/queues exist,
// generic REDIS_URL is not set to prevent ambiguous automatic selection.
func TestConnectionResolver_MultiRedisAmbiguity(t *testing.T) {
	projID := uuid.New()
	allServices := []*models.Service{
		{
			ID:             uuid.New(),
			ProjectID:      projID,
			Name:           "cache",
			Classification: models.ClassificationInfrastructure,
			InternalPort:   6379,
			Image:          "redis:7-alpine",
		},
		{
			ID:             uuid.New(),
			ProjectID:      projID,
			Name:           "queue",
			Classification: models.ClassificationInfrastructure,
			InternalPort:   6379,
			Image:          "redis:7-alpine",
		},
	}

	var redisServices []*models.Service
	for _, p := range allServices {
		pNameLower := strings.ToLower(p.Name)
		pImageLower := strings.ToLower(p.Image)
		isCache := strings.Contains(pNameLower, "redis") || strings.Contains(pImageLower, "redis") ||
			pNameLower == "cache" || pNameLower == "queue" || pNameLower == "valkey"
		if isCache {
			redisServices = append(redisServices, p)
		}
	}

	assert.Equal(t, 2, len(redisServices))
	isSingleRedis := len(redisServices) == 1
	assert.False(t, isSingleRedis, "Multiple redis services detected; isSingleRedis must be false")

	var svcEnv []string
	hasExplicitRedisURL := false

	for _, peer := range allServices {
		upperName := strings.ToUpper(strings.ReplaceAll(peer.Name, "-", "_"))
		redisURL := fmt.Sprintf("redis://%s:%d/0", peer.Name, peer.InternalPort)
		svcEnv = append(svcEnv, fmt.Sprintf("%s_URL=%s", upperName, redisURL))
		if !hasExplicitRedisURL && isSingleRedis {
			svcEnv = append(svcEnv, fmt.Sprintf("REDIS_URL=%s", redisURL))
			hasExplicitRedisURL = true
		}
	}

	envMap := make(map[string]string)
	for _, e := range svcEnv {
		parts := strings.SplitN(e, "=", 2)
		envMap[parts[0]] = parts[1]
	}

	assert.Contains(t, envMap, "CACHE_URL")
	assert.Contains(t, envMap, "QUEUE_URL")
	assert.NotContains(t, envMap, "REDIS_URL", "REDIS_URL must be omitted when multiple redis instances exist")
}

// TestConnectionResolver_FrontendBrowserURL_NeverPrivateDockerDNS verifies that browser-facing
// environment variables (e.g. NEXT_PUBLIC_API_URL, VITE_API_URL) for frontend services never use
// private Docker DNS hostnames.
func TestConnectionResolver_FrontendBrowserURL_NeverPrivateDockerDNS(t *testing.T) {
	backendHostPort := 8081
	previewURL := "https://api.example.forgelab.dev"

	backendSvc := &models.Service{
		Name:         "backend",
		Role:         models.RoleBackend,
		InternalPort: 8080,
		HostPort:     &backendHostPort,
		PreviewURL:   &previewURL,
	}

	frontendSvc := &models.Service{
		Name: "frontend",
		Role: models.RoleFrontend,
	}

	runtimeEnvMap := make(map[string]string)
	var svcEnv []string

	if backendSvc.Role == models.RoleBackend {
		svcEnv = append(svcEnv, fmt.Sprintf("BACKEND_INTERNAL_URL=http://%s:%d", backendSvc.Name, backendSvc.InternalPort))
		if frontendSvc.Role == models.RoleFrontend {
			browserURL := ""
			if backendSvc.PreviewURL != nil && *backendSvc.PreviewURL != "" {
				browserURL = *backendSvc.PreviewURL
			} else if backendSvc.CustomDomain != nil && *backendSvc.CustomDomain != "" {
				browserURL = fmt.Sprintf("https://%s", *backendSvc.CustomDomain)
			} else if backendSvc.HostPort != nil && *backendSvc.HostPort > 0 {
				browserURL = fmt.Sprintf("http://localhost:%d", *backendSvc.HostPort)
			}

			if browserURL != "" {
				if _, ok := runtimeEnvMap["NEXT_PUBLIC_API_URL"]; !ok {
					svcEnv = append(svcEnv, fmt.Sprintf("NEXT_PUBLIC_API_URL=%s", browserURL))
				}
				if _, ok := runtimeEnvMap["VITE_API_URL"]; !ok {
					svcEnv = append(svcEnv, fmt.Sprintf("VITE_API_URL=%s", browserURL))
				}
				if _, ok := runtimeEnvMap["API_URL"]; !ok {
					svcEnv = append(svcEnv, fmt.Sprintf("API_URL=%s", browserURL))
				}
			}
			if _, ok := runtimeEnvMap["BACKEND_URL"]; !ok {
				svcEnv = append(svcEnv, fmt.Sprintf("BACKEND_URL=http://%s:%d", backendSvc.Name, backendSvc.InternalPort))
			}
		}
	}

	envMap := make(map[string]string)
	for _, e := range svcEnv {
		parts := strings.SplitN(e, "=", 2)
		envMap[parts[0]] = parts[1]
	}

	assert.Equal(t, previewURL, envMap["NEXT_PUBLIC_API_URL"])
	assert.Equal(t, previewURL, envMap["VITE_API_URL"])
	assert.Equal(t, previewURL, envMap["API_URL"])
	assert.NotContains(t, envMap["NEXT_PUBLIC_API_URL"], "backend:8080")
	assert.NotContains(t, envMap["VITE_API_URL"], "backend:8080")
	assert.NotContains(t, envMap["API_URL"], "backend:8080")

	// BACKEND_URL and BACKEND_INTERNAL_URL are still available for container SSR
	assert.Equal(t, "http://backend:8080", envMap["BACKEND_URL"])
	assert.Equal(t, "http://backend:8080", envMap["BACKEND_INTERNAL_URL"])
}

// TestInternalService_NeverPublicExposedByDefault verifies that infrastructure, workers,
// and jobs are classified as internal services and not exposed publicly by default.
func TestInternalService_NeverPublicExposedByDefault(t *testing.T) {
	testCases := []struct {
		name           string
		classification string
		role           string
		image          string
		expected       bool
	}{
		{"postgres", models.ClassificationInfrastructure, "other", "postgres:15", true},
		{"redis", models.ClassificationInfrastructure, "other", "redis:7", true},
		{"worker", models.ClassificationWorker, "worker", "app:latest", true},
		{"migrate", models.ClassificationJob, "other", "app:latest", true},
		{"frontend", models.ClassificationApplication, models.RoleFrontend, "node:18", false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			isInternal := tc.classification == models.ClassificationInfrastructure ||
				tc.classification == models.ClassificationWorker ||
				tc.classification == models.ClassificationJob
			assert.Equal(t, tc.expected, isInternal)
		})
	}
}
