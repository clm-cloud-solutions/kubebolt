package kobiplan

import (
	"encoding/json"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/kubebolt/kubebolt/apps/api/internal/costmodel"
)

// What was really applied (plans-15, d85). A plan records the change a
// person made, which may not be the value the engine proposed: «10m, but I
// would rather keep 30m». The step then takes the person's values (Imposed),
// keeps the engine's as EngineParams, and its saving, rollback and risk are
// worked out again from what was applied. Pure: the API finds the steps a
// change touches and stores the result.

// RiskReasonBelowEngine: a request was set below what the engine measured as
// needed (the P95 with its headroom).
const RiskReasonBelowEngine = "below_engine_value"

var (
	highRiskReasons   = map[string]bool{RiskReasonStatefulSet: true, RiskReasonLowConfidence: true, RiskReasonImposed: true, RiskReasonBelowEngine: true, RiskReasonAdmissionWebhook: true, RiskReasonQoSChange: true}
	mediumRiskReasons = map[string]bool{RiskReasonLowersMemory: true, RiskReasonSinglePod: true, RiskReasonDaemonSet: true}
)

// AppliedInCluster says whether a step's change is made in the cluster
// itself: nobody owns the workload, or nobody was identified. A Helm or
// GitOps owner would put a cluster change back; those steps are applied
// where their owner keeps them (Export).
func (s Step) AppliedInCluster() bool {
	return s.Target.Owner == nil || s.Target.Owner.Kind == OwnerNone || s.Target.Owner.Kind == OwnerUnknown
}

// Touches says whether a set_resources change on (type, namespace, name)
// changes something this step changes: the same container and at least one
// of the step's dimensions, whatever the value.
func (s Step) Touches(typ, namespace, name string, applied SetResourcesParams) bool {
	if s.Action != ActionSetResources || s.Target.Type != typ || s.Target.Namespace != namespace || s.Target.Name != name {
		return false
	}
	params, err := DecodeSetResources(s.Params)
	if err != nil {
		return false
	}
	for _, want := range params.Containers {
		got, ok := findContainer(applied, want.Container, want.InitContainer)
		if !ok {
			continue
		}
		for _, d := range dimensions {
			if d.get(want) != nil && d.get(got) != nil {
				return true
			}
		}
	}
	return false
}

// AppliedRisk is the risk of a step applied with these values, and whether
// they are the step's own. A value the person chose is not riskier for being
// theirs; a request set below the engine's value is, and says so.
func (s Step) AppliedRisk(applied SetResourcesParams) (risk RiskLevel, reasons []string, deviates, belowEngine bool) {
	params, err := DecodeSetResources(s.Params)
	if err != nil {
		return s.Risk, s.RiskReasons, false, false
	}
	engine := params
	if len(s.EngineParams) > 0 {
		if e, err := DecodeSetResources(s.EngineParams); err == nil {
			engine = e
		}
	}
	before, _ := DecodeSetResources(s.Before)
	reasons = []string{} // stored as a list, never null: the plan view reads it as one
	lowersMemory := false
	for _, want := range params.Containers {
		got, ok := findContainer(applied, want.Container, want.InitContainer)
		if !ok {
			continue
		}
		eng, _ := findContainer(engine, want.Container, want.InitContainer)
		was, _ := findContainer(before, want.Container, want.InitContainer)
		for _, d := range dimensions {
			g := d.get(got)
			if g == nil {
				if d.get(want) != nil {
					deviates = true // the step's value was not applied
				}
				continue
			}
			if w := d.get(want); w == nil || cmpQuantity(*g, *w) != 0 {
				deviates = true
			}
			if d.request {
				if e := d.get(eng); e != nil && cmpQuantity(*g, *e) < 0 {
					belowEngine = true
				}
				if d.memory {
					if b := d.get(was); b != nil && cmpQuantity(*g, *b) < 0 {
						lowersMemory = true
					}
				}
			}
		}
	}
	for _, r := range s.RiskReasons {
		if r == RiskReasonLowersMemory || r == RiskReasonBelowEngine {
			continue // worked out again from what was applied
		}
		reasons = append(reasons, r)
	}
	if lowersMemory {
		reasons = append(reasons, RiskReasonLowersMemory)
	}
	if belowEngine {
		reasons = append(reasons, RiskReasonBelowEngine)
	}
	return riskOf(reasons), reasons, deviates, belowEngine
}

