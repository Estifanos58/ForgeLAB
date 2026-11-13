package docker

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/forgelab/backend/internal/models"
)

const (
	defaultVolumeHelperImage                 = "alpine:3.19"
	DefaultMaxVolumeRestoreCompressedBytes   = int64(100 * 1024 * 1024) // 100 MB
	DefaultMaxVolumeRestoreUncompressedBytes = int64(512 * 1024 * 1024) // 512 MB
	DefaultMaxVolumeRestoreFiles             = 50000
	DefaultDiskUsageWarningThresholdPercent  = 85
)

var (
	ErrInvalidVolumeName     = errors.New("invalid volume name")
	ErrVolumeNotFound        = errors.New("volume not found for project")
	ErrVolumeInUse           = errors.New("volume is currently mounted by running container(s); stop the service before restoring")
	ErrInvalidVolumeArchive  = errors.New("invalid or unsafe volume backup archive")
	ErrVolumeArchiveTooLarge = errors.New("volume backup archive exceeds maximum allowed size")
	ErrDockerUnavailable     = errors.New("docker engine is unavailable")
)

var validVolumeNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

// VolumeMountRef identifies a project service/container referencing a persistent volume.
type VolumeMountRef struct {
	ServiceID   uuid.UUID `json:"service_id,omitempty"`
	ServiceName string    `json:"service_name"`
	ContainerID string    `json:"container_id,omitempty"`
	Running     bool      `json:"running"`
	Target      string    `json:"target"`
	ReadOnly    bool      `json:"read_only"`
}

// ProjectVolumeInfo describes a persistent named volume scoped to a project.
type ProjectVolumeInfo struct {
	Name        string            `json:"name"`
	DockerName  string            `json:"docker_name"`
	Driver      string            `json:"driver"`
	Mountpoint  string            `json:"mountpoint,omitempty"`
	Scope       string            `json:"scope,omitempty"`
	CreatedAt   string            `json:"created_at,omitempty"`
	SizeBytes   int64             `json:"size_bytes"`
	RefCount    int64             `json:"ref_count"`
	InUse       bool              `json:"in_use"`
	Labels      map[string]string `json:"labels,omitempty"`
	Mounts      []VolumeMountRef  `json:"mounts"`
	BackupReady bool              `json:"backup_ready"`
}

// DiskUsageCategory holds storage usage metrics for a Docker resource class.
type DiskUsageCategory struct {
	Count       int64 `json:"count"`
	ActiveCount int64 `json:"active_count"`
	TotalBytes  int64 `json:"total_bytes"`
	Reclaimable int64 `json:"reclaimable_bytes"`
}

// DiskUsageSummary reports daemon-wide and project-scoped Docker storage visibility.
type DiskUsageSummary struct {
	DockerAvailable     bool              `json:"docker_available"`
	CollectedAt         time.Time         `json:"collected_at"`
	ProjectVolumesBytes int64             `json:"project_volumes_bytes"`
	ProjectVolumesCount int               `json:"project_volumes_count"`
	Containers          DiskUsageCategory `json:"containers"`
	Images              DiskUsageCategory `json:"images"`
	Volumes             DiskUsageCategory `json:"volumes"`
	BuildCache          DiskUsageCategory `json:"build_cache"`
	TotalBytes          int64             `json:"total_bytes"`
	TotalReclaimable    int64             `json:"total_reclaimable_bytes"`
	Warning             string            `json:"warning,omitempty"`
}

// ProjectVolumesResponse is returned by GET /api/projects/{id}/volumes.
type ProjectVolumesResponse struct {
	ProjectID       uuid.UUID           `json:"project_id"`
	ProjectName     string              `json:"project_name"`
	DockerAvailable bool                `json:"docker_available"`
	CollectedAt     time.Time           `json:"collected_at"`
	Volumes         []ProjectVolumeInfo `json:"volumes"`
	DiskUsage       DiskUsageSummary    `json:"disk_usage"`
}

// VolumeRestoreResult summarizes a completed volume restore operation.
type VolumeRestoreResult struct {
	VolumeName    string `json:"volume_name"`
	DockerName    string `json:"docker_name"`
	FilesRestored int    `json:"files_restored"`
	BytesRestored int64  `json:"bytes_restored"`
	ArchiveBytes  int64  `json:"archive_bytes"`
	Message       string `json:"message"`
}

// ValidateVolumeName checks that a volume identifier is syntactically safe and free of path traversal.
func ValidateVolumeName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" || len(name) > 128 {
		return "", fmt.Errorf("%w: must be 1-128 characters", ErrInvalidVolumeName)
	}
	if strings.Contains(name, "..") || strings.ContainsAny(name, `/\: `) {
		return "", fmt.Errorf("%w: contains illegal characters", ErrInvalidVolumeName)
	}
	if !validVolumeNamePattern.MatchString(name) {
		return "", fmt.Errorf("%w: must match [a-zA-Z0-9][a-zA-Z0-9._-]*", ErrInvalidVolumeName)
	}
	return name, nil
}

func projectVolumePrefix(projectID uuid.UUID) string {
	idStr := projectID.String()
	if len(idStr) >= 8 {
		idStr = idStr[:8]
	}
	return "forgelab-vol-" + idStr + "-"
}

// ListProjectVolumes enumerates persistent Docker volumes owned by a project along with mount references
// and Docker Engine disk usage telemetry.
func (e *Engine) ListProjectVolumes(ctx context.Context, projectID uuid.UUID) (*ProjectVolumesResponse, error) {
	return e.ListProjectVolumesWithOptions(ctx, projectID, false)
}

