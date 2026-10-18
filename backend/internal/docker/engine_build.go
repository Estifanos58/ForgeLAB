package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/google/uuid"

	"github.com/forgelab/backend/internal/agent"
	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/tararchive"
)

type buildContextSizeKey struct{}

type buildStreamRoundTripper struct {
	base http.RoundTripper
}

func (rt *buildStreamRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.Contains(req.URL.Path, "/build") {
		if sz, ok := req.Context().Value(buildContextSizeKey{}).(int64); ok && sz > 0 {
			req.ContentLength = sz
			req.TransferEncoding = nil
			req.Header.Del("Transfer-Encoding")
		}
	}
	return rt.base.RoundTrip(req)
}

func (e *Engine) ensureStreamingTransport() *client.Client {
	e.clientMu.Lock()
	defer e.clientMu.Unlock()

	if e.dockerClient == nil {
		return nil
	}
	httpClient := e.dockerClient.HTTPClient()
	if httpClient == nil {
		return e.dockerClient
	}
	if _, ok := httpClient.Transport.(*buildStreamRoundTripper); ok {
		return e.dockerClient
	}
	base := httpClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	httpClient.Transport = &buildStreamRoundTripper{base: base}
	origVersion := e.dockerClient.ClientVersion()
	opts := []client.Opt{
		client.WithHost(e.dockerClient.DaemonHost()),
		client.WithHTTPClient(httpClient),
		client.WithAPIVersionNegotiation(),
	}
	streamingCli, err := client.NewClientWithOpts(opts...)
	if err == nil {
		negotiateCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		streamingCli.NegotiateAPIVersion(negotiateCtx)
		cancel()

		// Protect against downgrade below Docker minimum for image prune (1.25)
		// if mock daemon didn't supply an API-Version header (which defaults to fallbackAPIVersion 1.24)
		if streamingCli.ClientVersion() == "1.24" && origVersion != "" && origVersion != "1.24" {
			_ = client.WithVersion(origVersion)(streamingCli)
		}
		e.dockerClient = streamingCli
	}
	return e.dockerClient
}

func (e *Engine) acquireBuildSlot(ctx context.Context) (func(), error) {
	if ctx.Value(buildSlotContextKey) != nil {
		return func() {}, nil
	}
	return e.getBuildSemaphore().Acquire(ctx)
}

// acquireBuildSlotWithInfo acquires a slot with rich owner diagnostics and queue wait progress callbacks.
func (e *Engine) acquireBuildSlotWithInfo(
	ctx context.Context,
	info SlotOwnerInfo,
	onWait func(pos int, waitDur time.Duration, actSlots []SlotOwnerInfo),
) (func(), error) {
	if ctx.Value(buildSlotContextKey) != nil {
		return func() {}, nil
	}
	return e.getBuildSemaphore().AcquireWithInfo(ctx, info, onWait)
}

type buildServiceNameKey struct{}

// WithBuildServiceName attaches the service name to the context for queue diagnostics.
func WithBuildServiceName(ctx context.Context, serviceName string) context.Context {
	return context.WithValue(ctx, buildServiceNameKey{}, serviceName)
}

// SetLocalBuildMode sets the local directory deployment mode ("direct" or "snapshot").
func (e *Engine) SetLocalBuildMode(mode string) {
	if mode != "" {
		e.localBuildMode = mode
	}
}

