package docker

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/moby/moby/api/types/build"
	"github.com/moby/moby/client"
)

const (
	MaxDockerFileSize       = 2 * 1024 * 1024 // 2MB maximum Dockerfile size
	MaxDockerFileLineLength = 1024 * 1024     // 1MB maximum individual line length
)

var (
	ErrBuildSecurityInsecureForbidden = errors.New("insecure BuildKit entitlement (RUN --security=insecure) is strictly forbidden for build execution isolation")
	ErrBuildHostNetworkForbidden      = errors.New("host networking entitlement (RUN --network=host) is strictly forbidden for build execution isolation")
	ErrDockerfileOversized            = errors.New("dockerfile exceeds maximum allowed size of 2MB")
	ErrDockerfileInvalidSyntax        = errors.New("dockerfile contains invalid syntax or malformed directives")
	ErrDockerfileEmpty                = errors.New("dockerfile content is empty")
)

// DockerfileDirectives stores parsed parser directives from the top of the Dockerfile.
type DockerfileDirectives struct {
	Escape rune   // '\\' (default) or '`'
	Syntax string // e.g. "docker/dockerfile:1.4"
	Check  string // e.g. "skip=all"
}

// parseDockerfileLogicalLines parses a Dockerfile into logical instructions while respecting
// official parser directives (# escape, # syntax), line continuations (both \ and `),
// comments between continuations, and strict input limits (fails closed on oversized input or parse errors).
func parseDockerfileLogicalLines(content []byte) ([]string, DockerfileDirectives, error) {
	directives := DockerfileDirectives{
		Escape: '\\',
	}

	if len(content) == 0 || len(bytes.TrimSpace(content)) == 0 {
		return nil, directives, ErrDockerfileEmpty
	}

	if len(content) > MaxDockerFileSize {
		return nil, directives, fmt.Errorf("%w: size %d exceeds limit of %d bytes", ErrDockerfileOversized, len(content), MaxDockerFileSize)
	}

	if bytes.ContainsRune(content, 0) {
		return nil, directives, fmt.Errorf("%w: dockerfile contains null bytes", ErrDockerfileInvalidSyntax)
	}

	scanner := bufio.NewScanner(bytes.NewReader(content))
	scanner.Buffer(make([]byte, 64*1024), MaxDockerFileLineLength)

	var logicalLines []string
	var currentLine strings.Builder
	inDirectiveBlock := true
	seenDirectives := make(map[string]bool)

	for scanner.Scan() {
		raw := scanner.Text()
		trimmed := strings.TrimSpace(raw)

		// 1. Check parser directives at the top of the file
		if inDirectiveBlock {
			if trimmed == "" {
				// Empty line ends parser directive block per Dockerfile specification
				inDirectiveBlock = false
				continue
			}
			if strings.HasPrefix(trimmed, "#") {
				commentBody := strings.TrimSpace(strings.TrimPrefix(trimmed, "#"))
				// Check for directive format: key=value
				if eqIdx := strings.Index(commentBody, "="); eqIdx != -1 {
					key := strings.ToLower(strings.TrimSpace(commentBody[:eqIdx]))
					val := strings.TrimSpace(commentBody[eqIdx+1:])

					switch key {
					case "escape":
						if seenDirectives["escape"] {
							return nil, directives, fmt.Errorf("%w: duplicate parser directive %q", ErrDockerfileInvalidSyntax, key)
						}
						seenDirectives["escape"] = true
						if val == `\` {
							directives.Escape = '\\'
						} else if val == "`" {
							directives.Escape = '`'
						} else {
							return nil, directives, fmt.Errorf("%w: invalid escape directive %q (must be \\ or `)", ErrDockerfileInvalidSyntax, val)
						}
						continue
					case "syntax":
						if seenDirectives["syntax"] {
							return nil, directives, fmt.Errorf("%w: duplicate parser directive %q", ErrDockerfileInvalidSyntax, key)
						}
						seenDirectives["syntax"] = true
						directives.Syntax = val
						continue
					case "check":
						if seenDirectives["check"] {
							return nil, directives, fmt.Errorf("%w: duplicate parser directive %q", ErrDockerfileInvalidSyntax, key)
						}
						seenDirectives["check"] = true
						directives.Check = val
						continue
					}
				}
				// Any comment that is not a recognized parser directive ends directive parsing
				inDirectiveBlock = false
				continue
			}
			// Any non-comment line ends parser directive block
			inDirectiveBlock = false
		}

		// 2. Handle comments and empty lines
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			// In BuildKit, comment lines between continuation lines are ignored and do not terminate continuation
			continue
		}

		// 3. Handle line continuations with the active escape character
		// Check if trimmedRight ends with an unescaped escape character.
		trimmedRight := strings.TrimRight(raw, " \t\r")
		continuationChar := directives.Escape

		// Count consecutive escape characters at the end of trimmedRight
		numEscapes := 0
		runes := []rune(trimmedRight)
		for i := len(runes) - 1; i >= 0; i-- {
			if runes[i] == continuationChar {
				numEscapes++
			} else {
				break
			}
		}

		isContinuation := (numEscapes%2 == 1)

		if isContinuation {
			// Strip the trailing continuation character and trailing spaces
			lineContent := string(runes[:len(runes)-1])
			lineContent = strings.TrimSpace(lineContent)
			if currentLine.Len() > 0 {
				currentLine.WriteString(" ")
			}
			currentLine.WriteString(lineContent)
			continue
		}

		// Not a continuation line
		if currentLine.Len() > 0 {
			currentLine.WriteString(" ")
		}
		currentLine.WriteString(trimmed)

		logicalLines = append(logicalLines, currentLine.String())
		currentLine.Reset()
	}

	if err := scanner.Err(); err != nil {
		return nil, directives, fmt.Errorf("%w: line reading error: %v", ErrDockerfileInvalidSyntax, err)
	}

	if currentLine.Len() > 0 {
		logicalLines = append(logicalLines, currentLine.String())
	}

	return logicalLines, directives, nil
}

