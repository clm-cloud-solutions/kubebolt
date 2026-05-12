package agent

import (
	"sync"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/agent/channel"
	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"
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
	got := maybeAutoRegisterCluster(reg, registry, true, "c-prod", "Prod EU", []string{"metrics", "kube-proxy"}, "")
	if !got {
		t.Fatal("registration should have happened")
	}
	if len(reg.added) != 1 || reg.added[0].clusterID != "c-prod" || reg.added[0].displayName != "Prod EU" {
		t.Errorf("added = %+v", reg.added)
	}
}

func TestAutoRegisterDisplayName_AppendsSuffix(t *testing.T) {
	// Pin the disambiguation contract: agent-proxy clusters MUST be
	// visually distinguishable from kubeconfig contexts in the listing,
	// even when they target the same physical cluster.
	cases := []struct {
		hello    *agentv2.Hello
		clusterID string
		want     string
	}{
		{
			// Honors the cluster_name label when present.
			hello:     &agentv2.Hello{Labels: map[string]string{"kubebolt.io/cluster-name": "kind-kubebolt-dev"}},
			clusterID: "local",
			want:      "kind-kubebolt-dev (via agent)",
		},
		{
			// Falls back to the cluster_id when label is missing.
			hello:     &agentv2.Hello{},
			clusterID: "local",
			want:      "local (via agent)",
		},
		{
			// Nil Hello tolerated (defensive — shouldn't happen in
			// production but the helper must not panic).
			hello:     nil,
			clusterID: "abc123",
			want:      "abc123 (via agent)",
		},
	}
	for _, tc := range cases {
		got := autoRegisterDisplayName(tc.hello, tc.clusterID)
		if got != tc.want {
			t.Errorf("displayName(%v, %q) = %q, want %q", tc.hello, tc.clusterID, got, tc.want)
		}
	}
}

func TestMaybeAutoRegister_SkipsWhenFlagOff(t *testing.T) {
	reg := &fakeRegistrar{}
	registry := channel.NewAgentRegistry()
	got := maybeAutoRegisterCluster(reg, registry, false, "c1", "x", []string{"kube-proxy"}, "")
	if got {
		t.Fatal("flag=false must skip registration")
	}
	if len(reg.added) != 0 {
		t.Errorf("AddAgentProxyCluster must not be called, got %+v", reg.added)
	}
}

func TestMaybeAutoRegister_SkipsWithoutKubeProxyCapability(t *testing.T) {
	reg := &fakeRegistrar{}
	got := maybeAutoRegisterCluster(reg, channel.NewAgentRegistry(), true, "c1", "x", []string{"metrics"}, "")
	if got {
		t.Fatal("agent without kube-proxy capability must skip registration")
	}
	if len(reg.added) != 0 {
		t.Errorf("got unexpected AddAgentProxyCluster call: %+v", reg.added)
	}
}

func TestMaybeAutoRegister_NilRegistrarIsNoop(t *testing.T) {
	got := maybeAutoRegisterCluster(nil, channel.NewAgentRegistry(), true, "c1", "x", []string{"kube-proxy"}, "")
	if got {
		t.Fatal("nil registrar must return false (no registration)")
	}
}

func TestMaybeAutoRegister_NilRegistrySkips(t *testing.T) {
	// Defensive guard: if the manager wasn't wired with a registry,
	// skipping is preferable to panicking.
	reg := &fakeRegistrar{}
	got := maybeAutoRegisterCluster(reg, nil, true, "c1", "x", []string{"kube-proxy"}, "")
	if got {
		t.Fatal("nil registry should short-circuit")
	}
	if len(reg.added) != 0 {
		t.Errorf("AddAgentProxyCluster must not be called: %+v", reg.added)
	}
}

func TestMaybeAutoRegister_AddErrorReturnsFalse(t *testing.T) {
	reg := &fakeRegistrar{addErr: errSentinel("boom")}
	got := maybeAutoRegisterCluster(reg, channel.NewAgentRegistry(), true, "c1", "x", []string{"kube-proxy"}, "")
	if got {
		t.Fatal("AddAgentProxyCluster error must surface as false (no cleanup defer scheduled)")
	}
}

// IsSelfCluster is the shared rule pinning the self-skip contract for
// every site that calls into the cluster manager's AddAgentProxyCluster.
// Both maybeAutoRegisterCluster (live-connect path) and the boot-time
// restore loop in cmd/server/main.go use it — see cluster-validation
// BUG-2 (live) and BUG-3 (boot) for the empirical bugs the helper
// prevents.
func TestIsSelfCluster(t *testing.T) {
	cases := []struct {
		name           string
		clusterID      string
		selfClusterID  string
		want           bool
	}{
		{"both empty → false (feature gated off)", "", "", false},
		{"selfClusterID empty → false regardless of clusterID", "any-id", "", false},
		{"clusterID empty + selfClusterID set → false", "", "self", false},
		{"matching IDs → true", "abc-123", "abc-123", true},
		{"different IDs → false", "abc-123", "xyz-789", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsSelfCluster(tc.clusterID, tc.selfClusterID); got != tc.want {
				t.Errorf("IsSelfCluster(%q, %q) = %v, want %v", tc.clusterID, tc.selfClusterID, got, tc.want)
			}
		})
	}
}

// BUG-2 regression (internal/cluster-validation/sessions/00-humo-test/10).
// When the backend runs in the same cluster as a connecting agent
// (single-cluster self-hosted topology — the obvious happy-path of OSS),
// the in-cluster kubeconfig context already exposes that cluster. An
// agent-proxy registration for the SAME cluster would surface the
// cluster TWICE in the UI selector. Skip the registration to keep the
// listing 1:1.
func TestMaybeAutoRegister_SkipsWhenClusterIDEqualsSelfClusterID(t *testing.T) {
	reg := &fakeRegistrar{}
	registry := channel.NewAgentRegistry()
	const self = "abc-123"
	got := maybeAutoRegisterCluster(reg, registry, true, self, "same-as-backend", []string{"kube-proxy"}, self)
	if got {
		t.Fatal("registration must be skipped when cluster_id == selfClusterID")
	}
	if len(reg.added) != 0 {
		t.Errorf("AddAgentProxyCluster must NOT be called (got %d call(s))", len(reg.added))
	}
}

// Backwards compatibility — empty selfClusterID disables the self-skip
// branch, preserving prior behavior for backends that run out-of-cluster
// (kubeconfig-on-disk dev path) or where in-cluster cluster_id discovery
// failed at boot.
func TestMaybeAutoRegister_RegistersWhenSelfClusterIDEmpty(t *testing.T) {
	reg := &fakeRegistrar{}
	registry := channel.NewAgentRegistry()
	// Same cluster_id as the would-be-self ("abc-123") but selfClusterID
	// is empty — the gate doesn't fire, registration proceeds.
	got := maybeAutoRegisterCluster(reg, registry, true, "abc-123", "kind", []string{"kube-proxy"}, "")
	if !got {
		t.Fatal("registration must proceed when selfClusterID is empty (feature gated off)")
	}
	if len(reg.added) != 1 {
		t.Errorf("expected exactly one AddAgentProxyCluster call, got %d", len(reg.added))
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
