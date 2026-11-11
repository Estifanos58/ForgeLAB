package docker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	dockernetwork "github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/models"
)

func TestLive_SelfDeployment_FourTiers(t *testing.T) {
	if testing.Short() || os.Getenv("SKIP_DOCKER_LIVE_TEST") != "" {
		t.Skip("Skipping live Docker deployment test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Skipf("Docker daemon not available: %v", err)
	}
	defer cli.Close()

	if _, err := cli.Ping(ctx, client.PingOptions{}); err != nil {
		t.Skipf("Docker daemon ping failed: %v", err)
	}

	eng := &Engine{dockerClient: cli}

	// Create dedicated bridge network
	testNetName := fmt.Sprintf("test-selfdeploy-%s", uuid.New().String()[:8])
	_, err = cli.NetworkCreate(ctx, testNetName, client.NetworkCreateOptions{Driver: "bridge"})
	require.NoError(t, err)
	defer func() {
		_, _ = cli.NetworkRemove(context.Background(), testNetName, client.NetworkRemoveOptions{})
	}()

	var containersToClean []string
	defer func() {
		for _, cID := range containersToClean {
			_, _ = cli.ContainerStop(context.Background(), cID, client.ContainerStopOptions{})
			_, _ = cli.ContainerRemove(context.Background(), cID, client.ContainerRemoveOptions{Force: true})
		}
	}()

	t.Log("====================================================")
	t.Log("Starting ForgeLAB Self-Deployment Verification")
	t.Log("Expected sequence: 5 services -> 4 tiers")
	t.Log("Tier 1: postgres + redis")
	t.Log("Tier 2: migrate")
	t.Log("Tier 3: backend")
	t.Log("Tier 4: frontend")
	t.Log("====================================================")

	// Ensure required infrastructure and frontend images are locally available or pulled/built
	ensureImageAvailable := func(img string) {
		t.Logf("Checking image %s...", img)
		_, inspectErr := cli.ImageInspect(ctx, img)
		if inspectErr == nil {
			return
		}
		t.Logf("Pulling image %s...", img)
		reader, pullErr := cli.ImagePull(ctx, img, client.ImagePullOptions{})
		if pullErr == nil && reader != nil {
			_, _ = io.Copy(io.Discard, reader)
			_ = reader.Close()
			return
		}
		require.NoError(t, inspectErr, "image %s is not available locally and pull failed: %v", img, pullErr)
	}

	ensureFrontendImage := func() {
		_, inspectErr := cli.ImageInspect(ctx, "forgelab-frontend:latest")
		if inspectErr == nil {
			t.Log("Image forgelab-frontend:latest is already available.")
			return
		}
		t.Log("Image forgelab-frontend:latest not found in local cache; creating reproducible test frontend image...")
		ensureImageAvailable("alpine:3.19")
		feTarData := createTarArchiveWithFiles(t, map[string][]byte{
			"Dockerfile": []byte("FROM alpine:3.19\nRUN apk add --no-cache busybox-extras && mkdir -p /var/www && echo 'ok' > /var/www/index.html\nEXPOSE 3000\nCMD [\"httpd\", \"-f\", \"-p\", \"3000\", \"-h\", \"/var/www\"]\n"),
		})
		feOpts := client.ImageBuildOptions{
			Tags:       []string{"forgelab-frontend:latest"},
			Dockerfile: "Dockerfile",
			Remove:     true,
		}
		feBuildErr := eng.buildImage(ctx, io.NopCloser(bytes.NewReader(feTarData)), feOpts, func(msg string) {
			t.Logf("[frontend-build] %s", msg)
		})
		require.NoError(t, feBuildErr, "building forgelab-frontend test image must succeed")
	}

	t.Log("Ensuring dependencies for reproducible execution...")
	ensureImageAvailable("postgres:16-alpine")
	ensureImageAvailable("redis:7-alpine")
	ensureImageAvailable("golang:alpine")
	ensureImageAvailable("alpine:3.19")
	ensureFrontendImage()

	// -------------------------------------------------------------------------
	// TIER 1: postgres + redis
	// -------------------------------------------------------------------------
	t.Log("--- Tier 1: Starting postgres and redis ---")

	// 1a. postgres
	pgHostConfig, err := ValidateAndBuildSecureHostConfig(ContainerSecurityOptions{})
	require.NoError(t, err)
	pgResp, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image: "postgres:16-alpine",
			Env: []string{
				"POSTGRES_USER=forgelab",
				"POSTGRES_PASSWORD=forgelab_dev_password",
				"POSTGRES_DB=forgelab",
			},
			Healthcheck: &container.HealthConfig{
				Test:        []string{"CMD-SHELL", "pg_isready -U forgelab -d forgelab"},
				Interval:    5 * time.Second,
				Timeout:     5 * time.Second,
				Retries:     20,
				StartPeriod: 45 * time.Second,
			},
		},
		HostConfig: pgHostConfig,
		NetworkingConfig: &dockernetwork.NetworkingConfig{
			EndpointsConfig: map[string]*dockernetwork.EndpointSettings{
				testNetName: {Aliases: []string{"postgres"}},
			},
		},
		Name: fmt.Sprintf("test-pg-%s", uuid.New().String()[:8]),
	})
	require.NoError(t, err)
	containersToClean = append(containersToClean, pgResp.ID)
	_, err = cli.ContainerStart(ctx, pgResp.ID, client.ContainerStartOptions{})
	require.NoError(t, err)

	// 1b. redis (verify nofile default does not trigger 10032 max open files failure)
	redisHostConfig, err := ValidateAndBuildSecureHostConfig(ContainerSecurityOptions{})
	require.NoError(t, err)
	redisResp, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image: "redis:7-alpine",
			Healthcheck: &container.HealthConfig{
				Test:     []string{"CMD", "redis-cli", "ping"},
				Interval: 5 * time.Second,
				Timeout:  5 * time.Second,
				Retries:  5,
			},
		},
		HostConfig: redisHostConfig,
		NetworkingConfig: &dockernetwork.NetworkingConfig{
			EndpointsConfig: map[string]*dockernetwork.EndpointSettings{
				testNetName: {Aliases: []string{"redis"}},
			},
		},
		Name: fmt.Sprintf("test-redis-%s", uuid.New().String()[:8]),
	})
	require.NoError(t, err)
	containersToClean = append(containersToClean, redisResp.ID)
	_, err = cli.ContainerStart(ctx, redisResp.ID, client.ContainerStartOptions{})
	require.NoError(t, err)

	// Verify Tier 1 health
	t.Log("Verifying postgres health...")
	pgHealthy, pgReason := eng.verifyServiceHealth(
		ctx,
		pgResp.ID,
		5432,
		nil,
		models.HealthStrategyDocker,
		"/",
		func(msg string) { t.Logf("[postgres] %s", msg) },
		func(msg string) { t.Logf("[postgres ERR] %s", msg) },
		&models.HealthCheckConfig{
			StartPeriodSeconds: 45,
			IntervalSeconds:    5,
			TimeoutSeconds:     5,
			Retries:            20,
		},
	)
	require.True(t, pgHealthy, "postgres must become healthy; reason: %s", pgReason)
	t.Logf("postgres -> starts -> healthy (%s)", pgReason)

	t.Log("Verifying redis health...")
	redisHealthy, redisReason := eng.verifyServiceHealth(
		ctx,
		redisResp.ID,
		6379,
		nil,
		models.HealthStrategyDocker,
		"/",
		func(msg string) { t.Logf("[redis] %s", msg) },
		func(msg string) { t.Logf("[redis ERR] %s", msg) },
		&models.HealthCheckConfig{
			IntervalSeconds: 5,
			TimeoutSeconds:  5,
			Retries:         5,
		},
	)
	require.True(t, redisHealthy, "redis must become healthy; reason: %s", redisReason)
	t.Logf("redis -> starts -> healthy (%s)", redisReason)

	// Invariant: Postgres and Redis must remain running after health verification
	pgInspect, err := cli.ContainerInspect(ctx, pgResp.ID, client.ContainerInspectOptions{})
	require.NoError(t, err)
	assert.True(t, pgInspect.Container.State.Running, "postgres must remain running after health check")

	redisInspect, err := cli.ContainerInspect(ctx, redisResp.ID, client.ContainerInspectOptions{})
	require.NoError(t, err)
	assert.True(t, redisInspect.Container.State.Running, "redis must remain running after health check")

	// -------------------------------------------------------------------------
	// TIER 2: migrate
	// -------------------------------------------------------------------------
	t.Log("--- Tier 2: Building backend image for migrate via BuildKit ---")
	backendDir := ""
	for _, candidate := range []string{".", "..", filepath.Join("..", ".."), filepath.Join("..", "backend"), "backend"} {
		if fi, err := os.Stat(filepath.Join(candidate, "Dockerfile")); err == nil && !fi.IsDir() {
			backendDir, _ = filepath.Abs(candidate)
			break
		}
	}
	require.NotEmpty(t, backendDir, "must locate backend directory containing Dockerfile")
	t.Logf("Using backend directory: %s", backendDir)

	matcher, err := LoadDockerignore(backendDir)
	if err != nil || matcher == nil {
		matcher = NewDockerignoreMatcher(DefaultIgnorePatterns)
	}

	tarArchive := StreamBuildContext(ctx, TarStreamerOptions{
		BuildContextDir: backendDir,
		Matcher:         matcher,
		EmitLog: func(phase, stream, msg string) {
			t.Logf("[%s] %s", stream, msg)
		},
	})

	buildOpts := client.ImageBuildOptions{
		Tags:       []string{"forgelab-backend:latest"},
		Dockerfile: "Dockerfile",
		Remove:     true,
	}

	buildErr := eng.buildImage(ctx, tarArchive, buildOpts, func(msg string) {
		t.Logf("[build] %s", msg)
	})
	require.NoError(t, buildErr, "backend image build with BuildKit must succeed")
	t.Log("migrate -> BuildKit Docker build succeeds")

	t.Log("--- Tier 2: Running migrate job ---")
	migHostConfig, err := ValidateAndBuildSecureHostConfig(ContainerSecurityOptions{})
	require.NoError(t, err)
	migResp, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image: "forgelab-backend:latest",
			Cmd:   []string{"./forgelab-migrate", "up"},
			Env: []string{
				"DATABASE_URL=postgres://forgelab:forgelab_dev_password@postgres:5432/forgelab?sslmode=disable",
			},
		},
		HostConfig: migHostConfig,
		NetworkingConfig: &dockernetwork.NetworkingConfig{
			EndpointsConfig: map[string]*dockernetwork.EndpointSettings{
				testNetName: {Aliases: []string{"migrate"}},
			},
		},
		Name: fmt.Sprintf("test-migrate-%s", uuid.New().String()[:8]),
	})
	require.NoError(t, err)
	containersToClean = append(containersToClean, migResp.ID)

	_, err = cli.ContainerStart(ctx, migResp.ID, client.ContainerStartOptions{})
	require.NoError(t, err)

	waitRes := cli.ContainerWait(ctx, migResp.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case err := <-waitRes.Error:
		require.NoError(t, err)
	case status := <-waitRes.Result:
		require.Equal(t, int64(0), status.StatusCode, "migrate job must exit with code 0")
		t.Logf("migrate -> runs -> exit code %d", status.StatusCode)
	}

	// -------------------------------------------------------------------------
	// TIER 3: backend
	// -------------------------------------------------------------------------
	t.Log("--- Tier 3: Starting backend service ---")
	beHostConfig, err := ValidateAndBuildSecureHostConfig(ContainerSecurityOptions{})
	require.NoError(t, err)
	beResp, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image: "forgelab-backend:latest",
			Env: []string{
				"SERVER_PORT=8080",
				"SERVER_HOST=0.0.0.0",
				"DATABASE_URL=postgres://forgelab:forgelab_dev_password@postgres:5432/forgelab?sslmode=disable",
				"REDIS_URL=redis://redis:6379/0",
				"JWT_SECRET=test-secret",
				"FORGELAB_ENCRYPTION_KEY=dGhpcy1pcy1hLWRldi1rZXktY2hhbmdlLWluLXByb2Q=",
			},
			Healthcheck: &container.HealthConfig{
				Test:        []string{"CMD", "curl", "-f", "http://localhost:8080/health"},
				Interval:    5 * time.Second,
				Timeout:     5 * time.Second,
				Retries:     10,
				StartPeriod: 15 * time.Second,
			},
		},
		HostConfig: beHostConfig,
		NetworkingConfig: &dockernetwork.NetworkingConfig{
			EndpointsConfig: map[string]*dockernetwork.EndpointSettings{
				testNetName: {Aliases: []string{"backend"}},
			},
		},
		Name: fmt.Sprintf("test-backend-%s", uuid.New().String()[:8]),
	})
	require.NoError(t, err)
	containersToClean = append(containersToClean, beResp.ID)

	_, err = cli.ContainerStart(ctx, beResp.ID, client.ContainerStartOptions{})
	require.NoError(t, err)

	t.Log("Verifying backend health...")
	beHealthy, beReason := eng.verifyServiceHealth(
		ctx,
		beResp.ID,
		8080,
		nil,
		models.HealthStrategyDocker,
		"/health",
		func(msg string) { t.Logf("[backend] %s", msg) },
		func(msg string) { t.Logf("[backend ERR] %s", msg) },
		&models.HealthCheckConfig{
			StartPeriodSeconds: 15,
			IntervalSeconds:    5,
			TimeoutSeconds:     5,
			Retries:            10,
		},
	)
	require.True(t, beHealthy, "backend must become healthy; reason: %s", beReason)
	t.Logf("backend -> starts -> Docker health healthy (%s)", beReason)

	// -------------------------------------------------------------------------
	// TIER 4: frontend
	// -------------------------------------------------------------------------
	t.Log("--- Tier 4: Starting frontend service ---")
	feHostConfig, err := ValidateAndBuildSecureHostConfig(ContainerSecurityOptions{})
	require.NoError(t, err)
	feResp, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image: "forgelab-frontend:latest",
			Env: []string{
				"PORT=3000",
				"BACKEND_INTERNAL_URL=http://backend:8080",
			},
		},
		HostConfig: feHostConfig,
		NetworkingConfig: &dockernetwork.NetworkingConfig{
			EndpointsConfig: map[string]*dockernetwork.EndpointSettings{
				testNetName: {Aliases: []string{"frontend"}},
			},
		},
		Name: fmt.Sprintf("test-frontend-%s", uuid.New().String()[:8]),
	})
	require.NoError(t, err)
	containersToClean = append(containersToClean, feResp.ID)

	_, err = cli.ContainerStart(ctx, feResp.ID, client.ContainerStartOptions{})
	require.NoError(t, err)

	t.Log("Verifying frontend health...")
	feHealthy, feReason := eng.verifyServiceHealth(
		ctx,
		feResp.ID,
		3000,
		nil,
		models.HealthStrategyAuto,
		"/",
		func(msg string) { t.Logf("[frontend] %s", msg) },
		func(msg string) { t.Logf("[frontend ERR] %s", msg) },
		&models.HealthCheckConfig{
			StartPeriodSeconds: 10,
			IntervalSeconds:    3,
			TimeoutSeconds:     5,
			Retries:            10,
		},
	)
	require.True(t, feHealthy, "frontend must become healthy; reason: %s", feReason)
	t.Logf("frontend -> starts -> healthy (%s)", feReason)

	// Final Invariant Check: Postgres and Redis must STILL be running!
	pgFinal, err := cli.ContainerInspect(ctx, pgResp.ID, client.ContainerInspectOptions{})
	require.NoError(t, err)
	assert.True(t, pgFinal.Container.State.Running, "postgres MUST remain running throughout entire self-deployment")

	redisFinal, err := cli.ContainerInspect(ctx, redisResp.ID, client.ContainerInspectOptions{})
	require.NoError(t, err)
	assert.True(t, redisFinal.Container.State.Running, "redis MUST remain running throughout entire self-deployment")

	t.Log("====================================================")
	t.Log("Release successfully deployed all 4 tiers!")
	t.Log("Postgres and Redis remained running without shutdown signals.")
	t.Log("====================================================")
}
