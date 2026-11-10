package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentServer_ListenAddrFormatting(t *testing.T) {
	srvDefault := NewAgentServer(AgentServerConfig{
		Port:         4142,
		DockerPinger: func(context.Context) error { return nil },
	})
	defer srvDefault.Close()
	assert.Equal(t, "127.0.0.1:4142", srvDefault.ListenAddr())

	srvCustom := NewAgentServer(AgentServerConfig{
		Host:         "0.0.0.0",
		Port:         4199,
		DockerPinger: func(context.Context) error { return nil },
	})
	defer srvCustom.Close()
	assert.Equal(t, "0.0.0.0:4199", srvCustom.ListenAddr())

	srvIPv6 := NewAgentServer(AgentServerConfig{
		Host:         "::1",
		Port:         4142,
		DockerPinger: func(context.Context) error { return nil },
	})
	defer srvIPv6.Close()
	assert.Equal(t, "[::1]:4142", srvIPv6.ListenAddr())

	srvDefault.SetBoundAddr("127.0.0.1:59123")
	assert.Equal(t, "127.0.0.1:59123", srvDefault.ListenAddr())
}

func TestAgentServer_StatusIndependentOfBlockedDockerPing(t *testing.T) {
	pingStarted := make(chan struct{}, 1)
	srv := NewAgentServer(AgentServerConfig{
		Port:                4142,
		DockerPingTimeout:   5 * time.Second,
		DockerCheckInterval: 5 * time.Second,
		DockerPinger: func(ctx context.Context) error {
			select {
			case pingStarted <- struct{}{}:
			default:
			}
			<-ctx.Done()
			return ctx.Err()
		},
	})

	select {
	case <-pingStarted:
	case <-time.After(1 * time.Second):
		t.Fatal("background docker monitor did not start promptly")
	}

	ts := httptest.NewServer(srv.Router())
	defer ts.Close()

	start := time.Now()
	resp, err := http.Get(ts.URL + "/api/agent/status")
	elapsed := time.Since(start)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Less(t, elapsed, 500*time.Millisecond, "/api/agent/status must not block waiting on Docker ping")

	var body map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "online", body["status"])
	assert.Equal(t, false, body["docker_available"])
	assert.Equal(t, "checking", body["docker_status"])

	dockerObj, ok := body["docker"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, false, dockerObj["available"])
	assert.Equal(t, "checking", dockerObj["status"])

	closeStart := time.Now()
	srv.Close()
	assert.Less(t, time.Since(closeStart), 1*time.Second, "Close must cancel blocked Docker ping promptly")
}

func TestAgentServer_StatusCachesDockerAvailabilityAndErrors(t *testing.T) {
	var healthy atomic.Bool
	srv := NewAgentServer(AgentServerConfig{
		Port:                4142,
		DockerCheckInterval: 20 * time.Millisecond,
		DockerPingTimeout:   200 * time.Millisecond,
		DockerPinger: func(context.Context) error {
			if !healthy.Load() {
				return errors.New("error during connect: open //./pipe/docker_engine: The system cannot find the file specified.")
			}
			return nil
		},
	})
	defer srv.Close()

	ts := httptest.NewServer(srv.Router())
	defer ts.Close()

	// Wait for unavailable cached status
	require.Eventually(t, func() bool {
		resp, err := http.Get(ts.URL + "/api/agent/status")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		var body map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return false
		}
		return body["docker_status"] == "unavailable" &&
			body["status"] == "online" &&
			body["docker_available"] == false &&
			body["docker_error"] == "Docker daemon is unreachable (start Docker Desktop or the Docker service)" &&
			body["docker_checked_at"] != ""
	}, 2*time.Second, 15*time.Millisecond)

	// Transition Docker to healthy and verify background monitor updates cache
	healthy.Store(true)
	require.Eventually(t, func() bool {
		resp, err := http.Get(ts.URL + "/api/agent/status")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		var body map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return false
		}
		_, hasErr := body["docker_error"]
		return body["docker_status"] == "available" &&
			body["docker_available"] == true &&
			!hasErr
	}, 2*time.Second, 15*time.Millisecond)
}

