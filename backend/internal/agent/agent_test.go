package agent

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAgentServer_Status(t *testing.T) {
	srv := NewAgentServer(AgentServerConfig{Port: 4142})
	ts := httptest.NewServer(srv.Router())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/agent/status")
	if err != nil {
		t.Fatalf("failed to query agent status: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", resp.StatusCode)
	}

	var data map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if data["status"] != "online" {
		t.Errorf("expected status 'online', got %v", data["status"])
	}
	if data["os"] != runtime.GOOS {
		t.Errorf("expected os %q, got %v", runtime.GOOS, data["os"])
	}
}

func TestAgentServer_SelectPath_ValidAndAnalyze(t *testing.T) {
	tempDir := t.TempDir()

	// Create frontend and backend
	_ = os.MkdirAll(filepath.Join(tempDir, "frontend"), 0755)
	_ = os.WriteFile(filepath.Join(tempDir, "frontend", "package.json"), []byte(`{"name":"web","dependencies":{"next":"14.0.0"}}`), 0644)

	_ = os.MkdirAll(filepath.Join(tempDir, "backend"), 0755)
	_ = os.WriteFile(filepath.Join(tempDir, "backend", "go.mod"), []byte("module api\ngo 1.22"), 0644)

	srv := NewAgentServer(AgentServerConfig{Port: 4142})
	ts := httptest.NewServer(srv.Router())
	defer ts.Close()

	payload, _ := json.Marshal(map[string]string{"path": tempDir})
	resp, err := http.Post(ts.URL+"/api/agent/select-path", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 OK, got %d: %s", resp.StatusCode, string(body))
	}

	var sessionResp map[string]interface{}
	_ = json.NewDecoder(resp.Body).Decode(&sessionResp)

	sourceID := sessionResp["source_id"].(string)
	if sourceID == "" {
		t.Fatalf("expected non-empty source_id")
	}

	services := sessionResp["services"].([]interface{})
	if len(services) != 2 {
		t.Fatalf("expected 2 discovered services, got %d", len(services))
	}

	// Verify path is NOT exposed in response!
	respBytes, _ := json.Marshal(sessionResp)
	if strings.Contains(string(respBytes), tempDir) {
		t.Errorf("security violation: raw filesystem path leaked in agent API response: %s", string(respBytes))
	}

	// Verify GET /api/agent/sources/:id
	getResp, err := http.Get(ts.URL + "/api/agent/sources/" + sourceID)
	if err != nil || getResp.StatusCode != http.StatusOK {
		t.Errorf("failed to retrieve registered source: %v", err)
	}
	getResp.Body.Close()

	// Verify DELETE /api/agent/sources/:id
	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/agent/sources/"+sourceID, nil)
	delResp, err := http.DefaultClient.Do(req)
	if err != nil || delResp.StatusCode != http.StatusOK {
		t.Errorf("failed to delete source session: %v", err)
	}
	delResp.Body.Close()

	// Verify source now returns 404
	afterDel, _ := http.Get(ts.URL + "/api/agent/sources/" + sourceID)
	if afterDel.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 after deletion, got %d", afterDel.StatusCode)
	}
	afterDel.Body.Close()
}

func TestAgentServer_Security_RestrictedPathsAndTraversal(t *testing.T) {
	tempDir := t.TempDir()
	allowedDir := filepath.Join(tempDir, "allowed")
	disallowedDir := filepath.Join(tempDir, "disallowed")
	_ = os.MkdirAll(allowedDir, 0755)
	_ = os.MkdirAll(disallowedDir, 0755)

	validator := NewPathValidator([]string{allowedDir})

	// 1. Allowed path succeeds
	validPath, err := validator.ValidateSourcePath(allowedDir)
	if err != nil {
		t.Errorf("expected allowed path to succeed, got %v", err)
	}
	if validPath != allowedDir {
		t.Errorf("expected %s, got %s", allowedDir, validPath)
	}

	// 2. Disallowed path fails
	_, err = validator.ValidateSourcePath(disallowedDir)
	if err != ErrPathOutsideAllowedRoot {
		t.Errorf("expected ErrPathOutsideAllowedRoot, got %v", err)
	}

	// 3. Traversal escapes allowed root
	traversalPath := filepath.Join(allowedDir, "..", "disallowed")
	_, err = validator.ValidateSourcePath(traversalPath)
	if err != ErrPathOutsideAllowedRoot {
		t.Errorf("expected ErrPathOutsideAllowedRoot on traversal, got %v", err)
	}

	// 4. Non-existent path fails
	_, err = validator.ValidateSourcePath(filepath.Join(allowedDir, "does-not-exist"))
	if err != ErrPathNotExist {
		t.Errorf("expected ErrPathNotExist, got %v", err)
	}

	// 5. System path fails
	unrestrictedValidator := NewPathValidator(nil)
	if runtime.GOOS == "windows" {
		_, err = unrestrictedValidator.ValidateSourcePath(`C:\Windows`)
		if err != ErrRestrictedSystemPath {
			t.Errorf("expected ErrRestrictedSystemPath for C:\\Windows, got %v", err)
		}
	} else {
		_, err = unrestrictedValidator.ValidateSourcePath(`/etc`)
		if err != ErrRestrictedSystemPath {
			t.Errorf("expected ErrRestrictedSystemPath for /etc, got %v", err)
		}
	}
}

func TestAgentServer_StreamContext(t *testing.T) {
	tempDir := t.TempDir()
	serviceDir := filepath.Join(tempDir, "frontend")
	_ = os.MkdirAll(serviceDir, 0755)
	_ = os.WriteFile(filepath.Join(serviceDir, "index.js"), []byte("console.log('hello')"), 0644)
	_ = os.WriteFile(filepath.Join(serviceDir, ".env"), []byte("SECRET=1234"), 0600) // Secret must be pruned!

	srv := NewAgentServer(AgentServerConfig{Port: 4142})
	session, err := srv.registerDirectory(tempDir)
	if err != nil {
		t.Fatalf("failed to register directory: %v", err)
	}

	ts := httptest.NewServer(srv.Router())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/agent/sources/" + session.SourceID.String() + "/stream-context?service_path=frontend")
	if err != nil {
		t.Fatalf("failed to stream context: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	tarReader := tar.NewReader(resp.Body)
	var foundIndex, foundSecret bool

	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar read error: %v", err)
		}

		if header.Name == "index.js" {
			foundIndex = true
		}
		if strings.Contains(header.Name, ".env") {
			foundSecret = true
		}
	}

	if !foundIndex {
		t.Errorf("expected index.js in streamed tar")
	}
	if foundSecret {
		t.Errorf("security violation: .env secret file included in streamed tar!")
	}
}
