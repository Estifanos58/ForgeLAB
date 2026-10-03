package agent

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestAgentServer_SelectPath_PollingLifecycle_SameToken(t *testing.T) {
	tempDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(tempDir, "package.json"), []byte(`{"name":"web","dependencies":{"react":"19.0.0"}}`), 0644)

	sm := GetGlobalSessionManager()
	testUser := uuid.New()
	authSession, err := sm.CreateSession(testUser, "agent-poll-test", 10*time.Minute)
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

	// 1. Select path
	payload, _ := json.Marshal(map[string]string{"path": tempDir})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/agent/select-path", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+authSession.Token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("select-path failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	var sessionResp map[string]interface{}
	_ = json.NewDecoder(resp.Body).Decode(&sessionResp)
	sourceID := sessionResp["source_id"].(string)

	// 2. Poll session with exact matching token until status reaches ready
	pollReq, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/agent/sources/"+sourceID, nil)
	pollReq.Header.Set("Authorization", "Bearer "+authSession.Token)

	pollResp, err := http.DefaultClient.Do(pollReq)
	if err != nil {
		t.Fatalf("poll failed: %v", err)
	}
	defer pollResp.Body.Close()

	if pollResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on poll, got %d", pollResp.StatusCode)
	}

	var pollData map[string]interface{}
	_ = json.NewDecoder(pollResp.Body).Decode(&pollData)

	if pollData["status"] != "ready" {
		t.Errorf("expected status 'ready', got %v", pollData["status"])
	}
	if pollData["phase"] != "ready" {
		t.Errorf("expected phase 'ready', got %v", pollData["phase"])
	}
}

func TestAgentServer_ChangeFolders_OldTokenCannotPollNewSource(t *testing.T) {
	tempDir1 := t.TempDir()
	_ = os.WriteFile(filepath.Join(tempDir1, "package.json"), []byte(`{"name":"app-one"}`), 0644)

	tempDir2 := t.TempDir()
	_ = os.WriteFile(filepath.Join(tempDir2, "package.json"), []byte(`{"name":"app-two"}`), 0644)

	sm := GetGlobalSessionManager()
	testUser := uuid.New()

	authSession1, _ := sm.CreateSession(testUser, "agent-test-1", 10*time.Minute)
	authSession2, _ := sm.CreateSession(testUser, "agent-test-1", 10*time.Minute)

	srv := NewAgentServer(AgentServerConfig{
		Port: 4142,
		SessionValidator: func(token, agentID string) (*AgentSession, error) {
			if token == authSession1.Token {
				return authSession1, nil
			}
			if token == authSession2.Token {
				return authSession2, nil
			}
			return nil, ErrSessionNotFound
		},
	})
	defer srv.Close()
	ts := httptest.NewServer(srv.Router())
	defer ts.Close()

	// 1. User selects folder 1 with token 1
	p1, _ := json.Marshal(map[string]string{"path": tempDir1})
	req1, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/agent/select-path", bytes.NewReader(p1))
	req1.Header.Set("Authorization", "Bearer "+authSession1.Token)
	resp1, err := http.DefaultClient.Do(req1)
	if err != nil || resp1.StatusCode != http.StatusOK {
		t.Fatalf("folder 1 selection failed")
	}
	var res1 map[string]interface{}
	_ = json.NewDecoder(resp1.Body).Decode(&res1)
	resp1.Body.Close()

	// 2. User changes folders -> selects folder 2 with token 2
	p2, _ := json.Marshal(map[string]string{"path": tempDir2})
	req2, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/agent/select-path", bytes.NewReader(p2))
	req2.Header.Set("Authorization", "Bearer "+authSession2.Token)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil || resp2.StatusCode != http.StatusOK {
		t.Fatalf("folder 2 selection failed")
	}
	var res2 map[string]interface{}
	_ = json.NewDecoder(resp2.Body).Decode(&res2)
	resp2.Body.Close()
	sourceID2 := res2["source_id"].(string)

	// 3. User attempts to poll folder 2's source with the old token 1
	// MUST be rejected with 403 Forbidden because token does not match source session!
	badPoll, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/agent/sources/"+sourceID2, nil)
	badPoll.Header.Set("Authorization", "Bearer "+authSession1.Token)
	badResp, err := http.DefaultClient.Do(badPoll)
	if err != nil {
		t.Fatalf("poll failed: %v", err)
	}
	defer badResp.Body.Close()

	if badResp.StatusCode != http.StatusForbidden {
		t.Fatalf("security violation: expected 403 Forbidden for mismatched session token, got %d", badResp.StatusCode)
	}

	var errBody map[string]string
	_ = json.NewDecoder(badResp.Body).Decode(&errBody)
	if !strings.Contains(errBody["error"], "does not match source session") {
		t.Errorf("expected 'does not match source session' in error, got %q", errBody["error"])
	}

	// 4. Polling folder 2 with matching token 2 succeeds with 200 OK
	goodPoll, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/agent/sources/"+sourceID2, nil)
	goodPoll.Header.Set("Authorization", "Bearer "+authSession2.Token)
	goodResp, err := http.DefaultClient.Do(goodPoll)
	if err != nil {
		t.Fatalf("good poll failed: %v", err)
	}
	defer goodResp.Body.Close()

	if goodResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK with correct token, got %d", goodResp.StatusCode)
	}
}

