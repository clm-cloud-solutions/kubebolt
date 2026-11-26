package kobiplan

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func consolidationInput(nodes []NodeFacts, current, planned []PodFacts) ConsolidationInput {
	now := time.Date(2026, 11, 26, 11, 2, 0, 0, time.UTC)
	return ConsolidationInput{
		ID: "pln_c", OrgID: "org", ClusterID: "c1", Title: "Consolidation · prod-eks",
		Source: Source{Initiator: InitiatorChat, UserID: "u1", ConversationID: "cv1"},
		Nodes:  nodes, Current: current, Planned: planned,
		Currency: "eur", PriceSource: "opencost", WindowDays: 7,
		ComputedAt: now, Now: now,
	}
}

// The Sucal shape: one export-only step for the pool, its node named with
// its instance, the list price as the figure and the pool as precondition.
func TestBuildConsolidation_Step(t *testing.T) {
	nodes := pool(5)
	nodes[4].ProviderID = "aws:///us-east-1b/i-0abc"
	pods := withDaemons(nodes, []PodFacts{
		pod("node-1", "api-1", 2000, 8), pod("node-2", "api-2", 1900, 7), pod("node-3", "web-1", 2100, 8),
		pod("node-4", "web-2", 1800, 8), pod("node-5", "cron-1", 600, 2), pod("node-5", "cron-2", 400, 2),
	})
	p, err := BuildConsolidation(consolidationInput(nodes, pods, nil))
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != KindConsolidation || len(p.Steps) != 1 || len(p.Excluded) != 0 || p.DependsOn != nil {
		t.Fatalf("plan = %+v", p)
	}
	s := p.Steps[0]
	if s.Action != ActionRemoveNodes || !s.ExportOnly || s.Target.Type != NodePoolType || s.Target.Name != "general" || s.Target.Owner.Kind != OwnerUnknown {
		t.Fatalf("step = %+v", s)
	}
	params, err := DecodeRemoveNodes(s.Params)
	if err != nil {
		t.Fatal(err)
	}
	if params.NodesBefore != 5 || params.NodesAfter != 4 || len(params.Nodes) != 1 || params.Nodes[0].Name != "node-5" || params.Nodes[0].ProviderID != "aws:///us-east-1b/i-0abc" {
		t.Fatalf("params = %+v", params)
	}
	if m := s.Impact.Estimated.Money; m == nil || m.Monthly != 129.40 || m.Currency != "eur" {
		t.Fatalf("money = %+v", m)
	}
	if s.Impact.Estimated.CPUMilli != 4000 || s.Impact.Estimated.MemBytes != 16*gib {
		t.Fatalf("units = %+v", s.Impact.Estimated)
	}
	if p.Impact.Estimated.Money == nil || p.Impact.Estimated.Money.Monthly != 129.40 {
		t.Fatalf("plan money = %+v", p.Impact.Estimated)
	}
	if s.Precondition.SpecHash != PoolHash(nodes) || s.Risk != RiskMedium || !slices.Equal(s.RiskReasons, []string{RiskReasonLessHeadroom}) {
		t.Fatalf("precondition / risk = %+v %s %v", s.Precondition, s.Risk, s.RiskReasons)
	}
	if s.Verification.Window != "7d" || !slices.Contains(s.Verification.Checks, "no_pending_pods") {
		t.Fatalf("verification = %+v", s.Verification)
	}
	if !strings.Contains(string(s.Evidence), `"scenario":"current"`) || !strings.Contains(string(s.Evidence), `"monthly":129.4`) {
		t.Fatalf("evidence = %s", s.Evidence)
	}
}

// A node left over only with the requests the right-sizing plan leaves: the
// plan depends on it and says so; with nothing more freed, it does not.
func TestBuildConsolidation_Planned(t *testing.T) {
	nodes := pool(4)
	today := withDaemons(nodes, []PodFacts{
		pod("node-1", "a", 2400, 9), pod("node-2", "b", 2400, 9), pod("node-3", "c", 2400, 9), pod("node-4", "d", 1600, 6),
	})
	after := withDaemons(nodes, []PodFacts{
		pod("node-1", "a", 1200, 5), pod("node-2", "b", 1200, 5), pod("node-3", "c", 1200, 5), pod("node-4", "d", 800, 3),
	})
	in := consolidationInput(nodes, today, after)
	in.DependsOn = []string{"pln_rs"}
	p, err := BuildConsolidation(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Steps) != 1 || !slices.Equal(p.DependsOn, []string{"pln_rs"}) {
		t.Fatalf("plan = %+v", p)
	}
	s := p.Steps[0]
	if !slices.Contains(s.RiskReasons, RiskReasonDependsOnPlan) || !strings.Contains(string(s.Evidence), `"scenario":"planned","removableToday":0`) {
		t.Fatalf("step = %+v %s", s.RiskReasons, s.Evidence)
	}

	same := consolidationInput(nodes, after, after)
	same.DependsOn = []string{"pln_rs"}
	if p, _ := BuildConsolidation(same); p.DependsOn != nil {
		t.Fatalf("today's requests free as much: no dependency, got %v", p.DependsOn)
	}
}

