package docker

import (
	"errors"
	"testing"
)

func TestTrustBoundary_RejectsDockerSocketMount(t *testing.T) {
	opts := ContainerSecurityOptions{
		Binds: []string{"/var/run/docker.sock:/var/run/docker.sock"},
	}

	_, err := ValidateAndBuildSecureHostConfig(opts)
	if err == nil {
		t.Fatalf("expected error mounting docker.sock, got nil")
	}
	if !errors.Is(err, ErrDockerSocketMountRejected) {
		t.Errorf("expected ErrDockerSocketMountRejected, got %v", err)
	}

	// Also test Windows named pipe
	optsWin := ContainerSecurityOptions{
		Binds: []string{"\\\\.\\pipe\\docker_engine:\\\\.\\pipe\\docker_engine"},
	}
	_, err = ValidateAndBuildSecureHostConfig(optsWin)
	if err == nil || !errors.Is(err, ErrDockerSocketMountRejected) {
		t.Errorf("expected ErrDockerSocketMountRejected for named pipe, got %v", err)
	}
}

func TestTrustBoundary_RejectsSensitiveHostMounts(t *testing.T) {
	sensitiveMounts := []string{
		"/proc:/host/proc",
		"/sys:/host/sys",
		"/etc:/host/etc",
		"/dev:/host/dev",
	}

	for _, m := range sensitiveMounts {
		opts := ContainerSecurityOptions{Binds: []string{m}}
		_, err := ValidateAndBuildSecureHostConfig(opts)
		if err == nil || !errors.Is(err, ErrSensitiveHostMountRejected) {
			t.Errorf("expected ErrSensitiveHostMountRejected for %s, got %v", m, err)
		}
	}
}

func TestTrustBoundary_RejectsHostNetworking(t *testing.T) {
	opts := ContainerSecurityOptions{
		NetworkMode: "host",
	}

	_, err := ValidateAndBuildSecureHostConfig(opts)
	if err == nil || !errors.Is(err, ErrHostNetworkingRejected) {
		t.Errorf("expected ErrHostNetworkingRejected, got %v", err)
	}
}

func TestTrustBoundary_EnforcesSecurityAndResourceClamping(t *testing.T) {
	// Extreme/unbounded inputs
	opts := ContainerSecurityOptions{
		TargetCpuMillicores: 999999, // Unbounded high
		TargetMemoryMB:      1,      // Dangerously low
		TargetPidsLimit:     0,      // Unspecified
	}

	cfg, err := ValidateAndBuildSecureHostConfig(opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Invariant: Privileged must be false
	if cfg.Privileged {
		t.Errorf("expected Privileged to be false")
	}

	// Invariant: no-new-privileges
	hasNoNewPriv := false
	for _, opt := range cfg.SecurityOpt {
		if opt == "no-new-privileges:true" {
			hasNoNewPriv = true
			break
		}
	}
	if !hasNoNewPriv {
		t.Errorf("expected security opt no-new-privileges:true, got %v", cfg.SecurityOpt)
	}

	// Clamped resources
	// 16 cores max = 16_000_000_000 nanocores
	if cfg.Resources.NanoCPUs != 16_000_000_000 {
		t.Errorf("expected clamped NanoCPUs 16_000_000_000, got %d", cfg.Resources.NanoCPUs)
	}
	// Min 32 MB = 32 * 1024 * 1024 bytes
	if cfg.Resources.Memory != 32*1024*1024 {
		t.Errorf("expected clamped Memory 33554432, got %d", cfg.Resources.Memory)
	}
	// Default pids limit = 256
	if *cfg.Resources.PidsLimit != 256 {
		t.Errorf("expected default PidsLimit 256, got %d", *cfg.Resources.PidsLimit)
	}
}
