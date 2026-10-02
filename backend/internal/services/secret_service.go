package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
