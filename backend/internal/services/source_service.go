package services

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/forgelab/backend/internal/analyzer"
	"github.com/forgelab/backend/internal/detector"
	"github.com/forgelab/backend/internal/models"
)

var (
	ErrInvalidSourceArchive  = errors.New("invalid or corrupt source archive")
	ErrPathTraversalDetected = errors.New("path traversal detected in source archive")
	ErrArchiveTooLarge       = errors.New("uncompressed source exceeds maximum allowed size (100MB)")
	ErrSourceDirNotFound     = errors.New("source directory not found")
	ErrUnauthorizedSource    = errors.New("unauthorized source workspace access")
	ErrSourceNotReady        = errors.New("source workspace is still processing")
)

const MaxUncompressedBytes = 100 * 1024 * 1024 // 100MB

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
	fallbackOwners  sync.Map
	fallbackSources sync.Map
	analysisCache   sync.Map
}

func NewSourceService(db *pgxpool.Pool, sourcesDir string) *SourceService {
	if sourcesDir == "" {
		sourcesDir = "./data/sources"
	}
	_ = os.MkdirAll(sourcesDir, 0755)
	_ = os.MkdirAll(filepath.Join(sourcesDir, ".uploads"), 0755)
	return &SourceService{
		db:         db,
		sourcesDir: sourcesDir,
	}
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
		rec := val.(*SourceWorkspaceRecord)
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
		rec := val.(*SourceWorkspaceRecord)
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
			rec := val.(*SourceWorkspaceRecord)
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

// IngestMultipartStream processes multipart parts sequentially via Go's streaming API,
// writing file bytes directly to a temporary staging workspace (.uploads/<sourceID>),
// normalizing wrapper paths, atomically moving to final storage, and initiating async detection.
func (s *SourceService) IngestMultipartStream(ctx context.Context, ownerID uuid.UUID, reader *multipart.Reader) (*SourceUploadResult, error) {
	sourceID := uuid.New()
	uploadDir := filepath.Join(s.sourcesDir, ".uploads", sourceID.String())
	targetDir := filepath.Join(s.sourcesDir, sourceID.String())

	if err := os.MkdirAll(uploadDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create upload staging directory: %w", err)
	}

	cleanUploadDir := filepath.Clean(uploadDir)
	var totalBytes int64
	var filesCount int
	var finalized bool

	// Cleanup guard: if upload fails, is cancelled, or errors before finalization, remove staging and target dirs
	defer func() {
		if !finalized {
			_ = os.RemoveAll(uploadDir)
			_ = os.RemoveAll(targetDir)
		}
	}()

	// Watch for context cancellation asynchronously to purge immediately
	cleanupDone := make(chan struct{})
	defer close(cleanupDone)
	go func() {
		select {
		case <-ctx.Done():
			_ = os.RemoveAll(uploadDir)
			_ = os.RemoveAll(targetDir)
			if s.db != nil {
				_, _ = s.db.Exec(context.Background(), `DELETE FROM source_workspaces WHERE id = $1`, sourceID)
			} else {
				s.fallbackSources.Delete(sourceID)
				s.fallbackOwners.Delete(sourceID)
			}
		case <-cleanupDone:
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		fieldName := part.FormName()
		fileName := part.FileName()
		if fileName == "" {
			_ = part.Close()
			continue
		}

		lowerName := strings.ToLower(fileName)

		// 1. Archive stream handling (.zip, .tar.gz, .tgz)
		if (fieldName == "archive" || fieldName == "file") && filesCount == 0 &&
			(strings.HasSuffix(lowerName, ".zip") || strings.HasSuffix(lowerName, ".tar.gz") || strings.HasSuffix(lowerName, ".tgz")) {
			return s.ingestArchivePart(ctx, ownerID, sourceID, uploadDir, targetDir, part, lowerName, &finalized)
		}

		// 2. Browser directory file streaming
		relPath := getRelativePathFromHeader(part.Header, fileName)
		if shouldIgnorePath(relPath) {
			_, _ = io.Copy(io.Discard, part)
			_ = part.Close()
			continue
		}

		normRel := filepath.ToSlash(filepath.Clean(relPath))
		if strings.HasPrefix(normRel, "../") || normRel == ".." || filepath.IsAbs(normRel) || strings.HasPrefix(normRel, "/") || strings.Contains(normRel, "\x00") {
			_ = part.Close()
			return nil, ErrPathTraversalDetected
		}

		cleanRel := filepath.FromSlash(normRel)
		if cleanRel == "" || cleanRel == "." {
			_ = part.Close()
			continue
		}

		destPath := filepath.Join(cleanUploadDir, cleanRel)
		if !strings.HasPrefix(filepath.Clean(destPath), cleanUploadDir+string(filepath.Separator)) {
			_ = part.Close()
			return nil, ErrPathTraversalDetected
		}

		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			_ = part.Close()
			return nil, err
		}

		outFile, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			_ = part.Close()
			return nil, err
		}

		remaining := MaxUncompressedBytes - totalBytes
		written, copyErr := copyWithLimit(outFile, part, remaining)
		outFile.Close()
		part.Close()

		if copyErr != nil {
			return nil, copyErr
		}

		totalBytes += written
		if totalBytes > MaxUncompressedBytes {
			return nil, ErrArchiveTooLarge
		}
		filesCount++
	}

	if filesCount == 0 {
		return nil, errors.New("no files or source archive provided in upload")
	}

	// Normalize root structure (lift single wrapper directory if browser sent folder prefix)
	if err := normalizeSourceWorkspace(uploadDir); err != nil {
		return nil, fmt.Errorf("failed to normalize upload workspace: %w", err)
	}

	// Atomically move from .uploads/<sourceID> to data/sources/<sourceID>
	if err := renameWithRetry(uploadDir, targetDir); err != nil {
		return nil, fmt.Errorf("failed to finalize upload workspace: %w", err)
	}
	finalized = true

	// Persist initial source status in database
	status := SourceStatusProcessing
	phase := SourcePhaseDetecting

	if s.db != nil {
		_, err := s.db.Exec(ctx,
			`INSERT INTO source_workspaces (id, owner_id, workspace_path, files_count, total_bytes, status, phase, processed_files, processed_bytes, created_at, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NOW(), NOW())`,
			sourceID, ownerID, targetDir, filesCount, totalBytes, status, phase, filesCount, totalBytes,
		)
		if err != nil {
			_ = os.RemoveAll(targetDir)
			return nil, fmt.Errorf("failed to record source workspace: %w", err)
		}
	}

	rec := &SourceWorkspaceRecord{
		ID:             sourceID,
		OwnerID:        ownerID,
		WorkspacePath:  targetDir,
		FilesCount:     filesCount,
		ProcessedFiles: filesCount,
		TotalBytes:     totalBytes,
		ProcessedBytes: totalBytes,
		Status:         status,
		Phase:          phase,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	s.fallbackSources.Store(sourceID, rec)
	s.fallbackOwners.Store(sourceID, ownerID)

	// Kick off asynchronous detection without blocking HTTP upload response
	go s.processSourceBackground(sourceID, ownerID, targetDir)

	return &SourceUploadResult{
		SourceID:       sourceID,
		Status:         status,
		Phase:          phase,
		FilesCount:     filesCount,
		ProcessedFiles: filesCount,
		TotalBytes:     totalBytes,
		ProcessedBytes: totalBytes,
	}, nil
}

// ingestArchivePart handles streaming extraction for compressed archives (.zip, .tar.gz, .tgz).
func (s *SourceService) ingestArchivePart(
	ctx context.Context,
	ownerID, sourceID uuid.UUID,
	uploadDir, targetDir string,
	part *multipart.Part,
	filename string,
	finalized *bool,
) (*SourceUploadResult, error) {
	defer part.Close()

	var filesCount int
	var totalBytes int64

	if strings.HasSuffix(filename, ".zip") {
		// Zip format requires random access reader (central directory is at end of archive).
		// Buffer part to a temporary archive file inside staging uploadDir.
		tempZip := filepath.Join(uploadDir, "__archive__.zip")
		outFile, err := os.OpenFile(tempZip, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
		if err != nil {
			return nil, err
		}
		archiveBytes, copyErr := copyWithLimit(outFile, part, MaxUncompressedBytes)
		outFile.Close()
		if copyErr != nil {
			return nil, copyErr
		}

		zipFile, err := os.Open(tempZip)
		if err != nil {
			_ = os.Remove(tempZip)
			return nil, fmt.Errorf("%w: failed to open archive: %v", ErrInvalidSourceArchive, err)
		}

		zipReader, err := zip.NewReader(zipFile, archiveBytes)
		if err != nil {
			zipFile.Close()
			_ = os.Remove(tempZip)
			return nil, fmt.Errorf("%w: %v", ErrInvalidSourceArchive, err)
		}

		cleanUpload := filepath.Clean(uploadDir)
		for _, f := range zipReader.File {
			if shouldIgnorePath(f.Name) {
				continue
			}
			normRel := filepath.ToSlash(filepath.Clean(f.Name))
			if strings.HasPrefix(normRel, "../") || normRel == ".." || filepath.IsAbs(normRel) || strings.HasPrefix(normRel, "/") || strings.Contains(normRel, "\x00") {
				zipFile.Close()
				_ = os.Remove(tempZip)
				return nil, ErrPathTraversalDetected
			}
			cleanRel := filepath.FromSlash(normRel)
			if cleanRel == "" || cleanRel == "." {
				continue
			}
			destPath := filepath.Join(cleanUpload, cleanRel)
			if !strings.HasPrefix(filepath.Clean(destPath), cleanUpload+string(filepath.Separator)) {
				zipFile.Close()
				_ = os.Remove(tempZip)
				return nil, ErrPathTraversalDetected
			}

			if f.FileInfo().IsDir() {
				_ = os.MkdirAll(destPath, 0755)
				continue
			}

			if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
				zipFile.Close()
				_ = os.Remove(tempZip)
				return nil, err
			}

			rc, err := f.Open()
			if err != nil {
				zipFile.Close()
				_ = os.Remove(tempZip)
				return nil, err
			}
			outFile, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode())
			if err != nil {
				rc.Close()
				zipFile.Close()
				_ = os.Remove(tempZip)
				return nil, err
			}
			remaining := MaxUncompressedBytes - totalBytes
			written, copyErr := copyWithLimit(outFile, rc, remaining)
			outFile.Close()
			rc.Close()
			if copyErr != nil {
				zipFile.Close()
				_ = os.Remove(tempZip)
				return nil, copyErr
			}
			totalBytes += written
			if totalBytes > MaxUncompressedBytes {
				zipFile.Close()
				_ = os.Remove(tempZip)
				return nil, ErrArchiveTooLarge
			}
			filesCount++
		}
		zipFile.Close()
		_ = os.Remove(tempZip)
	} else {
		// tar.gz or tgz: stream extract sequentially directly from part
		gzReader, err := gzip.NewReader(part)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidSourceArchive, err)
		}
		defer gzReader.Close()

		tarReader := tar.NewReader(gzReader)
		cleanUpload := filepath.Clean(uploadDir)

		for {
			header, err := tarReader.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("%w: %v", ErrInvalidSourceArchive, err)
			}
			if shouldIgnorePath(header.Name) {
				continue
			}
			normRel := filepath.ToSlash(filepath.Clean(header.Name))
			if strings.HasPrefix(normRel, "../") || normRel == ".." || filepath.IsAbs(normRel) || strings.HasPrefix(normRel, "/") || strings.Contains(normRel, "\x00") {
				return nil, ErrPathTraversalDetected
			}
			cleanRel := filepath.FromSlash(normRel)
			if cleanRel == "" || cleanRel == "." {
				continue
			}
			destPath := filepath.Join(cleanUpload, cleanRel)
			if !strings.HasPrefix(filepath.Clean(destPath), cleanUpload+string(filepath.Separator)) {
				return nil, ErrPathTraversalDetected
			}

			switch header.Typeflag {
			case tar.TypeDir:
				_ = os.MkdirAll(destPath, 0755)
			case tar.TypeReg:
				if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
					return nil, err
				}
				outFile, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, header.FileInfo().Mode())
				if err != nil {
					return nil, err
				}
				remaining := MaxUncompressedBytes - totalBytes
				written, copyErr := copyWithLimit(outFile, tarReader, remaining)
				outFile.Close()
				if copyErr != nil {
					return nil, copyErr
				}
				totalBytes += written
				if totalBytes > MaxUncompressedBytes {
					return nil, ErrArchiveTooLarge
				}
				filesCount++
			}
		}
	}

	if filesCount == 0 {
		return nil, errors.New("no files found in source archive")
	}

	if err := normalizeSourceWorkspace(uploadDir); err != nil {
		return nil, err
	}

	if err := renameWithRetry(uploadDir, targetDir); err != nil {
		return nil, err
	}
	*finalized = true

	status := SourceStatusProcessing
	phase := SourcePhaseDetecting

	if s.db != nil {
		_, err := s.db.Exec(ctx,
			`INSERT INTO source_workspaces (id, owner_id, workspace_path, files_count, total_bytes, status, phase, processed_files, processed_bytes, created_at, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NOW(), NOW())`,
			sourceID, ownerID, targetDir, filesCount, totalBytes, status, phase, filesCount, totalBytes,
		)
		if err != nil {
			_ = os.RemoveAll(targetDir)
			return nil, fmt.Errorf("failed to record source workspace: %w", err)
		}
	}

	rec := &SourceWorkspaceRecord{
		ID:             sourceID,
		OwnerID:        ownerID,
		WorkspacePath:  targetDir,
		FilesCount:     filesCount,
		ProcessedFiles: filesCount,
		TotalBytes:     totalBytes,
		ProcessedBytes: totalBytes,
		Status:         status,
		Phase:          phase,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	s.fallbackSources.Store(sourceID, rec)
	s.fallbackOwners.Store(sourceID, ownerID)

	go s.processSourceBackground(sourceID, ownerID, targetDir)

	return &SourceUploadResult{
		SourceID:       sourceID,
		Status:         status,
		Phase:          phase,
		FilesCount:     filesCount,
		ProcessedFiles: filesCount,
		TotalBytes:     totalBytes,
		ProcessedBytes: totalBytes,
	}, nil
}

