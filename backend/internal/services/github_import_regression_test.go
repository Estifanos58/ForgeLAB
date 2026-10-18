package services_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/config"
	"github.com/forgelab/backend/internal/crypto"
	"github.com/forgelab/backend/internal/discovery"
	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/services"
)

func createTestTarGz(files map[string]string) []byte {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	for path, content := range files {
		hdr := &tar.Header{
			Name: "owner-repo-commit123/" + path,
			Mode: 0644,
			Size: int64(len(content)),
		}
		_ = tw.WriteHeader(hdr)
		_, _ = tw.Write([]byte(content))
	}

	_ = tw.Close()
	_ = gw.Close()
	return buf.Bytes()
}

func setupImportTestServices(t *testing.T) (*services.RepositoryImportService, *services.GitHubService, *services.GitHubAppManager, *services.ProjectService, *services.SourceService, string) {
	pool, _ := connectTestDB(t)

	tempDir, err := os.MkdirTemp("", "forgelab-import-test-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(tempDir) })

	encryptor, err := crypto.NewEncryptor("test-secret-key-32-bytes-long!!!")
	require.NoError(t, err)

	sourceService := services.NewSourceService(pool, tempDir, encryptor)
	serviceService := services.NewServiceService(pool)
	deploymentService := services.NewDeploymentService(pool)
	secretService := services.NewSecretService(pool, encryptor, nil)
	deploymentService.SetSecretService(secretService)

	projectService := services.NewProjectService(pool, nil, sourceService, nil)
	projectService.SetServiceService(serviceService)
	projectService.SetDeploymentService(deploymentService)
	projectService.SetSecretService(secretService)

	appCfg := config.GitHubAppConfig{
		AppID:         "12345",
		Slug:          "forgelab-ci",
		WebhookSecret: "test-secret",
	}
	appManager := services.NewGitHubAppManager(appCfg, pool, nil)

	ghCfg := config.OAuthConfig{
		ClientID:     "test-gh-client-id",
		ClientSecret: "test-gh-secret",
	}
	ghService := services.NewGitHubService(pool, encryptor, ghCfg, nil)
	ghService.SetGitHubAppManager(appManager)

	importService := services.NewRepositoryImportService(pool, tempDir, ghService)
	projectService.SetImportService(importService)

	return importService, ghService, appManager, projectService, sourceService, tempDir
}

// 1. Regression Test: Public repository import without authorization
func TestGitHubImport_PublicRepoWithoutAuth_SuccessAndSnapshotReuse(t *testing.T) {
	importService, ghService, _, projectService, _, _ := setupImportTestServices(t)
	ctx := context.Background()
	ownerID := uuid.New()

	// Ensure user exists in DB
	pool, _ := connectTestDB(t)
	_, err := pool.Exec(ctx, `INSERT INTO users (id, email, password_hash) VALUES ($1, $2, $3) ON CONFLICT (id) DO NOTHING`,
		ownerID, fmt.Sprintf("test-%s@example.com", ownerID.String()[:8]), "hash")
	require.NoError(t, err)

	expectedCommitSHA := "7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f5a6b"
	tarballBytes := createTestTarGz(map[string]string{
		"package.json": `{"name": "public-web-app", "scripts": {"start": "node server.js"}}`,
		"server.js":    `const http = require('http'); http.createServer((req, res) => res.end('OK')).listen(3000);`,
	})

	var receivedAuthHeader string
	mockGitHub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuthHeader = r.Header.Get("Authorization")

		switch {
		case r.URL.Path == "/repos/publicorg/publicrepo":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"id":             99901,
				"name":           "publicrepo",
				"full_name":      "publicorg/publicrepo",
				"private":        false,
				"default_branch": "main",
			})
		case strings.HasPrefix(r.URL.Path, "/repos/publicorg/publicrepo/commits/"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"sha": expectedCommitSHA,
			})
		case strings.HasPrefix(r.URL.Path, "/repos/publicorg/publicrepo/tarball/"):
			w.Header().Set("Content-Type", "application/gzip")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(tarballBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockGitHub.Close()

	customClient := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			u, _ := url.Parse(mockGitHub.URL + req.URL.RequestURI())
			req.URL = u
			return http.DefaultTransport.RoundTrip(req)
		}),
	}
	ghService.SetHTTPClient(customClient)

	// User has NO connected GitHub account (unauthenticated public import)
	imp, err := importService.StartImport(ctx, ownerID, services.StartImportInput{
		Repository: "publicorg/publicrepo",
		Branch:     "main",
		RootDir:    "",
	})
	require.NoError(t, err)
	require.NotNil(t, imp)
	assert.Equal(t, models.ImportStatusPending, imp.Status)

	// Poll until ready
	var readyImport *models.RepositoryImport
	require.Eventually(t, func() bool {
		rec, getErr := importService.GetImport(ctx, ownerID, imp.ID)
		if getErr == nil && rec.Status == models.ImportStatusReady {
			readyImport = rec
			return true
		}
		return false
	}, 5*time.Second, 100*time.Millisecond, "import must complete and reach ready status")

	// Verify unauthenticated request sent NO Authorization header
	assert.Empty(t, receivedAuthHeader, "public import must not leak auth headers when user is unauthenticated")

	// Verify commit SHA resolution
	assert.Equal(t, expectedCommitSHA, readyImport.CommitSHA)
	assert.Equal(t, 100, readyImport.Progress)
	assert.NotEmpty(t, readyImport.SnapshotPath)
	assert.DirExists(t, readyImport.SnapshotPath)
	assert.FileExists(t, filepath.Join(readyImport.SnapshotPath, "package.json"))

	// Verify discovery result and deployment plan were generated
	require.NotEmpty(t, readyImport.DiscoveryResult)
	require.NotEmpty(t, readyImport.DeploymentPlan)

	var plan discovery.DeploymentPlan
	err = json.Unmarshal(readyImport.DeploymentPlan, &plan)
	require.NoError(t, err)
	assert.Equal(t, expectedCommitSHA, plan.SourceRevision)

	// Test Single Snapshot Reuse in Project Creation
	project, err := projectService.CreateProject(ctx, ownerID, services.CreateProjectInput{
		Name:            "Public Web Project",
		SourceType:      models.SourceTypeGitHubPublic,
		SourceReference: "publicorg/publicrepo",
		Branch:          "main",
		BuildContext:    ".",
		ImportID:        &readyImport.ID,
		Services: []services.CreateServiceInput{
			{
				Name:          "web",
				Role:          models.RoleFrontend,
				Runtime:       "nodejs",
				RuntimeType:   "nodejs",
				BuildStrategy: "dockerfile",
				InternalPort:  3000,
			},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "Public Web Project", project.Name)

	// Verify source record points to immutable snapshot and records commit SHA as fingerprint
	var snapshotPathVal, commitSHAVal, fingerprintVal string
	err = pool.QueryRow(ctx, `
		SELECT s.fingerprint, s.metadata->>'snapshot_path', s.metadata->>'commit_sha'
		FROM sources s
		JOIN projects p ON p.source_id = s.id
		WHERE p.id = $1
	`, project.ID).Scan(&fingerprintVal, &snapshotPathVal, &commitSHAVal)
	require.NoError(t, err)

	assert.Equal(t, expectedCommitSHA, fingerprintVal, "fingerprint must be the authoritative commit SHA")
	assert.Equal(t, expectedCommitSHA, commitSHAVal)
	assert.Equal(t, readyImport.SnapshotPath, snapshotPathVal, "sources must reference the immutable snapshot directory")

	// Verify import was marked completed
	finalImport, err := importService.GetImport(ctx, ownerID, imp.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ImportStatusCompleted, finalImport.Status)
	assert.Equal(t, project.ID.String(), finalImport.ProjectID.String())
}

// 2. Regression Test: Private repository with GitHub App installation and server-side verification
func TestGitHubImport_PrivateRepo_WithGitHubAppInstallation(t *testing.T) {
	importService, ghService, _, _, _, _ := setupImportTestServices(t)
	ctx := context.Background()
	ownerID := uuid.New()
	pool, _ := connectTestDB(t)

	_, err := pool.Exec(ctx, `INSERT INTO users (id, email, password_hash) VALUES ($1, $2, $3) ON CONFLICT (id) DO NOTHING`,
		ownerID, fmt.Sprintf("test-%s@example.com", ownerID.String()[:8]), "hash")
	require.NoError(t, err)

	installationID := int64(881122)
	_, err = pool.Exec(ctx, `
		INSERT INTO github_app_installations (user_id, installation_id, account_login, account_id, account_type, repository_selection)
		VALUES ($1, $2, 'private-org', 123456, 'Organization', 'all')
		ON CONFLICT (user_id, installation_id) DO NOTHING
	`, ownerID, installationID)
	require.NoError(t, err)

	// Configure mock token getter for installation token
	ghService.SetTokenGetterForTest(func(ctx context.Context, uid uuid.UUID) (string, string, error) {
		return "ghs_test_installation_token_abc", "", nil
	})

	var receivedAuthHeader string
	tarballBytes := createTestTarGz(map[string]string{
		"Dockerfile": "FROM alpine\nCMD [\"echo\", \"private app\"]",
	})

	mockGitHub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuthHeader = r.Header.Get("Authorization")

		switch {
		case r.URL.Path == "/repos/private-org/private-repo":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"id":             99902,
				"name":           "private-repo",
				"full_name":      "private-org/private-repo",
				"private":        true,
				"default_branch": "main",
			})
		case strings.HasPrefix(r.URL.Path, "/repos/private-org/private-repo/commits/"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"sha": "11223344556677889900aabbccddeeff11223344",
			})
		case strings.HasPrefix(r.URL.Path, "/repos/private-org/private-repo/tarball/"):
			w.Header().Set("Content-Type", "application/gzip")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(tarballBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockGitHub.Close()

	customClient := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			u, _ := url.Parse(mockGitHub.URL + req.URL.RequestURI())
			req.URL = u
			return http.DefaultTransport.RoundTrip(req)
		}),
	}
	ghService.SetHTTPClient(customClient)

	imp, err := importService.StartImport(ctx, ownerID, services.StartImportInput{
		Repository: "private-org/private-repo",
		Branch:     "main",
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		rec, _ := importService.GetImport(ctx, ownerID, imp.ID)
		return rec != nil && rec.Status == models.ImportStatusReady
	}, 5*time.Second, 100*time.Millisecond)

	// Verify the installation token was used
	assert.Equal(t, "Bearer ghs_test_installation_token_abc", receivedAuthHeader)
}

