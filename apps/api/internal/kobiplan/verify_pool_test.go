package kobiplan

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func exportedConsolidation(t *testing.T) (Plan, []NodeFacts) {
	t.Helper()
	nodes := pool(5)
	pods := withDaemons(nodes, []PodFacts{
		pod("node-1", "a", 2000, 8), pod("node-2", "b", 1900, 7), pod("node-3", "c", 2100, 8), pod("node-4", "d", 1800, 8), pod("node-5", "e", 600, 2),
	})
	p, err := BuildConsolidation(consolidationInput(nodes, pods, nil))
	if err != nil {
		t.Fatal(err)
	}
	p.RecordExport(ExportCommands, "ana", p.CreatedAt.Add(time.Hour), nil, nil)
	return p, nodes
}

func poolNow(nodes []NodeFacts) *PoolFacts {
	return &PoolFacts{Nodes: len(nodes), Hash: PoolHash(nodes), Present: nodes}
}

func verifyWith(p *Plan, f *PoolFacts, now time.Time) bool {
	changed, _ := p.Verify(PlanFacts{Steps: map[string]StepFacts{"s1": {Pool: f}}}, Pricing{}, now)
	return changed
}

// Pending while the pool is as it was; applied once it is at its new size,
// whoever removed the node; verified a week later when every check held, with
// the capacity it removed measured; the plan is then verified.
func TestVerifyPool_Lifecycle(t *testing.T) {
	p, nodes := exportedConsolidation(t)
	now := p.CreatedAt.Add(2 * time.Hour)
	if verifyWith(&p, poolNow(nodes), now) || p.Steps[0].Status != StepPending {
		t.Fatalf("unchanged pool: %s", p.Steps[0].Status)
	}

	gone := poolNow(nodes[:4])
	if !verifyWith(&p, gone, now) || p.Steps[0].Status != StepApplied || p.Steps[0].AppliedAt == nil {
		t.Fatalf("at its new size: %s", p.Steps[0].Status)
	}
	if !strings.Contains(p.Notes[len(p.Notes)-1].Text, "the pool is at 4 nodes") {
		t.Fatalf("note = %q", p.Notes[len(p.Notes)-1].Text)
	}

	later := now.Add(PoolVerifyAfter())
	checked := *gone
	checked.Checked = true
	verifyWith(&p, &checked, later)
	s := p.Steps[0]
	if s.Status != StepVerified || !s.Verification.Result.Passed || s.Verification.Result.Checks[CheckPoolHeld] != CheckOK {
		t.Fatalf("checked: %s %+v", s.Status, s.Verification.Result)
	}
	if s.Impact.Observed == nil || s.Impact.Observed.CPUMilliFreed != 4000 || s.Impact.Observed.WindowDays != 7 {
		t.Fatalf("observed = %+v", s.Impact.Observed)
	}
	if p.Status != StatusVerified || !strings.Contains(p.Notes[len(p.Notes)-1].Text, "held for 7 days") {
		t.Fatalf("plan = %s, %q", p.Status, p.Notes[len(p.Notes)-1].Text)
	}
}

// A check that fails says what in words and keeps the step applied.
func TestVerifyPool_FailedChecks(t *testing.T) {
	p, nodes := exportedConsolidation(t)
	now := p.CreatedAt.Add(2 * time.Hour)
	verifyWith(&p, poolNow(nodes[:4]), now)
	f := poolNow(nodes[:4])
	f.Checked, f.PendingPods, f.Evicted = true, 2, 1
	verifyWith(&p, f, now.Add(PoolVerifyAfter()))
	s := p.Steps[0]
	if s.Status != StepApplied || s.Verification.Result.Passed {
		t.Fatalf("step = %s %+v", s.Status, s.Verification.Result)
	}
	if d := s.Verification.Result.Detail; d != "2 pods wait for a node they can run on; the kubelet evicted 1 pod for pressure on the pool's nodes" {
		t.Fatalf("detail = %q", d)
	}
}