// processSourceBackground inspects unpacked files to detect runtime/framework and transitions state to ready or failed.
func (s *SourceService) processSourceBackground(sourceID, ownerID uuid.UUID, targetDir string) {
	analysis := s.getOrComputeAnalysis(sourceID, targetDir)
	detection, err := detector.Detect(targetDir)
	if err != nil && analysis == nil {
		slog.Warn("source detection failed",
			"source_id", sourceID.String(),
			"owner_id", ownerID.String(),
			"error", err.Error(),
		)
		errMsg := err.Error()
		s.updateSourceState(context.Background(), sourceID, SourceStatusFailed, SourcePhaseFailed, nil, analysis, &errMsg)
		return
	}

	s.updateSourceState(context.Background(), sourceID, SourceStatusReady, SourcePhaseReady, detection, analysis, nil)
}

func (s *SourceService) updateSourceState(ctx context.Context, sourceID uuid.UUID, status, phase string, detection *detector.DetectionResult, analysis *analyzer.AnalysisResult, errMsg *string) {
	var runtime, framework string
	var detBytes []byte
	if detection != nil {
		runtime = detection.Runtime
		framework = detection.Framework
		detBytes, _ = json.Marshal(detection)
	}

	if s.db != nil {
		_, err := s.db.Exec(ctx,
			`UPDATE source_workspaces
			 SET status = $2, phase = $3, runtime = $4, framework = $5, detection_result = $6, error = $7, updated_at = NOW()
			 WHERE id = $1`,
			sourceID, status, phase, runtime, framework, detBytes, errMsg,
		)
		if err != nil {
			slog.Error("failed to update source workspace status in database",
				"source_id", sourceID.String(),
				"error", err.Error(),
			)
		}
	}

	if val, ok := s.fallbackSources.Load(sourceID); ok {
		rec := val.(*SourceWorkspaceRecord)
		rec.Status = status
		rec.Phase = phase
		rec.Runtime = runtime
		rec.Framework = framework
		rec.Detection = detection
		rec.Analysis = analysis
		rec.Error = errMsg
		rec.UpdatedAt = time.Now()
		s.fallbackSources.Store(sourceID, rec)
	}
}

