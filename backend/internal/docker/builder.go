package docker

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
)

// DetectDockerfileRequiresBuildKit inspects Dockerfile contents and determines whether
// BuildKit is required by examining syntax directives, RUN mount options, link flags,
// heredocs, and other BuildKit-only features.
func DetectDockerfileRequiresBuildKit(content []byte) (bool, string) {
	if len(content) == 0 {
		return false, ""
	}

	var logicalLines []string
	var currentLine strings.Builder

	scanner := bufio.NewScanner(bytes.NewReader(content))
	for scanner.Scan() {
		raw := scanner.Text()
		trimmed := strings.TrimSpace(raw)

		// Syntax directives must be at the very top of the Dockerfile
		if len(logicalLines) == 0 && currentLine.Len() == 0 {
			if strings.HasPrefix(trimmed, "#") {
				lower := strings.ToLower(trimmed)
				if strings.HasPrefix(lower, "# syntax=") || strings.HasPrefix(lower, "#syntax=") {
					return true, fmt.Sprintf("syntax directive (%s)", trimmed)
				}
			}
		}

		if strings.HasPrefix(trimmed, "#") {
			// Skip comment lines
			continue
		}

		if trimmed == "" {
			continue
		}

		if strings.HasSuffix(trimmed, "\\") {
			currentLine.WriteString(strings.TrimSuffix(trimmed, "\\"))
			currentLine.WriteString(" ")
			continue
		}

		currentLine.WriteString(trimmed)
		if currentLine.Len() > 0 {
			logicalLines = append(logicalLines, currentLine.String())
			currentLine.Reset()
		}
	}
	if currentLine.Len() > 0 {
		logicalLines = append(logicalLines, currentLine.String())
	}

	for _, line := range logicalLines {
		upper := strings.ToUpper(line)

		// Check for heredoc syntax: <<EOF, <<-EOF, <<EOT, <<-EOT
		if strings.Contains(line, "<<EOF") || strings.Contains(line, "<<-EOF") ||
			strings.Contains(line, "<<EOT") || strings.Contains(line, "<<-EOT") {
			return true, "heredoc syntax"
		}

		// Check RUN instructions
		if strings.HasPrefix(upper, "RUN ") || strings.HasPrefix(upper, "RUN\t") {
			args := line[3:]
			if idx := strings.Index(args, "--mount="); idx != -1 {
				mountPart := args[idx:]
				// Extract the mount option token up to whitespace
				if spaceIdx := strings.IndexAny(mountPart, " \t\r\n"); spaceIdx != -1 {
					mountPart = mountPart[:spaceIdx]
				}
				return true, fmt.Sprintf("RUN %s", mountPart)
			}
			if strings.Contains(args, "--network=") {
				return true, "RUN --network="
			}
			if strings.Contains(args, "--security=") {
				return true, "RUN --security="
			}
		}

		// Check COPY instructions
		if strings.HasPrefix(upper, "COPY ") || strings.HasPrefix(upper, "COPY\t") {
			args := line[4:]
			if strings.Contains(args, "--link") {
				return true, "COPY --link"
			}
			if strings.Contains(args, "--chmod=") {
				return true, "COPY --chmod="
			}
		}

		// Check ADD instructions
		if strings.HasPrefix(upper, "ADD ") || strings.HasPrefix(upper, "ADD\t") {
			args := line[3:]
			if strings.Contains(args, "--link") {
				return true, "ADD --link"
			}
			if strings.Contains(args, "--chmod=") {
				return true, "ADD --chmod="
			}
			if strings.Contains(args, "--checksum=") {
				return true, "ADD --checksum="
			}
			if strings.Contains(args, "--keep-git-dir=") {
				return true, "ADD --keep-git-dir="
			}
		}
	}

	return false, ""
}


// extractDockerfileFromTar scans a tar reader to find and extract the Dockerfile content.
func extractDockerfileFromTar(r io.Reader, targetDockerfile string) ([]byte, error) {
	if r == nil {
		return nil, nil
	}
	cleanedTarget := filepath.Clean(strings.TrimSpace(targetDockerfile))
	if cleanedTarget == "" || cleanedTarget == "." {
		cleanedTarget = "Dockerfile"
	}
	cleanedTarget = strings.ReplaceAll(cleanedTarget, "\\", "/")
	cleanedTarget = strings.TrimPrefix(cleanedTarget, "./")
	baseTarget := filepath.Base(cleanedTarget)

	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		hdrName := strings.TrimPrefix(strings.ReplaceAll(hdr.Name, "\\", "/"), "./")
		isMatch := hdrName == cleanedTarget ||
			filepath.Base(hdrName) == baseTarget ||
			hdrName == "Dockerfile" ||
			hdrName == "Dockerfile.forgelab"

		if isMatch && (hdr.Typeflag == tar.TypeReg || hdr.Typeflag == tar.TypeRegA) {
			limit := hdr.Size
			if limit > 512*1024 {
				limit = 512 * 1024
			}
			content := make([]byte, limit)
			n, _ := io.ReadFull(tr, content)
			return content[:n], nil
		}
	}
	return nil, nil
}

