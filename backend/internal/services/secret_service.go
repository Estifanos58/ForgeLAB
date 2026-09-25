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
	ID        uuid.UUID `json:"id"`
	ProjectID uuid.UUID `json:"project_id"`
	Key       string    `json:"key"`
	Value     string    `json:"value"` // Masked if secret: "••••••••"
	IsSecret  bool      `json:"is_secret"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type SetEnvVarInput struct {
	Key      string `json:"key"`
	Value    string `json:"value"`
	IsSecret bool   `json:"is_secret"`
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

// SetEnvVar creates or updates an environment variable for a project.
func (s *SecretService) SetEnvVar(ctx context.Context, projectID, ownerID uuid.UUID, input SetEnvVarInput) (*EnvVarResponse, error) {
	// Verify project ownership
	if _, err := s.projectService.GetProject(ctx, projectID, ownerID); err != nil {
		return nil, err
	}

	encryptedVal, err := s.encryptor.Encrypt([]byte(input.Value))
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt environment variable: %w", err)
	}

	now := time.Now()
	var envVar models.EnvironmentVariable

	// Upsert query
	err = s.db.QueryRow(ctx,
		`INSERT INTO environment_variables (id, project_id, key, encrypted_value, is_secret, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $6)
		 ON CONFLICT (project_id, key) DO UPDATE
		 SET encrypted_value = EXCLUDED.encrypted_value,
		     is_secret = EXCLUDED.is_secret,
		     updated_at = EXCLUDED.updated_at
		 RETURNING id, project_id, key, is_secret, created_at, updated_at`,
		uuid.New(), projectID, input.Key, encryptedVal, input.IsSecret, now,
	).Scan(&envVar.ID, &envVar.ProjectID, &envVar.Key, &envVar.IsSecret, &envVar.CreatedAt, &envVar.UpdatedAt)

	if err != nil {
		return nil, fmt.Errorf("failed to set environment variable: %w", err)
	}

	slog.Info("environment variable set", "project_id", projectID, "key", input.Key, "is_secret", input.IsSecret)

	maskedVal := input.Value
	if input.IsSecret {
		maskedVal = "••••••••"
	}

	return &EnvVarResponse{
		ID:        envVar.ID,
		ProjectID: envVar.ProjectID,
		Key:       envVar.Key,
		Value:     maskedVal,
		IsSecret:  envVar.IsSecret,
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
		`SELECT id, project_id, key, encrypted_value, is_secret, created_at, updated_at
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
		var key string
		var encVal []byte
		var isSecret bool
		var createdAt, updatedAt time.Time

		if err := rows.Scan(&id, &projID, &key, &encVal, &isSecret, &createdAt, &updatedAt); err != nil {
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
			Key:       key,
			Value:     val,
			IsSecret:  isSecret,
			CreatedAt: createdAt,
			UpdatedAt: updatedAt,
		})
	}

	if result == nil {
		result = []*EnvVarResponse{}
	}

	return result, nil
}

// DeleteEnvVar removes an environment variable.
func (s *SecretService) DeleteEnvVar(ctx context.Context, projectID, ownerID uuid.UUID, key string) error {
	if _, err := s.projectService.GetProject(ctx, projectID, ownerID); err != nil {
		return err
	}

	res, err := s.db.Exec(ctx,
		"DELETE FROM environment_variables WHERE project_id = $1 AND key = $2",
		projectID, key,
	)
	if err != nil {
		return fmt.Errorf("failed to delete environment variable: %w", err)
	}
	if res.RowsAffected() == 0 {
		return ErrEnvVarNotFound
	}

	slog.Info("environment variable deleted", "project_id", projectID, "key", key)
	return nil
}

// GetDecryptedEnvMap retrieves plaintext KEY=VALUE map for container injection at deploy time.
func (s *SecretService) GetDecryptedEnvMap(ctx context.Context, projectID uuid.UUID) (map[string]string, []string, error) {
	rows, err := s.db.Query(ctx,
		`SELECT key, encrypted_value FROM environment_variables WHERE project_id = $1`,
		projectID,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to query project secrets: %w", err)
	}
	defer rows.Close()

	envMap := make(map[string]string)
	secretValues := make([]string, 0)

	for rows.Next() {
		var key string
		var encVal []byte
		if err := rows.Scan(&key, &encVal); err != nil {
			return nil, nil, err
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