// 3. Regression Test: Inaccessible / private repository without access fails safely with actionable error
func TestGitHubImport_InaccessibleRepo_FailsWithActionableError(t *testing.T) {
	importService, ghService, _, _, _, _ := setupImportTestServices(t)
	ctx := context.Background()
	ownerID := uuid.New()
	pool, _ := connectTestDB(t)

	_, err := pool.Exec(ctx, `INSERT INTO users (id, email, password_hash) VALUES ($1, $2, $3) ON CONFLICT (id) DO NOTHING`,
		ownerID, fmt.Sprintf("test-%s@example.com", ownerID.String()[:8]), "hash")
	require.NoError(t, err)

	mockGitHub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message": "Not Found"}`))
	}))
	defer mockGitHub.Close()

	customClient := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			u, _ := url.Parse(mockGitHub.URL + req.URL.RequestURI())
			req.URL = u
			return http.DefaultTransport.RoundTrip(req)
		}),
	}
	ghService.SetHTTPClient(customClient)

	// Start import of nonexistent or inaccessible repo
	imp, err := importService.StartImport(ctx, ownerID, services.StartImportInput{
		Repository: "secret-corp/top-secret-code",
		Branch:     "main",
	})
	require.NoError(t, err)

	var failedImport *models.RepositoryImport
	require.Eventually(t, func() bool {
		rec, _ := importService.GetImport(ctx, ownerID, imp.ID)
		if rec != nil && rec.Status == models.ImportStatusFailed {
			failedImport = rec
			return true
		}
		return false
	}, 5*time.Second, 100*time.Millisecond)

	assert.Equal(t, models.ImportStatusFailed, failedImport.Status)
	assert.Equal(t, "not_found", failedImport.ErrorCode)
	assert.Contains(t, failedImport.ErrorMessage, "not found or access denied")
	assert.Empty(t, failedImport.CommitSHA, "no fake commit SHA should ever be generated")
}

