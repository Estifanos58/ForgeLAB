package tararchive

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/forgelab/backend/internal/dockerignore"
)

var (
	ErrBuildContextTooLarge     = errors.New("build context exceeds maximum allowed total size limit")
	ErrBuildContextTooManyFiles = errors.New("build context exceeds maximum allowed file count limit")
	ErrBuildContextFileTooLarge = errors.New("build context contains a file exceeding maximum allowed single file size limit")
	ErrBuildContextAccessDenied = errors.New("build context file access denied or security violation during traversal")
	ErrSymlinkEscape            = errors.New("build context contains a symlink pointing outside the build context boundary")
	ErrDockerfileMissing        = errors.New("required Dockerfile not found in build context")
	ErrTarCorrupted             = errors.New("tar archive is corrupted or has an invalid header")
)

// BuildContextOptions configures TAR archive generation for a Docker build context.
type BuildContextOptions struct {
	BuildContextDir string
	Matcher         *dockerignore.DockerignoreMatcher
	VirtualFiles    map[string][]byte
	MaxTotalBytes   int64
	MaxFiles        int
	MaxFileSize     int64
	EmitLog         func(phase, stream, message string)
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

// WriteBuildContext generates a strict, validated TAR stream of the build context directory
// directly into the provided io.Writer.
//
// Invariant:
// 1. Files are opened and statted before the TAR header is written.
// 2. Exactly header.Size bytes must be written for every entry before moving to the next entry.
// 3. Short reads or disappearing files immediately abort the stream with a descriptive error.
// 4. tar.Writer.Close() must succeed or the operation fails.
func WriteBuildContext(ctx context.Context, w io.Writer, opts BuildContextOptions) (filesCount int, bytesCount int64, err error) {
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

	tw := tar.NewWriter(w)

	// 1. Write virtual files first (e.g. Dockerfile.forgelab)
	for vName, vContent := range opts.VirtualFiles {
		select {
		case <-ctx.Done():
			return filesCount, bytesCount, ctx.Err()
		default:
		}

		filesCount++
		if filesCount > maxFiles {
			return filesCount, bytesCount, fmt.Errorf("%w (limit: %d files exceeded at virtual file %s)", ErrBuildContextTooManyFiles, maxFiles, vName)
		}

		vSize := int64(len(vContent))
		if vSize > maxFileSize {
			return filesCount, bytesCount, fmt.Errorf("%w: virtual file %s size %d exceeds limit %d bytes", ErrBuildContextFileTooLarge, vName, vSize, maxFileSize)
		}

		bytesCount += vSize
		if bytesCount > maxTotalBytes {
			return filesCount, bytesCount, fmt.Errorf("%w: total size exceeds limit of %d bytes at virtual file %s", ErrBuildContextTooLarge, maxTotalBytes, vName)
		}

		vHeader := &tar.Header{
			Name:     path.Clean(filepath.ToSlash(strings.TrimPrefix(vName, "/"))),
			Mode:     0644,
			Size:     vSize,
			ModTime:  time.Now(),
			Typeflag: tar.TypeReg,
		}

		if err := tw.WriteHeader(vHeader); err != nil {
			return filesCount, bytesCount, fmt.Errorf("failed to write tar header for virtual file %s: %w", vName, err)
		}

		n, writeErr := tw.Write(vContent)
		if writeErr != nil {
			return filesCount, bytesCount, fmt.Errorf("failed to write payload for virtual file %s: %w", vName, writeErr)
		}
		if int64(n) != vSize {
			return filesCount, bytesCount, fmt.Errorf("virtual file %s size mismatch: wrote %d of %d bytes", vName, n, vSize)
		}
	}

	// 2. Walk build context directory
	if opts.BuildContextDir != "" {
		walkErr := filepath.WalkDir(opts.BuildContextDir, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				if os.IsPermission(err) {
					return fmt.Errorf("%w: permission denied accessing %s: %v", ErrBuildContextAccessDenied, p, err)
				}
				return fmt.Errorf("%w: error accessing %s: %v", ErrBuildContextAccessDenied, p, err)
			}

			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			relPath, err := filepath.Rel(opts.BuildContextDir, p)
			if err != nil {
				return err
			}
			if relPath == "." || relPath == "" {
				return nil
			}

			slashRelPath := filepath.ToSlash(relPath)
			isDir := d.IsDir()

			// Check .dockerignore early pruning
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

			// Handle symlinks
			if d.Type()&os.ModeSymlink != 0 {
				target, err := os.Readlink(p)
				if err != nil {
					return fmt.Errorf("%w: failed reading symlink %s: %v", ErrBuildContextAccessDenied, slashRelPath, err)
				}

				var resolvedTarget string
				if filepath.IsAbs(target) {
					resolvedTarget = filepath.Clean(target)
				} else {
					resolvedTarget = filepath.Clean(filepath.Join(filepath.Dir(p), target))
				}
				if realTarget, err := filepath.EvalSymlinks(resolvedTarget); err == nil {
					resolvedTarget = realTarget
				}
				relToRoot, err := filepath.Rel(canonicalContextDir, resolvedTarget)
				if err != nil || strings.HasPrefix(relToRoot, "..") || filepath.IsAbs(relToRoot) {
					return fmt.Errorf("%w: symlink %s points to %s outside build context", ErrSymlinkEscape, slashRelPath, target)
				}

				info, err := d.Info()
				if err != nil {
					return fmt.Errorf("stat failed for symlink %s: %w", slashRelPath, err)
				}

				header, err := tar.FileInfoHeader(info, target)
				if err != nil {
					return fmt.Errorf("failed creating tar header for symlink %s: %w", slashRelPath, err)
				}
				header.Name = slashRelPath

				if err := tw.WriteHeader(header); err != nil {
					return fmt.Errorf("failed writing tar header for symlink %s: %w", slashRelPath, err)
				}
				return nil
			}

			// Handle directories
			if isDir {
				info, err := d.Info()
				if err != nil {
					return fmt.Errorf("stat failed for directory %s: %w", slashRelPath, err)
				}

				header, err := tar.FileInfoHeader(info, "")
				if err != nil {
					return fmt.Errorf("failed creating tar header for directory %s: %w", slashRelPath, err)
				}
				header.Name = slashRelPath + "/"

				if err := tw.WriteHeader(header); err != nil {
					return fmt.Errorf("failed writing tar header for directory %s: %w", slashRelPath, err)
				}
				return nil
			}

			// Handle regular files:
			// Open the file handle BEFORE emitting the header to guarantee the file exists and is readable
			f, err := os.Open(p)
			if err != nil {
				return fmt.Errorf("failed to open file %s before tar header: %w", slashRelPath, err)
			}

			info, err := f.Stat()
			if err != nil {
				_ = f.Close()
				return fmt.Errorf("stat failed for file %s: %w", slashRelPath, err)
			}

			if !info.Mode().IsRegular() {
				_ = f.Close()
				return nil
			}

			expectedSize := info.Size()

			filesCount++
			if filesCount > maxFiles {
				_ = f.Close()
				return fmt.Errorf("%w (limit: %d files exceeded at %s)", ErrBuildContextTooManyFiles, maxFiles, slashRelPath)
			}

			if expectedSize > maxFileSize {
				_ = f.Close()
				return fmt.Errorf("%w: file %s size %d exceeds limit %d bytes", ErrBuildContextFileTooLarge, slashRelPath, expectedSize, maxFileSize)
			}

			bytesCount += expectedSize
			if bytesCount > maxTotalBytes {
				_ = f.Close()
				return fmt.Errorf("%w: total size %d exceeds limit of %d bytes at %s", ErrBuildContextTooLarge, bytesCount, maxTotalBytes, slashRelPath)
			}

			header, err := tar.FileInfoHeader(info, "")
			if err != nil {
				_ = f.Close()
				return fmt.Errorf("failed creating tar header for %s: %w", slashRelPath, err)
			}
			header.Name = slashRelPath
			header.Size = expectedSize

			if err := tw.WriteHeader(header); err != nil {
				_ = f.Close()
				return fmt.Errorf("failed writing tar header for %s: %w", slashRelPath, err)
			}

			written, copyErr := io.Copy(tw, f)
			_ = f.Close()
			if copyErr != nil {
				return fmt.Errorf("failed writing file payload for %s: %w", slashRelPath, copyErr)
			}

			if written != expectedSize {
				return fmt.Errorf("tar payload size mismatch for %s: declared %d bytes, wrote %d bytes", slashRelPath, expectedSize, written)
			}

			return nil
		})

		if walkErr != nil {
			return filesCount, bytesCount, walkErr
		}
	}

	if err := tw.Close(); err != nil {
		return filesCount, bytesCount, fmt.Errorf("failed to finalize tar archive (tar.Writer.Close): %w", err)
	}

	return filesCount, bytesCount, nil
}

