package integrations

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
)

// promPodReady builds a Prometheus-like pod ready for inclusion in
// a fake clientset. ns + version are configurable; the readiness
// condition matches what isPodReady looks for.
func promPodReady(ns, name, version string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels: map[string]string{
				"app.kubernetes.io/name":    "prometheus",
				"app.kubernetes.io/version": version,
			},
		},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
		},
	}
}

// promPodNotReady is the same but with the Ready condition False —
// drives the Degraded path even when the pod is otherwise present.
func promPodNotReady(ns, name, version string) *corev1.Pod {
	pod := promPodReady(ns, name, version)
	pod.Status.Conditions[0].Status = corev1.ConditionFalse
	return pod
}

// mockTenantsLister stands in for auth.TenantsStore in tests so we
// can drive Detect through every tier without spinning up BoltDB.
type mockTenantsLister struct {
	tenants []auth.Tenant
	err     error
}

func (m *mockTenantsLister) ListTenants() ([]auth.Tenant, error) {
	return m.tenants, m.err
}

// tokenAt is a small helper that builds an IngestToken with a given
// label and last-used age. Negative ageFromNow means "not used yet"
// (LastUsedAt = nil) — distinct from a zero time.
func tokenAt(label string, ageFromNow time.Duration) auth.IngestToken {
	tok := auth.IngestToken{
		ID:        "tok-" + label,
		Hash:      "deadbeef",
		Prefix:    "kb_",
		Label:     label,
		CreatedAt: time.Now().Add(-24 * time.Hour),
	}
	if ageFromNow >= 0 {
		last := time.Now().Add(-ageFromNow)
		tok.LastUsedAt = &last
	}
	return tok
}

func TestPrometheusProvider_Meta(t *testing.T) {
	p := NewPrometheus(&mockTenantsLister{}, nil)
	m := p.Meta()
	if m.ID != PrometheusID {
		t.Errorf("Meta().ID = %q, want %q", m.ID, PrometheusID)
	}
	if m.Name != PrometheusName {
		t.Errorf("Meta().Name = %q, want %q", m.Name, PrometheusName)
	}
	// Capabilities must include the scraped+historical pair so the UI
	// can resolve "this integration delivers metrics" without parsing
	// the description.
	if len(m.Capabilities) < 2 {
		t.Errorf("Meta().Capabilities = %v, want at least 2 entries", m.Capabilities)
	}
}

