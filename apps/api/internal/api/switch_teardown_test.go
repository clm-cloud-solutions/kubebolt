package api

import (
	"testing"
)

// A cluster switch tears down the switching caller's own transient state —
// port-forwards and in-flight drains — scoped by org. In a multi-organization
// install one org switching clusters must not tear down another org's. These
// guard the per-org variants; in single-tenant every record carries the one org.

func TestPortForwardStopAllForOrg_ScopesToOrg(t *testing.T) {
	pf := NewPortForwardManager()
	pf.forwards = map[string]*PortForward{
		"a": {ID: "a", OrgID: "org-a", stopCh: make(chan struct{})},
		"b": {ID: "b", OrgID: "org-b", stopCh: make(chan struct{})},
	}
	pf.StopAllForOrg("org-b")
	if _, ok := pf.forwards["a"]; !ok {
		t.Error("StopAllForOrg stopped a forward belonging to another org")
	}
	if _, ok := pf.forwards["b"]; ok {
		t.Error("StopAllForOrg left the target org's own forward running")
	}
}

// The port-forward list is per-org: one org's indicator must show only its own
// forwards, never another org's tunnels. In single-tenant every forward carries
// the one org, so the list is unchanged.
func TestPortForwardListForOrg_ScopesToOrg(t *testing.T) {
	pf := NewPortForwardManager()
	pf.forwards = map[string]*PortForward{
		"a": {ID: "a", OrgID: "org-a", stopCh: make(chan struct{})},
		"b": {ID: "b", OrgID: "org-b", stopCh: make(chan struct{})},
	}
	got := pf.ListForOrg("org-a")
	if len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("ListForOrg(org-a) = %v, want exactly org-a's forward 'a'", got)
	}
	for _, f := range got {
		if f.OrgID != "org-a" {
			t.Errorf("ListForOrg(org-a) returned a forward owned by %q", f.OrgID)
		}
	}
}

func TestDrainCancelAllForOrg_ScopesToOrg(t *testing.T) {
	m := newDrainSessionManager()
	_, ctxA, freshA := m.Start("org-a", "c1", "node-a", nil)
	_, ctxB, freshB := m.Start("org-b", "c1", "node-b", nil)
	if !freshA || !freshB {
		t.Fatal("setup: both drains should have started fresh")
	}

	m.CancelAllForOrg("org-b")

	if _, ok := m.Get("org-a", "c1", "node-a"); !ok {
		t.Error("CancelAllForOrg cancelled a drain belonging to another org")
	}
	select {
	case <-ctxA.Done():
		t.Error("CancelAllForOrg cancelled another org's drain context")
	default:
	}

	if _, ok := m.Get("org-b", "c1", "node-b"); ok {
		t.Error("CancelAllForOrg left the target org's own drain running")
	}
	select {
	case <-ctxB.Done():
	default:
		t.Error("CancelAllForOrg did not cancel the target org's drain context")
	}
}
