package services

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/models"
)

func TestAuditService_RecordSanitizesSecretsAndScopesByActor(t *testing.T) {
	ctx := context.Background()
	svc := NewAuditService(nil)

	actorA := uuid.New()
	actorB := uuid.New()
	projID := uuid.New()

	// Validation guards
	_, err := svc.Record(ctx, AuditRecordInput{Action: "env.set", ResourceType: "env_var"})
	require.Error(t, err)
	_, err = svc.Record(ctx, AuditRecordInput{ActorID: actorA, ResourceType: "env_var"})
	require.Error(t, err)
	_, err = svc.Record(ctx, AuditRecordInput{ActorID: actorA, Action: "env.set"})
	require.Error(t, err)

	entry, err := svc.Record(ctx, AuditRecordInput{
		ActorID:      actorA,
		ActorLabel:   "alice@example.com",
		ProjectID:    &projID,
		ProjectName:  "shop-api",
		Action:       "env.set",
		ResourceType: "env_var",
		ResourceID:   "DATABASE_URL",
		Status:       models.AuditStatusSuccess,
		Message:      "Set environment variable DATABASE_URL (project scope)",
		Metadata: map[string]any{
			"key":   "DATABASE_URL",
			"scope": "runtime",
			"value": "postgres://user:supersecret@db:5432/prod",
			"nested": map[string]any{
				"webhook_secret": "whsec_12345",
				"client_secret":  "cs_98765",
				"access_token":   "ghp_secret",
				"safe_field":     "ok",
			},
		},
		IPAddress: "203.0.113.10",
	})
	require.NoError(t, err)

	encoded, err := json.Marshal(entry)
	require.NoError(t, err)
	jsonText := string(encoded)
	assert.NotContains(t, jsonText, "supersecret")
	assert.NotContains(t, jsonText, "whsec_12345")
	assert.NotContains(t, jsonText, "cs_98765")
	assert.NotContains(t, jsonText, "ghp_secret")
	assert.Contains(t, jsonText, "[REDACTED]")
	assert.Equal(t, "DATABASE_URL", entry.Metadata["key"])
	assert.Equal(t, "runtime", entry.Metadata["scope"])

	// Record an action by actorB and another project action by actorA
	time.Sleep(2 * time.Millisecond)
	_, err = svc.Record(ctx, AuditRecordInput{
		ActorID:      actorA,
		ProjectID:    &projID,
		ProjectName:  "shop-api",
		Action:       "deployment.rollback",
		ResourceType: "deployment",
		ResourceID:   uuid.NewString(),
		Status:       models.AuditStatusSuccess,
		Message:      "Rolled back release",
	})
	require.NoError(t, err)

	_, err = svc.Record(ctx, AuditRecordInput{
		ActorID:      actorB,
		Action:       "domain.verify",
		ResourceType: "domain",
		ResourceID:   "b.example.com",
		Message:      "Verified domain",
	})
	require.NoError(t, err)

	// Actor A sees only Actor A's logs, newest first
	logsA, err := svc.List(ctx, actorA, AuditListFilter{ProjectID: &projID})
	require.NoError(t, err)
	require.Len(t, logsA, 2)
	assert.Equal(t, "deployment.rollback", logsA[0].Action)
	assert.Equal(t, "env.set", logsA[1].Action)

	// Action filter works
	filtered, err := svc.List(ctx, actorA, AuditListFilter{Action: "env.set"})
	require.NoError(t, err)
	require.Len(t, filtered, 1)
	assert.Equal(t, "DATABASE_URL", filtered[0].ResourceID)

	// Actor B never sees Actor A's logs
	logsB, err := svc.List(ctx, actorB, AuditListFilter{ProjectID: &projID})
	require.NoError(t, err)
	assert.Empty(t, logsB)
}
