package api

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
	"github.com/kubebolt/kubebolt/apps/api/internal/kobiplan"
)

// Plans that lead to one another (plans-16). A consolidation plan computed
// on the requests a live right-sizing plan leaves depends on it; that
// right-sizing plan leads to it. The view shows both ends, and the list's
// bill's path adds them up for one cluster: right-sizing frees capacity the
// bill keeps paying for, consolidation removes the node that is then left
// over, and only that moves the bill.

const clusterBillTimeout = 4 * time.Second

// planLink is another plan, as the view names it.
type planLink struct {
	ID              string           `json:"id"`
	Title           string           `json:"title"`
	Kind            kobiplan.Kind    `json:"kind"`
	EffectiveStatus kobiplan.Status  `json:"effectiveStatus"`
	StepCount       int              `json:"stepCount"`
	AppliedCount    int              `json:"appliedCount"`
	Estimated       kobiplan.Savings `json:"estimated"`
}

func linkOf(p kobiplan.Plan, now time.Time) planLink {
	status, _ := effectiveStatus(p, now)
	return planLink{
		ID: p.ID, Title: p.Title, Kind: p.Kind, EffectiveStatus: status,
		StepCount: len(p.Steps), AppliedCount: appliedSteps(p.Steps), Estimated: p.Impact.Estimated,
	}
}

// livePlan: a draft before its expiry, or an exported plan.
func livePlan(p kobiplan.Plan, now time.Time) bool {
	status, _ := effectiveStatus(p, now)
	return status.Live()
}

// planLinks are the plans p depends on and the live consolidation plans that
// depend on p.
func planLinks(store copilot.PlanStore, tenant string, p kobiplan.Plan, now time.Time) (dependsOn, leadsTo []planLink) {
	for _, id := range p.DependsOn {
		if q, err := store.Get(tenant, id); err == nil {
			dependsOn = append(dependsOn, linkOf(*q, now))
		}
	}
	if p.Kind != kobiplan.KindRightsizing {
		return dependsOn, nil
	}
	live, err := store.LiveForCluster(tenant, p.ClusterID)
	if err != nil {
		return dependsOn, nil
	}
	for _, q := range live {
		if q.Kind == kobiplan.KindConsolidation && livePlan(q, now) && slices.Contains(q.DependsOn, p.ID) {
			leadsTo = append(leadsTo, linkOf(q, now))
		}
	}
	return dependsOn, leadsTo
}

// clusterBillNow is the cluster's bill at list prices a month (clusterBillExpr,
// averaged over the last 7 days like the baseline), in the org's currency;
// nil without OpenCost.
func (h *handlers) clusterBillNow(ctx context.Context, clusterID string) *kobiplan.Money {
	ctx, cancel := context.WithTimeout(ctx, clusterBillTimeout)
	defer cancel()
	pricing, _ := h.planPricing(ctx, clusterID)
	if !pricing.Rates.Available() {
		return nil
	}
	q := scopeQueryByTenant(scopeQueryByCluster(fmt.Sprintf(`avg_over_time((%s)[%dd:1h])`, clusterBillExpr, planBaselineDays), clusterID), metricsTenantPinCtx(ctx))
	rows, err := runInstantQuery(ctx, q)
	if err != nil || len(rows) == 0 || math.IsNaN(rows[0].Value) {
		return nil
	}
	return &kobiplan.Money{Monthly: math.Round(rows[0].Value*hoursPerMonth*pricing.Factor*100) / 100, Currency: pricing.Currency, Source: "opencost"}
}

// consolidationBill is the bill a consolidation plan's headline reads before:
// its baseline once exported, else the bill today. Only for a live plan.
func (h *handlers) consolidationBill(ctx context.Context, p kobiplan.Plan, now time.Time) *kobiplan.Money {
	if p.Kind != kobiplan.KindConsolidation {
		return nil
	}
	if b := p.Baseline; b != nil && b.ClusterCost != nil && b.ClusterCostCovers == kobiplan.CoversBill {
		return b.ClusterCost
	}
	if !livePlan(p, now) {
		return nil
	}
	return h.clusterBillNow(ctx, p.ClusterID)
}

