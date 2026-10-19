package docker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/security"
	"github.com/forgelab/backend/internal/services"
)

func TestVolumeResolution_GitHubSnapshotRelativeMount(t *testing.T) {
	ctx := context.Background()

	// 1. Create a dummy snapshot directory representing an imported GitHub repository
	snapshotDir := t.TempDir()
	backendSubdir := filepath.Join(snapshotDir, "backend")
	require.NoError(t, os.MkdirAll(backendSubdir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(backendSubdir, "main.go"), []byte("package main"), 0644))

	srcService := services.NewSourceService(nil, t.TempDir())
	srcID := uuid.New()
	ownerID := uuid.New()

	// Save source record with snapshot_path in metadata
	err := srcService.SaveSource(ctx, &models.Source{
		ID:              srcID,
		OwnerID:         ownerID,
		SourceType:      models.SourceTypeGitHubPublic,
		SourceReference: "testowner/myrepo",
		Metadata: map[string]interface{}{
			"snapshot_path": snapshotDir,
			"commit_sha":    "abc123456789",
		},
	})
	require.NoError(t, err)

	project := &models.Project{
		ID:              uuid.New(),
		Name:            "github-import-test",
		OwnerID:         ownerID,
		SourceType:      models.SourceTypeGitHubPublic,
		SourceReference: "testowner/myrepo",
		SourceID:        &srcID,
	}

	pathVal := security.NewPathValidator([]string{})

	// 1. Resolve relative root mount "."
	rootVol := models.VolumeMountConfig{
		Source:   ".",
		Target:   "/host-projects",
		ReadOnly: true,
	}
	resRoot, err := ResolveAndValidateVolumeMount(ctx, project, rootVol, srcService, pathVal)
	require.NoError(t, err, "relative root mount '.' must resolve for github_public snapshot")
	require.NotNil(t, resRoot)
	assert.Equal(t, snapshotDir, resRoot.BackendPath)
	assert.Equal(t, snapshotDir, resRoot.DaemonPath)
	assert.Equal(t, "/host-projects", resRoot.ContainerPath)
	assert.True(t, resRoot.ReadOnly)

	// 2. Resolve relative subpath mount "./backend"
	subVol := models.VolumeMountConfig{
		Source:   "./backend",
		Target:   "/app",
		ReadOnly: false,
	}
	resSub, err := ResolveAndValidateVolumeMount(ctx, project, subVol, srcService, pathVal)
	require.NoError(t, err, "relative subpath mount './backend' must resolve")
	require.NotNil(t, resSub)
	assert.Equal(t, backendSubdir, resSub.BackendPath)
	assert.Equal(t, backendSubdir, resSub.DaemonPath)
	assert.Equal(t, "/app", resSub.ContainerPath)

	// 3. Preflight validation must pass cleanly for the project
	sd := &models.ServiceDeployment{
		ServiceName:   "backend",
		BuildStrategy: models.BuildStrategyAuto,
		Volumes:       []models.VolumeMountConfig{rootVol, subVol},
	}
	preflightErr := ValidateDeploymentPreflight(ctx, PreflightOptions{
		Project:       project,
		Deployments:   []*models.ServiceDeployment{sd},
		PathValidator: pathVal,
		SourceService: srcService,
	})
	require.NoError(t, preflightErr, "preflight validation must succeed for github_public project with relative volume mounts")
}

func TestVolumeResolution_PrivateGitHubSnapshotRelativeMount(t *testing.T) {
	ctx := context.Background()

	snapshotDir := t.TempDir()
	srcService := services.NewSourceService(nil, t.TempDir())
	srcID := uuid.New()
	ownerID := uuid.New()

	err := srcService.SaveSource(ctx, &models.Source{
		ID:              srcID,
		OwnerID:         ownerID,
		SourceType:      models.SourceTypeGitHub,
		SourceReference: "testowner/privaterepo",
		Metadata: map[string]interface{}{
			"snapshot_path": snapshotDir,
		},
	})
	require.NoError(t, err)

	project := &models.Project{
		ID:              uuid.New(),
		Name:            "github-private-test",
		OwnerID:         ownerID,
		SourceType:      models.SourceTypeGitHub,
		SourceReference: "testowner/privaterepo",
		SourceID:        &srcID,
	}

	pathVal := security.NewPathValidator([]string{})
	rootVol := models.VolumeMountConfig{
		Source: ".",
		Target: "/host-projects",
	}

	res, err := ResolveAndValidateVolumeMount(ctx, project, rootVol, srcService, pathVal)
	require.NoError(t, err, "private github snapshot must allow relative mounts")
	require.NotNil(t, res)
	assert.Equal(t, snapshotDir, res.BackendPath)
}

