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

func TestTrustBoundary_EnforcesTruthfulResourceLimitsAndIsolation(t *testing.T) {
	// Rejects unsupported/excessive CPU
	_, err := ValidateAndBuildSecureHostConfig(ContainerSecurityOptions{
		TargetCpuMillicores: 999999, // Exceeds 16 cores
	})
	if err == nil || !errors.Is(err, ErrCpuOutOfRange) {
		t.Fatalf("expected ErrCpuOutOfRange, got %v", err)
	}

	// Rejects unsupported/excessive Memory
	_, err = ValidateAndBuildSecureHostConfig(ContainerSecurityOptions{
		TargetMemoryMB: 1000000, // Exceeds 32GB
	})
	if err == nil || !errors.Is(err, ErrMemoryOutOfRange) {
		t.Fatalf("expected ErrMemoryOutOfRange, got %v", err)
	}

	// Rejects dangerously low Memory
	_, err = ValidateAndBuildSecureHostConfig(ContainerSecurityOptions{
		TargetMemoryMB: 10, // Below 64MB
	})
	if err == nil || !errors.Is(err, ErrMemoryOutOfRange) {
		t.Fatalf("expected ErrMemoryOutOfRange, got %v", err)
	}

	// Default inputs (0 specified) should apply secure platform defaults
	opts := ContainerSecurityOptions{
		TargetCpuMillicores: 0,
		TargetMemoryMB:      0,
		TargetPidsLimit:     0,
	}

	cfg, err := ValidateAndBuildSecureHostConfig(opts)
	if err != nil {
		t.Fatalf("unexpected error on default limits: %v", err)
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

	// Invariant: CapDrop ALL
	if len(cfg.CapDrop) != 1 || cfg.CapDrop[0] != "ALL" {
		t.Errorf("expected CapDrop [ALL], got %v", cfg.CapDrop)
	}

	// Default resources: 1 core = 1_000_000_000 nanocores
	if cfg.Resources.NanoCPUs != 1_000_000_000 {
		t.Errorf("expected default NanoCPUs 1_000_000_000, got %d", cfg.Resources.NanoCPUs)
	}
	// Default 1024 MB = 1024 * 1024 * 1024 bytes
	if cfg.Resources.Memory != 1024*1024*1024 {
		t.Errorf("expected default Memory 1073741824, got %d", cfg.Resources.Memory)
	}
	// Default pids limit = 256
	if *cfg.Resources.PidsLimit != 256 {
		t.Errorf("expected default PidsLimit 256, got %d", *cfg.Resources.PidsLimit)
	}

	// Invariant: tmpfs and log rotation bounded
	if cfg.Tmpfs["/tmp"] == "" {
		t.Errorf("expected /tmp tmpfs mount configured")
	}
	if cfg.LogConfig.Config["max-size"] != "10m" {
		t.Errorf("expected log rotation max-size 10m, got %s", cfg.LogConfig.Config["max-size"])
	}
}

func TestTrustBoundary_EnforcesMountAllowlist(t *testing.T) {
	opts := ContainerSecurityOptions{
		AllowedMountPrefixes: []string{"/app/data/builds"},
		Binds:                []string{"/home/user/malicious:/app/src"},
	}

	_, err := ValidateAndBuildSecureHostConfig(opts)
	if err == nil || !errors.Is(err, ErrHostMountNotAllowed) {
		t.Fatalf("expected ErrHostMountNotAllowed for mount outside allowlist, got %v", err)
	}
}