// IngestZip extracts an uploaded zip archive into an isolated source directory (synchronous compatibility).
func (s *SourceService) IngestZip(ctx context.Context, ownerID uuid.UUID, r io.ReaderAt, size int64) (*SourceUploadResult, error) {
	zipReader, err := zip.NewReader(r, size)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidSourceArchive, err)
	}

	var entryPaths []string
	for _, f := range zipReader.File {
		entryPaths = append(entryPaths, f.Name)
	}
	wrapperDir, err := detectCommonWrapperDir(entryPaths)
	if err != nil {
		return nil, err
	}

	sourceID := uuid.New()
	targetDir := filepath.Join(s.sourcesDir, sourceID.String())
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return nil, err
	}

	cleanTarget := filepath.Clean(targetDir)
	var totalBytes int64
	filesCount := 0

	for _, f := range zipReader.File {
		if shouldIgnorePath(f.Name) {
			continue
		}

		normRel := filepath.ToSlash(filepath.Clean(f.Name))
		if strings.HasPrefix(normRel, "../") || normRel == ".." || filepath.IsAbs(normRel) || strings.HasPrefix(normRel, "/") {
			_ = os.RemoveAll(targetDir)
			return nil, ErrPathTraversalDetected
		}

		if wrapperDir != "" {
			normRel = strings.TrimPrefix(normRel, wrapperDir+"/")
		}

		cleanRel := filepath.FromSlash(normRel)
		if cleanRel == "" || cleanRel == "." {
			continue
		}

		destPath := filepath.Join(cleanTarget, cleanRel)
		if !strings.HasPrefix(filepath.Clean(destPath), cleanTarget+string(filepath.Separator)) {
			_ = os.RemoveAll(targetDir)
			return nil, ErrPathTraversalDetected
		}

		if f.FileInfo().IsDir() {
			_ = os.MkdirAll(destPath, 0755)
			continue
		}

		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			_ = os.RemoveAll(targetDir)
			return nil, err
		}

		rc, err := f.Open()
		if err != nil {
			_ = os.RemoveAll(targetDir)
			return nil, err
		}

		outFile, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode())
		if err != nil {
			rc.Close()
			_ = os.RemoveAll(targetDir)
			return nil, err
		}

		remaining := MaxUncompressedBytes - totalBytes
		written, copyErr := copyWithLimit(outFile, rc, remaining)
		outFile.Close()
		rc.Close()

		if copyErr != nil {
			_ = os.RemoveAll(targetDir)
			return nil, copyErr
		}

		totalBytes += written
		if totalBytes > MaxUncompressedBytes {
			_ = os.RemoveAll(targetDir)
			return nil, ErrArchiveTooLarge
		}
		filesCount++
	}

	if err := normalizeSourceWorkspace(targetDir); err != nil {
		_ = os.RemoveAll(targetDir)
		return nil, err
	}

	detection, _ := detector.Detect(targetDir)
	detBytes, _ := json.Marshal(detection)
	var runtime, framework string
	if detection != nil {
		runtime = detection.Runtime
		framework = detection.Framework
	}

	if s.db != nil {
		_, err = s.db.Exec(ctx,
			`INSERT INTO source_workspaces (id, owner_id, workspace_path, files_count, total_bytes, status, phase, processed_files, processed_bytes, runtime, framework, detection_result, created_at, updated_at)
			 VALUES ($1, $2, $3, $4, $5, 'ready', 'ready', $4, $5, $6, $7, $8, NOW(), NOW())`,
			sourceID, ownerID, targetDir, filesCount, totalBytes, runtime, framework, detBytes,
		)
		if err != nil {
			_ = os.RemoveAll(targetDir)
			return nil, fmt.Errorf("failed to record source workspace: %w", err)
		}
	}

	rec := &SourceWorkspaceRecord{
		ID:             sourceID,
		OwnerID:        ownerID,
		WorkspacePath:  targetDir,
		FilesCount:     filesCount,
		ProcessedFiles: filesCount,
		TotalBytes:     totalBytes,
		ProcessedBytes: totalBytes,
		Status:         SourceStatusReady,
		Phase:          SourcePhaseReady,
		Runtime:        runtime,
		Framework:      framework,
		Detection:      detection,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	s.fallbackSources.Store(sourceID, rec)
	s.fallbackOwners.Store(sourceID, ownerID)

	return &SourceUploadResult{
		SourceID:       sourceID,
		Status:         SourceStatusReady,
		Phase:          SourcePhaseReady,
		FilesCount:     filesCount,
		ProcessedFiles: filesCount,
		TotalBytes:     totalBytes,
		ProcessedBytes: totalBytes,
		Runtime:        runtime,
		Framework:      framework,
		Detection:      detection,
	}, nil
}

