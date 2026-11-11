package docker

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/moby/moby/client"
)

// NetworkEnsureOptions specifies configuration parameters when ensuring a Docker network.
type NetworkEnsureOptions struct {
	Labels   map[string]string
	Driver   string
	Internal bool
	External bool
}

// EnsureNetwork ensures a Docker network exists with standard bridge driver and provided labels.
func (e *Engine) EnsureNetwork(ctx context.Context, networkName string, labels map[string]string) error {
	return e.EnsureNetworkWithOptions(ctx, networkName, NetworkEnsureOptions{
		Labels: labels,
		Driver: "bridge",
	})
}

// EnsureNetworkWithOptions creates or verifies a Docker network according to declared settings:
// - If External is true: verifies the network already exists; fails if missing.
// - If Internal is true: configures Docker bridge network with Internal: true (isolated from external traffic).
// - Preserves project labels and handles pool exhaustion self-healing.
func (e *Engine) EnsureNetworkWithOptions(ctx context.Context, networkName string, opts NetworkEnsureOptions) error {
	if e.dockerClient == nil {
		return nil
	}

	driver := opts.Driver
	if driver == "" {
		driver = "bridge"
	}

	// 1. Inspect check
	_, inspectErr := e.dockerClient.NetworkInspect(ctx, networkName, client.NetworkInspectOptions{})
	if inspectErr == nil {
		return nil
	}

	// If external network is declared but does not exist in Docker daemon -> must fail!
	if opts.External {
		return fmt.Errorf("external network '%s' not found: %w", networkName, inspectErr)
	}

	// 2. Attempt creation with Driver, Internal, and Labels
	createOpts := client.NetworkCreateOptions{
		Driver:   driver,
		Internal: opts.Internal,
		Labels:   opts.Labels,
	}

	_, err := e.dockerClient.NetworkCreate(ctx, networkName, createOpts)
	if err == nil {
		return nil
	}

	// 3. Handle conflict / already exists (concurrent worker created it)
	errStr := strings.ToLower(err.Error())
	if strings.Contains(errStr, "already exists") || strings.Contains(errStr, "conflict") {
		if _, inspectErr := e.dockerClient.NetworkInspect(ctx, networkName, client.NetworkInspectOptions{}); inspectErr == nil {
			return nil
		}
	}

	// 4. Handle pool exhaustion / overlap: self-heal by pruning orphaned unattached forgelab networks
	if strings.Contains(errStr, "pool") || strings.Contains(errStr, "address") || strings.Contains(errStr, "overlap") {
		slog.Warn("docker network pool exhaustion detected; pruning orphaned networks", "network", networkName, "error", err)
		_ = e.PruneDanglingNetworks(ctx)
		// Retry creation once after pruning
		_, retryErr := e.dockerClient.NetworkCreate(ctx, networkName, createOpts)
		if retryErr == nil {
			slog.Info("successfully created network after pruning orphaned networks", "network", networkName)
			return nil
		}
		if strings.Contains(strings.ToLower(retryErr.Error()), "already exists") || strings.Contains(strings.ToLower(retryErr.Error()), "conflict") {
			if _, inspectErr := e.dockerClient.NetworkInspect(ctx, networkName, client.NetworkInspectOptions{}); inspectErr == nil {
				return nil
			}
		}
		err = retryErr
	}

	return fmt.Errorf("failed to create network '%s': %w", networkName, err)
}

// PruneDanglingNetworks cleans up orphaned ForgeLAB bridge networks that have zero connected containers.
func (e *Engine) PruneDanglingNetworks(ctx context.Context) error {
	if e.dockerClient == nil {
		return nil
	}

	netsRes, err := e.dockerClient.NetworkList(ctx, client.NetworkListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list networks for pruning: %w", err)
	}

	for _, n := range netsRes.Items {
		// Only touch networks created by ForgeLAB
		isForgeLabNet := strings.HasPrefix(n.Name, "forgelab-net-") || (n.Labels != nil && n.Labels["forgelab.project_id"] != "")
		if !isForgeLabNet {
			continue
		}

		// Inspect network to verify no active containers are attached
		insp, inspectErr := e.dockerClient.NetworkInspect(ctx, n.ID, client.NetworkInspectOptions{})
		if inspectErr != nil {
			continue
		}
		if len(insp.Network.Containers) == 0 {
			if _, removeErr := e.dockerClient.NetworkRemove(ctx, n.ID, client.NetworkRemoveOptions{}); removeErr == nil {
				shortID := n.ID
				if len(shortID) > 12 {
					shortID = shortID[:12]
				}
				slog.Info("pruned orphaned forgelab network", "network_id", shortID, "network_name", n.Name)
			}
		}
	}
	return nil
}
