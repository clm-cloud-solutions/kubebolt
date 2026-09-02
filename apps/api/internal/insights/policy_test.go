package insights

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubebolt/kubebolt/apps/api/internal/models"
	"github.com/kubebolt/kubebolt/apps/api/internal/websocket"
)

// TestPolicyCatalog_CoversAllRules is the anti-drift guard: the catalog and
// AllRules() must name exactly the same rule set, the D-3 partition must hold
// (15 malfunctions / 9 expectations), the shipped severities must agree, and
// the numeric set must be exactly the six rules #44 §3.1 enumerated. A rule
// added without a catalog entry — or a silent reclassification — fails here.
func TestPolicyCatalog_CoversAllRules(t *testing.T) {
	rules := AllRules()
	if len(rules) != len(PolicyCatalog) {
		t.Fatalf("catalog has %d entries, engine ships %d rules", len(PolicyCatalog), len(rules))
	}
	var malfunctions, expectations int
	numeric := map[string]bool{}
	for _, r := range rules {
		def, ok := PolicyCatalog[r.ID]
		if !ok {
			t.Errorf("rule %q has no PolicyCatalog entry", r.ID)
			continue
		}
		if def.Severity != r.Severity {
			t.Errorf("rule %q: catalog severity %q != shipped severity %q", r.ID, def.Severity, r.Severity)
		}
		switch def.Class {
		case ClassMalfunction:
			malfunctions++
		case ClassExpectation:
			expectations++
		default:
			t.Errorf("rule %q has invalid class %q", r.ID, def.Class)
		}
		if def.HasThreshold {
			numeric[r.ID] = true
			if def.Threshold <= 0 {
				t.Errorf("rule %q claims a threshold but default is %v", r.ID, def.Threshold)
			}
			if def.ThresholdLabel == "" {
				t.Errorf("rule %q has a threshold but no label for the UI", r.ID)
			}
		}
	}
	if malfunctions != 15 || expectations != 9 {
		t.Errorf("partition = %d/%d, want 15/9 (D-3)", malfunctions, expectations)
	}
	wantNumeric := []string{"crash-loop", "frequent-restarts", "cpu-throttle-risk", "memory-pressure", "resource-underrequest", "cert-expiring"}
	if len(numeric) != len(wantNumeric) {
		t.Errorf("numeric rules = %v, want exactly %v", numeric, wantNumeric)
	}
	for _, id := range wantNumeric {
		if !numeric[id] {
			t.Errorf("expected %q to carry a threshold", id)
		}
	}
}

func TestValidatePolicyChange_ClassContract(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	sv := func(s string) *string { return &s }

	cases := []struct {
		name      string
		rule      string
		threshold *float64
		severity  *string
		wantErr   bool
	}{
		{"malfunction threshold ok", "crash-loop", f(10), nil, false},
		{"malfunction severity locked", "crash-loop", nil, sv("info"), true},
		{"malfunction off forbidden", "zero-replicas", nil, sv("off"), true},
		{"boolean malfunction has no bar", "zero-replicas", f(2), nil, true},
		{"expectation severity ok", "service-no-endpoints", nil, sv("info"), false},
		{"expectation off ok", "pdb-no-match", nil, sv("off"), false},
		{"numeric expectation threshold ok", "memory-pressure", f(0.95), nil, false},
		{"numeric expectation both knobs", "cpu-throttle-risk", f(0.95), sv("info"), false},
		{"invalid severity value", "pdb-no-match", nil, sv("silent"), true},
		{"threshold must be positive", "crash-loop", f(0), nil, true},
		{"unknown rule", "made-up", f(1), nil, true},
		{"empty change", "crash-loop", nil, nil, true},
	}
	for _, tc := range cases {
		err := ValidatePolicyChange(tc.rule, tc.threshold, tc.severity)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err=%v, wantErr=%v", tc.name, err, tc.wantErr)
		}
	}
}

// crashLoopPod builds the minimal pod that trips crash-loop at the shipped
// threshold (>3 restarts in CrashLoopBackOff).
func crashLoopPod(restarts int32) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "boom", Namespace: "default"},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name:         "app",
			RestartCount: restarts,
			State:        corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
		}}},
	}
}

