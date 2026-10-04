package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/forgelab/backend/internal/crypto"
	"github.com/forgelab/backend/internal/models"
)

var (
	ErrEnvVarNotFound = errors.New("environment variable not found")
)

type EnvVarResponse struct {
	ID        uuid.UUID  `json:"id"`
	ProjectID uuid.UUID  `json:"project_id"`
	ServiceID *uuid.UUID `json:"service_id,omitempty"`
	Key       string     `json:"key"`
	Value     string     `json:"value"` // Masked if secret: "••••••••"
	IsSecret  bool       `json:"is_secret"`
	Scope     string     `json:"scope"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

type SetEnvVarInput struct {
	Key       string     `json:"key"`
	Value     string     `json:"value"`
	IsSecret  bool       `json:"is_secret"`
	ServiceID *uuid.UUID `json:"service_id,omitempty"`
	Scope     string     `json:"scope,omitempty"` // "runtime", "build", "both"
}

type SecretService struct {
	db             *pgxpool.Pool
	encryptor      *crypto.Encryptor
	projectService *ProjectService
}

func NewSecretService(db *pgxpool.Pool, encryptor *crypto.Encryptor, projectService *ProjectService) *SecretService {
	return &SecretService{
		db:             db,
		encryptor:      encryptor,
		projectService: projectService,
	}
}

// SetEnvVar creates or updates an environment variable for a project or specific service.
func (s *SecretService) SetEnvVar(ctx context.Context, projectID, ownerID uuid.UUID, input SetEnvVarInput) (*EnvVarResponse, error) {
	// Verify project ownership
	if _, err := s.projectService.GetProject(ctx, projectID, ownerID); err != nil {
		return nil, err
	}

	trimmedKey := strings.TrimSpace(input.Key)
	if trimmedKey == "" || len(trimmedKey) > 255 {
		return nil, errors.New("environment variable key must be between 1 and 255 characters")
	}
	for i, r := range trimmedKey {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && r != '_' && (i == 0 || (r < '0' || r > '9')) {
			return nil, errors.New("environment variable key must begin with a letter or underscore and contain only alphanumeric characters and underscores")
		}
	}
	input.Key = trimmedKey

	if input.ServiceID != nil {
		var serviceExists bool
		err := s.db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM services WHERE id = $1 AND project_id = $2)", *input.ServiceID, projectID).Scan(&serviceExists)
		if err != nil || !serviceExists {
			return nil, errors.New("service does not belong to specified project")
		}
	}

	encryptedVal, err := s.encryptor.Encrypt([]byte(input.Value))
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt environment variable: %w", err)
	}

	scope := input.Scope
	if scope == "" {
		scope = models.EnvScopeRuntime
	}

	now := time.Now()
	var envVar models.EnvironmentVariable

	if input.ServiceID == nil {
		err = s.db.QueryRow(ctx,
			`INSERT INTO environment_variables (id, project_id, service_id, key, encrypted_value, is_secret, scope, created_at, updated_at)
			 VALUES ($1, $2, NULL, $3, $4, $5, $6, $7, $7)
			 ON CONFLICT (project_id, key) WHERE service_id IS NULL DO UPDATE
			 SET encrypted_value = EXCLUDED.encrypted_value,
			     is_secret = EXCLUDED.is_secret,
			     scope = EXCLUDED.scope,
			     updated_at = EXCLUDED.updated_at
			 RETURNING id, project_id, service_id, key, is_secret, scope, created_at, updated_at`,
			uuid.New(), projectID, input.Key, encryptedVal, input.IsSecret, scope, now,
		).Scan(&envVar.ID, &envVar.ProjectID, &envVar.ServiceID, &envVar.Key, &envVar.IsSecret, &envVar.Scope, &envVar.CreatedAt, &envVar.UpdatedAt)
	} else {
		err = s.db.QueryRow(ctx,
			`INSERT INTO environment_variables (id, project_id, service_id, key, encrypted_value, is_secret, scope, created_at, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)
			 ON CONFLICT (project_id, service_id, key) WHERE service_id IS NOT NULL DO UPDATE
			 SET encrypted_value = EXCLUDED.encrypted_value,
			     is_secret = EXCLUDED.is_secret,
			     scope = EXCLUDED.scope,
			     updated_at = EXCLUDED.updated_at
			 RETURNING id, project_id, service_id, key, is_secret, scope, created_at, updated_at`,
			uuid.New(), projectID, input.ServiceID, input.Key, encryptedVal, input.IsSecret, scope, now,
		).Scan(&envVar.ID, &envVar.ProjectID, &envVar.ServiceID, &envVar.Key, &envVar.IsSecret, &envVar.Scope, &envVar.CreatedAt, &envVar.UpdatedAt)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to set environment variable: %w", err)
	}

	slog.Info("environment variable set", "project_id", projectID, "service_id", input.ServiceID, "key", input.Key, "is_secret", input.IsSecret, "scope", scope)

	maskedVal := input.Value
	if input.IsSecret {
		maskedVal = "••••••••"
	}

	return &EnvVarResponse{
		ID:        envVar.ID,
		ProjectID: envVar.ProjectID,
		ServiceID: envVar.ServiceID,
		Key:       envVar.Key,
		Value:     maskedVal,
		IsSecret:  envVar.IsSecret,
		Scope:     envVar.Scope,
		CreatedAt: envVar.CreatedAt,
		UpdatedAt: envVar.UpdatedAt,
	}, nil
}

// ListEnvVars lists all environment variables for a project with values masked for secrets.
func (s *SecretService) ListEnvVars(ctx context.Context, projectID, ownerID uuid.UUID) ([]*EnvVarResponse, error) {
	if _, err := s.projectService.GetProject(ctx, projectID, ownerID); err != nil {
		return nil, err
	}

	rows, err := s.db.Query(ctx,
		`SELECT id, project_id, service_id, key, encrypted_value, is_secret, scope, created_at, updated_at
		 FROM environment_variables WHERE project_id = $1 ORDER BY key ASC`,
		projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list environment variables: %w", err)
	}
	defer rows.Close()

	var result []*EnvVarResponse
	for rows.Next() {
		var id, projID uuid.UUID
		var svcID *uuid.UUID
		var key, scope string
		var encVal []byte
		var isSecret bool
		var createdAt, updatedAt time.Time

		if err := rows.Scan(&id, &projID, &svcID, &key, &encVal, &isSecret, &scope, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan env var: %w", err)
		}

		val := "••••••••"
		if !isSecret {
			dec, err := s.encryptor.Decrypt(encVal)
			if err == nil {
				val = string(dec)
			}
		}

		result = append(result, &EnvVarResponse{
			ID:        id,
			ProjectID: projID,
			ServiceID: svcID,
			Key:       key,
			Value:     val,
			IsSecret:  isSecret,
			Scope:     scope,
			CreatedAt: createdAt,
			UpdatedAt: updatedAt,
		})
	}

	if result == nil {
		result = []*EnvVarResponse{}
	}

	return result, nil
}

// DeleteEnvVar removes an environment variable. If serviceID is provided, deletes the service-specific variable.
func (s *SecretService) DeleteEnvVar(ctx context.Context, projectID, ownerID uuid.UUID, key string, serviceID *uuid.UUID) error {
	if _, err := s.projectService.GetProject(ctx, projectID, ownerID); err != nil {
		return err
	}

	var res interface{ RowsAffected() int64 }
	var err error

	if serviceID == nil {
		res, err = s.db.Exec(ctx,
			"DELETE FROM environment_variables WHERE project_id = $1 AND key = $2 AND service_id IS NULL",
			projectID, key,
		)
	} else {
		res, err = s.db.Exec(ctx,
			"DELETE FROM environment_variables WHERE project_id = $1 AND key = $2 AND service_id = $3",
			projectID, key, serviceID,
		)
	}

	if err != nil {
		return fmt.Errorf("failed to delete environment variable: %w", err)
	}
	if res.RowsAffected() == 0 {
		return ErrEnvVarNotFound
	}

	slog.Info("environment variable deleted", "project_id", projectID, "service_id", serviceID, "key", key)
	return nil
}

// GetDecryptedEnvMap retrieves plaintext KEY=VALUE map for container or build injection.
// If serviceID is provided, service-scoped env vars override project-level env vars.
// If targetScope is provided ("runtime", "build"), filters by scope ("both" matches either).
func (s *SecretService) GetDecryptedEnvMap(ctx context.Context, projectID uuid.UUID, serviceID *uuid.UUID, targetScope string) (map[string]string, []string, error) {
	if s.db == nil {
		return make(map[string]string), []string{}, nil
	}

	rows, err := s.db.Query(ctx,
		`SELECT key, encrypted_value, service_id, scope
		 FROM environment_variables
		 WHERE project_id = $1 AND (service_id IS NULL OR ($2::uuid IS NOT NULL AND service_id = $2))
		 ORDER BY CASE WHEN service_id IS NULL THEN 0 ELSE 1 END ASC`,
		projectID, serviceID,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to query secrets: %w", err)
	}
	defer rows.Close()

	envMap := make(map[string]string)
	secretValues := make([]string, 0)

	for rows.Next() {
		var key, scope string
		var encVal []byte
		var rowServiceID *uuid.UUID
		if err := rows.Scan(&key, &encVal, &rowServiceID, &scope); err != nil {
			return nil, nil, err
		}

		if targetScope != "" && scope != models.EnvScopeBoth && scope != targetScope {
			continue
		}

		dec, err := s.encryptor.Decrypt(encVal)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to decrypt key %s: %w", key, err)
		}

		plaintext := string(dec)
		envMap[key] = plaintext
		secretValues = append(secretValues, plaintext)
	}

	return envMap, secretValues, nil
}

// ComputeEnvConfigHash calculates a deterministic SHA-256 hash of the effective environment configuration
// (project variables merged with service-specific overrides).
func (s *SecretService) ComputeEnvConfigHash(ctx context.Context, projectID uuid.UUID, serviceID *uuid.UUID) (*string, error) {
	if s.db == nil {
		return nil, nil
	}

	rows, err := s.db.Query(ctx,
		`SELECT key, encrypted_value, is_secret, scope, service_id
		 FROM environment_variables
		 WHERE project_id = $1 AND (service_id IS NULL OR ($2::uuid IS NOT NULL AND service_id = $2))
		 ORDER BY CASE WHEN service_id IS NULL THEN 0 ELSE 1 END ASC`,
		projectID, serviceID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query environment variables for hash: %w", err)
	}
	defer rows.Close()

	type varEntry struct {
		val      string
		isSecret bool
		scope    string
	}
	effectiveVars := make(map[string]varEntry)

	for rows.Next() {
		var key, scope string
		var encVal []byte
		var isSecret bool
		var rowServiceID *uuid.UUID

		if err := rows.Scan(&key, &encVal, &isSecret, &scope, &rowServiceID); err != nil {
			return nil, err
		}

		dec, err := s.encryptor.Decrypt(encVal)
		if err != nil {
			return nil, fmt.Errorf("failed to decrypt %s: %w", key, err)
		}

		// Service-specific rows will overwrite project-level defaults deterministically due to ordering
		effectiveVars[key] = varEntry{
			val:      string(dec),
			isSecret: isSecret,
			scope:    scope,
		}
	}

	if len(effectiveVars) == 0 {
		return nil, nil
	}

	// Deterministic sort by key
	keys := make([]string, 0, len(effectiveVars))
	for k := range effectiveVars {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var sb strings.Builder
	for _, k := range keys {
		entry := effectiveVars[k]
		sb.WriteString(fmt.Sprintf("%s=%s;scope=%s;secret=%t\n", k, entry.val, entry.scope, entry.isSecret))
	}

	sum := sha256.Sum256([]byte(sb.String()))
	hashStr := hex.EncodeToString(sum[:])
	return &hashStr, nil
}

// EnvSnapshotEntry represents an individual variable in a deployment's environment snapshot.
type EnvSnapshotEntry struct {
	Key      string `json:"key"`
	Value    string `json:"value"`
	Scope    string `json:"scope"`
	IsSecret bool   `json:"is_secret"`
}

// CreateEnvSnapshot captures all effective environment variables (project variables overridden by service vars),
// serializes them to JSON, and encrypts the payload using AES-256-GCM.
// Returns the encrypted snapshot bytes and the deterministic SHA-256 hash.
func (s *SecretService) CreateEnvSnapshot(ctx context.Context, projectID uuid.UUID, serviceID *uuid.UUID) ([]byte, *string, error) {
	if s.db == nil || s.encryptor == nil {
		return nil, nil, nil
	}

	rows, err := s.db.Query(ctx,
		`SELECT key, encrypted_value, is_secret, scope, service_id
		 FROM environment_variables
		 WHERE project_id = $1 AND (service_id IS NULL OR ($2::uuid IS NOT NULL AND service_id = $2))
		 ORDER BY CASE WHEN service_id IS NULL THEN 0 ELSE 1 END ASC`,
		projectID, serviceID,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to query environment variables for snapshot: %w", err)
	}
	defer rows.Close()

	effectiveVars := make(map[string]EnvSnapshotEntry)
	for rows.Next() {
		var key, scope string
		var encVal []byte
		var isSecret bool
		var rowServiceID *uuid.UUID

		if err := rows.Scan(&key, &encVal, &isSecret, &scope, &rowServiceID); err != nil {
			return nil, nil, err
		}

		dec, err := s.encryptor.Decrypt(encVal)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to decrypt %s: %w", key, err)
		}

		effectiveVars[key] = EnvSnapshotEntry{
			Key:      key,
			Value:    string(dec),
			Scope:    scope,
			IsSecret: isSecret,
		}
	}

	if len(effectiveVars) == 0 {
		return nil, nil, nil
	}

	// Deterministic sort by key
	keys := make([]string, 0, len(effectiveVars))
	for k := range effectiveVars {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var sb strings.Builder
	entries := make([]EnvSnapshotEntry, 0, len(keys))
	for _, k := range keys {
		entry := effectiveVars[k]
		entries = append(entries, entry)
		sb.WriteString(fmt.Sprintf("%s=%s;scope=%s;secret=%t\n", k, entry.Value, entry.Scope, entry.IsSecret))
	}

	sum := sha256.Sum256([]byte(sb.String()))
	hashStr := hex.EncodeToString(sum[:])

	jsonData, err := json.Marshal(entries)
	if err != nil {
		return nil, &hashStr, fmt.Errorf("failed to serialize env snapshot: %w", err)
	}

	encryptedSnapshot, err := s.encryptor.Encrypt(jsonData)
	if err != nil {
		return nil, &hashStr, fmt.Errorf("failed to encrypt env snapshot: %w", err)
	}

	return encryptedSnapshot, &hashStr, nil
}

// GetEnvMapFromSnapshot decrypts and parses an AES-256-GCM encrypted snapshot,
// filtering variables by targetScope ("runtime", "build", or "" for all).
// Returns plaintext key-value map and slice of secret values for log redaction.
func (s *SecretService) GetEnvMapFromSnapshot(snapshotBytes []byte, targetScope string) (map[string]string, []string, error) {
	if len(snapshotBytes) == 0 || s.encryptor == nil {
		return make(map[string]string), []string{}, nil
	}

	decrypted, err := s.encryptor.Decrypt(snapshotBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to decrypt env snapshot: %w", err)
	}

	var entries []EnvSnapshotEntry
	if err := json.Unmarshal(decrypted, &entries); err != nil {
		return nil, nil, fmt.Errorf("failed to parse env snapshot JSON: %w", err)
	}

	envMap := make(map[string]string)
	secretValues := make([]string, 0)

	for _, entry := range entries {
		if targetScope != "" && entry.Scope != models.EnvScopeBoth && entry.Scope != targetScope {
			continue
		}
		envMap[entry.Key] = entry.Value
		if entry.IsSecret {
			secretValues = append(secretValues, entry.Value)
		}
	}

	return envMap, secretValues, nil
}

// GetBuildVariables separates variables for build time into non-secret build arguments and secret variables.
// Non-secret variables can be passed as BuildArgs; secret variables must NEVER be passed as BuildArgs.
func (s *SecretService) GetBuildVariables(ctx context.Context, projectID uuid.UUID, serviceID *uuid.UUID, snapshotBytes []byte) (map[string]string, map[string]string, []string, error) {
	normalArgs := make(map[string]string)
	secretVars := make(map[string]string)
	allSecretValues := make([]string, 0)

	if len(snapshotBytes) > 0 && s.encryptor != nil {
		decrypted, err := s.encryptor.Decrypt(snapshotBytes)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to decrypt env snapshot: %w", err)
		}
		var entries []EnvSnapshotEntry
		if err := json.Unmarshal(decrypted, &entries); err != nil {
			return nil, nil, nil, fmt.Errorf("failed to parse env snapshot JSON: %w", err)
		}
		for _, entry := range entries {
			if entry.IsSecret {
				allSecretValues = append(allSecretValues, entry.Value)
			}
			if entry.Scope != models.EnvScopeBoth && entry.Scope != models.EnvScopeBuild {
				continue
			}
			if entry.IsSecret {
				secretVars[entry.Key] = entry.Value
			} else {
				normalArgs[entry.Key] = entry.Value
			}
		}
		return normalArgs, secretVars, allSecretValues, nil
	}

	if s.db == nil || s.encryptor == nil {
		return normalArgs, secretVars, allSecretValues, nil
	}

	rows, err := s.db.Query(ctx,
		`SELECT key, encrypted_value, is_secret, scope, service_id
		 FROM environment_variables
		 WHERE project_id = $1 AND (service_id IS NULL OR ($2::uuid IS NOT NULL AND service_id = $2))
		 ORDER BY CASE WHEN service_id IS NULL THEN 0 ELSE 1 END ASC`,
		projectID, serviceID,
	)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to query environment variables for build: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var key, scope string
		var encVal []byte
		var isSecret bool
		var rowServiceID *uuid.UUID
		if err := rows.Scan(&key, &encVal, &isSecret, &scope, &rowServiceID); err != nil {
			return nil, nil, nil, err
		}
		dec, err := s.encryptor.Decrypt(encVal)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to decrypt %s: %w", key, err)
		}
		val := string(dec)
		if isSecret {
			allSecretValues = append(allSecretValues, val)
		}
		if scope != models.EnvScopeBoth && scope != models.EnvScopeBuild {
			continue
		}
		if isSecret {
			secretVars[key] = val
		} else {
			normalArgs[key] = val
		}
	}

	return normalArgs, secretVars, allSecretValues, nil
}

// IsSecretKey uses sensible heuristics to classify whether an environment variable key represents a secret.
// Names containing PASSWORD, SECRET, TOKEN, API_KEY, etc. are classified as secret.
// Common public frontend variables (e.g. NEXT_PUBLIC_) without secret keywords remain non-secret.
func IsSecretKey(key string) bool {
	upper := strings.ToUpper(strings.TrimSpace(key))
	if upper == "" {
		return false
	}

	// Explicitly non-secret prefixes (e.g. frontend public config) unless they also contain explicit secret keywords
	isPublicPrefix := strings.HasPrefix(upper, "NEXT_PUBLIC_") ||
		strings.HasPrefix(upper, "VITE_") ||
		strings.HasPrefix(upper, "REACT_APP_") ||
		strings.HasPrefix(upper, "PUBLIC_")

	secretKeywords := []string{
		"PASSWORD", "PASSWD", "SECRET", "TOKEN", "API_KEY", "APIKEY",
		"PRIVATE_KEY", "PRIVKEY", "ACCESS_KEY", "AUTH_KEY", "DATABASE_URL",
		"DB_PASS", "DB_PASSWORD", "CREDENTIAL", "CREDENTIALS", "CERTIFICATE",
		"SIGNING_KEY", "ENCRYPTION_KEY", "BEARER", "SECRET_KEY", "CLIENT_SECRET",
	}

	for _, kw := range secretKeywords {
		if strings.Contains(upper, kw) {
			return true
		}
	}

	if isPublicPrefix {
		return false
	}

	return false
}

// ImportEnvVarEntry represents a candidate environment variable for import.
type ImportEnvVarEntry struct {
	ServiceID *uuid.UUID
	Key       string
	Value     string
	Scope     string
	IsSecret  *bool // nil = auto-classify via IsSecretKey
}

// ImportEnvVarsIfMissing idempotently imports environment variables, preserving existing user settings.
// Values are immediately encrypted at rest using AES-256-GCM.
// Precedence rule: existing ForgeLAB variable > imported .env value.
func (s *SecretService) ImportEnvVarsIfMissing(ctx context.Context, projectID, ownerID uuid.UUID, entries []ImportEnvVarEntry) (int, error) {
	if s.db == nil || s.encryptor == nil {
		return 0, nil
	}

	// Verify project ownership if ownerID is provided
	if ownerID != uuid.Nil && s.projectService != nil {
		if _, err := s.projectService.GetProject(ctx, projectID, ownerID); err != nil {
			return 0, err
		}
	}

	// 1. Fetch existing keys for this project to prevent overwriting user configuration
	rows, err := s.db.Query(ctx, `SELECT key, service_id FROM environment_variables WHERE project_id = $1`, projectID)
	if err != nil {
		return 0, fmt.Errorf("failed to query existing environment variables: %w", err)
	}
	defer rows.Close()

	existingSet := make(map[string]bool)
	for rows.Next() {
		var k string
		var svcID *uuid.UUID
		if err := rows.Scan(&k, &svcID); err == nil {
			svcKey := "project"
			if svcID != nil {
				svcKey = svcID.String()
			}
			existingSet[svcKey+":"+k] = true
		}
	}

	insertedCount := 0

	for _, entry := range entries {
		trimmedKey := strings.TrimSpace(entry.Key)
		if trimmedKey == "" || len(trimmedKey) > 255 {
			continue
		}

		svcKey := "project"
		if entry.ServiceID != nil {
			svcKey = entry.ServiceID.String()
		}

		// Conflict resolution rule: existing ForgeLAB variable > imported .env value
		if existingSet[svcKey+":"+trimmedKey] {
			continue
		}

		// Determine is_secret
		isSecret := IsSecretKey(trimmedKey)
		if entry.IsSecret != nil {
			isSecret = *entry.IsSecret
		}

		// Default scope = runtime
		scope := entry.Scope
		if scope == "" {
			scope = models.EnvScopeRuntime
		}

		// Encrypt value at rest immediately with AES-256-GCM
		encVal, err := s.encryptor.Encrypt([]byte(entry.Value))
		if err != nil {
			slog.Warn("failed to encrypt imported environment variable", "key", trimmedKey, "error", err)
			continue
		}

		now := time.Now()
		if entry.ServiceID == nil {
			_, err = s.db.Exec(ctx,
				`INSERT INTO environment_variables (id, project_id, service_id, key, encrypted_value, is_secret, scope, created_at, updated_at)
				 VALUES ($1, $2, NULL, $3, $4, $5, $6, $7, $7)
				 ON CONFLICT (project_id, key) WHERE service_id IS NULL DO NOTHING`,
				uuid.New(), projectID, trimmedKey, encVal, isSecret, scope, now,
			)
		} else {
			_, err = s.db.Exec(ctx,
				`INSERT INTO environment_variables (id, project_id, service_id, key, encrypted_value, is_secret, scope, created_at, updated_at)
				 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)
				 ON CONFLICT (project_id, service_id, key) WHERE service_id IS NOT NULL DO NOTHING`,
				uuid.New(), projectID, entry.ServiceID, trimmedKey, encVal, isSecret, scope, now,
			)
		}

		if err == nil {
			existingSet[svcKey+":"+trimmedKey] = true
			insertedCount++
		}
	}

	return insertedCount, nil
}
