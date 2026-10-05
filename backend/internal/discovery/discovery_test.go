package discovery_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/discovery"
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
	assert.Contains(t, svc.StartCommand, "artisan serve")
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
