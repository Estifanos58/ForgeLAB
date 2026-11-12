package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/docker"
	"github.com/forgelab/backend/internal/handlers"
	"github.com/forgelab/backend/internal/middleware"
	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/services"
)

type fakeVolumeManager struct {
	listFn    func(ctx context.Context, projectID uuid.UUID) (*docker.ProjectVolumesResponse, error)
	backupFn  func(ctx context.Context, projectID uuid.UUID, volumeName string, w io.Writer) error
	restoreFn func(ctx context.Context, projectID uuid.UUID, volumeName string, r io.Reader) (*docker.VolumeRestoreResult, error)
}

func (f *fakeVolumeManager) ListProjectVolumes(ctx context.Context, projectID uuid.UUID) (*docker.ProjectVolumesResponse, error) {
	return f.listFn(ctx, projectID)
}

func (f *fakeVolumeManager) BackupProjectVolume(ctx context.Context, projectID uuid.UUID, volumeName string, w io.Writer) error {
	return f.backupFn(ctx, projectID, volumeName, w)
}

func (f *fakeVolumeManager) RestoreProjectVolume(ctx context.Context, projectID uuid.UUID, volumeName string, r io.Reader) (*docker.VolumeRestoreResult, error) {
	return f.restoreFn(ctx, projectID, volumeName, r)
}

func TestVolumeHandler_AuthOwnershipAndOperations(t *testing.T) {
	ownerID := uuid.New()
	otherID := uuid.New()
	projectID := uuid.New()

	projSvc := services.NewProjectService(nil, nil, nil, nil)
	projSvc.SetProjectGetterForTest(func(_ context.Context, pID, uID uuid.UUID) (*models.Project, error) {
		if pID != projectID {
			return nil, services.ErrProjectNotFound
		}
		if uID != ownerID {
			return nil, services.ErrProjectNotOwned
		}
		return &models.Project{
			ID:      projectID,
			OwnerID: ownerID,
			Name:    "Shop DB",
		}, nil
	})

	fakeMgr := &fakeVolumeManager{
		listFn: func(_ context.Context, pid uuid.UUID) (*docker.ProjectVolumesResponse, error) {
			return &docker.ProjectVolumesResponse{
				ProjectID:       pid,
				ProjectName:     "Shop DB",
				DockerAvailable: true,
				Volumes: []docker.ProjectVolumeInfo{
					{
						Name:        "pgdata",
						DockerName:  "forgelab-vol-12345678-pgdata",
						Driver:      "local",
						SizeBytes:   2048,
						BackupReady: true,
					},
				},
				DiskUsage: docker.DiskUsageSummary{
					DockerAvailable:     true,
					ProjectVolumesBytes: 2048,
					ProjectVolumesCount: 1,
					TotalBytes:          10000,
				},
			}, nil
		},
		backupFn: func(_ context.Context, _ uuid.UUID, volumeName string, w io.Writer) error {
			if volumeName == "missing" {
				return docker.ErrVolumeNotFound
			}
			_, err := w.Write([]byte("\x1f\x8bfake-gzip-stream"))
			return err
		},
		restoreFn: func(_ context.Context, _ uuid.UUID, volumeName string, r io.Reader) (*docker.VolumeRestoreResult, error) {
			data, _ := io.ReadAll(r)
			switch volumeName {
			case "busy":
				return nil, docker.ErrVolumeInUse
			case "badtar":
				return nil, docker.ErrInvalidVolumeArchive
			case "huge":
				return nil, docker.ErrVolumeArchiveTooLarge
			default:
				return &docker.VolumeRestoreResult{
					VolumeName:    volumeName,
					DockerName:    "forgelab-vol-12345678-" + volumeName,
					FilesRestored: 3,
					BytesRestored: int64(len(data)),
					ArchiveBytes:  int64(len(data)),
					Message:       "restored",
				}, nil
			}
		},
	}

	h := handlers.NewVolumeHandler(projSvc, fakeMgr)

	r := chi.NewRouter()
	r.Use(middleware.MaxBodySize(1024, 4096))
	r.Use(middleware.ContentTypeJSON)
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if uidStr := req.Header.Get("X-Test-User"); uidStr != "" {
				uid, _ := uuid.Parse(uidStr)
				req = req.WithContext(withTestUser(req.Context(), uid))
			}
			next.ServeHTTP(w, req)
		})
	})
	r.Get("/api/projects/{id}/volumes", h.List)
	r.Post("/api/projects/{id}/volumes/{volumeName}/backup", h.Backup)
	r.Post("/api/projects/{id}/volumes/{volumeName}/restore", h.Restore)

	t.Run("unauthenticated and cross-user requests are rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID.String()+"/volumes", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)

		req = httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID.String()+"/volumes", nil)
		req.Header.Set("X-Test-User", otherID.String())
		rec = httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("list volumes returns project volumes and disk usage", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID.String()+"/volumes", nil)
		req.Header.Set("X-Test-User", ownerID.String())
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		var resp docker.ProjectVolumesResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Len(t, resp.Volumes, 1)
		assert.Equal(t, "pgdata", resp.Volumes[0].Name)
		assert.Equal(t, int64(2048), resp.DiskUsage.ProjectVolumesBytes)
	})

	t.Run("backup sets gzip headers on stream and json errors before stream", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID.String()+"/volumes/pgdata/backup", nil)
		req.Header.Set("X-Test-User", ownerID.String())
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "application/gzip", rec.Header().Get("Content-Type"))
		assert.Contains(t, rec.Header().Get("Content-Disposition"), "shop-db-pgdata-")
		assert.True(t, bytes.HasPrefix(rec.Body.Bytes(), []byte("\x1f\x8bfake-gzip-stream")))

		// Not found error keeps JSON response
		req = httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID.String()+"/volumes/missing/backup", nil)
		req.Header.Set("X-Test-User", ownerID.String())
		rec = httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusNotFound, rec.Code)
		assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

		// Traversal volume name rejected before calling manager
		req = httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID.String()+"/volumes/..bad/backup", nil)
		req.Header.Set("X-Test-User", ownerID.String())
		rec = httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("restore supports multipart form upload, raw gzip upload, and error mapping", func(t *testing.T) {
		// 1. Multipart form upload > default 1024 limit (within 4096 upload limit)
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		fw, err := mw.CreateFormFile("file", "pgdata.tar.gz")
		require.NoError(t, err)
		payload := bytes.Repeat([]byte("Z"), 1500)
		_, err = fw.Write(payload)
		require.NoError(t, err)
		require.NoError(t, mw.Close())

		req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID.String()+"/volumes/pgdata/restore", &body)
		req.Header.Set("X-Test-User", ownerID.String())
		req.Header.Set("Content-Type", mw.FormDataContentType())
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		var restoreRes docker.VolumeRestoreResult
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &restoreRes))
		assert.Equal(t, "pgdata", restoreRes.VolumeName)
		assert.Equal(t, int64(len(payload)), restoreRes.BytesRestored)

		// 2. Raw gzip upload also receives uploadLimit (1500 > 1024 default, <= 4096 upload limit)
		req = httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID.String()+"/volumes/pgdata/restore", bytes.NewReader(payload))
		req.Header.Set("X-Test-User", ownerID.String())
		req.Header.Set("Content-Type", "application/gzip")
		rec = httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)

		// 3. Error mapping: in-use -> 409, bad archive -> 400, archive too large -> 413
		for _, tc := range []struct {
			vol  string
			code int
		}{
			{vol: "busy", code: http.StatusConflict},
			{vol: "badtar", code: http.StatusBadRequest},
			{vol: "huge", code: http.StatusRequestEntityTooLarge},
		} {
			req = httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID.String()+"/volumes/"+tc.vol+"/restore", bytes.NewBufferString("abc"))
			req.Header.Set("X-Test-User", ownerID.String())
			rec = httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			assert.Equal(t, tc.code, rec.Code, "unexpected status for volume=%s", tc.vol)
		}

		// 4. Typed nil *docker.Engine safety
		var nilEng *docker.Engine
		nilH := handlers.NewVolumeHandler(projSvc, nilEng)
		rr := chi.NewRouter()
		rr.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				req = req.WithContext(withTestUser(req.Context(), ownerID))
				next.ServeHTTP(w, req)
			})
		})
		rr.Get("/api/projects/{id}/volumes", nilH.List)
		req = httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID.String()+"/volumes", nil)
		rec = httptest.NewRecorder()
		rr.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	})
}