// A pool that keeps every node is an exclusion with its reason in words, and
// a plan with nothing to remove has no money and says why.
func TestBuildConsolidation_Exclusions(t *testing.T) {
	small := pool(3)
	batch := pool(4)
	for i := range batch {
		batch[i].Name = "batch-" + batch[i].Name
		batch[i].Pool = "batch"
	}
	// The least loaded node of the pool runs a pod its PDB keeps in place, and
	// no other has room for the pods of another.
	loader := pod("batch-node-1", "loader", 200, 1)
	loader.Blocker, loader.Owner = BlockPDB, "etl/loader"
	full := []PodFacts{loader, pod("batch-node-2", "a", 3100, 11), pod("batch-node-3", "b", 3100, 11), pod("batch-node-4", "c", 3100, 11)}
	p, err := BuildConsolidation(consolidationInput(append(small, batch...), withDaemons(small, full), nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Steps) != 0 || p.Impact.Estimated.MoneyUnavailable != MoneyNoCostData || len(p.Excluded) != 2 {
		t.Fatalf("plan = %+v", p)
	}
	if e := p.Excluded[0]; e.Target.Name != "batch" || e.Reason != ReasonPoolBlocked || !strings.Contains(e.Detail, "PodDisruptionBudget etl/loader") {
		t.Fatalf("batch = %+v", e)
	}
	if e := p.Excluded[1]; e.Target.Name != "general" || e.Reason != ReasonPoolMinNodes {
		t.Fatalf("general = %+v", e)
	}
}

// Karpenter sizes its own pools; under a week of history the step is high
// risk; nodes with no pool label are one group; a node without a price
// leaves the plan without money.
func TestBuildConsolidation_OwnersRiskAndPrices(t *testing.T) {
	nodes := pool(5)
	for i := range nodes {
		nodes[i].Pool, nodes[i].PoolSource = "default", PoolKarpenter
	}
	nodes[4].Monthly = nil
	pods := withDaemons(nodes, []PodFacts{pod("node-1", "a", 1000, 4), pod("node-2", "b", 1000, 4), pod("node-3", "c", 1000, 4), pod("node-4", "d", 1000, 4)})
	in := consolidationInput(nodes, pods, nil)
	in.WindowDays = 3.2
	p, err := BuildConsolidation(in)
	if err != nil {
		t.Fatal(err)
	}
	s := p.Steps[0]
	if o := s.Target.Owner; o.Kind != OwnerOperator || o.Ref != "karpenter" {
		t.Fatalf("owner = %+v", o)
	}
	if s.Risk != RiskHigh || !slices.Contains(s.RiskReasons, RiskReasonShortHistory) || !p.Engine.LowConfidence || p.Engine.WindowDays != 3.2 {
		t.Fatalf("risk = %s %v, engine %+v", s.Risk, s.RiskReasons, p.Engine)
	}
	if params, _ := DecodeRemoveNodes(s.Params); slices.Contains(nodeNames(params.Nodes), "node-5") && s.Impact.Estimated.Money != nil {
		t.Fatal("a removed node without a price leaves the step without money")
	}

	bare := pool(4)
	for i := range bare {
		bare[i].Pool, bare[i].PoolSource = "", PoolNone
	}
	p, err = BuildConsolidation(consolidationInput(bare, withDaemons(bare, nil), nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Steps) != 1 || p.Steps[0].Target.Name != UnlabelledPool {
		t.Fatalf("unlabelled = %+v", p.Steps)
	}
}

// Validate keeps remove_nodes to consolidation plans, export-only, on a pool,
// with params that add up.
func TestValidate_RemoveNodes(t *testing.T) {
	nodes := pool(5)
	p, err := BuildConsolidation(consolidationInput(nodes, withDaemons(nodes, nil), nil))
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		mutate func(*Plan)
		want   string
	}{
		"wrong kind": {func(p *Plan) { p.Kind = KindRightsizing }, "does not remove nodes"},
		"executable": {func(p *Plan) { p.Steps[0].ExportOnly = false }, "export-only"},
		"not a pool": {func(p *Plan) { p.Steps[0].Target.Type = "deployments" }, "applies to a node pool"},
		"bad count": {func(p *Plan) {
			p.Steps[0].Params = []byte(`{"pool":"general","poolSource":"eks","autoscaler":"none","nodesBefore":5,"nodesAfter":4,"nodes":[]}`)
		}, "no node"},
		"extra field": {func(p *Plan) { p.Steps[0].Params = []byte(`{"pool":"general","drain":true}`) }, "unknown field"},
	} {
		q := p
		q.Steps = slices.Clone(p.Steps)
		c.mutate(&q)
		q.ContentHash = ""
		if err := q.Validate(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func nodeNames(ns []RemovedNode) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = n.Name
	}
	return out
}

// A cordoned node takes no pods but is still in the pool: the step's sizes
// and its precondition count it, so cordoning or verifying does not expire it.
func TestBuildConsolidation_CordonedNodeStaysInThePool(t *testing.T) {
	nodes := pool(6)
	nodes[5].Unschedulable = true
	pods := withDaemons(nodes[:5], []PodFacts{
		pod("node-1", "a", 2000, 8), pod("node-2", "b", 1900, 7), pod("node-3", "c", 2100, 8), pod("node-4", "d", 1800, 8), pod("node-5", "e", 600, 2),
	})
	p, err := BuildConsolidation(consolidationInput(nodes, pods, nil))
	if err != nil {
		t.Fatal(err)
	}
	params, _ := DecodeRemoveNodes(p.Steps[0].Params)
	if params.NodesBefore != 6 || params.NodesAfter != 5 || p.Steps[0].Precondition.SpecHash != PoolHash(nodes) {
		t.Fatalf("params = %+v", params)
	}
}
