package docker

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// DockerErrorCategory defines the classified root cause of a Docker build error.
type DockerErrorCategory string

const (
	// CategoryStorageDaemon represents Docker daemon/containerd/storage driver errors (e.g. layer export, CreateDiff).
	CategoryStorageDaemon DockerErrorCategory = "docker_storage_daemon"
	// CategoryDaemonUnreachable represents failures where the Docker daemon is down or unreachable.
	CategoryDaemonUnreachable DockerErrorCategory = "docker_daemon_unreachable"
	// CategoryContextCancelled represents timeout or user cancellation.
	CategoryContextCancelled DockerErrorCategory = "context_cancelled"
	// CategoryApplicationBuild represents application build failures (e.g. exit code non-zero in RUN steps).
	CategoryApplicationBuild DockerErrorCategory = "application_build"
	// CategoryGeneric represents unclassified Docker errors.
	CategoryGeneric DockerErrorCategory = "generic"
)

// ClassifyDockerBuildError inspects a Docker build error and categorizes it.
func ClassifyDockerBuildError(err error) DockerErrorCategory {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())

	// Storage layer / containerd / layer export failures
	if strings.Contains(msg, "failed to export layer") ||
		strings.Contains(msg, "creatediff") ||
		strings.Contains(msg, "mount callback failed") ||
		strings.Contains(msg, "io.containerd.content.v1.content") ||
		strings.Contains(msg, "failed to commit: rename") ||
		(strings.Contains(msg, "blobs/sha256") && strings.Contains(msg, "no such file or directory")) ||
		strings.Contains(msg, "error creating overlay mount") ||
		strings.Contains(msg, "no space left on device") ||
		strings.Contains(msg, "layer does not exist") {
		return CategoryStorageDaemon
	}

	// Daemon connectivity failures
	if strings.Contains(msg, "is the docker daemon running") ||
		strings.Contains(msg, "cannot connect to the docker daemon") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "docker daemon is unreachable") ||
		strings.Contains(msg, "pipe: The system cannot find the file specified") ||
		strings.Contains(msg, "docker.sock") {
		return CategoryDaemonUnreachable
	}

	// Cancellation / timeout
	if strings.Contains(msg, "context canceled") || strings.Contains(msg, "context deadline exceeded") {
		return CategoryContextCancelled
	}

	return CategoryGeneric
}

// FormatDockerBuildDiagnostic generates a clear, actionable diagnostic message
// while preserving the original error and adding safe environment metrics.
func (e *Engine) formatDockerDiagnostic(ctx context.Context, origErr error, cat DockerErrorCategory) string {
	var sb strings.Builder

	switch cat {
	case CategoryStorageDaemon:
		sb.WriteString("Docker daemon failed while exporting the image layer.\n")
		sb.WriteString("This appears to be a Docker/containerd storage failure rather than an application build error.\n")
		sb.WriteString("Check Docker Desktop health, available disk space, and containerd/image storage.\n\n")
	case CategoryDaemonUnreachable:
		sb.WriteString("Docker daemon is unreachable. Verify Docker Desktop or the Docker service is actively running.\n\n")
	}

	sb.WriteString(fmt.Sprintf("Original Docker error: %v\n", origErr))

	// Safe diagnostic collection (max 2 seconds)
	diagCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	if e.dockerClient != nil {
		sb.WriteString("\n--- Docker Diagnostics ---\n")

		// 1. Ping
		ping, err := e.dockerClient.Ping(diagCtx)
		if err != nil {
			sb.WriteString(fmt.Sprintf("Daemon Reachable: NO (%v)\n", err))
		} else {
			sb.WriteString(fmt.Sprintf("Daemon Reachable: YES (API Version: %s, Experimental: %v)\n", ping.APIVersion, ping.Experimental))
		}

		// 2. Server Version
		ver, err := e.dockerClient.ServerVersion(diagCtx)
		if err == nil {
			sb.WriteString(fmt.Sprintf("Docker Server Version: %s (OS/Arch: %s/%s)\n", ver.Version, ver.Os, ver.Arch))
		}

		// 3. Info (safe telemetry only - no environment variables or credentials)
		info, err := e.dockerClient.Info(diagCtx)
		if err == nil {
			sb.WriteString(fmt.Sprintf("Storage Driver: %s\n", info.Driver))
			sb.WriteString(fmt.Sprintf("Operating System: %s (%s)\n", info.OperatingSystem, info.OSType))
			sb.WriteString(fmt.Sprintf("System Resources: %d CPUs, %.2f GB RAM\n", info.NCPU, float64(info.MemTotal)/(1024*1024*1024)))
			if len(info.Warnings) > 0 {
				sb.WriteString(fmt.Sprintf("Daemon Warnings: %s\n", strings.Join(info.Warnings, "; ")))
			}
		}
	}

	return sb.String()
}
