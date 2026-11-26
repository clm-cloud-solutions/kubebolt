package kobiplan

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// Verification of a consolidation step (plans-16). The API reads the pool as
// it is now (PoolFacts) and verifyPool writes it into the plan:
//   - a pending step is applied once the pool is at its new size, whoever
//     removed the node; it expires when the pool changed otherwise first (a
//     node added or replaced: the evidence named nodes that are not there);
//   - an applied step is checked a week later (PoolVerifyAfter): the pool
//     held its size, no pod waits for room, the kubelet evicted nothing for
//     pressure on the pool's nodes and nothing there was OOMKilled;
//   - a pool that grows back expires the step: whatever brought the node
//     back (an autoscaler, whatever declares the pool's size) undid it, and
//     which node is left over now is a new plan's question.

// The checks a consolidation step carries.
const (
	CheckPoolHeld            = "pool_held"
	CheckNoPendingPods       = "no_pending_pods"
	CheckNoPressureEvictions = "no_pressure_evictions"
)

// PoolVerifyAfter is how long a pool is watched before its step is checked:
// the impact window, a week, so a weekly peak is in it.
func PoolVerifyAfter() time.Duration { return ImpactAfter }

// PoolFacts is what the cluster shows about a consolidation step's pool now.
type PoolFacts struct {
	// Nodes in the pool now (a cordoned node counts: it is still paid for),
	// their PoolHash, and the nodes themselves (name and type), to tell a
	// removal half done from a pool that changed.
	Nodes   int
	Hash    string
	Present []NodeFacts
	// Cordoned are the pool's nodes that take no new pods now.
	Cordoned []string
	// Checked: the checks were read (an applied step, PoolVerifyAfter after).
	Checked bool
	// PendingPods wait for a node since the step was applied: no room, or no
	// node their rules allow.
	PendingPods int
	// Evicted pods the kubelet evicted for pressure on the pool's nodes since
	// the step was applied.
	Evicted   int
	OOMKilled bool
}

// verifyPool writes a pool's facts into its step. It says whether the step
// changed and appends what happened to notes.
func (p *Plan) verifyPool(s *Step, f PoolFacts, now time.Time, notes *[]string) bool {
	params, err := DecodeRemoveNodes(s.Params)
	if err != nil {
		return false
	}
	name := fmt.Sprintf("Step %d (%s)", s.Order, poolWords(s.Target.Name))
	changed := false
	switch s.Status {
	case StepPending:
		switch {
		case f.Nodes == 0:
			s.Status = StepExpired
			*notes = append(*notes, name+" expired: the pool is gone.")
		case f.Nodes <= params.NodesAfter:
			p.markApplied(s, now)
			s.NodesGone, s.NodesCordoned = stepNodesGone(params, f), nil
			*notes = append(*notes, fmt.Sprintf("%s was found applied: the pool is at %s.", name, countWords(f.Nodes, "node", "nodes")))
		case f.Hash == s.Precondition.SpecHash || onlyStepNodesGone(params, f, s.Precondition.SpecHash):
			// Still to do, and maybe under way: the export empties the nodes
			// one at a time (cordon, drain) and removes each. What is being
			// emptied and what is gone is recorded once, as it changes, so
			// the plan shows a removal half done.
			gone, emptying := stepNodesGone(params, f), stepNodesCordoned(params, f)
			if slices.Equal(gone, s.NodesGone) && slices.Equal(emptying, s.NodesCordoned) {
				return false
			}
			newlyGone, newlyEmptying := added(s.NodesGone, gone), added(s.NodesCordoned, emptying)
			s.NodesGone, s.NodesCordoned = gone, emptying
			if len(newlyEmptying) > 0 {
				*notes = append(*notes, fmt.Sprintf("%s: %s %s being emptied (cordoned).", name, strings.Join(newlyEmptying, " and "), pick(len(newlyEmptying), "is", "are")))
			}
			if len(newlyGone) > 0 {
				left := len(params.Nodes) - len(gone)
				*notes = append(*notes, fmt.Sprintf("%s: %s %s gone; %s to go.", name, strings.Join(newlyGone, " and "),
					pick(len(newlyGone), "is", "are"), countWords(left, "more node", "more nodes")))
			}
		default:
			s.Status = StepExpired
			*notes = append(*notes, name+" expired: the pool's nodes changed before the step was applied.")
		}
		return true

	case StepApplied:
		if s.AppliedAt == nil {
			p.markApplied(s, now)
			changed = true
		}
		switch {
		case f.Nodes == 0:
			s.Status = StepExpired
			*notes = append(*notes, name+" expired: the pool is gone.")
			return true
		case f.Nodes > params.NodesAfter:
			s.Status, s.Verification.Result = StepExpired, nil
			*notes = append(*notes, fmt.Sprintf("%s expired: the pool grew back to %s (an autoscaler, or whatever declares its size, brought a node back); a new plan says which node is left over now.",
				name, countWords(f.Nodes, "node", "nodes")))
			return true
		case f.Checked:
			res := evaluatePoolChecks(*s, params, f, now)
			failedBefore := s.Verification.Result != nil && !s.Verification.Result.Passed
			s.Verification.Result = &res
			changed = true
			if res.Passed {
				s.Status = StepVerified
				t := now
				s.VerifiedAt = &t
				*notes = append(*notes, name+" verified: the pool held for "+durationWords(PoolVerifyAfter())+".")
			} else if !failedBefore {
				*notes = append(*notes, name+" failed its checks: "+res.Detail+". The rollback puts the node back.")
			}
		}
	}
	// What it removed, measured when it is checked: the bill before and after
	// is the plan's (observedPlan).
	if (s.Status == StepApplied || s.Status == StepVerified) && f.Checked && s.Impact.Observed == nil {
		s.Impact.Observed = &Observed{
			MeasuredAt: now, WindowDays: int(PoolVerifyAfter().Hours() / 24),
			CPUMilliFreed: s.Impact.Estimated.CPUMilli, MemBytesFreed: s.Impact.Estimated.MemBytes,
		}
		changed = true
	}
	return changed
}

