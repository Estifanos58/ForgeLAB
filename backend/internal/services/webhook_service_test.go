package services

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/crypto"
	"github.com/forgelab/backend/internal/models"
)

type fakeDeployEnqueuer struct {
	enqueued []uuid.UUID
}

func (f *fakeDeployEnqueuer) EnqueueDeployment(_ context.Context, deploymentID uuid.UUID) error {
	f.enqueued = append(f.enqueued, deploymentID)
	return nil
}

func signGitHubPayload(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyGitHubSignature(t *testing.T) {
	payload := []byte(`{"ref":"refs/heads/main","after":"0123456789abcdef0123456789abcdef01234567"}`)
	secret := "super-secret-token"
	validSig := signGitHubPayload(secret, payload)

	assert.True(t, VerifyGitHubSignature(payload, validSig, secret))
	assert.False(t, VerifyGitHubSignature(payload, "sha256=deadbeef", secret))
	assert.False(t, VerifyGitHubSignature(payload, strings.TrimPrefix(validSig, "sha256="), secret))
	assert.False(t, VerifyGitHubSignature(payload, validSig, "wrong-secret"))
	assert.False(t, VerifyGitHubSignature(payload, validSig, ""))
	assert.False(t, VerifyGitHubSignature(payload, "", secret))
}

func TestWebhookService_PushToDeployAndReplayProtection(t *testing.T) {
	ctx := context.Background()
	enc, err := crypto.NewEncryptor("dGhpcy1pcy1hLWRldi1rZXktY2hhbmdlLWluLXByb2Q=")
	require.NoError(t, err)

	depSvc := NewDeploymentService(nil)
	var createdCommits []string
	depSvc.SetCreateDeploymentWithCommitForTest(func(_ context.Context, p *models.Project, commitSHA *string) (*models.Deployment, error) {
		require.NotNil(t, commitSHA)
		createdCommits = append(createdCommits, *commitSHA)
		return &models.Deployment{
			ID:           uuid.New(),
			ProjectID:    p.ID,
			DeployNumber: len(createdCommits),
			CommitSHA:    commitSHA,
		}, nil
	})

	enq := &fakeDeployEnqueuer{}
	ws := NewWebhookService(nil, enc, nil, depSvc, enq, "https://forgelab.example.com")

	project := &models.Project{
		ID:              uuid.New(),
		OwnerID:         uuid.New(),
		Name:            "repo-app",
		SourceType:      models.SourceTypeGitHub,
		SourceReference: "acme/repo-app",
		Branch:          "main",
	}
	ws.RegisterProjectForTest(project)

	enabled := true
	branch := "main"
	cfg, err := ws.UpdateWebhookConfig(ctx, project, UpdateWebhookConfigInput{
		AutoDeploy:       &enabled,
		AutoDeployBranch: &branch,
	}, "")
	require.NoError(t, err)
	assert.True(t, cfg.AutoDeploy)
	assert.True(t, cfg.HasWebhookSecret)
	require.NotEmpty(t, cfg.WebhookSecret)
	assert.Contains(t, cfg.WebhookURL, project.ID.String())

	commit := "0123456789abcdef0123456789abcdef01234567"
	pushPayload := []byte(fmt.Sprintf(`{"ref":"refs/heads/main","after":"%s","repository":{"full_name":"acme/repo-app"}}`, commit))

	// 1. Invalid signature BEFORE delivery recording must fail closed & must NOT poison delivery_id
	deliveryID := "deliv-1001"
	_, err = ws.HandleGitHubWebhook(ctx, &project.ID, "push", deliveryID, "sha256="+strings.Repeat("a", 64), pushPayload)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidWebhookSignature)
	assert.Empty(t, enq.enqueued)

	// 2. Valid signature with SAME deliveryID must now succeed (not poisoned by step 1!)
	validSig := signGitHubPayload(cfg.WebhookSecret, pushPayload)
	res, err := ws.HandleGitHubWebhook(ctx, &project.ID, "push", deliveryID, validSig, pushPayload)
	require.NoError(t, err)
	assert.True(t, res.OK)
	assert.True(t, res.Triggered)
	assert.Equal(t, commit, res.CommitSHA)
	require.Len(t, enq.enqueued, 1)
	assert.Equal(t, []string{commit}, createdCommits)

	// 3. Replaying the same valid deliveryID must be safely deduplicated
	replayRes, err := ws.HandleGitHubWebhook(ctx, &project.ID, "push", deliveryID, validSig, pushPayload)
	require.NoError(t, err)
	assert.True(t, replayRes.Duplicate)
	assert.True(t, replayRes.Ignored)
	assert.False(t, replayRes.Triggered)
	require.Len(t, enq.enqueued, 1)

	// 4. Push to non-target branch is ignored cleanly
	otherBranchPayload := []byte(fmt.Sprintf(`{"ref":"refs/heads/dev","after":"%s","repository":{"full_name":"acme/repo-app"}}`, commit))
	otherRes, err := ws.HandleGitHubWebhook(ctx, nil, "push", "deliv-1002", signGitHubPayload(cfg.WebhookSecret, otherBranchPayload), otherBranchPayload)
	require.NoError(t, err)
	assert.True(t, otherRes.Ignored)
	assert.False(t, otherRes.Triggered)
	require.Len(t, enq.enqueued, 1)

	// 5. Ping event succeeds without deploying
	pingPayload := []byte(`{"zen":"Design for failure.","repository":{"full_name":"acme/repo-app"}}`)
	pingRes, err := ws.HandleGitHubWebhook(ctx, nil, "ping", "deliv-1003", signGitHubPayload(cfg.WebhookSecret, pingPayload), pingPayload)
	require.NoError(t, err)
	assert.True(t, pingRes.OK)
	assert.False(t, pingRes.Triggered)
	assert.Equal(t, "ping", pingRes.Event)
}
