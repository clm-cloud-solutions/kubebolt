package kobiplan

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/costmodel"
)

// The example step: payments/api, cpu request 500m → 120m and memory request
// 1Gi → 300Mi, 3 pods, nobody identified as its owner.

func changeOf(t *testing.T, raw string) SetResourcesParams {
	t.Helper()
	p, err := DecodeSetResources(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

var usdRates = Pricing{Rates: costmodel.NodeRates{CPUCoreHourly: 0.03, RAMGiBHourly: 0.004}, Factor: 1, Currency: "usd", Source: "opencost"}

func TestStepTouches(t *testing.T) {
	s := examplePlan().Steps[0]
	cpu := changeOf(t, `{"containers":[{"container":"api","requests":{"cpu":"300m"}}]}`)
	if !s.Touches("deployments", "payments", "api", cpu) {
		t.Fatal("a cpu request on the step's container is the step's change, whatever the value")
	}
	if s.Touches("deployments", "payments", "web", cpu) || s.Touches("statefulsets", "payments", "api", cpu) {
		t.Fatal("another workload touched the step")
	}
	if s.Touches("deployments", "payments", "api", changeOf(t, `{"containers":[{"container":"sidecar","requests":{"cpu":"300m"}}]}`)) {
		t.Fatal("another container touched the step")
	}
	if s.Touches("deployments", "payments", "api", changeOf(t, `{"containers":[{"container":"api","limits":{"cpu":"2"}}]}`)) {
		t.Fatal("a dimension the step does not change touched it")
	}
	if !s.AppliedInCluster() {
		t.Fatal("a step nobody owns is applied in the cluster")
	}
	s.Target.Owner = &Owner{Kind: OwnerHelm, Ref: "payments"}
	if s.AppliedInCluster() {
		t.Fatal("a Helm step is applied in the cluster")
	}
}

// Applied as written: the step is applied and the plan's content does not
// change, so no new version.
func TestApplyStep_AsProposed(t *testing.T) {
	p := examplePlan()
	changed, err := p.ApplyStep("s1", changeOf(t, `{"containers":[{"container":"api","requests":{"cpu":"120m","memory":"300Mi"}}]}`), usdRates, "Ana", t0)
	if err != nil || changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if p.Steps[0].Status != StepApplied || p.Version != 1 || p.Steps[0].Imposed || p.ContentHash != ContentHash(p) {
		t.Fatalf("step = %+v", p.Steps[0])
	}
}

// «10m, but I would rather keep more»: the step takes the person's values,
// keeps the engine's, and its saving and rollback follow what was applied.
func TestApplyStep_WithThePersonsValues(t *testing.T) {
	p := examplePlan()
	engine := string(p.Steps[0].Params)
	changed, err := p.ApplyStep("s1", changeOf(t, `{"containers":[{"container":"api","requests":{"cpu":"300m"}}]}`), usdRates, "Ana", t0)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	s := p.Steps[0]
	if s.Status != StepApplied || !s.Imposed || string(s.EngineParams) != engine {
		t.Fatalf("step = %+v", s)
	}
	if string(s.Params) != `{"containers":[{"container":"api","requests":{"cpu":"300m"}}]}` {
		t.Fatalf("params = %s (memory was not applied, so it is not in the step)", s.Params)
	}
	if string(s.Rollback.Params) != `{"containers":[{"container":"api","requests":{"cpu":"500m"}}]}` {
		t.Fatalf("rollback = %s", s.Rollback.Params)
	}
	// (500m − 300m) × 3 pods; at 0.03 per core-hour, 730 hours.
	if s.Impact.Estimated.CPUMilli != 600 || s.Impact.Estimated.MemBytes != 0 || s.Impact.Estimated.Money.Monthly != 13.14 {
		t.Fatalf("estimate = %+v", s.Impact.Estimated)
	}
	if p.Impact.Estimated.CPUMilli != 600 || p.Impact.Estimated.Money.Monthly != 13.14 || p.Impact.Estimated.Money.Currency != "usd" {
		t.Fatalf("plan estimate = %+v", p.Impact.Estimated)
	}
	// More than the engine's value is not riskier; memory is no longer lowered.
	if s.Risk != RiskLow || len(s.RiskReasons) != 0 {
		t.Fatalf("risk = %s %v", s.Risk, s.RiskReasons)
	}
	// No reason left is an empty list, never null: the plan view maps it.
	if raw, _ := json.Marshal(s); !strings.Contains(string(raw), `"riskReasons":[]`) {
		t.Fatalf("step JSON = %s", raw)
	}
	if p.Version != 2 || p.ContentHash != "" || !strings.Contains(p.Notes[len(p.Notes)-1].Text, "applied by Ana with values different from the engine's") {
		t.Fatalf("version %d, notes %v", p.Version, p.Notes)
	}
	p.ContentHash = ContentHash(p)
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
}

// Below what the engine measured as needed: allowed, and the risk says why.
func TestApplyStep_BelowTheEngine(t *testing.T) {
	p := examplePlan()
	applied := changeOf(t, `{"containers":[{"container":"api","requests":{"cpu":"50m","memory":"300Mi"}}]}`)
	risk, reasons, deviates, below := p.Steps[0].AppliedRisk(applied)
	if !deviates || !below || risk != RiskHigh || !contains(reasons, RiskReasonBelowEngine) {
		t.Fatalf("risk %s %v deviates=%v below=%v", risk, reasons, deviates, below)
	}
	if _, err := p.ApplyStep("s1", applied, usdRates, "", t0); err != nil {
		t.Fatal(err)
	}
	if p.Steps[0].Risk != RiskHigh {
		t.Fatalf("applied risk = %s", p.Steps[0].Risk)
	}
}

// A value that saves nothing is recorded with nothing saved, never a saving
// below zero.
func TestApplyStep_NothingSaved(t *testing.T) {
	p := examplePlan()
	if _, err := p.ApplyStep("s1", changeOf(t, `{"containers":[{"container":"api","requests":{"cpu":"600m"}}]}`), usdRates, "", t0); err != nil {
		t.Fatal(err)
	}
	if e := p.Steps[0].Impact.Estimated; e.CPUMilli != 0 || e.Money.Monthly != 0 {
		t.Fatalf("estimate = %+v", e)
	}
	p.ContentHash = ContentHash(p)
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
