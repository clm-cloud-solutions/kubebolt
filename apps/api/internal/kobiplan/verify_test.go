package kobiplan

import (
	"strings"
	"testing"
	"time"
)

// The example step: payments/api, cpu request 500m → 120m and memory request
// 1Gi → 300Mi, limits cpu 1 and memory 2Gi, 3 pods. Exported.

func exportedExample(t *testing.T) Plan {
	t.Helper()
	p := examplePlan()
	p.Steps[0].Verification.Checks = append([]string(nil), rightsizingChecks...)
	p.RecordExport(ExportCommands, "Ana", t0, &Baseline{CapturedAt: t0, ClusterCost: &Money{Monthly: 100, Currency: "usd"}},
		map[string]*Baseline{"s1": {CapturedAt: t0, AllocatedCost: &Money{Monthly: 20, Currency: "usd"}}})
	return p
}

func liveValues(raw string) SetResourcesParams {
	p, _ := DecodeSetResources([]byte(raw))
	return p
}

var (
	liveAsStep   = `{"containers":[{"container":"api","requests":{"cpu":"120m","memory":"300Mi"},"limits":{"cpu":"1","memory":"2Gi"}}]}`
	liveAsBefore = `{"containers":[{"container":"api","requests":{"cpu":"500m","memory":"1Gi"},"limits":{"cpu":"1","memory":"2Gi"}}]}`
)

func factsFor(f StepFacts) PlanFacts { return PlanFacts{Steps: map[string]StepFacts{"s1": f}} }

func lastNote(p Plan) string { return p.Notes[len(p.Notes)-1].Text }

func hasNote(p Plan, sub string) bool {
	for _, n := range p.Notes {
		if strings.Contains(n.Text, sub) {
			return true
		}
	}
	return false
}

// Applied outside KubeBolt with the step's values: found applied, its clock
// starts.
func TestVerify_FoundApplied(t *testing.T) {
	p := exportedExample(t)
	t1 := t0.Add(time.Hour)
	changed, content := p.Verify(factsFor(StepFacts{Found: true, SpecHash: "sha256:new", Live: liveValues(liveAsStep), Pods: 3}), usdRates, t1)
	if !changed || content || p.Steps[0].Status != StepApplied || !p.Steps[0].AppliedAt.Equal(t1) {
		t.Fatalf("changed=%v content=%v step=%+v", changed, content, p.Steps[0])
	}
	if !strings.Contains(lastNote(p), "found applied in the cluster") || p.Status != StatusExported {
		t.Fatalf("note %q, status %s", lastNote(p), p.Status)
	}
}

// Untouched: nothing moves.
func TestVerify_StillToDo(t *testing.T) {
	p := exportedExample(t)
	notes := len(p.Notes)
	changed, _ := p.Verify(factsFor(StepFacts{Found: true, SpecHash: p.Steps[0].Precondition.SpecHash, Live: liveValues(liveAsBefore), Pods: 3}), usdRates, t0.Add(time.Hour))
	if changed || p.Steps[0].Status != StepPending || len(p.Notes) != notes {
		t.Fatalf("changed=%v step=%+v", changed, p.Steps[0])
	}
}

