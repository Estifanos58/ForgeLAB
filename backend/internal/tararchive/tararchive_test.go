package tararchive

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/dockerignore"
)

func TestWriteAndValidateBuildContext_RoundTripAndFiltering(t *testing.T) {
	ctxDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(ctxDir, "Dockerfile"), []byte("FROM alpine:3.21\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(ctxDir, "app.go"), []byte("package main\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(ctxDir, ".env"), []byte("SECRET=topsecret\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(ctxDir, ".env.production"), []byte("TOKEN=abc\n"), 0600))

	// Valid directory whose name starts with ".." (must NOT be misclassified as parent traversal)
	dotdotDir := filepath.Join(ctxDir, "..cache_dir")
	require.NoError(t, os.MkdirAll(dotdotDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dotdotDir, "meta.txt"), []byte("ok"), 0644))

	// Ignored directory
	nodeModules := filepath.Join(ctxDir, "node_modules", "pkg")
	require.NoError(t, os.MkdirAll(nodeModules, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(nodeModules, "index.js"), []byte("ignored"), 0644))

	// Even an explicit negation rule in .dockerignore must NOT leak mandatory secrets
	matcher := dockerignore.NewDockerignoreMatcher([]string{"node_modules", "!.env", "!.env.production"})

	tmpFile, err := CreateCompletedArchiveFile(context.Background(), t.TempDir(), BuildContextOptions{
		BuildContextDir: ctxDir,
		Matcher:         matcher,
		VirtualFiles: map[string][]byte{
			"Dockerfile.forgelab": []byte("FROM alpine:3.21\nCMD [\"true\"]\n"),
		},
	}, "Dockerfile")
	require.NoError(t, err)
	defer func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmpFile.Name())
	}()

	entries := make(map[string]bool)
	tr := tar.NewReader(tmpFile)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		entries[hdr.Name] = true
		assert.False(t, strings.Contains(hdr.Name, ".env"), "mandatory secret leaked: %s", hdr.Name)
		assert.False(t, strings.Contains(hdr.Name, "node_modules"), "ignored directory leaked: %s", hdr.Name)
	}

	assert.True(t, entries["Dockerfile"])
	assert.True(t, entries["Dockerfile.forgelab"])
	assert.True(t, entries["app.go"])
	assert.True(t, entries["..cache_dir/meta.txt"], "valid directory starting with '..' must be archived")
}

func TestWriteBuildContext_SymlinkContainment(t *testing.T) {
	parentDir := t.TempDir()
	ctxDir := filepath.Join(parentDir, "service")
	outsideDir := filepath.Join(parentDir, "outside")
	require.NoError(t, os.MkdirAll(ctxDir, 0755))
	require.NoError(t, os.MkdirAll(outsideDir, 0755))

	require.NoError(t, os.WriteFile(filepath.Join(ctxDir, "Dockerfile"), []byte("FROM alpine:3.21\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(ctxDir, "target.txt"), []byte("inside"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(outsideDir, "secret.txt"), []byte("outside"), 0644))

	// Test valid internal relative symlink first
	internalLink := filepath.Join(ctxDir, "internal_link.txt")
	if err := os.Symlink("target.txt", internalLink); err != nil {
		t.Skipf("os.Symlink not permitted in this environment: %v", err)
	}

	var buf bytes.Buffer
	_, _, err := WriteBuildContext(context.Background(), &buf, BuildContextOptions{
		BuildContextDir: ctxDir,
	})
	require.NoError(t, err)

	tr := tar.NewReader(&buf)
	var foundInternalLink bool
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		if hdr.Name == "internal_link.txt" {
			foundInternalLink = true
			assert.Equal(t, byte(tar.TypeSymlink), hdr.Typeflag)
			assert.Equal(t, "target.txt", hdr.Linkname)
		}
	}
	assert.True(t, foundInternalLink, "internal relative symlink should be preserved in TAR")

	// Broken symlink pointing outside ctxDir must ALSO be caught via ancestor canonicalization
	brokenEscapeLink := filepath.Join(ctxDir, "broken_escape")
	require.NoError(t, os.Symlink(filepath.Join(outsideDir, "nonexistent.txt"), brokenEscapeLink))

	_, _, err = WriteBuildContext(context.Background(), io.Discard, BuildContextOptions{
		BuildContextDir: ctxDir,
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSymlinkEscape), "expected ErrSymlinkEscape for broken escaping symlink, got: %v", err)
}

func TestValidateTarArchive_RejectsTraversalAndMissingDockerfile(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "../../etc/passwd",
		Mode:     0644,
		Size:     4,
		Typeflag: tar.TypeReg,
	}))
	_, err := tw.Write([]byte("evil"))
	require.NoError(t, err)
	require.NoError(t, tw.Close())

	err = ValidateTarArchive(bytes.NewReader(buf.Bytes()), "Dockerfile")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrTarCorrupted))

	var okBuf bytes.Buffer
	tw2 := tar.NewWriter(&okBuf)
	require.NoError(t, tw2.WriteHeader(&tar.Header{
		Name:     "main.go",
		Mode:     0644,
		Size:     2,
		Typeflag: tar.TypeReg,
	}))
	_, err = tw2.Write([]byte("hi"))
	require.NoError(t, err)
	require.NoError(t, tw2.Close())

	err = ValidateTarArchive(bytes.NewReader(okBuf.Bytes()), "Dockerfile")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrDockerfileMissing))
}
