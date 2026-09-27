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
		"/usr",
		`C:\Windows`,
		`C:\Program Files`,
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

func TestPathValidatorNonExistentAndFile(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab_nonexist_")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	validator := security.NewPathValidator([]string{tempDir})

	// Non-existent directory
	_, err = validator.ValidateSourcePath(filepath.Join(tempDir, "does-not-exist"))
	if err != security.ErrPathNotExist {
		t.Errorf("expected ErrPathNotExist for missing directory, got %v", err)
	}

	// File instead of directory
	filePath := filepath.Join(tempDir, "somefile.txt")
	if err := os.WriteFile(filePath, []byte("hello"), 0644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}
	_, err = validator.ValidateSourcePath(filePath)
	if err != security.ErrPathNotDirectory {
		t.Errorf("expected ErrPathNotDirectory for regular file, got %v", err)
	}
}

func TestPathValidatorHostToContainerMapping(t *testing.T) {
	containerTemp, err := os.MkdirTemp("", "forgelab_container_")
	if err != nil {
		t.Fatalf("failed to create container temp: %v", err)
	}
	defer os.RemoveAll(containerTemp)

	appDir := filepath.Join(containerTemp, "my-app")
	if err := os.MkdirAll(appDir, 0755); err != nil {
		t.Fatalf("failed to create app dir: %v", err)
	}

	hostSourceRoot := `C:\Projects`
	validator := security.NewPathValidatorWithMapping(
		nil,
		hostSourceRoot,
		containerTemp,
	)

	// Valid host path mapped into container
	canonical, err := validator.ValidateSourcePath(`C:\Projects\my-app`)
	if err != nil {
		t.Fatalf("expected valid mapped path, got error: %v", err)
	}
	if canonical != appDir {
		t.Errorf("expected canonical %s, got %s", appDir, canonical)
	}

	// Host path with traversal
	_, err = validator.ValidateSourcePath(`C:\Projects\..\escape`)
	if err != security.ErrPathNotAllowed && err != security.ErrPathNotExist {
		t.Errorf("expected error for escaping host root, got %v", err)
	}
}
