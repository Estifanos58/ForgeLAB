package tunnel

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/forgelab/backend/internal/models"
)

// ShareEndpoint contains the public URL and local connectivity info for an active public share.
type ShareEndpoint struct {
	PublicURL  string `json:"public_url"`
	PublicHost string `json:"public_host"`
	LocalPort  int    `json:"local_port"`
	State      string `json:"state"`
}

// ShareProvider abstracts public tunnel implementations (Cloudflare Quick Tunnels, Custom Gateway, etc.).
type ShareProvider interface {
	// Name returns the provider identifier ("cloudflare" or "gateway").
	Name() string

	// IsConfigured returns true if the provider has all required configuration.
	IsConfigured() bool

	// CheckPrerequisites verifies that required local binaries and tools are installed and ready.
	// Returns true if ready, or false with an actionable explanation/guide.
	CheckPrerequisites() (ready bool, guide string, err error)

	// StartShare starts a public tunnel endpoint forwarding traffic to the localHandler.
	StartShare(ctx context.Context, share *models.PublicShare, localHandler http.Handler) (*ShareEndpoint, error)

	// StopShare stops the tunnel and cleans up all associated resources for the share token.
	StopShare(ctx context.Context, shareToken string) error

	// GetShareStatus returns the current lifecycle state and failure reason (if any) for a share.
	GetShareStatus(ctx context.Context, shareToken string) (state string, reason string, err error)

	// Close terminates all active tunnels and listeners managed by the provider.
	Close() error
}

// LocalShareServer manages a local loopback HTTP server forwarding to a share-scoped router.
type LocalShareServer struct {
	Listener net.Listener
	Server   *http.Server
	Port     int
}

// NewLocalShareServer creates and starts an HTTP server bound exclusively to a free loopback port on 127.0.0.1.
func NewLocalShareServer(handler http.Handler) (*LocalShareServer, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("failed to bind local loopback listener: %w", err)
	}

	port := listener.Addr().(*net.TCPAddr).Port

	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		_ = server.Serve(listener)
	}()

	return &LocalShareServer{
		Listener: listener,
		Server:   server,
		Port:     port,
	}, nil
}

// Close gracefully stops the local share HTTP server and releases the listener.
func (s *LocalShareServer) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if s.Server != nil {
		_ = s.Server.Shutdown(ctx)
	}
	if s.Listener != nil {
		_ = s.Listener.Close()
	}
	return nil
}