// The workload changed in something else (its image) before the step was
// applied: the step expires, and a plan with nothing left is expired too.
func TestVerify_ChangedElsewhere(t *testing.T) {
	p := exportedExample(t)
	changed, _ := p.Verify(factsFor(StepFacts{Found: true, SpecHash: "sha256:other-image", Live: liveValues(liveAsBefore), Pods: 3}), usdRates, t0.Add(time.Hour))
	if !changed || p.Steps[0].Status != StepExpired || p.Status != StatusExpired || p.ExpiredReason != ExpiredChanged {
		t.Fatalf("step %s, plan %s/%s", p.Steps[0].Status, p.Status, p.ExpiredReason)
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
}

// Other values in the step's dimension, set outside KubeBolt: the plan keeps
// them (d85), in a new version.
func TestVerify_AppliedWithOtherValues(t *testing.T) {
	p := exportedExample(t)
	other := `{"containers":[{"container":"api","requests":{"cpu":"200m","memory":"300Mi"},"limits":{"cpu":"1","memory":"2Gi"}}]}`
	changed, content := p.Verify(factsFor(StepFacts{Found: true, SpecHash: "sha256:new", Live: liveValues(other), Pods: 3}), usdRates, t0.Add(time.Hour))
	s := p.Steps[0]
	if !changed || !content || s.Status != StepApplied || !s.Imposed || p.Version != 2 || p.ContentHash != "" {
		t.Fatalf("changed=%v content=%v step=%+v v%d", changed, content, s, p.Version)
	}
	if string(s.Params) != `{"containers":[{"container":"api","requests":{"cpu":"200m","memory":"300Mi"}}]}` {
		t.Fatalf("params = %s", s.Params)
	}
	p.ContentHash = ContentHash(p)
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
}

func appliedExample(t *testing.T) Plan {
	t.Helper()
	p := exportedExample(t)
	p.Verify(factsFor(StepFacts{Found: true, SpecHash: "sha256:new", Live: liveValues(liveAsStep), Pods: 3}), usdRates, t0)
	return p
}

// VerifyAfter later, every check passes: verified, and the plan with it.
func TestVerify_Verified(t *testing.T) {
	p := appliedExample(t)
	ok := StepFacts{Found: true, SpecHash: "sha256:new", Live: liveValues(liveAsStep), Pods: 3, Checked: true, RolloutComplete: true,
		P95: map[bool]int64{false: 100, true: 250 << 20}}
	t1 := t0.Add(VerifyAfter)
	if changed, _ := p.Verify(factsFor(ok), usdRates, t1); !changed {
		t.Fatal("nothing moved")
	}
	s := p.Steps[0]
	if s.Status != StepVerified || s.Verification.Result == nil || !s.Verification.Result.Passed || !s.VerifiedAt.Equal(t1) {
		t.Fatalf("step = %+v %+v", s, s.Verification.Result)
	}
	for _, c := range []string{CheckRolloutComplete, CheckNoOOMKilled, CheckRestartsNotUp, CheckP95UnderLimit} {
		if s.Verification.Result.Checks[c] != CheckOK {
			t.Fatalf("%s = %s", c, s.Verification.Result.Checks[c])
		}
	}
	if p.Status != StatusVerified || p.Status.Live() {
		t.Fatalf("plan %s", p.Status)
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
}

// A failed check keeps the step applied, says what failed once, and points
// to the rollback.
func TestVerify_FailedChecks(t *testing.T) {
	p := appliedExample(t)
	bad := StepFacts{Found: true, SpecHash: "sha256:new", Live: liveValues(liveAsStep), Pods: 3, Checked: true, RolloutComplete: true,
		OOMKilled: true, Restarts: 2, P95: map[bool]int64{false: 900, true: 250 << 20}}
	p.Verify(factsFor(bad), usdRates, t0.Add(VerifyAfter))
	s := p.Steps[0]
	r := s.Verification.Result
	if s.Status != StepApplied || r == nil || r.Passed || r.Checks[CheckNoOOMKilled] != CheckFailed || r.Checks[CheckRestartsNotUp] != CheckFailed || r.Checks[CheckP95UnderLimit] != CheckFailed {
		t.Fatalf("step %s, result %+v", s.Status, r)
	}
	if !strings.Contains(r.Detail, "OOMKilled") || !strings.Contains(r.Detail, "restarted 2 times") || !strings.Contains(r.Detail, "CPU P95 of 900m is over 80% of its 1 limit") {
		t.Fatalf("detail = %q", r.Detail)
	}
	notes := len(p.Notes)
	if !strings.Contains(lastNote(p), "failed its checks") || p.Status != StatusExported {
		t.Fatalf("note %q, plan %s", lastNote(p), p.Status)
	}
	// The next sweep finds the same: no second note.
	p.Verify(factsFor(bad), usdRates, t0.Add(VerifyAfter+time.Hour))
	if len(p.Notes) != notes {
		t.Fatalf("the failure was noted twice: %v", p.Notes)
	}
}

// Without a limit the P95 check does not apply; without data it is unknown
// and not held against the step.
func TestVerify_P95WithoutALimitOrData(t *testing.T) {
	noLimit := StepFacts{Live: liveValues(`{"containers":[{"container":"api","requests":{"cpu":"120m"}}]}`)}
	if out, _ := p95UnderLimit(noLimit); out != CheckNotApplicable {
		t.Fatalf("no limit = %s", out)
	}
	noData := StepFacts{Live: liveValues(liveAsStep)}
	if out, _ := p95UnderLimit(noData); out != CheckUnknown {
		t.Fatalf("no data = %s", out)
	}
	p := appliedExample(t)
	p.Verify(factsFor(StepFacts{Found: true, SpecHash: "sha256:new", Live: liveValues(liveAsStep), Pods: 3, Checked: true, RolloutComplete: true}), usdRates, t0.Add(VerifyAfter))
	if p.Steps[0].Status != StepVerified || p.Steps[0].Verification.Result.Checks[CheckP95UnderLimit] != CheckUnknown {
		t.Fatalf("step %s %+v", p.Steps[0].Status, p.Steps[0].Verification.Result)
	}
}

// The values went back to the ones before (a rollback, or Helm reverting
// them): the step is pending again.
func TestVerify_Reverted(t *testing.T) {
	p := appliedExample(t)
	p.Verify(factsFor(StepFacts{Found: true, SpecHash: p.Steps[0].Precondition.SpecHash, Live: liveValues(liveAsBefore), Pods: 3}), usdRates, t0.Add(time.Hour))
	if s := p.Steps[0]; s.Status != StepPending || s.AppliedAt != nil || !strings.Contains(lastNote(p), "pending again") {
		t.Fatalf("step %+v, note %q", s, lastNote(p))
	}
}

// ImpactAfter later: what it freed and what the workload costs now, against
// the baseline; the plan sums its steps and adds the cluster's cost as
// context.
func TestVerify_ObservedImpact(t *testing.T) {
	p := appliedExample(t)
	p.Verify(factsFor(StepFacts{Found: true, SpecHash: "sha256:new", Live: liveValues(liveAsStep), Pods: 3, Checked: true, RolloutComplete: true}), usdRates, t0.Add(VerifyAfter))
	t2 := t0.Add(ImpactAfter)
	f := PlanFacts{
		Steps:            map[string]StepFacts{"s1": {Found: true, SpecHash: "sha256:new", Live: liveValues(liveAsStep), Pods: 3, Measured: true, WindowDays: 7, AllocatedAfter: &Money{Monthly: 6.5, Currency: "usd"}}},
		ClusterCostAfter: &Money{Monthly: 96, Currency: "usd"},
	}
	if changed, _ := p.Verify(f, usdRates, t2); !changed {
		t.Fatal("nothing measured")
	}
	o := p.Steps[0].Impact.Observed
	// (500m − 120m) × 3 and (1Gi − 300Mi) × 3.
	if o == nil || o.CPUMilliFreed != 1140 || o.MemBytesFreed != 3*(1024-300)<<20 || o.AllocatedCost == nil || o.AllocatedCost.Before != 20 || o.AllocatedCost.After != 6.5 {
		t.Fatalf("step observed = %+v %+v", o, o.AllocatedCost)
	}
	po := p.Impact.Observed
	if po == nil || po.CPUMilliFreed != 1140 || po.ClusterCost == nil || po.ClusterCost.Before != 100 || po.ClusterCost.After != 96 || po.ClusterCost.Attributed {
		t.Fatalf("plan observed = %+v", po)
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
}

// The workload is gone: the step expires.
func TestVerify_Gone(t *testing.T) {
	p := exportedExample(t)
	p.Verify(factsFor(StepFacts{Found: false}), usdRates, t0.Add(time.Hour))
	if p.Steps[0].Status != StepExpired || !hasNote(p, "the workload is gone") {
		t.Fatalf("step %s, note %q", p.Steps[0].Status, lastNote(p))
	}
}