// ListProjectVolumesWithOptions enumerates persistent Docker volumes owned by a project along with mount references
// and Docker Engine disk usage telemetry, optionally bypassing the short-lived disk usage cache.
func (e *Engine) ListProjectVolumesWithOptions(ctx context.Context, projectID uuid.UUID, forceRefresh bool) (*ProjectVolumesResponse, error) {
	if e.projectService == nil {
		return nil, errors.New("project service unavailable")
	}

	project, err := e.projectService.GetProjectByIDWithoutOwnership(ctx, projectID)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	resp := &ProjectVolumesResponse{
		ProjectID:       project.ID,
		ProjectName:     project.Name,
		DockerAvailable: e.dockerClient != nil,
		CollectedAt:     now,
		Volumes:         []ProjectVolumeInfo{},
		DiskUsage: DiskUsageSummary{
			DockerAvailable: e.dockerClient != nil,
			CollectedAt:     now,
		},
	}

	prefix := projectVolumePrefix(projectID)
	volMap := make(map[string]*ProjectVolumeInfo)
	serviceByID := make(map[uuid.UUID]*models.Service)
	serviceByName := make(map[string]*models.Service)
	declaredVolNames := make(map[string]bool)

	if e.serviceService != nil {
		svcs, svcErr := e.serviceService.ListServices(ctx, projectID)
		if svcErr == nil {
			for _, svc := range svcs {
				serviceByID[svc.ID] = svc
				serviceByName[svc.Name] = svc
				for _, v := range svc.Volumes {
					if !isNamedVolumeSource(v.Source, v.Type) {
						continue
					}
					logicalName := strings.TrimSpace(v.Source)
					if logicalName == "" {
						continue
					}
					dockerName := logicalName
					if !strings.HasPrefix(logicalName, prefix) {
						dockerName = prefix + logicalName
					} else {
						logicalName = strings.TrimPrefix(logicalName, prefix)
					}
					declaredVolNames[dockerName] = true

					entry, exists := volMap[dockerName]
					if !exists {
						entry = &ProjectVolumeInfo{
							Name:       logicalName,
							DockerName: dockerName,
							Driver:     "local",
							SizeBytes:  -1,
							RefCount:   -1,
							Mounts:     []VolumeMountRef{},
						}
						volMap[dockerName] = entry
					}
					var cid string
					if svc.ContainerID != nil {
						cid = *svc.ContainerID
					}
					entry.Mounts = append(entry.Mounts, VolumeMountRef{
						ServiceID:   svc.ID,
						ServiceName: svc.Name,
						ContainerID: cid,
						Target:      v.Target,
						ReadOnly:    v.ReadOnly,
					})
				}
			}
		}
	}

	if e.dockerClient == nil {
		resp.DiskUsage.Warning = "Docker daemon connection is unavailable."
		resp.Volumes = flattenSortedVolumes(volMap)
		return resp, nil
	}

	// Query containers, volumes, and daemon disk usage concurrently with bounded timeouts
	// so a slow/stalled `system/df` never hangs the entire Storage response.
	var (
		wg                sync.WaitGroup
		containerList     client.ContainerListResult
		listContErr       error
		dfSummary         DiskUsageSummary
		usageByDockerName map[string]volumeDiskUsage
		dfErr             error
		volListRes        client.VolumeListResult
		volListErr        error
	)

	wg.Add(3)

	go func() {
		defer wg.Done()
		cCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		containerList, listContErr = e.dockerClient.ContainerList(cCtx, client.ContainerListOptions{All: true})
	}()

	go func() {
		defer wg.Done()
		dfSummary, usageByDockerName, dfErr = e.getDockerDiskUsageCached(ctx, forceRefresh)
	}()

	go func() {
		defer wg.Done()
		vCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		volListRes, volListErr = e.dockerClient.VolumeList(vCtx, client.VolumeListOptions{})
	}()

	wg.Wait()

	// Inspect Docker containers to find active/running mounts on project volumes.
	runningByContainerID := make(map[string]bool)
	liveMountsByDockerName := make(map[string][]VolumeMountRef)
	if listContErr != nil {
		slog.Warn("failed to list containers during volume inspection", "project_id", projectID, "error", listContErr)
	} else {
		projIDStr := projectID.String()
		for _, c := range containerList.Items {
			isRunning := c.State == container.StateRunning
			runningByContainerID[c.ID] = isRunning
			if len(c.ID) > 12 {
				runningByContainerID[c.ID[:12]] = isRunning
			}

			belongs := c.Labels != nil && c.Labels["forgelab.project_id"] == projIDStr
			if !belongs {
				for _, m := range c.Mounts {
					if m.Name != "" && (strings.HasPrefix(m.Name, prefix) || declaredVolNames[m.Name]) {
						belongs = true
						break
					}
				}
			}
			if !belongs {
				continue
			}

			svcName := ""
			var svcID uuid.UUID
			if c.Labels != nil {
				svcName = c.Labels["forgelab.service_name"]
				if rawSID := c.Labels["forgelab.service_id"]; rawSID != "" {
					if parsedSID, pErr := uuid.Parse(rawSID); pErr == nil {
						svcID = parsedSID
					}
				}
			}
			if svcName == "" && svcID != uuid.Nil {
				if svc, ok := serviceByID[svcID]; ok {
					svcName = svc.Name
				}
			}
			if svcName == "" && len(c.Names) > 0 {
				svcName = strings.TrimPrefix(c.Names[0], "/")
			}

			for _, m := range c.Mounts {
				if m.Type != "volume" || m.Name == "" {
					continue
				}
				liveMountsByDockerName[m.Name] = append(liveMountsByDockerName[m.Name], VolumeMountRef{
					ServiceID:   svcID,
					ServiceName: svcName,
					ContainerID: c.ID,
					Running:     isRunning,
					Target:      m.Destination,
					ReadOnly:    !m.RW,
				})
			}
		}
	}

	// Mark running state on configured mounts from container inventory or bounded fallback inspect
	for _, entry := range volMap {
		for i := range entry.Mounts {
			cid := entry.Mounts[i].ContainerID
			if cid == "" {
				continue
			}
			if running, known := runningByContainerID[cid]; known {
				entry.Mounts[i].Running = running
				if running {
					entry.InUse = true
				}
				continue
			}
			inspCtx, cancelInsp := context.WithTimeout(ctx, 2*time.Second)
			insp, iErr := e.dockerClient.ContainerInspect(inspCtx, cid, client.ContainerInspectOptions{})
			cancelInsp()
			if iErr == nil && insp.Container.State != nil {
				entry.Mounts[i].Running = insp.Container.State.Running
				if insp.Container.State.Running {
					entry.InUse = true
				}
			}
		}
	}

	// Apply DiskUsage (system/df) telemetry without letting a slow/timed-out system/df block volume management
	if dfErr != nil {
		resp.DiskUsage.DockerAvailable = false
		resp.DiskUsage.Warning = dfSummary.Warning
		if listContErr != nil && volListErr != nil {
			resp.DockerAvailable = false
		}
	} else {
		dfSummary.ProjectVolumesBytes = resp.DiskUsage.ProjectVolumesBytes
		dfSummary.ProjectVolumesCount = resp.DiskUsage.ProjectVolumesCount
		resp.DiskUsage = dfSummary
	}

	// List Docker volumes and merge with project volumes
	if volListErr != nil {
		if resp.DiskUsage.Warning == "" {
			resp.DiskUsage.Warning = fmt.Sprintf("Failed to list Docker volumes: %v", volListErr)
		}
		if listContErr != nil {
			resp.DockerAvailable = false
			resp.DiskUsage.DockerAvailable = false
		}
	} else {
		projIDStr := projectID.String()
		for _, dv := range volListRes.Items {
			isOwned := (dv.Labels != nil && dv.Labels["forgelab.project_id"] == projIDStr) ||
				strings.HasPrefix(dv.Name, prefix) ||
				volMap[dv.Name] != nil
			if !isOwned {
				continue
			}

			logicalName := ""
			if dv.Labels != nil {
				logicalName = dv.Labels["forgelab.volume_name"]
			}
			if logicalName == "" {
				logicalName = strings.TrimPrefix(dv.Name, prefix)
			}

			entry, exists := volMap[dv.Name]
			if !exists {
				entry = &ProjectVolumeInfo{
					Name:       logicalName,
					DockerName: dv.Name,
					SizeBytes:  -1,
					RefCount:   -1,
					Mounts:     []VolumeMountRef{},
				}
				volMap[dv.Name] = entry
			}
			if entry.Name == "" {
				entry.Name = logicalName
			}
			entry.Driver = dv.Driver
			entry.Mountpoint = dv.Mountpoint
			entry.Scope = dv.Scope
			entry.CreatedAt = dv.CreatedAt
			entry.Labels = dv.Labels
			entry.BackupReady = true

			if dv.UsageData != nil {
				entry.SizeBytes = dv.UsageData.Size
				entry.RefCount = dv.UsageData.RefCount
			}
			if dfUsage, ok := usageByDockerName[dv.Name]; ok {
				if dfUsage.size >= 0 || entry.SizeBytes < 0 {
					entry.SizeBytes = dfUsage.size
				}
				if dfUsage.refCount >= 0 || entry.RefCount < 0 {
					entry.RefCount = dfUsage.refCount
				}
			}
		}
	}

	// Merge live mounts observed directly on running/stopped containers
	for dockerName, liveMounts := range liveMountsByDockerName {
		entry, exists := volMap[dockerName]
		if !exists {
			continue
		}
		for _, lm := range liveMounts {
			if lm.Running {
				entry.InUse = true
			}
			merged := false
			for i := range entry.Mounts {
				if entry.Mounts[i].ServiceName == lm.ServiceName && entry.Mounts[i].Target == lm.Target {
					if lm.ContainerID != "" {
						entry.Mounts[i].ContainerID = lm.ContainerID
					}
					if lm.Running {
						entry.Mounts[i].Running = true
					}
					merged = true
					break
				}
			}
			if !merged {
				entry.Mounts = append(entry.Mounts, lm)
			}
		}
	}

	var projectBytes int64
	for _, entry := range volMap {
		if entry.SizeBytes > 0 {
			projectBytes += entry.SizeBytes
		}
	}
	resp.DiskUsage.ProjectVolumesBytes = projectBytes
	resp.DiskUsage.ProjectVolumesCount = len(volMap)
	resp.Volumes = flattenSortedVolumes(volMap)
	return resp, nil
}

