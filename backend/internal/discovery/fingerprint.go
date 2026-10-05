package discovery

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// ComputeDirectoryContentFingerprint walks a workspace directory and deterministically
// hashes all non-pruned files to produce an immutable content fingerprint.
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
			return nil
		}
		if d.IsDir() {
			if path != cleanDir && IsPrunedDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}

		rel, relErr := filepath.Rel(cleanDir, path)
		if relErr != nil {
			return nil
		}
		slashRel := filepath.ToSlash(rel)

		// Exclude pruned files
		if IsPrunedDir(filepath.Base(path)) {
			return nil
		}

		f, openErr := os.Open(path)
		if openErr != nil {
			return nil
		}
		defer f.Close()

		h := sha256.New()
		sz, copyErr := io.Copy(h, f)
		if copyErr != nil {
			return nil
		}

		files = append(files, fileInfo{
			relPath: slashRel,
			size:    sz,
			hash:    hex.EncodeToString(h.Sum(nil)),
		})
		return nil
	})

	if err != nil {
		return "", fmt.Errorf("walk failed: %w", err)
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