// billPath is one cluster's bill and what its live plans do to it.
type billPath struct {
	ClusterID   string `json:"clusterId"`
	ClusterName string `json:"clusterName,omitempty"`
	// Bill is the cluster's bill today at list prices, nil without OpenCost.
	Bill *kobiplan.Money `json:"bill"`
	// Rightsizing are the live right-sizing plans: what they free, priced at
	// list prices, moves the bill by nothing.
	Rightsizing []planLink `json:"rightsizing"`
	// Consolidation are the live consolidation plans, with what their steps
	// still to do or done take off the bill and the pools they shrink.
	Consolidation []billPathConsolidation `json:"consolidation"`
	// Reachable is the bill once the consolidation plans' nodes are gone, nil
	// when it cannot be said in one currency.
	Reachable *kobiplan.Money `json:"reachable"`
	// The fleet's path (no ?cluster=): each cluster with live plans and a
	// bill, the worst-off first, and those without OpenCost prices, which
	// are not in the figures.
	Clusters []billPathCluster `json:"clusters,omitempty"`
	Unpriced []billPathName    `json:"unpriced,omitempty"`
}

// billPathCluster is one cluster in the fleet's path.
type billPathCluster struct {
	ClusterID   string          `json:"clusterId"`
	ClusterName string          `json:"clusterName,omitempty"`
	Bill        *kobiplan.Money `json:"bill"`
	Reachable   *kobiplan.Money `json:"reachable"`
	// Off is what its consolidation plans take off the bill; Nodes the nodes
	// they remove.
	Off   float64 `json:"off"`
	Nodes int     `json:"nodes"`
	Plans int     `json:"plans"`
}

type billPathName struct {
	ClusterID   string `json:"clusterId"`
	ClusterName string `json:"clusterName,omitempty"`
}

// fleetBillPathReads bounds the bills read at once for the fleet's path.
const fleetBillPathReads = 4

type billPathConsolidation struct {
	planLink
	Monthly *kobiplan.Money `json:"monthly"`
	Pools   []billPathPool  `json:"pools"`
}

type billPathPool struct {
	Pool        string `json:"pool"`
	NodesBefore int    `json:"nodesBefore"`
	NodesAfter  int    `json:"nodesAfter"`
}

// GET /plans/bill-path?cluster= (without it, the fleet's)
func (h *handlers) handlePlanBillPath(w http.ResponseWriter, r *http.Request) {
	if !consolidationEdition {
		respondConsolidationNotInEdition(w, "the bill's path")
		return
	}
	store := kobiPlanStore()
	if store == nil {
		respondError(w, http.StatusServiceUnavailable, "plans are not enabled on this install")
		return
	}
	scope := ClusterScopeFrom(r.Context())
	clusterID := scope.Requested()
	if clusterID == "" {
		h.fleetBillPath(w, r, store, scope)
		return
	}
	if !scope.May(clusterID) {
		respondError(w, http.StatusNotFound, "cluster not found")
		return
	}
	live, err := store.LiveForCluster(auth.ContextTenantID(r), clusterID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "could not read the plans")
		return
	}
	now := time.Now()
	out := buildBillPath(clusterID, live, h.clusterBillNow(r.Context(), clusterID), now)
	out.ClusterName = h.clusterNames(r)(clusterID)
	respondJSON(w, http.StatusOK, out)
}

// buildBillPath adds up one cluster's live plans against its bill.
func buildBillPath(clusterID string, live []kobiplan.Plan, bill *kobiplan.Money, now time.Time) billPath {
	out := billPath{ClusterID: clusterID, Bill: bill, Rightsizing: []planLink{}, Consolidation: []billPathConsolidation{}}
	var off float64
	currency := ""
	sameCurrency := true
	for _, p := range live {
		if !livePlan(p, now) {
			continue
		}
		switch p.Kind {
		case kobiplan.KindRightsizing:
			out.Rightsizing = append(out.Rightsizing, linkOf(p, now))
		case kobiplan.KindConsolidation:
			c := billPathConsolidation{planLink: linkOf(p, now), Pools: []billPathPool{}}
			var monthly float64
			priced := false
			for _, s := range p.Steps {
				if s.Action != kobiplan.ActionRemoveNodes || s.Status == kobiplan.StepExpired {
					continue
				}
				if params, err := kobiplan.DecodeRemoveNodes(s.Params); err == nil {
					c.Pools = append(c.Pools, billPathPool{Pool: params.Pool, NodesBefore: params.NodesBefore, NodesAfter: params.NodesAfter})
				}
				if m := s.Impact.Estimated.Money; m != nil {
					if currency == "" {
						currency = m.Currency
					}
					sameCurrency = sameCurrency && m.Currency == currency
					monthly += m.Monthly
					priced = true
				}
			}
			if priced {
				c.Monthly = &kobiplan.Money{Monthly: math.Round(monthly*100) / 100, Currency: currency, Source: "opencost"}
				off += monthly
			}
			out.Consolidation = append(out.Consolidation, c)
		}
	}
	if bill != nil && sameCurrency && (currency == "" || currency == bill.Currency) {
		out.Reachable = &kobiplan.Money{Monthly: math.Round((bill.Monthly-off)*100) / 100, Currency: bill.Currency, Source: bill.Source}
	}
	return out
}

