package agent

import (
	"sync"
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/agent/channel"
	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"
)

// swapScheduleAfter replaces the package timer seam for the duration of a test
// (finding #71) and returns a restore func. Lets a test capture or short-circuit
// the deferred re-check instead of waiting out demotionGraceWindow.
func swapScheduleAfter(fn func(time.Duration, func())) func() {
	old := scheduleAfter
	scheduleAfter = fn
	return func() { scheduleAfter = old }
}

// fakeRegistrar captures calls so tests can assert on them. Methods
// are concurrency-safe in case the helper later runs from multiple
// goroutines.
type fakeRegistrar struct {
	mu            sync.Mutex
	added         []addCall
	metricsAdded  []addCall
	removed       []string
	addErr        error
	proxyClusters map[string]bool // clusterID -> currently agent-proxy (IsAgentProxyCluster)
	// becomesProxy: a proxy-capable agent's registration lands between the
	// caller's IsAgentProxyCluster look and its write (finding #71's residual race).
	becomesProxy map[string]bool
	// deleted: the operator removed the cluster (DemoteRegisteredCluster refuses).
	deleted map[string]bool
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

func (r *fakeRegistrar) AddMetricsOnlyClusterUnlessProxy(clusterID, displayName string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.addErr != nil {
		return false, r.addErr
	}
	if r.proxyClusters[clusterID] || r.becomesProxy[clusterID] {
		return false, nil
	}
	r.metricsAdded = append(r.metricsAdded, addCall{clusterID, displayName})
	return true, nil
}

func (r *fakeRegistrar) DemoteRegisteredCluster(clusterID, displayName string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.addErr != nil {
		return false, r.addErr
	}
	if r.deleted[clusterID] {
		return false, nil
	}
	r.metricsAdded = append(r.metricsAdded, addCall{clusterID, displayName})
	return true, nil
}

func (r *fakeRegistrar) RemoveAgentProxyCluster(clusterID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.removed = append(r.removed, clusterID)
}

func (r *fakeRegistrar) IsAgentProxyCluster(clusterID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.proxyClusters[clusterID]
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

func TestMaybeAutoRegister_MetricsOnlyRegistersAsMonitored(t *testing.T) {
	// An agent WITHOUT kube-proxy is no longer skipped — it registers as a metrics-only
	// cluster (surfaced monitored-only, no connector) so the cluster isn't invisible.
	// AddAgentProxyCluster must NOT fire (there's no connector); it lands via
	// AddMetricsOnlyCluster instead.
	reg := &fakeRegistrar{}
	got := maybeAutoRegisterCluster(reg, channel.NewAgentRegistry(), true, "c1", "x", []string{"metrics"}, "")
	if !got {
		t.Fatal("metrics-only agent must register (as monitored-only), not skip")
	}
	if len(reg.added) != 0 {
		t.Errorf("AddAgentProxyCluster must NOT be called for a metrics-only agent, got %+v", reg.added)
	}
	if len(reg.metricsAdded) != 1 || reg.metricsAdded[0].clusterID != "c1" {
		t.Errorf("expected one AddMetricsOnlyCluster call for c1, got %+v", reg.metricsAdded)
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

// TestMaybeAutoRegister_CapabilityLessAgentDoesNotDemoteProxyCapableCluster pins
// finding #41. A DaemonSet puts a proxy-capable agent on every node; a Mode C
// (promread) Deployment adds one more that advertises NO capabilities. In
// production 2026-08-19 that single pod registered one second after the DaemonSet
// and its AddMetricsOnlyCluster deleted the cluster's agent-proxy context, so
// every subsequent request 403'd with "you do not have access to this cluster" —
// on a cluster whose owner was an org admin AND a member of the owning team.
//
// The cluster's mode is the UNION over its live agents, not the opinion of
// whichever one registered last.
func TestMaybeAutoRegister_CapabilityLessAgentDoesNotDemoteProxyCapableCluster(t *testing.T) {
	reg := &fakeRegistrar{}
	registry := channel.NewAgentRegistry()

	// The DaemonSet is already up: one proxy-capable agent is live for this cluster.
	registry.Register(channel.NewAgent(
		"c-eks", "ds-node-1", "ip-10-0-36-79",
		&auth.AgentIdentity{TenantID: "org-1", Mode: auth.ModeIngestToken},
		[]string{"metrics", "kube-proxy"}, nil,
	))

	// Now the promread pod says Hello with no capabilities at all.
	got := maybeAutoRegisterCluster(reg, registry, true, "c-eks", "yagan-eks-prod",
		nil, "")

	if len(reg.metricsAdded) != 0 {
		t.Fatalf("cluster was demoted to metrics-only while a proxy-capable agent is live: %+v", reg.metricsAdded)
	}
	if got {
		t.Errorf("maybeAutoRegisterCluster = true; a no-op re-registration should report false")
	}
}

// The other direction must keep working: with no proxy-capable agent anywhere,
// a capability-less agent still registers the cluster as metrics-only.
func TestMaybeAutoRegister_DemotesWhenNoProxyCapableAgentExists(t *testing.T) {
	reg := &fakeRegistrar{}
	registry := channel.NewAgentRegistry()

	// A metrics-only sibling is live, but it cannot proxy either.
	registry.Register(channel.NewAgent(
		"c-metrics", "sibling", "node-b",
		&auth.AgentIdentity{TenantID: "org-1", Mode: auth.ModeIngestToken},
		[]string{"metrics"}, nil,
	))

	got := maybeAutoRegisterCluster(reg, registry, true, "c-metrics", "metrics-cluster",
		[]string{"metrics"}, "")

	if !got {
		t.Fatal("a cluster with no proxy-capable agent should register as metrics-only")
	}
	if len(reg.metricsAdded) != 1 {
		t.Errorf("expected exactly one AddMetricsOnlyCluster call, got %+v", reg.metricsAdded)
	}
	if len(reg.added) != 0 {
		t.Errorf("must not register agent-proxy for a capability-less agent: %+v", reg.added)
	}
}

// (The EE build adds a third case here: another org's proxy-capable agent on the
// same cluster_id must not keep this org's cluster out of metrics-only. OSS is
// single-tenant — HasProxyCapableAgent is called with an empty tenant and the
// union spans every live agent of the cluster — so that case does not exist.)

// finding #71: after an API restart every agent of a cluster reconnects at once.
// A capability-less (promread) agent sees no proxy-capable agent in the registry
// — the DaemonSet agents are mid-handshake, not Registered yet
// (maybeAutoRegisterCluster runs BEFORE registry.Register) — but the cluster IS
// agent-proxy at this instant (boot restore registered it). It must NOT demote
// now; it defers and re-checks after the burst settles, so a proxy-capable agent
// that registers during the window cancels the demotion.
func TestMaybeAutoRegister_DefersDemotionWhileClusterIsAgentProxy(t *testing.T) {
	var captured func()
	restore := swapScheduleAfter(func(_ time.Duration, fn func()) { captured = fn })
	defer restore()

	reg := &fakeRegistrar{proxyClusters: map[string]bool{"c-eks": true}}
	registry := channel.NewAgentRegistry() // empty: no proxy-capable agent live yet

	got := maybeAutoRegisterCluster(reg, registry, true, "c-eks", "yagan-eks-prod",
		[]string{"metrics"}, "")

	if got {
		t.Error("deferring a demotion must report false (no synchronous metrics-only registration)")
	}
	if len(reg.metricsAdded) != 0 {
		t.Fatalf("cluster must NOT be demoted synchronously while it is agent-proxy: %+v", reg.metricsAdded)
	}
	if captured == nil {
		t.Fatal("a deferred re-check must have been scheduled")
	}
	// The DaemonSet agents finish their handshake during the grace window.
	registry.Register(channel.NewAgent(
		"c-eks", "ds-node-1", "ip-10-0-36-79",
		&auth.AgentIdentity{TenantID: "org-1", Mode: auth.ModeIngestToken},
		[]string{"metrics", "kube-proxy"}, nil,
	))
	captured() // grace fires
	if len(reg.metricsAdded) != 0 {
		t.Errorf("demotion must be cancelled once a proxy-capable agent registered during the grace window: %+v", reg.metricsAdded)
	}
}

// finding #71: if the grace window passes with still no proxy-capable agent, the
// cluster genuinely has none — demote it then.
func TestMaybeAutoRegister_DemotesAfterGraceWhenStillNoProxy(t *testing.T) {
	var captured func()
	restore := swapScheduleAfter(func(_ time.Duration, fn func()) { captured = fn })
	defer restore()

	reg := &fakeRegistrar{proxyClusters: map[string]bool{"c-eks": true}}
	registry := channel.NewAgentRegistry()

	maybeAutoRegisterCluster(reg, registry, true, "c-eks", "eks", []string{"metrics"}, "")
	if len(reg.metricsAdded) != 0 {
		t.Fatalf("must not demote synchronously: %+v", reg.metricsAdded)
	}
	if captured == nil {
		t.Fatal("a deferred re-check must have been scheduled")
	}
	captured() // grace fires, still no proxy-capable agent
	if len(reg.metricsAdded) != 1 || reg.metricsAdded[0].clusterID != "c-eks" {
		t.Errorf("expected a metrics-only registration after the grace window, got %+v", reg.metricsAdded)
	}
}

// A cluster that was never agent-proxy (a genuine metrics-only install, or a
// promread-only cluster) must surface at once — no grace. A later proxy-capable
// agent still promotes it (AddAgentProxyCluster clears the metrics-only entry).
// The grace only protects a cluster that is agent-proxy right now (finding #71).
func TestMaybeAutoRegister_NewMetricsOnlyInstallRegistersImmediately(t *testing.T) {
	scheduled := false
	restore := swapScheduleAfter(func(_ time.Duration, _ func()) { scheduled = true })
	defer restore()

	reg := &fakeRegistrar{} // proxyClusters nil: cluster is NOT agent-proxy
	got := maybeAutoRegisterCluster(reg, channel.NewAgentRegistry(), true, "c-new", "new",
		[]string{"metrics"}, "")

	if !got {
		t.Error("a brand-new metrics-only cluster must register immediately (report true)")
	}
	if scheduled {
		t.Error("a never-agent-proxy cluster must not defer — no grace window")
	}
	if len(reg.metricsAdded) != 1 {
		t.Errorf("expected one immediate AddMetricsOnlyCluster, got %+v", reg.metricsAdded)
	}
}

// The operator deletes the cluster during the grace window: the deferred
// demotion must not bring it back as a monitored-only cluster with no agent.
func TestMaybeAutoRegister_DeferredDemotionDropsADeletedCluster(t *testing.T) {
	var captured func()
	restore := swapScheduleAfter(func(_ time.Duration, fn func()) { captured = fn })
	defer restore()

	reg := &fakeRegistrar{proxyClusters: map[string]bool{"c-eks": true}}
	maybeAutoRegisterCluster(reg, channel.NewAgentRegistry(), true, "c-eks", "eks", []string{"metrics"}, "")
	if captured == nil {
		t.Fatal("a deferred re-check must have been scheduled")
	}
	reg.deleted = map[string]bool{"c-eks": true} // DELETE /clusters/by-id during the window
	captured()
	if len(reg.metricsAdded) != 0 {
		t.Errorf("a cluster deleted during the grace window came back as metrics-only: %+v", reg.metricsAdded)
	}
}

// A proxy-capable agent's registration lands between the capability-less
// agent's look (not agent-proxy) and its write: the write must not overwrite it
// — it falls back to the deferred re-check instead (finding #71's residual race).
func TestMaybeAutoRegister_ProxyRegistrationInTheWindowIsNotOverwritten(t *testing.T) {
	scheduled := false
	restore := swapScheduleAfter(func(_ time.Duration, _ func()) { scheduled = true })
	defer restore()

	reg := &fakeRegistrar{becomesProxy: map[string]bool{"c-new": true}}
	got := maybeAutoRegisterCluster(reg, channel.NewAgentRegistry(), true, "c-new", "new", []string{"metrics"}, "")
	if got {
		t.Error("the cluster turned agent-proxy: the capability-less agent must not report a registration")
	}
	if len(reg.metricsAdded) != 0 {
		t.Errorf("the demotion overwrote a proxy-capable registration: %+v", reg.metricsAdded)
	}
	if !scheduled {
		t.Error("the race must fall back to the deferred re-check")
	}
}
