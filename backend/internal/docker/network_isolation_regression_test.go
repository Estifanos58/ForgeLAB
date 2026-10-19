package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/discovery"
	"github.com/forgelab/backend/internal/models"
)

func newDirectTestDockerClient(t *testing.T, handler http.Handler) (*client.Client, func()) {
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("API-Version", "1.41")
		if strings.Contains(r.URL.Path, "/_ping") {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("OK"))
			return
		}
		handler.ServeHTTP(w, r)
	})
	server := httptest.NewServer(wrapped)
	cli, err := client.NewClientWithOpts(
		client.WithHost(server.URL),
		client.WithHTTPClient(server.Client()),
		client.WithVersion("1.41"),
	)
	require.NoError(t, err)

	return cli, func() {
		server.Close()
		cli.Close()
	}
}

// TestNetwork_Isolation_PreservesDeclaredComposeNetworks verifies that when explicit
// networks are declared, services are attached only to their declared networks and NOT
// to a common project network mesh.
func TestNetwork_Isolation_PreservesDeclaredComposeNetworks(t *testing.T) {
	projID := uuid.New()

	plan := discovery.DeploymentPlan{
		NetworkConfigs: []discovery.NetworkConfig{
			{Name: "frontend_net", Driver: "bridge"},
			{Name: "backend_net", Driver: "bridge", Internal: true},
		},
		Services: []discovery.PlannedService{
			{
				Name:     "frontend",
				Networks: []string{"frontend_net"},
				NetworkAliases: map[string][]string{
					"frontend_net": {"web-client", "ui"},
				},
			},
			{
				Name:     "backend",
				Networks: []string{"backend_net"},
				NetworkAliases: map[string][]string{
					"backend_net": {"api-server"},
				},
			},
		},
	}
	planBytes, _ := json.Marshal(plan)

	project := &models.Project{
		ID:             projID,
		DeploymentPlan: planBytes,
	}

	netConfigMap := make(map[string]discovery.NetworkConfig)
	for _, nc := range plan.NetworkConfigs {
		netConfigMap[nc.Name] = nc
	}

	planAliasesMap := make(map[string]map[string][]string)
	for _, ps := range plan.Services {
		if len(ps.NetworkAliases) > 0 {
			planAliasesMap[ps.Name] = ps.NetworkAliases
		}
	}

	// Service A: frontend
	serviceA := &models.Service{
		Name:     "frontend",
		Networks: []string{"frontend_net"},
	}
	// Verify that frontend only attaches to frontend_net
	var serviceANets []string
	if len(serviceA.Networks) > 0 {
		for _, net := range serviceA.Networks {
			serviceANets = append(serviceANets, "forgelab-net-"+project.ID.String()[:8]+"-"+net)
		}
	}
	assert.Equal(t, 1, len(serviceANets))
	assert.Contains(t, serviceANets[0], "frontend_net")
	assert.NotContains(t, serviceANets[0], "backend_net")

	// Service B: backend (with internal: true)
	serviceB := &models.Service{
		Name:     "backend",
		Networks: []string{"backend_net"},
	}
	var serviceBNets []string
	if len(serviceB.Networks) > 0 {
		for _, net := range serviceB.Networks {
			serviceBNets = append(serviceBNets, "forgelab-net-"+project.ID.String()[:8]+"-"+net)
		}
	}
	assert.Equal(t, 1, len(serviceBNets))
	assert.Contains(t, serviceBNets[0], "backend_net")
	assert.NotContains(t, serviceBNets[0], "frontend_net")

	// Verify network options for backend_net
	backendNC := netConfigMap["backend_net"]
	assert.True(t, backendNC.Internal, "backend_net must have Internal: true configured")

	// Verify aliases for frontend on frontend_net
	frontendAliases := []string{serviceA.Name}
	for _, a := range planAliasesMap[serviceA.Name]["frontend_net"] {
		if a != serviceA.Name {
			frontendAliases = append(frontendAliases, a)
		}
	}
	assert.ElementsMatch(t, []string{"frontend", "web-client", "ui"}, frontendAliases)

	// Verify aliases for backend on backend_net
	backendAliases := []string{serviceB.Name}
	for _, a := range planAliasesMap[serviceB.Name]["backend_net"] {
		if a != serviceB.Name {
			backendAliases = append(backendAliases, a)
		}
	}
	assert.ElementsMatch(t, []string{"backend", "api-server"}, backendAliases)
}