func extractFlagValue(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "=") {
		s = strings.TrimPrefix(s, "=")
		s = strings.TrimSpace(s)
	}
	if s == "" {
		return ""
	}

	// Check if quoted
	if s[0] == '"' || s[0] == '\'' {
		quote := s[0]
		endQuote := strings.IndexByte(s[1:], quote)
		if endQuote != -1 {
			return s[1 : 1+endQuote]
		}
		// Unterminated quote
		return s[1:]
	}

	// Unquoted: token ends at whitespace
	if spaceIdx := strings.IndexAny(s, " \t\r\n"); spaceIdx != -1 {
		return s[:spaceIdx]
	}
	return s
}

func checkSecurityFlags(args string) error {
	lower := strings.ToLower(args)
	searchFrom := 0
	target := "--security"

	for {
		idx := strings.Index(lower[searchFrom:], target)
		if idx == -1 {
			break
		}
		actualIdx := searchFrom + idx
		// Ensure it's a flag boundary (start of args or preceded by whitespace)
		if actualIdx > 0 {
			prev := rune(args[actualIdx-1])
			if prev != ' ' && prev != '\t' && prev != '\r' && prev != '\n' {
				searchFrom = actualIdx + len(target)
				continue
			}
		}

		after := args[actualIdx+len(target):]
		val := extractFlagValue(after)
		cleanVal := strings.ToLower(strings.Trim(val, `"' `))

		if cleanVal == "insecure" {
			return ErrBuildSecurityInsecureForbidden
		}
		if cleanVal != "" && cleanVal != "sandbox" {
			return fmt.Errorf("%w: requested security mode %q", ErrBuildSecurityInsecureForbidden, cleanVal)
		}

		searchFrom = actualIdx + len(target)
	}
	return nil
}

func checkNetworkFlags(args string) error {
	lower := strings.ToLower(args)
	searchFrom := 0
	target := "--network"

	for {
		idx := strings.Index(lower[searchFrom:], target)
		if idx == -1 {
			break
		}
		actualIdx := searchFrom + idx
		// Ensure it's a flag boundary (start of args or preceded by whitespace)
		if actualIdx > 0 {
			prev := rune(args[actualIdx-1])
			if prev != ' ' && prev != '\t' && prev != '\r' && prev != '\n' {
				searchFrom = actualIdx + len(target)
				continue
			}
		}

		after := args[actualIdx+len(target):]
		val := extractFlagValue(after)
		cleanVal := strings.ToLower(strings.Trim(val, `"' `))

		if cleanVal == "host" || strings.HasPrefix(cleanVal, "container:") || cleanVal == "container" {
			return fmt.Errorf("%w: requested network mode %q", ErrBuildHostNetworkForbidden, cleanVal)
		}

		searchFrom = actualIdx + len(target)
	}
	return nil
}

