package services

import (
	"context"
	"encoding/json"
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

	rev := deploySvc.resolveSourceRevision(ctx, ghProject, nil)
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
		RepositoryPath: "/tmp/some/path",
		DeploymentPlan: plan2Bytes,
	}

	localRev := deploySvc.resolveSourceRevision(ctx, localProject, nil)
	require.NotNil(t, localRev)
	assert.Equal(t, contentFP, *localRev, "Deployment must bind directly to the deterministic content snapshot fingerprint from discovery plan")

	// 3. Fallback when DeploymentPlan is absent: uses GitHub commitSHA if provided or branch
	customSHA := "fedcba9876543210fedcba9876543210fedcba98"
	ghProjectNoPlan := &models.Project{
		ID:              uuid.New(),
		OwnerID:         uuid.New(),
		SourceType:      models.SourceTypeGitHub,
		SourceReference: "owner/repo",
		Branch:          "feature",
	}
	fallbackRev := deploySvc.resolveSourceRevision(ctx, ghProjectNoPlan, &customSHA)
	require.NotNil(t, fallbackRev)
	assert.Equal(t, customSHA, *fallbackRev)
}
