package services_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/agent"
	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/security"
	"github.com/forgelab/backend/internal/services"
)

func TestProjectService_CreateProject_Validation(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-project-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	sourceSvc := services.NewSourceService(nil, tempDir)
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
		assert.Contains(t, err.Error(), "source files not found")
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

	sourceSvc := services.NewSourceService(nil, tempDir)
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

func TestProjectService_CreateProject_LocalDirectory(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-proj-localdir-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	subDir := filepath.Join(tempDir, "my-app")
	require.NoError(t, os.MkdirAll(subDir, 0755))

	validator := security.NewPathValidator([]string{tempDir})
	projectSvc := services.NewProjectService(nil, validator, nil, nil)
	ctx := context.Background()
	ownerID := uuid.New()

	t.Run("missing repository path", func(t *testing.T) {
		_, err := projectSvc.CreateProject(ctx, ownerID, services.CreateProjectInput{
			Name:           "Local Dir Project",
			SourceType:     models.SourceTypeLocalDirectory,
			RepositoryPath: "",
		})
		require.Error(t, err)
		assert.ErrorIs(t, err, services.ErrValidationFailed)
		assert.Contains(t, err.Error(), "repository path is required")
	})

	t.Run("non-existent repository path", func(t *testing.T) {
		_, err := projectSvc.CreateProject(ctx, ownerID, services.CreateProjectInput{
			Name:           "Local Dir Project",
			SourceType:     models.SourceTypeLocalDirectory,
			RepositoryPath: filepath.Join(tempDir, "does-not-exist"),
		})
		require.Error(t, err)
		assert.ErrorIs(t, err, services.ErrInvalidSource)
	})

	t.Run("valid local directory passes validation", func(t *testing.T) {
		// Passes source validation, panics on nil DB
		assert.Panics(t, func() {
			_, _ = projectSvc.CreateProject(ctx, ownerID, services.CreateProjectInput{
				Name:           "Local Dir Project",
				SourceType:     models.SourceTypeLocalDirectory,
				RepositoryPath: subDir,
			})
		})
	})
}

func TestProjectService_CreateProject_LocalAgent_Validation(t *testing.T) {
	sm := agent.GetGlobalSessionManager()
	projectSvc := services.NewProjectService(nil, nil, nil, nil)
	ctx := context.Background()
	ownerID := uuid.New()
	agentID := "test-agent-local-1"

	t.Run("invalid UUID source reference", func(t *testing.T) {
		_, err := projectSvc.CreateProject(ctx, ownerID, services.CreateProjectInput{
			Name:            "Agent Project",
			SourceType:      models.SourceTypeLocalAgent,
			SourceReference: "invalid-uuid-string",
			AgentID:         agentID,
		})
		require.Error(t, err)
		assert.ErrorIs(t, err, services.ErrInvalidSource)
		assert.Contains(t, err.Error(), "invalid agent source ID")
	})

	t.Run("unowned or unknown agent session", func(t *testing.T) {
		randomSourceID := uuid.New().String()
		_, err := projectSvc.CreateProject(ctx, ownerID, services.CreateProjectInput{
			Name:            "Agent Project",
			SourceType:      models.SourceTypeLocalAgent,
			SourceReference: randomSourceID,
			AgentID:         agentID,
		})
		require.Error(t, err)
		assert.ErrorIs(t, err, services.ErrInvalidSource)
		assert.Contains(t, err.Error(), "agent source not found or unauthorized")
	})

	t.Run("session belonging to another user rejected", func(t *testing.T) {
		otherUser := uuid.New()
		sess, err := sm.CreateSession(otherUser, agentID, 10*time.Minute)
		require.NoError(t, err)
		sourceUUID := uuid.New()
		_, err = sm.BindSource(sess.Token, sourceUUID, "repo", agentID)
		require.NoError(t, err)

		_, err = projectSvc.CreateProject(ctx, ownerID, services.CreateProjectInput{
			Name:            "Agent Project",
			SourceType:      models.SourceTypeLocalAgent,
			SourceReference: sourceUUID.String(),
			AgentID:         agentID,
		})
		require.Error(t, err)
		assert.ErrorIs(t, err, services.ErrInvalidSource)
		assert.Contains(t, err.Error(), "does not belong to user")
	})

	t.Run("consumed session rejected", func(t *testing.T) {
		sess, err := sm.CreateSession(ownerID, agentID, 10*time.Minute)
		require.NoError(t, err)
		sourceUUID := uuid.New()
		_, err = sm.BindSource(sess.Token, sourceUUID, "repo", agentID)
		require.NoError(t, err)
		require.NoError(t, sm.MarkConsumed(sess.ID))

		_, err = projectSvc.CreateProject(ctx, ownerID, services.CreateProjectInput{
			Name:            "Agent Project",
			SourceType:      models.SourceTypeLocalAgent,
			SourceReference: sourceUUID.String(),
			AgentID:         agentID,
		})
		require.Error(t, err)
		assert.ErrorIs(t, err, services.ErrInvalidSource)
		assert.Contains(t, err.Error(), "already been consumed")
	})

	t.Run("agent ID mismatch rejected", func(t *testing.T) {
		sess, err := sm.CreateSession(ownerID, "actual-agent-id", 10*time.Minute)
		require.NoError(t, err)
		sourceUUID := uuid.New()
		_, err = sm.BindSource(sess.Token, sourceUUID, "repo", "actual-agent-id")
		require.NoError(t, err)

		wrongAgentID := "wrong-agent-id"
		_, err = projectSvc.CreateProject(ctx, ownerID, services.CreateProjectInput{
			Name:            "Agent Project",
			SourceType:      models.SourceTypeLocalAgent,
			SourceReference: sourceUUID.String(),
			AgentID:         wrongAgentID,
		})
		require.Error(t, err)
		assert.ErrorIs(t, err, services.ErrValidationFailed)
		assert.Contains(t, err.Error(), "agent ID mismatch")
	})

	t.Run("valid session passes validation and consumes session", func(t *testing.T) {
		sess, err := sm.CreateSession(ownerID, agentID, 10*time.Minute)
		require.NoError(t, err)
		sourceUUID := uuid.New()
		_, err = sm.BindSource(sess.Token, sourceUUID, "repo", agentID)
		require.NoError(t, err)

		// Without DB pool, it will pass validation and panic on DB query execution
		assert.Panics(t, func() {
			_, _ = projectSvc.CreateProject(ctx, ownerID, services.CreateProjectInput{
				Name:            "Agent Project",
				SourceType:      models.SourceTypeLocalAgent,
				SourceReference: sourceUUID.String(),
				AgentID:         agentID,
			})
		})
	})
}