// stepNodesGone: the step's nodes no longer in the pool, in the step's order.
func stepNodesGone(p RemoveNodesParams, f PoolFacts) []string {
	if len(f.Present) == 0 {
		return nil
	}
	present := map[string]bool{}
	for _, n := range f.Present {
		present[n.Name] = true
	}
	var gone []string
	for _, n := range p.Nodes {
		if !present[n.Name] {
			gone = append(gone, n.Name)
		}
	}
	return gone
}

// stepNodesCordoned: the step's nodes still in the pool but cordoned, being
// emptied, in the step's order.
func stepNodesCordoned(p RemoveNodesParams, f PoolFacts) []string {
	var out []string
	for _, n := range p.Nodes {
		if slices.Contains(f.Cordoned, n.Name) {
			out = append(out, n.Name)
		}
	}
	return out
}

// added: what is in now and was not in before.
func added(before, now []string) []string {
	var out []string
	for _, n := range now {
		if !slices.Contains(before, n) {
			out = append(out, n)
		}
	}
	return out
}

// onlyStepNodesGone: the pool as it is now, with the step's nodes that are
// gone put back, is the pool the step was made for.
func onlyStepNodesGone(p RemoveNodesParams, f PoolFacts, precondition string) bool {
	if len(f.Present) == 0 {
		return false
	}
	present := map[string]bool{}
	nodes := append([]NodeFacts(nil), f.Present...)
	for _, n := range f.Present {
		present[n.Name] = true
	}
	gone := 0
	for _, n := range p.Nodes {
		if !present[n.Name] {
			nodes = append(nodes, NodeFacts{Name: n.Name, InstanceType: n.InstanceType})
			gone++
		}
	}
	return gone > 0 && PoolHash(nodes) == precondition
}

func evaluatePoolChecks(s Step, params RemoveNodesParams, f PoolFacts, now time.Time) VerificationResult {
	res := VerificationResult{CheckedAt: now, Checks: map[string]string{}}
	var failed []string
	for _, c := range s.Verification.Checks {
		out := CheckUnknown
		switch c {
		case CheckPoolHeld:
			out = okIf(f.Nodes <= params.NodesAfter)
		case CheckNoPendingPods:
			out = okIf(f.PendingPods == 0)
			if out == CheckFailed {
				failed = append(failed, countWords(f.PendingPods, "pod waits for a node it can run on", "pods wait for a node they can run on"))
			}
		case CheckNoPressureEvictions:
			out = okIf(f.Evicted == 0)
			if out == CheckFailed {
				failed = append(failed, fmt.Sprintf("the kubelet evicted %s for pressure on the pool's nodes", countWords(f.Evicted, "pod", "pods")))
			}
		case CheckNoOOMKilled:
			out = okIf(!f.OOMKilled)
			if out == CheckFailed {
				failed = append(failed, "a container on the pool's nodes was OOMKilled")
			}
		}
		res.Checks[c] = out
	}
	res.Passed = len(failed) == 0
	res.Detail = strings.Join(failed, "; ")
	return res
}