func TestPrometheusProvider_Detect(t *testing.T) {
	tests := []struct {
		name               string
		tenants            []auth.Tenant
		err                error
		wantStatus         Status
		wantMessageContain string // substring match
	}{
		{
			name:               "store error → unknown with reason",
			err:                errors.New("boltdb closed"),
			wantStatus:         StatusUnknown,
			wantMessageContain: "boltdb closed",
		},
		{
			name:               "no tenants at all → not installed, no tokens",
			tenants:            nil,
			wantStatus:         StatusNotInstalled,
			wantMessageContain: "No Prometheus detected in cluster",
		},
		{
			name: "tokens issued but never used → not installed, hint to configure",
			tenants: []auth.Tenant{
				{
					ID:           "t1",
					Name:         "default",
					IngestTokens: []auth.IngestToken{tokenAt("never-used", -1)},
				},
			},
			wantStatus:         StatusNotInstalled,
			wantMessageContain: "no Prometheus pushing yet",
		},
		{
			name: "token used 10s ago → streaming",
			tenants: []auth.Tenant{
				{
					ID:           "t1",
					Name:         "default",
					IngestTokens: []auth.IngestToken{tokenAt("prom-1", 10*time.Second)},
				},
			},
			wantStatus:         StatusInstalled,
			wantMessageContain: "Streaming",
		},
		{
			name: "token used 5m ago → stale (still installed)",
			tenants: []auth.Tenant{
				{
					ID:           "t1",
					Name:         "default",
					IngestTokens: []auth.IngestToken{tokenAt("prom-1", 5*time.Minute)},
				},
			},
			wantStatus:         StatusInstalled,
			wantMessageContain: "Stale",
		},
		{
			name: "token used 1h ago → cold (degraded)",
			tenants: []auth.Tenant{
				{
					ID:           "t1",
					Name:         "default",
					IngestTokens: []auth.IngestToken{tokenAt("prom-1", 1*time.Hour)},
				},
			},
			wantStatus:         StatusDegraded,
			wantMessageContain: "Cold",
		},
		{
			name: "two senders, both fresh → streaming + active senders count",
			tenants: []auth.Tenant{
				{
					ID:   "t1",
					Name: "default",
					IngestTokens: []auth.IngestToken{
						tokenAt("prom-a", 10*time.Second),
						tokenAt("prom-b", 5*time.Second),
					},
				},
			},
			wantStatus:         StatusInstalled,
			wantMessageContain: "2 active senders",
		},
		{
			name: "revoked token ignored — only active count",
			tenants: []auth.Tenant{
				{
					ID:   "t1",
					Name: "default",
					IngestTokens: []auth.IngestToken{
						// Revoked: doesn't count as active, even if recently used.
						func() auth.IngestToken {
							tok := tokenAt("revoked", 10*time.Second)
							rev := time.Now().Add(-1 * time.Hour)
							tok.RevokedAt = &rev
							return tok
						}(),
					},
				},
			},
			// Revoked is excluded from activeTokens → 0 tokens, 0 workload signal.
			wantStatus:         StatusNotInstalled,
			wantMessageContain: "No Prometheus detected in cluster",
		},
		{
			// Regression for the bug we hit in v1.10 capture: a token
			// used long ago (e.g. another cluster from past sessions)
			// must NOT inflate the "active senders" count. The status
			// is still driven by the most-recent token (here, the
			// fresh one wins → Streaming). With cs=nil (no workload
			// signal), the message uses the "external source" phrasing
			// — that's the advanced cross-cluster path, the correct
			// branch when no in-cluster Prom is detectable.
			name: "old token does not inflate active senders count",
			tenants: []auth.Tenant{
				{
					ID:   "t1",
					Name: "default",
					IngestTokens: []auth.IngestToken{
						tokenAt("old-other-cluster", 18*24*time.Hour), // 18 days ago
						tokenAt("prom-local", 10*time.Second),         // streaming now
					},
				},
			},
			wantStatus: StatusInstalled,
			// 5a.1.c — single-tenant store drops the "default/" prefix
			// from the source label. Only one tenant exists in this
			// test fixture, so the message renders "from prom-local"
			// rather than "from default/prom-local".
			wantMessageContain: "Streaming from external source · last sample 10s ago from prom-local",
		},
		{
			// Inverse case: the most-recent token IS old. Status is
			// driven by it (Cold), and active senders count drops to
			// 0 — no contradictory "Cold + N active senders" message.
			name: "cold most-recent + no fresh senders → no inflated count",
			tenants: []auth.Tenant{
				{
					ID:   "t1",
					Name: "default",
					IngestTokens: []auth.IngestToken{
						tokenAt("a", 1*time.Hour),
						tokenAt("b", 3*time.Hour),
					},
				},
			},
			wantStatus: StatusDegraded,
			// Just verify the absence of "active senders" suffix when
			// every token is outside the stale window. The Cold prefix
			// is already covered by the "1h ago" test above.
			wantMessageContain: "Cold",
		},
	}

	// Belt-and-suspenders for the regression: assert the new test
	// case ALSO does not have "active senders" suffix (the substring
	// check above is positive-only). Run the inverse-cold case
	// separately so we can assert what's NOT in the message.
	t.Run("cold case has no active-senders suffix", func(t *testing.T) {
		lister := &mockTenantsLister{tenants: []auth.Tenant{
			{
				ID:   "t1",
				Name: "default",
				IngestTokens: []auth.IngestToken{
					tokenAt("a", 1*time.Hour),
					tokenAt("b", 3*time.Hour),
				},
			},
		}}
		p := NewPrometheus(lister, nil)
		got, _ := p.Detect(context.Background(), nil)
		if got.Health == nil {
			t.Fatal("Health is nil")
		}
		if strings.Contains(got.Health.Message, "active senders") {
			t.Errorf("Health.Message contains 'active senders' for cold-only state — should be omitted: %q", got.Health.Message)
		}
	})

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lister := &mockTenantsLister{tenants: tc.tenants, err: tc.err}
			p := NewPrometheus(lister, nil)
			got, err := p.Detect(context.Background(), nil /* k8s client unused */)
			if err != nil {
				t.Fatalf("Detect() returned error: %v (Detect should never return errors — surface them via Status=Unknown)", err)
			}
			if got.Status != tc.wantStatus {
				t.Errorf("Status = %q, want %q", got.Status, tc.wantStatus)
			}
			if tc.wantMessageContain != "" {
				if got.Health == nil {
					t.Fatalf("Health is nil; want message containing %q", tc.wantMessageContain)
				}
				if !strings.Contains(got.Health.Message, tc.wantMessageContain) {
					t.Errorf("Health.Message = %q, want substring %q", got.Health.Message, tc.wantMessageContain)
				}
			}
		})
	}
}

