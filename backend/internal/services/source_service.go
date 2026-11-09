package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/forgelab/backend/internal/analyzer"
	"github.com/forgelab/backend/internal/crypto"
	"github.com/forgelab/backend/internal/detector"
	"github.com/forgelab/backend/internal/models"
)

var (
	ErrInvalidSourceArchive      = errors.New("invalid or corrupt source archive")
	ErrPathTraversalDetected     = errors.New("path traversal detected in source archive")
	ErrArchiveTooLarge           = errors.New("uncompressed source exceeds maximum allowed size (100MB)")
	ErrSourceDirNotFound         = errors.New("source directory not found")
	ErrUnauthorizedSource        = errors.New("unauthorized source workspace access")
	ErrSourceNotReady            = errors.New("source workspace is still processing")
	ErrOwnerStorageQuotaExceeded = errors.New("owner storage quota exceeded: total source storage exceeds maximum allowed quota (1GB)")
)

const (
	MaxUncompressedBytes      = 100 * 1024 * 1024  // 100MB per archive
	MaxOwnerStorageQuotaBytes = 1024 * 1024 * 1024 // 1GB cumulative per owner
)

const (
	SourceStatusUploading  = "uploading"
	SourceStatusProcessing = "processing"
	SourceStatusReady      = "ready"
	SourceStatusFailed     = "failed"
	SourceStatusCancelled  = "cancelled"

	SourcePhaseUploading  = "uploading"
	SourcePhaseFinalizing = "finalizing"
	SourcePhaseDetecting  = "detecting"
	SourcePhaseReady      = "ready"
	SourcePhaseFailed     = "failed"
)

type SourceUploadResult struct {
	SourceID       uuid.UUID                    `json:"source_id"`
	Source         map[string]interface{}       `json:"source,omitempty"`
	Services       []analyzer.ServiceDefinition `json:"services,omitempty"`
	Status         string                       `json:"status"`
	Phase          string                       `json:"phase"`
	FilesCount     int                          `json:"files_count"`
	ProcessedFiles int                          `json:"processed_files"`
	TotalBytes     int64                        `json:"total_bytes"`
	ProcessedBytes int64                        `json:"processed_bytes"`
	Runtime        string                       `json:"runtime,omitempty"`
	Framework      string                       `json:"framework,omitempty"`
	Detection      *detector.DetectionResult    `json:"detection,omitempty"`
	Analysis       *analyzer.AnalysisResult     `json:"analysis,omitempty"`
	Error          *string                      `json:"error"`
}

