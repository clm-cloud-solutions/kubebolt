package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/kobiplan"
)

// A consolidation plan that depends on a right-sizing plan, both live.
func linkedPlans(now time.Time) (kobiplan.Plan, kobiplan.Plan) {
	rs := routePlan("pln_rs", "c-dev", now.Add(-2*time.Hour), kobiplan.RiskLow)
	rs.Title = "Right-sizing · dev"
	cons := routePlan("pln_cons", "c-dev", now.Add(-time.Hour), kobiplan.RiskMedium)
	cons.Title, cons.Kind, cons.DependsOn = "Consolidation · dev", kobiplan.KindConsolidation, []string{"pln_rs"}
	params, _ := json.Marshal(kobiplan.RemoveNodesParams{Pool: "general", PoolSource: kobiplan.PoolEKS, Autoscaler: kobiplan.AutoscalerNone,
		NodesBefore: 5, NodesAfter: 4, Nodes: []kobiplan.RemovedNode{{Name: "n5"}}})
	cons.Engine.Name = "consolidation"
	cons.Steps = []kobiplan.Step{{
		ID: "s1", Order: 1, Action: kobiplan.ActionRemoveNodes, ExportOnly: true, ExportOnlyReason: "infrastructure",
		Target:       kobiplan.Target{Type: kobiplan.NodePoolType, Name: "general", Owner: &kobiplan.Owner{Kind: kobiplan.OwnerUnknown}},
		Params:       params,
		Precondition: kobiplan.Precondition{SpecHash: "sha256:pool"},
		Risk:         kobiplan.RiskMedium, RiskReasons: []string{}, Status: kobiplan.StepPending,
		Impact: kobiplan.Impact{Estimated: kobiplan.Savings{CPUMilli: 4000, Money: &kobiplan.Money{Monthly: 129.4, Currency: "eur"}}},
	}}
	cons.Impact.Estimated = kobiplan.Savings{CPUMilli: 4000, Money: &kobiplan.Money{Monthly: 129.4, Currency: "eur"}}
	return rs, cons
}

// The view names both ends of the link; the list names them on each row.
func TestPlanLinks(t *testing.T) {
	now := time.Now().UTC()
	rs, cons := linkedPlans(now)
	withPlanStore(t, rs, cons)

	_, body := planRequest(t, "/plans/pln_cons", ClusterScope{})
	deps, _ := body["dependsOnPlans"].([]any)
	if len(deps) != 1 || deps[0].(map[string]any)["title"] != "Right-sizing · dev" || deps[0].(map[string]any)["stepCount"] != float64(1) {
		t.Fatalf("dependsOnPlans = %v", body["dependsOnPlans"])
	}
	_, body = planRequest(t, "/plans/pln_rs", ClusterScope{})
	leads, _ := body["leadsTo"].([]any)
	if len(leads) != 1 || leads[0].(map[string]any)["id"] != "pln_cons" {
		t.Fatalf("leadsTo = %v", body["leadsTo"])
	}

	_, list := planRequest(t, "/plans", ClusterScope{})
	rows := map[string]map[string]any{}
	for _, r := range list["plans"].([]any) {
		row := r.(map[string]any)
		rows[row["id"].(string)] = row
	}
	if d := rows["pln_cons"]["dependsOn"].([]any); d[0].(map[string]any)["title"] != "Right-sizing · dev" {
		t.Fatalf("list dependsOn = %v", d)
	}
	if l := rows["pln_rs"]["leadsTo"].([]any); l[0].(map[string]any)["title"] != "Consolidation · dev" {
		t.Fatalf("list leadsTo = %v", l)
	}
	if rows["pln_cons"]["nodesRemoved"] != float64(1) {
		t.Fatalf("nodesRemoved = %v", rows["pln_cons"]["nodesRemoved"])
	}
}

// The bill's path adds up one cluster's live plans: right-sizing moves the
// bill by nothing, consolidation by its nodes' list price.
func TestBuildBillPath(t *testing.T) {
	now := time.Now().UTC()
	rs, cons := linkedPlans(now)
	done := cons
	done.ID, done.Status, done.ExpiredReason = "pln_gone", kobiplan.StatusExpired, kobiplan.ExpiredDiscarded
	out := buildBillPath("c-dev", []kobiplan.Plan{rs, cons, done}, &kobiplan.Money{Monthly: 1210, Currency: "eur"}, now)
	if len(out.Rightsizing) != 1 || len(out.Consolidation) != 1 {
		t.Fatalf("path = %+v", out)
	}
	c := out.Consolidation[0]
	if c.Monthly == nil || c.Monthly.Monthly != 129.4 || len(c.Pools) != 1 || c.Pools[0].NodesAfter != 4 {
		t.Fatalf("consolidation = %+v", c)
	}
	if out.Reachable == nil || out.Reachable.Monthly != 1080.6 {
		t.Fatalf("reachable = %+v", out.Reachable)
	}
	if usd := buildBillPath("c-dev", []kobiplan.Plan{cons}, &kobiplan.Money{Monthly: 1300, Currency: "usd"}, now); usd.Reachable != nil {
		t.Fatal("a bill in another currency than the nodes' prices has no reachable figure")
	}
	if none := buildBillPath("c-dev", nil, nil, now); none.Reachable != nil || none.Rightsizing == nil || none.Consolidation == nil {
		t.Fatalf("empty path = %+v", none)
	}
}

