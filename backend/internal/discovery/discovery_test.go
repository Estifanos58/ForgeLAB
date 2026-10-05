package discovery_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/discovery"
	"github.com/forgelab/backend/internal/models"
)

func TestDiscover_DockerCompose(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-discovery-compose-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	composeYAML := `version: '3.8'
services:
  web:
    build:
      context: ./web
      dockerfile: Dockerfile
    ports:
      - "3000:3000"
    depends_on:
      - api
    environment:
      - API_URL=http://api:8080

  api:
    build: ./api
    ports:
      - "8080:8080"
    depends_on:
      - db
      - redis
    environment:
      DATABASE_URL: postgres://user:pass@db:5432/main
      REDIS_URL: redis://redis:6379

  worker:
    build: ./api
    command: python worker.py
    depends_on:
      - redis
      - db

  db:
    image: postgres:16-alpine
    volumes:
      - pgdata:/var/lib/postgresql/data
    environment:
      POSTGRES_PASSWORD: secretpassword
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U postgres"]
      interval: 5s
      timeout: 3s
      retries: 5

  redis:
    image: redis:7-alpine
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]

volumes:
  pgdata:
`
	err = os.WriteFile(filepath.Join(tempDir, "compose.yaml"), []byte(composeYAML), 0644)
	require.NoError(t, err)

	res, err := discovery.Discover(tempDir)
	require.NoError(t, err)
	require.NotNil(t, res)

	assert.Equal(t, discovery.TopologyCompose, res.Topology.Type)
	assert.Equal(t, discovery.StrategyCompose, res.PrimaryStrategy)
	assert.Len(t, res.Services, 5)

	// Verify service classifications
	svcMap := make(map[string]discovery.DiscoveredService)
	for _, s := range res.Services {
		svcMap[s.Name] = s
	}

	assert.Equal(t, discovery.ClassificationInfrastructure, svcMap["db"].Classification)
	assert.Equal(t, discovery.ClassificationInfrastructure, svcMap["redis"].Classification)
	assert.Equal(t, discovery.ClassificationWorker, svcMap["worker"].Classification)
	assert.Equal(t, discovery.ClassificationApplication, svcMap["web"].Classification)
	assert.Equal(t, discovery.ClassificationApplication, svcMap["api"].Classification)

	// Verify volumes
	assert.Len(t, svcMap["db"].Volumes, 1)
	assert.Equal(t, "pgdata", svcMap["db"].Volumes[0].Source)
	assert.Equal(t, "volume", svcMap["db"].Volumes[0].Type)

	// Verify healthchecks
	assert.Equal(t, "docker", svcMap["db"].HealthCheck.Strategy)
	assert.Contains(t, svcMap["db"].HealthCheck.Test, "pg_isready -U postgres")

	// Verify DAG execution tiers
	plan, err := discovery.GeneratePlan(res, discovery.PlanOptions{
		SourceType:     "local_directory",
		SourceRevision: "rev-test-123",
	})
	require.NoError(t, err)
	require.NotNil(t, plan)

	// Tier 0 must contain db and redis (no dependencies)
	require.GreaterOrEqual(t, len(plan.ExecutionTiers), 3)
	tier0 := plan.ExecutionTiers[0]
	assert.Contains(t, tier0, "db")
	assert.Contains(t, tier0, "redis")

	// Public endpoints: web and api should have public endpoints, db/redis/worker should not
	publicServices := make(map[string]bool)
	for _, ep := range plan.PublicEndpoints {
		publicServices[ep.ServiceName] = true
	}
	assert.True(t, publicServices["web"])
	assert.True(t, publicServices["api"])
	assert.False(t, publicServices["db"])
	assert.False(t, publicServices["redis"])
}

