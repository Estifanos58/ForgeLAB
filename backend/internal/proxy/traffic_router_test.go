package proxy

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func extractHostPort(t *testing.T, serverURL string) (string, int) {
	t.Helper()
	u, err := url.Parse(serverURL)
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)
	return u.Hostname(), port
}

func TestTrafficRouter_AtomicBlueGreenSwitchUnderConcurrentLoad(t *testing.T) {
	blueServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Upstream-Version", "v1-blue")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("blue-response:" + r.URL.Path))
	}))
	defer blueServer.Close()

	greenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Upstream-Version", "v2-green")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("green-response:" + r.URL.Path))
	}))
	defer greenServer.Close()

	blueHost, bluePort := extractHostPort(t, blueServer.URL)
	greenHost, greenPort := extractHostPort(t, greenServer.URL)

	router := NewTrafficRouter()
	projectID := uuid.New()
	serviceID := uuid.New()

	// Step 1: Initialize Blue upstream
	route, err := router.SwitchTraffic(serviceID, projectID, "web", blueHost, bluePort, "container-blue-1")
	require.NoError(t, err)
	assert.Equal(t, SlotBlue, route.ActiveSlot)
	assert.Equal(t, bluePort, route.HostPort)

	proxyGateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		router.ServeServiceHTTP(serviceID, w, r)
	}))
	defer proxyGateway.Close()

	// Step 2: Concurrent reader load while switching Blue -> Green
	var blueSeen atomic.Int64
	var greenSeen atomic.Int64
	var non200 atomic.Int64

	stopCh := make(chan struct{})
	var wg sync.WaitGroup
	httpClient := &http.Client{Timeout: 5 * time.Second}

	for w := 0; w < 6; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stopCh:
					return
				default:
					resp, err := httpClient.Get(fmt.Sprintf("%s/health", proxyGateway.URL))
					if err != nil {
						non200.Add(1)
						continue
					}
					body, _ := io.ReadAll(resp.Body)
					resp.Body.Close()
					if resp.StatusCode != http.StatusOK {
						non200.Add(1)
						continue
					}
					switch string(body) {
					case "blue-response:/health":
						blueSeen.Add(1)
					case "green-response:/health":
						greenSeen.Add(1)
					default:
						non200.Add(1)
					}
				}
			}
		}()
	}

	// Let readers hit Blue first
	require.Eventually(t, func() bool {
		return blueSeen.Load() >= 10
	}, 2*time.Second, 5*time.Millisecond)

	// Step 3: Atomically switch traffic to Green while readers keep hitting proxy
	route, err = router.SwitchTraffic(serviceID, projectID, "web", greenHost, greenPort, "container-green-2")
	require.NoError(t, err)
	assert.Equal(t, SlotGreen, route.ActiveSlot)
	assert.Equal(t, greenPort, route.HostPort)

	// Confirm readers seamlessly transition to Green
	require.Eventually(t, func() bool {
		return greenSeen.Load() >= 10
	}, 2*time.Second, 5*time.Millisecond)

	close(stopCh)
	wg.Wait()

	assert.Zero(t, non200.Load(), "zero dropped or failed requests expected during atomic cutover")

	// Step 4: Verify header reflects active slot after switch
	resp, err := httpClient.Get(fmt.Sprintf("%s/check", proxyGateway.URL))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, SlotGreen, resp.Header.Get("X-ForgeLAB-Slot"))
	assert.Equal(t, "v2-green", resp.Header.Get("X-Upstream-Version"))
}

func TestTrafficRouter_EnsureRestoreRemove(t *testing.T) {
	blueServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("blue-ok"))
	}))
	defer blueServer.Close()

	greenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("green-ok"))
	}))
	defer greenServer.Close()

	blueHost, bluePort := extractHostPort(t, blueServer.URL)
	greenHost, greenPort := extractHostPort(t, greenServer.URL)

	router := NewTrafficRouter()
	projectID := uuid.New()
	serviceID := uuid.New()

	// EnsureRoute creates initial Blue route and does not overwrite on second call
	r1, created, err := router.EnsureRoute(serviceID, projectID, "api", blueHost, bluePort, "c-blue")
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, SlotBlue, r1.ActiveSlot)

	r2, createdAgain, err := router.EnsureRoute(serviceID, projectID, "api", greenHost, greenPort, "c-green")
	require.NoError(t, err)
	assert.False(t, createdAgain)
	assert.Equal(t, bluePort, r2.HostPort)

	// Switch to green, then restore previous blue snapshot
	r3, err := router.SwitchTraffic(serviceID, projectID, "api", greenHost, greenPort, "c-green")
	require.NoError(t, err)
	assert.Equal(t, SlotGreen, r3.ActiveSlot)

	require.NoError(t, router.RestoreRoute(r1))
	got, ok := router.GetRoute(serviceID)
	require.True(t, ok)
	assert.Equal(t, SlotBlue, got.ActiveSlot)
	assert.Equal(t, "c-blue", got.ContainerID)

	// Idempotent switch with same container keeps slot
	rSame, err := router.SwitchTraffic(serviceID, projectID, "api", blueHost, bluePort, "c-blue")
	require.NoError(t, err)
	assert.Equal(t, SlotBlue, rSame.ActiveSlot)

	// Remove route -> 503
	router.RemoveRoute(serviceID)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	router.ServeServiceHTTP(serviceID, rec, req)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)

	// RemoveProjectRoutes cleans up all routes for project
	_, _, err = router.EnsureRoute(serviceID, projectID, "api", blueHost, bluePort, "c-blue")
	require.NoError(t, err)
	assert.Len(t, router.ListRoutes(), 1)
	router.RemoveProjectRoutes(projectID)
	assert.Len(t, router.ListRoutes(), 0)
}