// peekDockerfileFromTar reads and buffers the tar stream into an intact, unfragmented reader
// ensuring BuildKit and legacy builders receive complete, properly formatted tar archives without
// pipe chunking artifacts, while extracting the Dockerfile contents for capability detection.
func peekDockerfileFromTar(tarArchive io.ReadCloser, targetDockerfile string) (io.ReadCloser, []byte, error) {
	if tarArchive == nil {
		return nil, nil, nil
	}
	defer tarArchive.Close()

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, tarArchive); err != nil {
		return nil, nil, fmt.Errorf("failed buffering build context tar: %w", err)
	}

	rawBytes := buf.Bytes()
	dfContent, _ := extractDockerfileFromTar(bytes.NewReader(rawBytes), targetDockerfile)
	slog.Info("buffered tar context", "total_bytes", len(rawBytes), "df_bytes", len(dfContent))

	rc := io.NopCloser(bytes.NewReader(rawBytes))
	return rc, dfContent, nil
}

func (e *Engine) getCachedBuilderVersion() (types.BuilderVersion, bool) {
	val := e.cachedBuilderVer.Load()
	if val == nil {
		return "", false
	}
	v, ok := val.(types.BuilderVersion)
	return v, ok
}

func (e *Engine) setCachedBuilderVersion(v types.BuilderVersion) {
	e.cachedBuilderVer.Store(v)
}

func (e *Engine) detectDaemonBuilderCapability(ctx context.Context) (types.BuilderVersion, error) {
	if cached, ok := e.getCachedBuilderVersion(); ok && cached != "" {
		return cached, nil
	}
	if e.dockerClient == nil {
		return "", errors.New("docker client is nil")
	}
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ping, err := e.dockerClient.Ping(pingCtx)
	if err != nil {
		return "", err
	}
	e.setCachedBuilderVersion(ping.BuilderVersion)
	return ping.BuilderVersion, nil
}

func (e *Engine) resolveBuilder(
	ctx context.Context,
	dfContent []byte,
	options types.ImageBuildOptions,
	onLogLine func(string),
) (types.ImageBuildOptions, error) {
	daemonBuilderVer, pingErr := e.detectDaemonBuilderCapability(ctx)
	if pingErr == nil && onLogLine != nil {
		onLogLine("Docker daemon available")
		if daemonBuilderVer == types.BuilderBuildKit {
			onLogLine("Builder version: 2 / BuildKit")
		} else if daemonBuilderVer == types.BuilderV1 {
			onLogLine("Builder version: 1 / legacy")
		} else if daemonBuilderVer != "" {
			onLogLine(fmt.Sprintf("Builder version: %s", daemonBuilderVer))
		} else {
			onLogLine("Builder version: 1 / legacy")
		}
	}

	requiresBuildKit, featureReason := DetectDockerfileRequiresBuildKit(dfContent)

	if requiresBuildKit {
		// BuildKit is required by Dockerfile features (e.g. RUN --mount=type=cache)
		if daemonBuilderVer == types.BuilderV1 {
			errMsg := fmt.Sprintf("BuildKit is required by Dockerfile feature %s but the configured Docker daemon does not support BuildKit.", featureReason)
			if onLogLine != nil {
				onLogLine(errMsg)
			}
			return options, errors.New(errMsg)
		}
		options.Version = types.BuilderBuildKit
		if onLogLine != nil {
			onLogLine(fmt.Sprintf("Using BuildKit builder for Docker image build (required by: %s)", featureReason))
		}
		return options, nil
	}

	// Normal Dockerfile does not require BuildKit
	if options.Version != "" {
		if options.Version == types.BuilderBuildKit && daemonBuilderVer == types.BuilderV1 {
			errMsg := "BuildKit builder requested but the configured Docker daemon does not support BuildKit."
			if onLogLine != nil {
				onLogLine(errMsg)
			}
			return options, errors.New(errMsg)
		}
		if onLogLine != nil {
			if options.Version == types.BuilderBuildKit {
				onLogLine("Using BuildKit builder for Docker image build")
			} else {
				onLogLine("Using legacy builder for Docker image build")
			}
		}
		return options, nil
	}

	// Select builder based on daemon advertised capability.
	// Note: Docker Engine REST API does not accept plain tar build contexts when Version=BuilderBuildKit
	// without a BuildKit control session, returning "failed to read dockerfile: archive/tar: invalid tar header".
	// For standard Dockerfiles that do not require BuildKit-specific features, leave options.Version empty
	// (or BuilderV1 if legacy) so Docker Engine's standard builder natively consumes the TAR context.
	if daemonBuilderVer == types.BuilderV1 {
		options.Version = types.BuilderV1
		if onLogLine != nil {
			onLogLine("Using legacy builder for Docker image build")
		}
	} else {
		if onLogLine != nil {
			onLogLine("Using standard Docker builder for image build")
		}
	}

	return options, nil
}