func TestDiscover_StaticSite(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-discovery-static-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	htmlContent := `<!DOCTYPE html><html><head><title>Test App</title></head><body>Hello</body></html>`
	err = os.WriteFile(filepath.Join(tempDir, "index.html"), []byte(htmlContent), 0644)
	require.NoError(t, err)

	res, err := discovery.Discover(tempDir)
	require.NoError(t, err)
	require.NotNil(t, res)

	assert.Equal(t, discovery.TopologySingleService, res.Topology.Type)
	require.Len(t, res.Services, 1)
	svc := res.Services[0]
	assert.Equal(t, "static", svc.Runtime)
	assert.Equal(t, "Static HTML/CSS/JS", svc.Framework)
	assert.Equal(t, 80, svc.InternalPort)

	df := discovery.GenerateDockerfile(svc.Runtime, svc.InternalPort, svc.StartCommand, svc.PackageManager)
	assert.Contains(t, df, "FROM nginx:alpine")
	assert.Contains(t, df, "EXPOSE 80")
}

func TestDiscover_Laravel(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-discovery-laravel-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	composerJSON := `{"name": "test/laravel-app", "require": {"php": "^8.2", "laravel/framework": "^10.0"}}`
	err = os.WriteFile(filepath.Join(tempDir, "composer.json"), []byte(composerJSON), 0644)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(tempDir, "artisan"), []byte("#!/usr/bin/env php\n<?php"), 0755)
	require.NoError(t, err)

	res, err := discovery.Discover(tempDir)
	require.NoError(t, err)
	require.NotNil(t, res)

	require.Len(t, res.Services, 1)
	svc := res.Services[0]
	assert.Equal(t, "php", svc.Runtime)
	assert.Equal(t, "Laravel", svc.Framework)
	assert.Equal(t, 8000, svc.InternalPort)
	assert.Contains(t, svc.StartCommand, "public")
}

func TestBuildDAG_CycleDetection(t *testing.T) {
	services := []discovery.DiscoveredService{
		{Name: "svc-a", DependsOn: []string{"svc-b"}},
		{Name: "svc-b", DependsOn: []string{"svc-a"}},
	}

	_, _, err := discovery.BuildDAG(services)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cyclic service dependency")
}

func TestDiscover_SecretRedaction(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-secret-redact-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	composeYAML := `version: '3.8'
services:
  app:
    image: myapp:1.0
    ports:
      - "8080:8080"
    environment:
      - PUBLIC_PORT=8080
      - APP_NAME=ForgeDemo
      - DATABASE_PASSWORD=supersecret_pass123
      - JWT_SECRET=shhhh_topsecret
      - API_KEY=xyz987secret
`
	err = os.WriteFile(filepath.Join(tempDir, "compose.yaml"), []byte(composeYAML), 0644)
	require.NoError(t, err)

	envContent := `ROOT_API_KEY=root_secret_999
NORMAL_SETTING=production
`
	err = os.WriteFile(filepath.Join(tempDir, ".env"), []byte(envContent), 0644)
	require.NoError(t, err)

	res, err := discovery.Discover(tempDir)
	require.NoError(t, err)
	require.NotNil(t, res)

	// Check discovery result secret redaction
	for _, env := range res.Topology.RootEnvVars {
		if env.IsSecret {
			assert.Equal(t, "[REDACTED]", env.Value, "Root secret %s must be redacted in discovery", env.Key)
		}
	}

	require.Len(t, res.Services, 1)
	svc := res.Services[0]
	for _, env := range svc.Environment {
		if env.IsSecret {
			assert.Equal(t, "[REDACTED]", env.Value, "Service secret %s must be redacted in discovery", env.Key)
		}
	}

	// Check plan generation secret redaction
	plan, err := discovery.GeneratePlan(res, discovery.PlanOptions{
		SourceType:     "local_directory",
		SourceRevision: "fp_test123456",
	})
	require.NoError(t, err)
	require.NotNil(t, plan)

	for _, env := range plan.Environment {
		if env.IsSecret {
			assert.Equal(t, "[REDACTED]", env.Value, "Plan secret %s must be redacted", env.Key)
			assert.Equal(t, "source", env.ActiveValue, "Plan secret %s active_value provenance is source", env.Key)
		} else {
			assert.NotEqual(t, "[REDACTED]", env.Value, "Non-secret %s must not be redacted", env.Key)
		}
	}

	for _, psvc := range plan.Services {
		for _, env := range psvc.Environment {
			if env.IsSecret {
				assert.Equal(t, "[REDACTED]", env.Value, "PlannedService secret %s must be redacted", env.Key)
				assert.Equal(t, "source", env.ActiveValue, "PlannedService secret %s active_value provenance is source", env.Key)
			}
		}
	}
}

