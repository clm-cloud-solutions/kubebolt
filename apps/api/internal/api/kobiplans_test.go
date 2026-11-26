package api

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
	"github.com/kubebolt/kubebolt/apps/api/internal/kobiplan"
)

// The pod-count query reads the same series as the P95, collapsed the same
// way, and the scoping reaches both of its selectors.
func TestRightSizingPodsQuery_ScopedLikeTheP95(t *testing.T) {
	q := scopeQueryByCluster(rightSizingPodsQuery("0.95"), "c-uid")
	if n := strings.Count(q, `container_cpu_usage_seconds_total{cluster_id="c-uid",`); n != 2 {
		t.Fatalf("%d scoped selectors in %s, want 2", n, q)
	}
	for _, want := range []string{"quantile_over_time(0.95,", "count by (workload_kind, workload_name, namespace, pod)", "[7d:5m]", `"workload_kind", "Deployment", "workload_kind", "ReplicaSet"`} {
		if !strings.Contains(q, want) {
			t.Errorf("query lacks %q: %s", want, q)
		}
	}
}

func TestPodStability(t *testing.T) {
	s := podStability{
		p05: map[string]int64{"ns/Deployment/a": 3, "ns/Deployment/b": 2, "ns/Deployment/c": 3},
		p95: map[string]int64{"ns/Deployment/a": 3, "ns/Deployment/b": 3, "ns/Deployment/c": 3},
	}
	cases := []struct {
		key  string
		pods int
		want bool
	}{
		{"ns/Deployment/a", 3, true},
		{"ns/Deployment/b", 3, false}, // the count moved in the window
		{"ns/Deployment/c", 4, false}, // today is not what the window ran
		{"ns/Deployment/d", 1, false}, // never sampled
		{"ns/Deployment/a", 0, false},
	}
	for _, c := range cases {
		if got := s.stable(c.key, c.pods); got != c.want {
			t.Errorf("stable(%s, %d) = %v, want %v", c.key, c.pods, got, c.want)
		}
	}
}

