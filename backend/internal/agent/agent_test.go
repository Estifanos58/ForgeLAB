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
	"time"

	"github.com/google/uuid"
)

func TestAgentServer_Status(t *testing.T) {
	srv := NewAgentServer(AgentServerConfig{Port: 4142})
	defer srv.Close()
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

func TestAgentServer_AuthRequired(t *testing.T) {
	srv := NewAgentServer(AgentServerConfig{Port: 4142})
	defer srv.Close()
	ts := httptest.NewServer(srv.Router())
	defer ts.Close()

	// 1. Unauthenticated request to /select-path must be rejected with 401
	payload, _ := json.Marshal(map[string]string{"path": t.TempDir()})
	resp, err := http.Post(ts.URL+"/api/agent/select-path", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized for unauthenticated request, got %d", resp.StatusCode)
	}

	// 2. Request with invalid token must also be rejected with 401
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/agent/select-path", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer invalid-random-token-1234")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized for invalid token, got %d", resp2.StatusCode)
	}
}

func TestAgentServer_CORS_OriginAllowlist(t *testing.T) {
	srv := NewAgentServer(AgentServerConfig{
		Port:           4142,
		AllowedOrigins: []string{"http://localhost:3000", "http://127.0.0.1:3000"},
	})
	defer srv.Close()
	ts := httptest.NewServer(srv.Router())
	defer ts.Close()

	// 1. Allowed origin preflight returns 200 with matching CORS headers
	req, _ := http.NewRequest(http.MethodOptions, ts.URL+"/api/agent/status", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK for allowed origin preflight, got %d", resp.StatusCode)
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != "http://localhost:3000" {
		t.Errorf("expected Access-Control-Allow-Origin to be http://localhost:3000, got %s", resp.Header.Get("Access-Control-Allow-Origin"))
	}

	// 2. Disallowed external origin is rejected with 403 Forbidden
	reqEvil, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/agent/status", nil)
	reqEvil.Header.Set("Origin", "http://malicious-site.com")
	respEvil, err := http.DefaultClient.Do(reqEvil)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	respEvil.Body.Close()
	if respEvil.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for disallowed origin, got %d", respEvil.StatusCode)
	}
}

func TestAgentServer_SelectPath_ValidAndAnalyze(t *testing.T) {
	tempDir := t.TempDir()

	// Create frontend and backend mock services
	_ = os.MkdirAll(filepath.Join(tempDir, "frontend"), 0755)
	_ = os.WriteFile(filepath.Join(tempDir, "frontend", "package.json"), []byte(`{"name":"web","dependencies":{"next":"14.0.0"}}`), 0644)

	_ = os.MkdirAll(filepath.Join(tempDir, "backend"), 0755)
	_ = os.WriteFile(filepath.Join(tempDir, "backend", "go.mod"), []byte("module api\ngo 1.22"), 0644)

	sm := GetGlobalSessionManager()
	testUser := uuid.New()
	authSession, err := sm.CreateSession(testUser, "agent-test-1", 10*time.Minute)
	if err != nil {
		t.Fatalf("failed to create auth session: %v", err)
	}

	srv := NewAgentServer(AgentServerConfig{
		Port: 4142,
		SessionValidator: func(token, agentID string) (*AgentSession, error) {
			if token == authSession.Token {
				return authSession, nil
			}
			return nil, ErrSessionNotFound
		},
	})
	defer srv.Close()
	ts := httptest.NewServer(srv.Router())
	defer ts.Close()

	payload, _ := json.Marshal(map[string]string{"path": tempDir})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/agent/select-path", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+authSession.Token)

	resp, err := http.DefaultClient.Do(req)
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

	// Verify path is NOT exposed in response
	respBytes, _ := json.Marshal(sessionResp)
	if strings.Contains(string(respBytes), tempDir) {
		t.Errorf("security violation: raw filesystem path leaked in agent API response: %s", string(respBytes))
	}

	// Verify GET /api/agent/sources/:id with token
	getReq, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/agent/sources/"+sourceID, nil)
	getReq.Header.Set("Authorization", "Bearer "+authSession.Token)
	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil || getResp.StatusCode != http.StatusOK {
		t.Errorf("failed to retrieve registered source: %v", err)
	}
	getResp.Body.Close()

	// Verify DELETE /api/agent/sources/:id with token
	delReq, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/agent/sources/"+sourceID, nil)
	delReq.Header.Set("Authorization", "Bearer "+authSession.Token)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil || delResp.StatusCode != http.StatusOK {
		t.Errorf("failed to delete source session: %v", err)
	}
	delResp.Body.Close()

	// Verify source now returns 404
	afterDelReq, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/agent/sources/"+sourceID, nil)
	afterDelReq.Header.Set("Authorization", "Bearer "+authSession.Token)
	afterDel, _ := http.DefaultClient.Do(afterDelReq)
	if afterDel.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 after deletion, got %d", afterDel.StatusCode)
	}
	afterDel.Body.Close()
}