func TestAgentServer_HeaderOnlyAuth_QueryStringRejected(t *testing.T) {
	tempDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(tempDir, "package.json"), []byte(`{"name":"test"}`), 0644)

	sm := GetGlobalSessionManager()
	testUser := uuid.New()
	authSession, _ := sm.CreateSession(testUser, "agent-test-1", 10*time.Minute)

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

	sess, err := srv.registerDirectory(tempDir, authSession.Token)
	if err != nil {
		t.Fatalf("failed to register directory: %v", err)
	}

	ts := httptest.NewServer(srv.Router())
	defer ts.Close()

	// 1. Query parameter token ?token=... is rejected with 401 Unauthorized
	queryURL := fmt.Sprintf("%s/api/agent/sources/%s?token=%s", ts.URL, sess.SourceID, authSession.Token)
	resp, err := http.Get(queryURL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized for query string token, got %d", resp.StatusCode)
	}

	// 2. Header X-Agent-Session-Token is accepted with 200 OK
	reqHeader, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/agent/sources/"+sess.SourceID.String(), nil)
	reqHeader.Header.Set("X-Agent-Session-Token", authSession.Token)
	respHeader, err := http.DefaultClient.Do(reqHeader)
	if err != nil {
		t.Fatalf("request with X-Agent-Session-Token failed: %v", err)
	}
	defer respHeader.Body.Close()

	if respHeader.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK with X-Agent-Session-Token, got %d", respHeader.StatusCode)
	}
}

func TestAgentServer_Picker_CancellationVsFailureVsBusy(t *testing.T) {
	picker := NewNativeFolderPicker()

	// 1. Context cancellation returns ErrPickerCancelled
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel context

	_, err := picker.PickFolder(ctx, "Test Title")
	if !errors.Is(err, ErrPickerCancelled) {
		t.Errorf("expected ErrPickerCancelled on cancelled context, got %v", err)
	}

	// 2. Picker busy concurrency guard
	picker.picking = true
	_, busyErr := picker.PickFolder(context.Background(), "Another Title")
	if !errors.Is(busyErr, ErrPickerBusy) {
		t.Errorf("expected ErrPickerBusy when picker is running, got %v", busyErr)
	}
	picker.picking = false
}

func TestAgentServer_SymlinkEscape_Rejected(t *testing.T) {
	tempDir := t.TempDir()
	outsideDir := filepath.Join(tempDir, "outside_secret")
	_ = os.MkdirAll(outsideDir, 0755)
	_ = os.WriteFile(filepath.Join(outsideDir, "secret_password.txt"), []byte("super-secret"), 0644)

	repoDir := filepath.Join(tempDir, "project")
	serviceDir := filepath.Join(repoDir, "service")
	_ = os.MkdirAll(serviceDir, 0755)
	_ = os.WriteFile(filepath.Join(serviceDir, "index.js"), []byte("console.log('hi')"), 0644)

	// Create symlink escaping outside serviceDir boundary
	escapeLink := filepath.Join(serviceDir, "escape_link")
	symlinkErr := os.Symlink(outsideDir, escapeLink)
	if symlinkErr != nil {
		t.Skip("Symlink creation not permitted in this environment, skipping symlink escape test")
	}

	sm := GetGlobalSessionManager()
	testUser := uuid.New()
	authSession, _ := sm.CreateSession(testUser, "agent-test-symlink", 10*time.Minute)

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

	session, err := srv.registerDirectory(repoDir, authSession.Token)
	if err != nil {
		t.Fatalf("failed to register directory: %v", err)
	}

	ts := httptest.NewServer(srv.Router())
	defer ts.Close()

	streamURL := fmt.Sprintf("%s/api/agent/sources/%s/stream-context?service_path=service", ts.URL, session.SourceID)
	req, _ := http.NewRequest(http.MethodGet, streamURL, nil)
	req.Header.Set("Authorization", "Bearer "+authSession.Token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("stream-context failed: %v", err)
	}
	defer resp.Body.Close()

	// Read tar stream and verify outside secret file was NOT included
	tarReader := tar.NewReader(resp.Body)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		if strings.Contains(header.Name, "secret_password.txt") {
			t.Fatalf("security violation: outside symlink target leaked into streamed tar archive!")
		}
	}
}

