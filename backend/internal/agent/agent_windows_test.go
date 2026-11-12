//go:build windows

package agent

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unicode/utf16"
	"unsafe"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/discovery"
	"github.com/forgelab/backend/internal/security"
	"github.com/forgelab/backend/internal/tararchive"
)

func TestWindows_InteractiveSessionInspection(t *testing.T) {
	info := InspectInteractiveSession()

	if info.SessionID == 0 {
		assert.False(t, info.Interactive, "Session 0 must never be marked interactive")
		assert.NotEmpty(t, info.Reason)
	} else if info.WindowStationVisible {
		assert.True(t, info.Interactive, "non-zero session with visible window station should be interactive")
	}

	// Verify NativeFolderPicker fails fast with ErrPickerNotInteractive in Session 0 / hidden window station
	picker := &NativeFolderPicker{
		dialogTimeout: 2 * time.Second,
		inspectSession: func() InteractiveSessionInfo {
			return InteractiveSessionInfo{
				SessionID:            0,
				Interactive:          false,
				WindowStation:        "Service-0x0-3e7$",
				WindowStationVisible: false,
				Reason:               "running inside Windows Session 0 service",
			}
		},
	}

	start := time.Now()
	_, err := picker.PickFolder(context.Background(), "Select Folder")
	assert.ErrorIs(t, err, ErrPickerNotInteractive)
	assert.Less(t, time.Since(start), 500*time.Millisecond, "non-interactive check must reject immediately without spawning PowerShell")
}

