package docker

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/build"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/services"
)

func TestValidateVolumeName(t *testing.T) {
	valid := []string{"pgdata", "app_uploads-v1.0", "forgelab-vol-12345678-pgdata", "cache1"}
	for _, name := range valid {
		got, err := ValidateVolumeName(name)
		require.NoError(t, err, "expected valid name: %s", name)
		assert.Equal(t, name, got)
	}

	invalid := []string{"", "   ", "../etc", "a/b", `a\b`, "bad name", ".hidden", "-leading", strings.Repeat("a", 129)}
	for _, name := range invalid {
		_, err := ValidateVolumeName(name)
		require.Error(t, err, "expected invalid name to be rejected: %q", name)
		assert.True(t, errors.Is(err, ErrInvalidVolumeName))
	}
}

func writeStdFrame(w io.Writer, streamType stdcopy.StdType, payload []byte) {
	var hdr [8]byte
	hdr[0] = byte(streamType)
	binary.BigEndian.PutUint32(hdr[4:8], uint32(len(payload)))
	_, _ = w.Write(hdr[:])
	_, _ = w.Write(payload)
}

func makeTestTarGz(t *testing.T, entries []struct {
	hdr  tar.Header
	body string
}) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := e.hdr
		if hdr.Typeflag == 0 {
			hdr.Typeflag = tar.TypeReg
		}
		if hdr.Typeflag == tar.TypeReg {
			hdr.Size = int64(len(e.body))
		}
		require.NoError(t, tw.WriteHeader(&hdr))
		if hdr.Typeflag == tar.TypeReg && len(e.body) > 0 {
			_, err := tw.Write([]byte(e.body))
			require.NoError(t, err)
		}
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

