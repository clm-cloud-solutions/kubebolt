package kobiplan

import (
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// Verification (plans-07, spec «verify_plan»). The API reads what the cluster
// shows about each step's workload (StepFacts) and Verify writes it into the
// plan. Pure: no clock but the one it is given.
//   - A pending step is applied when the workload has its values, whoever
//     applied them. Other values in the step's dimensions are the person's
//     (d85): the plan keeps them. It expires when the workload changed
//     otherwise, or is gone, before the step was applied.
//   - An applied step is checked VerifyAfter later: rollout complete, no
//     OOMKilled, restarts not up, P95 under 80 % of the limit (not
//     applicable without a limit). It is verified when none fails; values
//     that went back to the ones before put it back to pending.
//   - ImpactAfter after it was applied, its impact is measured (P20).
//   - An exported plan with nothing pending or applied is done: verified
//     when a step was, expired (changed) when every step expired. A
//     verified plan is no longer live.

// The timings. Variables so a test stack can shorten them
// (KUBEBOLT_KOBI_PLAN_VERIFY_AFTER, KUBEBOLT_KOBI_PLAN_IMPACT_AFTER).
var (
	VerifyAfter = 24 * time.Hour
	ImpactAfter = 7 * 24 * time.Hour
)

// The outcome of one check.
const (
	CheckOK            = "ok"
	CheckFailed        = "failed"
	CheckNotApplicable = "not_applicable"
	CheckUnknown       = "unknown"
)

// The checks a rightsizing step carries.
const (
	CheckRolloutComplete = "rollout_complete"
	CheckNoOOMKilled     = "no_oom_killed"
	CheckRestartsNotUp   = "restarts_not_up"
	CheckP95UnderLimit   = "p95_under_80pct_of_limit"
)

// StepFacts is what the cluster shows about one step's workload now.
type StepFacts struct {
	// Found: the workload exists. SpecHash and Live are its template now,
	// Live holding the step's containers' requests and limits.
	Found    bool
	SpecHash string
	Live     SetResourcesParams
	Pods     int
	// Checked: the checks were read (an applied step, VerifyAfter after).
	Checked         bool
	RolloutComplete bool
	OOMKilled       bool
	Restarts        int32
	// P95 per pod since the step was applied, by dimension: CPU in
	// millicores (false), memory in bytes (true). Absent: no data.
	P95 map[bool]int64
	// AllocatedAfter is OpenCost's monthly cost for the workload over the
	// ImpactAfter window after it was applied; Measured says the window was
	// read (an applied step, ImpactAfter after). WindowDays is the days of
	// data that window has.
	Measured       bool
	AllocatedAfter *Money
	WindowDays     int
	// Pool is a consolidation step's pool, nil for any other step.
	Pool *PoolFacts
}

// PlanFacts is every step's facts and, once the plan's last applied step is
// ImpactAfter old, the cluster's cost over that window (context, P20).
type PlanFacts struct {
	Steps            map[string]StepFacts
	ClusterCostAfter *Money
}

// Verify writes the facts into the plan. changed: anything moved (Update);
// contentChanged: a step took values applied outside KubeBolt, so the plan
// needs a new version (Revise).
func (p *Plan) Verify(f PlanFacts, pricing Pricing, now time.Time) (changed, contentChanged bool) {
	var notes []string
	for i := range p.Steps {
		s := &p.Steps[i]
		facts, ok := f.Steps[s.ID]
		if ok && s.Action == ActionRemoveNodes && facts.Pool != nil {
			if p.verifyPool(s, *facts.Pool, now, &notes) {
				changed = true
			}
			continue
		}
		if !ok || s.Action != ActionSetResources {
			continue
		}
		params, err := DecodeSetResources(s.Params)
		if err != nil {
			continue
		}
		before, _ := DecodeSetResources(s.Before)
		atParams := facts.Found && sameOnDims(facts.Live, params, params)
		atBefore := facts.Found && sameOnDims(facts.Live, before, params)
		name := fmt.Sprintf("Step %d (%s)", s.Order, s.Target.Name)

		switch s.Status {
		case StepPending:
			switch {
			case !facts.Found:
				s.Status = StepExpired
				notes = append(notes, name+" expired: the workload is gone.")
			case atParams:
				p.markApplied(s, now)
				notes = append(notes, name+" was found applied in the cluster.")
			case facts.SpecHash == s.Precondition.SpecHash:
				continue // unchanged: still to do
			case atBefore:
				s.Status = StepExpired
				notes = append(notes, name+" expired: the workload changed before the step was applied.")
			default:
				p.takeOutside(i, facts.Live, params, pricing, now)
				contentChanged = true
				notes = append(notes, name+" was found applied in the cluster with values different from the engine's; the plan now keeps them.")
			}
			changed = true

		case StepApplied:
			if s.AppliedAt == nil {
				// Applied before plans-07 recorded when: the clock starts now.
				p.markApplied(s, now)
				changed = true
			}
			switch {
			case !facts.Found:
				s.Status = StepExpired
				notes = append(notes, name+" expired: the workload is gone.")
				changed = true
			case atBefore && !atParams:
				s.Status, s.AppliedAt, s.Verification.Result = StepPending, nil, nil
				notes = append(notes, name+" is pending again: the workload's values went back to the ones before (rolled back, or reverted by its owner).")
				changed = true
			case !atParams:
				p.takeOutside(i, facts.Live, params, pricing, now)
				s.Verification.Result = nil
				contentChanged, changed = true, true
				notes = append(notes, name+" was changed again in the cluster; the plan now keeps the new values and checks them from now.")
			case facts.Checked:
				res := evaluateChecks(*s, facts, now)
				failedBefore := s.Verification.Result != nil && !s.Verification.Result.Passed
				s.Verification.Result = &res
				changed = true
				if res.Passed {
					s.Status = StepVerified
					t := now
					s.VerifiedAt = &t
					notes = append(notes, name+" verified: it held for "+durationWords(VerifyAfter)+".")
				} else if !failedBefore {
					notes = append(notes, name+" failed its checks: "+res.Detail+". Roll it back from the plan.")
				}
			}
		}

		// Its impact, ImpactAfter after it was applied (P20).
		if (s.Status == StepApplied || s.Status == StepVerified) && facts.Measured && s.Impact.Observed == nil {
			s.Impact.Observed = observedStep(*s, facts, now)
			changed = true
		}
	}

	if p.Status == StatusExported && !p.HasPendingStep() && !p.hasStep(StepApplied) {
		changed = true
		if p.hasStep(StepVerified) {
			p.Status = StatusVerified
			held := VerifyAfter
			if p.Kind == KindConsolidation {
				held = PoolVerifyAfter()
			}
			notes = append(notes, "Verified: every applied step held for "+durationWords(held)+".")
		} else {
			p.Status, p.ExpiredReason = StatusExpired, ExpiredChanged
			if p.Kind == KindConsolidation {
				notes = append(notes, "Expired: every step's pool changed before the step was applied, or grew back.")
			} else {
				notes = append(notes, "Expired: every step's workload changed before the step was applied.")
			}
		}
	}
	if p.Impact.Observed == nil && p.Status == StatusVerified {
		if o := p.observedPlan(f, now); o != nil {
			p.Impact.Observed = o
			changed = true
		}
	}

	if contentChanged {
		p.Version++
		p.ContentHash = ""
	}
	for _, n := range notes {
		p.Notes = append(p.Notes, Note{Version: p.Version, At: now, Text: n})
	}
	return changed, contentChanged
}

func (p *Plan) markApplied(s *Step, now time.Time) {
	t := now
	s.Status, s.AppliedAt, s.Verification.Result = StepApplied, &t, nil
}

// takeOutside records values someone applied in the cluster (d85): the
// step's dimensions as the workload has them now.
func (p *Plan) takeOutside(i int, live, params SetResourcesParams, pricing Pricing, now time.Time) {
	var applied SetResourcesParams
	for _, want := range params.Containers {
		got, _ := findContainer(live, want.Container, want.InitContainer)
		row := ContainerResources{Container: want.Container, InitContainer: want.InitContainer}
		for _, d := range dimensions {
			if d.get(want) == nil {
				continue
			}
			if v := d.get(got); v != nil {
				d.set(&row, *v)
			}
		}
		applied.Containers = append(applied.Containers, row)
	}
	s := &p.Steps[i]
	risk, reasons, _, _ := s.AppliedRisk(applied)
	_ = p.takeValues(i, applied, risk, reasons, pricing)
	p.markApplied(s, now)
}

// sameOnDims says whether a and b hold the same values in the dimensions the
// step changes (dims). A dimension neither sets is the same.
func sameOnDims(a, b, dims SetResourcesParams) bool {
	for _, want := range dims.Containers {
		x, _ := findContainer(a, want.Container, want.InitContainer)
		y, _ := findContainer(b, want.Container, want.InitContainer)
		for _, d := range dimensions {
			if d.get(want) == nil {
				continue
			}
			u, v := d.get(x), d.get(y)
			if (u == nil) != (v == nil) || (u != nil && cmpQuantity(*u, *v) != 0) {
				return false
			}
		}
	}
	return true
}

// evaluateChecks runs the step's checks on the facts. Passed: none failed. A
// check the data cannot answer is unknown, recorded and not held against
// the step.
func evaluateChecks(s Step, f StepFacts, now time.Time) VerificationResult {
	res := VerificationResult{CheckedAt: now, Checks: map[string]string{}}
	var failed []string
	for _, c := range s.Verification.Checks {
		out := CheckUnknown
		switch c {
		case CheckRolloutComplete:
			out = okIf(f.RolloutComplete)
			if out == CheckFailed {
				failed = append(failed, "the rollout did not complete")
			}
		case CheckNoOOMKilled:
			out = okIf(!f.OOMKilled)
			if out == CheckFailed {
				failed = append(failed, "a container was OOMKilled")
			}
		case CheckRestartsNotUp:
			out = okIf(f.Restarts == 0)
			if out == CheckFailed {
				failed = append(failed, fmt.Sprintf("the pods restarted %d times", f.Restarts))
			}
		case CheckP95UnderLimit:
			var why string
			out, why = p95UnderLimit(f)
			if why != "" {
				failed = append(failed, why)
			}
		}
		res.Checks[c] = out
	}
	res.Passed = len(failed) == 0
	res.Detail = strings.Join(failed, "; ")
	return res
}

func okIf(ok bool) string {
	if ok {
		return CheckOK
	}
	return CheckFailed
}

// p95UnderLimit: the P95 per pod since the step was applied stays under 80 %
// of each limit the containers have. Not applicable without a limit (the
// Kyverno controllers of the 25-nov test have none for CPU).
func p95UnderLimit(f StepFacts) (string, string) {
	out := CheckNotApplicable
	for _, c := range f.Live.Containers {
		for _, d := range []dimension{{false, false}, {false, true}} {
			lim := d.get(c)
			if lim == nil {
				continue
			}
			p95, known := f.P95[d.memory]
			if !known {
				if out == CheckNotApplicable {
					out = CheckUnknown
				}
				continue
			}
			limit := milliOrBytes(*lim, d.memory)
			if limit > 0 && p95*10 > limit*8 {
				return CheckFailed, fmt.Sprintf("the %s P95 of %s is over 80%% of its %s limit", resWord(d.memory), floorQuantity(p95, d.memory), *lim)
			}
			if out != CheckUnknown {
				out = CheckOK
			}
		}
	}
	return out, ""
}

func resWord(memory bool) string {
	if memory {
		return "memory"
	}
	return "CPU"
}

// observedStep is the step's measured impact: the requests it freed (the
// values before minus the values now, per pod, times the pods) and what
// OpenCost allocates to the workload before and after.
func observedStep(s Step, f StepFacts, now time.Time) *Observed {
	params, _ := DecodeSetResources(s.Params)
	before, _ := DecodeSetResources(s.Before)
	pods := f.Pods
	if pods < 1 {
		pods = stepPods(s)
	}
	o := &Observed{MeasuredAt: now, WindowDays: f.WindowDays}
	for _, want := range params.Containers {
		was, _ := findContainer(before, want.Container, want.InitContainer)
		got, _ := findContainer(f.Live, want.Container, want.InitContainer)
		for _, d := range []dimension{{true, false}, {true, true}} {
			if d.get(want) == nil {
				continue
			}
			b, g := d.get(was), d.get(got)
			if b == nil || g == nil {
				continue
			}
			freed := (milliOrBytes(*b, d.memory) - milliOrBytes(*g, d.memory)) * int64(pods)
			if freed < 0 {
				freed = 0
			}
			if d.memory {
				o.MemBytesFreed += freed
			} else {
				o.CPUMilliFreed += freed
			}
		}
	}
	if s.Baseline != nil && s.Baseline.AllocatedCost != nil && f.AllocatedAfter != nil && s.Baseline.AllocatedCost.Currency == f.AllocatedAfter.Currency {
		o.AllocatedCost = &CostDelta{Before: s.Baseline.AllocatedCost.Monthly, After: f.AllocatedAfter.Monthly, Currency: f.AllocatedAfter.Currency}
	}
	return o
}

// observedPlan sums the measured steps once every one is measured, with the
// cluster's cost as context (never attributed to the plan).
func (p *Plan) observedPlan(f PlanFacts, now time.Time) *Observed {
	o := &Observed{MeasuredAt: now}
	var cost *CostDelta
	measured := 0
	for _, s := range p.Steps {
		if s.Status != StepVerified && s.Status != StepApplied {
			continue
		}
		if s.Impact.Observed == nil {
			return nil
		}
		measured++
		so := s.Impact.Observed
		o.CPUMilliFreed += so.CPUMilliFreed
		o.MemBytesFreed += so.MemBytesFreed
		if so.WindowDays > o.WindowDays {
			o.WindowDays = so.WindowDays
		}
		if so.AllocatedCost != nil {
			if cost == nil {
				cost = &CostDelta{Currency: so.AllocatedCost.Currency}
			}
			if cost.Currency == so.AllocatedCost.Currency {
				cost.Before += so.AllocatedCost.Before
				cost.After += so.AllocatedCost.After
			}
		}
	}
	if measured == 0 {
		return nil
	}
	if cost != nil {
		cost.Before, cost.After = round2(cost.Before), round2(cost.After)
		o.AllocatedCost = cost
	}
	if p.Baseline != nil && p.Baseline.ClusterCost != nil && f.ClusterCostAfter != nil && p.Baseline.ClusterCost.Currency == f.ClusterCostAfter.Currency {
		o.ClusterCost = &ClusterCostDelta{CostDelta: CostDelta{Before: p.Baseline.ClusterCost.Monthly, After: f.ClusterCostAfter.Monthly, Currency: f.ClusterCostAfter.Currency}}
	}
	return o
}

func (p Plan) hasStep(st StepStatus) bool {
	for _, s := range p.Steps {
		if s.Status == st {
			return true
		}
	}
	return false
}

// LastAppliedAt is when the plan's latest applied or verified step was
// applied: the plan's impact window ends ImpactAfter after it.
func (p Plan) LastAppliedAt() *time.Time {
	var last *time.Time
	for _, s := range p.Steps {
		if (s.Status == StepApplied || s.Status == StepVerified) && s.AppliedAt != nil && (last == nil || s.AppliedAt.After(*last)) {
			last = s.AppliedAt
		}
	}
	return last
}

func round2(v float64) float64 { return float64(int64(v*100+0.5)) / 100 }

func durationWords(d time.Duration) string {
	switch {
	case d%(24*time.Hour) == 0 && d >= 24*time.Hour:
		if d == 24*time.Hour {
			return "24 hours"
		}
		return fmt.Sprintf("%d days", int(d/(24*time.Hour)))
	case d%time.Hour == 0:
		return fmt.Sprintf("%d hours", int(d/time.Hour))
	}
	return d.String()
}

// LiveResources is the requests and limits a template gives the containers
// a step changes, in the step's shape.
func LiveResources(spec corev1.PodSpec, want SetResourcesParams) SetResourcesParams {
	var out SetResourcesParams
	for _, w := range want.Containers {
		list := spec.Containers
		if w.InitContainer {
			list = spec.InitContainers
		}
		for _, c := range list {
			if c.Name == w.Container {
				out.Containers = append(out.Containers, ContainerResources{
					Container: c.Name, InitContainer: w.InitContainer,
					Requests: resourcesOf(c.Resources.Requests), Limits: resourcesOf(c.Resources.Limits),
				})
			}
		}
	}
	return out
}
