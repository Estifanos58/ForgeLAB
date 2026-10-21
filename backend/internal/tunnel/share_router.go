package tunnel

import (
	"context"
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

	"github.com/forgelab/backend/internal/models"
)

// ShareScopedRouter serves HTTP and WebSocket requests for an individual active share.
type ShareScopedRouter struct {
	share            *models.PublicShare
	serviceLookup    ServiceLookup
	upstreamResolver UpstreamResolver
	httpClient       *http.Client
}

// NewShareScopedRouter creates a new ShareScopedRouter bound to a specific PublicShare.
func NewShareScopedRouter(
	share *models.PublicShare,
	serviceLookup ServiceLookup,
	upstreamResolver UpstreamResolver,
) *ShareScopedRouter {
	return &ShareScopedRouter{
		share:            share,
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
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				// Do not follow redirects automatically; pass redirect headers back to visitor
				return http.ErrUseLastResponse
			},
		},
	}
}

// ServeHTTP handles incoming HTTP and WebSocket requests routed through the tunnel.
func (r *ShareScopedRouter) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// 1. Path traversal defense
	if r.isPathTraversal(req) {
		r.renderError(w, http.StatusBadRequest, "Invalid Path", "Path traversal is strictly prohibited.")
		return
	}

	rawPath := req.URL.Path
	if rawPath == "" {
		rawPath = "/"
	}
	cleanPath := path.Clean(rawPath)
	if strings.Contains(cleanPath, "..") {
		r.renderError(w, http.StatusBadRequest, "Invalid Path", "Path traversal is strictly prohibited.")
		return
	}

	// 2. Route matching (longest prefix first)
	mappings := make([]models.RouteMapping, len(r.share.RouteMappings))
	copy(mappings, r.share.RouteMappings)
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
		matchedMapping = &models.RouteMapping{
			PathPrefix: "/",
			ServiceID:  r.share.PrimaryServiceID,
		}
	}

	ctx, cancel := context.WithTimeout(req.Context(), 60*time.Second)
	defer cancel()

	// 3. Service validation
	targetService, err := r.serviceLookup.GetService(ctx, matchedMapping.ServiceID)
	if err != nil || targetService == nil {
		r.renderError(w, http.StatusBadGateway, "Service Unavailable", "The target service mapped to this route could not be found.")
		return
	}

	if targetService.ProjectID != r.share.ProjectID {
		slog.Error("security boundary violation: route mapped service does not belong to share project",
			"share_id", r.share.ID, "service_id", targetService.ID, "service_project_id", targetService.ProjectID)
		r.renderError(w, http.StatusForbidden, "Access Denied", "Security boundary violation.")
		return
	}

	if targetService.Status != models.ServiceStatusRunning {
		r.renderError(w, http.StatusServiceUnavailable, "Service Stopped",
			fmt.Sprintf("Service '%s' is currently %s. Please start the service in ForgeLAB.", targetService.Name, targetService.Status))
		return
	}

	// 4. Discover reachable upstream address
	upstreamBase, err := r.upstreamResolver.ResolveUpstream(ctx, targetService)
	if err != nil || upstreamBase == "" {
		slog.Warn("share router failed to resolve upstream for service", "service", targetService.Name, "error", err)
		r.renderError(w, http.StatusBadGateway, "Upstream Unreachable",
			fmt.Sprintf("Could not connect to service '%s' on local container/host port: %v", targetService.Name, err))
		return
	}

	// 5. Block access to ForgeLAB management plane or infrastructure ports
	if r.isForbiddenUpstream(upstreamBase) {
		slog.Error("security violation: share router blocked request to internal management address", "upstream", upstreamBase)
		r.renderError(w, http.StatusForbidden, "Access Denied", "Requests to internal infrastructure are blocked.")
		return
	}

	// Calculate target path taking into account StripPrefix
	targetPath := cleanPath
	if matchedMapping.StripPrefix && matchedMapping.PathPrefix != "/" {
		targetPath = strings.TrimPrefix(targetPath, matchedMapping.PathPrefix)
		if !strings.HasPrefix(targetPath, "/") {
			targetPath = "/" + targetPath
		}
	}

	// 6. WebSocket upgrade
	if r.isWebSocket(req) {
		r.proxyWebSocket(w, req, upstreamBase, targetPath)
		return
	}

	// 7. Standard HTTP / Streaming Proxy
	r.proxyHTTP(ctx, w, req, upstreamBase, targetPath)
}

func (r *ShareScopedRouter) isPathTraversal(req *http.Request) bool {
	if strings.Contains(req.RequestURI, "..") || strings.Contains(req.URL.Path, "..") || strings.Contains(req.URL.RawPath, "..") {
		return true
	}
	if unescaped, err := url.QueryUnescape(req.RequestURI); err == nil && strings.Contains(unescaped, "..") {
		return true
	}
	if unescapedPath, err := url.PathUnescape(req.URL.Path); err == nil && strings.Contains(unescapedPath, "..") {
		return true
	}
	return false
}

