package tunnel

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/forgelab/backend/internal/models"
)

var (
	tryCloudflareRegex = regexp.MustCompile(`https://[a-zA-Z0-9-]+\.trycloudflare\.com`)
	validHostnameRegex = regexp.MustCompile(`^[a-zA-Z0-9-]+\.trycloudflare\.com$`)

	ErrCloudflaredNotFound    = errors.New("cloudflared executable not found in PATH")
	ErrTunnelDiscoveryTimeout = errors.New("timed out waiting for cloudflared to assign a public quick tunnel URL")
	ErrInvalidTunnelURL       = errors.New("cloudflared returned an invalid or malformed public URL")
	ErrTunnelProcessExited    = errors.New("cloudflared tunnel process exited unexpectedly during startup")
)

const CloudflaredInstallGuide = `cloudflared is required for domainless public sharing but was not found in PATH.

Installation Instructions:
• Debian / Ubuntu / Linux Mint (Recommended .deb):
  curl -L --output cloudflared.deb https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-amd64.deb
  sudo dpkg -i cloudflared.deb
  rm cloudflared.deb

• Or via APT repository:
  sudo mkdir -p --mode=0755 /etc/apt/keyrings
  curl -fsSL https://pkg.cloudflare.com/cloudflare-main.gpg | sudo tee /etc/apt/keyrings/cloudflare-main.gpg >/dev/null
  echo "deb [signed-by=/etc/apt/keyrings/cloudflare-main.gpg] https://pkg.cloudflare.com/cloudflared any main" | sudo tee /etc/apt/sources.list.d/cloudflared.list
  sudo apt-get update && sudo apt-get install -y cloudflared

• macOS (Homebrew):
  brew install cloudflared

• Windows (winget):
  winget install --id Cloudflare.cloudflared

Documentation: https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/`

type managedTunnel struct {
	shareToken   string
	cmd          *exec.Cmd
	cancel       context.CancelFunc
	localServer  *LocalShareServer
	publicURL    string
	publicHost   string
	localPort    int
	state        string
	reason       string
	recentLogs   []string
	mu           sync.RWMutex
	done         chan struct{}
}

func (t *managedTunnel) addLog(line string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	// Bounded ring buffer: keep latest 60 lines
	if len(t.recentLogs) >= 60 {
		t.recentLogs = t.recentLogs[1:]
	}
	t.recentLogs = append(t.recentLogs, line)
}

func (t *managedTunnel) getStatus() (string, string) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.state, t.reason
}

func (t *managedTunnel) setStatus(state, reason string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.state = state
	t.reason = reason
}

// CloudflareProvider implements ShareProvider for Cloudflare Quick Tunnels.
type CloudflareProvider struct {
	cloudflaredPath string
	enabled         bool
	mu              sync.RWMutex
	tunnels         map[string]*managedTunnel
	stopped         bool
}

// NewCloudflareProvider creates a new Cloudflare Quick Tunnel provider.
func NewCloudflareProvider(enabled bool, cloudflaredPath string) *CloudflareProvider {
	if strings.TrimSpace(cloudflaredPath) == "" {
		cloudflaredPath = "cloudflared"
	}
	return &CloudflareProvider{
		cloudflaredPath: cloudflaredPath,
		enabled:         enabled,
		tunnels:         make(map[string]*managedTunnel),
	}
}

func (p *CloudflareProvider) Name() string {
	return models.ProviderCloudflare
}

func (p *CloudflareProvider) IsConfigured() bool {
	return p.enabled
}

// CheckPrerequisites verifies that cloudflared executable is available on the host system.
func (p *CloudflareProvider) CheckPrerequisites() (bool, string, error) {
	path, err := exec.LookPath(p.cloudflaredPath)
	if err != nil || path == "" {
		return false, CloudflaredInstallGuide, fmt.Errorf("%w: path %q", ErrCloudflaredNotFound, p.cloudflaredPath)
	}
	return true, "", nil
}

