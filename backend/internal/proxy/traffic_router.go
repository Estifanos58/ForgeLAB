package proxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	SlotBlue  = "blue"
	SlotGreen = "green"
)

var (
	ErrRouteNotFound = errors.New("service upstream route not found")
	ErrInvalidTarget = errors.New("invalid upstream target host or port")
)

// UpstreamRoute describes the active upstream slot and destination for a proxied service.
type UpstreamRoute struct {
	ServiceID   uuid.UUID `json:"service_id"`
	ProjectID   uuid.UUID `json:"project_id"`
	ServiceName string    `json:"service_name"`
	ActiveSlot  string    `json:"active_slot"` // "blue" or "green"
	UpstreamURL string    `json:"upstream_url"`
	ContainerID string    `json:"container_id,omitempty"`
	HostPort    int       `json:"host_port"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type routeEntry struct {
	info  UpstreamRoute
	proxy *httputil.ReverseProxy
}

// TrafficRouter provides thread-safe atomic upstream switching for blue-green service deployments.
type TrafficRouter struct {
	mu        sync.RWMutex
	routes    map[uuid.UUID]*routeEntry
	transport http.RoundTripper
}

// NewTrafficRouter creates a new thread-safe TrafficRouter.
func NewTrafficRouter() *TrafficRouter {
	return &TrafficRouter{
		routes: make(map[uuid.UUID]*routeEntry),
		transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			MaxIdleConns:          128,
			MaxIdleConnsPerHost:   32,
			IdleConnTimeout:       60 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
		},
	}
}

func normalizeTargetHost(targetHost string) string {
	h := strings.TrimSpace(targetHost)
	if h == "" || h == "0.0.0.0" || h == "::" {
		return "127.0.0.1"
	}
	return h
}

func (tr *TrafficRouter) buildProxy(rawURL, slot string) (*httputil.ReverseProxy, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("%w: %q", ErrInvalidTarget, rawURL)
	}

	transport := tr.transport
	if transport == nil {
		transport = http.DefaultTransport
	}

	rp := &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(parsed)
			pr.SetXForwarded()
			if pr.In.Host != "" {
				pr.Out.Host = pr.In.Host
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			if slot != "" {
				resp.Header.Set("X-ForgeLAB-Slot", slot)
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if slot != "" {
				w.Header().Set("X-ForgeLAB-Slot", slot)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error": "upstream service unavailable",
			})
		},
	}
	return rp, nil
}

// EnsureRoute registers a baseline blue upstream route only if no route currently exists for the service.
func (tr *TrafficRouter) EnsureRoute(
	serviceID, projectID uuid.UUID,
	serviceName, targetHost string,
	targetPort int,
	containerID string,
) (UpstreamRoute, bool, error) {
	if serviceID == uuid.Nil || targetPort <= 0 || targetPort > 65535 {
		return UpstreamRoute{}, false, ErrInvalidTarget
	}

	host := normalizeTargetHost(targetHost)
	upstreamURL := fmt.Sprintf("http://%s:%d", host, targetPort)

	tr.mu.Lock()
	defer tr.mu.Unlock()

	if existing, ok := tr.routes[serviceID]; ok {
		return existing.info, false, nil
	}

	slot := SlotBlue
	rp, err := tr.buildProxy(upstreamURL, slot)
	if err != nil {
		return UpstreamRoute{}, false, err
	}

	info := UpstreamRoute{
		ServiceID:   serviceID,
		ProjectID:   projectID,
		ServiceName: serviceName,
		ActiveSlot:  slot,
		UpstreamURL: upstreamURL,
		ContainerID: containerID,
		HostPort:    targetPort,
		UpdatedAt:   time.Now().UTC(),
	}
	tr.routes[serviceID] = &routeEntry{info: info, proxy: rp}
	return info, true, nil
}

// SwitchTraffic atomically updates the active upstream for a service, alternating between blue and green slots.
func (tr *TrafficRouter) SwitchTraffic(
	serviceID, projectID uuid.UUID,
	serviceName, targetHost string,
	targetPort int,
	containerID string,
) (UpstreamRoute, error) {
	if serviceID == uuid.Nil || targetPort <= 0 || targetPort > 65535 {
		return UpstreamRoute{}, ErrInvalidTarget
	}

	host := normalizeTargetHost(targetHost)
	upstreamURL := fmt.Sprintf("http://%s:%d", host, targetPort)

	tr.mu.Lock()
	defer tr.mu.Unlock()

	nextSlot := SlotBlue
	if existing, ok := tr.routes[serviceID]; ok {
		sameContainer := containerID != "" && existing.info.ContainerID == containerID
		sameEndpoint := existing.info.UpstreamURL == upstreamURL && existing.info.HostPort == targetPort
		if sameContainer && sameEndpoint {
			nextSlot = existing.info.ActiveSlot
		} else if existing.info.ActiveSlot == SlotBlue {
			nextSlot = SlotGreen
		} else {
			nextSlot = SlotBlue
		}
	}

	rp, err := tr.buildProxy(upstreamURL, nextSlot)
	if err != nil {
		return UpstreamRoute{}, err
	}

	info := UpstreamRoute{
		ServiceID:   serviceID,
		ProjectID:   projectID,
		ServiceName: serviceName,
		ActiveSlot:  nextSlot,
		UpstreamURL: upstreamURL,
		ContainerID: containerID,
		HostPort:    targetPort,
		UpdatedAt:   time.Now().UTC(),
	}
	tr.routes[serviceID] = &routeEntry{info: info, proxy: rp}
	return info, nil
}

// RestoreRoute restores a previously captured route snapshot without altering its slot label.
func (tr *TrafficRouter) RestoreRoute(prev UpstreamRoute) error {
	if prev.ServiceID == uuid.Nil || prev.UpstreamURL == "" {
		return ErrInvalidTarget
	}
	slot := prev.ActiveSlot
	if slot != SlotBlue && slot != SlotGreen {
		slot = SlotBlue
	}
	prev.ActiveSlot = slot
	prev.UpdatedAt = time.Now().UTC()

	rp, err := tr.buildProxy(prev.UpstreamURL, slot)
	if err != nil {
		return err
	}

	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.routes[prev.ServiceID] = &routeEntry{info: prev, proxy: rp}
	return nil
}

// RemoveRoute removes an upstream route for a service.
func (tr *TrafficRouter) RemoveRoute(serviceID uuid.UUID) {
	if serviceID == uuid.Nil {
		return
	}
	tr.mu.Lock()
	defer tr.mu.Unlock()
	delete(tr.routes, serviceID)
}

// RemoveProjectRoutes removes all upstream routes belonging to a project.
func (tr *TrafficRouter) RemoveProjectRoutes(projectID uuid.UUID) {
	if projectID == uuid.Nil {
		return
	}
	tr.mu.Lock()
	defer tr.mu.Unlock()
	for id, entry := range tr.routes {
		if entry.info.ProjectID == projectID {
			delete(tr.routes, id)
		}
	}
}

// GetRoute returns a snapshot of the active route for a service.
func (tr *TrafficRouter) GetRoute(serviceID uuid.UUID) (UpstreamRoute, bool) {
	tr.mu.RLock()
	defer tr.mu.RUnlock()
	entry, ok := tr.routes[serviceID]
	if !ok {
		return UpstreamRoute{}, false
	}
	return entry.info, true
}

// ListRoutes returns all currently registered service upstream routes sorted deterministically.
func (tr *TrafficRouter) ListRoutes() []UpstreamRoute {
	tr.mu.RLock()
	defer tr.mu.RUnlock()
	res := make([]UpstreamRoute, 0, len(tr.routes))
	for _, entry := range tr.routes {
		res = append(res, entry.info)
	}
	sort.Slice(res, func(i, j int) bool {
		if res[i].ServiceName != res[j].ServiceName {
			return res[i].ServiceName < res[j].ServiceName
		}
		return res[i].ServiceID.String() < res[j].ServiceID.String()
	})
	return res
}

// ServeServiceHTTP forwards an HTTP request to the service's active blue/green upstream slot.
func (tr *TrafficRouter) ServeServiceHTTP(serviceID uuid.UUID, w http.ResponseWriter, r *http.Request) {
	tr.mu.RLock()
	entry, ok := tr.routes[serviceID]
	tr.mu.RUnlock()

	if !ok || entry == nil || entry.proxy == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": ErrRouteNotFound.Error(),
		})
		return
	}

	entry.proxy.ServeHTTP(w, r)
}