// TestPrometheusProvider_Detect_WithWorkload covers the in-cluster
// Prom branches of the combination matrix — the 80/20 default the
// rest of the test cases above don't exercise because they use cs=nil
// to drive the heartbeat-only branches. Each case here wires a fake
// clientset with Prom pods present and asserts the Namespace,
// Version, and Pods Ready/Desired surface correctly.
func TestPrometheusProvider_Detect_WithWorkload(t *testing.T) {
	tests := []struct {
		name               string
		objs               []*corev1.Pod
		tenants            []auth.Tenant
		wantStatus         Status
		wantNamespace      string
		wantVersion        string
		wantPodsReady      int
		wantPodsDesired    int
		wantMessageContain string
	}{
		{
			// Common case: kube-prometheus-stack running + an active
			// Prom pushing samples right now.
			name: "pod ready + fresh heartbeat → Installed (Healthy)",
			objs: []*corev1.Pod{
				promPodReady("monitoring", "prometheus-0", "3.11.3"),
			},
			tenants: []auth.Tenant{
				{
					ID:           "t1",
					Name:         "default",
					IngestTokens: []auth.IngestToken{tokenAt("prom-local", 10*time.Second)},
				},
			},
			wantStatus:         StatusInstalled,
			wantNamespace:      "monitoring",
			wantVersion:        "3.11.3",
			wantPodsReady:      1,
			wantPodsDesired:    1,
			wantMessageContain: "Streaming",
		},
		{
			// Pod present but a replica isn't Ready — degraded
			// regardless of heartbeat health. We carry the heartbeat
			// info in the message because the operator wants to see
			// both signals at once.
			name: "pod not ready + fresh heartbeat → Degraded",
			objs: []*corev1.Pod{
				promPodReady("monitoring", "prometheus-0", "3.11.3"),
				promPodNotReady("monitoring", "prometheus-1", "3.11.3"),
			},
			tenants: []auth.Tenant{
				{
					ID:           "t1",
					Name:         "default",
					IngestTokens: []auth.IngestToken{tokenAt("prom-local", 10*time.Second)},
				},
			},
			wantStatus:      StatusDegraded,
			wantNamespace:   "monitoring",
			wantPodsReady:   1,
			wantPodsDesired: 2,
		},
		{
			// Operator installed Prom but hasn't wired remote_write
			// yet — actionable next step in the message.
			name: "pod ready + no heartbeat → Degraded (not configured)",
			objs: []*corev1.Pod{
				promPodReady("monitoring", "prometheus-0", "3.11.3"),
			},
			tenants: []auth.Tenant{
				{
					ID:           "t1",
					Name:         "default",
					IngestTokens: []auth.IngestToken{tokenAt("never-used", -1)},
				},
			},
			wantStatus:         StatusDegraded,
			wantNamespace:      "monitoring",
			wantPodsReady:      1,
			wantPodsDesired:    1,
			wantMessageContain: "not configured for KubeBolt",
		},
		{
			// Pod stale (heartbeat too old) drops below the stale
			// window, even though pod itself is Ready — the receiver
			// hasn't been seeing samples, that's degraded UX.
			name: "pod ready + cold heartbeat → Degraded",
			objs: []*corev1.Pod{
				promPodReady("monitoring", "prometheus-0", "3.11.3"),
			},
			tenants: []auth.Tenant{
				{
					ID:           "t1",
					Name:         "default",
					IngestTokens: []auth.IngestToken{tokenAt("prom-local", 1*time.Hour)},
				},
			},
			wantStatus:         StatusDegraded,
			wantNamespace:      "monitoring",
			wantMessageContain: "Cold",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Build a fake clientset preloaded with the pods this
			// case needs. kubernetes.Interface is the Provider's
			// dependency; fake.NewSimpleClientset() conforms to it.
			objs := make([]runtime.Object, 0, len(tc.objs))
			for _, p := range tc.objs {
				objs = append(objs, p)
			}
			cs := fake.NewSimpleClientset(objs...)
			lister := &mockTenantsLister{tenants: tc.tenants}
			p := NewPrometheus(lister, nil)

			got, err := p.Detect(context.Background(), kubernetes.Interface(cs))
			if err != nil {
				t.Fatalf("Detect() returned error: %v", err)
			}
			if got.Status != tc.wantStatus {
				t.Errorf("Status = %q, want %q", got.Status, tc.wantStatus)
			}
			if tc.wantNamespace != "" && got.Namespace != tc.wantNamespace {
				t.Errorf("Namespace = %q, want %q", got.Namespace, tc.wantNamespace)
			}
			if tc.wantVersion != "" && got.Version != tc.wantVersion {
				t.Errorf("Version = %q, want %q", got.Version, tc.wantVersion)
			}
			if got.Health == nil {
				t.Fatalf("Health is nil")
			}
			if tc.wantPodsDesired > 0 && got.Health.PodsDesired != tc.wantPodsDesired {
				t.Errorf("Health.PodsDesired = %d, want %d", got.Health.PodsDesired, tc.wantPodsDesired)
			}
			if tc.wantPodsReady > 0 && got.Health.PodsReady != tc.wantPodsReady {
				t.Errorf("Health.PodsReady = %d, want %d", got.Health.PodsReady, tc.wantPodsReady)
			}
			if tc.wantMessageContain != "" && !strings.Contains(got.Health.Message, tc.wantMessageContain) {
				t.Errorf("Health.Message = %q, want substring %q", got.Health.Message, tc.wantMessageContain)
			}
		})
	}
}