func TestComputeDirectoryContentFingerprint_Deterministic(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-fingerprint-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	err = os.WriteFile(filepath.Join(tempDir, "main.go"), []byte("package main\nfunc main() {}\n"), 0644)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(tempDir, "go.mod"), []byte("module test\ngo 1.22\n"), 0644)
	require.NoError(t, err)

	fp1, err := discovery.ComputeDirectoryContentFingerprint(tempDir)
	require.NoError(t, err)
	assert.True(t, len(fp1) > 10)
	assert.Contains(t, fp1, "fp_")

	// Idempotent: repeated computation produces identical hash
	fp2, err := discovery.ComputeDirectoryContentFingerprint(tempDir)
	require.NoError(t, err)
	assert.Equal(t, fp1, fp2)

	// Changing pruned directories (.git, node_modules) does not change content fingerprint
	err = os.MkdirAll(filepath.Join(tempDir, ".git"), 0755)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(tempDir, ".git", "config"), []byte("[core]\n"), 0644)
	require.NoError(t, err)

	fp3, err := discovery.ComputeDirectoryContentFingerprint(tempDir)
	require.NoError(t, err)
	assert.Equal(t, fp1, fp3, "Pruned directory changes must not alter content fingerprint")

	// Changing application content changes the fingerprint
	err = os.WriteFile(filepath.Join(tempDir, "main.go"), []byte("package main\n// updated\nfunc main() {}\n"), 0644)
	require.NoError(t, err)

	fp4, err := discovery.ComputeDirectoryContentFingerprint(tempDir)
	require.NoError(t, err)
	assert.NotEqual(t, fp1, fp4, "Content changes must alter fingerprint")
}

func TestCompose_EnvFileParsingAndOverride(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-envfile-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	// Write .env.base and .env.custom
	err = os.WriteFile(filepath.Join(tempDir, ".env.base"), []byte("BASE_KEY=base_val\nSHARED=from_base\nSECRET_KEY=base_secret\n"), 0644)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(tempDir, ".env.custom"), []byte("CUSTOM_KEY=custom_val\nSHARED=from_custom\n"), 0644)
	require.NoError(t, err)

	// Test list form env_file with explicit override in environment
	composeYAML := `version: '3.8'
services:
  web:
    image: nginx:alpine
    env_file:
      - .env.base
      - .env.custom
    environment:
      - SHARED=from_environment
      - EXPLICIT=explicit_val
`
	err = os.WriteFile(filepath.Join(tempDir, "compose.yaml"), []byte(composeYAML), 0644)
	require.NoError(t, err)

	res, err := discovery.Discover(tempDir)
	require.NoError(t, err)
	require.Len(t, res.Services, 1)

	svc := res.Services[0]
	envMap := make(map[string]discovery.EnvironmentProvenance)
	for _, env := range svc.Environment {
		envMap[env.Key] = env
	}

	assert.Equal(t, "base_val", envMap["BASE_KEY"].Value)
	assert.Equal(t, "custom_val", envMap["CUSTOM_KEY"].Value)
	// Explicit environment overrides env_file
	assert.Equal(t, "from_environment", envMap["SHARED"].Value)
	assert.Equal(t, "explicit_val", envMap["EXPLICIT"].Value)
	// Secret key was redacted
	assert.True(t, envMap["SECRET_KEY"].IsSecret)
	assert.Equal(t, "[REDACTED]", envMap["SECRET_KEY"].Value)
}

