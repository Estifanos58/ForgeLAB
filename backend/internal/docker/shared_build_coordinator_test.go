package docker

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSharedBuildCoordinator_ComputeSharedBuildKey(t *testing.T) {
	strPtr := func(s string) *string { return &s }

	args1 := map[string]*string{
		"NODE_ENV": strPtr("production"),
		"PORT":     strPtr("3000"),
	}
	args2 := map[string]*string{
		"PORT":     strPtr("3000"),
		"NODE_ENV": strPtr("production"),
	}
	argsDiff := map[string]*string{
		"NODE_ENV": strPtr("development"),
		"PORT":     strPtr("3000"),
	}

	key1 := ComputeSharedBuildKey("proj1", "v1.0", "./app", "Dockerfile", "dockerfile", "", args1, "node", 3000, "npm start", "npm")
	key2 := ComputeSharedBuildKey("proj1", "v1.0", "app", "./Dockerfile", "dockerfile", "", args2, "node", 3000, "npm start", "npm")
	keyDiffArgs := ComputeSharedBuildKey("proj1", "v1.0", "./app", "Dockerfile", "dockerfile", "", argsDiff, "node", 3000, "npm start", "npm")
	keyDiffPort := ComputeSharedBuildKey("proj1", "v1.0", "./app", "Dockerfile", "dockerfile", "", args1, "node", 8080, "npm start", "npm")
	keyDiffStrategy := ComputeSharedBuildKey("proj1", "v1.0", "./app", "Dockerfile", "nixpacks", "", args1, "node", 3000, "npm start", "npm")

	// Same args in different order and normalized paths should produce identical keys
	assert.Equal(t, key1, key2, "key normalization should produce identical hash")

	// Changing any build-affecting parameter should produce a different key
	assert.NotEqual(t, key1, keyDiffArgs, "different build args must produce different key")
	assert.NotEqual(t, key1, keyDiffPort, "different port must produce different key")
	assert.NotEqual(t, key1, keyDiffStrategy, "different strategy must produce different key")
}

func TestSharedBuildCoordinator_LeaderFollowerSuccess(t *testing.T) {
	coord := NewSharedBuildCoordinator()
	key := "proj1:rev1:context:Dockerfile:abc123"

	// Service 1 becomes leader
	p1, exists := coord.GetOrInit(key, "svc-1")
	assert.False(t, exists, "first caller should be the leader")
	assert.False(t, p1.IsDone(), "promise should not be done initially")

	// Service 2 becomes follower
	p2, exists := coord.GetOrInit(key, "svc-2")
	assert.True(t, exists, "second caller should be follower")
	assert.Same(t, p1, p2, "follower must receive same promise instance")

	var wg sync.WaitGroup
	wg.Add(1)
	var followerTag, followerDigest string
	var followerErr error

	go func() {
		defer wg.Done()
		select {
		case <-p2.Done():
			followerTag, followerDigest, followerErr = p2.Result()
		case <-time.After(2 * time.Second):
			followerErr = errors.New("timeout waiting on shared build")
		}
	}()

	// Leader completes the build
	coord.Complete(key, "myimage:latest", "sha256:123456")

	wg.Wait()
	require.NoError(t, followerErr)
	assert.Equal(t, "myimage:latest", followerTag)
	assert.Equal(t, "sha256:123456", followerDigest)
	assert.True(t, p1.IsDone())
}

func TestSharedBuildCoordinator_LeaderFailureNotifiesFollowers(t *testing.T) {
	coord := NewSharedBuildCoordinator()
	key := "proj2:rev1:context:Dockerfile:xyz789"

	p1, exists := coord.GetOrInit(key, "svc-1")
	assert.False(t, exists)

	p2, exists := coord.GetOrInit(key, "svc-2")
	assert.True(t, exists)

	var wg sync.WaitGroup
	wg.Add(1)
	var followerErr error

	go func() {
		defer wg.Done()
		select {
		case <-p2.Done():
			_, _, followerErr = p2.Result()
		case <-time.After(2 * time.Second):
			followerErr = errors.New("timeout waiting on failed shared build")
		}
	}()

	expectedErr := errors.New("docker build daemon out of memory")
	coord.Fail(key, expectedErr)

	wg.Wait()
	require.Error(t, followerErr)
	assert.Equal(t, expectedErr, followerErr)
	assert.True(t, p1.IsDone())
}

func TestSharedBuildCoordinator_ExactlyOnceCompletion(t *testing.T) {
	coord := NewSharedBuildCoordinator()
	key := "proj3:rev1:context:Dockerfile:single"

	p, _ := coord.GetOrInit(key, "svc-1")

	// Multiple completions should not panic
	assert.NotPanics(t, func() {
		p.Complete("img:v1", "sha256:abc")
		p.Complete("img:v2", "sha256:def")
		p.Fail(errors.New("late error"))
	})

	tag, digest, err := p.Result()
	assert.Equal(t, "img:v1", tag)
	assert.Equal(t, "sha256:abc", digest)
	assert.NoError(t, err)
}

func TestSharedBuildCoordinator_CleanupDeployment(t *testing.T) {
	coord := NewSharedBuildCoordinator()

	k1 := "projA:rev1:ctx1:Dockerfile:1"
	k2 := "projA:rev1:ctx2:Dockerfile:2"
	k3 := "projB:rev1:ctx1:Dockerfile:3"

	p1, _ := coord.GetOrInit(k1, "svc-1")
	_, _ = coord.GetOrInit(k2, "svc-2") // remains in-flight
	p3, _ := coord.GetOrInit(k3, "svc-3")

	p1.Complete("img1", "sha1")
	p3.Complete("img3", "sha3")

	cleaned := coord.CleanupDeployment("projA")
	assert.Equal(t, 1, cleaned, "should clean up only completed entry for projA")

	// Check entries
	_, exists1 := coord.Get(k1)
	assert.False(t, exists1, "completed projA entry should be removed")

	_, exists2 := coord.Get(k2)
	assert.True(t, exists2, "in-flight projA entry should be retained")

	_, exists3 := coord.Get(k3)
	assert.True(t, exists3, "projB entry should not be removed")
}

func TestSharedBuildCoordinator_Diagnostics(t *testing.T) {
	coord := NewSharedBuildCoordinator()

	k1 := "projD:rev1:ctx1:Dockerfile:1"
	k2 := "projD:rev1:ctx2:Dockerfile:2"

	p1, _ := coord.GetOrInit(k1, "svc-1")
	_, _ = coord.GetOrInit(k2, "svc-2")

	p1.Complete("img1", "sha1")

	diag := coord.GetDiagnostics()
	assert.Equal(t, 2, diag.TotalEntries)
	assert.Equal(t, 1, diag.ActiveBuilds)
	assert.Contains(t, diag.Entries, k1)
	assert.Contains(t, diag.Entries, k2)
	assert.Equal(t, "succeeded", diag.Entries[k1].Phase)
	assert.Equal(t, "building", diag.Entries[k2].Phase)
}
