package cluster

import (
	"context"
	"errors"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/agent/channel"
)

// The per-request replacements for the global-slot accessors (finding #55). In
// the Enterprise build each pins that what a handler reads describes the
// CALLER's organization, never whichever org last took the global seat; those
// cases need several orgs and live in the EE test file.
//
// OSS collapses the org dimension: one tenant, one seat, and the global active
// context is the right answer. The refactor must not regress that — this is the
// case the EE file carries under the same name.
func TestManager_PerOrgAccessors_OSSKeepsGlobalActive(t *testing.T) {
	m := newBareManager()
	m.SetAgentRegistry(channel.NewAgentRegistry())
	m.agentProxyContexts = map[string]string{"agent:cid": "cid"}
	m.activeContext = "agent:cid"
	m.connErr = errors.New("boom")

	ctx := context.Background() // no RuntimeKey at all — the OSS shape
	if got := m.ActiveContextFor(ctx); got != "agent:cid" {
		t.Errorf("OSS ActiveContextFor = %q, want agent:cid", got)
	}
	if got := m.ActiveAgentProxyClusterIDFor(ctx); got != "cid" {
		t.Errorf("OSS ActiveAgentProxyClusterIDFor = %q, want cid", got)
	}
	if err := m.ConnErrorFor(ctx); err == nil {
		t.Error("OSS ConnErrorFor = nil, want the active connect error")
	}
}

// An explicit X-KubeBolt-Cluster names the cluster the request operates on and
// always wins over the active selection.
func TestManager_ActiveContextFor_ExplicitClusterWins(t *testing.T) {
	m := newBareManager()
	m.activeContext = "agent:cid"
	ctx := WithRuntimeKey(context.Background(), RuntimeKey{Cluster: "explicit-ctx"})
	if got := m.ActiveContextFor(ctx); got != "explicit-ctx" {
		t.Errorf("explicit cluster ignored: got %q", got)
	}
	// A non-active cluster with no pooled runtime has no recorded failure.
	if err := m.ConnErrorFor(ctx); err != nil {
		t.Errorf("ConnErrorFor(explicit, no runtime) = %v, want nil", err)
	}
}
