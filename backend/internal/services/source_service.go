package services

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/forgelab/backend/internal/detector"
)

var (
	ErrInvalidSourceArchive  = errors.New("invalid or corrupt source archive")
	ErrPathTraversalDetected = errors.New("path traversal detected in source archive")
	ErrArchiveTooLarge       = errors.New("uncompressed source exceeds maximum allowed size (100MB)")
	ErrSourceDirNotFound     = errors.New("source directory not found")
)

const MaxUncompressedBytes = 100 * 1024 * 1024 // 100MB

type SourceUploadResult struct {
	SourceID   uuid.UUID                `json:"source_id"`
	FilesCount int                      `json:"files_count"`
	TotalBytes int64                    `json:"total_bytes"`
	Detection  *detector.DetectionResult `json:"detection"`
}

type SourceService struct {
	sourcesDir string
}

func NewSourceService(sourcesDir string) *SourceService {
	if sourcesDir == "" {
		sourcesDir = "./data/sources"
	}
	_ = os.MkdirAll(sourcesDir, 0755)
	return &SourceService{sourcesDir: sourcesDir}
}

// GetSourcePath returns the isolated filesystem path for a previously uploaded source.
func (s *SourceService) GetSourcePath(sourceID uuid.UUID) (string, error) {
	if sourceID == uuid.Nil {
		return "", ErrSourceDirNotFound
	}
	dir := filepath.Join(s.sourcesDir, sourceID.String())
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return "", ErrSourceDirNotFound
	}
	return dir, nil
}

// DeleteSource cleans up an uploaded source workspace.
func (s *SourceService) DeleteSource(sourceID uuid.UUID) error {
	dir := filepath.Join(s.sourcesDir, sourceID.String())
	return os.RemoveAll(dir)
}

// IngestZip extracts an uploaded zip archive into an isolated source directory.
func (s *SourceService) IngestZip(ctx context.Context, r io.ReaderAt, size int64) (*SourceUploadResult, error) {
	zipReader, err := zip.NewReader(r, size)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidSourceArchive, err)
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

		// Security: Check for path traversal / Zip Slip
		cleanRel := filepath.Clean(f.Name)
		if strings.HasPrefix(cleanRel, "..") || filepath.IsAbs(cleanRel) {
			_ = os.RemoveAll(targetDir)
			return nil, ErrPathTraversalDetected
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

		written, err := io.Copy(outFile, io.LimitReader(rc, MaxUncompressedBytes-totalBytes))
		outFile.Close()
		rc.Close()
		if err != nil {
			_ = os.RemoveAll(targetDir)
			return nil, err
		}

		totalBytes += written
		if totalBytes > MaxUncompressedBytes {
			_ = os.RemoveAll(targetDir)
			return nil, ErrArchiveTooLarge
		}
		filesCount++
	}

	// Run detection
	detection, _ := detector.Detect(targetDir)

	return &SourceUploadResult{
		SourceID:   sourceID,
		FilesCount: filesCount,
		TotalBytes: totalBytes,
		Detection:  detection,
	}, nil
}

// IngestTarGz extracts an uploaded tar.gz archive into an isolated source directory.
func (s *SourceService) IngestTarGz(ctx context.Context, r io.Reader) (*SourceUploadResult, error) {
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

		cleanRel := filepath.Clean(header.Name)
		if strings.HasPrefix(cleanRel, "..") || filepath.IsAbs(cleanRel) {
			_ = os.RemoveAll(targetDir)
			return nil, ErrPathTraversalDetected
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

			written, err := io.Copy(outFile, io.LimitReader(tarReader, MaxUncompressedBytes-totalBytes))
			outFile.Close()
			if err != nil {
				_ = os.RemoveAll(targetDir)
				return nil, err
			}

			totalBytes += written
			if totalBytes > MaxUncompressedBytes {
				_ = os.RemoveAll(targetDir)
				return nil, ErrArchiveTooLarge
			}
			filesCount++
		}
	}

	detection, _ := detector.Detect(targetDir)

	return &SourceUploadResult{
		SourceID:   sourceID,
		FilesCount: filesCount,
		TotalBytes: totalBytes,
		Detection:  detection,
	}, nil
}

// IngestMultipartFiles writes multiple files uploaded via HTML5 directory selection.
func (s *SourceService) IngestMultipartFiles(ctx context.Context, files []*multipart.FileHeader) (*SourceUploadResult, error) {
	sourceID := uuid.New()
	targetDir := filepath.Join(s.sourcesDir, sourceID.String())
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return nil, err
	}

	cleanTarget := filepath.Clean(targetDir)
	var totalBytes int64
	filesCount := 0

	// Determine common prefix to strip if entire folder was uploaded
	for _, fh := range files {
		relPath := fh.Filename
		if shouldIgnorePath(relPath) {
			continue
		}

		cleanRel := filepath.Clean(relPath)
		if strings.HasPrefix(cleanRel, "..") || filepath.IsAbs(cleanRel) {
			_ = os.RemoveAll(targetDir)
			return nil, ErrPathTraversalDetected
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

		written, err := io.Copy(outFile, io.LimitReader(srcFile, MaxUncompressedBytes-totalBytes))
		outFile.Close()
		srcFile.Close()
		if err != nil {
			_ = os.RemoveAll(targetDir)
			return nil, err
		}

		totalBytes += written
		if totalBytes > MaxUncompressedBytes {
			_ = os.RemoveAll(targetDir)
			return nil, ErrArchiveTooLarge
		}
		filesCount++
	}

	detection, _ := detector.Detect(targetDir)

	return &SourceUploadResult{
		SourceID:   sourceID,
		FilesCount: filesCount,
		TotalBytes: totalBytes,
		Detection:  detection,
	}, nil
}

func shouldIgnorePath(path string) bool {
	norm := filepath.ToSlash(path)
	parts := strings.Split(norm, "/")
	for _, p := range parts {
		lower := strings.ToLower(p)
		if lower == "node_modules" || lower == ".git" || lower == ".next" ||
			lower == "dist" || lower == "build" || lower == ".venv" ||
			lower == "venv" || lower == "__pycache__" || lower == ".cache" ||
			lower == ".ds_store" || lower == "thumbs.db" {
			return true
		}
	}
	return false
}
