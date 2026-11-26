package kobiplan

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"
)

// The consolidation plan builder (plans-16): the engine's verdict per pool
// becomes one export-only step per pool with nodes to spare, and an
// exclusion, with its reason, for every pool that keeps them all. Freed
// requests lower the bill only when a node goes (26-nov): this is the plan
// that says which one, and what the bill would fall by at list prices.

// KindConsolidation: fewer nodes of the same type.
const KindConsolidation Kind = "consolidation"

// ActionRemoveNodes takes nodes out of a pool. It is not one of Kobi's verbs:
// the pool's size lives in the customer's infrastructure (Terraform, eksctl,
// the console), so the step is exported and never executed.
const ActionRemoveNodes Action = "remove_nodes"

// NodePoolType is the target type of a consolidation step or exclusion.
const NodePoolType = "nodepools"

// UnlabelledPool names the pool of nodes that carry no pool label.
const UnlabelledPool = "nodes"

// Exclusion reasons of a consolidation plan: why a pool keeps every node.
const (
	ReasonPoolMinNodes ExclusionReason = "pool_min_nodes"
	ReasonPoolHeadroom ExclusionReason = "pool_headroom"
	ReasonPoolBlocked  ExclusionReason = "pool_blocked"
)

// Risk reasons of a consolidation step.
const (
	// RiskReasonLessHeadroom: one node less is less room to absorb a failure
	// or a spike, whatever the engine measured.
	RiskReasonLessHeadroom = "less_headroom"
	// RiskReasonShortHistory: the usage window is under a week, so a weekly
	// or monthly peak may not be in it.
	RiskReasonShortHistory = "short_history"
	// RiskReasonDependsOnPlan: the node is left over only with the requests
	// a live right-sizing plan leaves; until they are applied it is not.
	RiskReasonDependsOnPlan = "depends_on_plan"
)

// Scenarios a pool's verdict is computed on.
const (
	ScenarioCurrent = "current"
	ScenarioPlanned = "planned"
)

// consolidationChecks run over the step's window once its nodes are gone.
var consolidationChecks = []string{CheckPoolHeld, CheckNoPendingPods, CheckNoPressureEvictions, CheckNoOOMKilled}

// ConsolidationWindow is how long a removal is watched: a week, so a weekly
// peak is in it.
const ConsolidationWindow = "7d"

// FullWindowDays is the usage history a verdict wants; less is low
// confidence.
const FullWindowDays = 7

// RemoveNodesParams is a consolidation step's change: the nodes it suggests
// taking out of the pool, least loaded first. No prices: they move with the
// list, not with what the plan proposes (ContentHash).
type RemoveNodesParams struct {
	Pool        string        `json:"pool"`
	PoolSource  string        `json:"poolSource"`
	Autoscaler  string        `json:"autoscaler"`
	NodesBefore int           `json:"nodesBefore"`
	NodesAfter  int           `json:"nodesAfter"`
	Nodes       []RemovedNode `json:"nodes"`
}

// RemovedNode is one node a step suggests removing.
type RemovedNode struct {
	Name         string `json:"name"`
	Zone         string `json:"zone,omitempty"`
	InstanceType string `json:"instanceType,omitempty"`
	ProviderID   string `json:"providerId,omitempty"`
}

// PoolBefore is the pool as the plan saw it: the precondition, in words.
type PoolBefore struct {
	Nodes         int      `json:"nodes"`
	InstanceTypes []string `json:"instanceTypes"`
	Zones         []string `json:"zones"`
}

// ConsolidationEvidence is why the step's nodes are left over.
type ConsolidationEvidence struct {
	Scenario string `json:"scenario"`
	// RemovableToday is how many nodes the requests of today alone free.
	RemovableToday int         `json:"removableToday"`
	Ceiling        float64     `json:"ceiling"`
	MinNodes       int         `json:"minNodes"`
	WindowDays     float64     `json:"windowDays,omitempty"`
	Loads          []NodeLoad  `json:"loads"`
	Blocked        []NodeBlock `json:"blocked,omitempty"`
}

// DecodeRemoveNodes reads remove_nodes params, refusing unknown fields.
func DecodeRemoveNodes(raw json.RawMessage) (RemoveNodesParams, error) {
	var p RemoveNodesParams
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return p, fmt.Errorf("remove_nodes params: %w", err)
	}
	return p, nil
}

