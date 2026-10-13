package proxy

import (
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
