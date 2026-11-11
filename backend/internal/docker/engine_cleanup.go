package docker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// CleanUpProjectContainers is a backward-compatible wrapper calling CleanUpProjectResources.
func (e *Engine) CleanUpProjectContainers(ctx context.Context, projectID uuid.UUID) {
	_ = e.CleanUpProjectResources(ctx, projectID)
}

// CleanUpProjectResources stops and removes all containers, images, and networks owned by a project.
// It is idempotent, error-aware, and safely ignores already-removed resources.
// It never touches Compose infrastructure containers, persistent volumes, or unrelated images.
func (e *Engine) CleanUpProjectResources(ctx context.Context, projectID uuid.UUID) error {
	if e.dockerClient == nil {
		return nil
	}

	// 0. Cancel and drain all active in-flight deployment workers for this project
	if err := e.CancelAndDrainProjectDeployments(ctx, projectID); err != nil {
		slog.Warn("timeout or error waiting to drain project deployments during cleanup", "project_id", projectID, "error", err)
	}

	var cleanupErrs []error
	projIDStr := projectID.String()

	// 1. Gather all containers owned by this project (including orphaned containers)
	knownContainerIDs := make(map[string]bool)
	if e.deploymentService != nil {
		if deployments, err := e.deploymentService.ListDeployments(ctx, projectID); err == nil {
			for _, d := range deployments {
				if d.ContainerID != nil && *d.ContainerID != "" {
					knownContainerIDs[*d.ContainerID] = true
				}
			}
		}
	}
	if e.serviceService != nil {
		if svcs, err := e.serviceService.ListServices(ctx, projectID); err == nil {
			for _, svc := range svcs {
				if svc.CurrentServiceDeploymentID != nil {
					e.StopLogCollector(*svc.CurrentServiceDeploymentID)
				}
				if svc.ContainerID != nil && *svc.ContainerID != "" {
					knownContainerIDs[*svc.ContainerID] = true
				}
			}
		}
	}

	containersRes, err := e.dockerClient.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return fmt.Errorf("failed to list containers for project cleanup: %w", err)
	}

	stopTimeout := 5
	for _, c := range containersRes.Items {
		// NEVER touch Docker Compose infrastructure containers
		if _, isCompose := c.Labels["com.docker.compose.project"]; isCompose {
			continue
		}
		if _, isComposeService := c.Labels["com.docker.compose.service"]; isComposeService {
			continue
		}

		isOwned := false
		if c.Labels != nil && c.Labels["forgelab.project_id"] == projIDStr {
			isOwned = true
		} else if knownContainerIDs[c.ID] || (len(c.ID) > 12 && knownContainerIDs[c.ID[:12]]) {
			isOwned = true
		}

		if isOwned {
			e.StopContainerLogCollector(c.ID)
			// Stop container (ignore not found or already stopped)
			_, _ = e.dockerClient.ContainerStop(ctx, c.ID, client.ContainerStopOptions{Timeout: &stopTimeout})

			// Remove container with RemoveVolumes: false (Never remove persistent volumes!)
			_, remErr := e.dockerClient.ContainerRemove(ctx, c.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: false})
			if remErr != nil && !cerrdefs.IsNotFound(remErr) {
				slog.Error("failed to remove project container", "container_id", c.ID, "project_id", projIDStr, "error", remErr)
				cleanupErrs = append(cleanupErrs, fmt.Errorf("failed to remove container %s: %w", c.ID, remErr))
			} else {
				slog.Info("removed project container", "container_id", c.ID, "project_id", projIDStr)
			}
		}
	}

	// 2. Remove project networks
	netsRes, netListErr := e.dockerClient.NetworkList(ctx, client.NetworkListOptions{})
	if netListErr != nil {
		cleanupErrs = append(cleanupErrs, fmt.Errorf("failed to list networks: %w", netListErr))
	} else {
		projPrefix := fmt.Sprintf("forgelab-net-%s", projIDStr)
		projShortPrefix := fmt.Sprintf("forgelab-net-%s", projIDStr[:8])
		for _, n := range netsRes.Items {
			isProjectNet := (n.Labels != nil && n.Labels["forgelab.project_id"] == projIDStr) ||
				strings.HasPrefix(n.Name, projPrefix) ||
				strings.HasPrefix(n.Name, projShortPrefix)

			if isProjectNet {
				if myHost := os.Getenv("HOSTNAME"); myHost != "" {
					_, _ = e.dockerClient.NetworkDisconnect(ctx, n.ID, client.NetworkDisconnectOptions{
						Container: myHost,
						Force:     true,
					})
				}
				_, remErr := e.dockerClient.NetworkRemove(ctx, n.ID, client.NetworkRemoveOptions{})
				if remErr != nil && !cerrdefs.IsNotFound(remErr) {
					slog.Error("failed to remove project network", "network_id", n.ID, "name", n.Name, "error", remErr)
					cleanupErrs = append(cleanupErrs, fmt.Errorf("failed to remove network %s: %w", n.Name, remErr))
				} else {
					slog.Info("removed project network", "network_id", n.ID, "name", n.Name)
				}
			}
		}
	}

	// 3. Remove project-owned images (including retained rollback images)
	// Never use global image prune! Remove only images owned by this project.
	imagesRes, imgListErr := e.dockerClient.ImageList(ctx, client.ImageListOptions{All: true})
	if imgListErr != nil {
		cleanupErrs = append(cleanupErrs, fmt.Errorf("failed to list images: %w", imgListErr))
	} else {
		projTagPrefix := fmt.Sprintf("forgelab/%s", projIDStr)
		for _, img := range imagesRes.Items {
			isProjectImage := false
			if img.Labels != nil && img.Labels["forgelab.project_id"] == projIDStr {
				isProjectImage = true
			} else {
				for _, tag := range img.RepoTags {
					if strings.HasPrefix(tag, projTagPrefix) {
						isProjectImage = true
						break
					}
				}
			}

			if isProjectImage {
				_, remErr := e.dockerClient.ImageRemove(ctx, img.ID, client.ImageRemoveOptions{Force: true, PruneChildren: true})
				if remErr != nil && !cerrdefs.IsNotFound(remErr) {
					slog.Warn("failed to remove project image", "image_id", img.ID, "error", remErr)
					cleanupErrs = append(cleanupErrs, fmt.Errorf("failed to remove image %s: %w", img.ID, remErr))
				} else {
					slog.Info("removed project image", "image_id", img.ID, "project_id", projIDStr)
				}
			}
		}
	}

	// 4. Release in-memory port allocations
	if e.portManager != nil {
		if activeRes, err := e.dockerClient.ContainerList(ctx, client.ContainerListOptions{}); err == nil {
			var runningPorts []int
			for _, ac := range activeRes.Items {
				if ac.State == container.StateRunning {
					for _, p := range ac.Ports {
						if p.PublicPort > 0 {
							runningPorts = append(runningPorts, int(p.PublicPort))
						}
					}
				}
			}
			e.portManager.ReconcileUsedPorts(runningPorts)
		}
	}

	if len(cleanupErrs) > 0 {
		return errors.Join(cleanupErrs...)
	}
	return nil
}