// resolveSourceDirectory acquires or resolves the source code directory for build operations.
func (e *Engine) resolveSourceDirectory(ctx context.Context, project *models.Project, execID string, emitLog func(phase, stream, message string), sourceRevision ...string) (buildSourceDir string, cleanupDir string, err error) {
	if project.SourceType == models.SourceTypeGitHub || project.SourceType == models.SourceTypeGitHubPublic {
		snapshotDir := filepath.Join(e.workDir, execID)
		_ = os.MkdirAll(snapshotDir, 0755)
		cleanupDir = snapshotDir
		buildSourceDir = snapshotDir

		// Check if an immutable repository snapshot is cached in source metadata
		if project.SourceID != nil && e.sourceService != nil {
			if src, err := e.sourceService.GetSourceByID(ctx, *project.SourceID); err == nil && src != nil && src.Metadata != nil {
				if snapVal, ok := src.Metadata["snapshot_path"].(string); ok && snapVal != "" {
					if info, err := os.Stat(snapVal); err == nil && info.IsDir() {
						emitLog(models.LogPhaseSource, models.LogStreamSystem, fmt.Sprintf("Reusing immutable repository snapshot from %s...", snapVal))
						if err := copyDirectory(snapVal, snapshotDir); err == nil {
							emitLog(models.LogPhaseSource, models.LogStreamSystem, "Immutable repository snapshot reused successfully.")
							return buildSourceDir, cleanupDir, nil
						}
						emitLog(models.LogPhaseSource, models.LogStreamSystem, "Notice: failed to copy immutable snapshot, falling back to archive acquisition")
					}
				}
			}
		}

		ref := project.Branch
		if len(sourceRevision) > 0 && sourceRevision[0] != "" {
			ref = sourceRevision[0]
		}
		if ref == "" {
			ref = "main"
		}

		parts := strings.Split(project.SourceReference, "/")
		if len(parts) != 2 {
			return "", cleanupDir, fmt.Errorf("invalid GitHub repository reference '%s'. Expected format 'owner/repo'", project.SourceReference)
		}
		if e.githubService == nil {
			return "", cleanupDir, errors.New("GitHub integration service is not available")
		}
		if len(ref) != 40 {
			sha, err := e.githubService.ResolveCommitSHA(ctx, project.OwnerID, parts[0], parts[1], ref)
			if err != nil {
				return "", cleanupDir, fmt.Errorf("failed to pin immutable commit SHA for GitHub revision '%s': %w", ref, err)
			}
			if len(sha) != 40 {
				return "", cleanupDir, fmt.Errorf("resolved GitHub revision '%s' is not an immutable 40-character commit SHA", sha)
			}
			ref = sha
		}
		emitLog(models.LogPhaseSource, models.LogStreamSystem, fmt.Sprintf("Acquiring GitHub repository archive for '%s' (revision: %s)...", project.SourceReference, ref))
		if err := e.githubService.AcquireRepoTarball(ctx, project.OwnerID, parts[0], parts[1], ref, snapshotDir); err != nil {
			return "", cleanupDir, fmt.Errorf("failed to acquire GitHub repository archive: %w", err)
		}
		emitLog(models.LogPhaseSource, models.LogStreamSystem, "GitHub repository archive acquired successfully.")
		return buildSourceDir, cleanupDir, nil

	} else if project.SourceType == models.SourceTypeLocalUpload || (project.SourceType == models.SourceTypeLocal && project.SourceReference != "") {
		sourceUUID, err := uuid.Parse(project.SourceReference)
		if err != nil {
			return "", "", fmt.Errorf("invalid local source upload ID '%s': %w", project.SourceReference, err)
		}
		if e.sourceService == nil {
			return "", "", errors.New("local source upload service is not available")
		}
		p, err := e.sourceService.GetSourcePath(ctx, project.OwnerID, sourceUUID)
		if err != nil {
			return "", "", fmt.Errorf("failed to locate uploaded source files: %w", err)
		}
		buildSourceDir = p
		emitLog(models.LogPhaseSource, models.LogStreamSystem, "Using isolated uploaded source workspace.")
		return buildSourceDir, "", nil

	} else if project.SourceType == models.SourceTypeLocalDirectory || project.RepositoryPath != "" {
		canonicalSource, err := e.pathValidator.ValidateSourcePath(project.RepositoryPath)
		if err != nil {
			return "", "", fmt.Errorf("source path validation failed: %w", err)
		}

		emitLog(models.LogPhaseSource, models.LogStreamSystem, fmt.Sprintf("Using local directory: %s", project.RepositoryPath))

		if e.localBuildMode != "direct" {
			emitLog(models.LogPhaseSource, models.LogStreamSystem, "Using snapshot filesystem build mode.")
			snapshotDir := filepath.Join(e.workDir, execID)
			_ = os.MkdirAll(snapshotDir, 0755)
			cleanupDir = snapshotDir
			if err := copyDirectory(canonicalSource, snapshotDir); err != nil {
				return "", cleanupDir, fmt.Errorf("failed to snapshot source files: %w", err)
			}
			buildSourceDir = snapshotDir
		} else {
			emitLog(models.LogPhaseSource, models.LogStreamSystem, "Using direct filesystem build mode.")
			buildSourceDir = canonicalSource
		}
		return buildSourceDir, cleanupDir, nil

	} else if project.SourceType == models.SourceTypeLocalAgent {
		emitLog(models.LogPhaseSource, models.LogStreamSystem, fmt.Sprintf("Connected to ForgeLAB local agent (session: %s).", project.SourceReference))
		return "", "", nil
	}

	return "", "", errors.New("no valid local directory, uploaded source, or repository configured")
}