func flattenSortedVolumes(m map[string]*ProjectVolumeInfo) []ProjectVolumeInfo {
	out := make([]ProjectVolumeInfo, 0, len(m))
	for _, v := range m {
		if v.Mounts == nil {
			v.Mounts = []VolumeMountRef{}
		}
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].DockerName < out[j].DockerName
	})
	return out
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

type volumeDiskUsage struct {
	size     int64
	refCount int64
}

type diskUsageCacheEntry struct {
	summary           DiskUsageSummary
	usageByDockerName map[string]volumeDiskUsage
	expiresAt         time.Time
}

type diskUsageInFlightCall struct {
	done              chan struct{}
	summary           DiskUsageSummary
	usageByDockerName map[string]volumeDiskUsage
	err               error
}

func cloneVolumeDiskUsageMap(src map[string]volumeDiskUsage) map[string]volumeDiskUsage {
	if len(src) == 0 {
		return make(map[string]volumeDiskUsage)
	}
	dst := make(map[string]volumeDiskUsage, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// collectDockerDiskUsage queries Docker daemon system disk usage using short-lived caching,
// singleflight deduplication, and bounded timeouts so slow Docker daemons never hang callers.
func (e *Engine) collectDockerDiskUsage(ctx context.Context) (DiskUsageSummary, map[string]volumeDiskUsage, error) {
	return e.getDockerDiskUsageCached(ctx, false)
}

func (e *Engine) getDockerDiskUsageCached(ctx context.Context, forceRefresh bool) (DiskUsageSummary, map[string]volumeDiskUsage, error) {
	if e.dockerClient == nil {
		return DiskUsageSummary{
			DockerAvailable: false,
			CollectedAt:     time.Now().UTC(),
			Warning:         "Docker daemon connection is unavailable.",
		}, make(map[string]volumeDiskUsage), ErrDockerUnavailable
	}

	now := time.Now().UTC()
	e.diskUsageCacheMu.Lock()
	ttl := e.diskUsageCacheTTL
	if !forceRefresh && ttl > 0 && e.diskUsageCache != nil && now.Before(e.diskUsageCache.expiresAt) {
		cachedSummary := e.diskUsageCache.summary
		cachedMap := cloneVolumeDiskUsageMap(e.diskUsageCache.usageByDockerName)
		e.diskUsageCacheMu.Unlock()
		return cachedSummary, cachedMap, nil
	}
	if e.diskUsageInFlight != nil {
		existing := e.diskUsageInFlight
		e.diskUsageCacheMu.Unlock()
		select {
		case <-existing.done:
			return existing.summary, cloneVolumeDiskUsageMap(existing.usageByDockerName), existing.err
		case <-ctx.Done():
			return DiskUsageSummary{
				DockerAvailable: true,
				CollectedAt:     time.Now().UTC(),
				Warning:         fmt.Sprintf("Disk usage query cancelled: %v", ctx.Err()),
			}, make(map[string]volumeDiskUsage), ctx.Err()
		}
	}

	call := &diskUsageInFlightCall{done: make(chan struct{})}
	e.diskUsageInFlight = call
	e.diskUsageCacheMu.Unlock()

	summary, usageByDockerName, err := e.collectDockerDiskUsageFresh(context.WithoutCancel(ctx))

	e.diskUsageCacheMu.Lock()
	e.diskUsageInFlight = nil
	if err == nil && ttl > 0 {
		e.diskUsageCache = &diskUsageCacheEntry{
			summary:           summary,
			usageByDockerName: cloneVolumeDiskUsageMap(usageByDockerName),
			expiresAt:         time.Now().UTC().Add(ttl),
		}
	}
	call.summary = summary
	call.usageByDockerName = cloneVolumeDiskUsageMap(usageByDockerName)
	call.err = err
	close(call.done)
	e.diskUsageCacheMu.Unlock()

	return summary, usageByDockerName, err
}

func (e *Engine) collectDockerDiskUsageFresh(ctx context.Context) (DiskUsageSummary, map[string]volumeDiskUsage, error) {
	now := time.Now().UTC()
	summary := DiskUsageSummary{
		DockerAvailable: e.dockerClient != nil,
		CollectedAt:     now,
	}
	usageByDockerName := make(map[string]volumeDiskUsage)
	if e.dockerClient == nil {
		summary.Warning = "Docker daemon connection is unavailable."
		return summary, usageByDockerName, ErrDockerUnavailable
	}

	// Bound verbose DiskUsage (system/df?verbose=1) which can hang on Docker Desktop / BuildKit.
	dfCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	dfRes, dfErr := e.dockerClient.DiskUsage(dfCtx, client.DiskUsageOptions{
		Containers: true,
		Images:     true,
		Volumes:    true,
		BuildCache: true,
		Verbose:    true,
	})
	cancel()

	usedLightweightFallback := false
	if dfErr != nil && (errors.Is(dfErr, context.DeadlineExceeded) || errors.Is(dfCtx.Err(), context.DeadlineExceeded)) {
		slog.Warn("verbose Docker DiskUsage timed out; retrying lightweight summary without build cache/verbose scan", "error", dfErr)
		fbCtx, fbCancel := context.WithTimeout(ctx, 3*time.Second)
		fbRes, fbErr := e.dockerClient.DiskUsage(fbCtx, client.DiskUsageOptions{
			Containers: true,
			Images:     true,
			Volumes:    true,
			BuildCache: false,
			Verbose:    false,
		})
		fbCancel()
		if fbErr == nil {
			dfRes = fbRes
			dfErr = nil
			usedLightweightFallback = true
		}
	}

	if dfErr != nil {
		summary.DockerAvailable = false
		if errors.Is(dfErr, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			summary.Warning = "Docker daemon disk usage inspection timed out; project volumes are listed below."
		} else {
			summary.Warning = fmt.Sprintf("Failed to collect Docker disk usage: %v", dfErr)
		}
		return summary, usageByDockerName, dfErr
	}

	if usedLightweightFallback {
		summary.Warning = "Verbose disk usage and build cache scan timed out; showing fast Docker disk summary."
	}

	summary.Containers = DiskUsageCategory{
		Count:       dfRes.Containers.TotalCount,
		ActiveCount: dfRes.Containers.ActiveCount,
		TotalBytes:  maxInt64(0, dfRes.Containers.TotalSize),
		Reclaimable: maxInt64(0, dfRes.Containers.Reclaimable),
	}
	summary.Images = DiskUsageCategory{
		Count:       dfRes.Images.TotalCount,
		ActiveCount: dfRes.Images.ActiveCount,
		TotalBytes:  maxInt64(0, dfRes.Images.TotalSize),
		Reclaimable: maxInt64(0, dfRes.Images.Reclaimable),
	}
	summary.Volumes = DiskUsageCategory{
		Count:       dfRes.Volumes.TotalCount,
		ActiveCount: dfRes.Volumes.ActiveCount,
		TotalBytes:  maxInt64(0, dfRes.Volumes.TotalSize),
		Reclaimable: maxInt64(0, dfRes.Volumes.Reclaimable),
	}
	summary.BuildCache = DiskUsageCategory{
		Count:       dfRes.BuildCache.TotalCount,
		ActiveCount: dfRes.BuildCache.ActiveCount,
		TotalBytes:  maxInt64(0, dfRes.BuildCache.TotalSize),
		Reclaimable: maxInt64(0, dfRes.BuildCache.Reclaimable),
	}
	summary.TotalBytes = summary.Containers.TotalBytes +
		summary.Images.TotalBytes +
		summary.Volumes.TotalBytes +
		summary.BuildCache.TotalBytes
	summary.TotalReclaimable = summary.Containers.Reclaimable +
		summary.Images.Reclaimable +
		summary.Volumes.Reclaimable +
		summary.BuildCache.Reclaimable

	for _, dv := range dfRes.Volumes.Items {
		if dv.UsageData != nil {
			usageByDockerName[dv.Name] = volumeDiskUsage{
				size:     dv.UsageData.Size,
				refCount: dv.UsageData.RefCount,
			}
		}
	}

	return summary, usageByDockerName, nil
}

func (e *Engine) resolveProjectVolume(ctx context.Context, projectID uuid.UUID, rawName string) (*ProjectVolumeInfo, error) {
	validName, err := ValidateVolumeName(rawName)
	if err != nil {
		return nil, err
	}

	volsResp, err := e.ListProjectVolumes(ctx, projectID)
	if err != nil {
		return nil, err
	}

	prefix := projectVolumePrefix(projectID)
	expectedDocker := prefix + validName
	for i := range volsResp.Volumes {
		v := &volsResp.Volumes[i]
		if v.Name == validName || v.DockerName == validName || v.DockerName == expectedDocker {
			return v, nil
		}
	}
	return nil, ErrVolumeNotFound
}

func (e *Engine) ensureVolumeHelperImage(ctx context.Context) (string, error) {
	img := e.volumeHelperImage
	if img == "" {
		img = defaultVolumeHelperImage
	}
	if _, err := e.dockerClient.ImageInspect(ctx, img); err == nil {
		return img, nil
	}
	reader, pullErr := e.dockerClient.ImagePull(ctx, img, client.ImagePullOptions{})
	if pullErr != nil {
		return "", fmt.Errorf("failed to pull helper image %s: %w", img, pullErr)
	}
	if reader != nil {
		_, _ = io.Copy(io.Discard, reader)
		_ = reader.Close()
	}
	return img, nil
}

// BackupProjectVolume streams a gzipped tar archive (.tar.gz) of the specified project volume to w.
func (e *Engine) BackupProjectVolume(ctx context.Context, projectID uuid.UUID, volumeName string, w io.Writer) error {
	if e.dockerClient == nil {
		return ErrDockerUnavailable
	}
	if w == nil {
		return errors.New("destination writer is nil")
	}

	volInfo, err := e.resolveProjectVolume(ctx, projectID, volumeName)
	if err != nil {
		return err
	}
	if !volInfo.BackupReady {
		if _, iErr := e.dockerClient.VolumeInspect(ctx, volInfo.DockerName, client.VolumeInspectOptions{}); iErr != nil {
			if cerrdefs.IsNotFound(iErr) {
				return fmt.Errorf("%w: volume '%s' has not been created in Docker yet", ErrVolumeNotFound, volInfo.Name)
			}
			return fmt.Errorf("failed to inspect volume '%s': %w", volInfo.DockerName, iErr)
		}
	}

	helperImg, err := e.ensureVolumeHelperImage(ctx)
	if err != nil {
		return err
	}

	pidsLimit := int64(64)
	createRes, err := e.dockerClient.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image:        helperImg,
			Cmd:          []string{"tar", "-czf", "-", "-C", "/volume", "."},
			Tty:          false,
			AttachStdout: true,
			AttachStderr: true,
			Labels: map[string]string{
				"forgelab.managed":    "true",
				"forgelab.helper":     "volume-backup",
				"forgelab.project_id": projectID.String(),
			},
		},
		HostConfig: &container.HostConfig{
			Binds:          []string{volInfo.DockerName + ":/volume:ro"},
			CapDrop:        []string{"ALL"},
			SecurityOpt:    []string{"no-new-privileges:true"},
			NetworkMode:    "none",
			ReadonlyRootfs: true,
			RestartPolicy:  container.RestartPolicy{Name: "no"},
			Resources: container.Resources{
				Memory:    256 * 1024 * 1024,
				NanoCPUs:  1_000_000_000,
				PidsLimit: &pidsLimit,
			},
		},
		Name: fmt.Sprintf("forgelab-volbak-%s-%d", projectID.String()[:8], time.Now().UnixNano()),
	})
	if err != nil {
		return fmt.Errorf("failed to create volume backup helper container: %w", err)
	}
	helperID := createRes.ID
	defer func() {
		cleanCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = e.dockerClient.ContainerRemove(cleanCtx, helperID, client.ContainerRemoveOptions{
			Force:         true,
			RemoveVolumes: false,
		})
	}()

	attachRes, err := e.dockerClient.ContainerAttach(ctx, helperID, client.ContainerAttachOptions{
		Stream: true,
		Stdout: true,
		Stderr: true,
	})
	if err != nil {
		return fmt.Errorf("failed to attach to backup container stream: %w", err)
	}
	defer attachRes.Close()

	waitRes := e.dockerClient.ContainerWait(ctx, helperID, client.ContainerWaitOptions{
		Condition: container.WaitConditionNextExit,
	})

	if _, err := e.dockerClient.ContainerStart(ctx, helperID, client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("failed to start volume backup helper container: %w", err)
	}

	var stderrBuf bytes.Buffer
	_, copyErr := stdcopy.StdCopy(w, &stderrBuf, attachRes.Reader)
	attachRes.Close()

	var exitCode int64
	select {
	case wErr := <-waitRes.Error:
		if wErr != nil && copyErr == nil {
			return fmt.Errorf("volume backup wait failed: %w", wErr)
		}
	case status := <-waitRes.Result:
		exitCode = status.StatusCode
	case <-ctx.Done():
		return ctx.Err()
	}

	if copyErr != nil {
		return fmt.Errorf("failed reading backup archive stream: %w", copyErr)
	}
	if exitCode != 0 {
		errDetail := strings.TrimSpace(stderrBuf.String())
		if errDetail != "" {
			return fmt.Errorf("volume backup failed with exit code %d: %s", exitCode, errDetail)
		}
		return fmt.Errorf("volume backup failed with exit code %d", exitCode)
	}
	return nil
}