func TestAgentServer_SessionExpiryAndCleanup(t *testing.T) {
	sm := NewSessionManager()
	defer sm.Stop()

	testUser := uuid.New()
	// Create session with 50ms TTL
	sess, err := sm.CreateSession(testUser, "agent-expiring", 50*time.Millisecond)
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	// Immediate validation succeeds
	v1, err := sm.ValidateToken(sess.Token)
	if err != nil || v1.ID != sess.ID {
		t.Fatalf("expected valid session, got err: %v", err)
	}

	// Wait for TTL expiry
	time.Sleep(100 * time.Millisecond)

	// Validation now returns ErrSessionExpired
	_, err = sm.ValidateToken(sess.Token)
	if err != ErrSessionExpired {
		t.Fatalf("expected ErrSessionExpired, got %v", err)
	}

	// Cleanup removes expired session
	cleaned := sm.CleanupExpired()
	if cleaned != 1 {
		t.Errorf("expected 1 session cleaned, got %d", cleaned)
	}
}

func TestAgentServer_RegistrationVerification_NoSpoofing(t *testing.T) {
	sm := NewSessionManager()
	defer sm.Stop()

	userA := uuid.New()
	userB := uuid.New()
	sourceID := uuid.New()

	// User A creates an agent session
	sess, err := sm.CreateSession(userA, "agent-123", 10*time.Minute)
	if err != nil {
		t.Fatalf("create session error: %v", err)
	}

	// Bind source to session
	_, err = sm.BindSource(sess.Token, sourceID, "my-repo", "agent-123")
	if err != nil {
		t.Fatalf("bind source error: %v", err)
	}

	// 1. User B cannot register User A's session (ownership verification)
	_, err = sm.VerifyForRegistration(userB, sess.ID, sess.Token)
	if err != ErrUnauthorized {
		t.Fatalf("expected ErrUnauthorized for user B, got %v", err)
	}

	// 2. User A with correct session succeeds
	verified, err := sm.VerifyForRegistration(userA, sess.ID, sess.Token)
	if err != nil {
		t.Fatalf("expected registration verification to succeed for user A, got %v", err)
	}
	if *verified.SourceID != sourceID {
		t.Fatalf("expected verified source ID %s, got %s", sourceID, *verified.SourceID)
	}
	if verified.AgentID != "agent-123" {
		t.Fatalf("expected verified agent ID 'agent-123', got %s", verified.AgentID)
	}
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
}

func TestAgentServer_StreamContext(t *testing.T) {
	tempDir := t.TempDir()
	serviceDir := filepath.Join(tempDir, "frontend")
	_ = os.MkdirAll(serviceDir, 0755)
	_ = os.WriteFile(filepath.Join(serviceDir, "index.js"), []byte("console.log('hello')"), 0644)
	_ = os.WriteFile(filepath.Join(serviceDir, ".env"), []byte("SECRET=1234"), 0600) // Secret must be pruned!

	sm := GetGlobalSessionManager()
	testUser := uuid.New()
	authSession, err := sm.CreateSession(testUser, "agent-test-stream", 10*time.Minute)
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	srv := NewAgentServer(AgentServerConfig{
		Port: 4142,
		SessionValidator: func(token, agentID string) (*AgentSession, error) {
			if token == authSession.Token {
				return authSession, nil
			}
			return nil, ErrSessionNotFound
		},
	})
	defer srv.Close()

	session, err := srv.registerDirectory(tempDir, authSession.Token)
	if err != nil {
		t.Fatalf("failed to register directory: %v", err)
	}

	ts := httptest.NewServer(srv.Router())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/agent/sources/"+session.SourceID.String()+"/stream-context?service_path=frontend", nil)
	req.Header.Set("Authorization", "Bearer "+authSession.Token)

	resp, err := http.DefaultClient.Do(req)
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