// 4. Regression Test: Non-main branch resolution (e.g. develop or release/v1)
func TestGitHubImport_NonMainBranch_ResolvesAccurately(t *testing.T) {
	importService, ghService, _, _, _, _ := setupImportTestServices(t)
	ctx := context.Background()
	ownerID := uuid.New()
	pool, _ := connectTestDB(t)

	_, err := pool.Exec(ctx, `INSERT INTO users (id, email, password_hash) VALUES ($1, $2, $3) ON CONFLICT (id) DO NOTHING`,
		ownerID, fmt.Sprintf("test-%s@example.com", ownerID.String()[:8]), "hash")
	require.NoError(t, err)

	targetBranch := "feature/cool-redesign"
	expectedSHA := "f00ba4411223344556677889900aabbccddeeff0"

	var requestedCommitRefs []string
	tarballBytes := createTestTarGz(map[string]string{
		"index.html": "<h1>Cool Redesign</h1>",
	})

	mockGitHub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/myorg/coolrepo":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"id":             99903,
				"name":           "coolrepo",
				"full_name":      "myorg/coolrepo",
				"private":        false,
				"default_branch": "main",
			})
		case strings.HasPrefix(r.URL.Path, "/repos/myorg/coolrepo/commits/"):
			ref := strings.TrimPrefix(r.URL.Path, "/repos/myorg/coolrepo/commits/")
			requestedCommitRefs = append(requestedCommitRefs, ref)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"sha": expectedSHA,
			})
		case strings.HasPrefix(r.URL.Path, "/repos/myorg/coolrepo/tarball/"):
			w.Header().Set("Content-Type", "application/gzip")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(tarballBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockGitHub.Close()

	customClient := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			u, _ := url.Parse(mockGitHub.URL + req.URL.RequestURI())
			req.URL = u
			return http.DefaultTransport.RoundTrip(req)
		}),
	}
	ghService.SetHTTPClient(customClient)

	imp, err := importService.StartImport(ctx, ownerID, services.StartImportInput{
		Repository: "myorg/coolrepo",
		Branch:     targetBranch,
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		rec, _ := importService.GetImport(ctx, ownerID, imp.ID)
		return rec != nil && rec.Status == models.ImportStatusReady
	}, 5*time.Second, 100*time.Millisecond)

	require.NotEmpty(t, requestedCommitRefs)
	unescapedRef, _ := url.PathUnescape(requestedCommitRefs[0])
	assert.Contains(t, unescapedRef, "feature/cool-redesign")

	rec, err := importService.GetImport(ctx, ownerID, imp.ID)
	require.NoError(t, err)
	assert.Equal(t, targetBranch, rec.Branch)
	assert.Equal(t, expectedSHA, rec.CommitSHA)
}