type fakeRefreshVolumeManager struct {
	fakeVolumeManager
	listWithOptsFn func(ctx context.Context, projectID uuid.UUID, forceRefresh bool) (*docker.ProjectVolumesResponse, error)
}

func (f *fakeRefreshVolumeManager) ListProjectVolumesWithOptions(ctx context.Context, projectID uuid.UUID, forceRefresh bool) (*docker.ProjectVolumesResponse, error) {
	return f.listWithOptsFn(ctx, projectID, forceRefresh)
}

func TestVolumeHandler_ForceRefreshQueryBypass(t *testing.T) {
	ownerID := uuid.New()
	projectID := uuid.New()

	projSvc := services.NewProjectService(nil, nil, nil, nil)
	projSvc.SetProjectGetterForTest(func(_ context.Context, pID, uID uuid.UUID) (*models.Project, error) {
		if pID == projectID && uID == ownerID {
			return &models.Project{ID: projectID, OwnerID: ownerID, Name: "RefreshDB"}, nil
		}
		return nil, services.ErrProjectNotFound
	})

	var sawDefaultCall bool
	var sawRefreshCall bool
	mgr := &fakeRefreshVolumeManager{
		fakeVolumeManager: fakeVolumeManager{
			listFn: func(_ context.Context, pid uuid.UUID) (*docker.ProjectVolumesResponse, error) {
				sawDefaultCall = true
				return &docker.ProjectVolumesResponse{ProjectID: pid, DockerAvailable: true}, nil
			},
		},
		listWithOptsFn: func(_ context.Context, pid uuid.UUID, forceRefresh bool) (*docker.ProjectVolumesResponse, error) {
			sawRefreshCall = forceRefresh
			return &docker.ProjectVolumesResponse{ProjectID: pid, DockerAvailable: true}, nil
		},
	}

	h := handlers.NewVolumeHandler(projSvc, mgr)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			req = req.WithContext(withTestUser(req.Context(), ownerID))
			next.ServeHTTP(w, req)
		})
	})
	r.Get("/api/projects/{id}/volumes", h.List)

	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID.String()+"/volumes", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, sawDefaultCall)
	assert.False(t, sawRefreshCall)

	req = httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID.String()+"/volumes?refresh=true", nil)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, sawRefreshCall)
}

var _ = errors.New
