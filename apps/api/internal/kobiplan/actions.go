package kobiplan

import (
	"bytes"
	"encoding/json"
	"fmt"

	"k8s.io/apimachinery/pkg/api/resource"
)

// Action is what a step does: one of Kobi's verbs, named as the tool without
// its propose_ prefix. A step whose action is not here is export-only.
type Action string

const (
	ActionSetResources       Action = "set_resources"
	ActionScaleWorkload      Action = "scale_workload"
	ActionSetImage           Action = "set_image"
	ActionSetEnv             Action = "set_env"
	ActionPatchHPA           Action = "patch_hpa"
	ActionDeleteResource     Action = "delete_resource"
	ActionRestartWorkload    Action = "restart_workload"
	ActionRollbackDeployment Action = "rollback_deployment"
	ActionDebugPod           Action = "debug_pod"
)

// Actions are Kobi's verbs. verbs_test.go fails when one has no propose_ tool.
var Actions = []Action{
	ActionSetResources, ActionScaleWorkload, ActionSetImage, ActionSetEnv,
	ActionPatchHPA, ActionDeleteResource, ActionRestartWorkload,
	ActionRollbackDeployment, ActionDebugPod,
}

// kindActions is what each kind may do. v0: rightsizing only sets resources
// (P1). A new kind or a new action for a kind is one line here.
var kindActions = map[Kind][]Action{
	KindRightsizing: {ActionSetResources},
	// Consolidation has no verb: its steps change a pool, which lives in the
	// customer's infrastructure, so every one is export-only (plans-16).
	KindConsolidation: {},
}

func isAction(a Action) bool {
	for _, x := range Actions {
		if x == a {
			return true
		}
	}
	return false
}

func kindAllows(k Kind, a Action) bool {
	for _, x := range kindActions[k] {
		if x == a {
			return true
		}
	}
	return false
}

// SetResourcesParams is the body of POST
// /resources/{type}/{namespace}/{name}/set-resources, byte for byte: a step's
// params are sent to that endpoint as they are. api's setResourcesRequest is
// the endpoint's own type; a test in package api fails when they drift.
type SetResourcesParams struct {
	Containers []ContainerResources `json:"containers"`
}

// ContainerResources is one container's change. Only the dimensions that
// change are sent.
type ContainerResources struct {
	Container     string     `json:"container"`
	InitContainer bool       `json:"initContainer,omitempty"`
	Requests      *Resources `json:"requests,omitempty"`
	Limits        *Resources `json:"limits,omitempty"`
}

// Resources are Kubernetes quantities, as strings.
type Resources struct {
	CPU    *string `json:"cpu,omitempty"`
	Memory *string `json:"memory,omitempty"`
}

// DecodeSetResources reads set_resources params, refusing unknown fields: a
// field the endpoint does not know would be dropped silently on the way.
func DecodeSetResources(raw json.RawMessage) (SetResourcesParams, error) {
	var p SetResourcesParams
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return p, fmt.Errorf("set_resources params: %w", err)
	}
	return p, nil
}

// validate checks what the endpoint would refuse, before anything is
// exported: an empty change, a bad quantity, a limit under the request.
func (p SetResourcesParams) validate() []string {
	var problems []string
	if len(p.Containers) == 0 {
		return []string{"set_resources: no container"}
	}
	seen := map[string]bool{}
	for i, c := range p.Containers {
		where := fmt.Sprintf("set_resources container %d", i)
		if c.Container == "" {
			problems = append(problems, where+": no name")
		} else {
			where = "set_resources container " + c.Container
			if seen[c.Container] {
				problems = append(problems, where+": named twice")
			}
			seen[c.Container] = true
		}
		if c.Requests.empty() && c.Limits.empty() {
			problems = append(problems, where+": changes nothing")
		}
		problems = append(problems, c.Requests.validate(where+" requests")...)
		problems = append(problems, c.Limits.validate(where+" limits")...)
		problems = append(problems, limitUnderRequest(where, "cpu", c.Requests.cpu(), c.Limits.cpu())...)
		problems = append(problems, limitUnderRequest(where, "memory", c.Requests.memory(), c.Limits.memory())...)
	}
	return problems
}

// CPUValue is the CPU quantity, nil when unset or when r is nil.
func (r *Resources) CPUValue() *string { return r.cpu() }

// MemoryValue is the memory quantity, nil when unset or when r is nil.
func (r *Resources) MemoryValue() *string { return r.memory() }

func (r *Resources) empty() bool {
	return r == nil || (r.CPU == nil && r.Memory == nil)
}

func (r *Resources) cpu() *string {
	if r == nil {
		return nil
	}
	return r.CPU
}

func (r *Resources) memory() *string {
	if r == nil {
		return nil
	}
	return r.Memory
}

func (r *Resources) validate(where string) []string {
	if r == nil {
		return nil
	}
	var problems []string
	for _, d := range []struct {
		name string
		v    *string
	}{{"cpu", r.CPU}, {"memory", r.Memory}} {
		if d.v == nil {
			continue
		}
		q, err := resource.ParseQuantity(*d.v)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s %s: %q is not a quantity", where, d.name, *d.v))
			continue
		}
		if q.Sign() <= 0 {
			problems = append(problems, fmt.Sprintf("%s %s: %q is not above zero", where, d.name, *d.v))
		}
	}
	return problems
}

// limitUnderRequest compares only when the step sets both; the endpoint also
// checks against the live object, which this package does not see.
func limitUnderRequest(where, name string, req, lim *string) []string {
	if req == nil || lim == nil {
		return nil
	}
	rq, err1 := resource.ParseQuantity(*req)
	lq, err2 := resource.ParseQuantity(*lim)
	if err1 != nil || err2 != nil {
		return nil // reported by validate
	}
	if lq.Cmp(rq) < 0 {
		return []string{fmt.Sprintf("%s %s: limit %s under request %s", where, name, *lim, *req)}
	}
	return nil
}