// StartShare starts a managed Cloudflare Quick Tunnel forwarding to the localHandler.
func (p *CloudflareProvider) StartShare(
	ctx context.Context,
	share *models.PublicShare,
	localHandler http.Handler,
) (*ShareEndpoint, error) {
	if !p.enabled {
		return nil, errors.New("public sharing is disabled")
	}

	// 1. Verify prerequisites
	ready, guide, err := p.CheckPrerequisites()
	if !ready {
		return nil, fmt.Errorf("%w:\n\n%s", err, guide)
	}

	// Prevent duplicate tunnel for same share token
	p.mu.Lock()
	if old, exists := p.tunnels[share.ShareToken]; exists && old != nil {
		p.mu.Unlock()
		_ = p.StopShare(ctx, share.ShareToken)
		p.mu.Lock()
	}
	p.mu.Unlock()

	// 2. Start local share server on 127.0.0.1:0
	localServer, err := NewLocalShareServer(localHandler)
	if err != nil {
		return nil, fmt.Errorf("failed to start local share router listener: %w", err)
	}

	// Verify local listener is responsive
	probeConn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", localServer.Port), 1*time.Second)
	if err != nil {
		_ = localServer.Close()
		return nil, fmt.Errorf("local share listener failed readiness probe: %w", err)
	}
	probeConn.Close()

	// 3. Prepare child process execution
	// Explicit arguments without shell interpolation
	tunnelURLArg := fmt.Sprintf("http://127.0.0.1:%d", localServer.Port)
	cmdArgs := []string{
		"tunnel",
		"--no-autoupdate",
		"--url", tunnelURLArg,
	}

	procCtx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(procCtx, p.cloudflaredPath, cmdArgs...)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		_ = localServer.Close()
		return nil, fmt.Errorf("failed to open stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		_ = localServer.Close()
		return nil, fmt.Errorf("failed to open stderr pipe: %w", err)
	}

	managed := &managedTunnel{
		shareToken:  share.ShareToken,
		cmd:         cmd,
		cancel:      cancel,
		localServer: localServer,
		localPort:   localServer.Port,
		state:       models.TunnelStateStartingTunnel,
		done:        make(chan struct{}),
	}

	if err := cmd.Start(); err != nil {
		cancel()
		_ = localServer.Close()
		return nil, fmt.Errorf("failed to spawn cloudflared process: %w", err)
	}

	// 4. Capture process output and discover generated trycloudflare.com URL
	urlFoundCh := make(chan string, 1)
	errCh := make(chan error, 2)

	var (
		scanErrMu sync.Mutex
		scanErr   error
	)

	var scanWg sync.WaitGroup
	scanWg.Add(2)

	pipeScanner := func(r io.Reader, isStderr bool) {
		defer scanWg.Done()
		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			line := scanner.Text()
			managed.addLog(line)

			// Detect configuration file conflict
			lineLower := strings.ToLower(line)
			if strings.Contains(lineLower, "cannot use --url with a configuration file") ||
				strings.Contains(lineLower, "you cannot use --url when a configuration file is present") ||
				(strings.Contains(lineLower, "config") && strings.Contains(lineLower, "conflict")) {
				scanErrMu.Lock()
				scanErr = errors.New("a pre-existing cloudflared configuration file in your user directory conflicts with quick tunnels. Please move or isolate ~/.cloudflared/config.yml")
				scanErrMu.Unlock()
			}

			// Check for generated trycloudflare URL
			if match := tryCloudflareRegex.FindString(line); match != "" {
				select {
				case urlFoundCh <- match:
				default:
				}
			}
		}
	}

	go pipeScanner(stdoutPipe, false)
	go pipeScanner(stderrPipe, true)

	// Background exit watcher
	go func() {
		waitErr := cmd.Wait()
		scanWg.Wait()
		close(managed.done)

		scanErrMu.Lock()
		finalErr := scanErr
		scanErrMu.Unlock()

		if finalErr == nil && waitErr != nil {
			finalErr = fmt.Errorf("%w: %v", ErrTunnelProcessExited, waitErr)
		} else if finalErr == nil {
			finalErr = errors.New("cloudflared process exited")
		}

		managed.mu.Lock()
		managed.state = models.TunnelStateDisconnected
		managed.reason = finalErr.Error()
		managed.mu.Unlock()

		_ = localServer.Close()

		select {
		case errCh <- finalErr:
		default:
		}
	}()

	// 5. Await discovery within bounded timeout (30 seconds)
	var discoveredURL string
	discoveryTimeout := 30 * time.Second

	select {
	case discoveredURL = <-urlFoundCh:
		// Succeeded in finding URL
	case err := <-errCh:
		cancel()
		_ = localServer.Close()
		return nil, fmt.Errorf("cloudflared failed during tunnel startup: %w", err)
	case <-time.After(discoveryTimeout):
		cancel()
		_ = localServer.Close()
		return nil, ErrTunnelDiscoveryTimeout
	case <-ctx.Done():
		cancel()
		_ = localServer.Close()
		return nil, ctx.Err()
	}

	// 6. Validate returned URL, scheme, and hostname
	parsed, err := url.Parse(discoveredURL)
	if err != nil || parsed.Scheme != "https" {
		cancel()
		_ = localServer.Close()
		return nil, fmt.Errorf("%w: URL scheme must be https, got %q", ErrInvalidTunnelURL, discoveredURL)
	}

	publicHost := strings.ToLower(parsed.Hostname())
	if !validHostnameRegex.MatchString(publicHost) {
		cancel()
		_ = localServer.Close()
		return nil, fmt.Errorf("%w: hostname %q does not match *.trycloudflare.com pattern", ErrInvalidTunnelURL, publicHost)
	}

	// 7. Mark online and register in manager
	managed.mu.Lock()
	managed.publicURL = discoveredURL
	managed.publicHost = publicHost
	managed.state = models.TunnelStateOnline
	managed.reason = ""
	managed.mu.Unlock()

	p.mu.Lock()
	p.tunnels[share.ShareToken] = managed
	p.mu.Unlock()

	slog.Info("cloudflare quick tunnel online",
		"share_token", share.ShareToken,
		"public_url", discoveredURL,
		"local_port", localServer.Port)

	return &ShareEndpoint{
		PublicURL:  discoveredURL,
		PublicHost: publicHost,
		LocalPort:  localServer.Port,
		State:      models.TunnelStateOnline,
	}, nil
}