func nodePricesVM(t *testing.T, cpu, ram string) {
	t.Helper()
	vm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		q := r.URL.Query().Get("query")
		v := ""
		switch {
		case strings.Contains(q, "node_cpu_hourly_cost"):
			v = cpu
		case strings.Contains(q, "node_ram_hourly_cost"):
			v = ram
		}
		if v == "" {
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1700000000,"` + v + `"]}]}}`))
	}))
	t.Cleanup(vm.Close)
	t.Setenv("KUBEBOLT_METRICS_STORAGE_URL", vm.URL)
}

type fixedFX struct {
	rate float64
	ok   bool
}

func (f fixedFX) Rate(context.Context, string, string) (float64, bool) { return f.rate, f.ok }

func withFX(t *testing.T, fx fixedFX) {
	prev := planFX
	planFX = fx
	t.Cleanup(func() { planFX = prev })
}

func TestPlanPricing(t *testing.T) {
	nodePricesVM(t, "0.03", "0.004")
	ctx := auth.WithTenantID(context.Background(), "org-1")

	t.Run("single-tenant keeps OpenCost's currency", func(t *testing.T) {
		p, notes := (&handlers{}).planPricing(ctx, "c-uid")
		if p.Rates.CPUCoreHourly != 0.03 || p.Rates.RAMGiBHourly != 0.004 || p.Currency != "usd" || p.Factor != 1 || len(notes) != 0 {
			t.Fatalf("pricing = %+v, notes %v", p, notes)
		}
	})
	t.Run("converted into the org's currency", func(t *testing.T) {
		withFX(t, fixedFX{rate: 0.92, ok: true})
		withPlanCurrency(t, "eur")
		p, notes := (&handlers{}).planPricing(ctx, "c-uid")
		if p.Currency != "eur" || math.Abs(p.Factor-0.92) > 1e-9 || len(notes) != 0 {
			t.Fatalf("pricing = %+v, notes %v", p, notes)
		}
	})
	t.Run("no rate leaves the amount unconverted and says so", func(t *testing.T) {
		withFX(t, fixedFX{})
		withPlanCurrency(t, "eur")
		p, notes := (&handlers{}).planPricing(ctx, "c-uid")
		if p.Currency != "usd" || p.Factor != 1 || len(notes) != 1 || !strings.Contains(notes[0], "no exchange rate to EUR") {
			t.Fatalf("pricing = %+v, notes %v", p, notes)
		}
	})
}

// withPlanCurrency makes the edition's currency seam answer cur for one test.
func withPlanCurrency(t *testing.T, cur string) {
	t.Helper()
	prev := planCurrency
	planCurrency = func(*handlers, context.Context) string { return cur }
	t.Cleanup(func() { planCurrency = prev })
}

func TestPlanPricing_NoOpenCostNoMoney(t *testing.T) {
	nodePricesVM(t, "", "")
	withFX(t, fixedFX{rate: 0.92, ok: true})
	withPlanCurrency(t, "eur")
	p, notes := (&handlers{}).planPricing(auth.WithTenantID(context.Background(), "org-1"), "c-uid")
	if p.Rates.Available() || len(notes) != 0 {
		t.Fatalf("pricing = %+v, notes %v", p, notes)
	}
}

// Free's one live plan per cluster is a multi-tenant limit; a self-hosted
// install, whose org has no plan, never has it.
func TestOneLivePlanPerCluster(t *testing.T) {
	ctx := auth.WithTenantID(context.Background(), "org-1")
	free := &handlers{tenantsStore: &fakeTenantStore{tenant: &auth.Tenant{ID: "org-1", Plan: auth.PlanFree}}}
	team := &handlers{tenantsStore: &fakeTenantStore{tenant: &auth.Tenant{ID: "org-1", Plan: auth.PlanTeam}}}

	prev := auth.MultiTenantEnabled
	t.Cleanup(func() { auth.MultiTenantEnabled = prev })

	auth.MultiTenantEnabled = false
	if free.oneLivePlanPerCluster(ctx) || (&handlers{}).oneLivePlanPerCluster(ctx) {
		t.Fatal("a single-tenant install got the Free limit")
	}
	auth.MultiTenantEnabled = true
	if !free.oneLivePlanPerCluster(ctx) {
		t.Fatal("a Free org has no limit")
	}
	if team.oneLivePlanPerCluster(ctx) {
		t.Fatal("a Team org got the Free limit")
	}
}

// Consolidation plans are for paid plans: a Free org is told so before
// anything is read, a self-hosted install has them.
func TestConsolidationOnPlan(t *testing.T) {
	withConsolidationEdition(t, true)
	ctx := auth.WithTenantID(context.Background(), "org-1")
	free := &handlers{tenantsStore: &fakeTenantStore{tenant: &auth.Tenant{ID: "org-1", Plan: auth.PlanFree}}}
	team := &handlers{tenantsStore: &fakeTenantStore{tenant: &auth.Tenant{ID: "org-1", Plan: auth.PlanTeam}}}

	prev := auth.MultiTenantEnabled
	t.Cleanup(func() { auth.MultiTenantEnabled = prev })

	auth.MultiTenantEnabled = false
	if !free.consolidationOnPlan(ctx) || !(&handlers{}).consolidationOnPlan(ctx) {
		t.Fatal("a self-hosted install has every plan kind")
	}
	auth.MultiTenantEnabled = true
	if free.consolidationOnPlan(ctx) || !team.consolidationOnPlan(ctx) {
		t.Fatal("consolidation is for paid plans")
	}
	// Refused before the cluster is read: no manager is needed to say it.
	src := planSource{h: free}
	if _, err := src.CreatePlan(ctx, copilot.CreatePlanRequest{Kind: kobiplan.KindConsolidation}); !errors.Is(err, errConsolidationNotOnPlan) {
		t.Fatalf("a Free consolidation request = %v", err)
	}
}

// What create_plan does with the cluster's live plans (plans-06, P21).
func TestLiveAnswer(t *testing.T) {
	now := time.Now().UTC()
	whole := routePlan("pln_whole", "c-dev", now.Add(-time.Hour), kobiplan.RiskLow) // whole cluster, kyverno/cleanup pending
	expired := routePlan("pln_old", "c-dev", now.Add(-8*24*time.Hour), kobiplan.RiskLow)

	// Free: the live plan comes back, whatever its scope; an expired draft
	// does not count.
	if p, why, _ := liveAnswer([]kobiplan.Plan{whole}, kobiplan.KindRightsizing, kobiplan.Scope{Namespaces: []string{"x"}}, true, now); p == nil || p.ID != "pln_whole" || why != copilot.ExistingOnePerCluster {
		t.Fatalf("free = %v %q", p, why)
	}
	if p, _, _ := liveAnswer([]kobiplan.Plan{expired}, kobiplan.KindRightsizing, kobiplan.Scope{}, true, now); p != nil {
		t.Fatal("an expired draft held Free's slot")
	}
	applied := routePlan("pln_done", "c-dev", now, kobiplan.RiskLow)
	applied.Status = kobiplan.StatusExported
	applied.Steps[0].Status = kobiplan.StepApplied
	if p, _, _ := liveAnswer([]kobiplan.Plan{applied}, kobiplan.KindRightsizing, kobiplan.Scope{}, true, now); p != nil {
		t.Fatal("a plan with every step applied held Free's slot")
	}

	// The same request again: the plan that answers it.
	if p, why, _ := liveAnswer([]kobiplan.Plan{expired, whole}, kobiplan.KindRightsizing, kobiplan.Scope{Namespaces: []string{}}, false, now); p == nil || p.ID != "pln_whole" || why != copilot.ExistingSameScope {
		t.Fatalf("same scope = %v %q", p, why)
	}

	// Another scope: a new plan, without what another one has pending.
	p, _, planned := liveAnswer([]kobiplan.Plan{whole}, kobiplan.KindRightsizing, kobiplan.Scope{Namespaces: []string{"kyverno"}}, false, now)
	if p != nil || planned[kobiplan.TargetKey("deployments", "kyverno", "cleanup")] != whole.Title {
		t.Fatalf("another scope = %v, planned %v", p, planned)
	}

	// A plan with nothing left to do answers nothing and holds nothing.
	done := whole
	done.Steps = []kobiplan.Step{whole.Steps[0]}
	done.Steps[0].Status = kobiplan.StepApplied
	p, _, planned = liveAnswer([]kobiplan.Plan{done}, kobiplan.KindRightsizing, kobiplan.Scope{}, false, now)
	if p != nil || len(planned) != 0 {
		t.Fatalf("a finished plan = %v, planned %v", p, planned)
	}

	// A consolidation request is not answered by a right-sizing plan of the
	// same scope (it builds on it), only by a consolidation plan.
	if p, _, _ := liveAnswer([]kobiplan.Plan{whole}, kobiplan.KindConsolidation, kobiplan.Scope{}, false, now); p != nil {
		t.Fatalf("a right-sizing plan answered a consolidation request: %v", p.ID)
	}
	pools := whole
	pools.ID, pools.Kind = "pln_pools", kobiplan.KindConsolidation
	if p, why, _ := liveAnswer([]kobiplan.Plan{whole, pools}, kobiplan.KindConsolidation, kobiplan.Scope{}, false, now); p == nil || p.ID != "pln_pools" || why != copilot.ExistingSameScope {
		t.Fatalf("consolidation again = %v %q", p, why)
	}
}

func TestPlanTitle(t *testing.T) {
	cases := map[string]copilot.CreatePlanRequest{
		"Plan de ahorro":              {Title: "Plan de ahorro", Namespaces: []string{"x"}},
		"Right-sizing: payments, web": {Namespaces: []string{"payments", "web"}},
		"Right-sizing: shop/api":      {Workloads: []string{"shop/api"}},
		"Right-sizing: whole cluster": {},
	}
	for want, req := range cases {
		if got := planTitle(req); got != want {
			t.Errorf("planTitle(%+v) = %q, want %q", req, got, want)
		}
	}
}

func TestPlanSourceFor_NeedsAStore(t *testing.T) {
	prev := planStorePtr.Load()
	t.Cleanup(func() { planStorePtr.Store(prev) })
	planStorePtr.Store(nil)
	if src := (&handlers{}).planSourceFor("org", "u", "ana", "conv"); src != nil {
		t.Fatal("a plan source without a store")
	}
}