type SourceWorkspaceRecord struct {
	ID             uuid.UUID
	OwnerID        uuid.UUID
	WorkspacePath  string
	FilesCount     int
	ProcessedFiles int
	TotalBytes     int64
	ProcessedBytes int64
	Status         string
	Phase          string
	Runtime        string
	Framework      string
	Detection      *detector.DetectionResult
	Analysis       *analyzer.AnalysisResult
	Error          *string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type SourceService struct {
	db              *pgxpool.Pool
	sourcesDir      string
	encryptor       *crypto.Encryptor
	fallbackOwners  sync.Map
	fallbackSources sync.Map
	sourceRecords   sync.Map // In-memory Source cache for tests
	analysisCache   sync.Map
}

func NewSourceService(db *pgxpool.Pool, sourcesDir string, encryptor ...*crypto.Encryptor) *SourceService {
	if sourcesDir == "" {
		sourcesDir = "./data/sources"
	}
	_ = os.MkdirAll(sourcesDir, 0700)
	_ = os.MkdirAll(filepath.Join(sourcesDir, ".uploads"), 0700)

	var enc *crypto.Encryptor
	if len(encryptor) > 0 && encryptor[0] != nil {
		enc = encryptor[0]
	} else if key := os.Getenv("FORGELAB_ENCRYPTION_KEY"); key != "" {
		if e, err := crypto.NewEncryptor(key); err == nil {
			enc = e
		}
	} else if key := os.Getenv("ENCRYPTION_KEY"); key != "" {
		if e, err := crypto.NewEncryptor(key); err == nil {
			enc = e
		}
	}

	return &SourceService{
		db:         db,
		sourcesDir: sourcesDir,
		encryptor:  enc,
	}
}

// NewSecureSourceService initializes a SourceService and returns an error if encryption is unavailable.
func NewSecureSourceService(db *pgxpool.Pool, sourcesDir string, encryptor *crypto.Encryptor) (*SourceService, error) {
	if encryptor == nil {
		return nil, errors.New("cannot initialize SourceService: encryption key is unavailable")
	}
	return NewSourceService(db, sourcesDir, encryptor), nil
}

// SetEncryptor configures or overrides the encryptor.
func (s *SourceService) SetEncryptor(enc *crypto.Encryptor) {
	if enc != nil {
		s.encryptor = enc
	}
}

// DB returns the underlying pgxpool.Pool.
func (s *SourceService) DB() *pgxpool.Pool {
	return s.db
}

// SourcesDir returns the configured sources directory.
func (s *SourceService) SourcesDir() string {
	return s.sourcesDir
}

func (s *SourceService) getOrComputeAnalysis(sourceID uuid.UUID, dirPath string) *analyzer.AnalysisResult {
	if val, ok := s.analysisCache.Load(sourceID); ok {
		if res, ok := val.(*analyzer.AnalysisResult); ok && res != nil {
			return res
		}
	}

	analysisPath := filepath.Join(dirPath, ".forgelab-analysis.json")
	if data, err := os.ReadFile(analysisPath); err == nil {
		var res analyzer.AnalysisResult
		if err := json.Unmarshal(data, &res); err == nil {
			s.analysisCache.Store(sourceID, &res)
			return &res
		}
	}

	if res, err := analyzer.AnalyzeRepository(dirPath); err == nil && res != nil {
		s.saveAnalysis(sourceID, dirPath, res)
		return res
	}

	return nil
}

func (s *SourceService) saveAnalysis(sourceID uuid.UUID, dirPath string, res *analyzer.AnalysisResult) {
	if res == nil {
		return
	}
	s.analysisCache.Store(sourceID, res)
	if data, err := json.Marshal(res); err == nil {
		_ = os.WriteFile(filepath.Join(dirPath, ".forgelab-analysis.json"), data, 0644)
	}
}

// GetSourcePath returns the isolated filesystem path for a previously uploaded source,
// verifying that ownerID owns the source workspace and that it is ready.
func (s *SourceService) GetSourcePath(ctx context.Context, ownerID, sourceID uuid.UUID) (string, error) {
	if sourceID == uuid.Nil {
		return "", ErrSourceDirNotFound
	}

	if s.db != nil {
		var realOwner uuid.UUID
		var workspacePath string
		var status string
		err := s.db.QueryRow(ctx,
			`SELECT owner_id, workspace_path, status FROM source_workspaces WHERE id = $1`,
			sourceID,
		).Scan(&realOwner, &workspacePath, &status)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return "", ErrSourceDirNotFound
			}
			return "", fmt.Errorf("failed to query source workspace: %w", err)
		}
		if realOwner != ownerID {
			return "", ErrUnauthorizedSource
		}
		if status == SourceStatusFailed || status == SourceStatusCancelled {
			return "", ErrSourceDirNotFound
		}
		info, err := os.Stat(workspacePath)
		if err != nil || !info.IsDir() {
			return "", ErrSourceDirNotFound
		}
		return workspacePath, nil
	}

	// Fallback for tests running without database connection
	if val, ok := s.fallbackSources.Load(sourceID); ok {
		rec := *(val.(*SourceWorkspaceRecord))
		if rec.OwnerID != ownerID {
			return "", ErrUnauthorizedSource
		}
		if rec.Status == SourceStatusFailed || rec.Status == SourceStatusCancelled {
			return "", ErrSourceDirNotFound
		}
		info, err := os.Stat(rec.WorkspacePath)
		if err != nil || !info.IsDir() {
			return "", ErrSourceDirNotFound
		}
		return rec.WorkspacePath, nil
	}

	if val, ok := s.fallbackOwners.Load(sourceID); ok {
		if val.(uuid.UUID) != ownerID {
			return "", ErrUnauthorizedSource
		}
	}

	dir := filepath.Join(s.sourcesDir, sourceID.String())
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return "", ErrSourceDirNotFound
	}
	return dir, nil
}