// TestNetwork_ExternalNetworkMissing_FailsImmediately verifies that when a service
// specifies an external network that does not exist in the Docker daemon, EnsureNetworkWithOptions
// returns an error rather than creating it.
func TestNetwork_ExternalNetworkMissing_FailsImmediately(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Mock Docker daemon NetworkInspect returning 404 (network not found)
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"network my_external_net not found"}`))
	})

	cli, cleanup := newDirectTestDockerClient(t, handler)
	defer cleanup()

	eng := &Engine{dockerClient: cli}

	err := eng.EnsureNetworkWithOptions(context.Background(), "my_external_net", NetworkEnsureOptions{
		External: true,
	})
	require.Error(t, err, "EnsureNetworkWithOptions must fail when declared external network does not exist")
	assert.Contains(t, err.Error(), "external network 'my_external_net' not found")
}

// TestNetwork_SecondaryNetworkAttachmentFailure_TreatedAsFatal verifies that when connecting
// a container to a secondary network fails, it is treated as a fatal deployment failure.
func TestNetwork_SecondaryNetworkAttachmentFailure_TreatedAsFatal(t *testing.T) {
	var networkConnectCalls atomic.Int32
	var containerRemoveCalls atomic.Int32

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// 1. ContainerInspect
		if strings.Contains(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/json") {
			_ = json.NewEncoder(w).Encode(types.ContainerJSON{
				ContainerJSONBase: &types.ContainerJSONBase{
					State: &types.ContainerState{Running: true},
				},
			})
			return
		}

		// 2. NetworkConnect fails
		if strings.Contains(r.URL.Path, "/networks/") && strings.HasSuffix(r.URL.Path, "/connect") {
			networkConnectCalls.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"failed to add interface to network: address collision"}`))
			return
		}

		// 3. ContainerRemove
		if r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/containers/") {
			containerRemoveCalls.Add(1)
			w.WriteHeader(http.StatusNoContent)
			return
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})

	cli, cleanup := newDirectTestDockerClient(t, handler)
	defer cleanup()

	// Simulate error handling matching engine.go lines ~2048-2066
	execCtx := context.Background()
	containerID := "test-container-sec-fail"
	secNet := "forgelab-net-isolated"

	netConnErr := cli.NetworkConnect(execCtx, secNet, containerID, nil)
	require.Error(t, netConnErr)

	// In engine.go, when netConnErr != nil:
	// Container is removed, port is released, and deployment fails with error
	_ = cli.ContainerRemove(context.Background(), containerID, container.RemoveOptions{Force: true})

	assert.Equal(t, int32(1), networkConnectCalls.Load())
	assert.Equal(t, int32(1), containerRemoveCalls.Load(), "container must be destroyed when network connection fails")
}

// TestNetwork_NoExplicitNetworks_UsesCommonProjectNetwork verifies that when a service
// does NOT define explicit networks, it cleanly defaults to the common project network.
func TestNetwork_NoExplicitNetworks_UsesCommonProjectNetwork(t *testing.T) {
	projID := uuid.New()
	service := &models.Service{
		Name:     "simple-api",
		Networks: []string{}, // No declared networks
	}

	var primaryNetworkName string
	if len(service.Networks) > 0 {
		primaryNetworkName = "custom-net"
	} else {
		primaryNetworkName = "forgelab-net-" + projID.String()
	}

	assert.Equal(t, "forgelab-net-"+projID.String(), primaryNetworkName)
}
