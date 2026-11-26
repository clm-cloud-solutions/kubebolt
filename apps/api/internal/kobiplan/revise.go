package kobiplan

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
)

// A person edits a pending step's values in the plan (plans-06, d85): «the
// engine says 10m, I'd rather keep 30m». The spec's rules (revise_plan):
//   - under the per-pod P95 the engine measured, the value is refused with
//     that figure, and the refusal stays in the plan's notes;
//   - between the P95 and the engine's value it is taken, at high risk
//     (below_engine_value), and the step is imposed with its author;
//   - at or above the engine's value it is taken and is no riskier for being
//     the person's;
//   - the step's direction holds: a request it lowers stays under today's
//     value (at or above it saves nothing), a limit it raises stays above
//     today's. Refused like a value under the P95.
//
// Only the values the step already changes can be edited: a step is one
// change, and adding another dimension would be a new step.

// ValueRejected is a value the plan refuses, with the figure that says why.
type ValueRejected struct{ Reason string }

func (e *ValueRejected) Error() string { return e.Reason }

// ReviseStep takes the person's values for a pending step. changed is false
// when they are the step's already. A *ValueRejected is a value under the
// P95; any other error is a request that does not fit the step.
func (p *Plan) ReviseStep(stepID string, values SetResourcesParams, pricing Pricing, by string, at time.Time) (changed bool, err error) {
	i := p.stepIndex(stepID)
	if i < 0 {
		return false, fmt.Errorf("the plan has no step %s", stepID)
	}
	s := &p.Steps[i]
	if s.Status != StepPending {
		return false, fmt.Errorf("step %d is %s: only a pending step's values can change", s.Order, s.Status)
	}
	if s.Action != ActionSetResources {
		return false, fmt.Errorf("step %d does not set resources", s.Order)
	}
	values, err = s.checkValues(values)
	if err != nil {
		return false, err
	}
	risk, reasons, deviates, _ := s.AppliedRisk(values)
	if !deviates {
		return false, nil
	}
	if err := p.takeValues(i, values, risk, reasons, pricing); err != nil {
		return false, err
	}
	// Back to the engine's values: the step is the engine's again.
	back := false
	if engine, err := DecodeSetResources(s.EngineParams); err == nil && sameValues(engine, values) {
		s.Imposed, s.EngineParams, back = false, nil, true
	}
	p.Version++
	p.ContentHash = ""
	p.Notes = append(p.Notes, Note{Version: p.Version, At: at, Text: revisedNote(*s, by, back)})
	return true, nil
}

// checkValues fits the person's values to the step: the same containers and
// dimensions, each a quantity above zero written the canonical way, a limit
// not under its request, and nothing under the per-pod P95.
func (s Step) checkValues(values SetResourcesParams) (SetResourcesParams, error) {
	params, err := DecodeSetResources(s.Params)
	if err != nil {
		return values, err
	}
	before, _ := DecodeSetResources(s.Before)
	floors := stepFloors(s)
	var out SetResourcesParams
	for _, got := range values.Containers {
		if _, ok := findContainer(params, got.Container, got.InitContainer); !ok {
			return values, fmt.Errorf("the step does not change container %s", got.Container)
		}
	}
	for _, want := range params.Containers {
		got, ok := findContainer(values, want.Container, want.InitContainer)
		if !ok {
			return values, fmt.Errorf("no values for container %s", want.Container)
		}
		row := ContainerResources{Container: want.Container, InitContainer: want.InitContainer}
		was, _ := findContainer(before, want.Container, want.InitContainer)
		for _, d := range dimensions {
			g, w := d.get(got), d.get(want)
			if g == nil && w == nil {
				continue
			}
			if w == nil {
				return values, fmt.Errorf("the step does not change the %s of %s", d.words(), want.Container)
			}
			if g == nil {
				return values, fmt.Errorf("no %s for %s", d.words(), want.Container)
			}
			q, err := resource.ParseQuantity(strings.TrimSpace(*g))
			if err != nil || q.Sign() <= 0 {
				return values, fmt.Errorf("%s %q is not a quantity above zero", d.words(), *g)
			}
			if floor := floors[d.memory]; floor > 0 && milliOrBytes(q.String(), d.memory) < floor {
				return values, &ValueRejected{Reason: fmt.Sprintf("%s %s is under the %s per pod the engine measured as the P95; the step keeps %s",
					d.words(), q.String(), floorQuantity(floor, d.memory), *w)}
			}
			if today := d.get(was); today != nil {
				switch c := cmpQuantity(q.String(), *today); {
				case d.request && c >= 0:
					return values, &ValueRejected{Reason: fmt.Sprintf("%s %s is not under today's %s: the step lowers it, and a value at or above today's saves nothing; the step keeps %s",
						d.words(), q.String(), *today, *w)}
				case !d.request && c <= 0:
					return values, &ValueRejected{Reason: fmt.Sprintf("%s %s is not above today's %s: the step raises it so the pods are not held at the limit; the step keeps %s",
						d.words(), q.String(), *today, *w)}
				}
			}
			d.set(&row, q.String())
		}
		// A limit under its request is refused by Kubernetes: say it here.
		for _, memory := range []bool{false, true} {
			req, lim := dimension{true, memory}, dimension{false, memory}
			r, l := req.get(row), lim.get(row)
			if r == nil {
				r = req.get(was)
			}
			if l == nil {
				l = lim.get(was)
			}
			if r != nil && l != nil && cmpQuantity(*l, *r) < 0 {
				return values, fmt.Errorf("the %s %s is under its request %s", lim.words(), *l, *r)
			}
		}
		out.Containers = append(out.Containers, row)
	}
	return out, nil
}

