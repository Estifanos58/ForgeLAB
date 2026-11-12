package services

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/forgelab/backend/internal/models"
)

const (
	defaultAuditLogLimit = 50
	maxAuditLogLimit     = 200
	maxMemAuditLogs      = 5000
)

// AuditRecordInput describes an operational audit event to persist.
type AuditRecordInput struct {
	ActorID      uuid.UUID
	ActorLabel   string
	IsGuest      bool
	ProjectID    *uuid.UUID
	ProjectName  string
	Action       string
	ResourceType string
	ResourceID   string
	Status       string
	Message      string
	Metadata     map[string]any
	IPAddress    string
}

// AuditListFilter controls audit log listing.
type AuditListFilter struct {
	ProjectID    *uuid.UUID
	Action       string
	ResourceType string
	Limit        int
	Offset       int
}

// AuditService persists and queries operational audit trail entries.
type AuditService struct {
	db *pgxpool.Pool

	memMu   sync.RWMutex
	memLogs []models.AuditLog
}

// NewAuditService creates an AuditService backed by PostgreSQL (or in-memory store when db is nil).
func NewAuditService(db *pgxpool.Pool) *AuditService {
	return &AuditService{db: db}
}

// Record validates, sanitizes, and stores an operational audit event.
func (s *AuditService) Record(ctx context.Context, input AuditRecordInput) (*models.AuditLog, error) {
	if input.ActorID == uuid.Nil {
		return nil, fmt.Errorf("audit actor_id is required")
	}
	if strings.TrimSpace(input.Action) == "" {
		return nil, fmt.Errorf("audit action is required")
	}
	if strings.TrimSpace(input.ResourceType) == "" {
		return nil, fmt.Errorf("audit resource_type is required")
	}

	status := strings.TrimSpace(input.Status)
	if status == "" {
		status = models.AuditStatusSuccess
	}

	actorLabel := strings.TrimSpace(input.ActorLabel)
	isGuest := input.IsGuest || models.IsGuest(input.ActorID)
	if actorLabel == "" {
		if isGuest {
			actorLabel = "Guest Session"
		} else {
			actorLabel = input.ActorID.String()[:8]
		}
	}

	var projIDCopy *uuid.UUID
	if input.ProjectID != nil && *input.ProjectID != uuid.Nil {
		pid := *input.ProjectID
		projIDCopy = &pid
	}

	sanitizedMeta := sanitizeAuditMetadata(input.Metadata)
	entry := models.AuditLog{
		ID:           uuid.New(),
		ActorID:      input.ActorID,
		ActorLabel:   actorLabel,
		IsGuest:      isGuest,
		ProjectID:    projIDCopy,
		ProjectName:  strings.TrimSpace(input.ProjectName),
		Action:       strings.TrimSpace(input.Action),
		ResourceType: strings.TrimSpace(input.ResourceType),
		ResourceID:   strings.TrimSpace(input.ResourceID),
		Status:       status,
		Message:      strings.TrimSpace(input.Message),
		Metadata:     sanitizedMeta,
		IPAddress:    strings.TrimSpace(input.IPAddress),
		CreatedAt:    time.Now().UTC(),
	}

	if s.db == nil {
		s.memMu.Lock()
		defer s.memMu.Unlock()
		s.memLogs = append(s.memLogs, entry)
		if len(s.memLogs) > maxMemAuditLogs {
			trim := len(s.memLogs) - maxMemAuditLogs
			s.memLogs = append([]models.AuditLog(nil), s.memLogs[trim:]...)
		}
		cp := entry
		return &cp, nil
	}

	metaJSON := []byte("{}")
	if len(sanitizedMeta) > 0 {
		if encoded, err := json.Marshal(sanitizedMeta); err == nil {
			metaJSON = encoded
		}
	}

	_, err := s.db.Exec(ctx, `
		INSERT INTO audit_logs (
			id, actor_id, actor_label, is_guest, project_id, project_name,
			action, resource_type, resource_id, status, message, metadata, ip_address, created_at
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10, $11, $12, $13, $14
		)`,
		entry.ID, entry.ActorID, entry.ActorLabel, entry.IsGuest, entry.ProjectID, entry.ProjectName,
		entry.Action, entry.ResourceType, entry.ResourceID, entry.Status, entry.Message, metaJSON, entry.IPAddress, entry.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("insert audit log: %w", err)
	}
	return &entry, nil
}