func validateInstructionSecurity(line string) error {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return nil
	}

	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return nil
	}
	inst := strings.ToUpper(fields[0])
	if inst != "RUN" {
		return nil
	}

	runArgs := trimmed[len(fields[0]):]

	// 1. Check for --security flags
	if err := checkSecurityFlags(runArgs); err != nil {
		return err
	}

	// 2. Check for --network flags
	if err := checkNetworkFlags(runArgs); err != nil {
		return err
	}

	return nil
}

// ValidateDockerfileSecurity inspects Dockerfile instructions for dangerous build directives or entitlements
// that could break out of container isolation, access the host network, or request unconfined host capabilities.
// It fails closed on parsing errors, oversized input, or dangerous entitlements.
func ValidateDockerfileSecurity(content []byte) error {
	logicalLines, _, err := parseDockerfileLogicalLines(content)
	if err != nil {
		return err
	}

	for _, line := range logicalLines {
		if err := validateInstructionSecurity(line); err != nil {
			return err
		}
	}
	return nil
}

// DetectDockerfileRequiresBuildKit inspects Dockerfile contents and determines whether
// BuildKit is required by examining syntax directives, RUN mount options, link flags,
// heredocs, and other BuildKit-only features.
func DetectDockerfileRequiresBuildKit(content []byte) (bool, string) {
	if len(content) == 0 {
		return false, ""
	}

	logicalLines, directives, err := parseDockerfileLogicalLines(content)
	if err != nil {
		return false, ""
	}

	if directives.Syntax != "" {
		return true, fmt.Sprintf("syntax directive (%s)", directives.Syntax)
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
// It fails closed if the Dockerfile is missing, oversized, or if reading fails.
func extractDockerfileFromTar(r io.Reader, targetDockerfile string) ([]byte, error) {
	if r == nil {
		return nil, errors.New("tar reader is nil")
	}
	cleanedTarget := filepath.Clean(strings.TrimSpace(targetDockerfile))
	if cleanedTarget == "" || cleanedTarget == "." {
		cleanedTarget = "Dockerfile"
	}
	cleanedTarget = strings.ReplaceAll(cleanedTarget, "\\", "/")
	cleanedTarget = strings.TrimPrefix(cleanedTarget, "./")

	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("error reading tar archive: %w", err)
		}
		hdrName := strings.TrimPrefix(strings.ReplaceAll(hdr.Name, "\\", "/"), "./")
		isMatch := hdrName == cleanedTarget || (cleanedTarget == "Dockerfile" && hdrName == "Dockerfile.forgelab")

		if isMatch && (hdr.Typeflag == tar.TypeReg || hdr.Typeflag == tar.TypeRegA) {
			if hdr.Size > MaxDockerFileSize {
				return nil, fmt.Errorf("%w: dockerfile size %d exceeds limit of %d bytes", ErrDockerfileOversized, hdr.Size, MaxDockerFileSize)
			}
			content, err := io.ReadAll(io.LimitReader(tr, MaxDockerFileSize+1))
			if err != nil {
				return nil, fmt.Errorf("failed to read dockerfile from tar: %w", err)
			}
			if int64(len(content)) > MaxDockerFileSize {
				return nil, fmt.Errorf("%w: dockerfile size %d exceeds limit of %d bytes", ErrDockerfileOversized, len(content), MaxDockerFileSize)
			}
			return content, nil
		}
	}
	return nil, fmt.Errorf("dockerfile %q not found in tar archive", cleanedTarget)
}

type autoCleaningFile struct {
	*os.File
}

func (f autoCleaningFile) Close() error {
	name := f.File.Name()
	err := f.File.Close()
	_ = os.Remove(name)
	return err
}