func TestAgentServer_Lifecycle_ConsumedSession(t *testing.T) {
	tempDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(tempDir, "package.json"), []byte(`{"name":"consumed-test"}`), 0644)

	sm := GetGlobalSessionManager()
	testUser := uuid.New()
	authSession, _ := sm.CreateSession(testUser, "agent-test-consume", 10*time.Minute)

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

	// 1. Consume session on local agent
	consumeURL := fmt.Sprintf("%s/api/agent/sources/%s/consume", ts.URL, session.SourceID)
	req, _ := http.NewRequest(http.MethodPost, consumeURL, nil)
	req.Header.Set("Authorization", "Bearer "+authSession.Token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("consume request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on consume, got %d", resp.StatusCode)
	}

	var data map[string]interface{}
	_ = json.NewDecoder(resp.Body).Decode(&data)
	if data["status"] != "consumed" {
		t.Errorf("expected status 'consumed', got %v", data["status"])
	}

	// 2. Mark consumed in SessionManager and verify subsequent validation fails
	if err := sm.MarkConsumed(authSession.ID); err != nil {
		t.Fatalf("mark consumed error: %v", err)
	}

	_, valErr := sm.ValidateToken(authSession.Token)
	if !errors.Is(valErr, ErrSessionConsumed) {
		t.Errorf("expected ErrSessionConsumed on ValidateToken, got %v", valErr)
	}

	_, regErr := sm.VerifyForRegistration(testUser, authSession.ID, authSession.Token)
	if !errors.Is(regErr, ErrSessionConsumed) {
		t.Errorf("expected ErrSessionConsumed on VerifyForRegistration, got %v", regErr)
	}
}

func TestAgentServer_StreamContext_AuthFlowAndTokenIsolation(t *testing.T) {
	tempDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(tempDir, "server.js"), []byte("console.log('hi');"), 0644)
	_ = os.WriteFile(filepath.Join(tempDir, "package.json"), []byte(`{"name":"test"}`), 0644)

	validToken := "secure-agent-session-token-9988"
	testUser := uuid.New()
	authSession := &AgentSession{
		ID:        uuid.New(),
		Token:     validToken,
		UserID:    testUser,
		AgentID:   "test-agent-auth",
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(10 * time.Minute),
	}

	srv := NewAgentServer(AgentServerConfig{
		Port: 4142,
		SessionValidator: func(token, agentID string) (*AgentSession, error) {
			if token == validToken {
				return authSession, nil
			}
			return nil, ErrSessionNotFound
		},
	})
	defer srv.Close()

	session, err := srv.registerDirectory(tempDir, validToken)
	if err != nil {
		t.Fatalf("failed to register directory: %v", err)
	}

	ts := httptest.NewServer(srv.Router())
	defer ts.Close()

	streamURL := fmt.Sprintf("%s/api/agent/sources/%s/stream-context", ts.URL, session.SourceID)

	// 1. Unauthenticated request without headers must be rejected with 401 Unauthorized
	unauthReq, _ := http.NewRequest(http.MethodGet, streamURL, nil)
	resp, err := http.DefaultClient.Do(unauthReq)
	if err != nil {
		t.Fatalf("unauthenticated request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized for missing auth header, got %d", resp.StatusCode)
	}

	// 2. Query param only (?token=...) without Authorization header must NOT authenticate (prevents URL token leakage)
	queryReq, _ := http.NewRequest(http.MethodGet, streamURL+"?token="+validToken, nil)
	resp, err = http.DefaultClient.Do(queryReq)
	if err != nil {
		t.Fatalf("query-param request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized when token passed via query param only, got %d", resp.StatusCode)
	}

	// 3. Invalid token in Authorization header must be rejected with 401 Unauthorized
	badTokenReq, _ := http.NewRequest(http.MethodGet, streamURL, nil)
	badTokenReq.Header.Set("Authorization", "Bearer invalid-token-xyz")
	resp, err = http.DefaultClient.Do(badTokenReq)
	if err != nil {
		t.Fatalf("bad token request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 401 or 403 for bad token, got %d", resp.StatusCode)
	}

	// 4. Valid token via Authorization: Bearer must succeed with 200 OK and stream valid tarball
	bearerReq, _ := http.NewRequest(http.MethodGet, streamURL, nil)
	bearerReq.Header.Set("Authorization", "Bearer "+validToken)
	resp, err = http.DefaultClient.Do(bearerReq)
	if err != nil {
		t.Fatalf("bearer request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK with Bearer token, got %d", resp.StatusCode)
	}

	tr := tar.NewReader(resp.Body)
	var foundPackageJSON bool
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar read error: %v", err)
		}
		if hdr.Name == "package.json" {
			foundPackageJSON = true
		}
	}
	if !foundPackageJSON {
		t.Errorf("expected package.json in tar stream")
	}

	// 5. Valid token via X-Agent-Session-Token header must also succeed with 200 OK
	customHeaderReq, _ := http.NewRequest(http.MethodGet, streamURL, nil)
	customHeaderReq.Header.Set("X-Agent-Session-Token", validToken)
	resp2, err := http.DefaultClient.Do(customHeaderReq)
	if err != nil {
		t.Fatalf("custom header request failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK with X-Agent-Session-Token, got %d", resp2.StatusCode)
	}
}

