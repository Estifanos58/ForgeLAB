package services_test

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/textproto"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/crypto"
	"github.com/forgelab/backend/internal/models"
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

type streamFile struct {
	field    string
	filename string
	content  []byte
}

func createMultipartStream(files ...streamFile) (*multipart.Reader, func()) {
	pr, pw := io.Pipe()
	writer := multipart.NewWriter(pw)

	done := make(chan struct{})
	go func() {
		defer pw.Close()
		defer writer.Close()
		defer close(done)

		for _, f := range files {
			h := make(textproto.MIMEHeader)
			fieldName := f.field
			if fieldName == "" {
				fieldName = "files"
			}
			h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, fieldName, f.filename))
			part, err := writer.CreatePart(h)
			if err != nil {
				return
			}
			if _, err := part.Write(f.content); err != nil {
				return
			}
		}
	}()

	cleanup := func() {
		_ = pr.Close()
		<-done
	}

	return multipart.NewReader(pr, writer.Boundary()), cleanup
}

func TestSourceService_IngestMultipartStream_ValidDirectory(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-stream-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	svc := services.NewSourceService(nil, tempDir)
	ownerID := uuid.New()

	reader, cleanup := createMultipartStream(
		streamFile{filename: "my-node-app/package.json", content: []byte(`{"name":"my-node-app","scripts":{"start":"node index.js"}}`)},
		streamFile{filename: "my-node-app/index.js", content: []byte(`console.log("hello world");`)},
	)
	defer cleanup()

	res, err := svc.IngestMultipartStream(context.Background(), ownerID, reader)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.NotEqual(t, uuid.Nil, res.SourceID)
	assert.Equal(t, 2, res.FilesCount)
	assert.Equal(t, services.SourceStatusProcessing, res.Status)

	// Workspace path on disk should be normalized (no my-node-app/ wrapper)
	path, err := svc.GetSourcePath(context.Background(), ownerID, res.SourceID)
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(path, "package.json"))
	assert.FileExists(t, filepath.Join(path, "index.js"))
	assert.NoDirExists(t, filepath.Join(path, "my-node-app"))

	// Wait briefly for background detection
	var status *services.SourceUploadResult
	for i := 0; i < 20; i++ {
		status, err = svc.GetSourceStatus(context.Background(), ownerID, res.SourceID)
		require.NoError(t, err)
		if status.Status == services.SourceStatusReady {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	assert.Equal(t, services.SourceStatusReady, status.Status)
	assert.Equal(t, "nodejs", status.Runtime)
}

func TestSourceService_IngestMultipartStream_NestedDirectories(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-stream-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	svc := services.NewSourceService(nil, tempDir)
	ownerID := uuid.New()

	reader, cleanup := createMultipartStream(
		streamFile{filename: "app/src/components/Button.tsx", content: []byte(`export const Button = () => null;`)},
		streamFile{filename: "app/src/lib/utils.ts", content: []byte(`export const add = (a: number, b: number) => a + b;`)},
		streamFile{filename: "app/public/assets/icon.svg", content: []byte(`<svg></svg>`)},
		streamFile{filename: "app/package.json", content: []byte(`{"name":"nested-app"}`)},
	)
	defer cleanup()

	res, err := svc.IngestMultipartStream(context.Background(), ownerID, reader)
	require.NoError(t, err)
	assert.Equal(t, 4, res.FilesCount)

	path, err := svc.GetSourcePath(context.Background(), ownerID, res.SourceID)
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(path, "src", "components", "Button.tsx"))
	assert.FileExists(t, filepath.Join(path, "src", "lib", "utils.ts"))
	assert.FileExists(t, filepath.Join(path, "public", "assets", "icon.svg"))
	assert.FileExists(t, filepath.Join(path, "package.json"))
}

func TestSourceService_IngestMultipartStream_IgnoredDirectories(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-stream-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	svc := services.NewSourceService(nil, tempDir)
	ownerID := uuid.New()

	reader, cleanup := createMultipartStream(
		streamFile{filename: "proj/package.json", content: []byte(`{"name":"app"}`)},
		streamFile{filename: "proj/node_modules/foo/index.js", content: []byte(`// ignored`)},
		streamFile{filename: "proj/.git/HEAD", content: []byte(`ref: refs/heads/main`)},
		streamFile{filename: "proj/.next/cache/turbopack.bin", content: []byte(`binary cache`)},
		streamFile{filename: "proj/dist/bundle.js", content: []byte(`bundled`)},
	)
	defer cleanup()

	res, err := svc.IngestMultipartStream(context.Background(), ownerID, reader)
	require.NoError(t, err)
	assert.Equal(t, 1, res.FilesCount) // Only package.json accepted

	path, err := svc.GetSourcePath(context.Background(), ownerID, res.SourceID)
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(path, "package.json"))
	assert.NoDirExists(t, filepath.Join(path, "node_modules"))
	assert.NoDirExists(t, filepath.Join(path, ".git"))
	assert.NoDirExists(t, filepath.Join(path, ".next"))
	assert.NoDirExists(t, filepath.Join(path, "dist"))
}