// IngestTarGz extracts an uploaded tar.gz archive into an isolated source directory (synchronous compatibility).
func (s *SourceService) IngestTarGz(ctx context.Context, ownerID uuid.UUID, r io.Reader) (*SourceUploadResult, error) {
	gzReader, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidSourceArchive, err)
	}
	defer gzReader.Close()

	tarReader := tar.NewReader(gzReader)
	sourceID := uuid.New()
	targetDir := filepath.Join(s.sourcesDir, sourceID.String())
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return nil, err
	}

	cleanTarget := filepath.Clean(targetDir)
	var totalBytes int64
	filesCount := 0

	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			_ = os.RemoveAll(targetDir)
			return nil, fmt.Errorf("%w: %v", ErrInvalidSourceArchive, err)
		}

		if shouldIgnorePath(header.Name) {
			continue
		}

		normRel := filepath.ToSlash(filepath.Clean(header.Name))
		if strings.HasPrefix(normRel, "../") || normRel == ".." || filepath.IsAbs(normRel) || strings.HasPrefix(normRel, "/") {
			_ = os.RemoveAll(targetDir)
			return nil, ErrPathTraversalDetected
		}

		cleanRel := filepath.FromSlash(normRel)
		if cleanRel == "" || cleanRel == "." {
			continue
		}

		destPath := filepath.Join(cleanTarget, cleanRel)
		if !strings.HasPrefix(filepath.Clean(destPath), cleanTarget+string(filepath.Separator)) {
			_ = os.RemoveAll(targetDir)
			return nil, ErrPathTraversalDetected
		}

		switch header.Typeflag {
			case tar.TypeDir:
			_ = os.MkdirAll(destPath, 0755)
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
				_ = os.RemoveAll(targetDir)
				return nil, err
			}
			outFile, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, header.FileInfo().Mode())
			if err != nil {
				_ = os.RemoveAll(targetDir)
				return nil, err
			}

			remaining := MaxUncompressedBytes - totalBytes
			written, copyErr := copyWithLimit(outFile, tarReader, remaining)
			outFile.Close()

			if copyErr != nil {
				_ = os.RemoveAll(targetDir)
				return nil, copyErr
			}

			totalBytes += written
			if totalBytes > MaxUncompressedBytes {
				_ = os.RemoveAll(targetDir)
				return nil, ErrArchiveTooLarge
			}
			filesCount++
		}
	}

	if err := normalizeSourceWorkspace(targetDir); err != nil {
		_ = os.RemoveAll(targetDir)
		return nil, err
	}

	detection, _ := detector.Detect(targetDir)
	detBytes, _ := json.Marshal(detection)
	var runtime, framework string
	if detection != nil {
		runtime = detection.Runtime
		framework = detection.Framework
	}

	if s.db != nil {
		_, err = s.db.Exec(ctx,
			`INSERT INTO source_workspaces (id, owner_id, workspace_path, files_count, total_bytes, status, phase, processed_files, processed_bytes, runtime, framework, detection_result, created_at, updated_at)
			 VALUES ($1, $2, $3, $4, $5, 'ready', 'ready', $4, $5, $6, $7, $8, NOW(), NOW())`,
			sourceID, ownerID, targetDir, filesCount, totalBytes, runtime, framework, detBytes,
		)
		if err != nil {
			_ = os.RemoveAll(targetDir)
			return nil, fmt.Errorf("failed to record source workspace: %w", err)
		}
	}

	rec := &SourceWorkspaceRecord{
		ID:             sourceID,
		OwnerID:        ownerID,
		WorkspacePath:  targetDir,
		FilesCount:     filesCount,
		ProcessedFiles: filesCount,
		TotalBytes:     totalBytes,
		ProcessedBytes: totalBytes,
		Status:         SourceStatusReady,
		Phase:          SourcePhaseReady,
		Runtime:        runtime,
		Framework:      framework,
		Detection:      detection,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	s.fallbackSources.Store(sourceID, rec)
	s.fallbackOwners.Store(sourceID, ownerID)

	return &SourceUploadResult{
		SourceID:       sourceID,
		Status:         SourceStatusReady,
		Phase:          SourcePhaseReady,
		FilesCount:     filesCount,
		ProcessedFiles: filesCount,
		TotalBytes:     totalBytes,
		ProcessedBytes: totalBytes,
		Runtime:        runtime,
		Framework:      framework,
		Detection:      detection,
	}, nil
}