// TestPrometheusProvider_Detect_ClusterScope exercises the
// per-token cluster filter (5a.1.a). Tokens whose ClusterID matches
// the active cluster pass; tokens scoped to another cluster are
// silently dropped before the tier / freshness logic sees them;
// unscoped tokens (ClusterID == "") always pass for backward-compat
// with legacy issued-before-this-field tokens.
func TestPrometheusProvider_Detect_ClusterScope(t *testing.T) {
	const (
		activeClusterID = "active-cluster-uid"
		otherClusterID  = "other-cluster-uid"
	)

	tests := []struct {
		name               string
		tokens             []auth.IngestToken
		currentClusterID   string
		wantStatus         Status
		wantMessageContain string
		wantMessageOmits   string
	}{
		{
			name: "scoped to active cluster → counted",
			tokens: []auth.IngestToken{
				func() auth.IngestToken {
					tok := tokenAt("active-prom", 10*time.Second)
					tok.ClusterID = activeClusterID
					return tok
				}(),
			},
			currentClusterID:   activeClusterID,
			wantStatus:         StatusInstalled,
			wantMessageContain: "Streaming",
		},
		{
			name: "scoped to other cluster → filtered out (NotInstalled)",
			tokens: []auth.IngestToken{
				func() auth.IngestToken {
					tok := tokenAt("other-prom", 10*time.Second)
					tok.ClusterID = otherClusterID
					return tok
				}(),
			},
			currentClusterID: activeClusterID,
			wantStatus:       StatusNotInstalled,
			wantMessageOmits: "other-prom",
		},
		{
			name: "unscoped token (legacy) → passes through",
			tokens: []auth.IngestToken{
				tokenAt("legacy-prom", 10*time.Second), // no ClusterID set
			},
			currentClusterID:   activeClusterID,
			wantStatus:         StatusInstalled,
			wantMessageContain: "legacy-prom",
		},
		{
			name: "empty currentClusterID disables filter (every token visible)",
			tokens: []auth.IngestToken{
				func() auth.IngestToken {
					tok := tokenAt("other-prom", 10*time.Second)
					tok.ClusterID = otherClusterID
					return tok
				}(),
			},
			currentClusterID:   "",
			wantStatus:         StatusInstalled,
			wantMessageContain: "other-prom",
		},
		{
			name: "mixed: active + other cluster → only active counted",
			tokens: []auth.IngestToken{
				func() auth.IngestToken {
					tok := tokenAt("active-prom", 5*time.Second)
					tok.ClusterID = activeClusterID
					return tok
				}(),
				func() auth.IngestToken {
					tok := tokenAt("other-prom", 1*time.Second) // would be most-recent if not filtered
					tok.ClusterID = otherClusterID
					return tok
				}(),
			},
			currentClusterID:   activeClusterID,
			wantStatus:         StatusInstalled,
			wantMessageContain: "active-prom",
			wantMessageOmits:   "other-prom",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lister := &mockTenantsLister{tenants: []auth.Tenant{
				{ID: "t1", Name: "default", IngestTokens: tc.tokens},
			}}
			p := NewPrometheus(lister, func() string { return tc.currentClusterID })
			got, err := p.Detect(context.Background(), nil)
			if err != nil {
				t.Fatalf("Detect() returned error: %v", err)
			}
			if got.Status != tc.wantStatus {
				t.Errorf("Status = %q, want %q", got.Status, tc.wantStatus)
			}
			if got.Health == nil {
				t.Fatalf("Health is nil")
			}
			if tc.wantMessageContain != "" && !strings.Contains(got.Health.Message, tc.wantMessageContain) {
				t.Errorf("Health.Message = %q, want substring %q", got.Health.Message, tc.wantMessageContain)
			}
			if tc.wantMessageOmits != "" && strings.Contains(got.Health.Message, tc.wantMessageOmits) {
				t.Errorf("Health.Message = %q, must NOT contain %q (other-cluster token leaked through filter)", got.Health.Message, tc.wantMessageOmits)
			}
		})
	}
}

