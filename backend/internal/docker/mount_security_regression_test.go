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
)

func TestMountSecurity_RejectDockerSocketMounts_PreflightAndRuntime(t *testing.T) {
	ctx := context.Background()
	projID := uuid.New()
	project := &models.Project{
		ID:             projID,
		Name:           "sec-test",
		SourceType:     models.SourceTypeLocalDirectory,
		RepositoryPath: t.TempDir(),
	}

	pathVal := security.NewPathValidator([]string{project.RepositoryPath})

	// 1. Preflight rejection of docker socket
	dockerSockVols := []string{
		"/var/run/docker.sock",
		"/run/docker.sock",
		`\\.\pipe\docker_engine`,
	}

	for _, sockPath := range dockerSockVols {
		sd := &models.ServiceDeployment{
			ServiceName:   "app",
			BuildStrategy: models.BuildStrategyAuto,
			Volumes: []models.VolumeMountConfig{
				{
					Source:   sockPath,
					Target:   "/var/run/docker.sock",
					ReadOnly: false,
				},
			},
		}

		err := ValidateDeploymentPreflight(ctx, PreflightOptions{
			Project:       project,
			Deployments:   []*models.ServiceDeployment{sd},
			PathValidator: pathVal,
		})
		require.Error(t, err, "preflight must reject docker socket mount: %s", sockPath)
		assert.Contains(t, err.Error(), ErrDockerSocketMountRejected.Error())

		// 2. Volume resolver rejection of docker socket
		_, resErr := ResolveAndValidateVolumeMount(ctx, project, sd.Volumes[0], nil, pathVal)
		require.Error(t, resErr, "volume resolver must reject docker socket: %s", sockPath)
		assert.True(t, errors.Is(resErr, ErrDockerSocketMountRejected))
	}
}

func TestMountSecurity_EnforcesAllowlistForAbsoluteBindMounts_PreflightAndResolver(t *testing.T) {
	ctx := context.Background()
	projID := uuid.New()
	allowedDir := t.TempDir()
	unauthorizedDir := t.TempDir()

	project := &models.Project{
		ID:             projID,
		Name:           "allowlist-test",
		SourceType:     models.SourceTypeLocalDirectory,
		RepositoryPath: allowedDir,
	}

	pathVal := security.NewPathValidator([]string{allowedDir})

	// Mount within allowlist must be accepted
	validVol := models.VolumeMountConfig{
		Source:   filepath.Join(allowedDir, "subdata"),
		Target:   "/app/data",
		ReadOnly: false,
	}
	_ = os.MkdirAll(validVol.Source, 0755)

	resValid, err := ResolveAndValidateVolumeMount(ctx, project, validVol, nil, pathVal)
	require.NoError(t, err, "mount within allowlist should succeed")
	require.NotNil(t, resValid)

	// Mount outside allowlist must be strictly rejected
	maliciousVol := models.VolumeMountConfig{
		Source:   filepath.Join(unauthorizedDir, "stolen"),
		Target:   "/app/secrets",
		ReadOnly: false,
	}
	_ = os.MkdirAll(maliciousVol.Source, 0755)

	_, err = ResolveAndValidateVolumeMount(ctx, project, maliciousVol, nil, pathVal)
	require.Error(t, err, "mount outside allowlist must be rejected")
	assert.True(t, errors.Is(err, ErrHostMountNotAllowed), "expected ErrHostMountNotAllowed, got: %v", err)

	// Preflight must also reject mount outside allowlist
	sd := &models.ServiceDeployment{
		ServiceName:   "app",
		BuildStrategy: models.BuildStrategyAuto,
		Volumes:       []models.VolumeMountConfig{maliciousVol},
	}
	preErr := ValidateDeploymentPreflight(ctx, PreflightOptions{
		Project:       project,
		Deployments:   []*models.ServiceDeployment{sd},
		PathValidator: pathVal,
	})
	require.Error(t, preErr, "preflight must reject mount outside allowlist")
	assert.Contains(t, preErr.Error(), ErrHostMountNotAllowed.Error())
}

func TestMountSecurity_NamedVolumesAllowedWithoutHostPath(t *testing.T) {
	ctx := context.Background()
	projID := uuid.New()
	allowedDir := t.TempDir()

	project := &models.Project{
		ID:             projID,
		Name:           "vol-test",
		SourceType:     models.SourceTypeLocalDirectory,
		RepositoryPath: allowedDir,
	}

	pathVal := security.NewPathValidator([]string{allowedDir})

	namedVol := models.VolumeMountConfig{
		Type:     models.VolumeTypeNamed,
		Source:   "pgdata",
		Target:   "/var/lib/postgresql/data",
		ReadOnly: false,
	}

	res, err := ResolveAndValidateVolumeMount(ctx, project, namedVol, nil, pathVal)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.True(t, res.IsNamedVolume)
	assert.Equal(t, "pgdata", res.VolumeName)
}
