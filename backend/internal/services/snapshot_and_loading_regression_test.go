package services_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/crypto"
	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/services"
)

func TestSnapshotHandling_DecryptionAndParseFailure(t *testing.T) {
	encKey := "dGhpcy1pcy1hLWRldi1rZXktY2hhbmdlLWluLXByb2Q="
	encryptor, err := crypto.NewEncryptor(encKey)
	require.NoError(t, err)

	secretService := services.NewSecretService(nil, encryptor, nil)
	ctx := context.Background()
	projectID := uuid.New()

	t.Run("GetEnvMapFromSnapshot fails safely on corrupted ciphertext", func(t *testing.T) {
		corruptedCiphertext := []byte("not-a-valid-encrypted-payload")
		envMap, secrets, err := secretService.GetEnvMapFromSnapshot(corruptedCiphertext, models.EnvScopeRuntime)
		assert.Error(t, err, "must return error on corrupted snapshot ciphertext")
		assert.Nil(t, envMap, "envMap must be nil on failure")
		assert.Nil(t, secrets, "secrets must be nil on failure")
	})

	t.Run("GetEnvMapFromSnapshot fails safely on decrypted payload that is not JSON", func(t *testing.T) {
		encryptedInvalidJSON, err := encryptor.Encrypt([]byte("this is not valid JSON!"))
		require.NoError(t, err)

		envMap, secrets, err := secretService.GetEnvMapFromSnapshot(encryptedInvalidJSON, models.EnvScopeRuntime)
		assert.Error(t, err, "must return error on invalid JSON snapshot payload")
		assert.Contains(t, err.Error(), "failed to parse env snapshot JSON")
		assert.Nil(t, envMap)
		assert.Nil(t, secrets)
	})

	t.Run("GetBuildVariables fails safely on corrupted ciphertext", func(t *testing.T) {
		corruptedCiphertext := []byte("corrupted-bytes-for-snapshot")
		normalArgs, secretVars, allSecrets, err := secretService.GetBuildVariables(ctx, projectID, nil, corruptedCiphertext)
		assert.Error(t, err, "must return error on corrupted snapshot ciphertext")
		assert.Nil(t, normalArgs)
		assert.Nil(t, secretVars)
		assert.Nil(t, allSecrets)
	})

	t.Run("GetBuildVariables fails safely on decrypted payload that is not JSON", func(t *testing.T) {
		encryptedInvalidJSON, err := encryptor.Encrypt([]byte("invalid json string"))
		require.NoError(t, err)

		normalArgs, secretVars, allSecrets, err := secretService.GetBuildVariables(ctx, projectID, nil, encryptedInvalidJSON)
		assert.Error(t, err, "must return error on invalid JSON snapshot payload")
		assert.Contains(t, err.Error(), "failed to parse env snapshot JSON")
		assert.Nil(t, normalArgs)
		assert.Nil(t, secretVars)
		assert.Nil(t, allSecrets)
	})

	t.Run("GetEnvMapFromSnapshot and GetBuildVariables preserve snapshot contents accurately", func(t *testing.T) {
		validEntries := []services.EnvSnapshotEntry{
			{
				Key:      "DATABASE_URL",
				Value:    "postgres://user:pass@db:5432/app",
				Scope:    models.EnvScopeBoth,
				IsSecret: true,
			},
			{
				Key:      "NEXT_PUBLIC_API_URL",
				Value:    "https://api.example.com",
				Scope:    models.EnvScopeRuntime,
				IsSecret: false,
			},
			{
				Key:      "NODE_ENV",
				Value:    "production",
				Scope:    models.EnvScopeRuntime,
				IsSecret: false,
			},
		}
		rawJSON, err := json.Marshal(validEntries)
		require.NoError(t, err)

		encryptedValid, err := encryptor.Encrypt(rawJSON)
		require.NoError(t, err)

		// Test GetEnvMapFromSnapshot
		runtimeMap, secrets, err := secretService.GetEnvMapFromSnapshot(encryptedValid, models.EnvScopeRuntime)
		require.NoError(t, err)
		assert.Equal(t, "postgres://user:pass@db:5432/app", runtimeMap["DATABASE_URL"])
		assert.Equal(t, "https://api.example.com", runtimeMap["NEXT_PUBLIC_API_URL"])
		assert.Equal(t, "production", runtimeMap["NODE_ENV"])
		assert.Contains(t, secrets, "postgres://user:pass@db:5432/app")

		// Test GetBuildVariables
		normalArgs, secretVars, allSecrets, err := secretService.GetBuildVariables(ctx, projectID, nil, encryptedValid)
		require.NoError(t, err)
		assert.Equal(t, "postgres://user:pass@db:5432/app", secretVars["DATABASE_URL"])
		assert.Equal(t, "https://api.example.com", normalArgs["NEXT_PUBLIC_API_URL"])
		assert.Contains(t, allSecrets, "postgres://user:pass@db:5432/app")
	})
}

func TestServiceDeployment_MalformedJSONFieldValidation(t *testing.T) {
	badJSON := []byte(`{invalid-json}`)

	t.Run("Malformed depends_on produces unmarshal error", func(t *testing.T) {
		var dependsOn []string
		err := json.Unmarshal(badJSON, &dependsOn)
		assert.Error(t, err, "must reject malformed depends_on JSON")
	})

	t.Run("Malformed volumes produces unmarshal error", func(t *testing.T) {
		var volumes []models.VolumeMountConfig
		err := json.Unmarshal(badJSON, &volumes)
		assert.Error(t, err, "must reject malformed volumes JSON")
	})

	t.Run("Malformed networks produces unmarshal error", func(t *testing.T) {
		var networks []string
		err := json.Unmarshal(badJSON, &networks)
		assert.Error(t, err, "must reject malformed networks JSON")
	})

	t.Run("Malformed healthcheck_config produces unmarshal error", func(t *testing.T) {
		var hc models.HealthCheckConfig
		err := json.Unmarshal(badJSON, &hc)
		assert.Error(t, err, "must reject malformed healthcheck_config JSON")
	})

	t.Run("Malformed depends_on_conditions produces unmarshal error", func(t *testing.T) {
		var depConds map[string]string
		err := json.Unmarshal(badJSON, &depConds)
		assert.Error(t, err, "must reject malformed depends_on_conditions JSON")
	})

	t.Run("Malformed build_candidates produces unmarshal error", func(t *testing.T) {
		var candidates []models.BuildCandidate
		err := json.Unmarshal(badJSON, &candidates)
		assert.Error(t, err, "must reject malformed build_candidates JSON")
	})
}
