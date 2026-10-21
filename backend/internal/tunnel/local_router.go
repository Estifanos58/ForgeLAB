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
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/forgelab/backend/internal/models"
)

// UpstreamResolver discovers reachable local addresses for running services.
type UpstreamResolver interface {
	ResolveUpstream(ctx context.Context, service *models.Service) (string, error)
}

// ShareLookup provides read-only share inspection for the local router.
type ShareLookup interface {
	GetShareByToken(ctx context.Context, shareToken string) (*models.PublicShare, error)
}

// ServiceLookup provides read-only service inspection.
type ServiceLookup interface {
	GetService(ctx context.Context, serviceID uuid.UUID) (*models.Service, error)
}

// LocalRouter routes decrypted tunnel streams from the gateway to authorized project services.
type LocalRouter struct {
	shareLookup      ShareLookup
	serviceLookup    ServiceLookup
	upstreamResolver UpstreamResolver
	httpClient       *http.Client
}

// NewLocalRouter creates a new LocalRouter.
func NewLocalRouter(
	shareLookup ShareLookup,
	serviceLookup ServiceLookup,
	upstreamResolver UpstreamResolver,
) *LocalRouter {
	return &LocalRouter{
		shareLookup:      shareLookup,
		serviceLookup:    serviceLookup,
		upstreamResolver: upstreamResolver,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
			Transport: &http.Transport{
				Proxy: http.ProxyFromEnvironment,
				DialContext: (&net.Dialer{
					Timeout:   5 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext,
				MaxIdleConns:          100,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   5 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
				ResponseHeaderTimeout: 30 * time.Second,
			},
		},
	}
}

// HandleStream processes an incoming Yamux stream containing an HTTP request or WebSocket upgrade.
func (r *LocalRouter) HandleStream(stream net.Conn) {
	defer stream.Close()

	reader := bufio.NewReader(stream)
	req, err := http.ReadRequest(reader)
	if err != nil {
		if !errors.Is(err, io.EOF) {
			slog.Warn("local router failed to read request from tunnel stream", "error", err)
		}
		return
	}

	// 1. Identify and authenticate the public share token
	shareToken := req.Header.Get(HeaderForgeLabShareToken)
	if shareToken == "" {
		// Fallback: extract from Host header (e.g. share-123456.preview.domain.com)
		hostPart := req.Host
		if colon := strings.Index(hostPart, ":"); colon != -1 {
			hostPart = hostPart[:colon]
		}
		parts := strings.Split(hostPart, ".")
		if len(parts) > 0 {
			shareToken = parts[0]
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	share, err := r.shareLookup.GetShareByToken(ctx, shareToken)
	if err != nil || share == nil || !share.IsActive() {
		r.writeErrorResponse(stream, http.StatusNotFound, "Share Not Found", "The requested public share link does not exist, has been revoked, or has expired.")
		return
	}

	// 2. Normalize and validate request path
	if strings.Contains(req.RequestURI, "..") || strings.Contains(req.URL.Path, "..") || strings.Contains(req.URL.RawPath, "..") {
		r.writeErrorResponse(stream, http.StatusBadRequest, "Invalid Path", "Path traversal is strictly prohibited.")
		return
	}
	rawPath := req.URL.Path
	if rawPath == "" {
		rawPath = "/"
	}
	cleanPath := path.Clean(rawPath)
	if strings.Contains(cleanPath, "..") {
		r.writeErrorResponse(stream, http.StatusBadRequest, "Invalid Path", "Path traversal is strictly prohibited.")
		return
	}

	// 3. Match route mapping (longest prefix first)
	mappings := make([]models.RouteMapping, len(share.RouteMappings))
	copy(mappings, share.RouteMappings)
	sort.Slice(mappings, func(i, j int) bool {
		return len(mappings[i].PathPrefix) > len(mappings[j].PathPrefix)
	})

	var matchedMapping *models.RouteMapping
	for _, m := range mappings {
		pfx := m.PathPrefix
		if pfx == "/" || cleanPath == pfx || strings.HasPrefix(cleanPath, pfx+"/") {
			matchedMapping = &m
			break
		}
	}

	if matchedMapping == nil {
		// Fallback to primary service
		matchedMapping = &models.RouteMapping{
			PathPrefix: "/",
			ServiceID:  share.PrimaryServiceID,
		}
	}

	// 4. Validate target service existence and running status
	targetService, err := r.serviceLookup.GetService(ctx, matchedMapping.ServiceID)
	if err != nil || targetService == nil {
		r.writeErrorResponse(stream, http.StatusBadGateway, "Service Unavailable", "The target service mapped to this route could not be found.")
		return
	}

	if targetService.ProjectID != share.ProjectID {
		slog.Error("security boundary violation: route mapped service does not belong to share project",
			"share_id", share.ID, "service_id", targetService.ID, "service_project_id", targetService.ProjectID)
		r.writeErrorResponse(stream, http.StatusForbidden, "Access Denied", "Security boundary violation.")
		return
	}

	if targetService.Status != models.ServiceStatusRunning {
		r.writeErrorResponse(stream, http.StatusServiceUnavailable, "Service Stopped",
			fmt.Sprintf("Service '%s' is currently %s. Please start the service in ForgeLAB.", targetService.Name, targetService.Status))
		return
	}

	// 5. Discover reachable upstream address
	upstreamBase, err := r.upstreamResolver.ResolveUpstream(ctx, targetService)
	if err != nil || upstreamBase == "" {
		slog.Warn("local router failed to resolve upstream for service", "service", targetService.Name, "error", err)
		r.writeErrorResponse(stream, http.StatusBadGateway, "Upstream Unreachable",
			fmt.Sprintf("Could not connect to service '%s' on local container/host port.", targetService.Name))
		return
	}

	// 6. Security Check: Block access to ForgeLAB management plane or infrastructure ports
	if r.isForbiddenUpstream(upstreamBase) {
		slog.Error("security violation: local router blocked request to internal management address", "upstream", upstreamBase)
		r.writeErrorResponse(stream, http.StatusForbidden, "Access Denied", "Requests to internal infrastructure are blocked.")
		return
	}

	// Check if this is a WebSocket upgrade request
	isWebSocket := strings.EqualFold(req.Header.Get("Upgrade"), "websocket") ||
		strings.Contains(strings.ToLower(req.Header.Get("Connection")), "upgrade")

	if isWebSocket {
		r.proxyWebSocket(stream, req, upstreamBase, cleanPath)
		return
	}

	// 7. Standard HTTP / Streaming Proxy
	r.proxyHTTP(ctx, stream, req, upstreamBase, cleanPath, share)
}

// proxyHTTP proxies standard HTTP requests with streaming request and response bodies.
func (r *LocalRouter) proxyHTTP(
	ctx context.Context,
	stream net.Conn,
	req *http.Request,
	upstreamBase string,
	cleanPath string,
	share *models.PublicShare,
) {
	targetURL := strings.TrimRight(upstreamBase, "/") + cleanPath
	if req.URL.RawQuery != "" {
		targetURL += "?" + req.URL.RawQuery
	}

	upstreamReq, err := http.NewRequestWithContext(ctx, req.Method, targetURL, req.Body)
	if err != nil {
		r.writeErrorResponse(stream, http.StatusInternalServerError, "Proxy Error", "Failed to construct upstream request.")
		return
	}

	// Copy incoming headers excluding hop-by-hop and ForgeLAB internal headers
	for k, vv := range req.Header {
		lower := strings.ToLower(k)
		if r.isHopByHopHeader(lower) || strings.HasPrefix(lower, "x-forgelab-") {
			continue
		}
		for _, v := range vv {
			upstreamReq.Header.Add(k, v)
		}
	}

	// Set standard forwarding headers
	clientIP := req.Header.Get(HeaderForgeLabClientIP)
	if clientIP == "" {
		clientIP = req.RemoteAddr
		if host, _, err := net.SplitHostPort(clientIP); err == nil {
			clientIP = host
		}
	}
	upstreamReq.Header.Set("X-Forwarded-For", clientIP)
	upstreamReq.Header.Set("X-Forwarded-Proto", "https")
	upstreamReq.Header.Set("X-Forwarded-Host", share.PublicHost)
	upstreamReq.Header.Set("X-Forwarded-Port", "443")

	// Set Host header to upstream target
	if parsed, err := url.Parse(upstreamBase); err == nil && parsed.Host != "" {
		upstreamReq.Host = parsed.Host
	}

	resp, err := r.httpClient.Do(upstreamReq)
	if err != nil {
		slog.Warn("upstream HTTP request failed", "target", targetURL, "error", err)
		r.writeErrorResponse(stream, http.StatusBadGateway, "Bad Gateway", fmt.Sprintf("Failed to communicate with service: %v", err))
		return
	}
	defer resp.Body.Close()

	// Stream response back into Yamux stream
	if err := resp.Write(stream); err != nil {
		slog.Debug("failed to write upstream response to stream", "error", err)
	}
}

// proxyWebSocket handles WebSocket upgrade by establishing a raw TCP stream to the upstream service.
func (r *LocalRouter) proxyWebSocket(
	stream net.Conn,
	req *http.Request,
	upstreamBase string,
	cleanPath string,
) {
	u, err := url.Parse(upstreamBase)
	if err != nil {
		r.writeErrorResponse(stream, http.StatusInternalServerError, "Proxy Error", "Invalid upstream URL.")
		return
	}

	upstreamHost := u.Host
	if !strings.Contains(upstreamHost, ":") {
		if u.Scheme == "https" {
			upstreamHost += ":443"
		} else {
			upstreamHost += ":80"
		}
	}

	upstreamConn, err := net.DialTimeout("tcp", upstreamHost, 5*time.Second)
	if err != nil {
		slog.Warn("failed to dial upstream for websocket upgrade", "upstream", upstreamHost, "error", err)
		r.writeErrorResponse(stream, http.StatusBadGateway, "WebSocket Connection Failed", "Could not reach upstream service for WebSocket handshake.")
		return
	}
	defer upstreamConn.Close()

	// Rewrite request URL and Host
	req.URL.Scheme = "http"
	req.URL.Host = upstreamHost
	req.Host = upstreamHost
	req.Header.Del(HeaderForgeLabShareToken)
	req.Header.Del(HeaderForgeLabClientIP)

	// Write handshake to upstream
	if err := req.Write(upstreamConn); err != nil {
		slog.Warn("failed to write websocket handshake to upstream", "error", err)
		return
	}

	// Bidirectional stream pipe
	var wg sync.WaitGroup
	wg.Add(2)

	pipe := func(dst io.Writer, src io.Reader, cancelClose io.Closer) {
		defer wg.Done()
		_, _ = io.Copy(dst, src)
		_ = cancelClose.Close()
	}

	go pipe(upstreamConn, stream, upstreamConn)
	go pipe(stream, upstreamConn, stream)

	wg.Wait()
}

func (r *LocalRouter) isHopByHopHeader(header string) bool {
	switch header {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization",
		"te", "trailers", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

// isForbiddenUpstream checks whether the target URL points to a forbidden ForgeLAB management or database infrastructure port.
func (r *LocalRouter) isForbiddenUpstream(upstreamURL string) bool {
	u, err := url.Parse(upstreamURL)
	if err != nil {
		return true
	}
	host := u.Hostname()
	port := u.Port()

	// Databases must never receive proxied HTTP traffic
	if port == "5432" || port == "6379" {
		return true
	}

	// ForgeLAB control plane (8080) and local agent (4142) on loopback or core management containers
	isManagementHost := host == "127.0.0.1" ||
		host == "localhost" ||
		host == "::1" ||
		host == "0.0.0.0" ||
		host == "host.docker.internal" ||
		host == "forgelab-backend" ||
		host == "forgelab-postgres" ||
		host == "forgelab-redis"

	if isManagementHost && (port == "8080" || port == "4142") {
		return true
	}

	return false
}

// writeErrorResponse writes a clean, formatted HTTP error response into the stream.
func (r *LocalRouter) writeErrorResponse(stream io.Writer, statusCode int, title, message string) {
	htmlBody := fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
  <meta charset="utf-8">
  <title>%d %s — ForgeLAB Share</title>
  <style>
    body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; background: #0a0a0a; color: #ededed; display: flex; align-items: center; justify-content: center; height: 100vh; margin: 0; }
    .card { background: #171717; border: 1px solid #262626; border-radius: 8px; padding: 32px; max-width: 480px; width: 90%%; box-shadow: 0 4px 20px rgba(0,0,0,0.5); }
    h1 { font-size: 20px; font-weight: 600; margin: 0 0 12px; color: #ffffff; }
    p { font-size: 14px; line-height: 1.5; color: #a1a1aa; margin: 0 0 20px; }
    .badge { display: inline-block; font-size: 11px; font-family: monospace; background: #27272a; color: #f43f5e; padding: 2px 8px; border-radius: 4px; margin-bottom: 12px; }
    .footer { font-size: 12px; color: #52525b; border-top: 1px solid #262626; pt: 16px; margin-top: 16px; }
  </style>
</head>
<body>
  <div class="card">
    <div class="badge">HTTP %d ERROR</div>
    <h1>%s</h1>
    <p>%s</p>
    <div class="footer">ForgeLAB Secure Tunnel Gateway</div>
  </div>
</body>
</html>`, statusCode, title, statusCode, title, message)

	resp := fmt.Sprintf("HTTP/1.1 %d %s\r\nContent-Type: text/html; charset=utf-8\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		statusCode, http.StatusText(statusCode), len(htmlBody), htmlBody)
	_, _ = stream.Write([]byte(resp))
}
