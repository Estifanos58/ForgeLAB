package services_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/database"
	"github.com/forgelab/backend/internal/handlers"
	"github.com/forgelab/backend/internal/middleware"
	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/services"
)

func getTestDatabaseURL() string {
	if url := os.Getenv("DATABASE_URL"); url != "" {
		return url
	}
	return "postgres://forgelab:forgelab_dev_password@localhost:5432/forgelab?sslmode=disable"
}

func connectTestDB(t *testing.T) (*pgxpool.Pool, string) {
	dbURL := getTestDatabaseURL()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := database.Connect(ctx, dbURL)
	if err != nil {
		t.Skipf("Skipping integration test; cannot connect to database at %s: %v", dbURL, err)
		return nil, ""
	}
	return pool, dbURL
}

func getMigrationsPath() string {
	candidates := []string{"../../migrations", "../migrations", "migrations"}
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && fi.IsDir() {
			return "file://" + c
		}
	}
	return "file://migrations"
}

func TestMigration_Version14To15Ordering(t *testing.T) {
	pool, dbURL := connectTestDB(t)
	defer pool.Close()

	ctx := context.Background()

	// Locate migrations directory
	migrationsPath := getMigrationsPath()
	m, err := migrate.New(migrationsPath, dbURL)
	require.NoError(t, err, "failed to initialize migrate")
	defer m.Close()

	// Ensure we are at migration 15 first
	err = m.Up()
	if err != nil && err != migrate.ErrNoChange {
		require.NoError(t, err, "migrating up failed")
	}

	version, dirty, err := m.Version()
	require.NoError(t, err)
	require.False(t, dirty)
	require.Equal(t, uint(15), version)

	// Step 1: Rollback 1 step (to version 14)
	err = m.Steps(-1)
	require.NoError(t, err, "rollback to version 14 failed")

	version, dirty, err = m.Version()
	require.NoError(t, err)
	require.False(t, dirty)
	require.Equal(t, uint(14), version)

	// Verify networks column is absent from services and service_deployments at version 14
	var colCount int
	err = pool.QueryRow(ctx, `
		SELECT COUNT(*) 
		FROM information_schema.columns 
		WHERE table_name = 'services' AND column_name = 'networks'
	`).Scan(&colCount)
	require.NoError(t, err)
	assert.Equal(t, 0, colCount, "networks column must NOT exist on services at migration 14")

	err = pool.QueryRow(ctx, `
		SELECT COUNT(*) 
		FROM information_schema.columns 
		WHERE table_name = 'service_deployments' AND column_name = 'networks'
	`).Scan(&colCount)
	require.NoError(t, err)
	assert.Equal(t, 0, colCount, "networks column must NOT exist on service_deployments at migration 14")

	// Step 2: Migrate 1 step forward (to version 15)
	err = m.Steps(1)
	require.NoError(t, err, "applying migration 15 from version 14 failed")

	version, dirty, err = m.Version()
	require.NoError(t, err)
	require.False(t, dirty)
	require.Equal(t, uint(15), version)

	// Verify networks column is present in both tables at version 15
	err = pool.QueryRow(ctx, `
		SELECT COUNT(*) 
		FROM information_schema.columns 
		WHERE table_name = 'services' AND column_name = 'networks'
	`).Scan(&colCount)
	require.NoError(t, err)
	assert.Equal(t, 1, colCount, "networks column must exist on services at migration 15")

	err = pool.QueryRow(ctx, `
		SELECT COUNT(*) 
		FROM information_schema.columns 
		WHERE table_name = 'service_deployments' AND column_name = 'networks'
	`).Scan(&colCount)
	require.NoError(t, err)
	assert.Equal(t, 1, colCount, "networks column must exist on service_deployments at migration 15")
}