// PruneDanglingResources removes expired build workspaces and dangling Docker images to bound storage growth.
func (e *Engine) PruneDanglingResources(ctx context.Context, maxBuildAge time.Duration) error {
	if maxBuildAge <= 0 {
		maxBuildAge = 2 * time.Hour
	}

	var pruneErrs []error

	// 1. Prune old build directories in workDir
	if e.workDir != "" {
		entries, err := os.ReadDir(e.workDir)
		if err == nil {
			now := time.Now()
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				info, err := entry.Info()
				if err != nil {
					continue
				}
				if now.Sub(info.ModTime()) > maxBuildAge {
					dirPath := filepath.Join(e.workDir, entry.Name())
					if err := os.RemoveAll(dirPath); err != nil {
						slog.Warn("failed to remove expired build directory", "path", dirPath, "error", err)
						pruneErrs = append(pruneErrs, fmt.Errorf("failed to remove build dir %s: %w", dirPath, err))
					} else {
						slog.Info("pruned expired build directory", "path", dirPath, "age", now.Sub(info.ModTime()).String())
					}
				}
			}
		} else if !os.IsNotExist(err) {
			slog.Warn("failed to read build workDir for pruning", "path", e.workDir, "error", err)
			pruneErrs = append(pruneErrs, fmt.Errorf("failed to read workDir %s: %w", e.workDir, err))
		}
	}

	// 2. Prune dangling Docker images with exclusive maintenance synchronization
	if e.dockerClient != nil {
		if err := e.pruneDanglingImages(ctx); err != nil {
			pruneErrs = append(pruneErrs, err)
		}
	}

	// 3. Prune dangling/orphaned ForgeLAB networks with zero active containers
	if e.dockerClient != nil {
		if err := e.PruneDanglingNetworks(ctx); err != nil {
			pruneErrs = append(pruneErrs, err)
		}
	}

	if len(pruneErrs) > 0 {
		return errors.Join(pruneErrs...)
	}
	return nil
}