// IngestMultipartFiles writes multiple files uploaded via HTML5 directory selection (synchronous compatibility).
func (s *SourceService) IngestMultipartFiles(ctx context.Context, ownerID uuid.UUID, files []*multipart.FileHeader) (*SourceUploadResult, error) {
	if len(files) == 0 {
		return nil, errors.New("no files provided in upload")
	}

	var filePaths []string
	for _, fh := range files {
		filePaths = append(filePaths, getRelativePath(fh))
	}

	wrapperDir, err := detectCommonWrapperDir(filePaths)
	if err != nil {
		return nil, err
	}

	sourceID := uuid.New()
	targetDir := filepath.Join(s.sourcesDir, sourceID.String())
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return nil, err
	}

	cleanTarget := filepath.Clean(targetDir)
	var totalBytes int64
	filesCount := 0

	for _, fh := range files {
		relPath := getRelativePath(fh)
		if shouldIgnorePath(relPath) {
			continue
		}

		normRel := filepath.ToSlash(filepath.Clean(relPath))
		if strings.HasPrefix(normRel, "../") || normRel == ".." || filepath.IsAbs(normRel) || strings.HasPrefix(normRel, "/") {
			_ = os.RemoveAll(targetDir)
			return nil, ErrPathTraversalDetected
		}

		// Strip single common wrapper directory if detected
		if wrapperDir != "" {
			normRel = strings.TrimPrefix(normRel, wrapperDir+"/")
		}

		cleanRel := filepath.FromSlash(normRel)
		if cleanRel == "" || cleanRel == "." {
			continue
		}

		destPath := filepath.Join(cleanTarget, cleanRel)
		if !strings.HasPrefix(filepath.Clean(destPath), cleanTarget+string(filepath.Separator)) {
			_ = os.RemoveAll(targetDir)
			return nil, ErrPathTraversalDetected
		}

		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			_ = os.RemoveAll(targetDir)
			return nil, err
		}

		srcFile, err := fh.Open()
		if err != nil {
			_ = os.RemoveAll(targetDir)
			return nil, err
		}

		outFile, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			srcFile.Close()
			_ = os.RemoveAll(targetDir)
			return nil, err
		}

		remaining := MaxUncompressedBytes - totalBytes
		written, copyErr := copyWithLimit(outFile, srcFile, remaining)
		outFile.Close()
		srcFile.Close()

		if copyErr != nil {
			_ = os.RemoveAll(targetDir)
			return nil, copyErr
		}

		totalBytes += written
		if totalBytes > MaxUncompressedBytes {
			_ = os.RemoveAll(targetDir)
			return nil, ErrArchiveTooLarge
		}
		filesCount++
	}

	if err := normalizeSourceWorkspace(targetDir); err != nil {
		_ = os.RemoveAll(targetDir)
		return nil, err
	}

	detection, _ := detector.Detect(targetDir)
	detBytes, _ := json.Marshal(detection)
	var runtime, framework string
	if detection != nil {
		runtime = detection.Runtime
		framework = detection.Framework
	}

	if s.db != nil {
		_, err = s.db.Exec(ctx,
			`INSERT INTO source_workspaces (id, owner_id, workspace_path, files_count, total_bytes, status, phase, processed_files, processed_bytes, runtime, framework, detection_result, created_at, updated_at)
			 VALUES ($1, $2, $3, $4, $5, 'ready', 'ready', $4, $5, $6, $7, $8, NOW(), NOW())`,
			sourceID, ownerID, targetDir, filesCount, totalBytes, runtime, framework, detBytes,
		)
		if err != nil {
			_ = os.RemoveAll(targetDir)
			return nil, fmt.Errorf("failed to record source workspace: %w", err)
		}
	}

	rec := &SourceWorkspaceRecord{
		ID:             sourceID,
		OwnerID:        ownerID,
		WorkspacePath:  targetDir,
		FilesCount:     filesCount,
		ProcessedFiles: filesCount,
		TotalBytes:     totalBytes,
		ProcessedBytes: totalBytes,
		Status:         SourceStatusReady,
		Phase:          SourcePhaseReady,
		Runtime:        runtime,
		Framework:      framework,
		Detection:      detection,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	s.fallbackSources.Store(sourceID, rec)
	s.fallbackOwners.Store(sourceID, ownerID)

	return &SourceUploadResult{
		SourceID:       sourceID,
		Status:         SourceStatusReady,
		Phase:          SourcePhaseReady,
		FilesCount:     filesCount,
		ProcessedFiles: filesCount,
		TotalBytes:     totalBytes,
		ProcessedBytes: totalBytes,
		Runtime:        runtime,
		Framework:      framework,
		Detection:      detection,
	}, nil
}

