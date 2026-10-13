package dockerignore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMandatorySecretProtection(t *testing.T) {
	// A custom .dockerignore that attempts to un-ignore .env or ignores other files
	customRules := []string{
		"node_modules",
		"!.env",
		"!.env.local",
		"!secrets.key",
	}

	matcher := NewDockerignoreMatcher(customRules)

	secretFiles := []string{
		".env",
		".env.local",
		".env.production",
		"subfolder/.env",
		"id_rsa",
		"id_ed25519",
		"server.key",
		"cert.pem",
	}

	for _, sf := range secretFiles {
		if !matcher.Matches(sf, false) {
			t.Errorf("expected secret file %q to be ignored despite custom rules/negations", sf)
		}
	}

	// Normal source code should not be ignored
	nonSecretFiles := []string{
		"src/index.js",
		"package.json",
		"environment.ts",
		"config.go",
	}

	for _, nsf := range nonSecretFiles {
		if matcher.Matches(nsf, false) {
			t.Errorf("expected non-secret file %q to NOT be ignored", nsf)
		}
	}
}

func TestLoadDockerignoreWithCustomFile(t *testing.T) {
	tmpDir := t.TempDir()
	ignoreContent := "dist/\n*.tmp\n"
	err := os.WriteFile(filepath.Join(tmpDir, ".dockerignore"), []byte(ignoreContent), 0644)
	if err != nil {
		t.Fatalf("failed to create temp .dockerignore: %v", err)
	}

	matcher, err := LoadDockerignore(tmpDir)
	if err != nil {
		t.Fatalf("LoadDockerignore failed: %v", err)
	}

	// Custom rule should match
	if !matcher.Matches("dist/bundle.js", false) {
		t.Errorf("expected dist/bundle.js to be ignored by custom rule")
	}

	// Mandatory secret should STILL match even though not in custom .dockerignore!
	if !matcher.Matches(".env", false) {
		t.Errorf("expected .env to be ignored even when absent from custom .dockerignore")
	}
	if !matcher.Matches("app/.env.local", false) {
		t.Errorf("expected app/.env.local to be ignored even when absent from custom .dockerignore")
	}
}
