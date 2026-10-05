package analyzer

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/forgelab/backend/internal/discovery"
	"github.com/forgelab/backend/internal/models"
)

// ServiceDefinition represents a discovered service and its detected build candidates.
type ServiceDefinition struct {
	Name               string                  `json:"name"`
	Role               string                  `json:"role"` // "frontend", "backend", "worker", "other"
	SourcePath         string                  `json:"source_path"`
	Runtime            string                  `json:"runtime"`
	RuntimeType        string                  `json:"runtime_type"`
	Framework          string                  `json:"framework"`
	PackageManager     string                  `json:"package_manager"`
	BuildStrategy      string                  `json:"build_strategy"`
	BuildCandidates    []models.BuildCandidate `json:"build_candidates"`
	BuildCommand       string                  `json:"build_command"`
	StartCommand       string                  `json:"start_command"`
	DockerfilePath     string                  `json:"dockerfile_path"`
	BuildContext       string                  `json:"build_context"`
	InternalPort       int                     `json:"internal_port"`
	HealthStrategy     string                  `json:"health_strategy"`
	HealthCheckPath    string                  `json:"health_check_path"`
	HealthCheckEnabled bool                    `json:"health_check_enabled"`
	FilesCount         int                     `json:"files_count"`
	TotalBytes         int64                   `json:"total_bytes"`
}

// AnalysisResult represents the overall repository inspection result.
type AnalysisResult struct {
	RepositoryName string              `json:"repository_name"`
	TotalFiles     int                 `json:"total_files"`
	TotalBytes     int64               `json:"total_bytes"`
	Services       []ServiceDefinition `json:"services"`
}

// IsPrunedDir delegates to discovery.IsPrunedDir for consistent directory pruning.
func IsPrunedDir(name string) bool {
	return discovery.IsPrunedDir(name)
}

// IsSecretFile delegates to discovery.IsSecretFile for consistent secret file detection.
func IsSecretFile(name string) bool {
	return discovery.IsSecretFile(name)
}

// ProgressCallback defines progress reporting for repository analysis.
type ProgressCallback func(phase string, filesScanned int, totalFiles int, detectedCount int)

// AnalyzeRepository recursively inspects the repository root, discovers services,
// detects technologies using evidence, and produces ranked build candidates.
func AnalyzeRepository(repoRoot string) (*AnalysisResult, error) {
	return AnalyzeRepositoryWithProgress(repoRoot, nil)
}

// AnalyzeRepositoryWithProgress recursively inspects the repository root with progress reporting
// by delegating to the authoritative discovery engine.
func AnalyzeRepositoryWithProgress(repoRoot string, onProgress ProgressCallback) (*AnalysisResult, error) {
	cleanRoot := filepath.Clean(repoRoot)
	info, err := os.Stat(cleanRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to access repository path: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("repository path is not a directory")
	}

	if onProgress != nil {
		onProgress("scanning", 0, 0, 0)
	}

	discResult, err := discovery.Discover(cleanRoot)
	if err != nil {
		return nil, fmt.Errorf("discovery failed: %w", err)
	}

	if onProgress != nil {
		onProgress("detecting", discResult.TotalFiles, discResult.TotalFiles, len(discResult.Services))
	}

	var services []ServiceDefinition
	for _, s := range discResult.Services {
		services = append(services, ServiceDefinition{
			Name:               s.Name,
			Role:               s.Role,
			SourcePath:         s.SourcePath,
			Runtime:            s.Runtime,
			RuntimeType:        s.Runtime,
			Framework:          s.Framework,
			PackageManager:     s.PackageManager,
			BuildStrategy:      s.BuildStrategy,
			BuildCandidates:    s.BuildCandidates,
			BuildCommand:       s.BuildCommand,
			StartCommand:       s.StartCommand,
			DockerfilePath:     s.DockerfilePath,
			BuildContext:       s.BuildContext,
			InternalPort:       s.InternalPort,
			HealthStrategy:     s.HealthStrategy,
			HealthCheckPath:    s.HealthCheckPath,
			HealthCheckEnabled: s.HealthCheckEnabled,
			FilesCount:         s.FilesCount,
			TotalBytes:         s.TotalBytes,
		})
	}

	if onProgress != nil {
		onProgress("ready", discResult.TotalFiles, discResult.TotalFiles, len(services))
	}

	return &AnalysisResult{
		RepositoryName: discResult.RepositoryName,
		TotalFiles:     discResult.TotalFiles,
		TotalBytes:     discResult.TotalBytes,
		Services:       services,
	}, nil
}

// GenerateDockerfile delegates to discovery.GenerateDockerfile.
func GenerateDockerfile(runtime string, port int, startCmd, pkgManager string) string {
	return discovery.GenerateDockerfile(runtime, port, startCmd, pkgManager)
}