// RestoreProjectVolume validates an incoming .tar.gz stream and restores its contents into the specified project volume.
func (e *Engine) RestoreProjectVolume(ctx context.Context, projectID uuid.UUID, volumeName string, r io.Reader) (*VolumeRestoreResult, error) {
	if e.dockerClient == nil {
		return nil, ErrDockerUnavailable
	}
	if r == nil {
		return nil, fmt.Errorf("%w: empty backup payload", ErrInvalidVolumeArchive)
	}

	volInfo, err := e.resolveProjectVolume(ctx, projectID, volumeName)
	if err != nil {
		return nil, err
	}
	if volInfo.InUse {
		return nil, fmt.Errorf("%w: volume '%s' is mounted by active service container(s)", ErrVolumeInUse, volInfo.Name)
	}

	// Stage and strictly validate the archive BEFORE modifying any volume data.
	validatedTarFile, stats, err := validateAndStageVolumeRestoreTar(
		ctx,
		r,
		DefaultMaxVolumeRestoreCompressedBytes,
		DefaultMaxVolumeRestoreUncompressedBytes,
		DefaultMaxVolumeRestoreFiles,
	)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = validatedTarFile.Close()
		_ = os.Remove(validatedTarFile.Name())
	}()

	// Ensure target volume exists in Docker daemon with ownership labels
	if !volInfo.BackupReady {
		if _, inspErr := e.dockerClient.VolumeInspect(ctx, volInfo.DockerName, client.VolumeInspectOptions{}); inspErr != nil {
			if cerrdefs.IsNotFound(inspErr) {
				if _, createVolErr := e.dockerClient.VolumeCreate(ctx, client.VolumeCreateOptions{
					Name: volInfo.DockerName,
					Labels: map[string]string{
						"forgelab.project_id":  projectID.String(),
						"forgelab.volume_name": volInfo.Name,
					},
				}); createVolErr != nil {
					return nil, fmt.Errorf("failed to initialize volume '%s' for restore: %w", volInfo.DockerName, createVolErr)
				}
			} else {
				return nil, fmt.Errorf("failed to inspect volume '%s': %w", volInfo.DockerName, inspErr)
			}
		}
	}

	helperImg, err := e.ensureVolumeHelperImage(ctx)
	if err != nil {
		return nil, err
	}

	pidsLimit := int64(64)
	createRes, err := e.dockerClient.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image: helperImg,
			Cmd:   []string{"sh", "-c", "find /volume -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +"},
			Labels: map[string]string{
				"forgelab.managed":    "true",
				"forgelab.helper":     "volume-restore",
				"forgelab.project_id": projectID.String(),
			},
		},
		HostConfig: &container.HostConfig{
			Binds:         []string{volInfo.DockerName + ":/volume"},
			CapDrop:       []string{"ALL"},
			SecurityOpt:   []string{"no-new-privileges:true"},
			NetworkMode:   "none",
			RestartPolicy: container.RestartPolicy{Name: "no"},
			Resources: container.Resources{
				Memory:    256 * 1024 * 1024,
				NanoCPUs:  1_000_000_000,
				PidsLimit: &pidsLimit,
			},
		},
		Name: fmt.Sprintf("forgelab-volrst-%s-%d", projectID.String()[:8], time.Now().UnixNano()),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create volume restore helper container: %w", err)
	}
	helperID := createRes.ID
	defer func() {
		cleanCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = e.dockerClient.ContainerRemove(cleanCtx, helperID, client.ContainerRemoveOptions{
			Force:         true,
			RemoveVolumes: false,
		})
	}()

	waitRes := e.dockerClient.ContainerWait(ctx, helperID, client.ContainerWaitOptions{
		Condition: container.WaitConditionNextExit,
	})
	if _, err := e.dockerClient.ContainerStart(ctx, helperID, client.ContainerStartOptions{}); err != nil {
		return nil, fmt.Errorf("failed to start volume wipe helper container: %w", err)
	}

	var exitCode int64
	select {
	case wErr := <-waitRes.Error:
		if wErr != nil {
			return nil, fmt.Errorf("volume wipe wait failed: %w", wErr)
		}
	case status := <-waitRes.Result:
		exitCode = status.StatusCode
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("volume cleanup helper exited with code %d", exitCode)
	}

	if _, err := validatedTarFile.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("failed to rewind validated archive: %w", err)
	}

	if _, err := e.dockerClient.CopyToContainer(ctx, helperID, client.CopyToContainerOptions{
		DestinationPath:           "/volume",
		Content:                   validatedTarFile,
		AllowOverwriteDirWithFile: true,
	}); err != nil {
		return nil, fmt.Errorf("failed extracting backup archive into volume '%s': %w", volInfo.Name, err)
	}

	return &VolumeRestoreResult{
		VolumeName:    volInfo.Name,
		DockerName:    volInfo.DockerName,
		FilesRestored: stats.filesRestored,
		BytesRestored: stats.bytesRestored,
		ArchiveBytes:  stats.archiveBytes,
		Message:       fmt.Sprintf("Volume '%s' restored successfully (%d file(s), %d bytes).", volInfo.Name, stats.filesRestored, stats.bytesRestored),
	}, nil
}

