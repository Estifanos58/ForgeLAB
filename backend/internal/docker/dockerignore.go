package docker

import (
	"github.com/forgelab/backend/internal/dockerignore"
)

// DefaultIgnorePatterns are standard development directories/files to ignore
// when no .dockerignore exists or as baseline exclusions.
var DefaultIgnorePatterns = dockerignore.DefaultIgnorePatterns

// IgnoreRule represents a single parsed rule from .dockerignore.
type IgnoreRule = dockerignore.IgnoreRule

// DockerignoreMatcher handles filtering files based on .dockerignore semantics.
type DockerignoreMatcher = dockerignore.DockerignoreMatcher

// NewDockerignoreMatcher creates a matcher from a slice of rule patterns.
var NewDockerignoreMatcher = dockerignore.NewDockerignoreMatcher

// LoadDockerignore reads a .dockerignore file from rootDir.
var LoadDockerignore = dockerignore.LoadDockerignore