func TestProjectCreation_WithComposeSemanticsRegression(t *testing.T) {
	pool, _ := connectTestDB(t)
	defer pool.Close()

	ctx := context.Background()

	// Ensure migration 15 has been applied
	var colCount int
	err := pool.QueryRow(ctx, `
		SELECT COUNT(*) 
		FROM information_schema.columns 
		WHERE table_name = 'services' AND column_name = 'networks'
	`).Scan(&colCount)
	require.NoError(t, err)
	require.Equal(t, 1, colCount, "database must have migration 15 applied for this test")

	// Create test user
	userID := uuid.New()
	userEmail := fmt.Sprintf("test-%s@forgelab.local", userID.String()[:8])
	_, err = pool.Exec(ctx, `
		INSERT INTO users (id, email, password_hash, display_name)
		VALUES ($1, $2, 'hash123', 'Test User')
	`, userID, userEmail)
	require.NoError(t, err)
	defer func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", userID)
	}()

	tempRepoDir, err := os.MkdirTemp("", "forgelab-test-repo-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempRepoDir)

	sourceSvc := services.NewSourceService(pool, tempRepoDir)
	projectSvc := services.NewProjectService(pool, nil, sourceSvc, nil)

	// Build service with all 5 compose semantic properties
	expectedClassification := "worker"
	expectedDependsOn := []string{"redis", "postgres"}
	expectedVolumes := []models.VolumeMountConfig{
		{
			Source:   "app_cache",
			Target:   "/cache",
			Type:     models.VolumeTypeNamed,
			ReadOnly: false,
		},
	}
	expectedNetworks := []string{"frontend_net", "backend_net"}
	expectedHealthCheck := &models.HealthCheckConfig{
		Strategy:           "docker",
		Test:               []string{"CMD-SHELL", "curl -f http://localhost:8080/health || exit 1"},
		IntervalSeconds:    10,
		TimeoutSeconds:     5,
		Retries:            3,
		StartPeriodSeconds: 15,
	}

	createInput := services.CreateProjectInput{
		Name:               fmt.Sprintf("Regression-Project-%s", userID.String()[:8]),
		SourceType:         models.SourceTypeLocalDirectory,
		RepositoryPath:     tempRepoDir,
		DeploymentStrategy: "compose",
		Services: []services.CreateServiceInput{
			{
				Name:              "worker-svc",
				Role:              "worker",
				Classification:    expectedClassification,
				Image:             "alpine:latest",
				DependsOn:         expectedDependsOn,
				Volumes:           expectedVolumes,
				Networks:          expectedNetworks,
				HealthCheckConfig: expectedHealthCheck,
				RuntimeType:       "generic",
				InternalPort:      8080,
				PublicExposed:     false,
			},
		},
	}

	// Create project and service
	project, err := projectSvc.CreateProject(ctx, userID, createInput)
	require.NoError(t, err, "CreateProject must succeed with all compose properties against current schema")
	require.NotNil(t, project)
	require.Len(t, project.Services, 1)

	createdSvc := project.Services[0]
	assert.Equal(t, expectedClassification, createdSvc.Classification)
	assert.Equal(t, expectedDependsOn, createdSvc.DependsOn)
	assert.Equal(t, expectedVolumes, createdSvc.Volumes)
	assert.Equal(t, expectedNetworks, createdSvc.Networks)
	require.NotNil(t, createdSvc.HealthCheckConfig)
	assert.Equal(t, expectedHealthCheck.Test, createdSvc.HealthCheckConfig.Test)
	assert.Equal(t, expectedHealthCheck.Retries, createdSvc.HealthCheckConfig.Retries)

	// Verify directly from PostgreSQL relation
	var (
		dbClassification string
		dbDependsOnJSON  []byte
		dbVolumesJSON    []byte
		dbNetworksJSON   []byte
		dbHealthCheckJSON []byte
	)

	err = pool.QueryRow(ctx, `
		SELECT classification, depends_on, volumes, networks, healthcheck_config
		FROM services
		WHERE id = $1
	`, createdSvc.ID).Scan(
		&dbClassification,
		&dbDependsOnJSON,
		&dbVolumesJSON,
		&dbNetworksJSON,
		&dbHealthCheckJSON,
	)
	require.NoError(t, err)

	assert.Equal(t, expectedClassification, dbClassification)

	var actualDependsOn []string
	require.NoError(t, json.Unmarshal(dbDependsOnJSON, &actualDependsOn))
	assert.Equal(t, expectedDependsOn, actualDependsOn)

	var actualVolumes []models.VolumeMountConfig
	require.NoError(t, json.Unmarshal(dbVolumesJSON, &actualVolumes))
	assert.Equal(t, expectedVolumes, actualVolumes)

	var actualNetworks []string
	require.NoError(t, json.Unmarshal(dbNetworksJSON, &actualNetworks))
	assert.Equal(t, expectedNetworks, actualNetworks)

	var actualHealthCheck models.HealthCheckConfig
	require.NoError(t, json.Unmarshal(dbHealthCheckJSON, &actualHealthCheck))
	assert.Equal(t, expectedHealthCheck.Test, actualHealthCheck.Test)
	assert.Equal(t, expectedHealthCheck.Retries, actualHealthCheck.Retries)

	// Verify GetService retrieval handles all fields
	serviceSvc := services.NewServiceService(pool)
	fetchedSvc, err := serviceSvc.GetService(ctx, createdSvc.ID)
	require.NoError(t, err)
	assert.Equal(t, expectedNetworks, fetchedSvc.Networks)
	assert.Equal(t, expectedVolumes, fetchedSvc.Volumes)
	assert.Equal(t, expectedDependsOn, fetchedSvc.DependsOn)
	assert.Equal(t, expectedClassification, fetchedSvc.Classification)
	require.NotNil(t, fetchedSvc.HealthCheckConfig)
	assert.Equal(t, expectedHealthCheck.Test, fetchedSvc.HealthCheckConfig.Test)
}