type restoreArchiveStats struct {
	filesRestored int
	bytesRestored int64
	archiveBytes  int64
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func validateAndStageVolumeRestoreTar(
	ctx context.Context,
	src io.Reader,
	maxCompressedBytes int64,
	maxUncompressedBytes int64,
	maxFiles int,
) (*os.File, restoreArchiveStats, error) {
	var stats restoreArchiveStats
	limitedSrc := io.LimitReader(src, maxCompressedBytes+1)
	cr := &countingReader{r: limitedSrc}

	gz, err := gzip.NewReader(cr)
	if err != nil {
		if cr.n > maxCompressedBytes {
			return nil, stats, fmt.Errorf("%w (compressed stream limit %d bytes)", ErrVolumeArchiveTooLarge, maxCompressedBytes)
		}
		return nil, stats, fmt.Errorf("%w: expected valid gzip-compressed tar archive: %v", ErrInvalidVolumeArchive, err)
	}
	defer func() {
		_ = gz.Close()
	}()

	tmpFile, err := os.CreateTemp("", "forgelab-vol-restore-*.tar")
	if err != nil {
		return nil, stats, fmt.Errorf("failed to allocate temporary archive file: %w", err)
	}
	cleanup := func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmpFile.Name())
	}

	tw := tar.NewWriter(tmpFile)
	tr := tar.NewReader(gz)

	for {
		if ctxErr := ctx.Err(); ctxErr != nil {
			cleanup()
			return nil, stats, ctxErr
		}
		if cr.n > maxCompressedBytes {
			cleanup()
			return nil, stats, fmt.Errorf("%w (compressed stream limit %d bytes)", ErrVolumeArchiveTooLarge, maxCompressedBytes)
		}

		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			cleanup()
			return nil, stats, fmt.Errorf("%w: corrupted tar header: %v", ErrInvalidVolumeArchive, err)
		}

		cleanName, err := sanitizeTarEntryPath(hdr.Name)
		if err != nil {
			cleanup()
			return nil, stats, err
		}

		// Strip setuid/setgid bits from restored files/directories.
		sanitizedMode := hdr.Mode &^ 0o6000
		if sanitizedMode == 0 {
			if hdr.Typeflag == tar.TypeDir {
				sanitizedMode = 0o755
			} else {
				sanitizedMode = 0o644
			}
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if cleanName == "." {
				continue
			}
			outHdr := &tar.Header{
				Name:     cleanName + "/",
				Mode:     sanitizedMode,
				ModTime:  hdr.ModTime,
				Typeflag: tar.TypeDir,
				Uid:      hdr.Uid,
				Gid:      hdr.Gid,
			}
			if err := tw.WriteHeader(outHdr); err != nil {
				cleanup()
				return nil, stats, fmt.Errorf("failed writing sanitized dir header: %w", err)
			}

		case tar.TypeReg, tar.TypeRegA:
			if cleanName == "." {
				cleanup()
				return nil, stats, fmt.Errorf("%w: regular file cannot overwrite archive root", ErrInvalidVolumeArchive)
			}
			if hdr.Size < 0 {
				cleanup()
				return nil, stats, fmt.Errorf("%w: negative file size for %s", ErrInvalidVolumeArchive, hdr.Name)
			}
			stats.filesRestored++
			if stats.filesRestored > maxFiles {
				cleanup()
				return nil, stats, fmt.Errorf("%w: exceeds maximum file count (%d)", ErrVolumeArchiveTooLarge, maxFiles)
			}
			if stats.bytesRestored+hdr.Size > maxUncompressedBytes {
				cleanup()
				return nil, stats, fmt.Errorf("%w: uncompressed payload exceeds limit (%d bytes)", ErrVolumeArchiveTooLarge, maxUncompressedBytes)
			}

			outHdr := &tar.Header{
				Name:     cleanName,
				Mode:     sanitizedMode,
				Size:     hdr.Size,
				ModTime:  hdr.ModTime,
				Typeflag: tar.TypeReg,
				Uid:      hdr.Uid,
				Gid:      hdr.Gid,
			}
			if err := tw.WriteHeader(outHdr); err != nil {
				cleanup()
				return nil, stats, fmt.Errorf("failed writing sanitized file header: %w", err)
			}

			n, copyErr := io.CopyN(tw, tr, hdr.Size)
			if copyErr != nil {
				cleanup()
				return nil, stats, fmt.Errorf("%w: failed reading file '%s': %v", ErrInvalidVolumeArchive, cleanName, copyErr)
			}
			stats.bytesRestored += n

		case tar.TypeSymlink:
			linkTarget, lErr := validateInternalSymlinkTarget(cleanName, hdr.Linkname)
			if lErr != nil {
				cleanup()
				return nil, stats, lErr
			}
			outHdr := &tar.Header{
				Name:     cleanName,
				Mode:     0o777,
				ModTime:  hdr.ModTime,
				Typeflag: tar.TypeSymlink,
				Linkname: linkTarget,
				Uid:      hdr.Uid,
				Gid:      hdr.Gid,
			}
			if err := tw.WriteHeader(outHdr); err != nil {
				cleanup()
				return nil, stats, fmt.Errorf("failed writing sanitized symlink header: %w", err)
			}

		case tar.TypeLink:
			linkTarget, lErr := sanitizeTarEntryPath(hdr.Linkname)
			if lErr != nil || linkTarget == "." {
				cleanup()
				return nil, stats, fmt.Errorf("%w: unsafe hardlink target %q", ErrInvalidVolumeArchive, hdr.Linkname)
			}
			outHdr := &tar.Header{
				Name:     cleanName,
				Mode:     sanitizedMode,
				ModTime:  hdr.ModTime,
				Typeflag: tar.TypeLink,
				Linkname: linkTarget,
				Uid:      hdr.Uid,
				Gid:      hdr.Gid,
			}
			if err := tw.WriteHeader(outHdr); err != nil {
				cleanup()
				return nil, stats, fmt.Errorf("failed writing sanitized hardlink header: %w", err)
			}

		case tar.TypeChar, tar.TypeBlock, tar.TypeFifo:
			cleanup()
			return nil, stats, fmt.Errorf("%w: special device or FIFO entry '%s' is not permitted", ErrInvalidVolumeArchive, hdr.Name)

		case tar.TypeXHeader, tar.TypeXGlobalHeader:
			continue

		default:
			cleanup()
			return nil, stats, fmt.Errorf("%w: unsupported tar entry type %q for '%s'", ErrInvalidVolumeArchive, hdr.Typeflag, hdr.Name)
		}
	}

	if err := tw.Close(); err != nil {
		cleanup()
		return nil, stats, fmt.Errorf("failed finalizing sanitized tar stream: %w", err)
	}
	stats.archiveBytes = cr.n
	if _, err := tmpFile.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, stats, fmt.Errorf("failed rewinding validated archive: %w", err)
	}

	return tmpFile, stats, nil
}