// fleetBillPath is the bill's path of every cluster the caller may read that
// has live plans, added up: the same columns, over the clusters' bills.
func (h *handlers) fleetBillPath(w http.ResponseWriter, r *http.Request, store copilot.PlanStore, scope ClusterScope) {
	plans, err := store.List(auth.ContextTenantID(r), copilot.PlanFilter{Statuses: []kobiplan.Status{kobiplan.StatusDraft, kobiplan.StatusExported}})
	if err != nil {
		respondError(w, http.StatusInternalServerError, "could not read the plans")
		return
	}
	now := time.Now()
	byCluster := map[string][]kobiplan.Plan{}
	for _, p := range plans {
		if p.ClusterID == "" || !scope.May(p.ClusterID) || !livePlan(p, now) {
			continue
		}
		byCluster[p.ClusterID] = append(byCluster[p.ClusterID], p)
	}
	bills := make(map[string]*kobiplan.Money, len(byCluster))
	var mu sync.Mutex
	var wg sync.WaitGroup
	slots := make(chan struct{}, fleetBillPathReads)
	for id := range byCluster {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			b := h.clusterBillNow(r.Context(), id)
			mu.Lock()
			bills[id] = b
			mu.Unlock()
		}()
	}
	wg.Wait()
	respondJSON(w, http.StatusOK, buildFleetBillPath(byCluster, bills, h.clusterNames(r), now))
}

// buildFleetBillPath adds up each cluster's path. A cluster without OpenCost
// prices, or priced in another currency than the first one, is named apart
// and left out of every figure: its plans neither free nor take off anything
// the sum could say.
func buildFleetBillPath(byCluster map[string][]kobiplan.Plan, bills map[string]*kobiplan.Money, name func(string) string, now time.Time) billPath {
	out := billPath{Rightsizing: []planLink{}, Consolidation: []billPathConsolidation{}, Clusters: []billPathCluster{}, Unpriced: []billPathName{}}
	ids := make([]string, 0, len(byCluster))
	for id := range byCluster {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var bill, reachable float64
	currency := ""
	for _, id := range ids {
		path := buildBillPath(id, byCluster[id], bills[id], now)
		if len(path.Rightsizing)+len(path.Consolidation) == 0 {
			continue
		}
		if path.Bill == nil || path.Reachable == nil || (currency != "" && path.Bill.Currency != currency) {
			out.Unpriced = append(out.Unpriced, billPathName{ClusterID: id, ClusterName: name(id)})
			continue
		}
		currency = path.Bill.Currency
		row := billPathCluster{
			ClusterID: id, ClusterName: name(id), Bill: path.Bill, Reachable: path.Reachable,
			Off:   math.Round((path.Bill.Monthly-path.Reachable.Monthly)*100) / 100,
			Plans: len(path.Rightsizing) + len(path.Consolidation),
		}
		for _, c := range path.Consolidation {
			for _, p := range c.Pools {
				row.Nodes += p.NodesBefore - p.NodesAfter
			}
		}
		bill += path.Bill.Monthly
		reachable += path.Reachable.Monthly
		out.Rightsizing = append(out.Rightsizing, path.Rightsizing...)
		out.Consolidation = append(out.Consolidation, path.Consolidation...)
		out.Clusters = append(out.Clusters, row)
	}
	sort.SliceStable(out.Clusters, func(i, j int) bool { return out.Clusters[i].Off > out.Clusters[j].Off })
	if len(out.Clusters) > 0 {
		out.Bill = &kobiplan.Money{Monthly: math.Round(bill*100) / 100, Currency: currency, Source: "opencost"}
		out.Reachable = &kobiplan.Money{Monthly: math.Round(reachable*100) / 100, Currency: currency, Source: "opencost"}
	}
	return out
}