// 5. Regression Test: Monorepo nested directory scoping
func TestGitHubImport_MonorepoDirectory_Scoping(t *testing.T) {
	importService, ghService, _, _, _, _ := setupImportTestServices(t)
	ctx := context.Background()
	ownerID := uuid.New()
	pool, _ := connectTestDB(t)

	_, err := pool.Exec(ctx, `INSERT INTO users (id, email, password_hash) VALUES ($1, $2, $3) ON CONFLICT (id) DO NOTHING`,
		ownerID, fmt.Sprintf("test-%s@example.com", ownerID.String()[:8]), "hash")
	require.NoError(t, err)

	tarballBytes := createTestTarGz(map[string]string{
		"apps/api/package.json": `{"name": "backend-api", "scripts": {"start": "node index.js"}}`,
		"apps/web/package.json": `{"name": "frontend-web", "scripts": {"start": "next start"}}`,
		"shared/utils.js":       `module.exports = {};`,
	})

	mockGitHub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/mono-org/monorepo":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"id":             99904,
				"name":           "monorepo",
				"full_name":      "mono-org/monorepo",
				"private":        false,
				"default_branch": "main",
			})
		case strings.HasPrefix(r.URL.Path, "/repos/mono-org/monorepo/commits/"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"sha": "99887766554433221100aabbccddeeff99887766",
			})
		case strings.HasPrefix(r.URL.Path, "/repos/mono-org/monorepo/tarball/"):
			w.Header().Set("Content-Type", "application/gzip")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(tarballBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockGitHub.Close()

	customClient := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			u, _ := url.Parse(mockGitHub.URL + req.URL.RequestURI())
			req.URL = u
			return http.DefaultTransport.RoundTrip(req)
		}),
	}
	ghService.SetHTTPClient(customClient)

	// Import scoped specifically to apps/web
	imp, err := importService.StartImport(ctx, ownerID, services.StartImportInput{
		Repository: "mono-org/monorepo",
		Branch:     "main",
		RootDir:    "apps/web",
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		rec, _ := importService.GetImport(ctx, ownerID, imp.ID)
		return rec != nil && rec.Status == models.ImportStatusReady
	}, 5*time.Second, 100*time.Millisecond)

	rec, err := importService.GetImport(ctx, ownerID, imp.ID)
	require.NoError(t, err)
	assert.Equal(t, "apps/web", rec.RootDir)
	require.NotEmpty(t, rec.DiscoveryResult)

	var disc discovery.DiscoveryResult
	err = json.Unmarshal(rec.DiscoveryResult, &disc)
	require.NoError(t, err)

	// Verify discovery found the service in apps/web
	require.NotEmpty(t, disc.Services)
	assert.Equal(t, "web", disc.Services[0].Name)
}

