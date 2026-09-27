package services_test

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/textproto"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/services"
)

func createMultipartFileHeader(filename string, content []byte) *multipart.FileHeader {
	body := new(bytes.Buffer)
	writer := multipart.NewWriter(body)
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="files"; filename="%s"`, filename))
	part, err := writer.CreatePart(h)
	if err != nil {
		panic(err)
	}
	_, _ = part.Write(content)
	_ = writer.Close()

	reader := multipart.NewReader(body, writer.Boundary())
	form, err := reader.ReadForm(10 * 1024 * 1024)
	if err != nil {
		panic(err)
	}
	return form.File["files"][0]
}

func TestSourceService_IngestZip_Valid(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-source-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	svc := services.NewSourceService(nil, tempDir)
	ownerID := uuid.New()

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
	res, err := svc.IngestZip(context.Background(), ownerID, readerAt, int64(buf.Len()))
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.NotEqual(t, uuid.Nil, res.SourceID)
	assert.Equal(t, 2, res.FilesCount)
	require.NotNil(t, res.Detection)
	assert.Equal(t, "nodejs", res.Detection.Runtime)

	// Check path exists on disk
	path, err := svc.GetSourcePath(context.Background(), ownerID, res.SourceID)
	require.NoError(t, err)
	assert.DirExists(t, path)
	assert.FileExists(t, filepath.Join(path, "package.json"))

	// Cleanup
	err = svc.DeleteSource(context.Background(), ownerID, res.SourceID)
	require.NoError(t, err)
	assert.NoDirExists(t, path)
}

func TestSourceService_IngestZip_PathTraversal(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-source-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	svc := services.NewSourceService(nil, tempDir)
	ownerID := uuid.New()

	buf := new(bytes.Buffer)
	zipWriter := zip.NewWriter(buf)

	// Attempt path traversal via zip entry
	f, err := zipWriter.Create("../../../evil.txt")
	require.NoError(t, err)
	_, err = f.Write([]byte("malicious content"))
	require.NoError(t, err)

	require.NoError(t, zipWriter.Close())

	readerAt := bytes.NewReader(buf.Bytes())
	res, err := svc.IngestZip(context.Background(), ownerID, readerAt, int64(buf.Len()))
	require.Error(t, err)
	assert.Nil(t, res)
	assert.ErrorIs(t, err, services.ErrPathTraversalDetected)
}

func TestSourceService_BrowserDirectoryUpload_Normalization(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-source-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	svc := services.NewSourceService(nil, tempDir)
	ownerID := uuid.New()

	// Simulate browser HTML5 directory picker:
	// Files are sent with their directory prefix, e.g. "MyProject/package.json"
	files := []*multipart.FileHeader{
		createMultipartFileHeader("MyProject/package.json", []byte(`{"name":"my-project","dependencies":{"next":"14.0.0"}}`)),
		createMultipartFileHeader("MyProject/src/app/page.tsx", []byte(`export default function Page() { return <h1>Hello</h1>; }`)),
		createMultipartFileHeader("MyProject/src/components/Button.tsx", []byte(`export function Button() { return <button>Click</button>; }`)),
		createMultipartFileHeader("MyProject/Dockerfile", []byte(`FROM node:20-alpine`)),
		createMultipartFileHeader("MyProject/node_modules/fake/index.js", []byte(`// Should be excluded`)),
		createMultipartFileHeader("MyProject/.git/config", []byte(`[core]`)),
	}

	res, err := svc.IngestMultipartFiles(context.Background(), ownerID, files)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.NotEqual(t, uuid.Nil, res.SourceID)
	// Excluded .git and node_modules, so 4 files remain
	assert.Equal(t, 4, res.FilesCount)

	path, err := svc.GetSourcePath(context.Background(), ownerID, res.SourceID)
	require.NoError(t, err)
	assert.DirExists(t, path)

	// CRITICAL ASSERTION: Root of workspace MUST look like the project root, NOT contain MyProject/
	assert.FileExists(t, filepath.Join(path, "package.json"))
	assert.FileExists(t, filepath.Join(path, "Dockerfile"))
	assert.FileExists(t, filepath.Join(path, "src", "app", "page.tsx"))
	assert.FileExists(t, filepath.Join(path, "src", "components", "Button.tsx"))
	assert.NoDirExists(t, filepath.Join(path, "MyProject"))
	assert.NoDirExists(t, filepath.Join(path, "node_modules"))
	assert.NoDirExists(t, filepath.Join(path, ".git"))
}