func (p RemoveNodesParams) validate() []string {
	var problems []string
	if p.Pool == "" {
		problems = append(problems, "remove_nodes: no pool")
	}
	if len(p.Nodes) == 0 {
		problems = append(problems, "remove_nodes: no node")
	}
	if p.NodesAfter != p.NodesBefore-len(p.Nodes) || p.NodesAfter < 1 {
		problems = append(problems, fmt.Sprintf("remove_nodes: %d nodes less %d is not %d", p.NodesBefore, len(p.Nodes), p.NodesAfter))
	}
	return problems
}

// PoolHash is a consolidation step's precondition: the pool's nodes and
// their types. A node added, replaced or resized changes it.
func PoolHash(nodes []NodeFacts) string {
	type entry struct {
		Name string `json:"name"`
		Type string `json:"type,omitempty"`
	}
	entries := make([]entry, 0, len(nodes))
	for _, n := range nodes {
		entries = append(entries, entry{n.Name, n.InstanceType})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return hashJSON(entries)
}

// ConsolidationInput is everything a consolidation plan is built from.
type ConsolidationInput struct {
	ID        string
	OrgID     string
	ClusterID string
	Title     string
	Source    Source
	// Nodes carry their monthly list price already in Currency.
	Nodes []NodeFacts
	// Current is every pod as it asks today; Planned as it would ask once
	// the live right-sizing plans' pending steps are applied, nil without
	// any. Both are the larger of request and usage.
	Current []PodFacts
	Planned []PodFacts
	// DependsOn are the live right-sizing plans Planned comes from.
	DependsOn  []string
	Autoscaler AutoscalerFacts
	Currency   string
	// PriceSource is where the node prices came from ("opencost").
	PriceSource string
	// WindowDays is the usage history the pods' needs were read over.
	WindowDays float64
	ComputedAt time.Time
	Notes      []string
	Now        time.Time
}

// BuildConsolidation builds the plan. A pool keeps its verdict on today's
// requests unless the live right-sizing plans free more of it; the plan then
// depends on them, and its step waits until they are in the cluster. It may
// have no steps: the caller stores nothing and says why.
func BuildConsolidation(in ConsolidationInput) (Plan, error) {
	p := Plan{
		ID: in.ID, OrgID: in.OrgID, ClusterID: in.ClusterID, Title: in.Title,
		Kind:      KindConsolidation,
		Scope:     normalizeScope(Scope{}),
		Version:   1,
		Status:    StatusDraft,
		CreatedAt: in.Now,
		Source:    in.Source,
		ExpiresAt: in.Now.Add(PlanTTL),
		Exports:   []Export{},
		Engine: Engine{
			Name:          "consolidation",
			Percentile:    0.95,
			WindowDays:    math.Round(in.WindowDays*10) / 10,
			LowConfidence: in.WindowDays < FullWindowDays,
			ComputedAt:    in.ComputedAt,
		},
		Steps:    []Step{},
		Excluded: []Exclusion{},
		Notes:    []Note{},
	}
	for _, text := range in.Notes {
		p.Notes = append(p.Notes, Note{Version: 1, At: in.Now, Text: text})
	}

	// A pool's size and its precondition count a cordoned node: it takes no
	// pods, so the engine packs without it, but it is still paid for.
	nodesOf := map[string][]NodeFacts{}
	for _, n := range in.Nodes {
		if !n.ControlPlane {
			nodesOf[n.PoolSource+"/"+n.Pool] = append(nodesOf[n.PoolSource+"/"+n.Pool], n)
		}
	}
	current := SimulateConsolidation(in.Nodes, in.Current, in.Autoscaler)
	var planned []PoolVerdict
	if in.Planned != nil {
		planned = SimulateConsolidation(in.Nodes, in.Planned, in.Autoscaler)
	}

	usedPlanned := false
	var money float64
	priced := true
	for i, v := range current {
		scenario := ScenarioCurrent
		today := len(v.Removable)
		if planned != nil && len(planned[i].Removable) > today {
			v, scenario = planned[i], ScenarioPlanned
		}
		target := Target{Type: NodePoolType, Name: poolName(v.Pool), Owner: poolOwner(v)}
		if len(v.Removable) == 0 {
			p.Excluded = append(p.Excluded, poolExclusion(target, v))
			continue
		}
		if scenario == ScenarioPlanned {
			usedPlanned = true
		}
		s := consolidationStep(v, nodesOf[v.PoolSource+"/"+v.Pool], target, scenario, today, in)
		if s.Impact.Estimated.Money == nil {
			priced = false
		} else {
			money += s.Impact.Estimated.Money.Monthly
		}
		p.Impact.Estimated.CPUMilli += s.Impact.Estimated.CPUMilli
		p.Impact.Estimated.MemBytes += s.Impact.Estimated.MemBytes
		p.Steps = append(p.Steps, s)
	}

	// The largest saving first; the pool's name breaks ties.
	sort.SliceStable(p.Steps, func(i, j int) bool {
		a, b := stepMoney(p.Steps[i]), stepMoney(p.Steps[j])
		if a != b {
			return a > b
		}
		return p.Steps[i].Target.Name < p.Steps[j].Target.Name
	})
	for i := range p.Steps {
		p.Steps[i].Order = i + 1
		p.Steps[i].ID = fmt.Sprintf("s%d", i+1)
	}
	if len(p.Steps) > 0 && priced && in.Currency != "" {
		p.Impact.Estimated.Money = &Money{Monthly: round2(money), Currency: in.Currency, Source: in.PriceSource}
	} else {
		p.Impact.Estimated.MoneyUnavailable = MoneyNoCostData
	}
	if usedPlanned {
		p.DependsOn = append([]string(nil), in.DependsOn...)
	}
	sort.SliceStable(p.Excluded, func(i, j int) bool { return p.Excluded[i].Target.Name < p.Excluded[j].Target.Name })
	p.ContentHash = ContentHash(p)
	return p, p.Validate()
}

func consolidationStep(v PoolVerdict, nodes []NodeFacts, target Target, scenario string, today int, in ConsolidationInput) Step {
	byName := map[string]NodeFacts{}
	for _, n := range nodes {
		byName[n.Name] = n
	}
	params := RemoveNodesParams{
		Pool: poolName(v.Pool), PoolSource: v.PoolSource, Autoscaler: v.Autoscaler,
		NodesBefore: len(nodes), NodesAfter: len(nodes) - len(v.Removable), Nodes: []RemovedNode{},
	}
	var cpu, mem int64
	for _, name := range v.Removable {
		n := byName[name]
		params.Nodes = append(params.Nodes, RemovedNode{Name: n.Name, Zone: n.Zone, InstanceType: n.InstanceType, ProviderID: n.ProviderID})
		cpu += n.CPUMilli
		mem += n.MemBytes
	}
	before := PoolBefore{Nodes: len(nodes), InstanceTypes: []string{}, Zones: []string{}}
	types, zones := map[string]bool{}, map[string]bool{}
	for _, n := range nodes {
		if n.InstanceType != "" && !types[n.InstanceType] {
			types[n.InstanceType] = true
			before.InstanceTypes = append(before.InstanceTypes, n.InstanceType)
		}
		if n.Zone != "" && !zones[n.Zone] {
			zones[n.Zone] = true
			before.Zones = append(before.Zones, n.Zone)
		}
	}
	sort.Strings(before.InstanceTypes)
	sort.Strings(before.Zones)
	evidence := ConsolidationEvidence{
		Scenario: scenario, RemovableToday: today,
		Ceiling: ConsolidationCeiling, MinNodes: MinPoolNodes,
		WindowDays: math.Round(in.WindowDays*10) / 10,
		Loads:      v.Loads, Blocked: v.Blocked,
	}

	risk, reasons := RiskMedium, []string{RiskReasonLessHeadroom}
	if in.WindowDays < FullWindowDays {
		risk, reasons = RiskHigh, append(reasons, RiskReasonShortHistory)
	}
	if scenario == ScenarioPlanned {
		reasons = append(reasons, RiskReasonDependsOnPlan)
	}

	s := Step{
		Action: ActionRemoveNodes, Target: target,
		ExportOnly:       true,
		ExportOnlyReason: "the pool's size lives in your infrastructure: KubeBolt exports the change and never makes it",
		Params:           mustJSON(params),
		Before:           mustJSON(before),
		Evidence:         mustJSON(evidence),
		Precondition:     Precondition{SpecHash: PoolHash(nodes)},
		Verification:     Verification{Window: ConsolidationWindow, Checks: append([]string(nil), consolidationChecks...)},
		Risk:             risk, RiskReasons: reasons,
		Impact:     Impact{Estimated: Savings{CPUMilli: cpu, MemBytes: mem}},
		Provenance: "consolidation",
		Status:     StepPending,
	}
	if v.Monthly != nil && in.Currency != "" {
		s.Impact.Estimated.Money = &Money{Monthly: round2(*v.Monthly), Currency: in.Currency, Source: in.PriceSource}
	}
	return s
}

// poolExclusion says why a pool keeps every node, in words.
func poolExclusion(t Target, v PoolVerdict) Exclusion {
	e := Exclusion{Target: t}
	switch v.Kept {
	case KeptMinNodes:
		e.Reason = ReasonPoolMinNodes
		// The engine counts the nodes that take pods: a control-plane or a
		// cordoned node is in no pool (Kobi counted kind's control-plane in).
		e.Detail = fmt.Sprintf("%s (control-plane and cordoned nodes are not counted): the fewest a pool is taken to is %d, and one in each zone it spans",
			countWords(v.Nodes, "node takes pods", "nodes take pods"), MinPoolNodes)
	case KeptBlocked:
		e.Reason = ReasonPoolBlocked
		if len(v.Blocked) > 0 {
			e.Detail = BlockWords(v.Blocked[0])
		}
	default:
		e.Reason = ReasonPoolHeadroom
		e.Detail = fmt.Sprintf("%s: no node's pods fit on the others under %d%% of their allocatable while still leaving room to lose one",
			countWords(v.Nodes, "node", "nodes"), int(ConsolidationCeiling*100))
	}
	return e
}

// BlockWords says why a node cannot be emptied.
func BlockWords(b NodeBlock) string {
	switch b.Reason {
	case BlockPDB:
		return fmt.Sprintf("the PodDisruptionBudget %s allows no disruption now, so %s keeps %s", b.Owner, b.Pod, b.Node)
	case BlockNoController:
		return fmt.Sprintf("%s has no controller to start it again elsewhere, so it keeps %s", b.Pod, b.Node)
	case BlockLocalStorage:
		return fmt.Sprintf("%s keeps data on the node's own disk, so it keeps %s", b.Owner, b.Node)
	case BlockNotEvictable:
		return fmt.Sprintf("%s is marked not to be evicted, so it keeps %s", b.Owner, b.Node)
	case BlockNoPlace:
		return fmt.Sprintf("a pod of %s has nowhere to go among the nodes that stay: its scheduling rules (one per node, node affinity, host ports, selector or taints) leave none, so it keeps %s", b.Owner, b.Node)
	case BlockUnsimulated:
		return fmt.Sprintf("%s has scheduling rules the plan does not simulate (pod affinity, a hard topology spread or anti-affinity across zones), so it keeps %s", b.Owner, b.Node)
	}
	return fmt.Sprintf("%s keeps %s", b.Pod, b.Node)
}

// poolOwner: Karpenter sizes its own pools; who sizes the others (Terraform,
// eksctl, the console) is not visible from the cluster.
func poolOwner(v PoolVerdict) *Owner {
	if v.PoolSource == PoolKarpenter {
		return &Owner{Kind: OwnerOperator, Ref: "karpenter"}
	}
	return &Owner{Kind: OwnerUnknown}
}

// PoolName is the name a plan gives a node's pool.
func PoolName(pool string) string { return poolName(pool) }

func poolName(pool string) string {
	if pool == "" {
		return UnlabelledPool
	}
	return pool
}

func stepMoney(s Step) float64 {
	if m := s.Impact.Estimated.Money; m != nil {
		return m.Monthly
	}
	return 0
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic("kobiplan: " + err.Error())
	}
	return b
}

// poolWords names a pool for the export and the screens: "pool general", or
// "the nodes" for a cluster whose nodes carry no pool label.
func poolWords(name string) string {
	if name == UnlabelledPool || name == "" {
		return "the cluster's nodes"
	}
	return "pool " + name
}
