package handlers_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/handlers"
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
	handler := handlers.NewSourceHandler(sourceSvc)

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
	handler := handlers.NewSourceHandler(sourceSvc)
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
	handler := handlers.NewSourceHandler(sourceSvc)

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
	handler := handlers.NewSourceHandler(sourceSvc)
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
	handler := handlers.NewSourceHandler(sourceSvc)
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
	handler := handlers.NewSourceHandler(sourceSvc)
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
	handler := handlers.NewSourceHandler(sourceSvc)
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