// TestEvaluate_ThresholdOverride pins the whole chain: SetPolicySource →
// snapshot on state → stateThreshold inside the real crash-loop rule. A pod
// at 5 restarts fires with the shipped bar (>3) and stays silent when the
// org raised the bar to 10 — #44 §3.1's "the third restart of a container
// someone is actively rebuilding".
func TestEvaluate_ThresholdOverride(t *testing.T) {
	t.Cleanup(func() { SetPolicySource(nil) })

	eval := func() []models.Insight {
		e := NewEngine(websocket.NewHub(), nil, "cluster", "org-x")
		e.Evaluate(&ClusterState{Pods: []*corev1.Pod{crashLoopPod(5)}})
		return e.GetAllInsights()
	}

	SetPolicySource(nil)
	fired := false
	for _, in := range eval() {
		if in.RuleID == "crash-loop" {
			fired = true
		}
	}
	if !fired {
		t.Fatal("shipped threshold (>3): a 5-restart crashloop pod must fire")
	}

	SetPolicySource(func(tenant, _ string) PolicySnapshot {
		if tenant != "org-x" {
			t.Errorf("policy source asked for tenant %q, want org-x", tenant)
		}
		return PolicySnapshot{Thresholds: map[string]float64{"crash-loop": 10}}
	})
	for _, in := range eval() {
		if in.RuleID == "crash-loop" {
			t.Fatalf("raised threshold (>10): a 5-restart pod must NOT fire, got %+v", in)
		}
	}
}

// TestEvaluate_SeverityOverrideAndOff pins the central application: an
// expectation's severity override rewrites the produced insights, and `off`
// stops the rule's output entirely (its actives resolve via the normal
// reconcile — a rule turned off clears, never freezes).
func TestEvaluate_SeverityOverrideAndOff(t *testing.T) {
	t.Cleanup(func() { SetPolicySource(nil) })

	fake := Rule{ID: "fake-expectation", Name: "Fake", Severity: "warning",
		Evaluate: func(*ClusterState) []models.Insight {
			return []models.Insight{{Severity: "warning", Resource: "ns/x", Title: "t", Message: "m"}}
		}}
	newE := func() *Engine {
		e := NewEngine(websocket.NewHub(), nil, "cluster", "org-y")
		e.rules = []Rule{fake}
		return e
	}

	SetPolicySource(func(string, string) PolicySnapshot {
		return PolicySnapshot{Severities: map[string]string{"fake-expectation": "info"}}
	})
	e := newE()
	e.Evaluate(&ClusterState{})
	got := e.GetAllInsights()
	if len(got) != 1 || got[0].Severity != "info" {
		t.Fatalf("severity override: got %+v, want one info insight", got)
	}

	SetPolicySource(func(string, string) PolicySnapshot {
		return PolicySnapshot{Severities: map[string]string{"fake-expectation": SeverityOff}}
	})
	e2 := newE()
	e2.Evaluate(&ClusterState{})
	if got := e2.GetAllInsights(); len(got) != 0 {
		t.Fatalf("off: got %+v, want none", got)
	}

	// And an ACTIVE insight resolves when the rule is switched off mid-flight.
	SetPolicySource(nil)
	e3 := newE()
	e3.Evaluate(&ClusterState{})
	if got := e3.GetAllInsights(); len(got) != 1 {
		t.Fatalf("precondition: %+v", got)
	}
	SetPolicySource(func(string, string) PolicySnapshot {
		return PolicySnapshot{Severities: map[string]string{"fake-expectation": SeverityOff}}
	})
	e3.Evaluate(&ClusterState{})
	if got := e3.GetAllInsights(); len(got) != 0 {
		t.Fatalf("active insight must resolve when its rule turns off, still have %+v", got)
	}
}

// The categories are CLOSED (#44/#14): the same field prices the node-hour.
func TestValidatePolicyCategory(t *testing.T) {
	for _, ok := range []string{"global", "production", "staging", "testing", "development"} {
		if err := ValidatePolicyCategory(ok); err != nil {
			t.Fatalf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "dev", "prod", "qa", "GLOBAL"} {
		if err := ValidatePolicyCategory(bad); err == nil {
			t.Fatalf("%q accepted — inventable categories are inventable discounts", bad)
		}
	}
}

// Every shipped rule carries a description — an id alone assumes the reader
// already knows all 24 rules.
func TestPolicyDescriptionsCoverCatalog(t *testing.T) {
	for id := range PolicyCatalog {
		if PolicyDescriptions[id] == "" {
			t.Errorf("rule %q has no description", id)
		}
	}
	for id := range PolicyDescriptions {
		if _, ok := PolicyCatalog[id]; !ok {
			t.Errorf("description for unknown rule %q", id)
		}
	}
}