func TestSourceService_BrowserDirectoryUpload_WithoutWrapper(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-source-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	svc := services.NewSourceService(nil, tempDir)
	ownerID := uuid.New()

	// Direct root upload (e.g. dragging loose files)
	files := []*multipart.FileHeader{
		createMultipartFileHeader("package.json", []byte(`{"name":"loose-files"}`)),
		createMultipartFileHeader("index.js", []byte(`console.log("direct");`)),
	}

	res, err := svc.IngestMultipartFiles(context.Background(), ownerID, files)
	require.NoError(t, err)
	require.NotNil(t, res)

	path, err := svc.GetSourcePath(context.Background(), ownerID, res.SourceID)
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(path, "package.json"))
	assert.FileExists(t, filepath.Join(path, "index.js"))
}

func TestSourceService_BrowserDirectoryUpload_PathTraversal(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-source-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	svc := services.NewSourceService(nil, tempDir)
	ownerID := uuid.New()

	files := []*multipart.FileHeader{
		createMultipartFileHeader("../../etc/passwd", []byte("root:x:0:0")),
	}

	res, err := svc.IngestMultipartFiles(context.Background(), ownerID, files)
	require.Error(t, err)
	assert.Nil(t, res)
	assert.ErrorIs(t, err, services.ErrPathTraversalDetected)
}

func TestSourceService_BrowserDirectoryUpload_SizeLimitOverflow(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-source-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	svc := services.NewSourceService(nil, tempDir)
	ownerID := uuid.New()

	// Create a simulated header indicating content larger than 100MB (101MB)
	// We can test this with a smaller limit or test the copyWithLimit directly
	// Let's create dummy 1MB chunk and repeat, or test overflow detection
	largeContent := make([]byte, 1024*1024) // 1MB
	files := []*multipart.FileHeader{
		createMultipartFileHeader("large1.bin", largeContent),
	}

	res, err := svc.IngestMultipartFiles(context.Background(), ownerID, files)
	require.NoError(t, err)
	assert.Equal(t, int64(1024*1024), res.TotalBytes)
}

func TestSourceService_BrowserDirectoryUpload_Exceeds100MB(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-source-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	svc := services.NewSourceService(nil, tempDir)
	ownerID := uuid.New()

	overflow := make([]byte, services.MaxUncompressedBytes+1024)
	files := []*multipart.FileHeader{
		createMultipartFileHeader("huge.bin", overflow),
	}

	res, err := svc.IngestMultipartFiles(context.Background(), ownerID, files)
	require.Error(t, err)
	assert.Nil(t, res)
	assert.ErrorIs(t, err, services.ErrArchiveTooLarge)
}

func TestSourceService_OwnershipIsolation(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-source-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	svc := services.NewSourceService(nil, tempDir)
	userA := uuid.New()
	userB := uuid.New()

	files := []*multipart.FileHeader{
		createMultipartFileHeader("app/package.json", []byte(`{"name":"project-a"}`)),
	}

	res, err := svc.IngestMultipartFiles(context.Background(), userA, files)
	require.NoError(t, err)
	require.NotNil(t, res)

	// User A can access
	pathA, err := svc.GetSourcePath(context.Background(), userA, res.SourceID)
	require.NoError(t, err)
	assert.NotEmpty(t, pathA)

	// User B cannot access
	_, err = svc.GetSourcePath(context.Background(), userB, res.SourceID)
	require.Error(t, err)
	assert.ErrorIs(t, err, services.ErrUnauthorizedSource)

	// User B cannot delete
	err = svc.DeleteSource(context.Background(), userB, res.SourceID)
	require.Error(t, err)
	assert.ErrorIs(t, err, services.ErrUnauthorizedSource)
	assert.DirExists(t, pathA)

	// User A can delete
	err = svc.DeleteSource(context.Background(), userA, res.SourceID)
	require.NoError(t, err)
	assert.NoDirExists(t, pathA)
}

func TestSourceService_GetSourcePath_NotFound(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-source-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	svc := services.NewSourceService(nil, tempDir)
	_, err = svc.GetSourcePath(context.Background(), uuid.New(), uuid.New())
	assert.ErrorIs(t, err, services.ErrSourceDirNotFound)
}

