package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAgentMain_VersionFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runWithContext(context.Background(), []string{"-version"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr=%s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "forgelab-agent v") {
		t.Fatalf("unexpected version output: %q", stdout.String())
	}
}

func TestAgentMain_InvalidPortRejected(t *testing.T) {
	for _, args := range [][]string{{"-port", "-1"}, {"-port", "70000"}} {
		var stdout, stderr bytes.Buffer
		code := runWithContext(context.Background(), args, &stdout, &stderr)
		if code != 1 {
			t.Fatalf("expected exit code 1 for %v, got %d", args, code)
		}
		if !strings.Contains(stderr.String(), "Invalid port") {
			t.Fatalf("expected invalid port error for %v, got %q", args, stderr.String())
		}
	}
}

func TestAgentMain_EphemeralLoopbackBindAndGracefulShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var stdout, stderr safeBuffer
	done := make(chan int, 1)
	go func() {
		done <- runWithContext(ctx, []string{"-host", "127.0.0.1", "-port", "0", "-agent-id", "test-cli-agent"}, &stdout, &stderr)
	}()

	readyRe := regexp.MustCompile(`Ready on (http://127\.0\.0\.1:\d+)`)
	var baseURL string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if m := readyRe.FindStringSubmatch(stdout.String()); len(m) == 2 {
			baseURL = m[1]
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if baseURL == "" {
		cancel()
		<-done
		t.Fatalf("timed out waiting for Ready banner; stdout=%q stderr=%q", stdout.String(), stderr.String())
	}

	resp, err := http.Get(baseURL + "/api/agent/status")
	if err != nil {
		cancel()
		<-done
		t.Fatalf("GET /api/agent/status failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		cancel()
		<-done
		t.Fatalf("expected 200 OK from /api/agent/status, got %d", resp.StatusCode)
	}

	var status map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		cancel()
		<-done
		t.Fatalf("failed to decode agent status: %v", err)
	}
	if status["status"] != "online" || status["agent_id_verified"] != false || status["agent_id"] != "" {
		t.Errorf("expected unauthenticated status not to leak agent_id, got %+v", status)
	}

	verifyResp, err := http.Get(baseURL + "/api/agent/status?expected_agent_id=test-cli-agent")
	if err != nil {
		cancel()
		<-done
		t.Fatalf("GET /api/agent/status?expected_agent_id failed: %v", err)
	}
	defer verifyResp.Body.Close()
	if verifyResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from verified status request, got %d", verifyResp.StatusCode)
	}
	var verifiedStatus map[string]interface{}
	if err := json.NewDecoder(verifyResp.Body).Decode(&verifiedStatus); err != nil {
		t.Fatalf("failed to decode verified agent status: %v", err)
	}
	if verifiedStatus["agent_id"] != "test-cli-agent" || verifiedStatus["agent_id_verified"] != true {
		t.Errorf("expected agent_id=test-cli-agent and agent_id_verified=true, got %+v", verifiedStatus)
	}
	if !strings.Contains(stdout.String(), "Agent ID: test-cli-agent") {
		t.Errorf("expected startup banner to include Agent ID: test-cli-agent, got %q", stdout.String())
	}

	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("expected clean exit code 0, got %d (stderr=%s)", code, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for agent clean shutdown")
	}
}

func TestAgentMain_OccupiedPortFailsBeforeReadyBanner(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to open occupying listener: %v", err)
	}
	defer occupied.Close()

	_, portStr, err := net.SplitHostPort(occupied.Addr().String())
	if err != nil {
		t.Fatalf("failed to split occupied address: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := runWithContext(context.Background(), []string{"-host", "127.0.0.1", "-port", portStr}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit code 1 when port is occupied, got %d", code)
	}
	if strings.Contains(stdout.String(), "Ready on") {
		t.Fatalf("agent must never emit Ready banner on bind failure: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "Startup Error") {
		t.Fatalf("expected Startup Error on stderr, got %q", stderr.String())
	}
}

type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
