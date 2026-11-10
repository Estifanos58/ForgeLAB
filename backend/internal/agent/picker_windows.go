//go:build windows

package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	uoiFlags   = 1
	uoiName    = 2
	wsfVisible = 0x0001

	windowsPickerTitleEnvVar = "FORGELAB_PICKER_TITLE"
)

var (
	user32DLL                   = windows.NewLazySystemDLL("user32.dll")
	procGetProcessWindowStation = user32DLL.NewProc("GetProcessWindowStation")
	procGetUserObjectInfoW      = user32DLL.NewProc("GetUserObjectInformationW")
)

type userObjectFlags struct {
	Inherit  int32
	Reserved int32
	Flags    uint32
}

// InspectInteractiveSession checks whether the current Windows process is running
// in an interactive user desktop session with a visible window station (not Session 0 / service).
func InspectInteractiveSession() InteractiveSessionInfo {
	var sessionID uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &sessionID); err != nil {
		return InteractiveSessionInfo{
			Interactive: false,
			Reason:      fmt.Sprintf("failed to resolve Windows session ID: %v", err),
		}
	}

	hwinsta, _, err := procGetProcessWindowStation.Call()
	if hwinsta == 0 {
		return InteractiveSessionInfo{
			SessionID:   sessionID,
			Interactive: false,
			Reason:      fmt.Sprintf("no process window station attached (session %d): %v", sessionID, err),
		}
	}

	winstaName := queryWindowStationName(hwinsta)
	visible, flagsKnown := queryWindowStationVisible(hwinsta)

	if sessionID == 0 {
		return InteractiveSessionInfo{
			SessionID:            sessionID,
			Interactive:          false,
			WindowStation:        winstaName,
			WindowStationVisible: visible,
			Reason:               "process is running in Windows Session 0 (services/non-interactive session); launch the local agent in the interactive logged-in user desktop session",
		}
	}

	if flagsKnown && !visible {
		return InteractiveSessionInfo{
			SessionID:            sessionID,
			Interactive:          false,
			WindowStation:        winstaName,
			WindowStationVisible: false,
			Reason:               fmt.Sprintf("window station %q in session %d is not visible (WSF_VISIBLE unset)", winstaName, sessionID),
		}
	}

	return InteractiveSessionInfo{
		SessionID:            sessionID,
		Interactive:          true,
		WindowStation:        winstaName,
		WindowStationVisible: !flagsKnown || visible,
	}
}

func queryWindowStationVisible(hwinsta uintptr) (visible bool, ok bool) {
	var flags userObjectFlags
	var needed uint32
	r1, _, _ := procGetUserObjectInfoW.Call(
		hwinsta,
		uintptr(uoiFlags),
		uintptr(unsafe.Pointer(&flags)),
		uintptr(unsafe.Sizeof(flags)),
		uintptr(unsafe.Pointer(&needed)),
	)
	if r1 == 0 {
		return false, false
	}
	return (flags.Flags & wsfVisible) != 0, true
}

func queryWindowStationName(hwinsta uintptr) string {
	buf := make([]uint16, 128)
	var needed uint32
	r1, _, _ := procGetUserObjectInfoW.Call(
		hwinsta,
		uintptr(uoiName),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)*2),
		uintptr(unsafe.Pointer(&needed)),
	)
	if r1 == 0 {
		return ""
	}
	return windows.UTF16ToString(buf)
}