// The route wants a cluster the caller may read, or none: the fleet's.
func TestPlanBillPathRoute(t *testing.T) {
	withConsolidationEdition(t, true)
	withPlanStore(t)
	h := &handlers{}
	for _, c := range []struct {
		scope ClusterScope
		want  int
	}{
		{ClusterScope{}, http.StatusOK}, // the fleet's
		{ClusterScope{requested: "c-dev", narrowed: true, allowed: map[string]bool{"c-other": true}}, http.StatusNotFound},
		{ClusterScope{requested: "c-dev"}, http.StatusOK},
	} {
		if code, _ := billPathRequest(t, h, c.scope); code != c.want {
			t.Errorf("scope %+v = %d, want %d", c.scope, code, c.want)
		}
	}
}

func billPathRequest(t *testing.T, h *handlers, scope ClusterScope) (int, map[string]any) {
	t.Helper()
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := auth.WithTenantID(req.Context(), "org-1")
			ctx = context.WithValue(ctx, clusterScopeKey, scope)
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	r.Get("/plans/bill-path", h.handlePlanBillPath)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/plans/bill-path", nil))
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body
}

// onCluster moves a plan, and its id, to another cluster; money rewrites
// what its steps take off the bill.
func onCluster(p kobiplan.Plan, cluster string, money *kobiplan.Money) kobiplan.Plan {
	p.ID, p.ClusterID = p.ID+"_"+cluster, cluster
	p.Steps = slices.Clone(p.Steps)
	for i := range p.Steps {
		if p.Steps[i].Action == kobiplan.ActionRemoveNodes {
			p.Steps[i].Impact.Estimated.Money = money
		}
	}
	return p
}

// The fleet's path adds up the clusters priced in one currency, the one that
// takes most off first, and names the others apart.
func TestBuildFleetBillPath(t *testing.T) {
	now := time.Now().UTC()
	rs, cons := linkedPlans(now)
	byCluster := map[string][]kobiplan.Plan{
		"c-dev": {rs, cons},
		"c-lab": {onCluster(cons, "c-lab", &kobiplan.Money{Monthly: 200, Currency: "eur"})},
		"c-old": {onCluster(rs, "c-old", nil)},
		"c-usd": {onCluster(cons, "c-usd", &kobiplan.Money{Monthly: 90, Currency: "usd"})},
	}
	bills := map[string]*kobiplan.Money{
		"c-dev": {Monthly: 1210, Currency: "eur"},
		"c-lab": {Monthly: 800, Currency: "eur"},
		"c-usd": {Monthly: 700, Currency: "usd"},
	}
	names := map[string]string{"c-dev": "dev", "c-lab": "lab", "c-old": "old", "c-usd": "usd"}
	out := buildFleetBillPath(byCluster, bills, func(id string) string { return names[id] }, now)

	if out.Bill == nil || out.Bill.Monthly != 2010 || out.Bill.Currency != "eur" {
		t.Fatalf("bill = %+v", out.Bill)
	}
	if out.Reachable == nil || out.Reachable.Monthly != 1680.6 {
		t.Fatalf("reachable = %+v", out.Reachable)
	}
	if len(out.Clusters) != 2 || out.Clusters[0].ClusterName != "lab" || out.Clusters[0].Off != 200 || out.Clusters[1].Off != 129.4 {
		t.Fatalf("clusters = %+v", out.Clusters)
	}
	if dev := out.Clusters[1]; dev.Nodes != 1 || dev.Plans != 2 || dev.Reachable.Monthly != 1080.6 {
		t.Fatalf("dev = %+v", dev)
	}
	if len(out.Unpriced) != 2 || out.Unpriced[0].ClusterName != "old" || out.Unpriced[1].ClusterName != "usd" {
		t.Fatalf("unpriced = %+v", out.Unpriced)
	}
	// Only the priced clusters' plans are in the columns.
	if len(out.Rightsizing) != 1 || len(out.Consolidation) != 2 {
		t.Fatalf("columns = %d right-sizing, %d consolidation", len(out.Rightsizing), len(out.Consolidation))
	}

	if none := buildFleetBillPath(map[string][]kobiplan.Plan{"c-old": {onCluster(rs, "c-old", nil)}}, nil, func(string) string { return "" }, now); none.Bill != nil || none.Reachable != nil || len(none.Unpriced) != 1 {
		t.Fatalf("no cluster priced = %+v", none)
	}
}

// The fleet's route reads only the clusters the caller may, and the plans
// still live.
func TestFleetBillPathRoute(t *testing.T) {
	withConsolidationEdition(t, true)
	now := time.Now().UTC()
	rs, cons := linkedPlans(now)
	gone := onCluster(cons, "c-gone", nil)
	gone.Status, gone.ExpiredReason = kobiplan.StatusExpired, kobiplan.ExpiredDiscarded
	withPlanStore(t, rs, cons, onCluster(cons, "c-lab", nil), gone)

	code, body := billPathRequest(t, &handlers{}, ClusterScope{narrowed: true, allowed: map[string]bool{"c-dev": true, "c-gone": true}})
	if code != http.StatusOK {
		t.Fatalf("code = %d", code)
	}
	// No OpenCost here: the one cluster read is named, unpriced.
	unpriced, _ := body["unpriced"].([]any)
	if len(unpriced) != 1 || unpriced[0].(map[string]any)["clusterId"] != "c-dev" || body["bill"] != nil {
		t.Fatalf("body = %v", body)
	}
}