// stepFloors is the per-pod P95 the step's evidence holds, rounded up: CPU in
// millicores (false) and memory in bytes (true). Zero when it is not known.
func stepFloors(s Step) map[bool]int64 {
	var ev struct {
		Pods int `json:"pods"`
		CPU  struct {
			P95 int64 `json:"p95"`
		} `json:"cpu"`
		Mem struct {
			P95 int64 `json:"p95"`
		} `json:"mem"`
	}
	out := map[bool]int64{}
	if len(s.Evidence) == 0 || json.Unmarshal(s.Evidence, &ev) != nil || ev.Pods < 1 {
		return out
	}
	ceil := func(v int64) int64 { return (v + int64(ev.Pods) - 1) / int64(ev.Pods) }
	out[false] = ceil(ev.CPU.P95)
	out[true] = ceil(ev.Mem.P95)
	return out
}

func floorQuantity(v int64, memory bool) string {
	if memory {
		const mi = 1 << 20
		return resource.NewQuantity((v+mi-1)/mi*mi, resource.BinarySI).String()
	}
	return resource.NewMilliQuantity(v, resource.DecimalSI).String()
}

// sameValues says whether two changes set the same values.
func sameValues(a, b SetResourcesParams) bool {
	if len(a.Containers) != len(b.Containers) {
		return false
	}
	for _, x := range a.Containers {
		y, ok := findContainer(b, x.Container, x.InitContainer)
		if !ok {
			return false
		}
		for _, d := range dimensions {
			u, v := d.get(x), d.get(y)
			if (u == nil) != (v == nil) || (u != nil && cmpQuantity(*u, *v) != 0) {
				return false
			}
		}
	}
	return true
}

func (d dimension) words() string {
	res := "cpu"
	if d.memory {
		res = "memory"
	}
	if d.request {
		return res + " request"
	}
	return res + " limit"
}

func revisedNote(s Step, by string, back bool) string {
	who := ""
	if by != "" {
		who = " by " + by
	}
	if back {
		return fmt.Sprintf("Step %d (%s) set back to the engine's values%s.", s.Order, s.Target.Name, who)
	}
	return fmt.Sprintf("Step %d (%s) values changed%s; the engine's stay as a reference.", s.Order, s.Target.Name, who)
}

// RejectedNote is the note a refused value leaves in the plan.
func RejectedNote(s Step, by string, reason string) string {
	from := ""
	if by != "" {
		from = " from " + by
	}
	return fmt.Sprintf("Step %d (%s): a value%s was refused: %s.", s.Order, s.Target.Name, from, reason)
}

// StepByID returns the plan's step, or nil.
func (p *Plan) StepByID(id string) *Step {
	if i := p.stepIndex(id); i >= 0 {
		return &p.Steps[i]
	}
	return nil
}