func TestSourceService_IngestMultipartStream_PathTraversal(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-stream-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	svc := services.NewSourceService(nil, tempDir)
	ownerID := uuid.New()

	reader, cleanup := createMultipartStream(
		streamFile{filename: "../../etc/shadow", content: []byte(`root:$6$xxx`)},
	)
	defer cleanup()

	res, err := svc.IngestMultipartStream(context.Background(), ownerID, reader)
	require.Error(t, err)
	assert.Nil(t, res)
	assert.ErrorIs(t, err, services.ErrPathTraversalDetected)

	// Confirm staging directory is purged
	uploadsDir := filepath.Join(tempDir, ".uploads")
	entries, _ := os.ReadDir(uploadsDir)
	assert.Empty(t, entries)
}

func TestSourceService_IngestMultipartStream_SizeLimitExceeded(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-stream-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	svc := services.NewSourceService(nil, tempDir)
	ownerID := uuid.New()

	overflow := make([]byte, services.MaxUncompressedBytes+1024)
	reader, cleanup := createMultipartStream(
		streamFile{filename: "huge.bin", content: overflow},
	)
	defer cleanup()

	res, err := svc.IngestMultipartStream(context.Background(), ownerID, reader)
	require.Error(t, err)
	assert.Nil(t, res)
	assert.ErrorIs(t, err, services.ErrArchiveTooLarge)

	// Staging dir should be cleaned up
	uploadsDir := filepath.Join(tempDir, ".uploads")
	entries, _ := os.ReadDir(uploadsDir)
	assert.Empty(t, entries)
}

func TestSourceService_IngestMultipartStream_ManyFiles(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-stream-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	svc := services.NewSourceService(nil, tempDir)
	ownerID := uuid.New()

	var files []streamFile
	files = append(files, streamFile{filename: "bulk/package.json", content: []byte(`{"name":"bulk"}`)})
	for i := 0; i < 100; i++ {
		files = append(files, streamFile{
			filename: fmt.Sprintf("bulk/src/module_%d/file_%d.txt", i/10, i),
			content:  []byte(fmt.Sprintf("content of file %d", i)),
		})
	}

	reader, cleanup := createMultipartStream(files...)
	defer cleanup()

	res, err := svc.IngestMultipartStream(context.Background(), ownerID, reader)
	require.NoError(t, err)
	assert.Equal(t, 101, res.FilesCount)

	path, err := svc.GetSourcePath(context.Background(), ownerID, res.SourceID)
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(path, "package.json"))
	assert.FileExists(t, filepath.Join(path, "src", "module_5", "file_55.txt"))
}

func TestSourceService_IngestMultipartStream_Empty(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-stream-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	svc := services.NewSourceService(nil, tempDir)
	ownerID := uuid.New()

	reader, cleanup := createMultipartStream(
		streamFile{filename: "app/node_modules/fake.js", content: []byte(`ignored`)},
	)
	defer cleanup()

	res, err := svc.IngestMultipartStream(context.Background(), ownerID, reader)
	require.Error(t, err)
	assert.Nil(t, res)
	assert.Contains(t, err.Error(), "no files")
}

func TestSourceService_IngestMultipartStream_ContextCancellation(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-stream-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	svc := services.NewSourceService(nil, tempDir)
	ownerID := uuid.New()

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel before or during stream
	cancel()

	reader, cleanup := createMultipartStream(
		streamFile{filename: "app/package.json", content: []byte(`{"name":"cancelled"}`)},
	)
	defer cleanup()

	res, err := svc.IngestMultipartStream(ctx, ownerID, reader)
	require.Error(t, err)
	assert.Nil(t, res)
	assert.ErrorIs(t, err, context.Canceled)

	// Confirm incomplete workspace was cleaned up
	uploadsDir := filepath.Join(tempDir, ".uploads")
	entries, _ := os.ReadDir(uploadsDir)
	assert.Empty(t, entries)
}