// StopShare terminates the cloudflared process and closes the local server for a share.
func (p *CloudflareProvider) StopShare(ctx context.Context, shareToken string) error {
	p.mu.Lock()
	managed, exists := p.tunnels[shareToken]
	if exists {
		delete(p.tunnels, shareToken)
	}
	p.mu.Unlock()

	if !exists || managed == nil {
		return nil
	}

	// Cancel context to send SIGINT/SIGTERM
	if managed.cancel != nil {
		managed.cancel()
	}

	// Close local HTTP listener
	if managed.localServer != nil {
		_ = managed.localServer.Close()
	}

	// Wait up to 3 seconds for graceful process exit, then kill if still running
	select {
	case <-managed.done:
	case <-time.After(3 * time.Second):
		if managed.cmd != nil && managed.cmd.Process != nil {
			_ = managed.cmd.Process.Kill()
		}
	case <-ctx.Done():
		if managed.cmd != nil && managed.cmd.Process != nil {
			_ = managed.cmd.Process.Kill()
		}
	}

	managed.setStatus(models.TunnelStateRevoked, "share revoked or expired")
	return nil
}

// GetShareStatus returns current tunnel state and reason for a share token.
func (p *CloudflareProvider) GetShareStatus(ctx context.Context, shareToken string) (string, string, error) {
	p.mu.RLock()
	managed, exists := p.tunnels[shareToken]
	p.mu.RUnlock()

	if !exists || managed == nil {
		return models.TunnelStateDisconnected, "no active tunnel process found for share", nil
	}

	state, reason := managed.getStatus()
	return state, reason, nil
}

// Close gracefully stops all active tunnels and listeners.
func (p *CloudflareProvider) Close() error {
	p.mu.Lock()
	p.stopped = true
	allTunnels := make([]*managedTunnel, 0, len(p.tunnels))
	for _, t := range p.tunnels {
		allTunnels = append(allTunnels, t)
	}
	p.tunnels = make(map[string]*managedTunnel)
	p.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for _, t := range allTunnels {
		if t.cancel != nil {
			t.cancel()
		}
		if t.localServer != nil {
			_ = t.localServer.Close()
		}
		if t.cmd != nil && t.cmd.Process != nil {
			go func(cmd *exec.Cmd, done chan struct{}) {
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					_ = cmd.Process.Kill()
				}
			}(t.cmd, t.done)
		}
	}

	_ = ctx
	return nil
}
