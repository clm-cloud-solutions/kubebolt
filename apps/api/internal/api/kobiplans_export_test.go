package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/kobiplan"
)

// exportRequest serves one request through the export routes with the
// caller's org and cluster scope resolved, as the middleware leaves them.
func exportRequest(t *testing.T, method, path, body string, scope ClusterScope) (int, planExportResponse) {
	t.Helper()
	h := &handlers{}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := auth.WithTenantID(req.Context(), "org-1")
			ctx = context.WithValue(ctx, clusterScopeKey, scope)
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	r.Get("/plans/{id}/export", h.handlePreviewPlanExport)
	r.Post("/plans/{id}/export", h.handleExportPlan)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	var out planExportResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// baselineVM answers the baseline's queries: node prices, the cluster's
// hourly cost and one workload's allocation. It keeps what it was asked.
type baselineVM struct {
	mu      sync.Mutex
	queries []string
}

func (b *baselineVM) asked(sub string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for _, q := range b.queries {
		if strings.Contains(q, sub) {
			out = append(out, q)
		}
	}
	return out
}

func withBaselineVM(t *testing.T) *baselineVM {
	t.Helper()
	b := &baselineVM{}
	vm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		b.mu.Lock()
		b.queries = append(b.queries, q)
		b.mu.Unlock()
		v := ""
		switch {
		case strings.HasPrefix(q, "count_over_time("):
			v = "75" // hours with data: 3.1 days
		case strings.Contains(q, "container_cpu_allocation"):
			v = "0.01"
		case strings.Contains(q, "node_total_hourly_cost"):
			v = "1.5"
		case strings.HasPrefix(q, "avg(node_cpu_hourly_cost"):
			v = "0.03"
		case strings.HasPrefix(q, "avg(node_ram_hourly_cost"):
			v = "0.004"
		}
		w.Header().Set("Content-Type", "application/json")
		if v == "" {
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1700000000,"` + v + `"]}]}}`))
	}))
	t.Cleanup(vm.Close)
	t.Setenv("KUBEBOLT_METRICS_STORAGE_URL", vm.URL)
	return b
}

// Looking at the text changes nothing: the plan stays a draft.
func TestPlanExportPreview(t *testing.T) {
	now := time.Now().UTC()
	withPlanStore(t, routePlan("pln_a", "c-dev", now, kobiplan.RiskMedium))

	code, out := exportRequest(t, http.MethodGet, "/plans/pln_a/export?format=commands", "", ClusterScope{})
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if out.Plan != nil || out.Export.Format != kobiplan.ExportCommands || !strings.HasSuffix(out.Export.Filename, "-commands.sh") {
		t.Fatalf("preview = %+v", out)
	}
	// Helm-owned, and the cluster is not readable here: said in words.
	if !strings.Contains(out.Export.Text, "# Helm release kyverno (namespace kyverno)") ||
		!strings.Contains(out.Export.Text, "Not placed (the release's values could not be read)") {
		t.Fatalf("text:\n%s", out.Export.Text)
	}
	p, err := kobiPlanStore().Get("org-1", "pln_a")
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != kobiplan.StatusDraft || len(p.Exports) != 0 {
		t.Fatalf("a preview recorded an export: %+v", p)
	}

	if code, _ := exportRequest(t, http.MethodGet, "/plans/pln_a/export?format=pdf", "", ClusterScope{}); code != http.StatusBadRequest {
		t.Fatalf("unknown format: %d", code)
	}
	if code, _ := exportRequest(t, http.MethodGet, "/plans/pln_a/export?format=diff", "", narrowedTo("c-other")); code != http.StatusNotFound {
		t.Fatalf("a plan of another cluster: %d", code)
	}
}

// Taking the text records the export, makes the plan exported and measures
// its baseline once (P20), in the cluster's own scope.
func TestPlanExportRecords(t *testing.T) {
	vm := withBaselineVM(t)
	now := time.Now().UTC()
	withPlanStore(t, routePlan("pln_a", "c-dev", now, kobiplan.RiskMedium))

	code, out := exportRequest(t, http.MethodPost, "/plans/pln_a/export", `{"format":"commands"}`, ClusterScope{})
	if code != http.StatusOK || out.Plan == nil {
		t.Fatalf("status %d: %+v", code, out)
	}
	p := out.Plan
	if p.EffectiveStatus != kobiplan.StatusExported || len(p.Exports) != 1 || p.ExportedAt == nil {
		t.Fatalf("after export: status %s, %d exports", p.EffectiveStatus, len(p.Exports))
	}
	// The cluster has 3.1 days of data: the baseline says so, not 7.
	if p.Baseline == nil || p.Baseline.ClusterCost == nil || p.Baseline.ClusterCost.Monthly != 1095 || p.Baseline.WindowDays != 3.1 ||
		p.Baseline.ClusterCostCovers != kobiplan.CoversBill {
		t.Fatalf("plan baseline = %+v", p.Baseline)
	}
	// The cluster's cost is its bill: nodes, disks priced by their size, load
	// balancers and the management fee, the parts OpenCost does not send as
	// zero.
	bill := vm.asked("avg_over_time((sum(node_total_hourly_cost")
	if len(bill) != 1 || !strings.Contains(bill[0], "pv_hourly_cost") || !strings.Contains(bill[0], "pod_pvc_allocation") ||
		!strings.Contains(bill[0], "kubecost_load_balancer_cost") || !strings.Contains(bill[0], "kubecost_cluster_management_cost") ||
		strings.Contains(bill[0], "kubecost_network_") {
		t.Fatalf("cluster cost query = %v", bill)
	}
	// Every part is this org's and this cluster's: the query reaches VictoriaMetrics with no
	// other filter behind it.
	for _, m := range []string{"node_total_hourly_cost", "pv_hourly_cost", "pod_pvc_allocation", "kubecost_load_balancer_cost", "kubecost_cluster_management_cost"} {
		if !strings.Contains(bill[0], m+`{tenant_id="org-1",cluster_id="c-dev"}`) {
			t.Fatalf("%s is not scoped to the cluster: %s", m, bill[0])
		}
	}
	sb := p.Steps[0].Baseline
	if sb == nil || sb.AllocatedCost == nil || sb.AllocatedCost.Monthly != 7.3 || sb.AllocatedCost.Currency != "usd" || sb.WindowDays != 3.1 {
		t.Fatalf("step baseline = %+v", sb)
	}
	if n := p.Notes[len(p.Notes)-1].Text; n != "Exported as commands." {
		t.Fatalf("note = %q", n)
	}
	alloc := vm.asked("container_cpu_allocation")
	// No zero for the hours without data: they would halve a young
	// cluster's average.
	if len(alloc) != 1 || strings.Contains(alloc[0], "vector(0)") || !strings.Contains(alloc[0], `cluster_id="c-dev"`) ||
		!strings.Contains(alloc[0], `namespace="kyverno"`) || !strings.Contains(alloc[0], `pod=~"cleanup-[a-z0-9]{6,12}-[a-z0-9]{5}"`) {
		t.Fatalf("allocation query = %v", alloc)
	}

	// A second export adds its record and keeps the first's baseline.
	code, out = exportRequest(t, http.MethodPost, "/plans/pln_a/export", `{"format":"diff"}`, ClusterScope{})
	if code != http.StatusOK || len(out.Plan.Exports) != 2 || !out.Plan.ExportedAt.Equal(*p.ExportedAt) {
		t.Fatalf("second export: %d %+v", code, out.Plan)
	}
	if got := len(vm.asked("avg_over_time((sum(node_total_hourly_cost")); got != 1 {
		t.Fatalf("the baseline was measured %d times", got)
	}
	stored, err := kobiPlanStore().Get("org-1", "pln_a")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != kobiplan.StatusExported || len(stored.Exports) != 2 || stored.Exports[1].Format != kobiplan.ExportDiff {
		t.Fatalf("stored = %+v", stored)
	}
}

func TestPlanExportRefusals(t *testing.T) {
	now := time.Now().UTC()
	old := routePlan("pln_old", "c-dev", now.Add(-8*24*time.Hour), kobiplan.RiskLow)
	withPlanStore(t, routePlan("pln_a", "c-dev", now, kobiplan.RiskLow), old)

	if code, _ := exportRequest(t, http.MethodPost, "/plans/pln_old/export", `{"format":"commands"}`, ClusterScope{}); code != http.StatusConflict {
		t.Fatalf("an expired draft: %d", code)
	}
	if code, _ := exportRequest(t, http.MethodPost, "/plans/pln_a/export", `{"format":"pr"}`, ClusterScope{}); code != http.StatusBadRequest {
		t.Fatalf("a format with no rendering: %d", code)
	}
	if code, _ := exportRequest(t, http.MethodPost, "/plans/pln_a/export", `{"format":"commands"}`, narrowedTo("c-other")); code != http.StatusNotFound {
		t.Fatalf("a plan of another cluster: %d", code)
	}
}

// Discarding ends a live plan, records who did it and frees its place
// (plans-06); an ended plan cannot be discarded again.
func TestPlanDiscard(t *testing.T) {
	now := time.Now().UTC()
	old := routePlan("pln_old", "c-dev", now.Add(-8*24*time.Hour), kobiplan.RiskLow)
	withPlanStore(t, routePlan("pln_a", "c-dev", now, kobiplan.RiskLow), old)

	discard := func(id string, scope ClusterScope) (int, planView) {
		h := &handlers{}
		r := chi.NewRouter()
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				ctx := auth.WithTenantID(req.Context(), "org-1")
				ctx = context.WithValue(ctx, clusterScopeKey, scope)
				next.ServeHTTP(w, req.WithContext(ctx))
			})
		})
		r.Post("/plans/{id}/discard", h.handleDiscardPlan)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/plans/"+id+"/discard", nil))
		var out planView
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	if code, _ := discard("pln_a", narrowedTo("c-other")); code != http.StatusNotFound {
		t.Fatalf("a plan of another cluster: %d", code)
	}
	code, out := discard("pln_a", ClusterScope{})
	if code != http.StatusOK || out.EffectiveStatus != kobiplan.StatusExpired || out.EffectiveExpiredReason != kobiplan.ExpiredDiscarded {
		t.Fatalf("discard: %d %+v", code, out)
	}
	stored, err := kobiPlanStore().Get("org-1", "pln_a")
	if err != nil || stored.Status != kobiplan.StatusExpired || stored.Notes[len(stored.Notes)-1].Text != "Discarded." {
		t.Fatalf("stored = %+v, %v", stored, err)
	}
	live, _ := kobiPlanStore().LiveForCluster("org-1", "c-dev")
	for _, p := range live {
		if p.ID == "pln_a" {
			t.Fatal("the discarded plan is still live")
		}
	}
	if code, _ := discard("pln_a", ClusterScope{}); code != http.StatusConflict {
		t.Fatalf("discarded twice: %d", code)
	}
	// A draft past its expiry has already ended.
	if code, _ := discard("pln_old", ClusterScope{}); code != http.StatusConflict {
		t.Fatalf("an expired draft: %d", code)
	}
	// A plan with every step applied is done: it is kept.
	done := routePlan("pln_done", "c-dev", now, kobiplan.RiskLow)
	done.RecordExport(kobiplan.ExportApply, "ana", now, nil, nil)
	done.Steps[0].Status = kobiplan.StepApplied
	if err := kobiPlanStore().Create(done); err != nil {
		t.Fatal(err)
	}
	if code, _ := discard("pln_done", ClusterScope{}); code != http.StatusConflict {
		t.Fatalf("a done plan: %d", code)
	}
}

// A plan's cluster cost is read after on the parts it was read on before:
// the bill for a plan exported since 26-nov, the nodes alone for an older one.
func TestClusterCostExpr_MatchesTheBaseline(t *testing.T) {
	nodes := `sum(node_total_hourly_cost)`
	if got := clusterCostExpr(nil); got != nodes {
		t.Fatalf("no baseline = %s", got)
	}
	if got := clusterCostExpr(&kobiplan.Baseline{ClusterCost: &kobiplan.Money{Monthly: 900}}); got != nodes {
		t.Fatalf("an older baseline = %s", got)
	}
	if got := clusterCostExpr(&kobiplan.Baseline{ClusterCostCovers: kobiplan.CoversBill}); got != clusterBillExpr {
		t.Fatalf("a bill baseline = %s", got)
	}
}