// peekDockerfileFromTar inspects the tar stream into an intact, unfragmented reader
// ensuring BuildKit and legacy builders receive complete, properly formatted tar archives without
// pipe chunking artifacts, while extracting the Dockerfile contents for capability detection.
// It avoids unbounded RAM buffering by using seekable files on disk.
func peekDockerfileFromTar(tarArchive io.ReadCloser, targetDockerfile string) (io.ReadCloser, []byte, error) {
	if tarArchive == nil {
		return nil, nil, nil
	}

	if file, ok := tarArchive.(*os.File); ok {
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return nil, nil, fmt.Errorf("failed rewinding tar archive file: %w", err)
		}
		dfContent, err := extractDockerfileFromTar(file, targetDockerfile)
		if err != nil {
			return nil, nil, fmt.Errorf("failed extracting dockerfile from tar file: %w", err)
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return nil, nil, fmt.Errorf("failed rewinding tar archive file after peek: %w", err)
		}
		return file, dfContent, nil
	}

	defer tarArchive.Close()

	tmp, err := os.CreateTemp("", "forgelab-tar-peek-*.tar")
	if err != nil {
		return nil, nil, fmt.Errorf("failed creating temporary file for tar peek: %w", err)
	}

	if _, err := io.Copy(tmp, tarArchive); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return nil, nil, fmt.Errorf("failed staging build context tar to disk: %w", err)
	}

	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return nil, nil, fmt.Errorf("failed rewinding staged tar file: %w", err)
	}

	dfContent, err := extractDockerfileFromTar(tmp, targetDockerfile)
	if err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return nil, nil, fmt.Errorf("failed extracting dockerfile from staged tar: %w", err)
	}

	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return nil, nil, fmt.Errorf("failed rewinding staged tar file after peek: %w", err)
	}

	return autoCleaningFile{tmp}, dfContent, nil
}

func (e *Engine) getCachedBuilderVersion() (build.BuilderVersion, bool) {
	val := e.cachedBuilderVer.Load()
	if val == nil {
		return "", false
	}
	v, ok := val.(build.BuilderVersion)
	return v, ok
}

func (e *Engine) setCachedBuilderVersion(v build.BuilderVersion) {
	e.cachedBuilderVer.Store(v)
}

func (e *Engine) detectDaemonBuilderCapability(ctx context.Context) (build.BuilderVersion, error) {
	if cached, ok := e.getCachedBuilderVersion(); ok && cached != "" {
		return cached, nil
	}
	if e.dockerClient == nil {
		return "", errors.New("docker client is nil")
	}
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ping, err := e.dockerClient.Ping(pingCtx, client.PingOptions{})
	if err != nil {
		return "", err
	}
	e.setCachedBuilderVersion(ping.BuilderVersion)
	return ping.BuilderVersion, nil
}

func (e *Engine) resolveBuilder(
	ctx context.Context,
	dfContent []byte,
	options client.ImageBuildOptions,
	onLogLine func(string),
) (client.ImageBuildOptions, error) {
	if err := ValidateDockerfileSecurity(dfContent); err != nil {
		if onLogLine != nil {
			onLogLine(fmt.Sprintf("Security violation in Dockerfile: %v", err))
		}
		return options, err
	}

	daemonBuilderVer, pingErr := e.detectDaemonBuilderCapability(ctx)
	if pingErr == nil && onLogLine != nil {
		onLogLine("Docker daemon available")
		if daemonBuilderVer == build.BuilderBuildKit {
			onLogLine("Builder version: 2 / BuildKit")
		} else if daemonBuilderVer == build.BuilderV1 {
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
		if daemonBuilderVer == build.BuilderV1 {
			errMsg := fmt.Sprintf("BuildKit is required by Dockerfile feature %s but the configured Docker daemon does not support BuildKit.", featureReason)
			if onLogLine != nil {
				onLogLine(errMsg)
			}
			return options, errors.New(errMsg)
		}
		options.Version = build.BuilderBuildKit
		if onLogLine != nil {
			onLogLine(fmt.Sprintf("Using BuildKit builder for Docker image build (required by: %s)", featureReason))
		}
		return options, nil
	}

	// Normal Dockerfile does not require BuildKit
	if options.Version != "" {
		if options.Version == build.BuilderBuildKit && daemonBuilderVer == build.BuilderV1 {
			errMsg := "BuildKit builder requested but the configured Docker daemon does not support BuildKit."
			if onLogLine != nil {
				onLogLine(errMsg)
			}
			return options, errors.New(errMsg)
		}
		if onLogLine != nil {
			if options.Version == build.BuilderBuildKit {
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
	if daemonBuilderVer == build.BuilderV1 {
		options.Version = build.BuilderV1
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
