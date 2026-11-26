package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/kobiplan"
)

// appliedPlan is a plan whose one step nobody owns: cleanup's cpu request
// 100m → 10m, one pod, the template as the plan saw it.
func appliedPlan(id string, created time.Time) kobiplan.Plan {
	p := routePlan(id, "c-dev", created, kobiplan.RiskLow)
	s := &p.Steps[0]
	s.Target.Owner = &kobiplan.Owner{Kind: kobiplan.OwnerUnknown}
	s.Params = json.RawMessage(`{"containers":[{"container":"controller","requests":{"cpu":"10m"}}]}`)
	s.Before = json.RawMessage(`{"containers":[{"container":"controller","requests":{"cpu":"100m","memory":"64Mi"}}]}`)
	s.Evidence = json.RawMessage(`{"pods":1}`)
	s.Rollback = &kobiplan.Rollback{Action: kobiplan.ActionSetResources, Params: json.RawMessage(`{"containers":[{"container":"controller","requests":{"cpu":"100m"}}]}`)}
	p.ContentHash = kobiplan.ContentHash(p)
	return p
}

func change(t *testing.T, raw string) kobiplan.SetResourcesParams {
	t.Helper()
	c, err := kobiplan.DecodeSetResources(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestTouchedPlanSteps(t *testing.T) {
	now := time.Now().UTC()
	helm := routePlan("pln_helm", "c-dev", now, kobiplan.RiskLow) // owned by a Helm release
	old := appliedPlan("pln_old", now.Add(-8*24*time.Hour))       // a draft past its expiry
	withPlanStore(t, appliedPlan("pln_a", now), helm, old)
	store := kobiPlanStore()
	cpu30 := change(t, `{"containers":[{"container":"controller","requests":{"cpu":"30m"}}]}`)

	links := touchedPlanSteps(store, "org-1", "c-dev", "sha256:abc", "deployments", "kyverno", "cleanup", cpu30, now)
	if len(links) != 1 || links[0].planID != "pln_a" || links[0].stepID != "s1" {
		t.Fatalf("links = %+v (a Helm step and an expired plan never link)", links)
	}
	if got := touchedPlanSteps(store, "org-1", "c-dev", "sha256:other", "deployments", "kyverno", "cleanup", cpu30, now); len(got) != 0 {
		t.Fatal("linked a step whose workload changed since the plan")
	}
	if got := touchedPlanSteps(store, "org-1", "c-other", "sha256:abc", "deployments", "kyverno", "cleanup", cpu30, now); len(got) != 0 {
		t.Fatal("linked a plan of another cluster")
	}
	mem := change(t, `{"containers":[{"container":"controller","limits":{"memory":"1Gi"}}]}`)
	if got := touchedPlanSteps(store, "org-1", "c-dev", "sha256:abc", "deployments", "kyverno", "cleanup", mem, now); len(got) != 0 {
		t.Fatal("linked a change to a dimension the step does not change")
	}
}

func recordFor(t *testing.T, applied kobiplan.SetResourcesParams) []appliedPlanStep {
	t.Helper()
	h := &handlers{}
	r := httptest.NewRequest("POST", "/", nil)
	r = r.WithContext(auth.WithTenantID(context.Background(), "org-1"))
	store := kobiPlanStore()
	links := touchedPlanSteps(store, "org-1", "c-dev", "sha256:abc", "deployments", "kyverno", "cleanup", applied, time.Now().UTC())
	base := h.measureLinkedBaselines(r.Context(), store, "org-1", links)
	return h.recordAppliedPlanSteps(r, "c-dev", links, applied, base)
}

// Applied as proposed: the step is applied, the plan is exported through
// the apply channel with its baseline, and its content does not change.
func TestRecordAppliedStep_AsProposed(t *testing.T) {
	withBaselineVM(t)
	withPlanStore(t, appliedPlan("pln_a", time.Now().UTC()))
	out := recordFor(t, change(t, `{"containers":[{"container":"controller","requests":{"cpu":"10m"}}]}`))
	if len(out) != 1 || !out[0].AsProposed || out[0].Step != 1 {
		t.Fatalf("out = %+v", out)
	}
	p, err := kobiPlanStore().Get("org-1", "pln_a")
	if err != nil {
		t.Fatal(err)
	}
	if p.Version != 1 || p.Steps[0].Status != kobiplan.StepApplied || p.Status != kobiplan.StatusExported {
		t.Fatalf("plan v%d %s, step %s", p.Version, p.Status, p.Steps[0].Status)
	}
	if len(p.Exports) != 1 || p.Exports[0].Format != kobiplan.ExportApply || p.Baseline == nil || p.Steps[0].Baseline == nil {
		t.Fatalf("exports %+v, baseline %+v", p.Exports, p.Baseline)
	}
	if n := p.Notes[len(p.Notes)-1].Text; n != "Step 1 applied as proposed." {
		t.Fatalf("note = %q", n)
	}
}

// The person kept more than the engine proposed: a new version keeps their
// value, the engine's as reference, and the saving that value brings.
func TestRecordAppliedStep_WithThePersonsValue(t *testing.T) {
	withBaselineVM(t)
	withPlanStore(t, appliedPlan("pln_a", time.Now().UTC()))
	out := recordFor(t, change(t, `{"containers":[{"container":"controller","requests":{"cpu":"30m"}}]}`))
	if len(out) != 1 || out[0].AsProposed {
		t.Fatalf("out = %+v", out)
	}
	p, err := kobiPlanStore().Get("org-1", "pln_a")
	if err != nil {
		t.Fatal(err)
	}
	s := p.Steps[0]
	if p.Version != 2 || !s.Imposed || s.Status != kobiplan.StepApplied || !strings.Contains(string(s.EngineParams), `"10m"`) || !strings.Contains(string(s.Params), `"30m"`) {
		t.Fatalf("plan v%d, step %+v", p.Version, s)
	}
	// (100m − 30m) × 1 pod at 0.03 per core-hour, 730 hours.
	if s.Impact.Estimated.CPUMilli != 70 || s.Impact.Estimated.Money == nil || s.Impact.Estimated.Money.Monthly != 1.53 {
		t.Fatalf("estimate = %+v", s.Impact.Estimated)
	}
	v1, err := kobiPlanStore().GetVersion("org-1", "pln_a", 1)
	if err != nil || !strings.Contains(string(v1.Steps[0].Params), `"10m"`) {
		t.Fatalf("version 1 is not kept as it was: %v", err)
	}
	// Already applied: a second change finds nothing pending.
	if again := recordFor(t, change(t, `{"containers":[{"container":"controller","requests":{"cpu":"40m"}}]}`)); len(again) != 0 {
		t.Fatalf("linked an applied step again: %+v", again)
	}
}
