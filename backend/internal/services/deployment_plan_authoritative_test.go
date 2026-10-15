package services

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/discovery"
	"github.com/forgelab/backend/internal/models"
)

func TestDeploymentService_ResolveSourceRevision_AuthoritativeDeploymentPlan(t *testing.T) {
	ctx := context.Background()
	deploySvc := NewDeploymentService(nil)

	// 1. GitHub project with authoritative pinned commit SHA in DeploymentPlan
	pinnedSHA := "a1b2c3d4e5f6789012345678901234567890abcd"
	plan1Bytes, _ := json.Marshal(discovery.DeploymentPlan{
		SourceRevision: pinnedSHA,
		SourceType:     models.SourceTypeGitHub,
	})
	ghProject := &models.Project{
		ID:              uuid.New(),
		OwnerID:         uuid.New(),
		Name:            "GH-App",
		SourceType:      models.SourceTypeGitHub,
		SourceReference: "owner/repo",
		Branch:          "main",
		DeploymentPlan:  plan1Bytes,
	}

	rev, err := deploySvc.resolveSourceRevision(ctx, ghProject, nil)
	require.NoError(t, err)
	require.NotNil(t, rev)
	assert.Equal(t, pinnedSHA, *rev, "Deployment must consume authoritative pinned commit SHA from plan without refetching branch HEAD")

	// 2. Local directory project with authoritative deterministic content fingerprint
	contentFP := "fp_3a8b29c410e5f6a7"
	plan2Bytes, _ := json.Marshal(discovery.DeploymentPlan{
		SourceRevision: contentFP,
		SourceType:     models.SourceTypeLocal,
	})
	localProject := &models.Project{
		ID:             uuid.New(),
		OwnerID:        uuid.New(),
		Name:           "Local-App",
		SourceType:     models.SourceTypeLocal,
		RepositoryPath: "/tmp/nonexistent/path/for/mock/plan",
		DeploymentPlan: plan2Bytes,
	}

	localRev, err := deploySvc.resolveSourceRevision(ctx, localProject, nil)
	require.NoError(t, err)
	require.NotNil(t, localRev)
	assert.Equal(t, contentFP, *localRev, "Deployment must bind directly to the deterministic content snapshot fingerprint from discovery plan")

	// 3. Fallback when DeploymentPlan is absent: uses GitHub commitSHA if provided
	customSHA := "fedcba9876543210fedcba9876543210fedcba98"
	ghProjectNoPlan := &models.Project{
		ID:              uuid.New(),
		OwnerID:         uuid.New(),
		SourceType:      models.SourceTypeGitHub,
		SourceReference: "owner/repo",
		Branch:          "feature",
	}
	fallbackRev, err := deploySvc.resolveSourceRevision(ctx, ghProjectNoPlan, &customSHA)
	require.NoError(t, err)
	require.NotNil(t, fallbackRev)
	assert.Equal(t, customSHA, *fallbackRev)
}

func TestDeploymentService_ResolveSourceRevision_InvalidatesPlanWhenSourceChanges(t *testing.T) {
	ctx := context.Background()
	deploySvc := NewDeploymentService(nil)

	// Create real temporary directory on disk
	tempDir, err := os.MkdirTemp("", "forgelab-deployplan-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	file1 := filepath.Join(tempDir, "main.go")
	err = os.WriteFile(file1, []byte("package main\nfunc main() { /* v1 */ }\n"), 0644)
	require.NoError(t, err)

	fp1, err := discovery.ComputeDirectoryContentFingerprint(tempDir)
	require.NoError(t, err)

	// Set up project with deployment plan containing fp1 (reviewed plan)
	planBytes, _ := json.Marshal(discovery.DeploymentPlan{
		SourceRevision: fp1,
		SourceType:     models.SourceTypeLocalDirectory,
	})
	localProject := &models.Project{
		ID:             uuid.New(),
		OwnerID:        uuid.New(),
		Name:           "Local-Live-App",
		SourceType:     models.SourceTypeLocalDirectory,
		RepositoryPath: tempDir,
		DeploymentPlan: planBytes,
	}

	// 1. When files match reviewed plan, resolveSourceRevision succeeds with fp1
	rev, err := deploySvc.resolveSourceRevision(ctx, localProject, nil)
	require.NoError(t, err)
	require.NotNil(t, rev)
	assert.Equal(t, fp1, *rev)

	// 2. Modify file content on disk (simulating user edit after plan review)
	err = os.WriteFile(file1, []byte("package main\nfunc main() { /* v2 modified after review */ }\n"), 0644)
	require.NoError(t, err)

	fp2, err := discovery.ComputeDirectoryContentFingerprint(tempDir)
	require.NoError(t, err)
	require.NotEqual(t, fp1, fp2, "content change must alter fingerprint")

	// 3. resolveSourceRevision must invalidate the plan and return an error requiring fresh discovery/review
	staleRev, staleErr := deploySvc.resolveSourceRevision(ctx, localProject, nil)
	require.Error(t, staleErr, "resolveSourceRevision must fail when source files change after plan review")
	assert.Nil(t, staleRev)
	assert.Contains(t, staleErr.Error(), "source files changed after deployment plan review")
	assert.Contains(t, staleErr.Error(), "deployment plan has been invalidated")
	assert.Equal(t, "{}", string(localProject.DeploymentPlan), "project deployment plan must be invalidated to {}")

	// 4. After fresh review (updating plan to fp2), deployment succeeds again
	freshPlanBytes, _ := json.Marshal(discovery.DeploymentPlan{
		SourceRevision: fp2,
		SourceType:     models.SourceTypeLocalDirectory,
	})
	localProject.DeploymentPlan = freshPlanBytes
	freshRev, freshErr := deploySvc.resolveSourceRevision(ctx, localProject, nil)
	require.NoError(t, freshErr)
	require.NotNil(t, freshRev)
	assert.Equal(t, fp2, *freshRev)
}