// 6. Regression Test: Expired and failed import pruning and directory cleanup
func TestGitHubImport_PruneExpiredImports_CleansDiskAndDatabase(t *testing.T) {
	importService, _, _, _, _, tempDir := setupImportTestServices(t)
	ctx := context.Background()
	ownerID := uuid.New()
	pool, _ := connectTestDB(t)

	_, err := pool.Exec(ctx, `INSERT INTO users (id, email, password_hash) VALUES ($1, $2, $3) ON CONFLICT (id) DO NOTHING`,
		ownerID, fmt.Sprintf("test-%s@example.com", ownerID.String()[:8]), "hash")
	require.NoError(t, err)

	expiredImportID := uuid.New()
	snapshotDir := filepath.Join(tempDir, "imports", expiredImportID.String())
	err = os.MkdirAll(snapshotDir, 0755)
	require.NoError(t, err)
	testFile := filepath.Join(snapshotDir, "dummy.txt")
	err = os.WriteFile(testFile, []byte("temporary data"), 0644)
	require.NoError(t, err)

	// Insert an expired import row (expires_at in the past)
	pastTime := time.Now().Add(-2 * time.Hour)
	_, err = pool.Exec(ctx, `
		INSERT INTO repository_imports (
			id, owner_id, repository_full_name, branch, root_dir, is_private,
			status, progress, stage, snapshot_path, expires_at, created_at, updated_at
		) VALUES ($1, $2, 'testorg/testrepo', 'main', '', false, 'ready', 100, 'ready', $3, $4, $4, $4)
	`, expiredImportID, ownerID, snapshotDir, pastTime)
	require.NoError(t, err)

	assert.DirExists(t, snapshotDir)

	// Run PruneExpiredImports
	prunedCount, err := importService.PruneExpiredImports(ctx)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, prunedCount, int64(1))

	// Snapshot directory must be removed from disk
	assert.NoDirExists(t, snapshotDir, "expired import directory must be cleaned up from disk")

	// DB record must be pruned
	var count int
	err = pool.QueryRow(ctx, `SELECT count(*) FROM repository_imports WHERE id = $1`, expiredImportID).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "expired import record must be pruned from database")
}
