package services

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/config"
	"github.com/forgelab/backend/internal/models"
)

type importTestRoundTripFunc func(req *http.Request) (*http.Response, error)

func (f importTestRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func makeImportTestTarGz(files map[string]string) []byte {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for name, content := range files {
		_ = tw.WriteHeader(&tar.Header{
			Name: "testorg-testrepo-sha/" + name,
			Mode: 0644,
			Size: int64(len(content)),
		})
		_, _ = tw.Write([]byte(content))
	}
	_ = tw.Close()
	_ = gw.Close()
	return buf.Bytes()
}

func newMockGitHubServiceForImportTest(t *testing.T, handler http.HandlerFunc) (*GitHubService, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	gh := NewGitHubService(nil, nil, config.OAuthConfig{}, nil)
	gh.SetHTTPClient(&http.Client{
		Transport: importTestRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			u, err := url.Parse(srv.URL + req.URL.RequestURI())
			if err != nil {
				return nil, err
			}
			clone := req.Clone(req.Context())
			clone.URL = u
			return http.DefaultTransport.RoundTrip(clone)
		}),
	})
	return gh, srv
}

func uniqueTestSHA() string {
	return strings.ReplaceAll(uuid.New().String()+uuid.New().String(), "-", "")[:40]
}

func TestRepositoryImportService_RecoverInterruptedImports_ResumesValidAndFailsExpired(t *testing.T) {
	tempDir := t.TempDir()
	ctx := context.Background()
	ownerID := uuid.New()

	pinnedSHA := uniqueTestSHA()
	latestBranchSHA := uniqueTestSHA()
	t.Cleanup(func() {
		_ = os.RemoveAll(filepath.Join(os.TempDir(), "forgelab_gh_cache", "public", "acme", "app", pinnedSHA))
		_ = os.RemoveAll(filepath.Join(os.TempDir(), "forgelab_gh_cache", "public", "acme", "app", latestBranchSHA))
	})
	tarBytes := makeImportTestTarGz(map[string]string{
		"package.json": `{"name":"recovered-app","scripts":{"start":"node index.js"}}`,
		"index.js":     `console.log("ok");`,
	})

	gh, _ := newMockGitHubServiceForImportTest(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/acme/app":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"id":             101,
				"name":           "app",
				"full_name":      "acme/app",
				"private":        false,
				"default_branch": "main",
			})
		case r.URL.Path == "/repos/acme/app/commits/"+pinnedSHA:
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"sha": pinnedSHA})
		case r.URL.Path == "/repos/acme/app/commits/"+latestBranchSHA:
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"sha": latestBranchSHA})
		case r.URL.Path == "/repos/acme/app/commits/main":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"sha": latestBranchSHA})
		case strings.HasPrefix(r.URL.Path, "/repos/acme/app/tarball/"):
			w.Header().Set("Content-Type", "application/gzip")
			_, _ = w.Write(tarBytes)
		default:
			http.NotFound(w, r)
		}
	})

	svc := NewRepositoryImportService(nil, tempDir, gh)
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = svc.Stop(stopCtx)
	})

	now := time.Now().UTC()

	// 1. Interrupted mid-download with pinned SHA and stale partial workspace on disk
	resumableWithSHAID := uuid.New()
	partialDir1 := svc.snapshotDirForImport(resumableWithSHAID)
	require.NoError(t, os.MkdirAll(partialDir1, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(partialDir1, "corrupt.partial"), []byte("partial"), 0644))

	// 2. Interrupted early (verifying) without resolved SHA yet
	resumableNoSHAID := uuid.New()

	// 3. Interrupted but expired before restart
	expiredInterruptedID := uuid.New()
	partialExpiredDir := svc.snapshotDirForImport(expiredInterruptedID)
	require.NoError(t, os.MkdirAll(partialExpiredDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(partialExpiredDir, "old.partial"), []byte("stale"), 0644))

	// Seed simulated persistent records before startup recovery
	svc.memMu.Lock()
	svc.memImports[resumableWithSHAID] = &models.RepositoryImport{
		ID:                 resumableWithSHAID,
		OwnerID:            ownerID,
		SourceType:         models.SourceTypeGitHubPublic,
		RepositoryFullName: "acme/app",
		Branch:             "main",
		CommitSHA:          pinnedSHA,
		SnapshotPath:       partialDir1,
		Status:             models.ImportStatusDownloading,
		Progress:           50,
		Stage:              "Downloading repository snapshot",
		CreatedAt:          now.Add(-5 * time.Minute),
		UpdatedAt:          now.Add(-4 * time.Minute),
		ExpiresAt:          now.Add(12 * time.Hour),
	}
	svc.memImports[resumableNoSHAID] = &models.RepositoryImport{
		ID:                 resumableNoSHAID,
		OwnerID:            ownerID,
		SourceType:         models.SourceTypeGitHub,
		RepositoryFullName: "acme/app",
		Branch:             "",
		Status:             models.ImportStatusVerifying,
		Progress:           15,
		Stage:              "Verifying repository access",
		CreatedAt:          now.Add(-3 * time.Minute),
		UpdatedAt:          now.Add(-2 * time.Minute),
		ExpiresAt:          now.Add(12 * time.Hour),
	}
	svc.memImports[expiredInterruptedID] = &models.RepositoryImport{
		ID:                 expiredInterruptedID,
		OwnerID:            ownerID,
		SourceType:         models.SourceTypeGitHub,
		RepositoryFullName: "acme/app",
		Branch:             "main",
		SnapshotPath:       partialExpiredDir,
		Status:             models.ImportStatusExtracting,
		Progress:           65,
		Stage:              "Extracting",
		CreatedAt:          now.Add(-48 * time.Hour),
		UpdatedAt:          now.Add(-48 * time.Hour),
		ExpiresAt:          now.Add(-1 * time.Hour),
	}
	svc.memMu.Unlock()

	recoveredCount, err := svc.RecoverInterruptedImports(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, recoveredCount)

	// Expired interrupted import must transition to failed with import_expired and clean disk
	expiredRec, err := svc.GetImport(ctx, ownerID, expiredInterruptedID)
	require.NoError(t, err)
	assert.Equal(t, models.ImportStatusFailed, expiredRec.Status)
	assert.Equal(t, "import_expired", expiredRec.ErrorCode)
	assert.NoDirExists(t, partialExpiredDir)

	// Both non-expired interrupted imports must resume and reach ready state
	require.Eventually(t, func() bool {
		r1, e1 := svc.GetImport(ctx, ownerID, resumableWithSHAID)
		r2, e2 := svc.GetImport(ctx, ownerID, resumableNoSHAID)
		return e1 == nil && e2 == nil &&
			r1.Status == models.ImportStatusReady &&
			r2.Status == models.ImportStatusReady
	}, 5*time.Second, 50*time.Millisecond)

	rec1, err := svc.GetImport(ctx, ownerID, resumableWithSHAID)
	require.NoError(t, err)
	assert.Equal(t, pinnedSHA, rec1.CommitSHA, "resumed import must preserve already-pinned commit SHA")
	assert.FileExists(t, filepath.Join(rec1.SnapshotPath, "package.json"))
	assert.NoFileExists(t, filepath.Join(rec1.SnapshotPath, "corrupt.partial"))

	rec2, err := svc.GetImport(ctx, ownerID, resumableNoSHAID)
	require.NoError(t, err)
	assert.Equal(t, "main", rec2.Branch)
	assert.Equal(t, latestBranchSHA, rec2.CommitSHA)
	assert.NotEmpty(t, rec2.DiscoveryResult)
	assert.NotEmpty(t, rec2.DeploymentPlan)

	// Calling RecoverInterruptedImports while jobs are running or finished must be idempotent
	secondCount, err := svc.RecoverInterruptedImports(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, secondCount)
}