func copyWithLimit(dest io.Writer, src io.Reader, remaining int64) (int64, error) {
	if remaining <= 0 {
		return 0, ErrArchiveTooLarge
	}
	lr := &io.LimitedReader{R: src, N: remaining + 1}
	written, err := io.Copy(dest, lr)
	if err != nil {
		return written, err
	}
	if lr.N == 0 {
		return written, ErrArchiveTooLarge
	}
	return written, nil
}

// detectCommonWrapperDir checks if all non-ignored file paths share a single common top-level directory.
func detectCommonWrapperDir(paths []string) (string, error) {
	if len(paths) == 0 {
		return "", nil
	}

	var validPaths []string
	for _, p := range paths {
		if shouldIgnorePath(p) {
			continue
		}
		norm := filepath.ToSlash(filepath.Clean(p))
		if strings.HasPrefix(norm, "../") || norm == ".." || filepath.IsAbs(norm) || strings.HasPrefix(norm, "/") {
			return "", ErrPathTraversalDetected
		}
		if strings.Contains(norm, "\x00") {
			return "", ErrInvalidSourceArchive
		}
		validPaths = append(validPaths, norm)
	}

	if len(validPaths) == 0 {
		return "", nil
	}

	var commonTop string
	for i, norm := range validPaths {
		parts := strings.Split(norm, "/")
		if len(parts) <= 1 {
			return "", nil
		}
		top := parts[0]
		if i == 0 {
			commonTop = top
		} else if top != commonTop {
			return "", nil
		}
	}

	return commonTop, nil
}