func TestMigration_FreshDatabaseAppliesAllMigrations(t *testing.T) {
	basePool, _ := connectTestDB(t)
	defer basePool.Close()

	ctx := context.Background()
	freshDBName := fmt.Sprintf("forgelab_fresh_%x", time.Now().UnixNano())

	// Create a new separate database
	_, err := basePool.Exec(ctx, fmt.Sprintf("CREATE DATABASE %s", freshDBName))
	require.NoError(t, err, "failed to create temporary test database")
	defer func() {
		_, _ = basePool.Exec(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s", freshDBName))
	}()

	freshDBURL := fmt.Sprintf("postgres://forgelab:forgelab_dev_password@localhost:5432/%s?sslmode=disable", freshDBName)
	migrationsPath := getMigrationsPath()

	m, err := migrate.New(migrationsPath, freshDBURL)
	require.NoError(t, err, "failed to initialize migrate on fresh database")
	defer m.Close()

	// Apply migrations 1 through 15
	err = m.Up()
	require.NoError(t, err, "fresh database migrations 1 through 15 must succeed")

	version, dirty, err := m.Version()
	require.NoError(t, err)
	assert.False(t, dirty, "migration state must not be dirty")
	assert.Equal(t, uint(15), version, "latest migration version must be 15")
}

func TestProjectCreation_NormalLocalProject_NoFailedToCreateProjectError(t *testing.T) {
	pool, _ := connectTestDB(t)
	defer pool.Close()

	ctx := context.Background()

	// Create test user
	userID := uuid.New()
	userEmail := fmt.Sprintf("test-normal-%s@forgelab.local", userID.String()[:8])
	_, err := pool.Exec(ctx, `
		INSERT INTO users (id, email, password_hash, display_name)
		VALUES ($1, $2, 'hash123', 'Normal Project User')
	`, userID, userEmail)
	require.NoError(t, err)
	defer func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", userID)
	}()

	tempRepoDir, err := os.MkdirTemp("", "forgelab-normal-repo-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempRepoDir)

	sourceSvc := services.NewSourceService(pool, tempRepoDir)
	projectSvc := services.NewProjectService(pool, nil, sourceSvc, nil)
	projectHandler := handlers.NewProjectHandler(projectSvc, nil, nil, nil)

	// 1. Direct ProjectService creation of normal local project (single service fallback)
	normalInput := services.CreateProjectInput{
		Name:           fmt.Sprintf("Normal-Local-App-%s", userID.String()[:8]),
		SourceType:     models.SourceTypeLocalDirectory,
		RepositoryPath: tempRepoDir,
		RuntimeType:    "nextjs",
		InternalPort:   3000,
	}

	project, err := projectSvc.CreateProject(ctx, userID, normalInput)
	require.NoError(t, err, "CreateProject for a normal local project must not fail")
	require.NotNil(t, project)
	require.Len(t, project.Services, 1)
	assert.Equal(t, "Normal-Local-App-"+userID.String()[:8], project.Services[0].Name)
	assert.Equal(t, 3000, project.Services[0].InternalPort)
	assert.Empty(t, project.Services[0].Networks)

	// 2. HTTP POST /api/projects creation of normal local project
	httpPayload := map[string]interface{}{
		"name":            fmt.Sprintf("HTTP-Local-App-%s", userID.String()[:8]),
		"source_type":     models.SourceTypeLocalDirectory,
		"repository_path": tempRepoDir,
		"runtime_type":    "go",
		"internal_port":   8080,
	}
	body, _ := json.Marshal(httpPayload)
	req := httptest.NewRequest(http.MethodPost, "/api/projects", bytes.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), middleware.UserIDKey, userID))
	rec := httptest.NewRecorder()

	projectHandler.Create(rec, req)

	// Explicitly verify response does NOT contain {"error":"failed to create project"}
	responseBody := rec.Body.String()
	assert.NotContains(t, responseBody, `{"error":"failed to create project"}`)
	assert.Equal(t, http.StatusCreated, rec.Code, "POST /api/projects must return HTTP 201 Created")

	var createdProject models.Project
	err = json.NewDecoder(rec.Body).Decode(&createdProject)
	require.NoError(t, err)
	assert.NotEmpty(t, createdProject.ID)
	assert.Equal(t, httpPayload["name"], createdProject.Name)
}