func TestRepositoryImportService_CancelRetryAndGuardRails(t *testing.T) {
	tempDir := t.TempDir()
	ctx := context.Background()
	ownerID := uuid.New()

	sha1 := uniqueTestSHA()
	t.Cleanup(func() {
		_ = os.RemoveAll(filepath.Join(os.TempDir(), "forgelab_gh_cache", "public", "acme", "cancellable", sha1))
	})
	tarBytes := makeImportTestTarGz(map[string]string{
		"Dockerfile": "FROM alpine:3.21\nCMD [\"echo\",\"ok\"]\n",
	})

	blockTarball := make(chan struct{})
	downloadStarted := make(chan struct{})
	var attempt int32

	gh, _ := newMockGitHubServiceForImportTest(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/acme/cancellable":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"id":             202,
				"name":           "cancellable",
				"full_name":      "acme/cancellable",
				"private":        false,
				"default_branch": "main",
			})
		case strings.HasPrefix(r.URL.Path, "/repos/acme/cancellable/commits/"):
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"sha": sha1})
		case strings.HasPrefix(r.URL.Path, "/repos/acme/cancellable/tarball/"):
			if atomic.AddInt32(&attempt, 1) == 1 {
				select {
				case <-downloadStarted:
				default:
					close(downloadStarted)
				}
				select {
				case <-blockTarball:
				case <-r.Context().Done():
					return
				}
			}
			w.Header().Set("Content-Type", "application/gzip")
			_, _ = w.Write(tarBytes)
		default:
			http.NotFound(w, r)
		}
	})

	svc := NewRepositoryImportService(nil, tempDir, gh)
	t.Cleanup(func() {
		close(blockTarball)
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = svc.Stop(stopCtx)
	})

	imp, err := svc.StartImport(ctx, ownerID, StartImportInput{
		Repository: "https://github.com/acme/cancellable",
		Branch:     "main",
	})
	require.NoError(t, err)

	// Wait until tarball request is blocked in flight
	select {
	case <-downloadStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for download stage to begin")
	}

	// Retry while active must return ErrImportAlreadyActive
	_, err = svc.RetryImport(ctx, ownerID, imp.ID)
	require.ErrorIs(t, err, ErrImportAlreadyActive)

	// Cancel in-flight import cleanly
	cancelled, err := svc.CancelImport(ctx, ownerID, imp.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ImportStatusCancelled, cancelled.Status)
	assert.Equal(t, "cancelled", cancelled.ErrorCode)
	assert.NoDirExists(t, svc.snapshotDirForImport(imp.ID))

	// Give background goroutine a moment and verify cancelled state is NOT overwritten to failed/ready
	time.Sleep(100 * time.Millisecond)
	afterCancel, err := svc.GetImport(ctx, ownerID, imp.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ImportStatusCancelled, afterCancel.Status)

	// Retry cancelled import -> succeeds and reaches ready
	retried, err := svc.RetryImport(ctx, ownerID, imp.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ImportStatusPending, retried.Status)
	assert.Empty(t, retried.ErrorCode)

	require.Eventually(t, func() bool {
		rec, getErr := svc.GetImport(ctx, ownerID, imp.ID)
		return getErr == nil && rec.Status == models.ImportStatusReady
	}, 5*time.Second, 50*time.Millisecond)

	// ListImports returns the user's import
	listed, err := svc.ListImports(ctx, ownerID, 10)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, imp.ID, listed[0].ID)

	// Mark completed (project attached) -> further cancel/retry must be rejected
	projectID := uuid.New()
	require.NoError(t, svc.MarkCompleted(ctx, ownerID, imp.ID, projectID))

	_, err = svc.CancelImport(ctx, ownerID, imp.ID)
	require.ErrorIs(t, err, ErrImportCannotCancelCompleted)

	_, err = svc.RetryImport(ctx, ownerID, imp.ID)
	require.ErrorIs(t, err, ErrImportNotRetryable)
}