// pruneDanglingImages acquires the exclusive buildMaintenanceMu lock to ensure no Docker image builds
// are active or can start while dangling Docker images are being pruned.
func (e *Engine) pruneDanglingImages(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if !e.buildMaintenanceMu.TryLock() {
		slog.Info("docker image prune waiting for active builds")
		e.buildMaintenanceMu.Lock()
	}
	defer e.buildMaintenanceMu.Unlock()

	slog.Info("docker image prune started")
	pruneFilters := make(client.Filters).Add("dangling", "true")
	if e.pruneFilterLabel != "" {
		pruneFilters.Add("label", e.pruneFilterLabel)
	}

	pruneRes, err := e.dockerClient.ImagePrune(ctx, client.ImagePruneOptions{Filters: pruneFilters})
	if err != nil {
		slog.Warn("failed to prune dangling docker images", "error", err)
		return fmt.Errorf("docker image prune failed: %w", err)
	}

	slog.Info("docker image prune completed", "deleted_count", len(pruneRes.Report.ImagesDeleted), "space_reclaimed_bytes", pruneRes.Report.SpaceReclaimed)
	return nil
}

// PruneOldProjectImages prunes older Docker images created for a specific project,
// preserving the latest keepRecent images to maintain fast rollbacks while freeing disk space.
func (e *Engine) PruneOldProjectImages(ctx context.Context, projectID string, keepRecent int) (int, error) {
	if e.dockerClient == nil || projectID == "" {
		return 0, nil
	}
	if keepRecent <= 0 {
		keepRecent = 3
	}

	opts := client.ImageListOptions{
		Filters: make(client.Filters).Add("label", fmt.Sprintf("forgelab.project_id=%s", projectID)),
	}

	imagesRes, err := e.dockerClient.ImageList(ctx, opts)
	if err != nil {
		return 0, fmt.Errorf("failed to list project images for pruning: %w", err)
	}

	images := imagesRes.Items
	if len(images) <= keepRecent {
		return 0, nil
	}

	// Sort images by Created timestamp descending (newest first)
	sort.Slice(images, func(i, j int) bool {
		return images[i].Created > images[j].Created
	})

	prunedCount := 0
	for _, img := range images[keepRecent:] {
		_, pruneErr := e.dockerClient.ImageRemove(ctx, img.ID, client.ImageRemoveOptions{
			PruneChildren: true,
		})
		if pruneErr == nil {
			prunedCount++
			shortID := img.ID
			if len(shortID) > 12 {
				shortID = shortID[:12]
			}
			slog.Info("pruned old project image", "project_id", projectID, "image_id", shortID)
		}
	}

	return prunedCount, nil
}