func TestAgentServer_ConsumedSession_SourceBoundAccess(t *testing.T) {
	tempDir1 := t.TempDir()
	_ = os.WriteFile(filepath.Join(tempDir1, "index.js"), []byte("console.log('service 1');"), 0644)
	tempDir2 := t.TempDir()
	_ = os.WriteFile(filepath.Join(tempDir2, "index.js"), []byte("console.log('service 2');"), 0644)

	sm := GetGlobalSessionManager()
	testUser := uuid.New()
	agentID := "test-agent-consumed-regression"
	authSession, err := sm.CreateSession(testUser, agentID, 10*time.Minute)
	require.NoError(t, err)

	srv := NewAgentServer(AgentServerConfig{
		AgentID: agentID,
		Port:    4142,
	})
	defer srv.Close()

	// 1. Register source 1 with unconsumed token
	session1, err := srv.registerDirectory(tempDir1, authSession.Token)
	require.NoError(t, err)
	require.NotNil(t, session1)

	// Verify session is bound in sm
	boundSess, err := sm.VerifyTokenForSource(authSession.Token, session1.SourceID, agentID)
	require.NoError(t, err)
	assert.Equal(t, session1.SourceID, *boundSess.SourceID)

	ts := httptest.NewServer(srv.Router())
	defer ts.Close()

	// 2. Mark session as consumed (simulating project creation)
	require.NoError(t, sm.MarkConsumed(authSession.ID))
	require.NoError(t, srv.ConsumeSession(session1.SourceID))

	// 3. Regression test: Same consumed token CAN access its original bound source
	streamURL1 := fmt.Sprintf("%s/api/agent/sources/%s/stream-context?service_path=.", ts.URL, session1.SourceID)
	req1, _ := http.NewRequest(http.MethodGet, streamURL1, nil)
	req1.Header.Set("Authorization", "Bearer "+authSession.Token)
	resp1, err := http.DefaultClient.Do(req1)
	require.NoError(t, err)
	defer resp1.Body.Close()
	assert.Equal(t, http.StatusOK, resp1.StatusCode, "consumed session must be allowed to access its original bound source")

	// 4. Regression test: Same consumed token CANNOT select another folder/path
	selectPayload, _ := json.Marshal(map[string]string{"path": tempDir2})
	selectReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/agent/select-path", bytes.NewReader(selectPayload))
	selectReq.Header.Set("Content-Type", "application/json")
	selectReq.Header.Set("Authorization", "Bearer "+authSession.Token)
	selectResp, err := http.DefaultClient.Do(selectReq)
	require.NoError(t, err)
	defer selectResp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, selectResp.StatusCode, "consumed token must NOT be allowed to select new paths")

	// 5. Regression test: Same consumed token CANNOT access or bind to a different source
	otherSourceID := uuid.New()
	_, err = sm.VerifyTokenForSource(authSession.Token, otherSourceID, agentID)
	assert.ErrorIs(t, err, ErrSessionConsumed, "consumed session cannot access a different source")

	_, err = sm.BindSource(authSession.Token, otherSourceID, "other", agentID)
	assert.ErrorIs(t, err, ErrSessionConsumed, "consumed session cannot bind to another source")

	streamURL2 := fmt.Sprintf("%s/api/agent/sources/%s/stream-context", ts.URL, otherSourceID)
	req2, _ := http.NewRequest(http.MethodGet, streamURL2, nil)
	req2.Header.Set("Authorization", "Bearer "+authSession.Token)
	resp2, err := http.DefaultClient.Do(req2)
	require.NoError(t, err)
	defer resp2.Body.Close()
	assert.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound}, resp2.StatusCode)

	// 6. Regression test: Expired token is rejected even for the bound source
	expiredSession, err := sm.CreateSession(testUser, agentID, 10*time.Minute)
	require.NoError(t, err)
	expiredSession.ExpiresAt = time.Now().Add(-1 * time.Minute)
	_, err = sm.VerifyTokenForSource(expiredSession.Token, session1.SourceID, agentID)
	assert.ErrorIs(t, err, ErrSessionExpired, "expired session must be rejected")
}