func TestWindows_PickerOutputDecoding_SpacesAndUnicode(t *testing.T) {
	cases := []struct {
		name      string
		stdout    string
		want      string
		wantErr   error
		expectErr bool
	}{
		{
			name:   "spaces in path",
			stdout: windowsPickerOutputPrefix + encodeBase64String(`C:\Users\Test User\My ForgeLAB Projects\app v1`) + "\r\n",
			want:   `C:\Users\Test User\My ForgeLAB Projects\app v1`,
		},
		{
			name:   "unicode accents CJK and emoji",
			stdout: windowsPickerOutputPrefix + encodeBase64String(`C:\Users\estif\Projet Été\ForgeLAB 测试 🚀\My App`) + "\r\n",
			want:   `C:\Users\estif\Projet Été\ForgeLAB 测试 🚀\My App`,
		},
		{
			name:    "empty stdout maps to cancelled",
			stdout:  "  \r\n ",
			wantErr: ErrPickerCancelled,
		},
		{
			name:      "invalid base64 fails loudly",
			stdout:    windowsPickerOutputPrefix + "not-valid-base64!!!\r\n",
			expectErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeWindowsPickerOutput(tc.stdout)
			if tc.wantErr != nil {
				assert.ErrorIs(t, err, tc.wantErr)
				return
			}
			if tc.expectErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func encodeBase64String(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

func TestWindows_PowerShellSubprocess_STA_Ownership_Disposal_And_Unicode(t *testing.T) {
	tempRoot := t.TempDir()
	targetDir := filepath.Join(tempRoot, "ForgeLAB Test — 项目 🚀", "My App")
	require.NoError(t, os.MkdirAll(targetDir, 0755))

	specialTitle := `Select "Project Folder" — 测试 'App' $env:TEMP`

	// Verify real Windows PowerShell 5.1 subprocess in -STA mode constructs WinForms owner + FolderBrowserDialog,
	// receives the title via env var without interpolation bugs, emits clean UTF-8 base64 stdout with zero stderr noise,
	// and disposes UI objects cleanly (without blocking on a human modal click).
	psScript := `
$ProgressPreference = 'SilentlyContinue'
$ErrorActionPreference = 'Stop'

if ([System.Threading.Thread]::CurrentThread.GetApartmentState() -ne [System.Threading.ApartmentState]::STA) {
    [Console]::Error.WriteLine('Not running in STA apartment')
    exit 3
}

Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
[System.Windows.Forms.Application]::EnableVisualStyles()

$dialog = $null
$ownerForm = $null
try {
    $ownerForm = New-Object System.Windows.Forms.Form
    $ownerForm.FormBorderStyle = [System.Windows.Forms.FormBorderStyle]::FixedToolWindow
    $ownerForm.StartPosition = [System.Windows.Forms.FormStartPosition]::Manual
    $ownerForm.ShowInTaskbar = $false
    $ownerForm.Opacity = 0
    $ownerForm.TopMost = $true
    $ownerForm.Size = New-Object System.Drawing.Size(1, 1)

    $dialog = New-Object System.Windows.Forms.FolderBrowserDialog
    $dialog.Description = $env:FORGELAB_PICKER_TITLE
    $dialog.ShowNewFolderButton = $false
    $dialog.SelectedPath = $env:FORGELAB_TEST_TARGET_DIR

    if ($dialog.Description -ne $env:FORGELAB_PICKER_TITLE) {
        [Console]::Error.WriteLine('Description mismatch')
        exit 5
    }

    $bytes = [System.Text.Encoding]::UTF8.GetBytes($dialog.SelectedPath)
    $encodedPath = [Convert]::ToBase64String($bytes)
    [Console]::Out.WriteLine('` + windowsPickerOutputPrefix + `' + $encodedPath)
} finally {
    if ($null -ne $dialog) { $dialog.Dispose() }
    if ($null -ne $ownerForm) { $ownerForm.Dispose() }
}
`

	psExe := resolveWindowsPowerShellPath()
	encodedCmd := encodeUTF16LEBase64(psScript)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	cmd := exec.CommandContext(
		ctx,
		psExe,
		"-NoLogo",
		"-NoProfile",
		"-NonInteractive",
		"-OutputFormat", "Text",
		"-STA",
		"-EncodedCommand", encodedCmd,
	)
	cmd.Stdin = nil
	cmd.WaitDelay = 2 * time.Second
	cmd.Env = append(os.Environ(),
		windowsPickerTitleEnvVar+"="+specialTitle,
		"FORGELAB_TEST_TARGET_DIR="+targetDir,
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	require.NoError(t, cmd.Run(), "PowerShell STA WinForms verification failed; ctxErr=%v stdout=%s stderr=%s", ctx.Err(), stdout.String(), stderr.String())
	assert.Empty(t, strings.TrimSpace(stderr.String()), "expected zero CLIXML/progress stderr noise")

	decoded, err := decodeWindowsPickerOutput(stdout.String())
	require.NoError(t, err)
	assert.Equal(t, targetDir, decoded)
}

func TestWindows_PowerShellSubprocess_CancellationAndTimeout(t *testing.T) {
	psExe := resolveWindowsPowerShellPath()
	sleepCmd := encodeUTF16LEBase64(`$ProgressPreference = 'SilentlyContinue'; Start-Sleep -Seconds 30`)

	t.Run("parent cancellation kills PowerShell subprocess cleanly", func(t *testing.T) {
		picker := &NativeFolderPicker{
			dialogTimeout: 10 * time.Second,
			runWindowsPicker: func(ctx context.Context, title string) (string, error) {
				cmd := exec.CommandContext(
					ctx,
					psExe,
					"-NoLogo", "-NoProfile", "-NonInteractive", "-OutputFormat", "Text", "-STA",
					"-EncodedCommand", sleepCmd,
				)
				cmd.Stdin = nil
				cmd.WaitDelay = 2 * time.Second
				return "", cmd.Run()
			},
		}

		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(150 * time.Millisecond)
			cancel()
		}()

		start := time.Now()
		_, err := picker.PickFolder(ctx, "Cancel Test")
		assert.ErrorIs(t, err, ErrPickerCancelled)
		assert.Less(t, time.Since(start), 10*time.Second)

		picker.isPicking.Lock()
		picking := picker.picking
		picker.isPicking.Unlock()
		assert.False(t, picking)
	})

	t.Run("dialog timeout kills PowerShell subprocess and returns ErrPickerTimedOut", func(t *testing.T) {
		picker := &NativeFolderPicker{
			dialogTimeout: 250 * time.Millisecond,
			runWindowsPicker: func(ctx context.Context, title string) (string, error) {
				cmd := exec.CommandContext(
					ctx,
					psExe,
					"-NoLogo", "-NoProfile", "-NonInteractive", "-OutputFormat", "Text", "-STA",
					"-EncodedCommand", sleepCmd,
				)
				cmd.Stdin = nil
				cmd.WaitDelay = 2 * time.Second
				return "", cmd.Run()
			},
		}

		start := time.Now()
		_, err := picker.PickFolder(context.Background(), "Timeout Test")
		assert.ErrorIs(t, err, ErrPickerTimedOut)
		assert.NotErrorIs(t, err, ErrPickerCancelled)
		assert.Less(t, time.Since(start), 10*time.Second)

		picker.isPicking.Lock()
		picking := picker.picking
		picker.isPicking.Unlock()
		assert.False(t, picking)
	})
}

type syncOutputBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncOutputBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncOutputBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestWindows_AgentStartupAndPortConflict(t *testing.T) {
	backendRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)

	binDir, err := os.MkdirTemp(backendRoot, ".test_win_agent_*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(binDir) })

	agentExe := filepath.Join(binDir, "forgelab-agent.exe")
	buildCmd := exec.Command("go", "build", "-o", agentExe, "./cmd/agent")
	buildCmd.Dir = backendRoot
	buildOut, bErr := buildCmd.CombinedOutput()
	require.NoError(t, bErr, "failed to compile forgelab-agent.exe: %s", string(buildOut))

	t.Run("announces ready only after bind and serves status with unreachable docker", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		cmd := exec.CommandContext(ctx, agentExe, "-host", "127.0.0.1", "-port", "0", "-agent-id", "win-startup-test")
		cmd.Env = append(os.Environ(), "DOCKER_HOST=tcp://127.0.0.1:1")
		var out syncOutputBuffer
		cmd.Stdout = &out
		cmd.Stderr = &out

		if err := cmd.Start(); err != nil {
			if strings.Contains(err.Error(), "Application Control") || strings.Contains(err.Error(), "Access is denied") {
				t.Skipf("skipping compiled binary execution due to Windows policy: %v", err)
			}
			require.NoError(t, err)
		}
		defer func() {
			cancel()
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
		}()

		readyRegex := regexp.MustCompile(`Ready on (http://127\.0\.0\.1:\d+)`)
		var baseURL string
		require.Eventually(t, func() bool {
			m := readyRegex.FindStringSubmatch(out.String())
			if len(m) == 2 {
				baseURL = m[1]
				return true
			}
			return false
		}, 15*time.Second, 50*time.Millisecond, "agent failed to log bound Ready URL; output=%s", out.String())

		resp, err := http.Get(baseURL + "/api/agent/status")
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var status map[string]interface{}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&status))
		assert.Equal(t, "online", status["status"])
		assert.Contains(t, []string{"checking", "unavailable"}, status["docker_status"])
	})

	t.Run("port conflict exits non-zero without printing Ready banner", func(t *testing.T) {
		occupied, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer occupied.Close()
		occupiedPort := occupied.Addr().(*net.TCPAddr).Port

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		cmd := exec.CommandContext(ctx, agentExe, "-host", "127.0.0.1", "-port", fmt.Sprintf("%d", occupiedPort))
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		err = cmd.Run()
		if err != nil && (strings.Contains(err.Error(), "Application Control") || strings.Contains(err.Error(), "Access is denied")) {
			t.Skipf("skipping compiled binary execution due to Windows policy: %v", err)
		}
		require.Error(t, err, "expected non-zero exit when port %d is already bound", occupiedPort)
		assert.NotContains(t, stdout.String(), "Ready on", "must NOT announce Ready when bind fails")
		assert.Contains(t, strings.ToLower(stderr.String()), "already in use")
	})
}

func windowsShortPath(t *testing.T, longPath string) (string, bool) {
	t.Helper()
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	proc := kernel32.NewProc("GetShortPathNameW")
	inUTF16, err := syscall.UTF16PtrFromString(longPath)
	if err != nil {
		return "", false
	}
	n, _, _ := proc.Call(uintptr(unsafe.Pointer(inUTF16)), 0, 0)
	if n == 0 {
		return "", false
	}
	buf := make([]uint16, n)
	r, _, _ := proc.Call(uintptr(unsafe.Pointer(inUTF16)), uintptr(unsafe.Pointer(&buf[0])), n)
	if r == 0 || r >= n {
		return "", false
	}
	short := string(utf16.Decode(buf[:r]))
	return short, !strings.EqualFold(filepath.Clean(short), filepath.Clean(longPath))
}

func TestWindows_ShortPathCanonicalization_And_EnvironmentDiscovery(t *testing.T) {
	tempRoot := t.TempDir()
	longRoot := filepath.Join(tempRoot, "ForgeLAB Runner Long Workspace Directory")
	svcDir := filepath.Join(longRoot, "frontend")
	require.NoError(t, os.MkdirAll(svcDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(longRoot, ".env"), []byte("ROOT_KEY=rootval\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(svcDir, ".env"), []byte("SVC_KEY=svcval\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(svcDir, "package.json"), []byte(`{"name":"fe"}`), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(svcDir, "index.js"), []byte("console.log('win ok');\n"), 0644))

	shortRoot, has83 := windowsShortPath(t, longRoot)
	if !has83 {
		// Fall back to mixed case + redundant separators if NTFS 8.3 names are disabled on this volume
		shortRoot = strings.ToLower(longRoot) + string(filepath.Separator)
	}

	// 1. PathValidator with 8.3 short allowedRoot validates long path (and vice versa)
	validatorFromShort := NewPathValidator([]string{shortRoot})
	gotLong, err := validatorFromShort.ValidateSourcePath(longRoot)
	require.NoError(t, err)
	expectedCanon, err := filepath.EvalSymlinks(longRoot)
	require.NoError(t, err)
	assert.Equal(t, filepath.Clean(expectedCanon), gotLong)

	validatorFromLong := NewPathValidator([]string{longRoot})
	gotFromShort, err := validatorFromLong.ValidateSourcePath(shortRoot)
	require.NoError(t, err)
	assert.Equal(t, filepath.Clean(expectedCanon), gotFromShort)

	// 2. Agent session registered with 8.3 short CanonicalPath serves /environment and /stream-context without 403/empty services
	token := "win-short-path-token"
	sourceID := uuid.New()
	agentSrv := NewAgentServer(AgentServerConfig{
		Port: 4142,
		SessionValidator: func(tok, agentID string) (*AgentSession, error) {
			if tok != token {
				return nil, ErrUnauthorized
			}
			return &AgentSession{
				Token:    tok,
				AgentID:  agentID,
				SourceID: &sourceID,
			}, nil
		},
	})
	defer agentSrv.Close()

	RegisterLocalSourceSession(&LocalSourceSession{
		SourceID:      sourceID,
		CanonicalPath: shortRoot,
		FolderName:    "win-short-repo",
		Status:        "ready",
		CreatedAt:     time.Now(),
		ExpiresAt:     time.Now().Add(1 * time.Hour),
		Token:         token,
	})
	t.Cleanup(func() { DeleteLocalSourceSession(sourceID) })

	ts := httptest.NewServer(agentSrv.Router())
	defer ts.Close()

	// Check /environment
	envReq, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/api/agent/sources/%s/environment?service_path=frontend", ts.URL, sourceID), nil)
	envReq.Header.Set("Authorization", "Bearer "+token)
	envResp, err := http.DefaultClient.Do(envReq)
	require.NoError(t, err)
	defer envResp.Body.Close()
	require.Equal(t, http.StatusOK, envResp.StatusCode)

	var envBody SourceEnvironmentResponse
	require.NoError(t, json.NewDecoder(envResp.Body).Decode(&envBody))
	assert.Equal(t, "rootval", envBody.Root["ROOT_KEY"])
	require.Contains(t, envBody.Services, "frontend")
	assert.Equal(t, "svcval", envBody.Services["frontend"]["SVC_KEY"])

	// Check /stream-context
	streamReq, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/api/agent/sources/%s/stream-context?service_path=frontend", ts.URL, sourceID), nil)
	streamReq.Header.Set("Authorization", "Bearer "+token)
	streamResp, err := http.DefaultClient.Do(streamReq)
	require.NoError(t, err)
	defer streamResp.Body.Close()
	require.Equal(t, http.StatusOK, streamResp.StatusCode)

	tr := tar.NewReader(streamResp.Body)
	var foundIndex bool
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		if hdr.Name == "index.js" {
			foundIndex = true
		}
		assert.False(t, strings.Contains(hdr.Name, ".env"), ".env secret leaked in tar: %s", hdr.Name)
	}
	assert.True(t, foundIndex)
}

func TestWindows_RestrictedPrefixBoundaries_UNC_And_JunctionEscape(t *testing.T) {
	tempRoot := t.TempDir()

	// Directory named windows_app inside allowed root must NOT trigger false-positive c:\windows restriction
	winPrefixSafe := filepath.Join(tempRoot, "windows_app")
	require.NoError(t, os.MkdirAll(winPrefixSafe, 0755))

	validator := NewPathValidator(nil)
	validated, err := validator.ValidateSourcePath(winPrefixSafe)
	require.NoError(t, err)
	assert.NotEmpty(t, validated)

	// UNC network share must be rejected
	_, err = validator.ValidateSourcePath(`\\127.0.0.1\C$\Users`)
	assert.ErrorIs(t, err, ErrRestrictedSystemPath)

	// Actual C:\Windows system path must remain rejected
	if _, statErr := os.Stat(`C:\Windows`); statErr == nil {
		_, err = validator.ValidateSourcePath(`C:\Windows\System32`)
		assert.ErrorIs(t, err, ErrRestrictedSystemPath)
	}

	// Real NTFS directory junction (no SeCreateSymbolicLinkPrivilege required) pointing outside build context
	outsideSecretDir := filepath.Join(tempRoot, "outside_secret")
	svcDir := filepath.Join(tempRoot, "service")
	require.NoError(t, os.MkdirAll(outsideSecretDir, 0755))
	require.NoError(t, os.MkdirAll(svcDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(outsideSecretDir, "secret.txt"), []byte("do-not-leak"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(svcDir, "Dockerfile"), []byte("FROM alpine:3.21\n"), 0644))

	junctionPath := filepath.Join(svcDir, "escape_junction")
	cmd := exec.Command("cmd", "/c", "mklink", "/J", junctionPath, outsideSecretDir)
	if out, mkErr := cmd.CombinedOutput(); mkErr != nil {
		t.Skipf("mklink /J unavailable: %v (%s)", mkErr, string(out))
	}

	// TAR build context creation MUST reject the escaping NTFS junction
	_, _, tarErr := tararchive.WriteBuildContext(context.Background(), io.Discard, tararchive.BuildContextOptions{
		BuildContextDir: svcDir,
	})
	require.Error(t, tarErr)
	assert.True(t, errors.Is(tarErr, tararchive.ErrSymlinkEscape), "expected ErrSymlinkEscape for escaping NTFS junction, got: %v", tarErr)

	// security.PathValidator MUST also reject an NTFS junction inside allowedRoot pointing outside
	secValidator := security.NewPathValidator([]string{svcDir})
	_, secErr := secValidator.ValidateSourcePath(junctionPath)
	require.Error(t, secErr)
	assert.ErrorIs(t, secErr, security.ErrPathNotAllowed)

	// discovery content fingerprinting MUST reject the escaping NTFS junction
	_, discErr := discovery.ComputeDirectoryContentFingerprintWithContext(context.Background(), svcDir)
	require.Error(t, discErr, "expected fingerprinting to reject escaping NTFS junction")

	// Valid internal NTFS junction inside build context must be archived cleanly as a portable relative symlink
	internalSvcDir := filepath.Join(tempRoot, "internal_service")
	internalTargetDir := filepath.Join(internalSvcDir, "shared_pkg")
	require.NoError(t, os.MkdirAll(internalTargetDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(internalSvcDir, "Dockerfile"), []byte("FROM alpine:3.21\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(internalTargetDir, "util.txt"), []byte("hello"), 0644))

	internalJunction := filepath.Join(internalSvcDir, "linked_pkg")
	cmdInternal := exec.Command("cmd", "/c", "mklink", "/J", internalJunction, internalTargetDir)
	if out, mkErr := cmdInternal.CombinedOutput(); mkErr != nil {
		t.Skipf("mklink /J unavailable for internal junction: %v (%s)", mkErr, string(out))
	}

	var tarBuf bytes.Buffer
	_, _, tarInternalErr := tararchive.WriteBuildContext(context.Background(), &tarBuf, tararchive.BuildContextOptions{
		BuildContextDir: internalSvcDir,
	})
	require.NoError(t, tarInternalErr)

	tr := tar.NewReader(&tarBuf)
	var foundJunctionLink bool
	for {
		hdr, rErr := tr.Next()
		if rErr == io.EOF {
			break
		}
		require.NoError(t, rErr)
		if hdr.Name == "linked_pkg" {
			foundJunctionLink = true
			assert.Equal(t, byte(tar.TypeSymlink), hdr.Typeflag)
			assert.Equal(t, "shared_pkg", hdr.Linkname)
		}
	}
	assert.True(t, foundJunctionLink, "valid internal NTFS junction should be archived as portable tar.TypeSymlink")
}
