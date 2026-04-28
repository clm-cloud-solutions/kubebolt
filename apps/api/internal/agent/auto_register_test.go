package agent

import (
	"sync"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/agent/channel"
)

// fakeRegistrar captures calls so tests can assert on them. Methods
// are concurrency-safe in case the helper later runs from multiple
// goroutines.
type fakeRegistrar struct {
	mu      sync.Mutex
	added   []addCall
	removed []string
	addErr  error
}

type addCall struct {
	clusterID, displayName string
}

func (r *fakeRegistrar) AddAgentProxyCluster(clusterID, displayName string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.addErr != nil {
		return "", r.addErr
	}
	r.added = append(r.added, addCall{clusterID, displayName})
	return "agent:" + clusterID, nil
}

func (r *fakeRegistrar) RemoveAgentProxyCluster(clusterID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.removed = append(r.removed, clusterID)
}

func TestMaybeAutoRegister_RegistersWhenAllConditionsMet(t *testing.T) {
	reg := &fakeRegistrar{}
	registry := channel.NewAgentRegistry()
	got := maybeAutoRegisterCluster(reg, registry, true, "c-prod", "Prod EU", []string{"metrics", "kube-proxy"})
	if !got {
		t.Fatal("registration should have happened")
	}
	if len(reg.added) != 1 || reg.added[0].clusterID != "c-prod" || reg.added[0].displayName != "Prod EU" {
		t.Errorf("added = %+v", reg.added)
	}
}

func TestMaybeAutoRegister_SkipsWhenFlagOff(t *testing.T) {
	reg := &fakeRegistrar{}
	registry := channel.NewAgentRegistry()
	got := maybeAutoRegisterCluster(reg, registry, false, "c1", "x", []string{"kube-proxy"})
	if got {
		t.Fatal("flag=false must skip registration")
	}
	if len(reg.added) != 0 {
		t.Errorf("AddAgentProxyCluster must not be called, got %+v", reg.added)
	}
}

func TestMaybeAutoRegister_SkipsWithoutKubeProxyCapability(t *testing.T) {
	reg := &fakeRegistrar{}
	got := maybeAutoRegisterCluster(reg, channel.NewAgentRegistry(), true, "c1", "x", []string{"metrics"})
	if got {
		t.Fatal("agent without kube-proxy capability must skip registration")
	}
	if len(reg.added) != 0 {
		t.Errorf("got unexpected AddAgentProxyCluster call: %+v", reg.added)
	}
}

func TestMaybeAutoRegister_NilRegistrarIsNoop(t *testing.T) {
	got := maybeAutoRegisterCluster(nil, channel.NewAgentRegistry(), true, "c1", "x", []string{"kube-proxy"})
	if got {
		t.Fatal("nil registrar must return false (no registration)")
	}
}

func TestMaybeAutoRegister_NilRegistrySkips(t *testing.T) {
	// Defensive guard: if the manager wasn't wired with a registry,
	// skipping is preferable to panicking.
	reg := &fakeRegistrar{}
	got := maybeAutoRegisterCluster(reg, nil, true, "c1", "x", []string{"kube-proxy"})
	if got {
		t.Fatal("nil registry should short-circuit")
	}
	if len(reg.added) != 0 {
		t.Errorf("AddAgentProxyCluster must not be called: %+v", reg.added)
	}
}

func TestMaybeAutoRegister_AddErrorReturnsFalse(t *testing.T) {
	reg := &fakeRegistrar{addErr: errSentinel("boom")}
	got := maybeAutoRegisterCluster(reg, channel.NewAgentRegistry(), true, "c1", "x", []string{"kube-proxy"})
	if got {
		t.Fatal("AddAgentProxyCluster error must surface as false (no cleanup defer scheduled)")
	}
}

func TestMaybeAutoUnregister_NoPeers_RemovesCluster(t *testing.T) {
	reg := &fakeRegistrar{}
	registry := channel.NewAgentRegistry()
	maybeAutoUnregisterCluster(reg, registry, "c-prod")
	if len(reg.removed) != 1 || reg.removed[0] != "c-prod" {
		t.Errorf("removed = %+v", reg.removed)
	}
}

func TestMaybeAutoUnregister_PeersRemain_KeepsCluster(t *testing.T) {
	// DaemonSet semantics: when one of N peer agents disconnects, the
	// cluster MUST stay registered as long as another peer is still
	// connected. Otherwise the manager would drop the cluster from
	// ListClusters every time any single pod restarted.
	reg := &fakeRegistrar{}
	registry := channel.NewAgentRegistry()
	registry.Register(channel.NewAgent("c-prod", "agent-other", "node-b", nil, nil))

	maybeAutoUnregisterCluster(reg, registry, "c-prod")
	if len(reg.removed) != 0 {
		t.Errorf("must keep cluster while peers remain, got removed = %+v", reg.removed)
	}
}

func TestMaybeAutoUnregister_NilRegistrarIsNoop(t *testing.T) {
	maybeAutoUnregisterCluster(nil, channel.NewAgentRegistry(), "c1") // must not panic
}

// errSentinel is a tiny error stand-in to keep test deps minimal.
type errSentinel string

func (e errSentinel) Error() string { return string(e) }

func TestHasCapability(t *testing.T) {
	cases := []struct {
		caps []string
		want string
		ok   bool
	}{
		{[]string{"metrics", "kube-proxy"}, "kube-proxy", true},
		{[]string{"metrics"}, "kube-proxy", false},
		{nil, "kube-proxy", false},
		{[]string{}, "kube-proxy", false},
	}
	for _, tc := range cases {
		if got := hasCapability(tc.caps, tc.want); got != tc.ok {
			t.Errorf("hasCapability(%v, %q) = %v, want %v", tc.caps, tc.want, got, tc.ok)
		}
	}
}
