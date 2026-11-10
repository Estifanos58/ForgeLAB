package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

var (
	ErrPickerCancelled      = errors.New("folder selection was cancelled by user")
	ErrPickerBusy           = errors.New("folder picker is already in progress")
	ErrPickerTimedOut       = errors.New("folder selection timed out waiting for user input")
	ErrPickerNotInteractive = errors.New("native folder picker requires an interactive desktop session")
)

const (
	defaultPickerDialogTimeout = 2 * time.Minute
	windowsPickerOutputPrefix  = "FORGELAB_PATH_B64:"
)

// InteractiveSessionInfo describes whether the current agent process can display native desktop GUI dialogs.
type InteractiveSessionInfo struct {
	SessionID            uint32 `json:"session_id"`
	Interactive          bool   `json:"interactive"`
	WindowStation        string `json:"window_station,omitempty"`
	WindowStationVisible bool   `json:"window_station_visible"`
	Reason               string `json:"reason,omitempty"`
}

// NativeFolderPicker provides operating-system native folder picker dialogs.
type NativeFolderPicker struct {
	isPicking        sync.Mutex
	picking          bool
	dialogTimeout    time.Duration
	inspectSession   func() InteractiveSessionInfo
	pickFunc         func(ctx context.Context, title string) (string, error)
	runWindowsPicker func(ctx context.Context, title string) (string, error)
}

func NewNativeFolderPicker() *NativeFolderPicker {
	return &NativeFolderPicker{
		dialogTimeout:  defaultPickerDialogTimeout,
		inspectSession: InspectInteractiveSession,
	}
}

func (p *NativeFolderPicker) effectiveTimeout() time.Duration {
	if p.dialogTimeout <= 0 {
		return defaultPickerDialogTimeout
	}
	return p.dialogTimeout
}

// PickFolder launches the native OS directory chooser dialog and returns the selected path.
func (p *NativeFolderPicker) PickFolder(ctx context.Context, title string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	p.isPicking.Lock()
	if p.picking {
		p.isPicking.Unlock()
		return "", ErrPickerBusy
	}
	p.picking = true
	p.isPicking.Unlock()

	defer func() {
		p.isPicking.Lock()
		p.picking = false
		p.isPicking.Unlock()
	}()

	if err := classifyPickerContextErr(ctx, nil); err != nil {
		return "", err
	}

	if strings.TrimSpace(title) == "" {
		title = "Select Project Folder for ForgeLAB"
	}

	dialogCtx, cancel := context.WithTimeout(ctx, p.effectiveTimeout())
	defer cancel()

	if p.pickFunc != nil {
		selected, err := p.pickFunc(dialogCtx, title)
		if err != nil {
			if ctxErr := classifyPickerContextErr(ctx, dialogCtx); ctxErr != nil {
				return "", ctxErr
			}
			return "", err
		}
		return selected, nil
	}

	switch runtime.GOOS {
	case "windows":
		return p.pickFolderWindows(ctx, dialogCtx, title)
	case "darwin":
		return p.pickFolderDarwin(ctx, dialogCtx, title)
	default:
		return p.pickFolderLinux(ctx, dialogCtx, title)
	}
}

func classifyPickerContextErr(parentCtx, dialogCtx context.Context) error {
	if parentCtx != nil && parentCtx.Err() != nil {
		if errors.Is(parentCtx.Err(), context.DeadlineExceeded) {
			return ErrPickerTimedOut
		}
		return ErrPickerCancelled
	}
	if dialogCtx != nil && dialogCtx.Err() != nil {
		if errors.Is(dialogCtx.Err(), context.DeadlineExceeded) {
			return ErrPickerTimedOut
		}
		return ErrPickerCancelled
	}
	return nil
}