// The pool changed before the step, or grew back after it: the step expires,
// and a plan whose every step expired expires with it.
func TestVerifyPool_Expires(t *testing.T) {
	p, nodes := exportedConsolidation(t)
	now := p.CreatedAt.Add(2 * time.Hour)
	replaced := append([]NodeFacts(nil), nodes...)
	replaced[0].Name = "node-9"
	verifyWith(&p, poolNow(replaced), now)
	if p.Steps[0].Status != StepExpired || p.Status != StatusExpired || !strings.Contains(p.Notes[0].Text, "the pool's nodes changed") {
		t.Fatalf("replaced: %s %s %v", p.Steps[0].Status, p.Status, p.Notes)
	}

	p, nodes = exportedConsolidation(t)
	verifyWith(&p, poolNow(nodes[:4]), now)
	verifyWith(&p, poolNow(append(nodes[:4:4], NodeFacts{Name: "node-6", InstanceType: "m6i.xlarge"})), now.Add(time.Hour))
	if p.Steps[0].Status != StepExpired || !strings.Contains(p.Notes[len(p.Notes)-2].Text, "grew back to 5 nodes") {
		t.Fatalf("grew back: %s %v", p.Steps[0].Status, p.Notes)
	}
}

// Removed one at a time, as the export says: with one of the step's nodes
// gone the step stays pending, and it is applied when they all are.
// Another node gone instead is a pool that changed.
func TestVerifyPool_OneNodeAtATime(t *testing.T) {
	nodes := pool(6) // zones a, a, b, c, c, b
	pods := withDaemons(nodes, []PodFacts{
		pod("node-1", "a", 1500, 6), pod("node-2", "b", 1500, 6), pod("node-3", "c", 1500, 6), pod("node-4", "d", 1500, 6),
		pod("node-5", "e", 300, 1), pod("node-6", "f", 300, 1),
	})
	p, err := BuildConsolidation(consolidationInput(nodes, pods, nil))
	if err != nil {
		t.Fatal(err)
	}
	params, _ := DecodeRemoveNodes(p.Steps[0].Params)
	if len(params.Nodes) < 2 {
		t.Fatalf("want a step removing two nodes, got %+v", params)
	}
	p.RecordExport(ExportCommands, "ana", p.CreatedAt.Add(time.Hour), nil, nil)
	now := p.CreatedAt.Add(2 * time.Hour)
	without := func(names ...string) []NodeFacts {
		var out []NodeFacts
		for _, n := range nodes {
			if !slices.Contains(names, n.Name) {
				out = append(out, n)
			}
		}
		return out
	}
	first := params.Nodes[0].Name
	// Cordoned first: being emptied, recorded with a note.
	cordoned := poolNow(nodes)
	cordoned.Cordoned = []string{first}
	if !verifyWith(&p, cordoned, now) || !slices.Equal(p.Steps[0].NodesCordoned, []string{first}) ||
		!strings.Contains(p.Notes[len(p.Notes)-1].Text, first+" is being emptied (cordoned)") {
		t.Fatalf("cordoned: %v %v", p.Steps[0].NodesCordoned, p.Notes)
	}
	// The first node gone is recorded once, with a note; the step is still
	// to do.
	if !verifyWith(&p, poolNow(without(first)), now) || p.Steps[0].Status != StepPending || !slices.Equal(p.Steps[0].NodesGone, []string{first}) {
		t.Fatalf("half done: %s %v %v", p.Steps[0].Status, p.Steps[0].NodesGone, p.Notes)
	}
	if n := p.Notes[len(p.Notes)-1].Text; !strings.Contains(n, first+" is gone; ") || !strings.Contains(n, "more node") {
		t.Fatalf("note = %q", n)
	}
	if verifyWith(&p, poolNow(without(first)), now.Add(time.Minute)) {
		t.Fatal("the same progress was recorded twice")
	}
	var all []string
	for _, n := range params.Nodes {
		all = append(all, n.Name)
	}
	verifyWith(&p, poolNow(without(all...)), now.Add(time.Hour))
	if p.Steps[0].Status != StepApplied || !slices.Equal(p.Steps[0].NodesGone, all) {
		t.Fatalf("all %d gone: %s %v", len(all), p.Steps[0].Status, p.Steps[0].NodesGone)
	}

	q, _ := BuildConsolidation(consolidationInput(nodes, pods, nil))
	q.RecordExport(ExportCommands, "ana", q.CreatedAt.Add(time.Hour), nil, nil)
	other := ""
	for _, n := range []string{"node-1", "node-2", "node-3", "node-4"} {
		if !slices.Contains(all, n) {
			other = n
			break
		}
	}
	verifyWith(&q, poolNow(without(other)), now)
	if q.Steps[0].Status != StepExpired {
		t.Fatalf("another node gone: %s", q.Steps[0].Status)
	}
}