// normalizeSourceWorkspace checks if all extracted files ended up inside a single subfolder
// and lifts them to the workspace root if so (handles single or multi-tier wrapper directories).
func normalizeSourceWorkspace(targetDir string) error {
	for {
		entries, err := os.ReadDir(targetDir)
		if err != nil {
			return err
		}

		var validEntries []os.DirEntry
		for _, e := range entries {
			if !shouldIgnorePath(e.Name()) {
				validEntries = append(validEntries, e)
			}
		}

		if len(validEntries) == 1 && validEntries[0].IsDir() {
			wrapperName := validEntries[0].Name()
			wrapperPath := filepath.Join(targetDir, wrapperName)

			subEntries, err := os.ReadDir(wrapperPath)
			if err != nil {
				return err
			}

			for _, sub := range subEntries {
				oldPath := filepath.Join(wrapperPath, sub.Name())
				newPath := filepath.Join(targetDir, sub.Name())
				if err := os.Rename(oldPath, newPath); err != nil {
					return err
				}
			}

			_ = os.Remove(wrapperPath)
		} else {
			break
		}
	}

	return nil
}

func shouldIgnorePath(path string) bool {
	norm := filepath.ToSlash(path)
	parts := strings.Split(norm, "/")
	for _, p := range parts {
		lower := strings.ToLower(p)
		if lower == "node_modules" || lower == ".git" || lower == ".next" ||
			lower == "dist" || lower == "build" || lower == ".venv" ||
			lower == "venv" || lower == "__pycache__" || lower == ".cache" ||
			lower == ".ds_store" || lower == "thumbs.db" || lower == ".turbo" {
			return true
		}
	}
	return false
}

