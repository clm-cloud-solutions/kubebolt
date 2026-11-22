package api

import "testing"

// Drain sessions are keyed by (org, cluster, node). Keyed by node name alone,
// GET (re-attach to the SSE stream) and DELETE (cancel) found a drain the caller
// did not start by naming its node, and two clusters with a same-named node
// refused each other's drains with a 409.

func TestDrainSession_AnotherOrgCannotReattachOrCancel(t *testing.T) {
	m := newDrainSessionManager()
	_, ctxA, fresh := m.Start("org-a", "prod", "node-1", nil)
	if !fresh {
		t.Fatal("setup: org-a's drain should start fresh")
	}

	if _, ok := m.Get("org-b", "prod", "node-1"); ok {
		t.Error("org-b re-attached to org-a's drain stream by naming the node")
	}
	if m.Cancel("org-b", "prod", "node-1") {
		t.Error("org-b cancelled org-a's drain by naming the node")
	}
	select {
	case <-ctxA.Done():
		t.Error("org-a's drain context was cancelled by org-b")
	default:
	}

	if _, ok := m.Get("org-a", "prod", "node-1"); !ok {
		t.Error("org-a must still find its own drain")
	}
	if !m.Cancel("org-a", "prod", "node-1") {
		t.Error("org-a must be able to cancel its own drain")
	}
}

func TestDrainSession_SameNodeNameInTwoClusters(t *testing.T) {
	m := newDrainSessionManager()
	_, ctx1, fresh1 := m.Start("org-a", "staging", "kind-control-plane", nil)
	_, _, fresh2 := m.Start("org-a", "lab", "kind-control-plane", nil)
	if !fresh1 || !fresh2 {
		t.Fatalf("a same-named node in another cluster was refused (fresh %v, %v)", fresh1, fresh2)
	}
	if m.Cancel("org-a", "lab", "other-node") {
		t.Error("cancel of a node with no drain must report false")
	}
	if !m.Cancel("org-a", "lab", "kind-control-plane") {
		t.Fatal("org-a must cancel the lab drain")
	}
	select {
	case <-ctx1.Done():
		t.Error("cancelling the lab drain cancelled the staging drain of the same node name")
	default:
	}
}

func TestPortForwardStop_OnlyTheOwningOrg(t *testing.T) {
	pf := NewPortForwardManager()
	pf.forwards = map[string]*PortForward{
		"a": {ID: "a", OrgID: "org-a", stopCh: make(chan struct{})},
	}
	if pf.Stop("org-b", "a") {
		t.Error("org-b stopped org-a's port-forward by naming its id")
	}
	if _, ok := pf.forwards["a"]; !ok {
		t.Fatal("org-a's port-forward is gone after org-b's stop")
	}
	if !pf.Stop("org-a", "a") {
		t.Error("org-a must be able to stop its own port-forward")
	}
	if _, ok := pf.forwards["a"]; ok {
		t.Error("org-a's own stop must remove the forward")
	}
}
