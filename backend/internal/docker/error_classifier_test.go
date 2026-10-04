package docker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/docker/docker/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClassifyDockerBuildError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected DockerErrorCategory
	}{
		{
			name:     "containerd layer export failure",
			err:      errors.New("failed to export layer: CreateDiff: mount callback failed: rename .../io.containerd.content.v1.content/ingest/.../data .../blobs/sha256/...: no such file or directory"),
			expected: CategoryStorageDaemon,
		},
		{
			name:     "overlay2 mount failure",
			err:      errors.New("error creating overlay mount to /var/lib/docker/overlay2/...: no such file or directory"),
			expected: CategoryStorageDaemon,
		},
		{
			name:     "disk full error",
			err:      errors.New("failed to write layer: write /var/lib/docker/...: no space left on device"),
			expected: CategoryStorageDaemon,
		},
		{
			name:     "daemon unreachable",
			err:      errors.New("Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?"),
			expected: CategoryDaemonUnreachable,
		},
		{
			name:     "context timeout",
			err:      errors.New("context deadline exceeded"),
			expected: CategoryContextCancelled,
		},
		{
			name:     "application compilation error",
			err:      errors.New("The command '/bin/sh -c npm run build' returned a non-zero code: 1"),
			expected: CategoryGeneric,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cat := ClassifyDockerBuildError(tc.err)
			assert.Equal(t, tc.expected, cat)
		})
	}
}

func TestEngine_FormatDockerDiagnostic_StorageFailure(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/_ping", "/v1.41/_ping":
			w.Header().Set("API-Version", "1.41")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("OK"))
		case "/version", "/v1.41/version":
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"Version":"24.0.7","Os":"linux","Arch":"amd64"}`)
		case "/info", "/v1.41/info":
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"Driver":"overlay2","OperatingSystem":"Docker Desktop","OSType":"linux","NCPU":8,"MemTotal":16777216000}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockServer.Close()

	cli, err := client.NewClientWithOpts(
		client.WithHost(mockServer.URL),
		client.WithHTTPClient(mockServer.Client()),
		client.WithVersion("1.41"),
	)
	require.NoError(t, err)
	defer cli.Close()

	engine := &Engine{dockerClient: cli}
	origErr := errors.New("failed to export layer: CreateDiff: mount callback failed: rename ...: no such file or directory")

	diag := engine.formatDockerDiagnostic(context.Background(), origErr, CategoryStorageDaemon)

	assert.Contains(t, diag, "Docker daemon failed while exporting the image layer")
	assert.Contains(t, diag, "This appears to be a Docker/containerd storage failure rather than an application build error")
	assert.Contains(t, diag, "Original Docker error: failed to export layer")
	assert.Contains(t, diag, "Storage Driver: overlay2")
	assert.Contains(t, diag, "Docker Server Version: 24.0.7")
	assert.False(t, strings.Contains(diag, "password"), "must not leak credentials")
	assert.False(t, strings.Contains(diag, "secret"), "must not leak secrets")
}
