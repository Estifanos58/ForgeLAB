package services

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/forgelab/backend/internal/detector"
)

var (
	ErrInvalidSourceArchive  = errors.New("invalid or corrupt source archive")
	ErrPathTraversalDetected = errors.New("path traversal detected in source archive")
	ErrArchiveTooLarge       = errors.New("uncompressed source exceeds maximum allowed size (100MB)")
	ErrSourceDirNotFound     = errors.New("source directory not found")
	ErrUnauthorizedSource    = errors.New("unauthorized source workspace access")
)

const MaxUncompressedBytes = 100 * 1024 * 1024 // 100MB

type SourceUploadResult struct {
	SourceID   uuid.UUID                `json:"source_id"`
	FilesCount int                      `json:"files_count"`
	TotalBytes int64                    `json:"total_bytes"`
	Detection  *detector.DetectionResult `json:"detection"`
}

type SourceService struct {
	db             *pgxpool.Pool
	sourcesDir     string
	fallbackOwners sync.Map
}

func NewSourceService(db *pgxpool.Pool, sourcesDir string) *SourceService {
	if sourcesDir == "" {
		sourcesDir = "./data/sources"
	}
	_ = os.MkdirAll(sourcesDir, 0755)
	return &SourceService{
		db:         db,
		sourcesDir: sourcesDir,
	}
}

// GetSourcePath returns the isolated filesystem path for a previously uploaded source,
// verifying that ownerID owns the source workspace.
func (s *SourceService) GetSourcePath(ctx context.Context, ownerID, sourceID uuid.UUID) (string, error) {
	if sourceID == uuid.Nil {
		return "", ErrSourceDirNotFound
	}

	if s.db != nil {
		var realOwner uuid.UUID
		var workspacePath string
		err := s.db.QueryRow(ctx,
			`SELECT owner_id, workspace_path FROM source_workspaces WHERE id = $1`,
			sourceID,
		).Scan(&realOwner, &workspacePath)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return "", ErrSourceDirNotFound
			}
			return "", fmt.Errorf("failed to query source workspace: %w", err)
		}
		if realOwner != ownerID {
			return "", ErrUnauthorizedSource
		}
		info, err := os.Stat(workspacePath)
		if err != nil || !info.IsDir() {
			return "", ErrSourceDirNotFound
		}
		return workspacePath, nil
	}

	// Fallback for tests running without database connection: verify in-memory ownership
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
		if val, ok := s.fallbackOwners.Load(sourceID); ok {
			if val.(uuid.UUID) != ownerID {
				return ErrUnauthorizedSource
			}
			s.fallbackOwners.Delete(sourceID)
		}

		dir = filepath.Join(s.sourcesDir, sourceID.String())
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			return ErrSourceDirNotFound
		}
	}

	return os.RemoveAll(dir)
}

// IngestZip extracts an uploaded zip archive into an isolated source directory.
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

	if s.db != nil {
		_, err = s.db.Exec(ctx,
			`INSERT INTO source_workspaces (id, owner_id, workspace_path, files_count, total_bytes, created_at)
			 VALUES ($1, $2, $3, $4, $5, $6)`,
			sourceID, ownerID, targetDir, filesCount, totalBytes, time.Now(),
		)
		if err != nil {
			_ = os.RemoveAll(targetDir)
			return nil, fmt.Errorf("failed to record source workspace: %w", err)
		}
	}

	s.fallbackOwners.Store(sourceID, ownerID)
	detection, _ := detector.Detect(targetDir)

	return &SourceUploadResult{
		SourceID:   sourceID,
		FilesCount: filesCount,
		TotalBytes: totalBytes,
		Detection:  detection,
	}, nil
}

// IngestTarGz extracts an uploaded tar.gz archive into an isolated source directory.
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
			_ = os.MkdirAll(filepath.Dir(destPath), 0755)
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

	if s.db != nil {
		_, err = s.db.Exec(ctx,
			`INSERT INTO source_workspaces (id, owner_id, workspace_path, files_count, total_bytes, created_at)
			 VALUES ($1, $2, $3, $4, $5, $6)`,
			sourceID, ownerID, targetDir, filesCount, totalBytes, time.Now(),
		)
		if err != nil {
			_ = os.RemoveAll(targetDir)
			return nil, fmt.Errorf("failed to record source workspace: %w", err)
		}
	}

	s.fallbackOwners.Store(sourceID, ownerID)
	detection, _ := detector.Detect(targetDir)

	return &SourceUploadResult{
		SourceID:   sourceID,
		FilesCount: filesCount,
		TotalBytes: totalBytes,
		Detection:  detection,
	}, nil
}

// IngestMultipartFiles writes multiple files uploaded via HTML5 directory selection.
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

	if s.db != nil {
		_, err = s.db.Exec(ctx,
			`INSERT INTO source_workspaces (id, owner_id, workspace_path, files_count, total_bytes, created_at)
			 VALUES ($1, $2, $3, $4, $5, $6)`,
			sourceID, ownerID, targetDir, filesCount, totalBytes, time.Now(),
		)
		if err != nil {
			_ = os.RemoveAll(targetDir)
			return nil, fmt.Errorf("failed to record source workspace: %w", err)
		}
	}

	s.fallbackOwners.Store(sourceID, ownerID)
	detection, _ := detector.Detect(targetDir)

	return &SourceUploadResult{
		SourceID:   sourceID,
		FilesCount: filesCount,
		TotalBytes: totalBytes,
		Detection:  detection,
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
			// Found a file at root level (e.g. "package.json")
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
// and lifts them to the workspace root if so.
func normalizeSourceWorkspace(targetDir string) error {
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