func TestCompose_VolumeSemantics_NamedVsBind_LongForm(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-volumes-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	composeYAML := `version: '3.8'
services:
  db:
    image: postgres:16
    volumes:
      - pgdata:/var/lib/postgresql/data
      - ./init.sql:/docker-entrypoint-initdb.d/init.sql:ro
      - type: bind
        source: ./configs
        target: /etc/postgresql/custom
        read_only: true
      - type: volume
        source: cache_vol
        target: /var/cache

volumes:
  pgdata:
  cache_vol:
`
	err = os.WriteFile(filepath.Join(tempDir, "compose.yaml"), []byte(composeYAML), 0644)
	require.NoError(t, err)

	res, err := discovery.Discover(tempDir)
	require.NoError(t, err)
	require.Len(t, res.Services, 1)

	vols := res.Services[0].Volumes
	require.Len(t, vols, 4)

	// Volume 0: Short named volume
	assert.Equal(t, "pgdata", vols[0].Source)
	assert.Equal(t, "/var/lib/postgresql/data", vols[0].Target)
	assert.Equal(t, "volume", vols[0].Type)
	assert.False(t, vols[0].ReadOnly)

	// Volume 1: Short bind mount with :ro
	assert.Equal(t, "./init.sql", vols[1].Source)
	assert.Equal(t, "/docker-entrypoint-initdb.d/init.sql", vols[1].Target)
	assert.Equal(t, "bind", vols[1].Type)
	assert.True(t, vols[1].ReadOnly)

	// Volume 2: Long-form bind mount with read_only: true
	assert.Equal(t, "./configs", vols[2].Source)
	assert.Equal(t, "/etc/postgresql/custom", vols[2].Target)
	assert.Equal(t, "bind", vols[2].Type)
	assert.True(t, vols[2].ReadOnly)

	// Volume 3: Long-form volume mount
	assert.Equal(t, "cache_vol", vols[3].Source)
	assert.Equal(t, "/var/cache", vols[3].Target)
	assert.Equal(t, "volume", vols[3].Type)
	assert.False(t, vols[3].ReadOnly)
}

func TestCompose_NetworksAndDisabledHealthcheck(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-net-hc-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	composeYAML := `version: '3.8'
services:
  web:
    image: nginx:alpine
    networks:
      - frontend
      - backend
    healthcheck:
      disable: true

networks:
  frontend:
  backend:
`
	err = os.WriteFile(filepath.Join(tempDir, "compose.yaml"), []byte(composeYAML), 0644)
	require.NoError(t, err)

	res, err := discovery.Discover(tempDir)
	require.NoError(t, err)
	require.Len(t, res.Services, 1)

	svc := res.Services[0]
	// Network membership
	assert.ElementsMatch(t, []string{"frontend", "backend"}, svc.Networks)

	// Disabled healthcheck
	assert.Equal(t, models.HealthStrategyNone, svc.HealthCheck.Strategy)
	assert.Equal(t, []string{"NONE"}, svc.HealthCheck.Test)
	assert.False(t, svc.HealthCheckEnabled)

	// Verify plan aggregates networks and preserves disabled healthcheck
	plan, err := discovery.GeneratePlan(res, discovery.PlanOptions{
		SourceType:     "local_directory",
		SourceRevision: "fp_networks_test",
	})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"frontend", "backend"}, plan.Networks)
	require.Len(t, plan.Services, 1)
	assert.ElementsMatch(t, []string{"frontend", "backend"}, plan.Services[0].Networks)
	assert.Equal(t, models.HealthStrategyNone, plan.Services[0].HealthCheck.Strategy)
}

