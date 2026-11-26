package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
	"github.com/kubebolt/kubebolt/apps/api/internal/kobiplan"
)

func reviseRequest(t *testing.T, planID, stepID, body string) (int, string, planView) {
	t.Helper()
	h := &handlers{}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := auth.WithTenantID(req.Context(), "org-1")
			ctx = context.WithValue(ctx, clusterScopeKey, ClusterScope{})
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	r.Put("/plans/{id}/steps/{step}", h.handleReviseStep)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/plans/"+planID+"/steps/"+stepID, strings.NewReader(body)))
	var out planView
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, rec.Body.String(), out
}

// A person's values make a new version that keeps the engine's; a value
// under the per-pod P95 is refused with the figure and noted (plans-06).
func TestReviseStep(t *testing.T) {
	withBaselineVM(t)
	now := time.Now().UTC()
	p := routePlan("pln_a", "c-dev", now, kobiplan.RiskLow) // kyverno/cleanup, cpu request 10m
	p.Steps[0].Before = json.RawMessage(`{"containers":[{"container":"controller","requests":{"cpu":"100m"}}]}`)
	p.Steps[0].Evidence = json.RawMessage(`{"cpu":{"p95":6},"pods":1}`)
	withPlanStore(t, p, routePlan("pln_old", "c-dev", now.Add(-8*24*time.Hour), kobiplan.RiskLow))

	code, body, out := reviseRequest(t, "pln_a", "s1", `{"containers":[{"container":"controller","requests":{"cpu":"30m"}}]}`)
	if code != http.StatusOK || out.Version != 2 || !out.Steps[0].Imposed || len(out.Steps[0].EngineParams) == 0 {
		t.Fatalf("revise: %d %s", code, body)
	}
	// (100m − 30m) × 1 pod.
	if out.Steps[0].Impact.Estimated.CPUMilli != 70 {
		t.Fatalf("estimate = %+v", out.Steps[0].Impact.Estimated)
	}
	if old, err := kobiPlanStore().Get("org-1", "pln_a"); err != nil || old.Version != 2 {
		t.Fatalf("stored = %v, %v", old, err)
	}

	// Under the 6m per pod: refused, noted, and the plan keeps its values.
	code, body, _ = reviseRequest(t, "pln_a", "s1", `{"containers":[{"container":"controller","requests":{"cpu":"5m"}}]}`)
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "under the 6m per pod") {
		t.Fatalf("refusal: %d %s", code, body)
	}
	stored, _ := kobiPlanStore().Get("org-1", "pln_a")
	if stored.Version != 2 || !strings.Contains(stored.Notes[len(stored.Notes)-1].Text, "was refused: cpu request 5m") {
		t.Fatalf("after the refusal: v%d %v", stored.Version, stored.Notes)
	}

	// What does not fit the step, a step that does not exist, a plan that
	// is no longer live.
	if code, _, _ := reviseRequest(t, "pln_a", "s1", `{"containers":[{"container":"controller","limits":{"cpu":"1"}}]}`); code != http.StatusBadRequest {
		t.Fatalf("another dimension: %d", code)
	}
	if code, _, _ := reviseRequest(t, "pln_a", "s9", `{"containers":[{"container":"controller","requests":{"cpu":"30m"}}]}`); code != http.StatusNotFound {
		t.Fatalf("unknown step: %d", code)
	}
	if code, _, _ := reviseRequest(t, "pln_old", "s1", `{"containers":[{"container":"controller","requests":{"cpu":"30m"}}]}`); code != http.StatusConflict {
		t.Fatalf("an expired draft: %d", code)
	}
}

// revise_plan's source: the step is found by its workload among the live
// plans, refused under the P95 (and noted), revised otherwise; several plans
// with the workload are asked about, by title.
func TestPlanSourceReviseStep(t *testing.T) {
	withBaselineVM(t)
	now := time.Now().UTC()
	p := routePlan("pln_a", "c-dev", now, kobiplan.RiskLow) // kyverno/cleanup, container controller, cpu request 10m
	p.Steps[0].Before = json.RawMessage(`{"containers":[{"container":"controller","requests":{"cpu":"100m"}}]}`)
	p.Steps[0].Evidence = json.RawMessage(`{"cpu":{"p95":6},"pods":1}`)
	withPlanStore(t, p)
	src := planSource{h: &handlers{}, store: kobiPlanStore(), tenant: "org-1", userName: "ana"}
	ctx := auth.WithTenantID(t.Context(), "org-1")

	out, err := src.reviseStepOn(ctx, "c-dev", copilot.ReviseStepRequest{Workload: "kyverno/deployment/cleanup", Requests: map[string]string{"cpu": "30m"}})
	if err != nil || !out.Changed || out.Order != 1 || out.Plan.Version != 2 || !out.Plan.Steps[0].Imposed {
		t.Fatalf("revise: %+v, %v", out, err)
	}
	out, err = src.reviseStepOn(ctx, "c-dev", copilot.ReviseStepRequest{Workload: "kyverno/cleanup", Requests: map[string]string{"cpu": "5m"}})
	if err != nil || out.Refused == "" || !strings.Contains(out.Refused, "under the 6m per pod") {
		t.Fatalf("refuse: %+v, %v", out, err)
	}
	if stored, _ := kobiPlanStore().Get("org-1", "pln_a"); stored.Version != 2 || !strings.Contains(stored.Notes[len(stored.Notes)-1].Text, "from ana was refused") {
		t.Fatalf("after the refusal: %+v", stored.Notes)
	}
	// Another workload, another cluster: nothing to revise.
	if _, err := src.reviseStepOn(ctx, "c-dev", copilot.ReviseStepRequest{Workload: "kyverno/other", Requests: map[string]string{"cpu": "30m"}}); err == nil {
		t.Fatal("a workload with no step was revised")
	}
	if _, err := src.reviseStepOn(ctx, "c-lab", copilot.ReviseStepRequest{Workload: "kyverno/cleanup", Requests: map[string]string{"cpu": "30m"}}); err == nil {
		t.Fatal("another cluster's plan was revised")
	}
	// A value for a dimension the step does not change is refused.
	if _, err := src.reviseStepOn(ctx, "c-dev", copilot.ReviseStepRequest{Workload: "kyverno/cleanup", Limits: map[string]string{"cpu": "1"}}); err == nil {
		t.Fatal("a limit the step does not change was taken")
	}

	// Two live plans with the workload: the title picks one.
	q := routePlan("pln_b", "c-dev", now, kobiplan.RiskLow)
	q.Title = "Otro plan"
	if err := kobiPlanStore().Create(q); err != nil {
		t.Fatal(err)
	}
	if _, err := src.reviseStepOn(ctx, "c-dev", copilot.ReviseStepRequest{Workload: "kyverno/cleanup", Requests: map[string]string{"cpu": "40m"}}); err == nil || !strings.Contains(err.Error(), "«Otro plan»") {
		t.Fatalf("two plans: %v", err)
	}
	out, err = src.reviseStepOn(ctx, "c-dev", copilot.ReviseStepRequest{Workload: "kyverno/cleanup", Plan: "otro plan", Requests: map[string]string{"cpu": "40m"}})
	if err != nil || out.Plan.ID != "pln_b" {
		t.Fatalf("by title: %+v, %v", out, err)
	}
}