// GetSourceStatus retrieves the status and detection metadata of an uploaded source workspace.
func (s *SourceService) GetSourceStatus(ctx context.Context, ownerID, sourceID uuid.UUID) (*SourceUploadResult, error) {
	if sourceID == uuid.Nil {
		return nil, ErrSourceDirNotFound
	}

	if s.db != nil {
		var realOwner uuid.UUID
		var res SourceUploadResult
		var detectionJSON []byte
		var runtime, framework, dbError *string

		err := s.db.QueryRow(ctx,
			`SELECT id, owner_id, status, phase, files_count, processed_files, total_bytes, processed_bytes, runtime, framework, detection_result, error
			 FROM source_workspaces WHERE id = $1`,
			sourceID,
		).Scan(
			&res.SourceID,
			&realOwner,
			&res.Status,
			&res.Phase,
			&res.FilesCount,
			&res.ProcessedFiles,
			&res.TotalBytes,
			&res.ProcessedBytes,
			&runtime,
			&framework,
			&detectionJSON,
			&dbError,
		)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, ErrSourceDirNotFound
			}
			return nil, fmt.Errorf("failed to query source status: %w", err)
		}
		if realOwner != ownerID {
			return nil, ErrUnauthorizedSource
		}

		if runtime != nil {
			res.Runtime = *runtime
		}
		if framework != nil {
			res.Framework = *framework
		}
		res.Error = dbError
		if len(detectionJSON) > 0 {
			var det detector.DetectionResult
			if err := json.Unmarshal(detectionJSON, &det); err == nil {
				res.Detection = &det
			}
		}
		if _, err := os.Stat(filepath.Join(s.sourcesDir, sourceID.String())); err == nil {
			dirPath := filepath.Join(s.sourcesDir, sourceID.String())
			if a := s.getOrComputeAnalysis(sourceID, dirPath); a != nil {
				res.Analysis = a
				res.Services = a.Services
				res.Source = map[string]interface{}{
					"id":               sourceID.String(),
					"source_type":      "local_upload",
					"source_reference": sourceID.String(),
					"folder_name":      a.RepositoryName,
					"total_files":      a.TotalFiles,
					"total_bytes":      a.TotalBytes,
				}
			}
		}

		return &res, nil
	}

	// Fallback for tests running without database connection
	if val, ok := s.fallbackSources.Load(sourceID); ok {
		rec := *(val.(*SourceWorkspaceRecord))
		if rec.OwnerID != ownerID {
			return nil, ErrUnauthorizedSource
		}
		var svcs []analyzer.ServiceDefinition
		var srcMap map[string]interface{}
		if rec.Analysis != nil {
			svcs = rec.Analysis.Services
			srcMap = map[string]interface{}{
				"id":               rec.ID.String(),
				"source_type":      "local_upload",
				"source_reference": rec.ID.String(),
				"folder_name":      rec.Analysis.RepositoryName,
				"total_files":      rec.Analysis.TotalFiles,
				"total_bytes":      rec.Analysis.TotalBytes,
			}
		}
		return &SourceUploadResult{
			SourceID:       rec.ID,
			Source:         srcMap,
			Services:       svcs,
			Status:         rec.Status,
			Phase:          rec.Phase,
			FilesCount:     rec.FilesCount,
			ProcessedFiles: rec.ProcessedFiles,
			TotalBytes:     rec.TotalBytes,
			ProcessedBytes: rec.ProcessedBytes,
			Runtime:        rec.Runtime,
			Framework:      rec.Framework,
			Detection:      rec.Detection,
			Analysis:       rec.Analysis,
			Error:          rec.Error,
		}, nil
	}

	if val, ok := s.fallbackOwners.Load(sourceID); ok {
		if val.(uuid.UUID) != ownerID {
			return nil, ErrUnauthorizedSource
		}
		dir := filepath.Join(s.sourcesDir, sourceID.String())
		if _, err := os.Stat(dir); err != nil {
			return nil, ErrSourceDirNotFound
		}
		return &SourceUploadResult{
			SourceID:       sourceID,
			Status:         SourceStatusReady,
			Phase:          SourcePhaseReady,
			FilesCount:     1,
			ProcessedFiles: 1,
			TotalBytes:     0,
			ProcessedBytes: 0,
		}, nil
	}

	return nil, ErrSourceDirNotFound
}

