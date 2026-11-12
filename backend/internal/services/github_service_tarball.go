package services

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/forgelab/backend/internal/analyzer"
	"github.com/google/uuid"
)

// MaterializeGitHubSnapshot downloads and extracts an isolated source snapshot pinned to exact commit SHA.
func (s *GitHubService) MaterializeGitHubSnapshot(ctx context.Context, userID uuid.UUID, owner, repo, ref, destDir string) (string, error) {
	sha, err := s.ResolveCommitSHA(ctx, userID, owner, repo, ref)
	if err != nil {
		return "", fmt.Errorf("failed to resolve commit SHA for %s/%s@%s: %w", owner, repo, ref, err)
	}
	if len(sha) != 40 {
		return "", fmt.Errorf("invalid commit SHA resolved for %s/%s@%s: %s", owner, repo, ref, sha)
	}
	if err := s.AcquireRepoTarball(ctx, userID, owner, repo, sha, destDir); err != nil {
		return "", err
	}
	return sha, nil
}

// AnalyzeRepo acquires the repository archive and runs analyzer.AnalyzeRepository to produce normalized services.
// It resolves the exact commit SHA for the analyzed snapshot and cleans up temporary workspaces.
func (s *GitHubService) AnalyzeRepo(ctx context.Context, userID uuid.UUID, owner, repo, branch, rootDir string) (*analyzer.AnalysisResult, error) {
	if branch == "" {
		branch = "main"
	}

	// Resolve exact commit SHA to pin analyzed revision
	commitSHA, err := s.ResolveCommitSHA(ctx, userID, owner, repo, branch)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve commit for repository %s/%s@%s: %w", owner, repo, branch, err)
	}
	if len(commitSHA) != 40 {
		return nil, fmt.Errorf("invalid commit SHA for repository %s/%s@%s: %s", owner, repo, branch, commitSHA)
	}
	revision := commitSHA

	targetDir, err := os.MkdirTemp("", fmt.Sprintf("forgelab-gh-%s-%s-*", owner, repo))
	if err != nil {
		targetDir = filepath.Join(os.TempDir(), fmt.Sprintf("forgelab-gh-%s-%s-%s", owner, repo, revision))
		_ = os.RemoveAll(targetDir)
		_ = os.MkdirAll(targetDir, 0755)
	}
	defer os.RemoveAll(targetDir) // Clean up temporary GitHub analysis workspace correctly

	err = s.AcquireRepoTarball(ctx, userID, owner, repo, revision, targetDir)
	if err != nil {
		return nil, fmt.Errorf("failed to acquire repository archive for %s/%s@%s: %w", owner, repo, revision, err)
	}

	analysisDir := targetDir
	if rootDir != "" && rootDir != "." {
		cleanRoot := filepath.Clean(filepath.FromSlash(rootDir))
		if strings.HasPrefix(cleanRoot, "..") || cleanRoot == ".." || filepath.IsAbs(cleanRoot) {
			return nil, fmt.Errorf("invalid root_dir path traversal: %s", rootDir)
		}
		analysisDir = filepath.Join(targetDir, cleanRoot)
		if fi, sErr := os.Stat(analysisDir); sErr != nil || !fi.IsDir() {
			return nil, fmt.Errorf("root_dir '%s' does not exist in repository %s/%s", rootDir, owner, repo)
		}
	}
	res, aErr := analyzer.AnalyzeRepository(analysisDir)
	if aErr != nil || res == nil || len(res.Services) == 0 {
		return nil, fmt.Errorf("failed to analyze repository in directory '%s': %v", rootDir, aErr)
	}
	res.RepositoryName = repo
	return res, nil
}

// copyDirectoryContents copies files from src into dst without leaking file descriptors.
func copyDirectoryContents(src, dst string) error {
	return copyDirectoryContentsWithPerms(src, dst, 0755, 0644)
}

// copyDirectoryContentsWithPerms copies files with explicit directory and file permission boundaries.
func copyDirectoryContentsWithPerms(src, dst string, dirPerm os.FileMode, filePerm os.FileMode) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." || rel == ".complete" {
			return nil
		}
		targetPath := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(targetPath, dirPerm)
		}
		if err := os.MkdirAll(filepath.Dir(targetPath), dirPerm); err != nil {
			return err
		}

		inFile, err := os.Open(path)
		if err != nil {
			return err
		}
		outFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, filePerm)
		if err != nil {
			inFile.Close()
			return err
		}
		_, copyErr := io.Copy(outFile, inFile)
		cErr1 := inFile.Close()
		cErr2 := outFile.Close()
		if copyErr != nil {
			return copyErr
		}
		if cErr1 != nil {
			return cErr1
		}
		return cErr2
	})
}