// TestPrometheusProvider_Detect_AdaptiveSourceLabel exercises
// 5a.1.c — the source label drops the "tenant_name/" prefix in
// single-tenant stores (the self-hosted default case) so the
// message doesn't read like a K8s `namespace/pod` path, but
// keeps it in multi-tenant stores where the disambiguation
// matters.
func TestPrometheusProvider_Detect_AdaptiveSourceLabel(t *testing.T) {
	tests := []struct {
		name               string
		tenants            []auth.Tenant
		wantContains       string
		wantOmits          string
	}{
		{
			name: "single-tenant → bare token label",
			tenants: []auth.Tenant{
				{
					ID:           "t1",
					Name:         "default",
					IngestTokens: []auth.IngestToken{tokenAt("prom-local", 10*time.Second)},
				},
			},
			wantContains: "from prom-local",
			wantOmits:    "default/",
		},
		{
			name: "multi-tenant → keeps tenant prefix",
			tenants: []auth.Tenant{
				{
					ID:           "t1",
					Name:         "acme",
					IngestTokens: []auth.IngestToken{tokenAt("prom-acme", 10*time.Second)},
				},
				{
					ID:           "t2",
					Name:         "globex",
					IngestTokens: []auth.IngestToken{tokenAt("prom-globex", 30*time.Second)},
				},
			},
			wantContains: "from acme/prom-acme",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lister := &mockTenantsLister{tenants: tc.tenants}
			p := NewPrometheus(lister, nil)
			got, err := p.Detect(context.Background(), nil)
			if err != nil {
				t.Fatalf("Detect() returned error: %v", err)
			}
			if got.Health == nil {
				t.Fatalf("Health is nil")
			}
			if !strings.Contains(got.Health.Message, tc.wantContains) {
				t.Errorf("Health.Message = %q, want substring %q", got.Health.Message, tc.wantContains)
			}
			if tc.wantOmits != "" && strings.Contains(got.Health.Message, tc.wantOmits) {
				t.Errorf("Health.Message = %q, must NOT contain %q", got.Health.Message, tc.wantOmits)
			}
		})
	}
}

func TestHumanDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "30s"},
		{59 * time.Second, "59s"},
		{1 * time.Minute, "1m"},
		{59 * time.Minute, "59m"},
		{1 * time.Hour, "1h"},
		{23 * time.Hour, "23h"},
		{24 * time.Hour, "1d"},
		{72 * time.Hour, "3d"},
	}
	for _, c := range cases {
		got := humanDuration(c.d)
		if got != c.want {
			t.Errorf("humanDuration(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}