func TestVolumeResolution_SafeCreationOfMissingProjectSubdir(t *testing.T) {
	ctx := context.Background()

	snapshotDir := t.TempDir()
	srcService := services.NewSourceService(nil, t.TempDir())
	srcID := uuid.New()
	ownerID := uuid.New()

	err := srcService.SaveSource(ctx, &models.Source{
		ID:         srcID,
		OwnerID:    ownerID,
		SourceType: models.SourceTypeGitHubPublic,
		Metadata: map[string]interface{}{
			"snapshot_path": snapshotDir,
		},
	})
	require.NoError(t, err)

	project := &models.Project{
		ID:         uuid.New(),
		Name:       "missing-subdir-test",
		OwnerID:    ownerID,
		SourceType: models.SourceTypeGitHubPublic,
		SourceID:   &srcID,
	}

	pathVal := security.NewPathValidator([]string{})
	missingSubdir := filepath.Join(snapshotDir, "runtime-data", "cache")

	// Ensure missingSubdir does not exist yet
	_, statErr := os.Stat(missingSubdir)
	require.True(t, os.IsNotExist(statErr))

	dataVol := models.VolumeMountConfig{
		Source: "./runtime-data/cache",
		Target: "/data/cache",
	}

	res, err := ResolveAndValidateVolumeMount(ctx, project, dataVol, srcService, pathVal)
	require.NoError(t, err, "relative mount to non-existent subdir inside project root should be safely created")
	require.NotNil(t, res)

	// Subdir must now exist
	fi, statErr := os.Stat(missingSubdir)
	require.NoError(t, statErr)
	assert.True(t, fi.IsDir())
}

func TestVolumeResolution_EscapesStrictlyRejected(t *testing.T) {
	ctx := context.Background()

	snapshotDir := t.TempDir()
	srcService := services.NewSourceService(nil, t.TempDir())
	srcID := uuid.New()
	ownerID := uuid.New()

	err := srcService.SaveSource(ctx, &models.Source{
		ID:         srcID,
		OwnerID:    ownerID,
		SourceType: models.SourceTypeGitHubPublic,
		Metadata: map[string]interface{}{
			"snapshot_path": snapshotDir,
		},
	})
	require.NoError(t, err)

	project := &models.Project{
		ID:         uuid.New(),
		Name:       "escape-test",
		OwnerID:    ownerID,
		SourceType: models.SourceTypeGitHubPublic,
		SourceID:   &srcID,
	}

	pathVal := security.NewPathValidator([]string{})

	escapeVols := []string{
		"../escape",
		"../../etc/passwd",
		"subdir/../../..",
	}

	for _, esc := range escapeVols {
		vol := models.VolumeMountConfig{
			Source: esc,
			Target: "/stolen",
		}
		_, err := ResolveAndValidateVolumeMount(ctx, project, vol, srcService, pathVal)
		require.Error(t, err, "path traversal '%s' must be rejected", esc)
		assert.Contains(t, err.Error(), "escapes project source root boundary")
	}
}

func TestVolumeResolution_DockerSocketStrictlyRejected(t *testing.T) {
	ctx := context.Background()

	snapshotDir := t.TempDir()
	project := &models.Project{
		ID:             uuid.New(),
		Name:           "docker-sock-test",
		SourceType:     models.SourceTypeGitHubPublic,
		RepositoryPath: snapshotDir,
	}

	pathVal := security.NewPathValidator([]string{})
	sockVol := models.VolumeMountConfig{
		Source: "/var/run/docker.sock",
		Target: "/var/run/docker.sock",
	}

	_, err := ResolveAndValidateVolumeMount(ctx, project, sockVol, nil, pathVal)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrDockerSocketMountRejected))
}