func TestRepositoryImportService_StopPreservesInProgressStateForRecovery(t *testing.T) {
	tempDir := t.TempDir()
	ctx := context.Background()
	ownerID := uuid.New()
	slowSHA := uniqueTestSHA()
	t.Cleanup(func() {
		_ = os.RemoveAll(filepath.Join(os.TempDir(), "forgelab_gh_cache", "public", "acme", "slow", slowSHA))
	})

	tarStarted := make(chan struct{})
	gh, _ := newMockGitHubServiceForImportTest(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/acme/slow":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"id":             303,
				"name":           "slow",
				"full_name":      "acme/slow",
				"private":        false,
				"default_branch": "main",
			})
		case strings.HasPrefix(r.URL.Path, "/repos/acme/slow/commits/"):
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"sha": slowSHA})
		case strings.HasPrefix(r.URL.Path, "/repos/acme/slow/tarball/"):
			select {
			case <-tarStarted:
			default:
				close(tarStarted)
			}
			<-r.Context().Done()
		default:
			http.NotFound(w, r)
		}
	})

	svc := NewRepositoryImportService(nil, tempDir, gh)
	imp, err := svc.StartImport(ctx, ownerID, StartImportInput{Repository: "acme/slow"})
	require.NoError(t, err)

	select {
	case <-tarStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for slow import")
	}

	// Simulate graceful server stop while import is extracting/downloading
	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, svc.Stop(stopCtx))

	// Durable status must remain non-terminal (extracting/downloading) rather than failed or cancelled
	persisted, err := svc.GetImport(ctx, ownerID, imp.ID)
	require.NoError(t, err)
	assert.True(t, IsImportInProgressStatus(persisted.Status), "graceful stop should keep import recoverable, got %s", persisted.Status)
	assert.Equal(t, slowSHA, persisted.CommitSHA)
	assert.NoDirExists(t, svc.snapshotDirForImport(imp.ID))
}
