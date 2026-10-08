package docker

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	ErrBuildContextTooLarge     = errors.New("build context exceeds maximum allowed total size limit")
	ErrBuildContextTooManyFiles = errors.New("build context exceeds maximum allowed file count limit")
	ErrBuildContextFileTooLarge = errors.New("build context contains a file exceeding maximum allowed single file size limit")
	ErrBuildContextAccessDenied = errors.New("build context file access denied or security violation during traversal")
	ErrSymlinkEscape            = errors.New("build context contains a symlink pointing outside the build context boundary")
)

// TarStreamTracker exposes tracking metrics and the underlying stream error.
type TarStreamTracker interface {
	io.ReadCloser
	StreamError() error
	FilesProcessed() int
	BytesProcessed() int64
	CurrentPath() string
}

type tarStreamTracker struct {
	*io.PipeReader
	mu             sync.Mutex
	streamErr      error
	filesProcessed int
	bytesProcessed int64
	currentPath    string
}

func (t *tarStreamTracker) StreamError() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.streamErr
}

func (t *tarStreamTracker) FilesProcessed() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.filesProcessed
}

func (t *tarStreamTracker) BytesProcessed() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.bytesProcessed
}

func (t *tarStreamTracker) CurrentPath() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.currentPath
}

// TarStreamerOptions configures the streaming build context generator.
type TarStreamerOptions struct {
	BuildContextDir string
	Matcher         *DockerignoreMatcher
	VirtualFiles    map[string][]byte
	EmitLog         func(phase, stream, message string)
	MaxTotalBytes   int64
	MaxFiles        int
	MaxFileSize     int64
}