func TestSourceService_IngestMultipartStream_ArchiveZip(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-stream-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	svc := services.NewSourceService(nil, tempDir)
	ownerID := uuid.New()

	// Build zip archive in memory
	buf := new(bytes.Buffer)
	zipWriter := zip.NewWriter(buf)
	f, err := zipWriter.Create("archive-app/package.json")
	require.NoError(t, err)
	_, _ = f.Write([]byte(`{"name":"zip-app","scripts":{"start":"node index.js"}}`))
	f2, err := zipWriter.Create("archive-app/index.js")
	require.NoError(t, err)
	_, _ = f2.Write([]byte(`console.log("from zip");`))
	require.NoError(t, zipWriter.Close())

	reader, cleanup := createMultipartStream(
		streamFile{field: "archive", filename: "project.zip", content: buf.Bytes()},
	)
	defer cleanup()

	res, err := svc.IngestMultipartStream(context.Background(), ownerID, reader)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, 2, res.FilesCount)

	path, err := svc.GetSourcePath(context.Background(), ownerID, res.SourceID)
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(path, "package.json"))
	assert.FileExists(t, filepath.Join(path, "index.js"))
	assert.NoDirExists(t, filepath.Join(path, "archive-app"))
}

func TestSourceService_IngestMultipartStream_ArchiveTarGz(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-stream-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	svc := services.NewSourceService(nil, tempDir)
	ownerID := uuid.New()

	// Build tar.gz archive in memory
	buf := new(bytes.Buffer)
	gw := gzip.NewWriter(buf)
	tw := tar.NewWriter(gw)

	content := []byte(`{"name":"targz-app"}`)
	hdr := &tar.Header{
		Name: "targz-app/package.json",
		Mode: 0644,
		Size: int64(len(content)),
	}
	require.NoError(t, tw.WriteHeader(hdr))
	_, _ = tw.Write(content)
	require.NoError(t, tw.Close())
	require.NoError(t, gw.Close())

	reader, cleanup := createMultipartStream(
		streamFile{field: "archive", filename: "project.tar.gz", content: buf.Bytes()},
	)
	defer cleanup()

	res, err := svc.IngestMultipartStream(context.Background(), ownerID, reader)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, 1, res.FilesCount)

	path, err := svc.GetSourcePath(context.Background(), ownerID, res.SourceID)
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(path, "package.json"))
	assert.NoDirExists(t, filepath.Join(path, "targz-app"))
}

func TestSourceService_GetSourceStatus_ReadyAndOwnership(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-stream-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	svc := services.NewSourceService(nil, tempDir)
	ownerID := uuid.New()
	intruderID := uuid.New()

	reader, cleanup := createMultipartStream(
		streamFile{filename: "app/main.go", content: []byte("package main\nfunc main() {}")},
		streamFile{filename: "app/go.mod", content: []byte("module myapp\ngo 1.22")},
	)
	defer cleanup()

	res, err := svc.IngestMultipartStream(context.Background(), ownerID, reader)
	require.NoError(t, err)

	// Owner can get status
	status, err := svc.GetSourceStatus(context.Background(), ownerID, res.SourceID)
	require.NoError(t, err)
	assert.Equal(t, res.SourceID, status.SourceID)
	assert.Equal(t, 2, status.FilesCount)

	// Intruder gets ErrUnauthorizedSource
	_, err = svc.GetSourceStatus(context.Background(), intruderID, res.SourceID)
	assert.ErrorIs(t, err, services.ErrUnauthorizedSource)

	// Non-existent source gets ErrSourceDirNotFound
	_, err = svc.GetSourceStatus(context.Background(), ownerID, uuid.New())
	assert.ErrorIs(t, err, services.ErrSourceDirNotFound)
}

