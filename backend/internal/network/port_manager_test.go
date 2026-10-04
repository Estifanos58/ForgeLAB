package network

import (
	"testing"
)

func TestPortManager_AllocateAndRelease(t *testing.T) {
	pm := NewPortManager(20000, 20010)

	port, err := pm.AllocatePort()
	if err != nil {
		t.Fatalf("unexpected allocation error: %v", err)
	}

	if port < 20000 || port > 20010 {
		t.Fatalf("port %d out of range [20000, 20010]", port)
	}

	if !pm.IsPortUsed(port) {
		t.Fatalf("expected port %d to be marked used", port)
	}

	pm.ReleasePort(port)
	if pm.IsPortUsed(port) {
		t.Fatalf("expected port %d to be marked freed", port)
	}
}

func TestPortManager_Reconciliation(t *testing.T) {
	pm := NewPortManager(20000, 20010)

	// Reconcile with active running ports discovered from Docker daemon
	activePorts := []int{20001, 20003, 20005}
	pm.ReconcileUsedPorts(activePorts)

	for _, p := range activePorts {
		if !pm.IsPortUsed(p) {
			t.Errorf("expected port %d to be used after reconciliation", p)
		}
	}

	if pm.IsPortUsed(20002) {
		t.Errorf("expected port 20002 to be free")
	}

	// Register single port
	pm.RegisterUsedPort(20002)
	if !pm.IsPortUsed(20002) {
		t.Errorf("expected port 20002 to be marked used after RegisterUsedPort")
	}

	used := pm.GetUsedPorts()
	if len(used) != 4 {
		t.Errorf("expected 4 used ports, got %d", len(used))
	}
}