// DeleteSource cleans up an uploaded source workspace after verifying ownership.
func (s *SourceService) DeleteSource(ctx context.Context, ownerID, sourceID uuid.UUID) error {
	if sourceID == uuid.Nil {
		return ErrSourceDirNotFound
	}

	var dir string
	if s.db != nil {
		var realOwner uuid.UUID
		err := s.db.QueryRow(ctx,
			`SELECT owner_id, workspace_path FROM source_workspaces WHERE id = $1`,
			sourceID,
		).Scan(&realOwner, &dir)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrSourceDirNotFound
			}
			return fmt.Errorf("failed to query source workspace for deletion: %w", err)
		}
		if realOwner != ownerID {
			return ErrUnauthorizedSource
		}

		_, err = s.db.Exec(ctx, `DELETE FROM source_workspaces WHERE id = $1 AND owner_id = $2`, sourceID, ownerID)
		if err != nil {
			return fmt.Errorf("failed to delete source workspace record: %w", err)
		}
	} else {
		// Fallback for tests running without database connection: verify in-memory ownership
		if val, ok := s.fallbackSources.Load(sourceID); ok {
			rec := *(val.(*SourceWorkspaceRecord))
			if rec.OwnerID != ownerID {
				return ErrUnauthorizedSource
			}
			dir = rec.WorkspacePath
			s.fallbackSources.Delete(sourceID)
			s.fallbackOwners.Delete(sourceID)
		} else if val, ok := s.fallbackOwners.Load(sourceID); ok {
			if val.(uuid.UUID) != ownerID {
				return ErrUnauthorizedSource
			}
			s.fallbackOwners.Delete(sourceID)
			dir = filepath.Join(s.sourcesDir, sourceID.String())
		} else {
			dir = filepath.Join(s.sourcesDir, sourceID.String())
		}

		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			return ErrSourceDirNotFound
		}
	}

	// Clean up both finalized workspace and any temporary upload staging directory
	s.analysisCache.Delete(sourceID)
	uploadStaging := filepath.Join(s.sourcesDir, ".uploads", sourceID.String())
	_ = removeAllWithRetry(uploadStaging)
	return removeAllWithRetry(dir)
}

func removeAllWithRetry(path string) error {
	var err error
	for i := 0; i < 8; i++ {
		err = os.RemoveAll(path)
		if err == nil || os.IsNotExist(err) {
			return nil
		}
		time.Sleep(15 * time.Millisecond)
	}
	return err
}

func renameWithRetry(oldPath, newPath string) error {
	var err error
	for i := 0; i < 8; i++ {
		err = os.Rename(oldPath, newPath)
		if err == nil {
			return nil
		}
		time.Sleep(15 * time.Millisecond)
	}
	return err
}

// SaveSource inserts or updates a source record in the sources table.
func (s *SourceService) SaveSource(ctx context.Context, src *models.Source) error {
	if src.ID == uuid.Nil {
		src.ID = uuid.New()
	}
	now := time.Now()
	if src.CreatedAt.IsZero() {
		src.CreatedAt = now
	}
	src.UpdatedAt = now

	// Always store in memory for test compatibility & fast lookup
	s.sourceRecords.Store(src.ID, src)

	if s.db == nil {
		return nil
	}

	metaJSON, _ := json.Marshal(src.Metadata)
	if len(metaJSON) == 0 {
		metaJSON = []byte("{}")
	}

	_, err := s.db.Exec(ctx,
		`INSERT INTO sources (id, owner_id, source_type, source_reference, agent_id, fingerprint, metadata, encrypted_session_token, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 ON CONFLICT (id) DO UPDATE SET
		    source_type = EXCLUDED.source_type,
		    source_reference = EXCLUDED.source_reference,
		    agent_id = EXCLUDED.agent_id,
		    fingerprint = EXCLUDED.fingerprint,
		    metadata = EXCLUDED.metadata,
		    encrypted_session_token = COALESCE(EXCLUDED.encrypted_session_token, sources.encrypted_session_token),
		    updated_at = NOW()`,
		src.ID, src.OwnerID, src.SourceType, src.SourceReference, src.AgentID, src.Fingerprint, metaJSON, src.EncryptedSessionToken, src.CreatedAt, src.UpdatedAt,
	)
	if err != nil && (strings.Contains(err.Error(), "sources_owner_id_fkey") || strings.Contains(err.Error(), "23503")) {
		return fmt.Errorf("%w: %v", ErrUserNotFound, err)
	}
	return err
}

