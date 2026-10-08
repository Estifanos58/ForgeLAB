package docker

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/forgelab/backend/internal/tararchive"
)

var (
	ErrBuildContextTooLarge     = tararchive.ErrBuildContextTooLarge
	ErrBuildContextTooManyFiles = tararchive.ErrBuildContextTooManyFiles
	ErrBuildContextFileTooLarge = tararchive.ErrBuildContextFileTooLarge
	ErrBuildContextAccessDenied = tararchive.ErrBuildContextAccessDenied
	ErrSymlinkEscape            = tararchive.ErrSymlinkEscape
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
// It executes the file traversal in a separate goroutine and writes strictly validated
// TAR entries directly into an io.PipeWriter, enforcing that every entry is completely written
// before the next begins.
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
		bOpts := tararchive.BuildContextOptions{
			BuildContextDir: canonicalContextDir,
			Matcher:         opts.Matcher,
			VirtualFiles:    opts.VirtualFiles,
			EmitLog:         opts.EmitLog,
			MaxTotalBytes:   maxTotalBytes,
			MaxFiles:        maxFiles,
			MaxFileSize:     maxFileSize,
		}

		filesCount, bytesCount, streamErr := tararchive.WriteBuildContext(ctx, pw, bOpts)
		if streamErr != nil {
			tracker.mu.Lock()
			tracker.streamErr = streamErr
			tracker.filesProcessed = filesCount
			tracker.bytesProcessed = bytesCount
			tracker.mu.Unlock()
			if opts.EmitLog != nil {
				opts.EmitLog("build", "stderr", streamErr.Error())
			}
			_ = pw.CloseWithError(streamErr)
		} else {
			tracker.mu.Lock()
			tracker.filesProcessed = filesCount
			tracker.bytesProcessed = bytesCount
			tracker.mu.Unlock()
			_ = pw.Close()
		}
	}()

	return tracker
}