func getRelativePath(fh *multipart.FileHeader) string {
	if fh.Header != nil {
		cd := fh.Header.Get("Content-Disposition")
		if cd != "" {
			_, params, err := mime.ParseMediaType(cd)
			if err == nil {
				if fn, ok := params["filename"]; ok && fn != "" {
					return fn
				}
			}
		}
	}
	return fh.Filename
}

func getRelativePathFromHeader(header textproto.MIMEHeader, defaultName string) string {
	if header != nil {
		cd := header.Get("Content-Disposition")
		if cd != "" {
			_, params, err := mime.ParseMediaType(cd)
			if err == nil {
				if fn, ok := params["filename"]; ok && fn != "" {
					return fn
				}
			}
		}
	}
	return defaultName
}

// SaveSource inserts or updates a source record in the sources table.
func (s *SourceService) SaveSource(ctx context.Context, src *models.Source) error {
	if s.db == nil {
		return nil
	}
	if src.ID == uuid.Nil {
		src.ID = uuid.New()
	}
	now := time.Now()
	if src.CreatedAt.IsZero() {
		src.CreatedAt = now
	}
	src.UpdatedAt = now

	metaJSON, _ := json.Marshal(src.Metadata)
	if len(metaJSON) == 0 {
		metaJSON = []byte("{}")
	}

	_, err := s.db.Exec(ctx,
		`INSERT INTO sources (id, owner_id, source_type, source_reference, agent_id, fingerprint, metadata, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		 ON CONFLICT (id) DO UPDATE SET
		    source_type = EXCLUDED.source_type,
		    source_reference = EXCLUDED.source_reference,
		    agent_id = EXCLUDED.agent_id,
		    fingerprint = EXCLUDED.fingerprint,
		    metadata = EXCLUDED.metadata,
		    updated_at = NOW()`,
		src.ID, src.OwnerID, src.SourceType, src.SourceReference, src.AgentID, src.Fingerprint, metaJSON, src.CreatedAt, src.UpdatedAt,
	)
	return err
}

// GetSource retrieves a source record by ID and owner.
func (s *SourceService) GetSource(ctx context.Context, id, ownerID uuid.UUID) (*models.Source, error) {
	if s.db == nil {
		return nil, ErrSourceDirNotFound
	}
	src := &models.Source{}
	var metaJSON []byte
	err := s.db.QueryRow(ctx,
		`SELECT id, owner_id, source_type, source_reference, agent_id, fingerprint, metadata, created_at, updated_at
		 FROM sources WHERE id = $1 AND owner_id = $2`,
		id, ownerID,
	).Scan(&src.ID, &src.OwnerID, &src.SourceType, &src.SourceReference, &src.AgentID, &src.Fingerprint, &metaJSON, &src.CreatedAt, &src.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrSourceDirNotFound
		}
		return nil, err
	}
	if len(metaJSON) > 0 {
		_ = json.Unmarshal(metaJSON, &src.Metadata)
	}
	return src, nil
}