func sanitizeTarEntryPath(raw string) (string, error) {
	if strings.ContainsRune(raw, 0) {
		return "", fmt.Errorf("%w: path contains null byte", ErrInvalidVolumeArchive)
	}
	norm := strings.ReplaceAll(strings.TrimSpace(raw), "\\", "/")
	if norm == "" {
		return "", fmt.Errorf("%w: empty entry name", ErrInvalidVolumeArchive)
	}
	if strings.HasPrefix(norm, "/") {
		return "", fmt.Errorf("%w: absolute path '%s' is forbidden", ErrInvalidVolumeArchive, raw)
	}
	// Reject Windows drive letter prefixes such as C:foo
	if len(norm) >= 2 && ((norm[0] >= 'a' && norm[0] <= 'z') || (norm[0] >= 'A' && norm[0] <= 'Z')) && norm[1] == ':' {
		return "", fmt.Errorf("%w: drive-prefixed path '%s' is forbidden", ErrInvalidVolumeArchive, raw)
	}

	cleaned := path.Clean(norm)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "/") {
		return "", fmt.Errorf("%w: path traversal detected in '%s'", ErrInvalidVolumeArchive, raw)
	}
	return cleaned, nil
}

func validateInternalSymlinkTarget(entryPath, rawTarget string) (string, error) {
	if strings.ContainsRune(rawTarget, 0) || strings.TrimSpace(rawTarget) == "" {
		return "", fmt.Errorf("%w: invalid symlink target for '%s'", ErrInvalidVolumeArchive, entryPath)
	}
	norm := strings.ReplaceAll(rawTarget, "\\", "/")
	if strings.HasPrefix(norm, "/") {
		return "", fmt.Errorf("%w: symlink '%s' points to absolute target '%s'", ErrInvalidVolumeArchive, entryPath, rawTarget)
	}
	if len(norm) >= 2 && ((norm[0] >= 'a' && norm[0] <= 'z') || (norm[0] >= 'A' && norm[0] <= 'Z')) && norm[1] == ':' {
		return "", fmt.Errorf("%w: symlink '%s' contains drive prefix '%s'", ErrInvalidVolumeArchive, entryPath, rawTarget)
	}
	parentDir := path.Dir(entryPath)
	resolved := path.Clean(path.Join(parentDir, norm))
	if resolved == ".." || strings.HasPrefix(resolved, "../") || strings.HasPrefix(resolved, "/") {
		return "", fmt.Errorf("%w: symlink '%s' escapes volume root via '%s'", ErrInvalidVolumeArchive, entryPath, rawTarget)
	}
	return norm, nil
}
