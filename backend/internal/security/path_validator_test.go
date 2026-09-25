package security_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/forgelab/backend/internal/security"
)

func TestPathValidatorAllowedRoots(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab_test_")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	subDir := filepath.Join(tempDir, "valid_repo")
	_ = os.MkdirAll(subDir, 0755)

	validator := security.NewPathValidator([]string{tempDir})

	// Valid path inside allowed root
	canonical, err := validator.ValidateSourcePath(subDir)
	if err != nil {
		t.Errorf("expected valid path inside allowed root, got error: %v", err)
	}
	if canonical == "" {
		t.Errorf("expected non-empty canonical path")
	}

	// Path outside allowed root
	outsideDir, err := os.MkdirTemp("", "outside_test_")
	if err == nil {
		defer os.RemoveAll(outsideDir)
		_, err = validator.ValidateSourcePath(outsideDir)
		if err != security.ErrPathNotAllowed {
			t.Errorf("expected ErrPathNotAllowed for path outside root, got %v", err)
		}
	}
}

func TestPathValidatorRestrictedSystemDirs(t *testing.T) {
	validator := security.NewPathValidator(nil)

	sysPaths := []string{
		"/etc",
		"/var",
		`C:\Windows`,
	}

	for _, p := range sysPaths {
		if _, err := os.Stat(p); err == nil {
			_, err := validator.ValidateSourcePath(p)
			if err != security.ErrRestrictedSystemPath {
				t.Errorf("expected ErrRestrictedSystemPath for system path %s, got %v", p, err)
			}
		}
	}
}

func TestPathValidatorPathTraversal(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab_traversal_")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	subDir := filepath.Join(tempDir, "app")
	_ = os.MkdirAll(subDir, 0755)

	validator := security.NewPathValidator([]string{subDir})

	// Traversal attempt: app/../ escapes subDir
	traversalPath := filepath.Join(subDir, "..")
	_, err = validator.ValidateSourcePath(traversalPath)
	if err != security.ErrPathNotAllowed {
		t.Errorf("expected ErrPathNotAllowed for traversal attempt, got %v", err)
	}
}
