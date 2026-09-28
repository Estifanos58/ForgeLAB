package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

var ErrPickerCancelled = errors.New("folder selection was cancelled by user")

// NativeFolderPicker provides operating-system native folder picker dialogs
type NativeFolderPicker struct{}

func NewNativeFolderPicker() *NativeFolderPicker {
	return &NativeFolderPicker{}
}

// PickFolder launches the native OS directory chooser dialog and returns the selected path
func (p *NativeFolderPicker) PickFolder(ctx context.Context, title string) (string, error) {
	if title == "" {
		title = "Select Project Folder for ForgeLAB"
	}

	// 2-minute timeout for dialog interaction
	dialogCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	switch runtime.GOOS {
	case "windows":
		return p.pickFolderWindows(dialogCtx, title)
	case "darwin":
		return p.pickFolderDarwin(dialogCtx, title)
	default:
		return p.pickFolderLinux(dialogCtx, title)
	}
}

func (p *NativeFolderPicker) pickFolderWindows(ctx context.Context, title string) (string, error) {
	// PowerShell script to invoke System.Windows.Forms.FolderBrowserDialog with TopMost flag
	script := fmt.Sprintf(`
Add-Type -AssemblyName System.Windows.Forms
$dialog = New-Object System.Windows.Forms.FolderBrowserDialog
$dialog.Description = '%s'
$dialog.ShowNewFolderButton = $false
$dummyForm = New-Object System.Windows.Forms.Form
$dummyForm.TopMost = $true
$result = $dialog.ShowDialog($dummyForm)
if ($result -eq [System.Windows.Forms.DialogResult]::OK) {
    Write-Output $dialog.SelectedPath
} else {
    exit 2
}
`, strings.ReplaceAll(title, "'", "''"))

	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 2 {
			return "", ErrPickerCancelled
		}
		return "", fmt.Errorf("native folder picker failed: %v (stderr: %s)", err, stderr.String())
	}

	path := strings.TrimSpace(stdout.String())
	if path == "" {
		return "", ErrPickerCancelled
	}

	return path, nil
}

func (p *NativeFolderPicker) pickFolderDarwin(ctx context.Context, title string) (string, error) {
	script := fmt.Sprintf(`POSIX path of (choose folder with prompt "%s")`, strings.ReplaceAll(title, `"`, `\"`))
	cmd := exec.CommandContext(ctx, "osascript", "-e", script)
	out, err := cmd.Output()
	if err != nil {
		return "", ErrPickerCancelled
	}
	path := strings.TrimSpace(string(out))
	if path == "" {
		return "", ErrPickerCancelled
	}
	return path, nil
}

func (p *NativeFolderPicker) pickFolderLinux(ctx context.Context, title string) (string, error) {
	// Try zenity first
	if _, err := exec.LookPath("zenity"); err == nil {
		cmd := exec.CommandContext(ctx, "zenity", "--file-selection", "--directory", "--title="+title)
		out, err := cmd.Output()
		if err != nil {
			return "", ErrPickerCancelled
		}
		return strings.TrimSpace(string(out)), nil
	}

	// Fallback to kdialog
	if _, err := exec.LookPath("kdialog"); err == nil {
		cmd := exec.CommandContext(ctx, "kdialog", "--getexistingdirectory", "--title", title)
		out, err := cmd.Output()
		if err != nil {
			return "", ErrPickerCancelled
		}
		return strings.TrimSpace(string(out)), nil
	}

	return "", errors.New("no native GUI folder picker (zenity or kdialog) found on this Linux system")
}
