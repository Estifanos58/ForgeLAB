package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/analyzer"
	"github.com/forgelab/backend/internal/detector"
	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/security"
)

// 1. Local Agent plan generation: workspace authoritative, never discovers "."
func TestLocalAgentPlanGeneration_AuthoritativeWorkspace(t *testing.T) {
	// Setup a mock local agent workspace with 'frontend' and 'backend'
	agentWorkspace := t.TempDir()

	frontDir := filepath.Join(agentWorkspace, "frontend")
	require.NoError(t, os.MkdirAll(frontDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(frontDir, "package.json"), []byte(`{"name":"frontend","scripts":{"start":"node index.js"}}`), 0644))

	backDir := filepath.Join(agentWorkspace, "backend")
	require.NoError(t, os.MkdirAll(backDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(backDir, "go.mod"), []byte("module mybackend\n\ngo 1.22\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(backDir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0644))

	// Run analyzer against the agent's real workspace
	res, err := analyzer.AnalyzeRepository(agentWorkspace)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Len(t, res.Services, 2)

	foundNames := make(map[string]bool)
	for _, s := range res.Services {
		foundNames[s.Name] = true
		assert.NotEqual(t, "project", s.Name, "must not invent a bogus 'project' service")
		assert.NotEqual(t, ".", s.SourcePath, "must not treat root as a service")
	}
	assert.True(t, foundNames["frontend"])
	assert.True(t, foundNames["backend"])
}

// 2. Compose infrastructure: postgres & redis image services, migrate job, backend service
func TestCompose_InfrastructureAndJobDetection(t *testing.T) {
	composeDir := t.TempDir()
	composeContent := `version: '3.8'
services:
  postgres:
    image: postgres:15-alpine
    environment:
      POSTGRES_DB: mydb
    ports:
      - "5432:5432"

  redis:
    image: redis:7-alpine

  migrate:
    build: .
    command: npm run migrate
    depends_on:
      postgres:
        condition: service_healthy

  backend:
    build: .
    ports:
      - "8080:8080"
    depends_on:
      migrate:
        condition: service_completed_successfully
`
	require.NoError(t, os.WriteFile(filepath.Join(composeDir, "docker-compose.yml"), []byte(composeContent), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(composeDir, "Dockerfile"), []byte("FROM alpine\nCMD [\"sh\"]\n"), 0644))

	res, err := analyzer.AnalyzeRepository(composeDir)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Len(t, res.Services, 4)

	servicesByName := make(map[string]analyzer.ServiceDefinition)
	for _, s := range res.Services {
		servicesByName[s.Name] = s
	}

	// Verify Postgres: infrastructure, image strategy, real image, no Dockerfile path
	pg := servicesByName["postgres"]
	assert.Equal(t, models.ClassificationInfrastructure, pg.Classification)
	assert.Equal(t, "image", pg.BuildStrategy)
	assert.Equal(t, "postgres:15-alpine", pg.Image)
	assert.Empty(t, pg.DockerfilePath, "image strategy services must not default dockerfile_path to Dockerfile")

	// Verify Redis: infrastructure, image strategy, real image, no Dockerfile path
	redis := servicesByName["redis"]
	assert.Equal(t, models.ClassificationInfrastructure, redis.Classification)
	assert.Equal(t, "image", redis.BuildStrategy)
	assert.Equal(t, "redis:7-alpine", redis.Image)
	assert.Empty(t, redis.DockerfilePath)

	// Verify Migrate: job classification, command preserved, depends_on preserved
	mig := servicesByName["migrate"]
	assert.Equal(t, models.ClassificationJob, mig.Classification)
	assert.Equal(t, "npm run migrate", mig.StartCommand)
	assert.Contains(t, mig.DependsOn, "postgres")
	assert.Equal(t, "service_healthy", mig.DependsOnConditions["postgres"])

	// Verify Backend: depends on migrate with service_completed_successfully
	be := servicesByName["backend"]
	assert.Contains(t, be.DependsOn, "migrate")
	assert.Equal(t, "service_completed_successfully", be.DependsOnConditions["migrate"])

	// Verify artifact reuse candidates: backend and migrate share build context and Dockerfile
	assert.Equal(t, mig.BuildContext, be.BuildContext)
	assert.Equal(t, mig.DockerfilePath, be.DockerfilePath)
}

// 3. Preflight validation: catches issues BEFORE starting any build
func TestPreflightValidation_AllChecks(t *testing.T) {
	tempRoot := t.TempDir()
	pv := security.NewPathValidator([]string{tempRoot})

	proj := &models.Project{
		ID:             uuid.New(),
		RepositoryPath: tempRoot,
		SourceType:     models.SourceTypeLocalDirectory,
	}

	t.Run("Duplicate service names fail preflight", func(t *testing.T) {
		err := ValidateDeploymentPreflight(context.Background(), PreflightOptions{
			Project: proj,
			Deployments: []*models.ServiceDeployment{
				{ServiceName: "web"},
				{ServiceName: "WEB"},
			},
			PathValidator: pv,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "duplicate service name")
	})

	t.Run("Non-existent dependency fails preflight", func(t *testing.T) {
		err := ValidateDeploymentPreflight(context.Background(), PreflightOptions{
			Project: proj,
			Deployments: []*models.ServiceDeployment{
				{ServiceName: "web", DependsOn: []string{"database"}},
			},
			PathValidator: pv,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "dependency 'database' does not exist")
	})

	t.Run("Self dependency fails preflight", func(t *testing.T) {
		err := ValidateDeploymentPreflight(context.Background(), PreflightOptions{
			Project: proj,
			Deployments: []*models.ServiceDeployment{
				{ServiceName: "web", DependsOn: []string{"web"}},
			},
			PathValidator: pv,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "self-dependency is not allowed")
	})

	t.Run("Dependency cycle fails preflight", func(t *testing.T) {
		err := ValidateDeploymentPreflight(context.Background(), PreflightOptions{
			Project: proj,
			Deployments: []*models.ServiceDeployment{
				{ServiceName: "svc-a", DependsOn: []string{"svc-b"}},
				{ServiceName: "svc-b", DependsOn: []string{"svc-c"}},
				{ServiceName: "svc-c", DependsOn: []string{"svc-a"}},
			},
			PathValidator: pv,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "dependency cycle detected")
	})

	t.Run("Image strategy with empty image fails preflight", func(t *testing.T) {
		err := ValidateDeploymentPreflight(context.Background(), PreflightOptions{
			Project: proj,
			Deployments: []*models.ServiceDeployment{
				{
					ServiceName:   "db",
					BuildStrategy: models.BuildStrategyImage,
					Image:         "",
				},
			},
			PathValidator: pv,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "requires a valid non-empty image reference")
	})

	t.Run("Dockerfile strategy with missing Dockerfile fails preflight", func(t *testing.T) {
		err := ValidateDeploymentPreflight(context.Background(), PreflightOptions{
			Project: proj,
			Deployments: []*models.ServiceDeployment{
				{
					ServiceName:    "api",
					BuildStrategy:  models.BuildStrategyDockerfile,
					DockerfilePath: "Dockerfile.missing",
				},
			},
			PathValidator: pv,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Dockerfile validation failed")
	})

	t.Run("Invalid internal port fails preflight", func(t *testing.T) {
		err := ValidateDeploymentPreflight(context.Background(), PreflightOptions{
			Project: proj,
			Deployments: []*models.ServiceDeployment{
				{
					ServiceName:   "redis",
					BuildStrategy: models.BuildStrategyImage,
					Image:         "redis:alpine",
					InternalPort:  70000,
				},
			},
			PathValidator: pv,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "out of valid range")
	})

	t.Run("Invalid volume target (not absolute) fails preflight", func(t *testing.T) {
		err := ValidateDeploymentPreflight(context.Background(), PreflightOptions{
			Project: proj,
			Deployments: []*models.ServiceDeployment{
				{
					ServiceName:   "redis",
					BuildStrategy: models.BuildStrategyImage,
					Image:         "redis:alpine",
					Volumes: []models.VolumeMountConfig{
						{Source: "data", Target: "relative/path"},
					},
				},
			},
			PathValidator: pv,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must be an absolute path starting with '/'")
	})

	t.Run("Valid deployment plan passes preflight", func(t *testing.T) {
		require.NoError(t, os.WriteFile(filepath.Join(tempRoot, "Dockerfile"), []byte("FROM alpine\n"), 0644))
		err := ValidateDeploymentPreflight(context.Background(), PreflightOptions{
			Project: proj,
			Deployments: []*models.ServiceDeployment{
				{
					ServiceName:    "redis",
					BuildStrategy:  models.BuildStrategyImage,
					Image:          "redis:alpine",
					InternalPort:   6379,
				},
				{
					ServiceName:    "backend",
					BuildStrategy:  models.BuildStrategyDockerfile,
					DockerfilePath: "Dockerfile",
					InternalPort:   8080,
					DependsOn:      []string{"redis"},
				},
			},
			PathValidator: pv,
		})
		require.NoError(t, err)
	})
}

// 4. Non-Dockerfile Next.js project: strategy auto, Dockerfile.forgelab generation
func TestNextJS_WithoutDockerfile_AutoStrategy(t *testing.T) {
	nextDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(nextDir, "package.json"), []byte(`{
		"name": "my-next-app",
		"scripts": {
			"build": "next build",
			"start": "next start"
		},
		"dependencies": {
			"next": "14.0.0",
			"react": "18.2.0"
		}
	}`), 0644))

	res, err := analyzer.AnalyzeRepository(nextDir)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Len(t, res.Services, 1)

	svc := res.Services[0]
	assert.Equal(t, models.BuildStrategyAuto, svc.BuildStrategy)
	assert.Empty(t, svc.DockerfilePath, "auto strategy should not set DockerfilePath to Dockerfile")

	// Verify Dockerfile.forgelab generation produces valid Dockerfile content
	dockerfileContent := detector.GenerateDockerfile(svc.RuntimeType, svc.InternalPort, svc.StartCommand)
	assert.Contains(t, dockerfileContent, "FROM node:")
	assert.Contains(t, dockerfileContent, "EXPOSE 3000")
	assert.Contains(t, dockerfileContent, "CMD")
}

// 5. Shared build artifact coordination & deduplication
func TestSharedBuildArtifact_Coordination(t *testing.T) {
	eng := &Engine{}

	buildKey := "test-proj::.:Dockerfile"
	var buildExecutions int32

	var wg sync.WaitGroup
	results := make([]string, 5)

	// Launch 5 concurrent workers requesting the same build artifact
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			promise, loaded := eng.getOrInitSharedBuild(buildKey)
			if !loaded {
				// This worker won the build race
				atomic.AddInt32(&buildExecutions, 1)
				time.Sleep(50 * time.Millisecond) // simulate build time
				promise.imageTag = "forgelab/test-proj/worker:1"
				promise.digest = "sha256:abc123456789"
				close(promise.done)
				results[idx] = promise.imageTag
			} else {
				// Waiting worker
				<-promise.done
				results[idx] = promise.imageTag
			}
		}(i)
	}

	wg.Wait()

	// Only 1 build should have actually run
	assert.Equal(t, int32(1), atomic.LoadInt32(&buildExecutions), "only 1 build should execute for shared context")
	for i := 0; i < 5; i++ {
		assert.Equal(t, "forgelab/test-proj/worker:1", results[i], "all workers must receive the exact same image tag")
	}
}

// 6. Command argument parser: spaces, quotes, json format
func TestParseCommandToArgs(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{"", nil},
		{"npm run migrate", []string{"npm", "run", "migrate"}},
		{`sh -c "npm run migrate && echo done"`, []string{"sh", "-c", "npm run migrate && echo done"}},
		{`python -m worker --env="production"`, []string{"python", "-m", "worker", "--env=production"}},
		{`["npm", "run", "seed"]`, []string{"npm", "run", "seed"}},
	}

	for _, tt := range tests {
		actual := parseCommandToArgs(tt.input)
		assert.Equal(t, tt.expected, actual, "failed for input: %s", tt.input)
	}
}

// 7. DAG execution tiers with depends_on_conditions
func TestDAGExecution_TiersAndConditions(t *testing.T) {
	// Setup service deployments:
	// db (no deps) -> Tier 1
	// migrate (depends on db with service_healthy) -> Tier 2
	// backend (depends on migrate with service_completed_successfully) -> Tier 3
	svcDeploys := []*models.ServiceDeployment{
		{
			ServiceName:   "backend",
			DependsOn:     []string{"migrate"},
			DependsOnConditions: map[string]string{
				"migrate": "service_completed_successfully",
			},
		},
		{
			ServiceName:   "migrate",
			Classification: models.ClassificationJob,
			DependsOn:     []string{"db"},
			DependsOnConditions: map[string]string{
				"db": "service_healthy",
			},
		},
		{
			ServiceName:   "db",
			Classification: models.ClassificationInfrastructure,
			BuildStrategy: models.BuildStrategyImage,
			Image:         "postgres:15",
		},
	}

	tiers := buildServiceDeploymentTiers(svcDeploys)
	require.Len(t, tiers, 3, "should generate 3 distinct execution tiers")

	assert.Equal(t, "db", tiers[0][0].ServiceName, "Tier 1 must be db")
	assert.Equal(t, "migrate", tiers[1][0].ServiceName, "Tier 2 must be migrate")
	assert.Equal(t, "backend", tiers[2][0].ServiceName, "Tier 3 must be backend")
}

// 8. Verify deployment path with ForgeLAB's own Compose project
func TestForgeLAB_OwnComposePipeline(t *testing.T) {
	// Root of repo contains docker-compose.yml
	var repoRoot string
	for _, cand := range []string{"/workspace", filepath.Join("..", ".."), filepath.Join("..", "..", ".."), "..", "."} {
		if _, err := os.Stat(filepath.Join(cand, "docker-compose.yml")); err == nil {
			repoRoot, _ = filepath.Abs(cand)
			break
		}
	}
	require.NotEmpty(t, repoRoot, "docker-compose.yml must be found at repo root")

	res, err := analyzer.AnalyzeRepository(repoRoot)
	require.NoError(t, err)
	require.NotNil(t, res)

	// Verify exact services discovered
	servicesMap := make(map[string]analyzer.ServiceDefinition)
	for _, s := range res.Services {
		servicesMap[s.Name] = s
	}

	assert.Len(t, res.Services, 5, "ForgeLAB compose must discover exactly 5 services: postgres, redis, migrate, backend, frontend")
	assert.NotContains(t, servicesMap, "project", "must not invent a bogus 'project' service")

	// 1. Postgres
	pg, ok := servicesMap["postgres"]
	require.True(t, ok)
	assert.Equal(t, models.ClassificationInfrastructure, pg.Classification)
	assert.Equal(t, "image", pg.BuildStrategy)
	assert.Equal(t, "postgres:16-alpine", pg.Image)
	assert.Empty(t, pg.DockerfilePath)

	// 2. Redis
	rd, ok := servicesMap["redis"]
	require.True(t, ok)
	assert.Equal(t, models.ClassificationInfrastructure, rd.Classification)
	assert.Equal(t, "image", rd.BuildStrategy)
	assert.Equal(t, "redis:7-alpine", rd.Image)
	assert.Empty(t, rd.DockerfilePath)

	// 3. Migrate (job)
	mig, ok := servicesMap["migrate"]
	require.True(t, ok)
	assert.Equal(t, models.ClassificationJob, mig.Classification)
	assert.Contains(t, mig.StartCommand, "forgelab-migrate")
	assert.Contains(t, mig.DependsOn, "postgres")
	assert.Equal(t, "service_healthy", mig.DependsOnConditions["postgres"])

	// 4. Backend
	be, ok := servicesMap["backend"]
	require.True(t, ok)
	assert.Contains(t, be.DependsOn, "postgres")
	assert.Contains(t, be.DependsOn, "redis")
	assert.Contains(t, be.DependsOn, "migrate")
	assert.Equal(t, "service_completed_successfully", be.DependsOnConditions["migrate"])

	// Shared artifact verification: migrate and backend share context and Dockerfile
	assert.Equal(t, mig.BuildContext, be.BuildContext)
	assert.Equal(t, mig.DockerfilePath, be.DockerfilePath)

	// 5. Frontend
	fe, ok := servicesMap["frontend"]
	require.True(t, ok)
	assert.Contains(t, fe.DependsOn, "backend")
	assert.Equal(t, "service_healthy", fe.DependsOnConditions["backend"])

	// Convert to ServiceDeployment models and run preflight validation
	var svcDeploys []*models.ServiceDeployment
	for _, s := range res.Services {
		svcDeploys = append(svcDeploys, &models.ServiceDeployment{
			ID:                  uuid.New(),
			ServiceName:         s.Name,
			BuildStrategy:       s.BuildStrategy,
			Image:               s.Image,
			DockerfilePath:      s.DockerfilePath,
			BuildContext:        s.BuildContext,
			InternalPort:        s.InternalPort,
			StartCommand:        s.StartCommand,
			Classification:      s.Classification,
			DependsOn:           s.DependsOn,
			DependsOnConditions: s.DependsOnConditions,
			Volumes:             s.Volumes,
			Networks:            s.Networks,
		})
	}

	pv := security.NewPathValidator([]string{repoRoot})
	proj := &models.Project{
		ID:             uuid.New(),
		RepositoryPath: repoRoot,
		SourceType:     models.SourceTypeLocalDirectory,
	}

	err = ValidateDeploymentPreflight(context.Background(), PreflightOptions{
		Project:       proj,
		Deployments:   svcDeploys,
		PathValidator: pv,
	})
	require.NoError(t, err, "ForgeLAB's own deployment plan must pass preflight validation")

	// Verify DAG execution tiers:
	// Tier 1: postgres, redis (infrastructure image)
	// Tier 2: migrate (depends on postgres)
	// Tier 3: backend (depends on migrate, postgres, redis)
	// Tier 4: frontend (depends on backend)
	tiers := buildServiceDeploymentTiers(svcDeploys)
	require.Len(t, tiers, 4, "ForgeLAB release must produce exactly 4 DAG execution tiers")

	tier1Names := map[string]bool{}
	for _, s := range tiers[0] {
		tier1Names[s.ServiceName] = true
	}
	assert.True(t, tier1Names["postgres"])
	assert.True(t, tier1Names["redis"])

	assert.Equal(t, "migrate", tiers[1][0].ServiceName)
	assert.Equal(t, "backend", tiers[2][0].ServiceName)
	assert.Equal(t, "frontend", tiers[3][0].ServiceName)
}

// 9. Independent service deployment validation vs release validation
func TestPreflightValidation_IndependentServiceVsReleaseDeployment(t *testing.T) {
	tempRoot := t.TempDir()
	pv := security.NewPathValidator([]string{tempRoot})
	require.NoError(t, os.WriteFile(filepath.Join(tempRoot, "Dockerfile"), []byte("FROM alpine\n"), 0644))

	proj := &models.Project{
		ID:             uuid.New(),
		RepositoryPath: tempRoot,
		SourceType:     models.SourceTypeLocalDirectory,
	}

	servicesInProject := map[string]*models.Service{
		"postgres": {Name: "postgres"},
		"redis":    {Name: "redis"},
		"migrate":  {Name: "migrate"},
		"backend":  {Name: "backend"},
		"frontend": {Name: "frontend"},
	}

	// 1. Independent service deployment for backend (only backend in Deployments)
	// Depends on postgres, redis, migrate which are NOT in Deployments, but ARE in project (ServicesMap).
	t.Run("ExecuteServiceDeployment allows dependencies that exist in project", func(t *testing.T) {
		err := ValidateDeploymentPreflight(context.Background(), PreflightOptions{
			Project: proj,
			Deployments: []*models.ServiceDeployment{
				{
					ServiceName:    "backend",
					BuildStrategy:  models.BuildStrategyDockerfile,
					DockerfilePath: "Dockerfile",
					InternalPort:   8080,
					DependsOn:      []string{"postgres", "redis", "migrate"},
				},
			},
			ServicesMap:         servicesInProject,
			PathValidator:       pv,
			IsServiceDeployment: true,
		})
		require.NoError(t, err, "independent service deployment must not reject valid project dependencies")
	})

	// 2. Independent service deployment with dependency that does NOT exist in project
	t.Run("ExecuteServiceDeployment rejects missing project dependency", func(t *testing.T) {
		err := ValidateDeploymentPreflight(context.Background(), PreflightOptions{
			Project: proj,
			Deployments: []*models.ServiceDeployment{
				{
					ServiceName:    "backend",
					BuildStrategy:  models.BuildStrategyDockerfile,
					DockerfilePath: "Dockerfile",
					InternalPort:   8080,
					DependsOn:      []string{"nonexistent-service"},
				},
			},
			ServicesMap:         servicesInProject,
			PathValidator:       pv,
			IsServiceDeployment: true,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "dependency 'nonexistent-service' does not exist in project")
	})

	// 3. Project release deployment requires all dependencies to be part of the deployment plan
	t.Run("Project release requires dependencies in deployment plan", func(t *testing.T) {
		err := ValidateDeploymentPreflight(context.Background(), PreflightOptions{
			Project: proj,
			Deployments: []*models.ServiceDeployment{
				{
					ServiceName:    "backend",
					BuildStrategy:  models.BuildStrategyDockerfile,
					DockerfilePath: "Dockerfile",
					InternalPort:   8080,
					DependsOn:      []string{"postgres"},
				},
			},
			ServicesMap:         servicesInProject,
			PathValidator:       pv,
			IsServiceDeployment: false, // Release deployment
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "dependency 'postgres' does not exist in deployment plan")
	})
}

// 10. Unhealthy infrastructure is not promoted to healthy/running
func TestUnhealthyInfrastructure_NotPromotedToHealthy(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/containers/test-pg/json") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"ID": "test-pg",
				"State": map[string]interface{}{
					"Status":  "running",
					"Running": true,
					"Health": map[string]interface{}{
						"Status": "unhealthy",
						"Log": []map[string]interface{}{
							{
								"Output":   "FATAL: database files are incompatible with server\n",
								"ExitCode": 1,
							},
						},
					},
				},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})

	cli, cleanup := newTestDockerClient(t, handler)
	defer cleanup()

	eng := &Engine{dockerClient: cli}

	var emittedErrors []string
	var emittedLogs []string
	logFn := func(message string) {
		emittedLogs = append(emittedLogs, message)
	}
	errLogFn := func(message string) {
		emittedErrors = append(emittedErrors, message)
	}

	healthy, _ := eng.verifyServiceHealth(
		context.Background(),
		"test-pg",
		5432,
		nil,
		models.HealthStrategyAuto,
		"/health",
		logFn,
		errLogFn,
	)
	assert.False(t, healthy, "unhealthy Docker container must NEVER be promoted to healthy")

	foundFatalDiagnostic := false
	for _, msg := range emittedErrors {
		if strings.Contains(msg, "database files are incompatible with server") {
			foundFatalDiagnostic = true
			break
		}
	}
	assert.True(t, foundFatalDiagnostic, "failure diagnostics from Docker health check must be preserved in deployment logs")
}

// 11. Compose shared build artifact linking: migrate inherits build from backend
func TestSharedBuildArtifact_MigrateBackendLinking(t *testing.T) {
	tempDir := t.TempDir()
	backendDir := filepath.Join(tempDir, "backend")
	require.NoError(t, os.MkdirAll(backendDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(backendDir, "Dockerfile"), []byte("FROM golang:alpine\n"), 0644))

	composeYAML := `
services:
  migrate:
    image: forgelab-backend:latest
    command: ["./forgelab-migrate", "up"]
  backend:
    image: forgelab-backend:latest
    build:
      context: ./backend
      dockerfile: Dockerfile
`
	require.NoError(t, os.WriteFile(filepath.Join(tempDir, "docker-compose.yml"), []byte(composeYAML), 0644))

	res, err := analyzer.AnalyzeRepository(tempDir)
	require.NoError(t, err)
	require.Len(t, res.Services, 2)

	servicesMap := make(map[string]analyzer.ServiceDefinition)
	for _, s := range res.Services {
		servicesMap[s.Name] = s
	}

	be := servicesMap["backend"]
	mig := servicesMap["migrate"]

	assert.Equal(t, models.BuildStrategyDockerfile, be.BuildStrategy)
	assert.Equal(t, "./backend", be.BuildContext)
	assert.Equal(t, "Dockerfile", be.DockerfilePath)

	assert.Equal(t, models.BuildStrategyDockerfile, mig.BuildStrategy, "migrate should adopt dockerfile build strategy from backend")
	assert.Equal(t, be.BuildContext, mig.BuildContext, "migrate should share build context with backend")
	assert.Equal(t, be.DockerfilePath, mig.DockerfilePath, "migrate should share dockerfile with backend")
}

