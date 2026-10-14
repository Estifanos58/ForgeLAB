package discovery

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

type cachedFileHash struct {
	size        int64
	modTimeNano int64
	hash        string
}

var contentHashCache sync.Map

// ComputeLightweightDirectoryFingerprint computes a reliable directory fingerprint.
// To guarantee reproducible deployments, metadata-only fingerprints are replaced
// with reliable content-based fingerprints backed by metadata caching.
func ComputeLightweightDirectoryFingerprint(dirPath string) (string, error) {
	return ComputeDirectoryContentFingerprint(dirPath)
}

// ComputeDirectoryContentFingerprint walks a workspace directory and deterministically
// hashes all non-pruned files to produce an immutable content fingerprint. It uses
// metadata-based caching to avoid re-reading unchanged files repeatedly, and propagates
// filesystem errors instead of silently skipping files.
func ComputeDirectoryContentFingerprint(dirPath string) (string, error) {
	cleanDir := filepath.Clean(dirPath)
	info, err := os.Stat(cleanDir)
	if err != nil {
		return "", fmt.Errorf("failed to access directory: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("path is not a directory: %s", cleanDir)
	}

	type fileInfo struct {
		relPath string
		size    int64
		hash    string
	}

	var files []fileInfo

	err = filepath.WalkDir(cleanDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("filesystem error traversing %s: %w", path, walkErr)
		}
		if d.IsDir() {
			if path != cleanDir && IsPrunedDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}

		rel, relErr := filepath.Rel(cleanDir, path)
		if relErr != nil {
			return fmt.Errorf("failed to compute relative path for %s: %w", path, relErr)
		}
		slashRel := filepath.ToSlash(rel)

		// Exclude pruned files
		if IsPrunedDir(filepath.Base(path)) {
			return nil
		}

		dInfo, infoErr := d.Info()
		if infoErr != nil {
			return fmt.Errorf("failed to stat %s: %w", path, infoErr)
		}

		sz := dInfo.Size()
		modNano := dInfo.ModTime().UnixNano()

		// Check metadata-keyed cache before reading file from disk
		cacheKey := path
		if cachedVal, ok := contentHashCache.Load(cacheKey); ok {
			c := cachedVal.(cachedFileHash)
			if c.size == sz && c.modTimeNano == modNano {
				files = append(files, fileInfo{
					relPath: slashRel,
					size:    sz,
					hash:    c.hash,
				})
				return nil
			}
		}

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
		contentHashCache.Store(cacheKey, cachedFileHash{
			size:        copiedSz,
			modTimeNano: modNano,
			hash:        fileHash,
		})

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