func TestAgentServer_StatusAuthScrubbingPreserved(t *testing.T) {
	validToken := "valid-status-token-123"
	authSess := &AgentSession{
		ID:        uuid.New(),
		Token:     validToken,
		UserID:    uuid.New(),
		AgentID:   "agent-identity-xyz",
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(10 * time.Minute),
	}

	srv := NewAgentServer(AgentServerConfig{
		AgentID:      "agent-identity-xyz",
		Version:      "1.2.3",
		CommitSHA:    "abc1234",
		Port:         4142,
		DockerPinger: func(context.Context) error { return nil },
		SessionValidator: func(token, agentID string) (*AgentSession, error) {
			if token == validToken {
				return authSess, nil
			}
			return nil, ErrSessionNotFound
		},
	})
	defer srv.Close()

	srv.mu.Lock()
	srv.sessions[uuid.New()] = &LocalSourceSession{SourceID: uuid.New(), ExpiresAt: time.Now().Add(time.Hour)}
	srv.mu.Unlock()

	ts := httptest.NewServer(srv.Router())
	defer ts.Close()

	// 1. Plain unauthenticated status hides agent_id and active_sessions
	resp, err := http.Get(ts.URL + "/api/agent/status")
	require.NoError(t, err)
	var unauth map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&unauth))
	resp.Body.Close()
	assert.Equal(t, "", unauth["agent_id"])
	assert.Equal(t, false, unauth["agent_id_verified"])
	assert.Equal(t, float64(0), unauth["active_sessions"])
	assert.Equal(t, "abc1234", unauth["commit"])

	// 2. Unauthenticated status with matching X-Expected-Agent-ID confirms identity without leaking sessions
	reqExpected, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/agent/status", nil)
	reqExpected.Header.Set("X-Expected-Agent-ID", "agent-identity-xyz")
	respExpected, err := http.DefaultClient.Do(reqExpected)
	require.NoError(t, err)
	var expectedBody map[string]interface{}
	require.NoError(t, json.NewDecoder(respExpected.Body).Decode(&expectedBody))
	respExpected.Body.Close()
	assert.Equal(t, "agent-identity-xyz", expectedBody["agent_id"])
	assert.Equal(t, true, expectedBody["agent_id_verified"])
	assert.Equal(t, float64(0), expectedBody["active_sessions"])

	// 3. Mismatched X-Expected-Agent-ID does NOT disclose real agent_id
	reqWrong, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/agent/status", nil)
	reqWrong.Header.Set("X-Expected-Agent-ID", "wrong-agent")
	respWrong, err := http.DefaultClient.Do(reqWrong)
	require.NoError(t, err)
	var wrongBody map[string]interface{}
	require.NoError(t, json.NewDecoder(respWrong.Body).Decode(&wrongBody))
	respWrong.Body.Close()
	assert.Equal(t, "", wrongBody["agent_id"])
	assert.Equal(t, false, wrongBody["agent_id_verified"])

	// 4. Authenticated request sees full agent_id and real active_sessions count
	reqAuth, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/agent/status", nil)
	reqAuth.Header.Set("Authorization", "Bearer "+validToken)
	respAuth, err := http.DefaultClient.Do(reqAuth)
	require.NoError(t, err)
	var authBody map[string]interface{}
	require.NoError(t, json.NewDecoder(respAuth.Body).Decode(&authBody))
	respAuth.Body.Close()
	assert.Equal(t, "agent-identity-xyz", authBody["agent_id"])
	assert.Equal(t, float64(1), authBody["active_sessions"])
}