func riskOf(reasons []string) RiskLevel {
	level := RiskLow
	for _, r := range reasons {
		switch {
		case highRiskReasons[r]:
			return RiskHigh
		case mediumRiskReasons[r]:
			level = RiskMedium
		}
	}
	return level
}

// ApplyStep records that a step was applied with these values: the step is
// applied, and when the values are not the step's it takes them (Imposed,
// the engine's kept as EngineParams) with its saving, rollback and risk
// worked out again and the plan's totals following. contentChanged says the
// plan needs a new version (Revise) rather than an Update.
func (p *Plan) ApplyStep(stepID string, applied SetResourcesParams, pricing Pricing, by string, at time.Time) (contentChanged bool, err error) {
	i := p.stepIndex(stepID)
	if i < 0 {
		return false, fmt.Errorf("plan %s has no step %s", p.ID, stepID)
	}
	s := &p.Steps[i]
	risk, reasons, deviates, _ := s.AppliedRisk(applied)
	s.Status = StepApplied
	appliedAt := at
	s.AppliedAt = &appliedAt
	if !deviates {
		return false, nil
	}
	if err := p.takeValues(i, applied, risk, reasons, pricing); err != nil {
		return false, err
	}
	p.Version++
	p.ContentHash = ""
	p.Notes = append(p.Notes, Note{Version: p.Version, At: at, Text: appliedNote(*s, by)})
	return true, nil
}

func (p *Plan) stepIndex(stepID string) int {
	for k := range p.Steps {
		if p.Steps[k].ID == stepID {
			return k
		}
	}
	return -1
}

// takeValues makes values the step's own: the engine's stay as
// EngineParams, and the saving, the rollback and the risk follow the values,
// with the plan's totals. Shared by a step applied with other values
// (ApplyStep) and a step a person edits in the plan (ReviseStep).
func (p *Plan) takeValues(i int, values SetResourcesParams, risk RiskLevel, reasons []string, pricing Pricing) error {
	s := &p.Steps[i]
	params, err := DecodeSetResources(s.Params)
	if err != nil {
		return err
	}
	before, _ := DecodeSetResources(s.Before)
	pods := stepPods(*s)
	var newParams, rollback []ContainerResources
	var cpuSaved, memSaved int64
	for _, want := range params.Containers {
		got, ok := findContainer(values, want.Container, want.InitContainer)
		if !ok {
			continue
		}
		was, _ := findContainer(before, want.Container, want.InitContainer)
		row := ContainerResources{Container: want.Container, InitContainer: want.InitContainer}
		back := ContainerResources{Container: want.Container, InitContainer: want.InitContainer}
		for _, d := range dimensions {
			g := d.get(got)
			if g == nil {
				continue
			}
			d.set(&row, *g)
			if b := d.get(was); b != nil {
				d.set(&back, *b)
				if d.request {
					saved := (milliOrBytes(*b, d.memory) - milliOrBytes(*g, d.memory)) * int64(pods)
					if saved > 0 { // a value that saves nothing is recorded with nothing saved
						if d.memory {
							memSaved += saved
						} else {
							cpuSaved += saved
						}
					}
				}
			}
		}
		newParams = append(newParams, row)
		rollback = append(rollback, back)
	}
	if len(s.EngineParams) == 0 {
		s.EngineParams = append(json.RawMessage(nil), s.Params...)
	}
	s.Params, _ = json.Marshal(SetResourcesParams{Containers: newParams})
	if s.Rollback != nil {
		s.Rollback.Params, _ = json.Marshal(SetResourcesParams{Containers: rollback})
	}
	s.Imposed = true
	s.Risk, s.RiskReasons = risk, reasons
	s.Impact.Estimated = Savings{CPUMilli: cpuSaved, MemBytes: memSaved}
	p.recomputeEstimate(pricing)
	return nil
}