func (p *NativeFolderPicker) pickFolderWindows(parentCtx, dialogCtx context.Context, title string) (string, error) {
	if p.runWindowsPicker != nil {
		selected, err := p.runWindowsPicker(dialogCtx, title)
		if err != nil {
			if ctxErr := classifyPickerContextErr(parentCtx, dialogCtx); ctxErr != nil {
				return "", ctxErr
			}
			return "", err
		}
		return selected, nil
	}

	inspectFn := p.inspectSession
	if inspectFn == nil {
		inspectFn = InspectInteractiveSession
	}
	info := inspectFn()
	if !info.Interactive {
		if info.Reason != "" {
			return "", fmt.Errorf("%w: %s", ErrPickerNotInteractive, info.Reason)
		}
		return "", ErrPickerNotInteractive
	}

	selected, err := runDefaultWindowsPicker(dialogCtx, title)
	if err != nil {
		if ctxErr := classifyPickerContextErr(parentCtx, dialogCtx); ctxErr != nil {
			return "", ctxErr
		}
		return "", err
	}
	return selected, nil
}

// decodeWindowsPickerOutput extracts the UTF-8 selected path from PowerShell stdout.
// Real invocations emit `FORGELAB_PATH_B64:<utf8-base64>` to avoid OEM/ANSI console codepage corruption.
func decodeWindowsPickerOutput(rawStdout string) (string, error) {
	normalized := strings.ReplaceAll(rawStdout, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, windowsPickerOutputPrefix) {
			encoded := strings.TrimPrefix(trimmed, windowsPickerOutputPrefix)
			decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
			if err != nil {
				return "", fmt.Errorf("failed to decode selected folder path from Windows picker: %w", err)
			}
			path := strings.TrimRight(string(decoded), "\r\n\x00")
			if strings.TrimSpace(path) == "" {
				return "", ErrPickerCancelled
			}
			return path, nil
		}
	}

	fallback := strings.TrimSpace(rawStdout)
	if fallback == "" {
		return "", ErrPickerCancelled
	}
	return fallback, nil
}

func (p *NativeFolderPicker) pickFolderDarwin(parentCtx, dialogCtx context.Context, title string) (string, error) {
	script := fmt.Sprintf(`POSIX path of (choose folder with prompt "%s")`, strings.ReplaceAll(title, `"`, `\"`))
	cmd := exec.CommandContext(dialogCtx, "osascript", "-e", script)
	cmd.WaitDelay = 2 * time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if ctxErr := classifyPickerContextErr(parentCtx, dialogCtx); ctxErr != nil {
			return "", ctxErr
		}
		if strings.Contains(stderr.String(), "User canceled") || strings.Contains(err.Error(), "exit status 1") {
			return "", ErrPickerCancelled
		}
		return "", fmt.Errorf("native folder picker failed: %v (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	path := strings.TrimSpace(string(out))
	if path == "" {
		return "", ErrPickerCancelled
	}
	return path, nil
}

func (p *NativeFolderPicker) pickFolderLinux(parentCtx, dialogCtx context.Context, title string) (string, error) {
	// Try zenity first
	if _, err := exec.LookPath("zenity"); err == nil {
		cmd := exec.CommandContext(dialogCtx, "zenity", "--file-selection", "--directory", "--title="+title)
		cmd.WaitDelay = 2 * time.Second
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			if ctxErr := classifyPickerContextErr(parentCtx, dialogCtx); ctxErr != nil {
				return "", ctxErr
			}
			if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
				return "", ErrPickerCancelled
			}
			return "", fmt.Errorf("zenity folder picker failed: %v (stderr: %s)", err, strings.TrimSpace(stderr.String()))
		}
		res := strings.TrimSpace(string(out))
		if res == "" {
			return "", ErrPickerCancelled
		}
		return res, nil
	}

	// Fallback to kdialog
	if _, err := exec.LookPath("kdialog"); err == nil {
		cmd := exec.CommandContext(dialogCtx, "kdialog", "--getexistingdirectory", "--title", title)
		cmd.WaitDelay = 2 * time.Second
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			if ctxErr := classifyPickerContextErr(parentCtx, dialogCtx); ctxErr != nil {
				return "", ctxErr
			}
			if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
				return "", ErrPickerCancelled
			}
			return "", fmt.Errorf("kdialog folder picker failed: %v (stderr: %s)", err, strings.TrimSpace(stderr.String()))
		}
		res := strings.TrimSpace(string(out))
		if res == "" {
			return "", ErrPickerCancelled
		}
		return res, nil
	}

	return "", errors.New("no native GUI folder picker (zenity or kdialog) found on this Linux system")
}
