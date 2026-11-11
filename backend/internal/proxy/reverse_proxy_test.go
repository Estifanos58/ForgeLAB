package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidateCustomDomain(t *testing.T) {
	tests := []struct {
		name      string
		domain    string
		want      string
		expectErr bool
	}{
		{"Valid domain", "api.example.org", "api.example.org", false},
		{"Valid domain with scheme prefix", "https://app.mycompany.io/", "app.mycompany.io", false},
		{"Uppercase normalized", "MY-APP.Cloud.Dev", "my-app.cloud.dev", false},
		{"Invalid characters", "app$bad.com", "", true},
		{"Port included", "example.com:8080", "", true},
		{"Raw IP rejected", "192.168.1.1", "", true},
		{"Reserved TLD local", "myservice.local", "", true},
		{"Single label no TLD", "localhost", "", true},
		{"Empty string", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateCustomDomain(tt.domain)
			if tt.expectErr && err == nil {
				t.Fatalf("expected error for %q, got nil", tt.domain)
			}
			if !tt.expectErr && err != nil {
				t.Fatalf("unexpected error for %q: %v", tt.domain, err)
			}
			if !tt.expectErr && got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGenerateCaddyfile(t *testing.T) {
	routes := []ProxyRoute{
		{
			Domain:      "api.example.com",
			TargetHost:  "127.0.0.1",
			TargetPort:  8081,
			TLSEnabled:  true,
			ServiceName: "backend",
		},
		{
			Domain:      "app.example.com",
			TargetHost:  "127.0.0.1",
			TargetPort:  3000,
			TLSEnabled:  false,
			ServiceName: "frontend",
		},
	}

	caddyfile := GenerateCaddyfile(routes)
	if !strings.Contains(caddyfile, "api.example.com {") {
		t.Errorf("expected api.example.com block in Caddyfile")
	}
	if !strings.Contains(caddyfile, "reverse_proxy 127.0.0.1:8081") {
		t.Errorf("expected target host:port in Caddyfile")
	}
	if !strings.Contains(caddyfile, "tls off") {
		t.Errorf("expected tls off for app.example.com")
	}
}

func TestBuildCaddyConfigJSON_AndSyncer(t *testing.T) {
	routes := []ProxyRoute{
		{
			Domain:     "app.example.com",
			TargetHost: "127.0.0.1",
			TargetPort: 8081,
			TLSEnabled: true,
		},
		{
			Domain:     "staging.example.com",
			TargetHost: "127.0.0.1",
			TargetPort: 8082,
			TLSEnabled: false,
		},
	}

	raw, err := BuildCaddyConfigJSON(routes, "ops@example.com")
	if err != nil {
		t.Fatalf("BuildCaddyConfigJSON failed: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("invalid JSON from BuildCaddyConfigJSON: %v", err)
	}

	text := string(raw)
	for _, want := range []string{`"app.example.com"`, `"127.0.0.1:8081"`, `"ops@example.com"`, `"acme"`, `"staging.example.com"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %s in Caddy JSON config, got:\n%s", want, text)
		}
	}

	// Unconfigured syncer should safely no-op
	noopSyncer := NewCaddySyncer("", "ops@example.com", nil)
	if noopSyncer.IsConfigured() {
		t.Fatalf("expected empty CaddySyncer not to report configured")
	}
	if err := noopSyncer.SyncRoutes(context.Background(), routes); err != nil {
		t.Fatalf("expected noop sync to succeed cleanly, got: %v", err)
	}

	// Configured syncer should POST JSON payload to /load
	var receivedPath string
	var receivedBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		receivedBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	syncer := NewCaddySyncer(srv.URL, "ops@example.com", srv.Client())
	if !syncer.IsConfigured() {
		t.Fatalf("expected syncer with URL to be configured")
	}
	if err := syncer.SyncRoutes(context.Background(), routes); err != nil {
		t.Fatalf("SyncRoutes failed: %v", err)
	}
	if receivedPath != "/load" {
		t.Fatalf("expected POST /load, got %q", receivedPath)
	}
	if !strings.Contains(receivedBody, `"app.example.com"`) {
		t.Fatalf("expected synced Caddy JSON payload to include domain, got %q", receivedBody)
	}

	// Error status codes must surface clearly
	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "invalid caddy config", http.StatusBadRequest)
	}))
	defer errSrv.Close()

	errSyncer := NewCaddySyncer(errSrv.URL, "", errSrv.Client())
	if err := errSyncer.SyncRoutes(context.Background(), routes); err == nil {
		t.Fatalf("expected error on HTTP 400 from Caddy Admin API")
	}
}