func TestAPIModelSerializationCompatibility(t *testing.T) {
	// Test VolumeMountConfig deserialization with frontend legacy fields (name, container_path)
	legacyVolJSON := `{"name": "my-vol", "container_path": "/var/lib/data", "read_only": true}`
	var v models.VolumeMountConfig
	err := json.Unmarshal([]byte(legacyVolJSON), &v)
	require.NoError(t, err)
	assert.Equal(t, "my-vol", v.Source)
	assert.Equal(t, "/var/lib/data", v.Target)
	assert.Equal(t, "volume", v.Type)
	assert.True(t, v.ReadOnly)

	// Test HealthCheckConfig deserialization with string duration aliases (interval, timeout, start_period)
	legacyHCJSON := `{"strategy": "auto", "test": ["CMD", "curl", "localhost:8080/health"], "interval": "10s", "timeout": "5s", "retries": 3, "start_period": "30s"}`
	var hc models.HealthCheckConfig
	err = json.Unmarshal([]byte(legacyHCJSON), &hc)
	require.NoError(t, err)
	assert.Equal(t, 10, hc.IntervalSeconds)
	assert.Equal(t, 5, hc.TimeoutSeconds)
	assert.Equal(t, 3, hc.Retries)
	assert.Equal(t, 30, hc.StartPeriodSeconds)

	// Test HealthCheckConfig disable detection
	disabledHCJSON := `{"strategy": "none", "test": ["NONE"]}`
	var hcNone models.HealthCheckConfig
	err = json.Unmarshal([]byte(disabledHCJSON), &hcNone)
	require.NoError(t, err)
	assert.Equal(t, models.HealthStrategyNone, hcNone.Strategy)
}

func TestProductionRuntimeCandidates_Recommendations(t *testing.T) {
	// 1. Django production start recommendation
	djangoFiles := map[string][]byte{
		"manage.py":        []byte("#!/usr/bin/env python"),
		"requirements.txt": []byte("django>=4.2\npsycopg2-binary\n"),
		"wsgi.py":          []byte("import os"),
	}
	runtime, framework, _, port, _, _, _, startCmd := discovery.DetectTechnology(djangoFiles)
	assert.Equal(t, "python-django", runtime)
	assert.Equal(t, "Django", framework)
	assert.Equal(t, 8000, port)
	assert.Contains(t, startCmd, "gunicorn")
	assert.Contains(t, startCmd, "0.0.0.0:8000")

	rec := discovery.RecommendProductionExecution("python", "Django", "pip", 8000, "/health", models.HealthStrategyHTTP)
	assert.Contains(t, rec.ProdStartCommand, "gunicorn")
	require.Len(t, rec.Candidates, 2)
	assert.Equal(t, "Django Production (Gunicorn)", rec.Candidates[0].Name)
	assert.Greater(t, rec.Candidates[0].Confidence, rec.Candidates[1].Confidence)

	// 2. Flask production start recommendation
	flaskFiles := map[string][]byte{
		"app.py":           []byte("from flask import Flask\napp = Flask(__name__)"),
		"requirements.txt": []byte("flask>=3.0\n"),
	}
	fRuntime, fFramework, _, fPort, _, _, _, fStartCmd := discovery.DetectTechnology(flaskFiles)
	assert.Equal(t, "python-flask", fRuntime)
	assert.Equal(t, "Flask", fFramework)
	assert.Equal(t, 5000, fPort)
	assert.Contains(t, fStartCmd, "gunicorn")

	fRec := discovery.RecommendProductionExecution("python", "Flask", "pip", 5000, "/health", models.HealthStrategyHTTP)
	assert.Contains(t, fRec.ProdStartCommand, "gunicorn")
	require.Len(t, fRec.Candidates, 2)

	// 3. Rails production start recommendation
	railsFiles := map[string][]byte{
		"Gemfile":        []byte("source 'https://rubygems.org'\ngem 'rails', '~> 7.1'"),
		"config/puma.rb": []byte("port ENV.fetch('PORT') { 3000 }"),
	}
	rRuntime, rFramework, _, _, _, _, _, rStartCmd := discovery.DetectTechnology(railsFiles)
	assert.Equal(t, "ruby", rRuntime)
	assert.Equal(t, "Ruby on Rails", rFramework)
	assert.Contains(t, rStartCmd, "puma")

	rRec := discovery.RecommendProductionExecution("ruby", "Ruby on Rails", "bundler", 3000, "/up", models.HealthStrategyHTTP)
	assert.Contains(t, rRec.ProdStartCommand, "puma")
	require.Len(t, rRec.Candidates, 2)
}