func resolveWindowsPowerShellPath() string {
	if systemRoot := strings.TrimSpace(os.Getenv("SystemRoot")); systemRoot != "" {
		candidate := filepath.Join(systemRoot, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	if p, err := exec.LookPath("powershell.exe"); err == nil {
		return p
	}
	return "powershell.exe"
}

func encodeUTF16LEBase64(script string) string {
	units := utf16.Encode([]rune(script))
	raw := make([]byte, len(units)*2)
	for i, u := range units {
		raw[i*2] = byte(u)
		raw[i*2+1] = byte(u >> 8)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

func buildWindowsFolderPickerScript() string {
	return `
$ProgressPreference = 'SilentlyContinue'
$ErrorActionPreference = 'Stop'

if ([System.Threading.Thread]::CurrentThread.GetApartmentState() -ne [System.Threading.ApartmentState]::STA) {
    [Console]::Error.WriteLine('FolderBrowserDialog requires an STA thread')
    exit 3
}

if (-not [Environment]::UserInteractive) {
    [Console]::Error.WriteLine('Windows session is not user-interactive')
    exit 4
}

Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
[System.Windows.Forms.Application]::EnableVisualStyles()

$title = $env:FORGELAB_PICKER_TITLE
if ([string]::IsNullOrWhiteSpace($title)) {
    $title = 'Select Project Folder for ForgeLAB'
}

$dialog = $null
$ownerForm = $null

try {
    $cursorPos = [System.Windows.Forms.Cursor]::Position
    $screenBounds = [System.Windows.Forms.Screen]::FromPoint($cursorPos).WorkingArea

    $ownerForm = New-Object System.Windows.Forms.Form
    $ownerForm.Text = 'ForgeLAB Folder Picker'
    $ownerForm.FormBorderStyle = [System.Windows.Forms.FormBorderStyle]::FixedToolWindow
    $ownerForm.StartPosition = [System.Windows.Forms.FormStartPosition]::Manual
    $ownerForm.ShowInTaskbar = $false
    $ownerForm.Opacity = 0
    $ownerForm.TopMost = $true
    $ownerForm.Size = New-Object System.Drawing.Size(1, 1)
    $ownerForm.Location = New-Object System.Drawing.Point(
        ($screenBounds.Left + [int]($screenBounds.Width / 2)),
        ($screenBounds.Top + [int]($screenBounds.Height / 2))
    )
    $ownerForm.Show()
    $ownerForm.Activate()
    $ownerForm.BringToFront()

    $dialog = New-Object System.Windows.Forms.FolderBrowserDialog
    $dialog.Description = $title
    $dialog.ShowNewFolderButton = $false

    $result = $dialog.ShowDialog($ownerForm)
    if ($result -eq [System.Windows.Forms.DialogResult]::OK -and -not [string]::IsNullOrWhiteSpace($dialog.SelectedPath)) {
        $bytes = [System.Text.Encoding]::UTF8.GetBytes($dialog.SelectedPath)
        $encodedPath = [Convert]::ToBase64String($bytes)
        [Console]::Out.WriteLine('` + windowsPickerOutputPrefix + `' + $encodedPath)
        exit 0
    }

    exit 2
} finally {
    if ($null -ne $dialog) {
        try { $dialog.Dispose() } catch {}
    }
    if ($null -ne $ownerForm) {
        try {
            $ownerForm.TopMost = $false
            $ownerForm.Close()
            $ownerForm.Dispose()
        } catch {}
    }
}
`
}

func runDefaultWindowsPicker(ctx context.Context, title string) (string, error) {
	psExe := resolveWindowsPowerShellPath()
	encodedCmd := encodeUTF16LEBase64(buildWindowsFolderPickerScript())

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
	cmd.Env = append(os.Environ(), windowsPickerTitleEnvVar+"="+title)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		if ctx.Err() != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return "", ErrPickerTimedOut
			}
			return "", ErrPickerCancelled
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			switch exitErr.ExitCode() {
			case 2:
				return "", ErrPickerCancelled
			case 3:
				return "", fmt.Errorf("Windows folder picker failed STA apartment check: %s", strings.TrimSpace(stderr.String()))
			case 4:
				return "", fmt.Errorf("%w: PowerShell reported [Environment]::UserInteractive = false", ErrPickerNotInteractive)
			default:
				errDetail := strings.TrimSpace(stderr.String())
				if errDetail == "" {
					errDetail = err.Error()
				}
				return "", fmt.Errorf("Windows folder picker exited with code %d: %s", exitErr.ExitCode(), errDetail)
			}
		}
		errDetail := strings.TrimSpace(stderr.String())
		if errDetail != "" {
			return "", fmt.Errorf("native folder picker failed: %w (stderr: %s)", err, errDetail)
		}
		return "", fmt.Errorf("native folder picker failed: %w", err)
	}

	return decodeWindowsPickerOutput(stdout.String())
}