func TestSourceService_AgentSessionToken_EncryptionDecryptionOwnership(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-agent-token-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	// 32-byte key for AES-256
	cryptoKey := "12345678901234567890123456789012"
	encryptor, err := crypto.NewEncryptor(cryptoKey)
	require.NoError(t, err)

	svc := services.NewSourceService(nil, tempDir, encryptor)
	ownerID := uuid.New()
	intruderID := uuid.New()
	sourceID := uuid.New()

	rawSecretToken := "super-secret-agent-session-token-12345"

	// 1. Verify EncryptToken
	encrypted, err := svc.EncryptToken(rawSecretToken)
	require.NoError(t, err)
	assert.NotEmpty(t, encrypted)
	assert.NotEqual(t, []byte(rawSecretToken), encrypted)

	// 2. Save source with encrypted token
	src := &models.Source{
		ID:                    sourceID,
		OwnerID:               ownerID,
		SourceType:            models.SourceTypeLocalAgent,
		SourceReference:       "agent-source-ref-123",
		AgentID:               "test-agent-id",
		Fingerprint:           "abc123sha",
		EncryptedSessionToken: encrypted,
		Metadata: map[string]interface{}{
			"agent_id": "test-agent-id",
		},
	}
	err = svc.SaveSource(context.Background(), src)
	require.NoError(t, err)

	// 3. Owner can securely decrypt token
	decrypted, err := svc.GetDecryptedAgentToken(context.Background(), ownerID, sourceID)
	require.NoError(t, err)
	assert.Equal(t, rawSecretToken, decrypted)

	// Also verify alias GetAgentSessionToken
	decryptedAlias, err := svc.GetAgentSessionToken(context.Background(), ownerID, sourceID)
	require.NoError(t, err)
	assert.Equal(t, rawSecretToken, decryptedAlias)

	// 4. Intruder is rejected with ErrUnauthorizedSource
	_, err = svc.GetDecryptedAgentToken(context.Background(), intruderID, sourceID)
	assert.ErrorIs(t, err, services.ErrUnauthorizedSource)

	// 5. Non-existent source returns ErrSourceDirNotFound
	_, err = svc.GetDecryptedAgentToken(context.Background(), ownerID, uuid.New())
	assert.ErrorIs(t, err, services.ErrSourceDirNotFound)

	// 6. Source without encrypted token fails with descriptive error
	unencryptedSourceID := uuid.New()
	unencryptedSrc := &models.Source{
		ID:              unencryptedSourceID,
		OwnerID:         ownerID,
		SourceType:      models.SourceTypeLocalAgent,
		SourceReference: "unencrypted-ref",
	}
	err = svc.SaveSource(context.Background(), unencryptedSrc)
	require.NoError(t, err)

	_, err = svc.GetDecryptedAgentToken(context.Background(), ownerID, unencryptedSourceID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent session credential missing or expired")

	// 7. Verify JSON serialization never includes EncryptedSessionToken or raw secrets
	jsonBytes, err := json.Marshal(src)
	require.NoError(t, err)
	jsonStr := string(jsonBytes)
	assert.NotContains(t, jsonStr, rawSecretToken)
	assert.NotContains(t, jsonStr, "encrypted_session_token")
	assert.NotContains(t, jsonStr, "EncryptedSessionToken")
}

func TestSourceService_SaveAndGetSource(t *testing.T) {
	tempDir := t.TempDir()
	cryptoKey := "12345678901234567890123456789012"
	encryptor, err := crypto.NewEncryptor(cryptoKey)
	require.NoError(t, err)

	svc := services.NewSourceService(nil, tempDir, encryptor)
	ownerID := uuid.New()
	intruderID := uuid.New()
	sourceID := uuid.New()

	rawSecretToken := "my-secret-agent-token-xyz"
	encrypted, err := svc.EncryptToken(rawSecretToken)
	require.NoError(t, err)

	src := &models.Source{
		ID:                    sourceID,
		OwnerID:               ownerID,
		SourceType:            models.SourceTypeLocalAgent,
		SourceReference:       "source-ref-xyz",
		AgentID:               "agent-123",
		Fingerprint:           "fp-abc",
		EncryptedSessionToken: encrypted,
		Metadata: map[string]interface{}{
			"folder_name": "my-project",
		},
	}

	// 1. SaveSource
	err = svc.SaveSource(context.Background(), src)
	require.NoError(t, err)

	// 2. GetSource by owner returns matching record and preserves encrypted token
	retrieved, err := svc.GetSource(context.Background(), sourceID, ownerID)
	require.NoError(t, err)
	require.NotNil(t, retrieved)
	assert.Equal(t, sourceID, retrieved.ID)
	assert.Equal(t, ownerID, retrieved.OwnerID)
	assert.Equal(t, models.SourceTypeLocalAgent, retrieved.SourceType)
	assert.Equal(t, "source-ref-xyz", retrieved.SourceReference)
	assert.Equal(t, "agent-123", retrieved.AgentID)
	assert.Equal(t, "fp-abc", retrieved.Fingerprint)
	assert.Equal(t, encrypted, retrieved.EncryptedSessionToken)
	assert.Equal(t, "my-project", retrieved.Metadata["folder_name"])

	// 3. GetSource by intruder fails with ErrUnauthorizedSource
	_, err = svc.GetSource(context.Background(), sourceID, intruderID)
	assert.ErrorIs(t, err, services.ErrUnauthorizedSource)

	// 4. GetSource for non-existent source fails with ErrSourceDirNotFound
	_, err = svc.GetSource(context.Background(), uuid.New(), ownerID)
	assert.ErrorIs(t, err, services.ErrSourceDirNotFound)
}