func (e *Engine) buildImage(
	ctx context.Context,
	tarArchive io.ReadCloser,
	options types.ImageBuildOptions,
	onLogLine func(string),
) error {
	var currentArchive io.ReadCloser = tarArchive
	if currentArchive != nil {
		defer func() {
			_ = currentArchive.Close()
		}()
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	// 1. Acquire Docker build slot under configurable concurrency limit (BuildSemaphore) if not already held.
	// Acquired BEFORE the build-maintenance lock so that queued builds waiting for a build slot do not
	// unnecessarily block Docker image pruning or maintenance operations.
	if ctx.Value(buildSlotContextKey) == nil {
		if onLogLine != nil {
			onLogLine("Waiting for Docker build slot...")
		}
		svcName := "build_image"
		if sn, ok := ctx.Value(buildServiceNameKey{}).(string); ok && sn != "" {
			svcName = sn
		}
		slotInfo := SlotOwnerInfo{
			ID:          uuid.New().String(),
			ServiceName: svcName,
			Phase:       "waiting_for_build_slot",
		}
		rel, err := e.acquireBuildSlotWithInfo(ctx, slotInfo, func(pos int, waitDur time.Duration, actSlots []SlotOwnerInfo) {
			if onLogLine != nil {
				var ownerDesc string
				if len(actSlots) > 0 {
					ownerDesc = fmt.Sprintf(" (currently held by service '%s' in phase '%s')", actSlots[0].ServiceName, actSlots[0].Phase)
				}
				onLogLine(fmt.Sprintf("Queued for Docker build slot: position %d, elapsed %s%s", pos, waitDur.Round(time.Second), ownerDesc))
			}
		})
		if err != nil {
			return fmt.Errorf("Docker build concurrency limit wait cancelled: %w", err)
		}
		defer rel()
		if onLogLine != nil {
			onLogLine("Docker build slot acquired.")
		}
	}

	// 2. Acquire shared build-maintenance lock (read side) only once the build slot is held.
	// Prevents Docker image pruning from running while any ImageBuild is active.
	if !e.buildMaintenanceMu.TryRLock() {
		if onLogLine != nil {
			onLogLine("Docker daemon is currently performing maintenance/image pruning; waiting for maintenance lock...")
		}
		slog.Info("docker build waiting for build-maintenance access")
		e.buildMaintenanceMu.RLock()
	}
	defer e.buildMaintenanceMu.RUnlock()

	// 3. Stage and validate build context archive onto disk
	var validatedArchiveFile *os.File
	var stagedCleanup string

	if file, ok := currentArchive.(*os.File); ok {
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("failed to rewind build context file: %w", err)
		}
		if err := tararchive.ValidateTarArchive(file, options.Dockerfile); err != nil {
			return fmt.Errorf("Build context validation failed: %w", err)
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("failed to rewind build context file after validation: %w", err)
		}
		validatedArchiveFile = file
	} else {
		if onLogLine != nil {
			onLogLine("Receiving and validating build context archive...")
		}
		stagedFile, err := tararchive.StageAndValidateArchiveStream(ctx, e.workDir, currentArchive, options.Dockerfile)
		if err != nil {
			if tracker, ok := currentArchive.(TarStreamTracker); ok {
				if streamErr := tracker.StreamError(); streamErr != nil {
					return fmt.Errorf("Build context generation failed: %w", streamErr)
				}
			}
			return fmt.Errorf("Build context generation failed: %w", err)
		}
		validatedArchiveFile = stagedFile
		stagedCleanup = stagedFile.Name()
		defer func() {
			_ = stagedFile.Close()
			if stagedCleanup != "" {
				_ = os.Remove(stagedCleanup)
			}
		}()
	}

	// 4. Inspect Dockerfile and resolve builder capability
	var dfContent []byte
	dfContent, _ = extractDockerfileFromTar(validatedArchiveFile, options.Dockerfile)
	_, _ = validatedArchiveFile.Seek(0, io.SeekStart)

	requiresBuildKit, _ := DetectDockerfileRequiresBuildKit(dfContent)
	_, hasCachedVer := e.getCachedBuilderVersion()

	if requiresBuildKit || hasCachedVer || options.Version != "" || (len(dfContent) > 0 && e.dockerClient != nil) || options.Version == "" {
		resolvedOptions, builderErr := e.resolveBuilder(ctx, dfContent, options, nil)
		if builderErr != nil {
			return builderErr
		}
		options = resolvedOptions
	}

	buildTimeout := getDockerBuildTimeout()
	buildCtx, buildCancel := context.WithTimeout(ctx, buildTimeout)
	defer buildCancel()

	slog.Info("docker image build started", "tags", options.Tags, "builder_version", options.Version, "timeout", buildTimeout.String())

	if onLogLine != nil {
		onLogLine(fmt.Sprintf("Docker build started (timeout: %s).", buildTimeout.Round(time.Second)))
	}

	if _, err := validatedArchiveFile.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("failed to rewind build context file before Docker ImageBuild: %w", err)
	}

	stat, statErr := validatedArchiveFile.Stat()
	if statErr != nil {
		return fmt.Errorf("failed to stat build context file: %w", statErr)
	}
	validatedArchiveFilePath := validatedArchiveFile.Name()

	buildCli := e.ensureStreamingTransport()
	if buildCli == nil {
		buildCli = e.dockerClient
	}
	buildCtx = context.WithValue(buildCtx, buildContextSizeKey{}, stat.Size())

	buildResponse, err := buildCli.ImageBuild(buildCtx, validatedArchiveFile, options)
	if err != nil {
		if errors.Is(buildCtx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("Docker build timed out after %s: %w", buildTimeout.Round(time.Second), err)
		}
		if !requiresBuildKit && options.Version == types.BuilderBuildKit && (strings.Contains(strings.ToLower(err.Error()), "no active session") || strings.Contains(strings.ToLower(err.Error()), "no active sessions")) {
			slog.Warn("buildkit returned session error on ImageBuild; retrying with standard builder", "error", err)
			if onLogLine != nil {
				onLogLine("BuildKit session unavailable on Docker daemon. Retrying with standard Docker builder...")
			}
			fallbackFile, openErr := os.Open(validatedArchiveFilePath)
			if openErr == nil {
				defer fallbackFile.Close()
				fallbackOpts := options
				fallbackOpts.Version = types.BuilderV1
				retryCtx, retryCancel := context.WithTimeout(ctx, buildTimeout)
				defer retryCancel()
				retryCtx = context.WithValue(retryCtx, buildContextSizeKey{}, stat.Size())
				retryResp, retryErr := buildCli.ImageBuild(retryCtx, fallbackFile, fallbackOpts)
				if retryErr == nil {
					defer retryResp.Body.Close()
					if retryParseErr := e.parseDockerStream(retryResp.Body, onLogLine); retryParseErr == nil {
						slog.Info("docker image build completed using fallback standard builder", "tags", fallbackOpts.Tags)
						return nil
					} else {
						return fmt.Errorf("Docker build error (fallback stream): %w", retryParseErr)
					}
				}
			}
		}
		return fmt.Errorf("Docker build failed: %w", err)
	}
	defer buildResponse.Body.Close()

	if parseErr := e.parseDockerStream(buildResponse.Body, onLogLine); parseErr != nil {
		if os.Getenv("FORGELAB_PRESERVE_FAILED_CONTEXTS") == "true" || os.Getenv("FORGELAB_PRESERVE_FAILED_CONTEXTS") == "1" {
			diagDir := filepath.Join(e.workDir, "diagnostics", "failed-build-contexts")
			_ = os.MkdirAll(diagDir, 0755)
			diagPath := filepath.Join(diagDir, fmt.Sprintf("failed-build-%d.tar", time.Now().UnixNano()))
			if diagFile, oErr := os.Open(validatedArchiveFilePath); oErr == nil {
				if df, cErr := os.Create(diagPath); cErr == nil {
					_, _ = io.Copy(df, diagFile)
					_ = df.Close()
					slog.Info("preserved failed build context for diagnostics", "path", diagPath, "size", stat.Size())
				}
				_ = diagFile.Close()
			}
		}

		if errors.Is(buildCtx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("Docker build timed out after %s: %w", buildTimeout.Round(time.Second), parseErr)
		}

		// If BuildKit was used and failed with session error or rejected the tar header, attempt fallback
		isSessionErr := strings.Contains(strings.ToLower(parseErr.Error()), "no active session") || strings.Contains(strings.ToLower(parseErr.Error()), "no active sessions")
		isTarHeaderErr := strings.Contains(parseErr.Error(), "archive/tar: invalid tar header")
		if !requiresBuildKit && options.Version == types.BuilderBuildKit && (isSessionErr || isTarHeaderErr) {
			slog.Warn("buildkit rejected build context or session unavailable; retrying with standard builder", "error", parseErr)
			if onLogLine != nil {
				if isSessionErr {
					onLogLine("BuildKit session unavailable on Docker daemon. Retrying with standard Docker builder...")
				} else {
					onLogLine("BuildKit rejected tar build context header. Retrying with standard Docker builder...")
				}
			}
			fallbackFile, openErr := os.Open(validatedArchiveFilePath)
			if openErr == nil {
				defer fallbackFile.Close()
				fallbackOpts := options
				fallbackOpts.Version = types.BuilderV1
				retryCtx, retryCancel := context.WithTimeout(ctx, buildTimeout)
				defer retryCancel()
				retryCtx = context.WithValue(retryCtx, buildContextSizeKey{}, stat.Size())
				retryResp, retryErr := buildCli.ImageBuild(retryCtx, fallbackFile, fallbackOpts)
				if retryErr == nil {
					defer retryResp.Body.Close()
					if retryParseErr := e.parseDockerStream(retryResp.Body, onLogLine); retryParseErr == nil {
						slog.Info("docker image build completed using fallback standard builder", "tags", fallbackOpts.Tags)
						return nil
					} else {
						return fmt.Errorf("Docker build error (fallback stream): %w", retryParseErr)
					}
				} else {
					slog.Error("docker fallback ImageBuild failed", "error", retryErr)
					return fmt.Errorf("Docker build fallback ImageBuild failed: %w", retryErr)
				}
			} else {
				slog.Error("failed to reopen archive for fallback", "error", openErr)
			}
		}
		return fmt.Errorf("Docker build error: %w", parseErr)
	}

	slog.Info("docker image build completed", "tags", options.Tags)
	return nil
}

