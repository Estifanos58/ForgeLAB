package docker_test

import (
	"archive/tar"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/forgelab/backend/internal/docker"
)

func TestDockerignoreMatcher(t *testing.T) {
	patterns := []string{
		"# Comments should be ignored",
		"node_modules",
		".git/",
		"*.log",
		"dist",
		"!dist/keep.txt",
		"coverage/**",
	}

	matcher := docker.NewDockerignoreMatcher(patterns)

	tests := []struct {
		path     string
		isDir    bool
		expected bool
	}{
		{"node_modules", true, true},
		{"node_modules/express/index.js", false, true},
		{".git", true, true},
		{".git/config", false, true},
		{"app.log", false, true},
		{"server/app.log", false, true},
		{"dist/bundle.js", false, true},
		{"dist/keep.txt", false, false}, // Negated
		{"src/index.ts", false, false},
		{"package.json", false, false},
	}

	for _, tt := range tests {
		got := matcher.Matches(tt.path, tt.isDir)
		if got != tt.expected {
			t.Errorf("Matches(%q, isDir=%v) = %v; want %v", tt.path, tt.isDir, got, tt.expected)
		}
	}
}

func TestDockerignoreCanSkipDir(t *testing.T) {
	patterns := []string{
		"node_modules",
		".git",
		"build",
		"!build/keep.txt",
	}
	matcher := docker.NewDockerignoreMatcher(patterns)

	// node_modules has no negation, so it can be skipped completely
	if !matcher.CanSkipDir("node_modules") {
		t.Errorf("expected CanSkipDir('node_modules') = true")
	}

	// build has a negated file inside, so it should not be skipped blindly
	if matcher.CanSkipDir("build") {
		t.Errorf("expected CanSkipDir('build') = false due to negation rule")
	}

	// src is not ignored
	if matcher.CanSkipDir("src") {
		t.Errorf("expected CanSkipDir('src') = false")
	}
}

func TestStreamBuildContext(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab_stream_test_")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create project structure:
	// src/index.js
	// node_modules/heavy.js (should be excluded)
	// package.json
	_ = os.MkdirAll(filepath.Join(tempDir, "src"), 0755)
	_ = os.MkdirAll(filepath.Join(tempDir, "node_modules"), 0755)
	_ = os.WriteFile(filepath.Join(tempDir, "src", "index.js"), []byte("console.log('hello');"), 0644)
	_ = os.WriteFile(filepath.Join(tempDir, "node_modules", "heavy.js"), []byte("huge data"), 0644)
	_ = os.WriteFile(filepath.Join(tempDir, "package.json"), []byte(`{"name":"test"}`), 0644)

	matcher := docker.NewDockerignoreMatcher([]string{"node_modules"})

	virtualFiles := map[string][]byte{
		"Dockerfile.forgelab": []byte("FROM alpine\nCMD [\"echo\", \"virtual\"]"),
	}

	ctx := context.Background()
	stream := docker.StreamBuildContext(ctx, docker.TarStreamerOptions{
		BuildContextDir: tempDir,
		Matcher:         matcher,
		VirtualFiles:    virtualFiles,
	})
	defer stream.Close()

	// Read and verify tar entries
	tr := tar.NewReader(stream)
	foundFiles := make(map[string]bool)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("error reading tar stream: %v", err)
		}
		foundFiles[header.Name] = true
	}

	// Verify expected files exist
	if !foundFiles["Dockerfile.forgelab"] {
		t.Errorf("expected virtual file 'Dockerfile.forgelab' in tar stream")
	}
	if !foundFiles["src/index.js"] {
		t.Errorf("expected 'src/index.js' in tar stream")
	}
	if !foundFiles["package.json"] {
		t.Errorf("expected 'package.json' in tar stream")
	}

	// Verify node_modules was completely skipped
	for name := range foundFiles {
		if strings.Contains(name, "node_modules") {
			t.Errorf("unexpected file in tar stream: %s", name)
		}
	}
}

func TestStreamBuildContextCancellation(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab_cancel_test_")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	for i := 0; i < 50; i++ {
		_ = os.WriteFile(filepath.Join(tempDir, "file.txt"), []byte("content"), 0644)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	stream := docker.StreamBuildContext(ctx, docker.TarStreamerOptions{
		BuildContextDir: tempDir,
	})
	defer stream.Close()

	buf := make([]byte, 1024)
	_, _ = stream.Read(buf)
	// Must not hang or block
	time.Sleep(10 * time.Millisecond)
}

func TestStreamBuildContext_LargeDirectorySimulation(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab_large_dir_test_")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create root project files
	_ = os.WriteFile(filepath.Join(tempDir, "package.json"), []byte(`{"name":"large-app"}`), 0644)
	_ = os.WriteFile(filepath.Join(tempDir, "Dockerfile"), []byte("FROM alpine\nCMD [\"echo\", \"done\"]"), 0644)

	// Simulate node_modules containing 1000 files
	nodeModules := filepath.Join(tempDir, "node_modules", "package-bundle")
	_ = os.MkdirAll(nodeModules, 0755)
	for i := 0; i < 500; i++ {
		_ = os.WriteFile(filepath.Join(nodeModules, "dep"+strings.Repeat("a", i%10)+".js"), []byte("module.exports = {};"), 0644)
	}

	matcher := docker.NewDockerignoreMatcher([]string{"node_modules"})

	start := time.Now()
	stream := docker.StreamBuildContext(context.Background(), docker.TarStreamerOptions{
		BuildContextDir: tempDir,
		Matcher:         matcher,
	})
	defer stream.Close()

	tr := tar.NewReader(stream)
	count := 0
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("unexpected tar error: %v", err)
		}
		if strings.Contains(header.Name, "node_modules") {
			t.Fatalf("node_modules entry found in tar stream: %s", header.Name)
		}
		count++
	}

	elapsed := time.Since(start)
	if count != 2 {
		t.Errorf("expected 2 files streamed, got %d", count)
	}
	if elapsed > 2*time.Second {
		t.Errorf("streaming large directory took too long: %v", elapsed)
	}
}
