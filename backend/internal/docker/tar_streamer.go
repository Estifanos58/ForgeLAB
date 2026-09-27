package docker

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TarStreamerOptions configures the streaming build context generator.
type TarStreamerOptions struct {
	BuildContextDir string
	Matcher         *DockerignoreMatcher
	VirtualFiles    map[string][]byte
	EmitLog         func(phase, stream, message string)
}

// StreamBuildContext creates a streaming tar archive of the build context directory.
// It runs the file traversal in a separate goroutine and streams the tar entries
// directly into an io.PipeReader, avoiding loading the archive into RAM.
//
// If the context is canceled or the reader is closed, the writer goroutine detects
// it immediately, closes the pipe, and terminates without leaking resources.
func StreamBuildContext(ctx context.Context, opts TarStreamerOptions) io.ReadCloser {
	pr, pw := io.Pipe()

	go func() {
		var streamErr error
		defer func() {
			pw.CloseWithError(streamErr)
		}()

		tw := tar.NewWriter(pw)
		defer tw.Close()

		// 1. Stream virtual in-memory files first (e.g. generated Dockerfile.forgelab)
		for vName, vContent := range opts.VirtualFiles {
			select {
			case <-ctx.Done():
				streamErr = ctx.Err()
				return
			default:
			}

			vHeader := &tar.Header{
				Name:     filepath.ToSlash(strings.TrimPrefix(vName, "/")),
				Mode:     0644,
				Size:     int64(len(vContent)),
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
				// Log warning but continue if permission or transient error on non-essential file
				if opts.EmitLog != nil {
					opts.EmitLog("build", "stderr", fmt.Sprintf("Warning: could not read path %s: %v", path, walkErr))
				}
				return nil
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
				// File may have been deleted concurrently
				return nil
			}

			var linkTarget string
			if info.Mode()&os.ModeSymlink != 0 {
				target, err := os.Readlink(path)
				if err != nil {
					return nil // Skip broken symlink
				}
				linkTarget = target
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

			// Stream regular file content
			if info.Mode().IsRegular() {
				file, err := os.Open(path)
				if err != nil {
					// File might be locked or removed
					return nil
				}
				defer file.Close()

				// Stream directly using internal 32KB copy buffer
				if _, err := io.Copy(tw, file); err != nil {
					if errors.Is(err, io.ErrClosedPipe) {
						return err
					}
					return fmt.Errorf("failed streaming file %s: %w", slashRelPath, err)
				}
			}

			return nil
		})
	}()

	return pr
}