func getEnvInt64(key string, fallback int64) int64 {
	if v, ok := os.LookupEnv(key); ok {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v, ok := os.LookupEnv(key); ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

// StreamBuildContext creates a streaming tar archive of the build context directory.
// It runs the file traversal in a separate goroutine and streams the tar entries
// directly into an io.PipeReader, avoiding loading the archive into RAM.
//
// If the context is canceled or the reader is closed, the writer goroutine detects
// it immediately, closes the pipe, and terminates without leaking resources.
func StreamBuildContext(ctx context.Context, opts TarStreamerOptions) io.ReadCloser {
	pr, pw := io.Pipe()
	tracker := &tarStreamTracker{PipeReader: pr}

	maxTotalBytes := opts.MaxTotalBytes
	if maxTotalBytes <= 0 {
		maxTotalBytes = getEnvInt64("BUILD_CONTEXT_MAX_BYTES", 500*1024*1024) // 500 MB default
	}
	maxFiles := opts.MaxFiles
	if maxFiles <= 0 {
		maxFiles = getEnvInt("BUILD_CONTEXT_MAX_FILES", 50000) // 50,000 files default
	}
	maxFileSize := opts.MaxFileSize
	if maxFileSize <= 0 {
		maxFileSize = getEnvInt64("BUILD_CONTEXT_MAX_FILE_BYTES", 100*1024*1024) // 100 MB default
	}

	canonicalContextDir, err := filepath.EvalSymlinks(opts.BuildContextDir)
	if err != nil {
		canonicalContextDir = filepath.Clean(opts.BuildContextDir)
	}

	go func() {
		var streamErr error
		tw := tar.NewWriter(pw)
		defer func() {
			if streamErr != nil {
				tracker.mu.Lock()
				curPath := tracker.currentPath
				filesCount := tracker.filesProcessed
				bytesCount := tracker.bytesProcessed
				diagErr := fmt.Errorf("build context streaming failed at path '%s' (processed %d files, %d bytes): %w", curPath, filesCount, bytesCount, streamErr)
				tracker.streamErr = diagErr
				tracker.mu.Unlock()
				if opts.EmitLog != nil {
					opts.EmitLog("build", "stderr", diagErr.Error())
				}
				_ = pw.CloseWithError(diagErr)
			} else {
				_ = tw.Close()
				_ = pw.Close()
			}
		}()

		var totalBytes int64
		var fileCount int

		// 1. Stream virtual in-memory files first (e.g. generated Dockerfile.forgelab)
		for vName, vContent := range opts.VirtualFiles {
			select {
			case <-ctx.Done():
				streamErr = ctx.Err()
				return
			default:
			}

			fileCount++
			if fileCount > maxFiles {
				streamErr = fmt.Errorf("%w (limit: %d files)", ErrBuildContextTooManyFiles, maxFiles)
				return
			}

			vSize := int64(len(vContent))
			if vSize > maxFileSize {
				streamErr = fmt.Errorf("%w: virtual file %s size %d exceeds limit %d", ErrBuildContextFileTooLarge, vName, vSize, maxFileSize)
				return
			}

			totalBytes += vSize
			tracker.mu.Lock()
			tracker.currentPath = vName
			tracker.filesProcessed = fileCount
			tracker.bytesProcessed = totalBytes
			tracker.mu.Unlock()

			if totalBytes > maxTotalBytes {
				streamErr = fmt.Errorf("%w: total size exceeds limit of %d bytes", ErrBuildContextTooLarge, maxTotalBytes)
				return
			}

			vHeader := &tar.Header{
				Name:     filepath.ToSlash(strings.TrimPrefix(vName, "/")),
				Mode:     0644,
				Size:     vSize,
				ModTime:  time.Now(),
				Typeflag: tar.TypeReg,
			}

			if err := tw.WriteHeader(vHeader); err != nil {
				streamErr = fmt.Errorf("failed to write virtual file header %s: %w", vName, err)
				return
			}

			if _, err := tw.Write(vContent); err != nil {
				streamErr = fmt.Errorf("failed to stream virtual file %s: %w", vName, err)
				return
			}
		}

		// 2. Efficiently walk the build context directory
		streamErr = filepath.WalkDir(opts.BuildContextDir, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				if os.IsNotExist(walkErr) {
					// File disappeared during traversal - non-fatal, log warning
					if opts.EmitLog != nil {
						opts.EmitLog("build", "stderr", fmt.Sprintf("Warning: file disappeared during traversal %s: %v", path, walkErr))
					}
					return nil
				}
				if os.IsPermission(walkErr) {
					return fmt.Errorf("%w: permission denied accessing %s: %v", ErrBuildContextAccessDenied, path, walkErr)
				}
				// Other unexpected or security errors must fail the build
				return fmt.Errorf("%w: failed accessing %s: %v", ErrBuildContextAccessDenied, path, walkErr)
			}

			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			relPath, err := filepath.Rel(opts.BuildContextDir, path)
			if err != nil {
				return err
			}

			// Skip root directory entry itself
			if relPath == "." || relPath == "" {
				return nil
			}

			slashRelPath := filepath.ToSlash(relPath)
			isDir := d.IsDir()

			// Check .dockerignore filtering
			if opts.Matcher != nil {
				if isDir && opts.Matcher.CanSkipDir(slashRelPath) {
					return filepath.SkipDir
				}
				if opts.Matcher.Matches(slashRelPath, isDir) {
					if isDir {
						return filepath.SkipDir
					}
					return nil
				}
			}

			info, err := d.Info()
			if err != nil {
				if os.IsNotExist(err) {
					// File removed concurrently
					return nil
				}
				if os.IsPermission(err) {
					return fmt.Errorf("%w: permission denied on %s: %v", ErrBuildContextAccessDenied, slashRelPath, err)
				}
				return fmt.Errorf("stat error for %s: %w", slashRelPath, err)
			}

			var linkTarget string
			if info.Mode()&os.ModeSymlink != 0 {
				target, err := os.Readlink(path)
				if err != nil {
					if os.IsNotExist(err) {
						return nil // Disappeared concurrently
					}
					return fmt.Errorf("%w: failed reading symlink %s: %v", ErrBuildContextAccessDenied, slashRelPath, err)
				}

				// Verify symlink does not escape build context boundary
				var resolvedTarget string
				if filepath.IsAbs(target) {
					resolvedTarget = filepath.Clean(target)
				} else {
					resolvedTarget = filepath.Clean(filepath.Join(filepath.Dir(path), target))
				}
				if realTarget, err := filepath.EvalSymlinks(resolvedTarget); err == nil {
					resolvedTarget = realTarget
				}
				relToRoot, err := filepath.Rel(canonicalContextDir, resolvedTarget)
				if err != nil || strings.HasPrefix(relToRoot, "..") || filepath.IsAbs(relToRoot) {
					return fmt.Errorf("%w: symlink %s points to %s outside build context", ErrSymlinkEscape, slashRelPath, target)
				}
				linkTarget = target
			}

			fileCount++
			if fileCount > maxFiles {
				return fmt.Errorf("%w (limit: %d files exceeded at %s)", ErrBuildContextTooManyFiles, maxFiles, slashRelPath)
			}

			if info.Mode().IsRegular() {
				if info.Size() > maxFileSize {
					return fmt.Errorf("%w: file %s size %d exceeds limit %d bytes", ErrBuildContextFileTooLarge, slashRelPath, info.Size(), maxFileSize)
				}
				totalBytes += info.Size()
				if totalBytes > maxTotalBytes {
					return fmt.Errorf("%w: total context size %d exceeds limit %d bytes", ErrBuildContextTooLarge, totalBytes, maxTotalBytes)
				}
			}

			header, err := tar.FileInfoHeader(info, linkTarget)
			if err != nil {
				return fmt.Errorf("tar header error for %s: %w", slashRelPath, err)
			}

			header.Name = slashRelPath
			if isDir {
				header.Name += "/"
			}

			if err := tw.WriteHeader(header); err != nil {
				return err
			}

			tracker.mu.Lock()
			tracker.currentPath = slashRelPath
			tracker.filesProcessed = fileCount
			tracker.bytesProcessed = totalBytes
			tracker.mu.Unlock()

			// Stream regular file content
			if info.Mode().IsRegular() {
				file, err := os.Open(path)
				if err != nil {
					if os.IsNotExist(err) {
						return nil // Disappeared concurrently
					}
					if os.IsPermission(err) {
						return fmt.Errorf("%w: permission denied opening %s: %v", ErrBuildContextAccessDenied, slashRelPath, err)
					}
					return fmt.Errorf("%w: failed opening %s: %v", ErrBuildContextAccessDenied, slashRelPath, err)
				}

				// Stream directly using internal 32KB copy buffer
				_, copyErr := io.Copy(tw, file)
				file.Close()
				if copyErr != nil {
					if errors.Is(copyErr, io.ErrClosedPipe) {
						return copyErr
					}
					return fmt.Errorf("failed streaming file %s: %w", slashRelPath, copyErr)
				}
			}

			return nil
		})
	}()

	return tracker
}
