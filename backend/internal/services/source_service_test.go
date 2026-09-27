package services_test

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/services"
)

func TestSourceService_IngestZip_Valid(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-source-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	svc := services.NewSourceService(tempDir)

	// Create in-memory zip containing package.json
	buf := new(bytes.Buffer)
	zipWriter := zip.NewWriter(buf)

	f, err := zipWriter.Create("package.json")
	require.NoError(t, err)
	_, err = f.Write([]byte(`{"name":"my-node-app","scripts":{"start":"node index.js"}}`))
	require.NoError(t, err)

	f2, err := zipWriter.Create("index.js")
	require.NoError(t, err)
	_, err = f2.Write([]byte(`console.log("hello world");`))
	require.NoError(t, err)

	require.NoError(t, zipWriter.Close())

	// Ingest zip
	readerAt := bytes.NewReader(buf.Bytes())
	res, err := svc.IngestZip(context.Background(), readerAt, int64(buf.Len()))
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.NotEqual(t, uuid.Nil, res.SourceID)
	assert.Equal(t, 2, res.FilesCount)
	require.NotNil(t, res.Detection)
	assert.Equal(t, "nodejs", res.Detection.Runtime)

	// Check path exists on disk
	path, err := svc.GetSourcePath(res.SourceID)
	require.NoError(t, err)
	assert.DirExists(t, path)
	assert.FileExists(t, filepath.Join(path, "package.json"))

	// Cleanup
	err = svc.DeleteSource(res.SourceID)
	require.NoError(t, err)
	assert.NoDirExists(t, path)
}

func TestSourceService_IngestZip_PathTraversal(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-source-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	svc := services.NewSourceService(tempDir)

	buf := new(bytes.Buffer)
	zipWriter := zip.NewWriter(buf)

	// Attempt path traversal via zip entry
	f, err := zipWriter.Create("../../../evil.txt")
	require.NoError(t, err)
	_, err = f.Write([]byte("malicious content"))
	require.NoError(t, err)

	require.NoError(t, zipWriter.Close())

	readerAt := bytes.NewReader(buf.Bytes())
	res, err := svc.IngestZip(context.Background(), readerAt, int64(buf.Len()))
	require.Error(t, err)
	assert.Nil(t, res)
	assert.ErrorIs(t, err, services.ErrPathTraversalDetected)
}

func TestSourceService_GetSourcePath_NotFound(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-source-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	svc := services.NewSourceService(tempDir)
	_, err = svc.GetSourcePath(uuid.New())
	assert.ErrorIs(t, err, services.ErrSourceDirNotFound)
}
