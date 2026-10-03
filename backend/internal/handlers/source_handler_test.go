package handlers_test

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/agent"
	"github.com/forgelab/backend/internal/handlers"
	"github.com/forgelab/backend/internal/security"
	"github.com/forgelab/backend/internal/services"
)

func createMultipartFormData(fieldname, filename string, content []byte) (*bytes.Buffer, string) {
	body := new(bytes.Buffer)
	writer := multipart.NewWriter(body)
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, fieldname, filename))
	part, _ := writer.CreatePart(h)
	_, _ = part.Write(content)
	_ = writer.Close()
	return body, writer.FormDataContentType()
}

func TestSourceHandler_Upload_AuthenticationRequired(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-sh-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	sourceSvc := services.NewSourceService(nil, tempDir)
	handler := handlers.NewSourceHandler(sourceSvc, nil)

	body, contentType := createMultipartFormData("files", "MyProject/package.json", []byte(`{"name":"test"}`))
	req := httptest.NewRequest(http.MethodPost, "/api/sources/upload", body)
	req.Header.Set("Content-Type", contentType)
	// No user in context!
	rec := httptest.NewRecorder()

	handler.Upload(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestSourceHandler_Upload_Authenticated_BrowserDirectory(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-sh-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	sourceSvc := services.NewSourceService(nil, tempDir)
	handler := handlers.NewSourceHandler(sourceSvc, nil)
	testUser := uuid.New()

	body, contentType := createMultipartFormData("files", "MyProject/package.json", []byte(`{"name":"test","scripts":{"start":"node index.js"}}`))
	req := httptest.NewRequest(http.MethodPost, "/api/sources/upload", body)
	req.Header.Set("Content-Type", contentType)
	req = req.WithContext(withTestUser(req.Context(), testUser))
	rec := httptest.NewRecorder()

	handler.Upload(rec, req)
	assert.Equal(t, http.StatusCreated, rec.Code)

	var res services.SourceUploadResult
	err = json.NewDecoder(rec.Body).Decode(&res)
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, res.SourceID)

	// Verify the wrapper directory was normalized
	uploadedPath := filepath.Join(tempDir, res.SourceID.String())
	assert.FileExists(t, filepath.Join(uploadedPath, "package.json"))
	assert.NoDirExists(t, filepath.Join(uploadedPath, "MyProject"))
}

func TestSourceHandler_Delete_AuthenticationRequired(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-sh-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	sourceSvc := services.NewSourceService(nil, tempDir)
	handler := handlers.NewSourceHandler(sourceSvc, nil)

	req := httptest.NewRequest(http.MethodDelete, "/api/sources/"+uuid.New().String(), nil)
	rec := httptest.NewRecorder()

	handler.Delete(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestSourceHandler_Delete_Authenticated_OwnerSuccess(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-sh-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	sourceSvc := services.NewSourceService(nil, tempDir)
	handler := handlers.NewSourceHandler(sourceSvc, nil)
	ownerID := uuid.New()

	body, contentType := createMultipartFormData("files", "package.json", []byte(`{"name":"test"}`))
	uploadReq := httptest.NewRequest(http.MethodPost, "/api/sources/upload", body)
	uploadReq.Header.Set("Content-Type", contentType)
	uploadReq = uploadReq.WithContext(withTestUser(uploadReq.Context(), ownerID))
	uploadRec := httptest.NewRecorder()
	handler.Upload(uploadRec, uploadReq)
	require.Equal(t, http.StatusCreated, uploadRec.Code)

	var res services.SourceUploadResult
	err = json.NewDecoder(uploadRec.Body).Decode(&res)
	require.NoError(t, err)

	sourceDir := filepath.Join(tempDir, res.SourceID.String())
	assert.DirExists(t, sourceDir)

	r := chi.NewRouter()
	r.Delete("/api/sources/{id}", handler.Delete)

	req := httptest.NewRequest(http.MethodDelete, "/api/sources/"+res.SourceID.String(), nil)
	req = req.WithContext(withTestUser(req.Context(), ownerID))
	rec := httptest.NewRecorder()

	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.NoDirExists(t, sourceDir)
}

func TestSourceHandler_Delete_Authenticated_OtherUserForbidden(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-sh-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	sourceSvc := services.NewSourceService(nil, tempDir)
	handler := handlers.NewSourceHandler(sourceSvc, nil)
	ownerID := uuid.New()
	attackerID := uuid.New()

	body, contentType := createMultipartFormData("files", "package.json", []byte(`{"name":"test"}`))
	uploadReq := httptest.NewRequest(http.MethodPost, "/api/sources/upload", body)
	uploadReq.Header.Set("Content-Type", contentType)
	uploadReq = uploadReq.WithContext(withTestUser(uploadReq.Context(), ownerID))
	uploadRec := httptest.NewRecorder()
	handler.Upload(uploadRec, uploadReq)
	require.Equal(t, http.StatusCreated, uploadRec.Code)

	var res services.SourceUploadResult
	err = json.NewDecoder(uploadRec.Body).Decode(&res)
	require.NoError(t, err)

	sourceDir := filepath.Join(tempDir, res.SourceID.String())
	assert.DirExists(t, sourceDir)

	r := chi.NewRouter()
	r.Delete("/api/sources/{id}", handler.Delete)

	req := httptest.NewRequest(http.MethodDelete, "/api/sources/"+res.SourceID.String(), nil)
	req = req.WithContext(withTestUser(req.Context(), attackerID))
	rec := httptest.NewRecorder()

	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.DirExists(t, sourceDir)
}

func TestSourceHandler_Upload_ContentLength_TooLarge(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-sh-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	sourceSvc := services.NewSourceService(nil, tempDir)
	handler := handlers.NewSourceHandler(sourceSvc, nil)
	ownerID := uuid.New()

	body, contentType := createMultipartFormData("files", "package.json", []byte(`{"name":"test"}`))
	req := httptest.NewRequest(http.MethodPost, "/api/sources/upload", body)
	req.Header.Set("Content-Type", contentType)
	req.ContentLength = handlers.MaxUploadBytes + 1024
	req = req.WithContext(withTestUser(req.Context(), ownerID))
	rec := httptest.NewRecorder()

	handler.Upload(rec, req)
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	assert.Contains(t, rec.Body.String(), "upload size exceeds")
}

func TestSourceHandler_Upload_Stream_TooLarge(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-sh-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	sourceSvc := services.NewSourceService(nil, tempDir)
	handler := handlers.NewSourceHandler(sourceSvc, nil)
	ownerID := uuid.New()

	// 106MB content streamed
	overflow := make([]byte, handlers.MaxUploadBytes+1024)
	body, contentType := createMultipartFormData("files", "large.bin", overflow)

	req := httptest.NewRequest(http.MethodPost, "/api/sources/upload", body)
	req.Header.Set("Content-Type", contentType)
	req.ContentLength = -1 // Chunked / unknown length so it tests MaxBytesReader
	req = req.WithContext(withTestUser(req.Context(), ownerID))
	rec := httptest.NewRecorder()

	handler.Upload(rec, req)
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	assert.Contains(t, rec.Body.String(), "upload size exceeds")
}

func TestSourceHandler_GetStatus_Authenticated_OwnerSuccess(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-sh-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	sourceSvc := services.NewSourceService(nil, tempDir)
	handler := handlers.NewSourceHandler(sourceSvc, nil)
	ownerID := uuid.New()

	body, contentType := createMultipartFormData("files", "package.json", []byte(`{"name":"test"}`))
	uploadReq := httptest.NewRequest(http.MethodPost, "/api/sources/upload", body)
	uploadReq.Header.Set("Content-Type", contentType)
	uploadReq = uploadReq.WithContext(withTestUser(uploadReq.Context(), ownerID))
	uploadRec := httptest.NewRecorder()
	handler.Upload(uploadRec, uploadReq)
	require.Equal(t, http.StatusCreated, uploadRec.Code)

	var uploadRes services.SourceUploadResult
	err = json.NewDecoder(uploadRec.Body).Decode(&uploadRes)
	require.NoError(t, err)

	r := chi.NewRouter()
	r.Get("/api/sources/{id}", handler.GetStatus)

	req := httptest.NewRequest(http.MethodGet, "/api/sources/"+uploadRes.SourceID.String(), nil)
	req = req.WithContext(withTestUser(req.Context(), ownerID))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var statusRes services.SourceUploadResult
	err = json.NewDecoder(rec.Body).Decode(&statusRes)
	require.NoError(t, err)
	assert.Equal(t, uploadRes.SourceID, statusRes.SourceID)
	assert.NotEmpty(t, statusRes.Status)
}

func TestSourceHandler_GetStatus_Authenticated_OtherUserForbidden(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-sh-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	sourceSvc := services.NewSourceService(nil, tempDir)
	handler := handlers.NewSourceHandler(sourceSvc, nil)
	ownerID := uuid.New()
	attackerID := uuid.New()

	body, contentType := createMultipartFormData("files", "package.json", []byte(`{"name":"test"}`))
	uploadReq := httptest.NewRequest(http.MethodPost, "/api/sources/upload", body)
	uploadReq.Header.Set("Content-Type", contentType)
	uploadReq = uploadReq.WithContext(withTestUser(uploadReq.Context(), ownerID))
	uploadRec := httptest.NewRecorder()
	handler.Upload(uploadRec, uploadReq)
	require.Equal(t, http.StatusCreated, uploadRec.Code)

	var uploadRes services.SourceUploadResult
	err = json.NewDecoder(uploadRec.Body).Decode(&uploadRes)
	require.NoError(t, err)

	r := chi.NewRouter()
	r.Get("/api/sources/{id}", handler.GetStatus)

	req := httptest.NewRequest(http.MethodGet, "/api/sources/"+uploadRes.SourceID.String(), nil)
	req = req.WithContext(withTestUser(req.Context(), attackerID))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestSourceHandler_GetStatus_NotFound(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-sh-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	sourceSvc := services.NewSourceService(nil, tempDir)
	handler := handlers.NewSourceHandler(sourceSvc, nil)
	userID := uuid.New()

	r := chi.NewRouter()
	r.Get("/api/sources/{id}", handler.GetStatus)

	req := httptest.NewRequest(http.MethodGet, "/api/sources/"+uuid.New().String(), nil)
	req = req.WithContext(withTestUser(req.Context(), userID))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestSourceHandler_GetStatus_Unauthenticated(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-sh-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	sourceSvc := services.NewSourceService(nil, tempDir)
	handler := handlers.NewSourceHandler(sourceSvc, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/sources/"+uuid.New().String(), nil)
	rec := httptest.NewRecorder()
	handler.GetStatus(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestSourceHandler_GetStatus_InvalidUUID(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-sh-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	sourceSvc := services.NewSourceService(nil, tempDir)
	handler := handlers.NewSourceHandler(sourceSvc, nil)
	userID := uuid.New()

	r := chi.NewRouter()
	r.Get("/api/sources/{id}", handler.GetStatus)

	req := httptest.NewRequest(http.MethodGet, "/api/sources/not-a-valid-uuid", nil)
	req = req.WithContext(withTestUser(req.Context(), userID))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestSourceHandler_Upload_ContextCancellation_Cleanup(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-sh-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	sourceSvc := services.NewSourceService(nil, tempDir)
	handler := handlers.NewSourceHandler(sourceSvc, nil)
	ownerID := uuid.New()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Pre-cancelled context simulates client connection drop/abort

	body, contentType := createMultipartFormData("files", "package.json", []byte(`{"name":"cancelled"}`))
	req := httptest.NewRequest(http.MethodPost, "/api/sources/upload", body)
	req.Header.Set("Content-Type", contentType)
	req = req.WithContext(withTestUser(ctx, ownerID))
	rec := httptest.NewRecorder()

	handler.Upload(rec, req)
	// Connection cancelled by client, handler returns without panic and cleans up
	uploadsDir := filepath.Join(tempDir, ".uploads")
	entries, _ := os.ReadDir(uploadsDir)
	assert.Empty(t, entries)
}

func TestSourceHandler_Upload_Archive_Zip(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-sh-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	sourceSvc := services.NewSourceService(nil, tempDir)
	handler := handlers.NewSourceHandler(sourceSvc, nil)
	ownerID := uuid.New()

	buf := new(bytes.Buffer)
	zw := zip.NewWriter(buf)
	f, err := zw.Create("app/package.json")
	require.NoError(t, err)
	_, _ = f.Write([]byte(`{"name":"zip-upload"}`))
	require.NoError(t, zw.Close())

	body, contentType := createMultipartFormData("archive", "project.zip", buf.Bytes())
	req := httptest.NewRequest(http.MethodPost, "/api/sources/upload", body)
	req.Header.Set("Content-Type", contentType)
	req = req.WithContext(withTestUser(req.Context(), ownerID))
	rec := httptest.NewRecorder()

	handler.Upload(rec, req)
	assert.Equal(t, http.StatusCreated, rec.Code)

	var res services.SourceUploadResult
	err = json.NewDecoder(rec.Body).Decode(&res)
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, res.SourceID)
	assert.Equal(t, 1, res.FilesCount)

	// Confirm normalized root
	uploadedPath := filepath.Join(tempDir, res.SourceID.String())
	assert.FileExists(t, filepath.Join(uploadedPath, "package.json"))
	assert.NoDirExists(t, filepath.Join(uploadedPath, "app"))
}

func TestSourceHandler_Upload_Archive_TarGz(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-sh-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	sourceSvc := services.NewSourceService(nil, tempDir)
	handler := handlers.NewSourceHandler(sourceSvc, nil)
	ownerID := uuid.New()

	buf := new(bytes.Buffer)
	gw := gzip.NewWriter(buf)
	tw := tar.NewWriter(gw)
	content := []byte(`{"name":"targz-upload"}`)
	hdr := &tar.Header{
		Name: "app/package.json",
		Mode: 0644,
		Size: int64(len(content)),
	}
	require.NoError(t, tw.WriteHeader(hdr))
	_, _ = tw.Write(content)
	require.NoError(t, tw.Close())
	require.NoError(t, gw.Close())

	body, contentType := createMultipartFormData("archive", "project.tar.gz", buf.Bytes())
	req := httptest.NewRequest(http.MethodPost, "/api/sources/upload", body)
	req.Header.Set("Content-Type", contentType)
	req = req.WithContext(withTestUser(req.Context(), ownerID))
	rec := httptest.NewRecorder()

	handler.Upload(rec, req)
	assert.Equal(t, http.StatusCreated, rec.Code)

	var res services.SourceUploadResult
	err = json.NewDecoder(rec.Body).Decode(&res)
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, res.SourceID)
	assert.Equal(t, 1, res.FilesCount)

	uploadedPath := filepath.Join(tempDir, res.SourceID.String())
	assert.FileExists(t, filepath.Join(uploadedPath, "package.json"))
	assert.NoDirExists(t, filepath.Join(uploadedPath, "app"))
}

func TestSourceHandler_ValidateLocalPath_Unauthorized(t *testing.T) {
	handler := handlers.NewSourceHandler(nil, nil)
	body := bytes.NewBufferString(`{"repository_path":"/some/path"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/sources/local/validate", body)
	rec := httptest.NewRecorder()

	handler.ValidateLocalPath(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestSourceHandler_ValidateLocalPath_MissingPath(t *testing.T) {
	handler := handlers.NewSourceHandler(nil, nil)
	body := bytes.NewBufferString(`{"repository_path":""}`)
	req := httptest.NewRequest(http.MethodPost, "/api/sources/local/validate", body)
	req = req.WithContext(withTestUser(req.Context(), uuid.New()))
	rec := httptest.NewRecorder()

	handler.ValidateLocalPath(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestSourceHandler_ValidateLocalPath_NonExistent(t *testing.T) {
	validator := security.NewPathValidator(nil)
	handler := handlers.NewSourceHandler(nil, validator)

	body := bytes.NewBufferString(`{"repository_path":"/nonexistent/directory/path/123"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/sources/local/validate", body)
	req = req.WithContext(withTestUser(req.Context(), uuid.New()))
	rec := httptest.NewRecorder()

	handler.ValidateLocalPath(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestSourceHandler_ValidateLocalPath_Success_WithDockerignoreAndDetector(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab_val_test_*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	// Create a Next.js style project structure
	_ = os.MkdirAll(filepath.Join(tempDir, "src"), 0755)
	_ = os.MkdirAll(filepath.Join(tempDir, "node_modules", "nested"), 0755)
	_ = os.WriteFile(filepath.Join(tempDir, "src", "index.ts"), []byte("console.log('hi');"), 0644)
	_ = os.WriteFile(filepath.Join(tempDir, "node_modules", "nested", "huge.js"), []byte("huge"), 0644)
	_ = os.WriteFile(filepath.Join(tempDir, "package.json"), []byte(`{"name":"my-next-app","dependencies":{"next":"14.0.0"}}`), 0644)
	_ = os.WriteFile(filepath.Join(tempDir, "next.config.js"), []byte(`module.exports = {};`), 0644)
	_ = os.WriteFile(filepath.Join(tempDir, ".dockerignore"), []byte("node_modules\n"), 0644)

	validator := security.NewPathValidator([]string{tempDir})
	handler := handlers.NewSourceHandler(nil, validator)

	payload, _ := json.Marshal(map[string]string{"repository_path": tempDir})
	req := httptest.NewRequest(http.MethodPost, "/api/sources/local/validate", bytes.NewBuffer(payload))
	req = req.WithContext(withTestUser(req.Context(), uuid.New()))
	rec := httptest.NewRecorder()

	handler.ValidateLocalPath(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	var res handlers.ValidateLocalPathResponse
	err = json.NewDecoder(rec.Body).Decode(&res)
	require.NoError(t, err)

	assert.True(t, res.Valid)
	assert.Equal(t, "nextjs", res.Runtime)
	assert.Equal(t, "Next.js", res.Framework)
	assert.Equal(t, 3000, res.SuggestedPort)
	assert.Equal(t, "auto", res.BuildStrategy)
	// node_modules excluded: package.json, next.config.js, .dockerignore, src/index.ts = 4 files
	assert.Equal(t, 4, res.FilesCount)
	assert.True(t, res.TotalBytes > 0)
}

func TestSourceHandler_ValidateLocalPath_AsyncPolling(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-async-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	_ = os.WriteFile(filepath.Join(tempDir, "package.json"), []byte(`{"name":"test-app"}`), 0644)

	validator := security.NewPathValidator([]string{tempDir})
	handler := handlers.NewSourceHandler(nil, validator)

	testUserID := uuid.New()

	// 1. Request async validation
	payload, _ := json.Marshal(map[string]interface{}{
		"repository_path": tempDir,
		"async":           true,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/sources/local/validate", bytes.NewBuffer(payload))
	req = req.WithContext(withTestUser(req.Context(), testUserID))
	rec := httptest.NewRecorder()

	handler.ValidateLocalPath(rec, req)
	assert.Equal(t, http.StatusAccepted, rec.Code)

	var res handlers.ValidateLocalPathResponse
	err = json.NewDecoder(rec.Body).Decode(&res)
	require.NoError(t, err)
	assert.NotEmpty(t, res.SessionID)
	assert.Equal(t, "scanning", res.Status)

	// 2. Poll session status until ready
	var pollMap map[string]interface{}
	for i := 0; i < 20; i++ {
		time.Sleep(50 * time.Millisecond)

		pollReq := httptest.NewRequest(http.MethodGet, "/api/sources/local/validate/"+res.SessionID, nil)
		pollReq = pollReq.WithContext(withTestUser(pollReq.Context(), testUserID))

		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("sessionId", res.SessionID)
		pollReq = pollReq.WithContext(context.WithValue(pollReq.Context(), chi.RouteCtxKey, rctx))

		pollRec := httptest.NewRecorder()
		handler.GetLocalValidationStatus(pollRec, pollReq)
		assert.Equal(t, http.StatusOK, pollRec.Code)

		err = json.NewDecoder(pollRec.Body).Decode(&pollMap)
		require.NoError(t, err)

		if pollMap["status"] == "ready" {
			break
		}
	}

	assert.Equal(t, "ready", pollMap["status"])
	assert.NotNil(t, pollMap["result"])
}

func TestSourceHandler_ValidateAgentSession_SourceAwareAuth(t *testing.T) {
	tempDir := t.TempDir()
	sourceService := services.NewSourceService(nil, tempDir, nil)
	handler := handlers.NewSourceHandler(sourceService, nil)

	sm := agent.GetGlobalSessionManager()
	testUserID := uuid.New()
	agentID := "test-agent-validate-handler"
	authSession, err := sm.CreateSession(testUserID, agentID, 10*time.Minute)
	require.NoError(t, err)

	sourceUUID := uuid.New()
	_, err = sm.BindSource(authSession.Token, sourceUUID, "my-repo", agentID)
	require.NoError(t, err)

	// Mark consumed
	require.NoError(t, sm.MarkConsumed(authSession.ID))

	// 1. Validate with correct source_id -> succeeds even though consumed
	payload1, _ := json.Marshal(map[string]string{
		"token":     authSession.Token,
		"source_id": sourceUUID.String(),
		"agent_id":  agentID,
	})
	req1 := httptest.NewRequest(http.MethodPost, "/api/sources/agent/session/validate", bytes.NewReader(payload1))
	req1.Header.Set("Content-Type", "application/json")
	rec1 := httptest.NewRecorder()
	handler.ValidateAgentSession(rec1, req1)
	assert.Equal(t, http.StatusOK, rec1.Code)

	var res1 map[string]interface{}
	err = json.NewDecoder(rec1.Body).Decode(&res1)
	require.NoError(t, err)
	assert.Equal(t, true, res1["valid"])
	assert.Equal(t, sourceUUID.String(), res1["source_id"])

	// 2. Validate with different source_id -> rejected
	otherSourceUUID := uuid.New()
	payload2, _ := json.Marshal(map[string]string{
		"token":     authSession.Token,
		"source_id": otherSourceUUID.String(),
		"agent_id":  agentID,
	})
	req2 := httptest.NewRequest(http.MethodPost, "/api/sources/agent/session/validate", bytes.NewReader(payload2))
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	handler.ValidateAgentSession(rec2, req2)
	assert.Equal(t, http.StatusUnauthorized, rec2.Code)

	// 3. Validate without source_id -> rejected (consumed session)
	payload3, _ := json.Marshal(map[string]string{
		"token":    authSession.Token,
		"agent_id": agentID,
	})
	req3 := httptest.NewRequest(http.MethodPost, "/api/sources/agent/session/validate", bytes.NewReader(payload3))
	req3.Header.Set("Content-Type", "application/json")
	rec3 := httptest.NewRecorder()
	handler.ValidateAgentSession(rec3, req3)
	assert.Equal(t, http.StatusUnauthorized, rec3.Code)

	// 4. Expired token -> rejected
	expiredSession, err := sm.CreateSession(testUserID, agentID, 10*time.Minute)
	require.NoError(t, err)
	expiredSession.ExpiresAt = time.Now().Add(-1 * time.Minute)
	payload4, _ := json.Marshal(map[string]string{
		"token":     expiredSession.Token,
		"source_id": sourceUUID.String(),
		"agent_id":  agentID,
	})
	req4 := httptest.NewRequest(http.MethodPost, "/api/sources/agent/session/validate", bytes.NewReader(payload4))
	req4.Header.Set("Content-Type", "application/json")
	rec4 := httptest.NewRecorder()
	handler.ValidateAgentSession(rec4, req4)
	assert.Equal(t, http.StatusUnauthorized, rec4.Code)
}