// GetSource retrieves a source record by ID and owner.
func (s *SourceService) GetSource(ctx context.Context, id, ownerID uuid.UUID) (*models.Source, error) {
	if s.db != nil {
		src := &models.Source{}
		var metaJSON []byte
		err := s.db.QueryRow(ctx,
			`SELECT id, owner_id, source_type, source_reference, agent_id, fingerprint, metadata, encrypted_session_token, created_at, updated_at
			 FROM sources WHERE id = $1 AND owner_id = $2`,
			id, ownerID,
		).Scan(&src.ID, &src.OwnerID, &src.SourceType, &src.SourceReference, &src.AgentID, &src.Fingerprint, &metaJSON, &src.EncryptedSessionToken, &src.CreatedAt, &src.UpdatedAt)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, ErrSourceDirNotFound
			}
			return nil, err
		}
		if len(metaJSON) > 0 {
			_ = json.Unmarshal(metaJSON, &src.Metadata)
		}
		s.sourceRecords.Store(src.ID, src)
		return src, nil
	}

	// Fallback for tests running without database connection
	if val, ok := s.sourceRecords.Load(id); ok {
		src := val.(*models.Source)
		if src.OwnerID != ownerID {
			return nil, ErrUnauthorizedSource
		}
		return src, nil
	}

	return nil, ErrSourceDirNotFound
}

// GetSourceByID retrieves a source record strictly by source ID without owner filtering.
func (s *SourceService) GetSourceByID(ctx context.Context, id uuid.UUID) (*models.Source, error) {
	if s.db != nil {
		src := &models.Source{}
		var metaJSON []byte
		err := s.db.QueryRow(ctx,
			`SELECT id, owner_id, source_type, source_reference, agent_id, fingerprint, metadata, encrypted_session_token, created_at, updated_at
			 FROM sources WHERE id = $1`,
			id,
		).Scan(&src.ID, &src.OwnerID, &src.SourceType, &src.SourceReference, &src.AgentID, &src.Fingerprint, &metaJSON, &src.EncryptedSessionToken, &src.CreatedAt, &src.UpdatedAt)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, ErrSourceDirNotFound
			}
			return nil, err
		}
		if len(metaJSON) > 0 {
			_ = json.Unmarshal(metaJSON, &src.Metadata)
		}
		s.sourceRecords.Store(src.ID, src)
		return src, nil
	}

	if val, ok := s.sourceRecords.Load(id); ok {
		return val.(*models.Source), nil
	}

	return nil, ErrSourceDirNotFound
}

// UpdateSourceMetadataField updates a specific metadata key in the sources record.
func (s *SourceService) UpdateSourceMetadataField(ctx context.Context, sourceID uuid.UUID, key string, value interface{}) error {
	src, err := s.GetSourceByID(ctx, sourceID)
	if err != nil {
		return err
	}
	if src.Metadata == nil {
		src.Metadata = make(map[string]interface{})
	}
	src.Metadata[key] = value
	return s.SaveSource(ctx, src)
}

// UserExists checks whether a user ID exists in the database.
func (s *SourceService) UserExists(ctx context.Context, userID uuid.UUID) bool {
	if s.db == nil {
		return true // In-memory/test fallback
	}
	var exists bool
	err := s.db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM users WHERE id = $1)", userID).Scan(&exists)
	return err == nil && exists
}