// ValidateTarArchive parses and verifies the structural integrity of a TAR archive reader.
// It verifies:
// 1. All TAR headers are readable and valid.
// 2. Every entry's declared size is fully readable without truncation or corruption.
// 3. The archive reaches a valid end (io.EOF).
// 4. The requested Dockerfile (if specified) exists in the archive.
func ValidateTarArchive(r io.Reader, requiredFile string) error {
	tr := tar.NewReader(r)
	var foundRequired bool
	cleanRequired := path.Clean(filepath.ToSlash(strings.TrimPrefix(requiredFile, "./")))
	if cleanRequired == "." || cleanRequired == "" {
		cleanRequired = "Dockerfile"
	}

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("%w: %v", ErrTarCorrupted, err)
		}

		cleanEntry := path.Clean(filepath.ToSlash(strings.TrimPrefix(hdr.Name, "./")))
		if cleanEntry == cleanRequired || strings.HasSuffix(cleanEntry, "/"+cleanRequired) {
			foundRequired = true
		} else if filepath.Base(cleanEntry) == filepath.Base(cleanRequired) {
			foundRequired = true
		}

		if hdr.Typeflag == tar.TypeReg || hdr.Typeflag == tar.TypeRegA || hdr.Typeflag == 0 {
			written, readErr := io.Copy(io.Discard, tr)
			if readErr != nil {
				return fmt.Errorf("corrupted tar entry payload for '%s': %w", hdr.Name, readErr)
			}
			if written != hdr.Size {
				return fmt.Errorf("incomplete tar entry '%s': declared %d bytes, read %d bytes", hdr.Name, hdr.Size, written)
			}
		}
	}

	if requiredFile != "" && !foundRequired {
		return fmt.Errorf("%w: '%s'", ErrDockerfileMissing, cleanRequired)
	}

	return nil
}

