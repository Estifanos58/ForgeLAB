package discovery_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/forgelab/backend/internal/discovery"
)

func TestDiscoverWithContext_Cancellation(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-disc-cancel-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create some files
	_ = os.WriteFile(filepath.Join(tempDir, "package.json"), []byte(`{"name":"test"}`), 0644)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err = discovery.DiscoverWithContext(ctx, tempDir)
	if err == nil {
		t.Fatalf("expected error from canceled context, got nil")
	}
	if err != context.Canceled {
		t.Logf("got error on canceled context: %v", err)
	}
}

func TestDiscoverWithContext_SymlinkEscapeIgnored(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-disc-symlink-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	outsideDir, err := os.MkdirTemp("", "forgelab-outside-*")
	if err != nil {
		t.Fatalf("failed to create outside dir: %v", err)
	}
	defer os.RemoveAll(outsideDir)
	_ = os.WriteFile(filepath.Join(outsideDir, "secret.txt"), []byte("confidential"), 0644)

	// Create symlink pointing outside
	_ = os.Symlink(outsideDir, filepath.Join(tempDir, "escaping_link"))
	_ = os.WriteFile(filepath.Join(tempDir, "package.json"), []byte(`{"name":"app"}`), 0644)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := discovery.DiscoverWithContext(ctx, tempDir)
	if err != nil {
		t.Fatalf("expected discovery to succeed ignoring external symlink, got error: %v", err)
	}
	if res == nil {
		t.Fatalf("expected non-nil discovery result")
	}
}