// AcquireRepoTarball downloads and safely extracts an authorized GitHub repository tarball.
// It verifies repository authorization before serving cached or newly downloaded snapshots.
// Private repository snapshots are strictly isolated per ForgeLAB user.
func (s *GitHubService) AcquireRepoTarball(ctx context.Context, userID uuid.UUID, owner, repo, branch, targetDir string) error {
	token, _ := s.getDecryptedTokenForRepo(ctx, userID, owner, repo)

	destClean := filepath.Clean(targetDir)

	// Security requirement: Always verify that the requesting user's credentials are authorized
	// to access this specific repository BEFORE returning any cached data or downloading anew.
	repoMeta, err := s.GetRepo(ctx, userID, owner, repo)
	if err != nil {
		return fmt.Errorf("authorization check failed for repository %s/%s: %w", owner, repo, err)
	}

	if branch == "" {
		branch = repoMeta.DefaultBranch
	}
	if branch == "" {
		branch = "main"
	}

	// Always resolve exact commit SHA to guarantee reproducibility and prevent SHA bypass.
	resolvedSHA, err := s.ResolveCommitSHA(ctx, userID, owner, repo, branch)
	if err != nil {
		return fmt.Errorf("failed to resolve commit SHA for %s/%s@%s: %w", owner, repo, branch, err)
	}
	if len(resolvedSHA) != 40 {
		return fmt.Errorf("invalid commit SHA resolved for %s/%s@%s: %s", owner, repo, branch, resolvedSHA)
	}

	// Permission isolation: private repositories use strict 0700/0600 modes
	dirPerm := os.FileMode(0755)
	filePerm := os.FileMode(0644)
	var cacheDir string
	if repoMeta.Private {
		dirPerm = 0700
		filePerm = 0600
		privateBase := filepath.Join(os.TempDir(), "forgelab_gh_cache", "private")
		_ = os.MkdirAll(privateBase, 0700)
		userPrivateBase := filepath.Join(privateBase, userID.String())
		_ = os.MkdirAll(userPrivateBase, 0700)
		cacheDir = filepath.Join(userPrivateBase, owner, repo, resolvedSHA)
	} else {
		publicBase := filepath.Join(os.TempDir(), "forgelab_gh_cache", "public")
		_ = os.MkdirAll(publicBase, 0755)
		cacheDir = filepath.Join(publicBase, owner, repo, resolvedSHA)
	}

	_ = os.MkdirAll(destClean, dirPerm)

	// Check existing verified cache
	completeMarker := filepath.Join(cacheDir, ".complete")
	if fi, err := os.Stat(completeMarker); err == nil && !fi.IsDir() {
		if content, readErr := os.ReadFile(completeMarker); readErr == nil && strings.TrimSpace(string(content)) == resolvedSHA {
			slog.Info("reusing authorized cached github snapshot", "owner", owner, "repo", repo, "sha", resolvedSHA, "private", repoMeta.Private, "user_id", userID)
			return copyDirectoryContentsWithPerms(cacheDir, destClean, dirPerm, filePerm)
		}
	}
	// Purge incomplete or corrupt cache directory
	_ = os.RemoveAll(cacheDir)

	tarURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/tarball/%s",
		url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(resolvedSHA))

	req, err := http.NewRequestWithContext(ctx, "GET", tarURL, nil)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "ForgeLAB-App")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to download repo tarball: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return ErrGitHubNotConnected
	}
	if resp.StatusCode == http.StatusForbidden {
		if resp.Header.Get("X-RateLimit-Remaining") == "0" {
			return ErrGitHubRateLimited
		}
		if sso := resp.Header.Get("X-GitHub-SSO"); sso != "" {
			return fmt.Errorf("%w: organization SAML SSO authorization required (%s)", ErrGitHubSSORequired, sso)
		}
		return ErrGitHubNeedsReauth
	}
	if resp.StatusCode == http.StatusNotFound {
		return ErrGitHubRepoNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("github returned HTTP %d when downloading repository archive", resp.StatusCode)
	}

	// Enforce compressed archive size limit
	limitedBody := io.LimitReader(resp.Body, MaxGitHubCompressedBytes+1)

	gzReader, err := gzip.NewReader(limitedBody)
	if err != nil {
		return fmt.Errorf("failed to decompress gzip stream: %w", err)
	}
	defer gzReader.Close()

	// Extract into an isolated staging directory first
	stagingDir, err := os.MkdirTemp("", fmt.Sprintf("forgelab-extract-%s-%s-*", owner, repo))
	if err != nil {
		stagingDir = filepath.Join(os.TempDir(), fmt.Sprintf("forgelab-extract-%s-%s-%s", owner, repo, resolvedSHA))
		_ = os.RemoveAll(stagingDir)
		_ = os.MkdirAll(stagingDir, dirPerm)
	}
	defer func() {
		_ = os.RemoveAll(stagingDir)
	}()

	var extractionSuccess bool
	defer func() {
		if !extractionSuccess {
			_ = os.RemoveAll(destClean)
			if cacheDir != "" {
				_ = os.RemoveAll(cacheDir)
			}
		}
	}()

	extractCtx, extractCancel := context.WithTimeout(ctx, GitHubExtractionTimeout)
	defer extractCancel()

	tarReader := tar.NewReader(gzReader)
	var rootPrefix string
	var totalUncompressedBytes int64
	var filesCount int

	for {
		select {
		case <-extractCtx.Done():
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return ErrGitHubExtractionTimeout
		default:
		}

		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar reading error: %w", err)
		}

		filesCount++
		if filesCount > MaxGitHubArchiveFiles {
			return ErrGitHubTooManyFiles
		}

		// GitHub tarballs contain a top-level directory like "owner-repo-commit/"
		parts := strings.Split(strings.Trim(header.Name, "/"), "/")
		if rootPrefix == "" && len(parts) > 0 {
			rootPrefix = parts[0]
		}

		// Strip top-level directory prefix
		if len(parts) <= 1 {
			continue
		}
		relPath := strings.Join(parts[1:], string(filepath.Separator))
		targetPath := filepath.Join(stagingDir, relPath)

		// Security: prevent Zip Slip / Tar Slip path traversal and reject malicious archives
		cleanTarget := filepath.Clean(targetPath)
		if !strings.HasPrefix(cleanTarget, stagingDir+string(filepath.Separator)) {
			return fmt.Errorf("%w: path traversal attempt in %s", ErrGitHubMaliciousArchive, header.Name)
		}

		// Reject dangerous / unsupported file types
		if header.Typeflag == tar.TypeSymlink || header.Typeflag == tar.TypeLink {
			linkTarget := header.Linkname
			if filepath.IsAbs(linkTarget) || strings.HasPrefix(filepath.ToSlash(linkTarget), "/") {
				return fmt.Errorf("%w: absolute link rejected in %s", ErrGitHubMaliciousArchive, header.Name)
			}
			resolvedTarget := filepath.Join(filepath.Dir(targetPath), linkTarget)
			if !strings.HasPrefix(filepath.Clean(resolvedTarget), stagingDir+string(filepath.Separator)) {
				return fmt.Errorf("%w: escaping link rejected in %s", ErrGitHubMaliciousArchive, header.Name)
			}
			// Unsafe link: reject malicious archive
			return fmt.Errorf("%w: symbolic or hard link rejected in %s", ErrGitHubMaliciousArchive, header.Name)
		} else if header.Typeflag != tar.TypeDir && header.Typeflag != tar.TypeReg {
			return fmt.Errorf("%w: unsupported entry type %v in %s", ErrGitHubMaliciousArchive, header.Typeflag, header.Name)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(targetPath, dirPerm); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(targetPath), dirPerm); err != nil {
				return err
			}
			outFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, filePerm)
			if err != nil {
				return err
			}

			// Enforce per-file and cumulative uncompressed size limits
			remainingLimit := MaxGitHubSingleFileBytes + 1
			written, copyErr := io.Copy(outFile, io.LimitReader(tarReader, remainingLimit))
			outFile.Close()
			if copyErr != nil {
				return copyErr
			}
			if written > MaxGitHubSingleFileBytes {
				return ErrGitHubFileTooLarge
			}
			totalUncompressedBytes += written
			if totalUncompressedBytes > MaxGitHubUncompressedBytes {
				return ErrGitHubArchiveTooLarge
			}
		}
	}

	// Copy staging directory contents to final target directory
	if err := copyDirectoryContentsWithPerms(stagingDir, destClean, dirPerm, filePerm); err != nil {
		return fmt.Errorf("failed to materialize extracted repository snapshot: %w", err)
	}

	// Atomic cache creation: write to temporary staging directory, verify completeness, and rename
	if cacheDir != "" {
		tmpCacheDir := fmt.Sprintf("%s.tmp.%s", cacheDir, uuid.New().String())
		_ = os.RemoveAll(tmpCacheDir)
		if err := os.MkdirAll(tmpCacheDir, dirPerm); err == nil {
			if copyErr := copyDirectoryContentsWithPerms(stagingDir, tmpCacheDir, dirPerm, filePerm); copyErr == nil {
				if writeErr := os.WriteFile(filepath.Join(tmpCacheDir, ".complete"), []byte(resolvedSHA), filePerm); writeErr == nil {
					_ = os.RemoveAll(cacheDir)
					if renErr := os.Rename(tmpCacheDir, cacheDir); renErr != nil {
						// On Windows rename may fail across certain boundaries or existing dirs, fallback copy
						if copyErr2 := copyDirectoryContentsWithPerms(tmpCacheDir, cacheDir, dirPerm, filePerm); copyErr2 == nil {
							_ = os.WriteFile(filepath.Join(cacheDir, ".complete"), []byte(resolvedSHA), filePerm)
						}
					}
				}
			}
			_ = os.RemoveAll(tmpCacheDir)
		}
	}

	extractionSuccess = true
	return nil
}
