package kobiplan

import (
	"errors"
	"strings"
	"testing"
)

// The example step: payments/api, cpu request 500m → 120m and memory request
// 1Gi → 300Mi, 3 pods, a CPU P95 of 300m for the workload: 100m per pod.

func TestReviseStep_ThePersonsValues(t *testing.T) {
	p := examplePlan()
	engine := string(p.Steps[0].Params)
	changed, err := p.ReviseStep("s1", changeOf(t, `{"containers":[{"container":"api","requests":{"cpu":"0.15","memory":"300Mi"}}]}`), usdRates, "Ana", t0)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	s := p.Steps[0]
	if s.Status != StepPending || !s.Imposed || string(s.EngineParams) != engine {
		t.Fatalf("step = %+v", s)
	}
	// Written the canonical way.
	if string(s.Params) != `{"containers":[{"container":"api","requests":{"cpu":"150m","memory":"300Mi"}}]}` {
		t.Fatalf("params = %s", s.Params)
	}
	// (500m − 150m) × 3 pods.
	if s.Impact.Estimated.CPUMilli != 1050 {
		t.Fatalf("estimate = %+v", s.Impact.Estimated)
	}
	if s.Risk != RiskMedium || !contains(s.RiskReasons, RiskReasonLowersMemory) {
		t.Fatalf("risk = %s %v", s.Risk, s.RiskReasons)
	}
	if p.Version != 2 || !strings.Contains(p.Notes[len(p.Notes)-1].Text, "values changed by Ana") {
		t.Fatalf("version %d, notes %v", p.Version, p.Notes)
	}
	p.ContentHash = ContentHash(p)
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}

	// The same values again change nothing.
	if changed, err := p.ReviseStep("s1", changeOf(t, `{"containers":[{"container":"api","requests":{"cpu":"150m","memory":"300Mi"}}]}`), usdRates, "Ana", t0); err != nil || changed {
		t.Fatalf("again: changed=%v err=%v", changed, err)
	}

	// Back to the engine's: the step is the engine's again.
	if changed, err := p.ReviseStep("s1", changeOf(t, `{"containers":[{"container":"api","requests":{"cpu":"120m","memory":"300Mi"}}]}`), usdRates, "Ana", t0); err != nil || !changed {
		t.Fatalf("back: changed=%v err=%v", changed, err)
	}
	if s := p.Steps[0]; s.Imposed || s.EngineParams != nil || string(s.Params) != engine {
		t.Fatalf("back = %+v", s)
	}
	if !strings.Contains(p.Notes[len(p.Notes)-1].Text, "set back to the engine's values") {
		t.Fatalf("notes %v", p.Notes)
	}
}

// Between the P95 and the engine's value: taken, at high risk.
func TestReviseStep_BelowTheEngine(t *testing.T) {
	p := examplePlan()
	if _, err := p.ReviseStep("s1", changeOf(t, `{"containers":[{"container":"api","requests":{"cpu":"110m","memory":"300Mi"}}]}`), usdRates, "", t0); err != nil {
		t.Fatal(err)
	}
	if s := p.Steps[0]; s.Risk != RiskHigh || !contains(s.RiskReasons, RiskReasonBelowEngine) {
		t.Fatalf("risk = %s %v", s.Risk, s.RiskReasons)
	}
}

// Under the per-pod P95: refused with the figure, and nothing changes.
func TestReviseStep_UnderTheP95IsRefused(t *testing.T) {
	p := examplePlan()
	before := string(p.Steps[0].Params)
	_, err := p.ReviseStep("s1", changeOf(t, `{"containers":[{"container":"api","requests":{"cpu":"50m","memory":"300Mi"}}]}`), usdRates, "", t0)
	var rej *ValueRejected
	if !errors.As(err, &rej) || !strings.Contains(rej.Reason, "cpu request 50m is under the 100m per pod") || !strings.Contains(rej.Reason, "keeps 120m") {
		t.Fatalf("err = %v", err)
	}
	if string(p.Steps[0].Params) != before || p.Version != 1 {
		t.Fatal("a refused value changed the plan")
	}
}