func TestListProjectVolumes_ProjectScopingAndDiskUsage(t *testing.T) {
	ctx := context.Background()
	projectID := uuid.New()
	otherProjectID := uuid.New()
	svcID := uuid.New()
	prefix := projectVolumePrefix(projectID)
	pgDockerVol := prefix + "pgdata"

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			_ = json.NewEncoder(w).Encode([]container.Summary{
				{
					ID:    "cid-db-running",
					State: container.StateRunning,
					Labels: map[string]string{
						"forgelab.project_id":   projectID.String(),
						"forgelab.service_id":   svcID.String(),
						"forgelab.service_name": "postgres",
					},
					Mounts: []container.MountPoint{
						{Type: "volume", Name: pgDockerVol, Destination: "/var/lib/postgresql/data", RW: true},
					},
				},
			})
		case strings.HasSuffix(r.URL.Path, "/system/df"):
			// newDirectTestDockerClient negotiates API v1.41, which decodes legacyDiskUsage format
			_ = json.NewEncoder(w).Encode(map[string]any{
				"LayersSize": 100_000,
				"Images": []image.Summary{
					{Containers: 1, Size: 75_000, SharedSize: 0},
					{Containers: 0, Size: 25_000, SharedSize: 0},
				},
				"Containers": []container.Summary{
					{State: container.StateRunning, SizeRw: 3072},
					{State: container.StateExited, SizeRw: 1024},
				},
				"Volumes": []volume.Volume{
					{
						Name:      pgDockerVol,
						Driver:    "local",
						UsageData: &volume.UsageData{Size: 12_345, RefCount: 1},
					},
					{
						Name:      "caddy_data",
						Driver:    "local",
						UsageData: &volume.UsageData{Size: 37_655, RefCount: 0},
					},
				},
				"BuildCache": []build.CacheRecord{
					{InUse: false, Shared: false, Size: 8000},
				},
			})
		case strings.HasSuffix(r.URL.Path, "/volumes"):
			_ = json.NewEncoder(w).Encode(volume.ListResponse{
				Volumes: []volume.Volume{
					{
						Name:       pgDockerVol,
						Driver:     "local",
						Scope:      "local",
						Mountpoint: "/var/lib/docker/volumes/" + pgDockerVol + "/_data",
						Labels: map[string]string{
							"forgelab.project_id":  projectID.String(),
							"forgelab.volume_name": "pgdata",
						},
					},
					{
						Name:   "forgelab-vol-" + otherProjectID.String()[:8] + "-pgdata",
						Driver: "local",
						Labels: map[string]string{
							"forgelab.project_id":  otherProjectID.String(),
							"forgelab.volume_name": "pgdata",
						},
					},
					{
						Name:   "caddy_data",
						Driver: "local",
					},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("API-Version", "1.41")
		if strings.Contains(r.URL.Path, "/_ping") {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("OK"))
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	cli, err := client.New(
		client.WithHost("tcp://"+strings.TrimPrefix(server.URL, "http://")),
		client.WithAPIVersion("1.41"),
	)
	require.NoError(t, err)
	defer cli.Close()

	projSvc := services.NewProjectService(nil, nil, nil, nil)
	projSvc.SetProjectByIDForTest(func(_ context.Context, pid uuid.UUID) (*models.Project, error) {
		return &models.Project{ID: pid, Name: "shop-api"}, nil
	})
	svcSvc := services.NewServiceService(nil)
	svcSvc.SetListServicesForTest(func(_ context.Context, pid uuid.UUID) ([]*models.Service, error) {
		cid := "cid-db-running"
		return []*models.Service{
			{
				ID:          svcID,
				ProjectID:   pid,
				Name:        "postgres",
				ContainerID: &cid,
				Volumes: []models.VolumeMountConfig{
					{Type: models.VolumeTypeNamed, Source: "pgdata", Target: "/var/lib/postgresql/data"},
					{Type: models.VolumeTypeNamed, Source: "uploads", Target: "/app/uploads", ReadOnly: false},
					{Type: models.VolumeTypeBind, Source: "./config", Target: "/etc/app", ReadOnly: true},
				},
			},
		}, nil
	})

	engine := NewEngine(cli, projSvc, nil, nil, nil, nil, nil, nil, nil, t.TempDir())
	engine.serviceService = svcSvc

	res, err := engine.ListProjectVolumes(ctx, projectID)
	require.NoError(t, err)
	assert.True(t, res.DockerAvailable)
	assert.Equal(t, projectID, res.ProjectID)
	assert.Len(t, res.Volumes, 2, "must include declared + existing project volumes, excluding other projects and system volumes")

	assert.Equal(t, "pgdata", res.Volumes[0].Name)
	assert.Equal(t, pgDockerVol, res.Volumes[0].DockerName)
	assert.True(t, res.Volumes[0].BackupReady)
	assert.True(t, res.Volumes[0].InUse)
	assert.Equal(t, int64(12_345), res.Volumes[0].SizeBytes)
	assert.Equal(t, int64(1), res.Volumes[0].RefCount)

	assert.Equal(t, "uploads", res.Volumes[1].Name)
	assert.False(t, res.Volumes[1].BackupReady)
	assert.Equal(t, int64(-1), res.Volumes[1].SizeBytes)

	assert.Equal(t, int64(12_345), res.DiskUsage.ProjectVolumesBytes)
	assert.Equal(t, 2, res.DiskUsage.ProjectVolumesCount)
	assert.Equal(t, int64(162_096), res.DiskUsage.TotalBytes)
	assert.Equal(t, int64(71_679), res.DiskUsage.TotalReclaimable)
}

func TestBackupProjectVolume_StreamsCleanGzipArchiveAndEnforcesHardening(t *testing.T) {
	ctx := context.Background()
	projectID := uuid.New()
	prefix := projectVolumePrefix(projectID)
	volName := "pgdata"
	dockerVolName := prefix + volName

	expectedGz := makeTestTarGz(t, []struct {
		hdr  tar.Header
		body string
	}{
		{hdr: tar.Header{Name: "./hello.txt", Mode: 0o644}, body: "backup payload"},
	})

	var createdConfig container.CreateRequest
	var removeCalled atomic.Bool
	var removeVolumesFlag atomic.Bool

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/json"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]container.Summary{})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/system/df"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/volumes"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(volume.ListResponse{
				Volumes: []volume.Volume{
					{
						Name:   dockerVolName,
						Driver: "local",
						Labels: map[string]string{
							"forgelab.project_id":  projectID.String(),
							"forgelab.volume_name": volName,
						},
					},
				},
			})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/images/alpine:3.19/json"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"Id":"sha256:alpine"}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/containers/create"):
			require.NoError(t, json.NewDecoder(r.Body).Decode(&createdConfig))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(container.CreateResponse{ID: "helper-bak-1"})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/containers/helper-bak-1/attach"):
			hj, ok := w.(http.Hijacker)
			require.True(t, ok)
			conn, rw, err := hj.Hijack()
			require.NoError(t, err)
			go func() {
				defer conn.Close()
				_, _ = rw.WriteString("HTTP/1.1 101 UPGRADED\r\nContent-Type: application/vnd.docker.raw-stream\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
				_ = rw.Flush()
				writeStdFrame(conn, stdcopy.Stdout, expectedGz)
				writeStdFrame(conn, stdcopy.Stderr, []byte("tar: removing leading '/'\n"))
			}()
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/containers/helper-bak-1/wait"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(container.WaitResponse{StatusCode: 0})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/containers/helper-bak-1/start"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/containers/helper-bak-1"):
			removeCalled.Store(true)
			removeVolumesFlag.Store(r.URL.Query().Get("v") == "1" || r.URL.Query().Get("v") == "true")
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("API-Version", "1.41")
		if strings.Contains(r.URL.Path, "/_ping") {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("OK"))
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	cli, err := client.New(
		client.WithHost("tcp://"+strings.TrimPrefix(server.URL, "http://")),
		client.WithAPIVersion("1.41"),
	)
	require.NoError(t, err)
	defer cli.Close()

	projSvc := services.NewProjectService(nil, nil, nil, nil)
	projSvc.SetProjectByIDForTest(func(_ context.Context, pid uuid.UUID) (*models.Project, error) {
		return &models.Project{ID: pid, Name: "bak-proj"}, nil
	})
	svcSvc := services.NewServiceService(nil)
	svcSvc.SetListServicesForTest(func(_ context.Context, _ uuid.UUID) ([]*models.Service, error) {
		return nil, nil
	})

	engine := NewEngine(cli, projSvc, nil, nil, nil, nil, nil, nil, nil, t.TempDir())
	engine.serviceService = svcSvc

	var out bytes.Buffer
	err = engine.BackupProjectVolume(ctx, projectID, volName, &out)
	require.NoError(t, err)

	// Assert hardened helper container configuration
	require.NotNil(t, createdConfig.HostConfig)
	assert.Equal(t, []string{dockerVolName + ":/volume:ro"}, createdConfig.HostConfig.Binds)
	assert.Equal(t, []string{"ALL"}, []string(createdConfig.HostConfig.CapDrop))
	assert.Equal(t, []string{"no-new-privileges:true"}, createdConfig.HostConfig.SecurityOpt)
	assert.Equal(t, container.NetworkMode("none"), createdConfig.HostConfig.NetworkMode)
	assert.True(t, createdConfig.HostConfig.ReadonlyRootfs)
	assert.True(t, removeCalled.Load(), "backup helper container must be removed after streaming")
	assert.False(t, removeVolumesFlag.Load(), "backup cleanup MUST NOT pass RemoveVolumes=true")

	// Verify output bytes are clean demultiplexed .tar.gz with original content
	gzReader, err := gzip.NewReader(&out)
	require.NoError(t, err)
	defer gzReader.Close()
	tr := tar.NewReader(gzReader)
	hdr, err := tr.Next()
	require.NoError(t, err)
	assert.Equal(t, "./hello.txt", hdr.Name)
	data, err := io.ReadAll(tr)
	require.NoError(t, err)
	assert.Equal(t, "backup payload", string(data))
}

func TestRestoreProjectVolume_SafetyAndExtraction(t *testing.T) {
	ctx := context.Background()
	projectID := uuid.New()
	prefix := projectVolumePrefix(projectID)
	volName := "pgdata"
	dockerVolName := prefix + volName

	t.Run("refuses restore when volume is mounted by running container", func(t *testing.T) {
		var containerCreated atomic.Bool
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch {
			case strings.HasSuffix(r.URL.Path, "/containers/json"):
				_ = json.NewEncoder(w).Encode([]container.Summary{
					{
						ID:    "cid-live",
						State: container.StateRunning,
						Labels: map[string]string{
							"forgelab.project_id": projectID.String(),
						},
						Mounts: []container.MountPoint{
							{Type: "volume", Name: dockerVolName, Destination: "/var/lib/postgresql/data", RW: true},
						},
					},
				})
			case strings.HasSuffix(r.URL.Path, "/system/df"):
				_ = json.NewEncoder(w).Encode(map[string]any{})
			case strings.HasSuffix(r.URL.Path, "/volumes"):
				_ = json.NewEncoder(w).Encode(volume.ListResponse{
					Volumes: []volume.Volume{
						{Name: dockerVolName, Driver: "local", Labels: map[string]string{"forgelab.project_id": projectID.String(), "forgelab.volume_name": volName}},
					},
				})
			case strings.HasSuffix(r.URL.Path, "/containers/create"):
				containerCreated.Store(true)
				w.WriteHeader(http.StatusCreated)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		})

		cli, cleanup := newDirectTestDockerClient(t, handler)
		defer cleanup()

		projSvc := services.NewProjectService(nil, nil, nil, nil)
		projSvc.SetProjectByIDForTest(func(_ context.Context, pid uuid.UUID) (*models.Project, error) {
			return &models.Project{ID: pid, Name: "inuse-proj"}, nil
		})
		engine := NewEngine(cli, projSvc, nil, nil, nil, nil, nil, nil, nil, t.TempDir())

		validGz := makeTestTarGz(t, []struct {
			hdr  tar.Header
			body string
		}{{hdr: tar.Header{Name: "data.txt", Mode: 0o644}, body: "hi"}})

		_, err := engine.RestoreProjectVolume(ctx, projectID, volName, bytes.NewReader(validGz))
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrVolumeInUse))
		assert.False(t, containerCreated.Load())
	})

	t.Run("rejects unsafe archives before spawning helper container", func(t *testing.T) {
		var containerCreated atomic.Bool
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch {
			case strings.HasSuffix(r.URL.Path, "/containers/json"):
				_ = json.NewEncoder(w).Encode([]container.Summary{})
			case strings.HasSuffix(r.URL.Path, "/system/df"):
				_ = json.NewEncoder(w).Encode(map[string]any{})
			case strings.HasSuffix(r.URL.Path, "/volumes"):
				_ = json.NewEncoder(w).Encode(volume.ListResponse{
					Volumes: []volume.Volume{
						{Name: dockerVolName, Driver: "local", Labels: map[string]string{"forgelab.project_id": projectID.String(), "forgelab.volume_name": volName}},
					},
				})
			case strings.HasSuffix(r.URL.Path, "/containers/create"):
				containerCreated.Store(true)
				w.WriteHeader(http.StatusCreated)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		})

		cli, cleanup := newDirectTestDockerClient(t, handler)
		defer cleanup()

		projSvc := services.NewProjectService(nil, nil, nil, nil)
		projSvc.SetProjectByIDForTest(func(_ context.Context, pid uuid.UUID) (*models.Project, error) {
			return &models.Project{ID: pid, Name: "sec-proj"}, nil
		})
		engine := NewEngine(cli, projSvc, nil, nil, nil, nil, nil, nil, nil, t.TempDir())

		badCases := []struct {
			name string
			data []byte
		}{
			{
				name: "not gzip",
				data: []byte("plain text not gzip"),
			},
			{
				name: "path traversal",
				data: makeTestTarGz(t, []struct {
					hdr  tar.Header
					body string
				}{{hdr: tar.Header{Name: "../../etc/passwd", Mode: 0o644}, body: "root:x"}}),
			},
			{
				name: "absolute path",
				data: makeTestTarGz(t, []struct {
					hdr  tar.Header
					body string
				}{{hdr: tar.Header{Name: "/etc/shadow", Mode: 0o600}, body: "x"}}),
			},
			{
				name: "escaping symlink",
				data: makeTestTarGz(t, []struct {
					hdr  tar.Header
					body string
				}{{hdr: tar.Header{Name: "sub/leak", Typeflag: tar.TypeSymlink, Linkname: "../../etc/passwd"}}}),
			},
			{
				name: "absolute symlink",
				data: makeTestTarGz(t, []struct {
					hdr  tar.Header
					body string
				}{{hdr: tar.Header{Name: "leak", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}}}),
			},
			{
				name: "char device node",
				data: makeTestTarGz(t, []struct {
					hdr  tar.Header
					body string
				}{{hdr: tar.Header{Name: "devnull", Typeflag: tar.TypeChar, Mode: 0o666}}}),
			},
		}

		for _, tc := range badCases {
			t.Run(tc.name, func(t *testing.T) {
				_, err := engine.RestoreProjectVolume(ctx, projectID, volName, bytes.NewReader(tc.data))
				require.Error(t, err)
				assert.True(t, errors.Is(err, ErrInvalidVolumeArchive), "got err=%v", err)
				assert.False(t, containerCreated.Load())
			})
		}
	})

	t.Run("sanitizes setuid bits, allows internal relative symlinks, and extracts to stopped volume", func(t *testing.T) {
		var mu sync.Mutex
		var extractedTarBytes []byte
		var wipeRemovedVol bool

		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch {
			case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/json"):
				_ = json.NewEncoder(w).Encode([]container.Summary{
					{
						ID:    "cid-stopped",
						State: container.StateExited,
						Labels: map[string]string{
							"forgelab.project_id": projectID.String(),
						},
						Mounts: []container.MountPoint{
							{Type: "volume", Name: dockerVolName, Destination: "/var/lib/postgresql/data", RW: true},
						},
					},
				})
			case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/system/df"):
				_ = json.NewEncoder(w).Encode(map[string]any{})
			case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/volumes"):
				_ = json.NewEncoder(w).Encode(volume.ListResponse{
					Volumes: []volume.Volume{
						{Name: dockerVolName, Driver: "local", Labels: map[string]string{"forgelab.project_id": projectID.String(), "forgelab.volume_name": volName}},
					},
				})
			case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/images/alpine:3.19/json"):
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"Id":"sha256:alpine"}`))
			case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/containers/create"):
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(container.CreateResponse{ID: "helper-rst-1"})
			case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/containers/helper-rst-1/wait"):
				_ = json.NewEncoder(w).Encode(container.WaitResponse{StatusCode: 0})
			case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/containers/helper-rst-1/start"):
				w.WriteHeader(http.StatusNoContent)
			case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/containers/helper-rst-1/archive"):
				assert.Equal(t, "/volume", r.URL.Query().Get("path"))
				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				mu.Lock()
				extractedTarBytes = body
				mu.Unlock()
				w.WriteHeader(http.StatusOK)
			case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/containers/helper-rst-1"):
				wipeRemovedVol = r.URL.Query().Get("v") == "1" || r.URL.Query().Get("v") == "true"
				w.WriteHeader(http.StatusNoContent)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		})

		cli, cleanup := newDirectTestDockerClient(t, handler)
		defer cleanup()

		projSvc := services.NewProjectService(nil, nil, nil, nil)
		projSvc.SetProjectByIDForTest(func(_ context.Context, pid uuid.UUID) (*models.Project, error) {
			return &models.Project{ID: pid, Name: "restore-ok"}, nil
		})
		engine := NewEngine(cli, projSvc, nil, nil, nil, nil, nil, nil, nil, t.TempDir())

		backupGz := makeTestTarGz(t, []struct {
			hdr  tar.Header
			body string
		}{
			{hdr: tar.Header{Name: "./data/suid-script.sh", Mode: 0o4755, Typeflag: tar.TypeReg}, body: "#!/bin/sh\necho ok"},
			{hdr: tar.Header{Name: "./data/current", Typeflag: tar.TypeSymlink, Linkname: "suid-script.sh"}},
		})

		res, err := engine.RestoreProjectVolume(ctx, projectID, volName, bytes.NewReader(backupGz))
		require.NoError(t, err)
		assert.Equal(t, "pgdata", res.VolumeName)
		assert.Equal(t, 1, res.FilesRestored)
		assert.Equal(t, int64(len("#!/bin/sh\necho ok")), res.BytesRestored)
		assert.False(t, wipeRemovedVol)

		mu.Lock()
		tarCopy := append([]byte(nil), extractedTarBytes...)
		mu.Unlock()
		require.NotEmpty(t, tarCopy)

		tr := tar.NewReader(bytes.NewReader(tarCopy))
		h1, err := tr.Next()
		require.NoError(t, err)
		assert.Equal(t, "data/suid-script.sh", h1.Name)
		assert.Equal(t, int64(0o755), h1.Mode&0o7777, "setuid bit must be stripped on restore")

		h2, err := tr.Next()
		require.NoError(t, err)
		assert.Equal(t, "data/current", h2.Name)
		assert.Equal(t, byte(tar.TypeSymlink), h2.Typeflag)
		assert.Equal(t, "suid-script.sh", h2.Linkname)
	})
}

func TestValidateAndStageVolumeRestoreTar_SizeCap(t *testing.T) {
	ctx := context.Background()
	bigPayload := strings.Repeat("A", 4096)
	gz := makeTestTarGz(t, []struct {
		hdr  tar.Header
		body string
	}{{hdr: tar.Header{Name: "big.bin", Mode: 0o644}, body: bigPayload}})

	_, _, err := validateAndStageVolumeRestoreTar(ctx, bytes.NewReader(gz), DefaultMaxVolumeRestoreCompressedBytes, 1024, 100)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrVolumeArchiveTooLarge))

	// Check corrupted trailing header
	var corrupt bytes.Buffer
	gw := gzip.NewWriter(&corrupt)
	_, _ = gw.Write([]byte("not a valid tar block"))
	_ = gw.Close()
	_, _, err = validateAndStageVolumeRestoreTar(ctx, &corrupt, 1024*1024, 1024*1024, 100)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidVolumeArchive))
}

func TestListProjectVolumes_DockerAPIV152VerboseDiskUsage(t *testing.T) {
	ctx := context.Background()
	projectID := uuid.New()
	prefix := projectVolumePrefix(projectID)
	pgDockerVol := prefix + "pgdata"
	var sawVerbose atomic.Bool

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("API-Version", "1.52")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/_ping"):
			_, _ = w.Write([]byte("OK"))
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			_ = json.NewEncoder(w).Encode([]container.Summary{})
		case strings.HasSuffix(r.URL.Path, "/system/df"):
			if r.URL.Query().Get("verbose") == "1" {
				sawVerbose.Store(true)
			}
			_ = json.NewEncoder(w).Encode(system.DiskUsage{
				ImageUsage: &image.DiskUsage{
					ActiveCount: 1,
					TotalCount:  2,
					Reclaimable: 500,
					TotalSize:   2000,
				},
				ContainerUsage: &container.DiskUsage{
					ActiveCount: 1,
					TotalCount:  1,
					Reclaimable: 100,
					TotalSize:   1000,
				},
				VolumeUsage: &volume.DiskUsage{
					ActiveCount: 1,
					TotalCount:  1,
					Reclaimable: 0,
					TotalSize:   4096,
					Items: []volume.Volume{
						{
							Name:      pgDockerVol,
							Driver:    "local",
							UsageData: &volume.UsageData{Size: 4096, RefCount: 1},
						},
					},
				},
				BuildCacheUsage: &build.DiskUsage{
					ActiveCount: 0,
					TotalCount:  1,
					Reclaimable: 300,
					TotalSize:   300,
				},
			})
		case strings.HasSuffix(r.URL.Path, "/volumes"):
			_ = json.NewEncoder(w).Encode(volume.ListResponse{
				Volumes: []volume.Volume{
					{
						Name:   pgDockerVol,
						Driver: "local",
						Labels: map[string]string{
							"forgelab.project_id":  projectID.String(),
							"forgelab.volume_name": "pgdata",
						},
					},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	server := httptest.NewServer(handler)
	defer server.Close()
	cli, err := client.New(
		client.WithHost(server.URL),
		client.WithHTTPClient(server.Client()),
		client.WithAPIVersion("1.52"),
	)
	require.NoError(t, err)
	defer cli.Close()

	projSvc := services.NewProjectService(nil, nil, nil, nil)
	projSvc.SetProjectByIDForTest(func(_ context.Context, pid uuid.UUID) (*models.Project, error) {
		return &models.Project{ID: pid, Name: "v152-proj"}, nil
	})
	svcSvc := services.NewServiceService(nil)
	svcSvc.SetListServicesForTest(func(_ context.Context, _ uuid.UUID) ([]*models.Service, error) {
		return nil, nil
	})

	engine := NewEngine(cli, projSvc, nil, nil, nil, nil, nil, nil, nil, t.TempDir())
	engine.serviceService = svcSvc

	res, err := engine.ListProjectVolumes(ctx, projectID)
	require.NoError(t, err)
	assert.True(t, sawVerbose.Load(), "must pass verbose=1 so Docker Engine >= 1.52 returns Volumes.Items")
	require.Len(t, res.Volumes, 1)
	assert.Equal(t, int64(4096), res.Volumes[0].SizeBytes)
	assert.Equal(t, int64(1), res.Volumes[0].RefCount)
	assert.Equal(t, int64(4096), res.DiskUsage.ProjectVolumesBytes)
	assert.Equal(t, int64(7396), res.DiskUsage.TotalBytes)
	assert.Equal(t, int64(900), res.DiskUsage.TotalReclaimable)
}

func TestListProjectVolumes_DiskUsageTimeoutFallbackAndCache(t *testing.T) {
	ctx := context.Background()
	projectID := uuid.New()
	prefix := projectVolumePrefix(projectID)
	pgDockerVol := prefix + "pgdata"
	var verboseCalls atomic.Int32
	var lightweightCalls atomic.Int32

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("API-Version", "1.52")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/_ping"):
			_, _ = w.Write([]byte("OK"))
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			_ = json.NewEncoder(w).Encode([]container.Summary{})
		case strings.HasSuffix(r.URL.Path, "/system/df"):
			if r.URL.Query().Get("verbose") == "1" {
				verboseCalls.Add(1)
				// Simulate a stalled verbose /system/df exceeding the 5s timeout.
				select {
				case <-r.Context().Done():
				case <-time.After(8 * time.Second):
				}
				return
			}
			lightweightCalls.Add(1)
			_ = json.NewEncoder(w).Encode(system.DiskUsage{
				ImageUsage: &image.DiskUsage{
					ActiveCount: 1,
					TotalCount:  1,
					Reclaimable: 0,
					TotalSize:   2048,
				},
				VolumeUsage: &volume.DiskUsage{
					ActiveCount: 1,
					TotalCount:  1,
					Reclaimable: 0,
					TotalSize:   4096,
				},
			})
		case strings.HasSuffix(r.URL.Path, "/volumes"):
			_ = json.NewEncoder(w).Encode(volume.ListResponse{
				Volumes: []volume.Volume{
					{
						Name:   pgDockerVol,
						Driver: "local",
						Labels: map[string]string{
							"forgelab.project_id":  projectID.String(),
							"forgelab.volume_name": "pgdata",
						},
					},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	server := httptest.NewServer(handler)
	defer server.Close()
	cli, err := client.New(
		client.WithHost(server.URL),
		client.WithHTTPClient(server.Client()),
		client.WithAPIVersion("1.52"),
	)
	require.NoError(t, err)
	defer cli.Close()

	projSvc := services.NewProjectService(nil, nil, nil, nil)
	projSvc.SetProjectByIDForTest(func(_ context.Context, pid uuid.UUID) (*models.Project, error) {
		return &models.Project{ID: pid, Name: "stalled-df-proj"}, nil
	})
	svcSvc := services.NewServiceService(nil)
	svcSvc.SetListServicesForTest(func(_ context.Context, _ uuid.UUID) ([]*models.Service, error) {
		return nil, nil
	})

	engine := NewEngine(cli, projSvc, nil, nil, nil, nil, nil, nil, nil, t.TempDir())
	engine.serviceService = svcSvc

	start := time.Now()
	res, err := engine.ListProjectVolumes(ctx, projectID)
	elapsed := time.Since(start)
	require.NoError(t, err)
	assert.Less(t, elapsed, 8*time.Second, "volume listing must complete promptly even when verbose system/df stalls")
	assert.True(t, res.DockerAvailable)
	require.Len(t, res.Volumes, 1)
	assert.Equal(t, "pgdata", res.Volumes[0].Name)
	assert.Equal(t, int64(6144), res.DiskUsage.TotalBytes)
	assert.Contains(t, res.DiskUsage.Warning, "fast Docker disk summary")
	assert.Equal(t, int32(1), verboseCalls.Load())
	assert.Equal(t, int32(1), lightweightCalls.Load())

	// Second call within TTL must hit the cache instead of stalling /system/df again.
	startCached := time.Now()
	res2, err := engine.ListProjectVolumes(ctx, projectID)
	require.NoError(t, err)
	assert.Less(t, time.Since(startCached), 500*time.Millisecond)
	assert.Equal(t, int64(6144), res2.DiskUsage.TotalBytes)
	assert.Equal(t, int32(1), verboseCalls.Load())
}