// CreateCompletedArchiveFile builds a complete, structurally validated TAR archive into a temporary file on disk.
// Returns an open *os.File positioned at offset 0, ready for Docker ImageBuild.
// The caller is responsible for closing and removing the returned file.
func CreateCompletedArchiveFile(ctx context.Context, tempDir string, opts BuildContextOptions, requiredFile string) (*os.File, error) {
	if tempDir == "" {
		tempDir = os.TempDir()
	}
	tmpFile, err := os.CreateTemp(tempDir, "forgelab-build-*.tar")
	if err != nil {
		return nil, fmt.Errorf("failed to allocate temporary build context file: %w", err)
	}

	_, _, buildErr := WriteBuildContext(ctx, tmpFile, opts)
	if buildErr != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpFile.Name())
		return nil, fmt.Errorf("build context generation failed: %w", buildErr)
	}

	if _, err := tmpFile.Seek(0, io.SeekStart); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpFile.Name())
		return nil, fmt.Errorf("failed to rewind build context archive for validation: %w", err)
	}

	if err := ValidateTarArchive(tmpFile, requiredFile); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpFile.Name())
		return nil, fmt.Errorf("build context validation failed: %w", err)
	}

	if _, err := tmpFile.Seek(0, io.SeekStart); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpFile.Name())
		return nil, fmt.Errorf("failed to rewind build context archive for reading: %w", err)
	}

	return tmpFile, nil
}

// StageAndValidateArchiveStream consumes an entire streaming TAR archive from src, writes it into a temporary
// disk-backed file, validates its structure and the presence of requiredFile, and returns an open *os.File
// rewound to offset 0.
//
// This guarantees that Docker receives a completed, validated archive rather than an unverified streaming pipe.
func StageAndValidateArchiveStream(ctx context.Context, tempDir string, src io.Reader, requiredFile string) (*os.File, error) {
	if tempDir == "" {
		tempDir = os.TempDir()
	}
	tmpFile, err := os.CreateTemp(tempDir, "forgelab-staged-*.tar")
	if err != nil {
		return nil, fmt.Errorf("failed to allocate temporary staged archive file: %w", err)
	}

	if _, err := io.Copy(tmpFile, src); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpFile.Name())
		return nil, fmt.Errorf("failed receiving build context stream: %w", err)
	}

	if _, err := tmpFile.Seek(0, io.SeekStart); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpFile.Name())
		return nil, fmt.Errorf("failed to rewind staged archive for validation: %w", err)
	}

	if err := ValidateTarArchive(tmpFile, requiredFile); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpFile.Name())
		return nil, fmt.Errorf("build context validation failed: %w", err)
	}

	if _, err := tmpFile.Seek(0, io.SeekStart); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpFile.Name())
		return nil, fmt.Errorf("failed to rewind staged archive for reading: %w", err)
	}

	return tmpFile, nil
}