func (r *ShareScopedRouter) isWebSocket(req *http.Request) bool {
	return strings.EqualFold(req.Header.Get("Upgrade"), "websocket") ||
		strings.Contains(strings.ToLower(req.Header.Get("Connection")), "upgrade")
}

func (r *ShareScopedRouter) proxyHTTP(
	ctx context.Context,
	w http.ResponseWriter,
	req *http.Request,
	upstreamBase string,
	targetPath string,
) {
	targetURL := strings.TrimRight(upstreamBase, "/") + targetPath
	if req.URL.RawQuery != "" {
		targetURL += "?" + req.URL.RawQuery
	}

	upstreamReq, err := http.NewRequestWithContext(ctx, req.Method, targetURL, req.Body)
	if err != nil {
		r.renderError(w, http.StatusInternalServerError, "Proxy Error", "Failed to construct upstream request.")
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

	// Set forwarding headers
	clientIP := req.Header.Get(HeaderForgeLabClientIP)
	if clientIP == "" {
		clientIP = req.RemoteAddr
		if host, _, err := net.SplitHostPort(clientIP); err == nil {
			clientIP = host
		}
	}
	if prior := req.Header.Get("X-Forwarded-For"); prior != "" {
		upstreamReq.Header.Set("X-Forwarded-For", prior+", "+clientIP)
	} else {
		upstreamReq.Header.Set("X-Forwarded-For", clientIP)
	}
	upstreamReq.Header.Set("X-Forwarded-Proto", "https")
	if r.share.PublicHost != "" {
		upstreamReq.Header.Set("X-Forwarded-Host", r.share.PublicHost)
	}
	upstreamReq.Header.Set("X-Forwarded-Port", "443")

	// Set Host header to upstream target host
	if parsed, err := url.Parse(upstreamBase); err == nil && parsed.Host != "" {
		upstreamReq.Host = parsed.Host
	}

	resp, err := r.httpClient.Do(upstreamReq)
	if err != nil {
		slog.Warn("upstream HTTP request failed", "target", targetURL, "error", err)
		r.renderError(w, http.StatusBadGateway, "Bad Gateway", fmt.Sprintf("Failed to communicate with service: %v", err))
		return
	}
	defer resp.Body.Close()

	// Copy response headers
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}

	w.WriteHeader(resp.StatusCode)

	// Stream response body back with flusher support
	flusher, hasFlusher := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				return
			}
			if hasFlusher {
				flusher.Flush()
			}
		}
		if readErr != nil {
			return
		}
	}
}

func (r *ShareScopedRouter) proxyWebSocket(
	w http.ResponseWriter,
	req *http.Request,
	upstreamBase string,
	targetPath string,
) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		r.renderError(w, http.StatusInternalServerError, "Upgrade Not Supported", "Webserver does not support connection hijacking.")
		return
	}

	u, err := url.Parse(upstreamBase)
	if err != nil {
		r.renderError(w, http.StatusInternalServerError, "Proxy Error", "Invalid upstream URL.")
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
		r.renderError(w, http.StatusBadGateway, "WebSocket Connection Failed", "Could not reach upstream service for WebSocket handshake.")
		return
	}
	defer upstreamConn.Close()

	// Hijack client visitor connection
	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		slog.Warn("failed to hijack client connection for websocket", "error", err)
		return
	}
	defer clientConn.Close()

	// Rewrite request URL and Host
	req.URL.Scheme = "http"
	req.URL.Host = upstreamHost
	req.URL.Path = targetPath
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

	go pipe(upstreamConn, clientConn, upstreamConn)
	go pipe(clientConn, upstreamConn, clientConn)

	wg.Wait()
}

func (r *ShareScopedRouter) isHopByHopHeader(header string) bool {
	switch header {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization",
		"te", "trailers", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

func (r *ShareScopedRouter) isForbiddenUpstream(upstreamURL string) bool {
	u, err := url.Parse(upstreamURL)
	if err != nil {
		return true
	}
	port := u.Port()
	switch port {
	case "8080", "5432", "6379", "4142":
		return true
	default:
		return false
	}
}

func (r *ShareScopedRouter) renderError(w http.ResponseWriter, statusCode int, title, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(statusCode)
	html := fmt.Sprintf(`<!DOCTYPE html>
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
    .footer { font-size: 12px; color: #52525b; border-top: 1px solid #262626; padding-top: 16px; margin-top: 16px; }
  </style>
</head>
<body>
  <div class="card">
    <div class="badge">HTTP %d</div>
    <h1>%s</h1>
    <p>%s</p>
    <div class="footer">ForgeLAB Secure Tunnel Gateway</div>
  </div>
</body>
</html>`, statusCode, title, statusCode, title, message)
	_, _ = w.Write([]byte(html))
}
