package docker

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/discovery"
	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/security"
)

func TestResolveSourceDirectory_BuildReproducibility(t *testing.T) {
	ctx := context.Background()
	srcDir := t.TempDir()
	workDir := t.TempDir()

	// Create sample project files
	err := os.WriteFile(filepath.Join(srcDir, "index.js"), []byte("console.log('hello v1');"), 0644)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(srcDir, "package.json"), []byte(`{"name": "test-app"}`), 0644)
	require.NoError(t, err)

	fp1, err := discovery.ComputeDirectoryContentFingerprintWithContext(ctx, srcDir)
	require.NoError(t, err)
	require.NotEmpty(t, fp1)

	pathValidator := security.NewPathValidator([]string{srcDir})
	engine := &Engine{
		workDir:        workDir,
		pathValidator:  pathValidator,
		localBuildMode: "direct",
	}

	project := &models.Project{
		SourceType:     models.SourceTypeLocalDirectory,
		RepositoryPath: srcDir,
	}

	t.Run("succeeds when source revision fingerprint matches", func(t *testing.T) {
		var logs []string
		emitLog := func(phase, stream, message string) {
			logs = append(logs, message)
		}

		buildDir, cleanupDir, err := engine.resolveSourceDirectory(ctx, project, "exec-1", emitLog, fp1)
		require.NoError(t, err)
		assert.Equal(t, srcDir, buildDir)
		assert.Empty(t, cleanupDir)

		foundMatchLog := false
		for _, l := range logs {
			if assert.ObjectsAreEqual(l, "Verified source content matches reviewed revision "+fp1+".") {
				foundMatchLog = true
				break
			}
		}
		assert.True(t, foundMatchLog, "Expected match confirmation log")
	})

	t.Run("fails closed when source content has changed since revision was generated", func(t *testing.T) {
		// Modify file to change content fingerprint
		err := os.WriteFile(filepath.Join(srcDir, "index.js"), []byte("console.log('hello v2 altered');"), 0644)
		require.NoError(t, err)

		var logs []string
		emitLog := func(phase, stream, message string) {
			logs = append(logs, message)
		}

		// Use original fp1 which no longer matches
		_, _, err = engine.resolveSourceDirectory(ctx, project, "exec-2", emitLog, fp1)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "source content changed since deployment was created")
		assert.Contains(t, err.Error(), fp1)
	})

	t.Run("succeeds with new fingerprint after modification", func(t *testing.T) {
		fp2, err := discovery.ComputeDirectoryContentFingerprintWithContext(ctx, srcDir)
		require.NoError(t, err)
		require.NotEqual(t, fp1, fp2)

		var logs []string
		emitLog := func(phase, stream, message string) {
			logs = append(logs, message)
		}

		buildDir, cleanupDir, err := engine.resolveSourceDirectory(ctx, project, "exec-3", emitLog, fp2)
		require.NoError(t, err)
		assert.Equal(t, srcDir, buildDir)
		assert.Empty(t, cleanupDir)
	})
}