func TestAgentServer_Picker_TimeoutVsCancelVsNonInteractive(t *testing.T) {
	validToken := "picker-auth-token"
	authSess := &AgentSession{
		ID:        uuid.New(),
		Token:     validToken,
		UserID:    uuid.New(),
		AgentID:   "picker-test-agent",
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(10 * time.Minute),
	}

	makeServer := func(picker *NativeFolderPicker) (*AgentServer, *httptest.Server) {
		srv := NewAgentServer(AgentServerConfig{
			Port:         4142,
			Picker:       picker,
			DockerPinger: func(context.Context) error { return nil },
			SessionValidator: func(token, agentID string) (*AgentSession, error) {
				if token == validToken {
					return authSess, nil
				}
				return nil, ErrSessionNotFound
			},
		})
		ts := httptest.NewServer(srv.Router())
		return srv, ts
	}

	t.Run("dialog timeout returns 408 timeout and resets picking lock", func(t *testing.T) {
		picker := &NativeFolderPicker{
			dialogTimeout: 40 * time.Millisecond,
			pickFunc: func(ctx context.Context, title string) (string, error) {
				<-ctx.Done()
				return "", ctx.Err()
			},
		}
		srv, ts := makeServer(picker)
		defer ts.Close()
		defer srv.Close()

		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/agent/select-folder", bytes.NewReader([]byte(`{}`)))
		req.Header.Set("Authorization", "Bearer "+validToken)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		require.Equal(t, http.StatusRequestTimeout, resp.StatusCode)
		var body map[string]string
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		assert.Equal(t, "timeout", body["reason"])

		// Verify picking guard was cleanly reset after timeout
		picker.isPicking.Lock()
		stillPicking := picker.picking
		picker.isPicking.Unlock()
		assert.False(t, stillPicking)
	})

	t.Run("parent cancel returns ErrPickerCancelled rather than ErrPickerTimedOut", func(t *testing.T) {
		picker := &NativeFolderPicker{
			dialogTimeout: 5 * time.Second,
			pickFunc: func(ctx context.Context, title string) (string, error) {
				<-ctx.Done()
				return "", ctx.Err()
			},
		}

		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(30 * time.Millisecond)
			cancel()
		}()

		_, err := picker.PickFolder(ctx, "Pick")
		assert.ErrorIs(t, err, ErrPickerCancelled)
		assert.NotErrorIs(t, err, ErrPickerTimedOut)
	})

	t.Run("user dialog cancel returns 200 cancelled true", func(t *testing.T) {
		picker := &NativeFolderPicker{
			dialogTimeout: 2 * time.Second,
			pickFunc: func(ctx context.Context, title string) (string, error) {
				return "", ErrPickerCancelled
			},
		}
		srv, ts := makeServer(picker)
		defer ts.Close()
		defer srv.Close()

		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/agent/select-folder", bytes.NewReader([]byte(`{}`)))
		req.Header.Set("Authorization", "Bearer "+validToken)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		require.Equal(t, http.StatusOK, resp.StatusCode)
		var body map[string]interface{}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		assert.Equal(t, true, body["cancelled"])
	})

	t.Run("non-interactive session returns 503 non_interactive_session", func(t *testing.T) {
		picker := &NativeFolderPicker{
			dialogTimeout: 2 * time.Second,
			pickFunc: func(ctx context.Context, title string) (string, error) {
				return "", fmt.Errorf("%w: Session 0", ErrPickerNotInteractive)
			},
		}
		srv, ts := makeServer(picker)
		defer ts.Close()
		defer srv.Close()

		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/agent/select-folder", bytes.NewReader([]byte(`{}`)))
		req.Header.Set("Authorization", "Bearer "+validToken)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
		var body map[string]string
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		assert.Equal(t, "non_interactive_session", body["reason"])
	})

	t.Run("concurrent select-folder returns 409 busy", func(t *testing.T) {
		started := make(chan struct{}, 1)
		release := make(chan struct{})
		picker := &NativeFolderPicker{
			dialogTimeout: 5 * time.Second,
			pickFunc: func(ctx context.Context, title string) (string, error) {
				started <- struct{}{}
				<-release
				return "", ErrPickerCancelled
			},
		}
		srv, ts := makeServer(picker)
		defer ts.Close()
		defer srv.Close()

		firstDone := make(chan int, 1)
		go func() {
			req1, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/agent/select-folder", bytes.NewReader([]byte(`{}`)))
			req1.Header.Set("Authorization", "Bearer "+validToken)
			resp1, err := http.DefaultClient.Do(req1)
			if err != nil {
				firstDone <- 0
				return
			}
			resp1.Body.Close()
			firstDone <- resp1.StatusCode
		}()

		<-started
		req2, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/agent/select-folder", bytes.NewReader([]byte(`{}`)))
		req2.Header.Set("Authorization", "Bearer "+validToken)
		resp2, err := http.DefaultClient.Do(req2)
		require.NoError(t, err)
		var busyBody map[string]string
		require.NoError(t, json.NewDecoder(resp2.Body).Decode(&busyBody))
		resp2.Body.Close()

		assert.Equal(t, http.StatusConflict, resp2.StatusCode)
		assert.Equal(t, "busy", busyBody["reason"])

		close(release)
		assert.Equal(t, http.StatusOK, <-firstDone)
	})
}

func TestAgentServer_ConcurrentStatusSessionAndClose(t *testing.T) {
	tempDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tempDir, "package.json"), []byte(`{"name":"race-test"}`), 0644))

	sm := NewSessionManager()
	defer sm.Stop()
	// Verify Stop is idempotent under repeated calls
	sm.Stop()

	sm2 := NewSessionManager()
	defer sm2.Stop()

	authSess, err := sm2.CreateSession(uuid.New(), "concurrent-agent", time.Minute)
	require.NoError(t, err)

	srv := NewAgentServer(AgentServerConfig{
		AgentID:             "concurrent-agent",
		Port:                4142,
		DockerCheckInterval: 10 * time.Millisecond,
		DockerPingTimeout:   50 * time.Millisecond,
		DockerPinger:        func(context.Context) error { return nil },
		SessionValidator: func(token, agentID string) (*AgentSession, error) {
			if token == authSess.Token {
				return authSess, nil
			}
			return nil, ErrSessionNotFound
		},
	})
	defer srv.Close()

	sess, err := srv.registerDirectory(tempDir, authSess.Token)
	require.NoError(t, err)

	ts := httptest.NewServer(srv.Router())
	defer ts.Close()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			for j := 0; j < 15; j++ {
				if idx%3 == 0 {
					resp, err := http.Get(ts.URL + "/api/agent/status")
					if err == nil {
						resp.Body.Close()
					}
				} else {
					req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/agent/sources/"+sess.SourceID.String(), nil)
					req.Header.Set("Authorization", "Bearer "+authSess.Token)
					resp, err := http.DefaultClient.Do(req)
					if err == nil {
						resp.Body.Close()
					}
				}
				_ = srv.CleanupExpiredSessions()
			}
		}(i)
	}

	time.Sleep(30 * time.Millisecond)
	_ = srv.ConsumeSession(sess.SourceID)
	srv.Close()
	// Double Close must be safe
	srv.Close()

	wg.Wait()
}
