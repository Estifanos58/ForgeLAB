package services_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/services"
)

func TestProjectService_CreateProject_Validation(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-project-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	sourceSvc := services.NewSourceService(tempDir)
	// ProjectService without db (for validation checks before DB interaction)
	projectSvc := services.NewProjectService(nil, nil, sourceSvc, nil)

	ctx := context.Background()
	ownerID := uuid.New()

	t.Run("missing project name", func(t *testing.T) {
		_, err := projectSvc.CreateProject(ctx, ownerID, services.CreateProjectInput{
			Name:       "",
			SourceType: models.SourceTypeLocal,
		})
		require.Error(t, err)
		assert.ErrorIs(t, err, services.ErrValidationFailed)
		assert.Contains(t, err.Error(), "project name is required")
	})

	t.Run("unsupported source type", func(t *testing.T) {
		_, err := projectSvc.CreateProject(ctx, ownerID, services.CreateProjectInput{
			Name:       "Test Project",
			SourceType: "invalid-source",
		})
		require.Error(t, err)
		assert.ErrorIs(t, err, services.ErrValidationFailed)
		assert.Contains(t, err.Error(), "unsupported source type")
	})

	t.Run("github source missing repository reference", func(t *testing.T) {
		_, err := projectSvc.CreateProject(ctx, ownerID, services.CreateProjectInput{
			Name:            "GitHub Project",
			SourceType:      models.SourceTypeGitHub,
			SourceReference: "",
		})
		require.Error(t, err)
		assert.ErrorIs(t, err, services.ErrValidationFailed)
		assert.Contains(t, err.Error(), "github repository (owner/name) is required")
	})

	t.Run("local source with invalid UUID reference", func(t *testing.T) {
		_, err := projectSvc.CreateProject(ctx, ownerID, services.CreateProjectInput{
			Name:            "Local Project",
			SourceType:      models.SourceTypeLocal,
			SourceReference: "not-a-uuid",
		})
		require.Error(t, err)
		assert.ErrorIs(t, err, services.ErrInvalidSource)
		assert.Contains(t, err.Error(), "invalid source upload ID")
	})

	t.Run("local source with missing source directory", func(t *testing.T) {
		_, err := projectSvc.CreateProject(ctx, ownerID, services.CreateProjectInput{
			Name:            "Local Project",
			SourceType:      models.SourceTypeLocal,
			SourceReference: uuid.New().String(),
		})
		require.Error(t, err)
		assert.ErrorIs(t, err, services.ErrInvalidSource)
		assert.Contains(t, err.Error(), "source files not found or expired")
	})

	t.Run("local source without reference and without repository path", func(t *testing.T) {
		_, err := projectSvc.CreateProject(ctx, ownerID, services.CreateProjectInput{
			Name:       "Local Project",
			SourceType: models.SourceTypeLocal,
		})
		require.Error(t, err)
		assert.ErrorIs(t, err, services.ErrInvalidSource)
		assert.Contains(t, err.Error(), "local source requires uploaded files or valid host repository path")
	})
}

func TestProjectService_CreateProject_LocalSource_ValidFiles(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-project-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	sourceID := uuid.New()
	sourceDir := filepath.Join(tempDir, sourceID.String())
	require.NoError(t, os.MkdirAll(sourceDir, 0755))

	sourceSvc := services.NewSourceService(tempDir)
	projectSvc := services.NewProjectService(nil, nil, sourceSvc, nil)

	ctx := context.Background()
	ownerID := uuid.New()

	// Since DB is nil, it should pass source validation and only panic when executing the DB query
	assert.Panics(t, func() {
		_, _ = projectSvc.CreateProject(ctx, ownerID, services.CreateProjectInput{
			Name:            "My Local Project",
			SourceType:      models.SourceTypeLocal,
			SourceReference: sourceID.String(),
		})
	})
}