// The step's direction holds: a request it lowers stays under today's, a
// limit it raises stays above today's (a request of 1 core for a step that
// lowers 500m saves nothing).
func TestReviseStep_TheStepsDirectionHolds(t *testing.T) {
	p := examplePlan()
	for _, cpu := range []string{"1", "500m"} {
		_, err := p.ReviseStep("s1", changeOf(t, `{"containers":[{"container":"api","requests":{"cpu":"`+cpu+`","memory":"300Mi"}}]}`), usdRates, "", t0)
		var rej *ValueRejected
		if !errors.As(err, &rej) || !strings.Contains(rej.Reason, "not under today's 500m") {
			t.Fatalf("cpu %s: %v", cpu, err)
		}
	}
	// Just under today's is taken: it saves little, but it saves.
	if changed, err := p.ReviseStep("s1", changeOf(t, `{"containers":[{"container":"api","requests":{"cpu":"490m","memory":"300Mi"}}]}`), usdRates, "", t0); err != nil || !changed {
		t.Fatalf("490m: changed=%v err=%v", changed, err)
	}

	// A step that raises a limit: 1 → 1500m.
	p = examplePlan()
	p.Steps[0].Params = []byte(`{"containers":[{"container":"api","limits":{"cpu":"1500m"}}]}`)
	_, err := p.ReviseStep("s1", changeOf(t, `{"containers":[{"container":"api","limits":{"cpu":"800m"}}]}`), usdRates, "", t0)
	var rej *ValueRejected
	if !errors.As(err, &rej) || !strings.Contains(rej.Reason, "not above today's 1") {
		t.Fatalf("limit down: %v", err)
	}
	if changed, err := p.ReviseStep("s1", changeOf(t, `{"containers":[{"container":"api","limits":{"cpu":"2"}}]}`), usdRates, "", t0); err != nil || !changed {
		t.Fatalf("limit 2: changed=%v err=%v", changed, err)
	}
}

// What does not fit the step is refused as a bad request.
func TestReviseStep_OnlyTheStepsValues(t *testing.T) {
	cases := map[string]string{
		"another container":    `{"containers":[{"container":"api","requests":{"cpu":"150m","memory":"300Mi"}},{"container":"x","requests":{"cpu":"1"}}]}`,
		"a value left out":     `{"containers":[{"container":"api","requests":{"cpu":"150m"}}]}`,
		"a dimension it lacks": `{"containers":[{"container":"api","requests":{"cpu":"150m","memory":"300Mi"},"limits":{"cpu":"2"}}]}`,
		"not a quantity":       `{"containers":[{"container":"api","requests":{"cpu":"lots","memory":"300Mi"}}]}`,
		"zero":                 `{"containers":[{"container":"api","requests":{"cpu":"0","memory":"300Mi"}}]}`,
	}
	for name, raw := range cases {
		p := examplePlan()
		_, err := p.ReviseStep("s1", changeOf(t, raw), usdRates, "", t0)
		var rej *ValueRejected
		if err == nil || errors.As(err, &rej) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	// A request above its limit (the limit stays at 2Gi), on a step whose
	// memory request today is not known, so only that check stops it.
	p := examplePlan()
	p.Steps[0].Before = []byte(`{"containers":[{"container":"api","requests":{"cpu":"500m"},"limits":{"cpu":"1","memory":"2Gi"}}]}`)
	if _, err := p.ReviseStep("s1", changeOf(t, `{"containers":[{"container":"api","requests":{"cpu":"150m","memory":"3Gi"}}]}`), usdRates, "", t0); err == nil || !strings.Contains(err.Error(), "under its request") {
		t.Fatalf("request over limit: %v", err)
	}
	// Only a pending step.
	p = examplePlan()
	p.Steps[0].Status = StepApplied
	if _, err := p.ReviseStep("s1", changeOf(t, `{"containers":[{"container":"api","requests":{"cpu":"150m","memory":"300Mi"}}]}`), usdRates, "", t0); err == nil {
		t.Fatal("an applied step was revised")
	}
}
