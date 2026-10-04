package network

import (
	"fmt"
	"net"
	"sync"
)

// PortManager handles dynamic host port allocation for deployed applications.
type PortManager struct {
	mu        sync.Mutex
	startPort int
	endPort   int
	usedPorts map[int]bool
}

func NewPortManager(startPort, endPort int) *PortManager {
	if startPort <= 0 {
		startPort = 10000
	}
	if endPort <= startPort {
		endPort = 60000
	}
	return &PortManager{
		startPort: startPort,
		endPort:   endPort,
		usedPorts: make(map[int]bool),
	}
}

// AllocatePort finds and reserves an available TCP port on the host.
func (pm *PortManager) AllocatePort() (int, error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	for port := pm.startPort; port <= pm.endPort; port++ {
		if pm.usedPorts[port] {
			continue
		}

		addr := fmt.Sprintf("127.0.0.1:%d", port)
		l, err := net.Listen("tcp", addr)
		if err == nil {
			l.Close()
			pm.usedPorts[port] = true
			return port, nil
		}
	}

	return 0, fmt.Errorf("no available host ports in range %d-%d", pm.startPort, pm.endPort)
}

// ReleasePort frees an allocated port.
func (pm *PortManager) ReleasePort(port int) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	delete(pm.usedPorts, port)
}

// RegisterUsedPort manually marks a host port as occupied (e.g. discovered during container reconciliation).
func (pm *PortManager) RegisterUsedPort(port int) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.usedPorts[port] = true
}

// IsPortUsed checks whether a port is currently marked as occupied.
func (pm *PortManager) IsPortUsed(port int) bool {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return pm.usedPorts[port]
}

// ReconcileUsedPorts resets and updates the manager with active host ports.
func (pm *PortManager) ReconcileUsedPorts(ports []int) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.usedPorts = make(map[int]bool, len(ports))
	for _, p := range ports {
		if p > 0 {
			pm.usedPorts[p] = true
		}
	}
}

// GetUsedPorts returns a slice of currently used ports.
func (pm *PortManager) GetUsedPorts() []int {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	res := make([]int, 0, len(pm.usedPorts))
	for p := range pm.usedPorts {
		res = append(res, p)
	}
	return res
}