func (e *Engine) parseDockerStream(r io.Reader, onLogLine func(string)) error {
	const maxLineBytes = 64 * 1024
	buf := make([]byte, 4096)
	var rawBuffer bytes.Buffer

	for {
		n, err := r.Read(buf)
		if n > 0 {
			rawBuffer.Write(buf[:n])

			// Bound buffer size if no newline encountered to prevent runaway memory usage
			if rawBuffer.Len() > maxLineBytes*2 && !bytes.Contains(rawBuffer.Bytes(), []byte("\n")) {
				line := rawBuffer.String()[:maxLineBytes] + " ...[truncated log line]"
				rawBuffer.Reset()
				if onLogLine != nil {
					onLogLine(line)
				}
			}

			lines := strings.Split(rawBuffer.String(), "\n")
			// Keep incomplete line in buffer
			rawBuffer.Reset()
			rawBuffer.WriteString(lines[len(lines)-1])

			for i := 0; i < len(lines)-1; i++ {
				line := lines[i]
				if strings.TrimSpace(line) == "" {
					continue
				}

				// Bound line size before processing
				if len(line) > maxLineBytes {
					line = line[:maxLineBytes] + " ...[truncated log line]"
				}

				// Docker JSON output parsing
				var jsonMsg struct {
					Stream string `json:"stream"`
					Error  string `json:"error"`
				}
				if jsonErr := json.Unmarshal([]byte(line), &jsonMsg); jsonErr == nil {
					if jsonMsg.Error != "" {
						return errors.New(jsonMsg.Error)
					}
					if jsonMsg.Stream != "" && onLogLine != nil {
						onLogLine(strings.TrimSuffix(jsonMsg.Stream, "\n"))
					}
				} else if onLogLine != nil {
					onLogLine(line)
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}

	// Flush trailing incomplete line at EOF
	if rawBuffer.Len() > 0 {
		line := strings.TrimSpace(rawBuffer.String())
		rawBuffer.Reset()
		if len(line) > maxLineBytes {
			line = line[:maxLineBytes] + " ...[truncated log line]"
		}
		if line != "" {
			var jsonMsg struct {
				Stream string `json:"stream"`
				Error  string `json:"error"`
			}
			if jsonErr := json.Unmarshal([]byte(line), &jsonMsg); jsonErr == nil {
				if jsonMsg.Error != "" {
					return errors.New(jsonMsg.Error)
				}
				if jsonMsg.Stream != "" && onLogLine != nil {
					onLogLine(strings.TrimSuffix(jsonMsg.Stream, "\n"))
				}
			} else if onLogLine != nil {
				onLogLine(line)
			}
		}
	}
	return nil
}

func copyDirectory(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		targetPath := filepath.Join(dst, relPath)

		if info.IsDir() {
			return os.MkdirAll(targetPath, info.Mode())
		}

		if info.Mode()&os.ModeSymlink != 0 {
			linkTarget, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(linkTarget, targetPath)
		}

		srcFile, err := os.Open(path)
		if err != nil {
			return err
		}
		defer srcFile.Close()

		dstFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
		if err != nil {
			return err
		}
		defer dstFile.Close()

		_, err = io.Copy(dstFile, srcFile)
		return err
	})
}

// resolveAgentBaseURL dynamically finds the local agent URL, handling containerized backend setups
func resolveAgentBaseURL() string {
	return agent.ResolveBaseURL()
}

// buildServiceDeploymentTiers partitions service deployments into dependency execution tiers (DAG)
func buildServiceDeploymentTiers(svcDeploys []*models.ServiceDeployment) [][]*models.ServiceDeployment {
	nameToDeploy := make(map[string]*models.ServiceDeployment)
	for _, sd := range svcDeploys {
		nameToDeploy[sd.ServiceName] = sd
	}

	deployed := make(map[string]bool)
	remaining := make(map[string]*models.ServiceDeployment)
	for _, sd := range svcDeploys {
		remaining[sd.ServiceName] = sd
	}

	var tiers [][]*models.ServiceDeployment
	for len(remaining) > 0 {
		var currentTier []*models.ServiceDeployment
		for _, sd := range remaining {
			depsSatisfied := true
			for _, dep := range sd.DependsOn {
				if _, exists := nameToDeploy[dep]; exists && !deployed[dep] {
					depsSatisfied = false
					break
				}
			}
			if depsSatisfied {
				currentTier = append(currentTier, sd)
			}
		}

		if len(currentTier) == 0 {
			// Circular dependency or unresolvable: group all remaining into a final tier
			var fallback []*models.ServiceDeployment
			for _, sd := range remaining {
				fallback = append(fallback, sd)
			}
			tiers = append(tiers, fallback)
			break
		}

		for _, sd := range currentTier {
			deployed[sd.ServiceName] = true
			delete(remaining, sd.ServiceName)
		}
		tiers = append(tiers, currentTier)
	}
	return tiers
}
