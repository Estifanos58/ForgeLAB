package discovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/forgelab/backend/internal/security"
)

// FingerprintOptions specifies limits and configuration for directory fingerprinting.
type FingerprintOptions struct {
	MaxFiles      int
	MaxFileSize   int64
	MaxTotalBytes int64
}

// DefaultFingerprintOptions returns safe default limits consistent with build-context rules.
func DefaultFingerprintOptions() FingerprintOptions {
	return FingerprintOptions{
		MaxFiles:      50000,
		MaxFileSize:   100 * 1024 * 1024, // 100 MB
		MaxTotalBytes: 500 * 1024 * 1024, // 500 MB
	}
}

// ComputeLightweightDirectoryFingerprint computes a reliable directory content fingerprint.
func ComputeLightweightDirectoryFingerprint(dirPath string) (string, error) {
	return ComputeDirectoryContentFingerprint(dirPath)
}

// ComputeDirectoryContentFingerprint walks a workspace directory and deterministically
// hashes all non-pruned files to produce an immutable content fingerprint.
func ComputeDirectoryContentFingerprint(dirPath string) (string, error) {
	return ComputeDirectoryContentFingerprintWithContext(context.Background(), dirPath)
}

func canonicalizeWithAncestor(raw string) string {
	if resolved, err := security.CanonicalizeWithAncestor(raw); err == nil {
		return resolved
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return filepath.Clean(raw)
	}
	return filepath.Clean(abs)
}

func isWithinRoot(root, target string) bool {
	return security.IsWithinRoot(root, target)
}

func normalizeWindowsReadlinkTarget(target string) string {
	return security.NormalizeReadlinkTarget(target)
}

func readLinkIfReparse(path string, d os.DirEntry) (target string, isLink bool, err error) {
	mode := d.Type()
	if mode&(os.ModeSymlink|os.ModeIrregular) == 0 {
		return "", false, nil
	}
	t, rErr := os.Readlink(path)
	if rErr != nil {
		if mode&os.ModeSymlink != 0 {
			return "", true, rErr
		}
		return "", false, nil
	}
	return normalizeWindowsReadlinkTarget(t), true, nil
}

// ComputeDirectoryContentFingerprintWithContext walks a workspace directory with context cancellation
// and traversal limits, hardened against symlink escape attacks.
func ComputeDirectoryContentFingerprintWithContext(ctx context.Context, dirPath string, opts ...FingerprintOptions) (string, error) {
	cleanDir := filepath.Clean(dirPath)
	info, err := os.Stat(cleanDir)
	if err != nil {
		return "", fmt.Errorf("failed to access directory: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("path is not a directory: %s", cleanDir)
	}

	canonicalDir := canonicalizeWithAncestor(cleanDir)

	options := DefaultFingerprintOptions()
	if len(opts) > 0 {
		if opts[0].MaxFiles > 0 {
			options.MaxFiles = opts[0].MaxFiles
		}
		if opts[0].MaxFileSize > 0 {
			options.MaxFileSize = opts[0].MaxFileSize
		}
		if opts[0].MaxTotalBytes > 0 {
			options.MaxTotalBytes = opts[0].MaxTotalBytes
		}
	}

	type fileInfo struct {
		relPath string
		size    int64
		hash    string
	}

	var files []fileInfo
	var totalBytes int64

	err = filepath.WalkDir(canonicalDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("filesystem error traversing %s: %w", path, walkErr)
		}

		if err := ctx.Err(); err != nil {
			return err
		}

		if d.IsDir() {
			if path != canonicalDir && IsPrunedDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}

		rel, relErr := filepath.Rel(canonicalDir, path)
		if relErr != nil {
			return fmt.Errorf("failed to compute relative path for %s: %w", path, relErr)
		}
		slashRel := filepath.ToSlash(rel)

		// Exclude pruned files
		if IsPrunedDir(filepath.Base(path)) {
			return nil
		}

		// Traversal limit check
		if len(files) >= options.MaxFiles {
			return fmt.Errorf("source directory exceeds maximum file count limit of %d files", options.MaxFiles)
		}

		// Harden symlink & Windows junction handling: prevent external links from escaping approved source root
		if target, isLink, linkErr := readLinkIfReparse(path, d); isLink {
			if linkErr != nil {
				return fmt.Errorf("failed to read symlink %s: %w", slashRel, linkErr)
			}

			var resolvedTarget string
			if filepath.IsAbs(target) {
				resolvedTarget = filepath.Clean(target)
			} else {
				resolvedTarget = filepath.Clean(filepath.Join(filepath.Dir(path), target))
			}
			resolvedTarget = canonicalizeWithAncestor(resolvedTarget)

			if !isWithinRoot(canonicalDir, resolvedTarget) {
				return fmt.Errorf("security violation: symlink %q points to %q outside approved source root", slashRel, target)
			}

			// Valid internal symlink/junction: record target string into fingerprint
			h := sha256.New()
			h.Write([]byte("symlink:" + filepath.ToSlash(target)))
			files = append(files, fileInfo{
				relPath: slashRel,
				size:    int64(len(target)),
				hash:    hex.EncodeToString(h.Sum(nil)),
			})
			return nil
		}

		fi, statErr := d.Info()
		if statErr != nil {
			return fmt.Errorf("failed to stat file %s: %w", path, statErr)
		}
		if !fi.Mode().IsRegular() {
			return nil
		}

		if fi.Size() > options.MaxFileSize {
			return fmt.Errorf("file %s size %d bytes exceeds maximum file size limit of %d bytes", slashRel, fi.Size(), options.MaxFileSize)
		}

		if totalBytes+fi.Size() > options.MaxTotalBytes {
			return fmt.Errorf("source directory total size exceeds maximum limit of %d bytes", options.MaxTotalBytes)
		}
		totalBytes += fi.Size()

		f, openErr := os.Open(path)
		if openErr != nil {
			return fmt.Errorf("failed to open file %s for fingerprinting: %w", path, openErr)
		}

		h := sha256.New()
		copiedSz, copyErr := io.Copy(h, f)
		_ = f.Close()
		if copyErr != nil {
			return fmt.Errorf("failed to read file %s for fingerprinting: %w", path, copyErr)
		}

		fileHash := hex.EncodeToString(h.Sum(nil))

		files = append(files, fileInfo{
			relPath: slashRel,
			size:    copiedSz,
			hash:    fileHash,
		})
		return nil
	})

	if err != nil {
		return "", fmt.Errorf("directory fingerprinting failed: %w", err)
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].relPath < files[j].relPath
	})

	combined := sha256.New()
	for _, fi := range files {
		combined.Write([]byte(fmt.Sprintf("%s:%d:%s\n", fi.relPath, fi.size, fi.hash)))
	}

	sum := combined.Sum(nil)
	return fmt.Sprintf("fp_%s", hex.EncodeToString(sum[:16])), nil
}