// recomputeEstimate prices every step again and sums them, the way
// BuildRightsizing does: the total is the sum of the amounts shown.
func (p *Plan) recomputeEstimate(pricing Pricing) {
	var cpu, mem int64
	var money float64
	priced := pricing.Rates.Available()
	for k := range p.Steps {
		e := &p.Steps[k].Impact.Estimated
		cpu += e.CPUMilli
		mem += e.MemBytes
		e.Money = nil
		if priced {
			v := costmodel.Round2(costmodel.MonthlySavings(e.CPUMilli, e.MemBytes, pricing.Rates) * pricing.Factor)
			e.Money = &Money{Monthly: v, Currency: pricing.Currency, Source: pricing.Source}
			money += v
		}
	}
	p.Impact.Estimated.CPUMilli, p.Impact.Estimated.MemBytes = cpu, mem
	if priced {
		p.Impact.Estimated.Money = &Money{Monthly: costmodel.Round2(money), Currency: pricing.Currency, Source: pricing.Source}
		p.Impact.Estimated.MoneyUnavailable = ""
	} else if p.Impact.Estimated.Money == nil && p.Impact.Estimated.MoneyUnavailable == "" {
		p.Impact.Estimated.MoneyUnavailable = MoneyNoCostData
	}
}

func appliedNote(s Step, by string) string {
	who := ""
	if by != "" {
		who = " by " + by
	}
	return fmt.Sprintf("Step %d (%s) applied%s with values different from the engine's; the plan now keeps them.", s.Order, s.Target.Name, who)
}

func stepPods(s Step) int {
	var ev struct {
		Pods int `json:"pods"`
	}
	if len(s.Evidence) > 0 {
		_ = json.Unmarshal(s.Evidence, &ev)
	}
	if ev.Pods < 1 {
		return 1
	}
	return ev.Pods
}

// dimension is one of the four values a step can change.
type dimension struct {
	request, memory bool
}

var dimensions = []dimension{{true, false}, {true, true}, {false, false}, {false, true}}

func (d dimension) side(c *ContainerResources) **Resources {
	if d.request {
		return &c.Requests
	}
	return &c.Limits
}

func (d dimension) get(c ContainerResources) *string {
	r := *d.side(&c)
	if r == nil {
		return nil
	}
	if d.memory {
		return r.Memory
	}
	return r.CPU
}

func (d dimension) set(c *ContainerResources, v string) {
	side := d.side(c)
	if *side == nil {
		*side = &Resources{}
	}
	val := v
	if d.memory {
		(*side).Memory = &val
	} else {
		(*side).CPU = &val
	}
}

func findContainer(p SetResourcesParams, name string, init bool) (ContainerResources, bool) {
	for _, c := range p.Containers {
		if c.Container == name && c.InitContainer == init {
			return c, true
		}
	}
	return ContainerResources{}, false
}

// cmpQuantity compares two quantities by value; one that does not parse
// compares by its text.
func cmpQuantity(a, b string) int {
	qa, errA := resource.ParseQuantity(a)
	qb, errB := resource.ParseQuantity(b)
	if errA != nil || errB != nil {
		switch {
		case a < b:
			return -1
		case a > b:
			return 1
		}
		return 0
	}
	return qa.Cmp(qb)
}

func milliOrBytes(v string, memory bool) int64 {
	q, err := resource.ParseQuantity(v)
	if err != nil {
		return 0
	}
	if memory {
		return q.Value()
	}
	return q.MilliValue()
}
