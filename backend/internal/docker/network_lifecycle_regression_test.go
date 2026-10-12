package docker

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	dockernetwork "github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEngine_EnsureNetwork_IdempotencyAndConcurrency(t *testing.T) {
	if os.Getenv("SKIP_DOCKER_LIVE_TEST") != "" {
		t.Skip("Skipping live Docker network test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Skipf("Docker daemon not available: %v", err)
	}
	defer cli.Close()

	if _, err := cli.Ping(ctx); err != nil {
		t.Skipf("Docker daemon ping failed: %v", err)
	}

	eng := &Engine{dockerClient: cli}
	testNetName := fmt.Sprintf("forgelab-net-test-%s", uuid.New().String()[:8])
	labels := map[string]string{
		"forgelab.managed":    "true",
		"forgelab.project_id": uuid.New().String(),
	}

	defer func() {
		_ = cli.NetworkRemove(context.Background(), testNetName)
	}()

	// 1. First EnsureNetwork call: should create the network
	err = eng.EnsureNetwork(ctx, testNetName, labels)
	require.NoError(t, err, "EnsureNetwork must successfully create new network")

	// Verify it exists in Docker
	inspectRes, err := cli.NetworkInspect(ctx, testNetName, dockernetwork.InspectOptions{})
	require.NoError(t, err)
	assert.Equal(t, "bridge", inspectRes.Driver)
	assert.Equal(t, labels["forgelab.managed"], inspectRes.Labels["forgelab.managed"])

	// 2. Second EnsureNetwork call: must be idempotent and succeed without duplicate name error
	err = eng.EnsureNetwork(ctx, testNetName, labels)
	require.NoError(t, err, "EnsureNetwork must idempotently succeed when network already exists")

	// 3. Concurrent EnsureNetwork calls (simulating DAG Tier 1 concurrent services e.g. redis + postgres)
	concurrentNetName := fmt.Sprintf("forgelab-net-conc-%s", uuid.New().String()[:8])
	defer func() {
		_ = cli.NetworkRemove(context.Background(), concurrentNetName)
	}()

	var wg sync.WaitGroup
	errCh := make(chan error, 5)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := eng.EnsureNetwork(ctx, concurrentNetName, labels); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)

	for concurrentErr := range errCh {
		assert.NoError(t, concurrentErr, "Concurrent EnsureNetwork calls must all resolve successfully")
	}
}

func TestEngine_PruneDanglingNetworks(t *testing.T) {
	if os.Getenv("SKIP_DOCKER_LIVE_TEST") != "" {
		t.Skip("Skipping live Docker network test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Skipf("Docker daemon not available: %v", err)
	}
	defer cli.Close()

	if _, err := cli.Ping(ctx); err != nil {
		t.Skipf("Docker daemon ping failed: %v", err)
	}

	eng := &Engine{dockerClient: cli}
	projID := uuid.New().String()
	danglingNetName := fmt.Sprintf("forgelab-net-prune-%s", uuid.New().String()[:8])

	err = eng.EnsureNetwork(ctx, danglingNetName, map[string]string{
		"forgelab.managed":    "true",
		"forgelab.project_id": projID,
	})
	require.NoError(t, err)

	// Network is unattached (0 containers). PruneDanglingNetworks should clean it up.
	eng.PruneDanglingNetworks(ctx)

	// Inspect should fail because network was pruned
	_, err = cli.NetworkInspect(ctx, danglingNetName, dockernetwork.InspectOptions{})
	assert.Error(t, err, "Dangling unattached ForgeLAB network must have been pruned")

	// Ensure system networks like 'bridge' still exist
	sysInspect, err := cli.NetworkInspect(ctx, "bridge", dockernetwork.InspectOptions{})
	require.NoError(t, err)
	assert.Equal(t, "bridge", sysInspect.Name)
}

func TestEngine_CleanUpProjectContainers_RemovesProjectNetworks(t *testing.T) {
	if os.Getenv("SKIP_DOCKER_LIVE_TEST") != "" {
		t.Skip("Skipping live Docker network test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Skipf("Docker daemon not available: %v", err)
	}
	defer cli.Close()

	if _, err := cli.Ping(ctx); err != nil {
		t.Skipf("Docker daemon ping failed: %v", err)
	}

	eng := &Engine{dockerClient: cli}
	projUUID := uuid.New()
	projNetName := fmt.Sprintf("forgelab-net-%s", projUUID.String()[:8])

	err = eng.EnsureNetwork(ctx, projNetName, map[string]string{
		"forgelab.managed":    "true",
		"forgelab.project_id": projUUID.String(),
	})
	require.NoError(t, err)

	// CleanUpProjectContainers should clean up project-associated networks
	eng.CleanUpProjectContainers(ctx, projUUID)

	_, err = cli.NetworkInspect(ctx, projNetName, dockernetwork.InspectOptions{})
	assert.Error(t, err, "Project network must be deleted during CleanUpProjectContainers")
}