// RecordBestEffort writes an audit log without propagating errors to the caller.
func (s *AuditService) RecordBestEffort(ctx context.Context, input AuditRecordInput) {
	if s == nil {
		return
	}
	writeCtx := context.WithoutCancel(ctx)
	writeCtx, cancel := context.WithTimeout(writeCtx, 3*time.Second)
	defer cancel()

	if _, err := s.Record(writeCtx, input); err != nil {
		slog.Warn("failed to persist operational audit log",
			"action", input.Action,
			"resource_type", input.ResourceType,
			"error", err,
		)
	}
}

// List retrieves operational audit logs strictly scoped to actorID.
func (s *AuditService) List(ctx context.Context, actorID uuid.UUID, filter AuditListFilter) ([]models.AuditLog, error) {
	if actorID == uuid.Nil {
		return nil, fmt.Errorf("actor_id is required")
	}

	limit := filter.Limit
	if limit <= 0 {
		limit = defaultAuditLogLimit
	}
	if limit > maxAuditLogLimit {
		limit = maxAuditLogLimit
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}

	actionFilter := strings.TrimSpace(filter.Action)
	resourceFilter := strings.TrimSpace(filter.ResourceType)

	if s.db == nil {
		s.memMu.RLock()
		var matched []models.AuditLog
		for _, item := range s.memLogs {
			if item.ActorID != actorID {
				continue
			}
			if filter.ProjectID != nil && *filter.ProjectID != uuid.Nil {
				if item.ProjectID == nil || *item.ProjectID != *filter.ProjectID {
					continue
				}
			}
			if actionFilter != "" && item.Action != actionFilter {
				continue
			}
			if resourceFilter != "" && item.ResourceType != resourceFilter {
				continue
			}
			matched = append(matched, item)
		}
		s.memMu.RUnlock()

		sort.Slice(matched, func(i, j int) bool {
			return matched[i].CreatedAt.After(matched[j].CreatedAt)
		})
		if offset >= len(matched) {
			return []models.AuditLog{}, nil
		}
		end := offset + limit
		if end > len(matched) {
			end = len(matched)
		}
		return matched[offset:end], nil
	}

	rows, err := s.db.Query(ctx, `
		SELECT id, actor_id, actor_label, is_guest, project_id, project_name,
		       action, resource_type, resource_id, status, message, metadata, ip_address, created_at
		FROM audit_logs
		WHERE actor_id = $1
		  AND ($2::uuid IS NULL OR project_id = $2)
		  AND ($3 = '' OR action = $3)
		  AND ($4 = '' OR resource_type = $4)
		ORDER BY created_at DESC
		LIMIT $5 OFFSET $6`,
		actorID, filter.ProjectID, actionFilter, resourceFilter, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("query audit logs: %w", err)
	}
	defer rows.Close()

	var logs []models.AuditLog
	for rows.Next() {
		var item models.AuditLog
		var rawMeta []byte
		if err := rows.Scan(
			&item.ID, &item.ActorID, &item.ActorLabel, &item.IsGuest, &item.ProjectID, &item.ProjectName,
			&item.Action, &item.ResourceType, &item.ResourceID, &item.Status, &item.Message, &rawMeta, &item.IPAddress, &item.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan audit log row: %w", err)
		}
		if len(rawMeta) > 0 && string(rawMeta) != "{}" {
			_ = json.Unmarshal(rawMeta, &item.Metadata)
		}
		logs = append(logs, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate audit logs: %w", err)
	}
	if logs == nil {
		logs = []models.AuditLog{}
	}
	return logs, nil
}

func sanitizeAuditMetadata(raw map[string]any) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	out := make(map[string]any, len(raw))
	for k, v := range raw {
		if isSensitiveAuditKey(k) {
			out[k] = "[REDACTED]"
			continue
		}
		switch typed := v.(type) {
		case map[string]any:
			out[k] = sanitizeAuditMetadata(typed)
		default:
			out[k] = v
		}
	}
	return out
}

func isSensitiveAuditKey(key string) bool {
	lower := strings.ToLower(strings.TrimSpace(key))
	if lower == "" {
		return false
	}
	if lower == "value" || lower == "secret" || lower == "password" || lower == "token" ||
		lower == "api_key" || lower == "apikey" || lower == "private_key" || lower == "encryption_key" {
		return true
	}
	return strings.Contains(lower, "password") ||
		strings.Contains(lower, "secret_value") ||
		strings.Contains(lower, "private_key") ||
		strings.Contains(lower, "access_token") ||
		strings.Contains(lower, "refresh_token") ||
		strings.Contains(lower, "webhook_secret") ||
		strings.Contains(lower, "client_secret")
}
