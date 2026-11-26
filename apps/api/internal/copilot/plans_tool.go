package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/kubebolt/kubebolt/apps/api/internal/kobiplan"
)

// ---- create_plan (Kobi Plans, plans-03) ----
//
// The plan tools are the chat's alone. They are not propose_* tools (P12): a
// plan changes nothing in the cluster, so they stay when actions are off. And
// they are not in ToolDefinitions: the public MCP server and Autopilot's
// profiles draw from that list, and a plan belongs to a person in a
// conversation (Source carries the user and the conversation). Autopilot will
// create the same entity through its own path (d24).

// CreatePlanRequest is what Kobi asks for. Kobi chooses the kind and the
// scope; every number comes from the engine.
type CreatePlanRequest struct {
	Kind       kobiplan.Kind
	Namespaces []string
	Workloads  []string
	Title      string
}

// CreatePlanOutcome is one of three answers.
type CreatePlanOutcome struct {
	// Created is the stored plan.
	Created *kobiplan.Plan
	// Existing is a live plan that answers the request, and nothing was
	// created; ExistingReason says why it came back.
	Existing       *kobiplan.Plan
	ExistingReason ExistingReason
	// Empty is a plan built and not stored: the engine flagged nothing in
	// scope, or everything it flagged is excluded.
	Empty *kobiplan.Plan
}

// ExistingReason is why create_plan answered with a live plan.
type ExistingReason string

const (
	// ExistingOnePerCluster: the org's plan keeps one live plan per cluster
	// (Free, P21).
	ExistingOnePerCluster ExistingReason = "one_per_cluster"
	// ExistingSameScope: a live plan with the same scope still has steps to
	// do (plans-06).
	ExistingSameScope ExistingReason = "same_scope"
)

// PlanSource builds and stores a plan for the person and the conversation
// it was wired for.
type PlanSource interface {
	CreatePlan(ctx context.Context, req CreatePlanRequest) (CreatePlanOutcome, error)
	// ReviseStep sets values the person gave for a pending step (revise_plan,
	// plans-06): the step is found by its workload among the cluster's live
	// plans, never by an id.
	ReviseStep(ctx context.Context, req ReviseStepRequest) (ReviseStepOutcome, error)
	// MatchStep finds the pending step of a live plan that a proposed
	// resources change touches (plans-15), or nil. containers is the
	// proposal's own list.
	MatchStep(ctx context.Context, typ, namespace, name string, containers []map[string]interface{}) *PlanStepMatch
	// ReadPlan returns one of the active cluster's plans by id: the plan the
	// person is looking at (its page is the session's current view).
	// ErrPlanNotFound, or ErrPlanOtherCluster for another cluster's plan.
	ReadPlan(ctx context.Context, id string) (*kobiplan.Plan, error)
}

// ErrPlanOtherCluster: the plan is for another cluster than the one the
// conversation is on.
var ErrPlanOtherCluster = errors.New("the plan is for another cluster")

// Reading a plan: Kobi has no tool of its own for it (the chat tool budget),
// but the plan's page is the session's current view (/plans/<id>) and Kobi
// reads it as a resource. get_resource_detail answers type plans here, in
// the chat only, with the same summary create_plan gives (no ids).
const planNoteRead = "The plan as it is stored now, read by its page. Present it as the kobi-plans procedure says (and kobi-plans-consolidation for a consolidation plan: load_skill if this conversation has not). Steps marked as being emptied or gone show a removal under way."

func (e *Executor) readPlan(ctx context.Context, id string) (string, bool) {
	p, err := e.plans.ReadPlan(ctx, id)
	switch {
	case errors.Is(err, ErrPlanOtherCluster):
		return jsonString(map[string]string{"error": "this plan is for another cluster than the one this conversation is on: say so, and offer to switch to that cluster to discuss it"}), true
	case errors.Is(err, ErrPlanNotFound):
		return jsonString(map[string]string{"error": "no such plan in this organization (it may have been pruned)"}), true
	case err != nil:
		return jsonString(map[string]string{"error": "the plan could not be read: " + err.Error()}), true
	}
	m := summarizePlan(*p)
	m["note"] = planNoteRead
	return jsonString(m), false
}

// ReviseStepRequest is a person's values for one step, as Kobi transcribed
// them. Workload is namespace/kind/name or namespace/name; Plan is a title,
// needed only when several live plans have a pending step for it.
type ReviseStepRequest struct {
	Workload  string
	Plan      string
	Container string
	Requests  map[string]string
	Limits    map[string]string
}

// ReviseStepOutcome: the plan as stored, the step's order, whether it
// changed, and, when the plan refused a value, why (with its figure).
type ReviseStepOutcome struct {
	Plan    *kobiplan.Plan
	Order   int
	Changed bool
	Refused string
}

// PlanStepMatch is a plan step a proposal would apply, in words for the card
// and the model: no ids (the server links the step again when it runs).
type PlanStepMatch struct {
	Order     int
	PlanTitle string
	Risk      string
	// Deviates: the proposal's values are not the step's; the plan will
	// record the person's. BelowEngine: a request below what the engine
	// measured as needed.
	Deviates    bool
	BelowEngine bool
}

// WithPlans wires create_plan. Chainable; nil means this executor creates
// no plans.
func (e *Executor) WithPlans(src PlanSource) *Executor {
	e.plans = src
	return e
}

// PlanToolNames are the chat-only plan tools.
func PlanToolNames() []string { return []string{"create_plan", "revise_plan"} }

// planToolDefinitions are short on purpose: every byte of a tool definition
// is read on every turn (prompt_budget_test.go). How to present a plan rides
// in the tool's output (planNote*), read only when a plan is made.
func planToolDefinitions() []ToolDefinition {
	return []ToolDefinition{{
		Name: "create_plan",
		Description: "Draft a plan of changes for the current cluster, built by KubeBolt's engine: you choose kind " +
			"and scope, never values. rightsizing: per-workload steps with risk, owner and what each frees (CPU, " +
			"memory, its list price with OpenCost), plus exclusions and why. consolidation: which nodes each pool " +
			"can do without and their list price (whole cluster); ask it before judging any node yourself. Applies nothing.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"kind":       strPropEnum("Plan kind", planKinds()),
				"namespaces": strListProp("Namespaces"),
				"workloads":  strListProp("namespace/name"),
				"title":      strProp("Title, in the person's language"),
			},
			"required": []string{"kind"},
		},
	}, {
		Name: "revise_plan",
		Description: "Change a live plan's pending step to values the person states («leave coredns at 30m»), found by workload. " +
			"Never values of your own. The plan may refuse one under what it measured.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"workload":  strProp("namespace/kind/name"),
				"plan":      strProp("Plan title, if several have it"),
				"container": strProp("Container, if several"),
				"requests":  resourcesProp(),
				"limits":    resourcesProp(),
			},
			"required": []string{"workload"},
		},
	}}
}

func planKinds() []string {
	out := make([]string, len(kobiplan.Kinds))
	for i, k := range kobiplan.Kinds {
		out[i] = string(k)
	}
	return out
}

func resourcesProp() map[string]interface{} {
	return map[string]interface{}{"type": "object", "properties": map[string]interface{}{"cpu": strProp(""), "memory": strProp("")}}
}

func strListProp(desc string) map[string]interface{} {
	return map[string]interface{}{"type": "array", "description": desc, "items": map[string]interface{}{"type": "string"}}
}

func stringsArg(args map[string]interface{}, key string) []string {
	raw, _ := args[key].([]interface{})
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimSpace(s))
		}
	}
	return out
}

func (e *Executor) runCreatePlan(ctx context.Context, args map[string]interface{}) (string, bool, *PlanRef) {
	if e.plans == nil {
		return `{"error":"plans are not available here"}`, true, nil
	}
	kind := kobiplan.Kind(stringArg(args, "kind"))
	if !slices.Contains(kobiplan.Kinds, kind) {
		return jsonString(map[string]string{"error": fmt.Sprintf("unknown plan kind %q; the kinds are %s", kind, strings.Join(planKinds(), " and "))}), true, nil
	}
	out, err := e.plans.CreatePlan(ctx, CreatePlanRequest{
		Kind:       kind,
		Namespaces: stringsArg(args, "namespaces"),
		Workloads:  stringsArg(args, "workloads"),
		Title:      strings.TrimSpace(stringArg(args, "title")),
	})
	if err != nil {
		return jsonString(map[string]string{"error": "could not create the plan: " + err.Error()}), true, nil
	}
	var ref *PlanRef
	switch {
	case out.Created != nil:
		ref = &PlanRef{ID: out.Created.ID, Outcome: "created"}
	case out.Existing != nil:
		ref = &PlanRef{ID: out.Existing.ID, Outcome: "existing_live_plan"}
		if out.ExistingReason == ExistingSameScope {
			// The card says why; the model reads the note.
			ref.Outcome = "existing_same_scope"
		}
	}
	return jsonString(summarizePlanOutcome(out)), false, ref
}

func (e *Executor) runRevisePlan(ctx context.Context, args map[string]interface{}) (string, bool, *PlanRef) {
	if e.plans == nil {
		return `{"error":"plans are not available here"}`, true, nil
	}
	req := ReviseStepRequest{
		Workload:  strings.TrimSpace(stringArg(args, "workload")),
		Plan:      strings.TrimSpace(stringArg(args, "plan")),
		Container: strings.TrimSpace(stringArg(args, "container")),
		Requests:  quantitiesArg(args, "requests"),
		Limits:    quantitiesArg(args, "limits"),
	}
	if req.Workload == "" || (len(req.Requests) == 0 && len(req.Limits) == 0) {
		return `{"error":"say the workload and at least one value the person gave"}`, true, nil
	}
	out, err := e.plans.ReviseStep(ctx, req)
	if err != nil {
		return jsonString(map[string]string{"error": "could not revise the plan: " + err.Error()}), true, nil
	}
	m := summarizePlan(*out.Plan)
	m["step"] = out.Order
	switch {
	case out.Refused != "":
		m["outcome"], m["reason"], m["note"] = "refused", out.Refused, planNoteRefused
	case out.Changed:
		m["outcome"], m["note"] = "revised", planNoteRevised
	default:
		m["outcome"], m["note"] = "unchanged", planNoteUnchanged
	}
	return jsonString(m), false, &PlanRef{ID: out.Plan.ID, Outcome: "revised"}
}

// quantitiesArg reads {"cpu": "30m", "memory": "256Mi"}.
func quantitiesArg(args map[string]interface{}, key string) map[string]string {
	raw, _ := args[key].(map[string]interface{})
	out := map[string]string{}
	for _, k := range []string{"cpu", "memory"} {
		if v, ok := raw[k].(string); ok && strings.TrimSpace(v) != "" {
			out[k] = strings.TrimSpace(v)
		}
	}
	return out
}

// The notes say what happened; how to present it is the kobi-plans
// procedure, attached to the conversation's first create_plan answer
// (WithConnectorSkill).
const (
	planNoteCreated      = "Created: the draft plan exists now and nothing changed in the cluster. Present it as the kobi-plans procedure says."
	planNoteExisting     = "Nothing was created: the organization's plan keeps one live plan per cluster and this one is live. Present it as the kobi-plans procedure says."
	planNoteSameScope    = "Nothing was created: this live plan already covers the same workloads and still has steps to do. Present it as the kobi-plans procedure says."
	planNoteEmpty        = "Nothing to plan: no plan was stored. Say why from the excluded reasons, as the kobi-plans procedure says."
	planNoteRevised      = "Revised: the step has the person's values in a new version of the plan; the engine's stay as a reference. Say what the step now frees and its risk."
	planNoteRefused      = "Refused: the step keeps its values. Tell the person why with the reason's figures: the floor is the P95 it names, and a value at or above it is taken. Add no mechanism of your own: the step's current limits say whether throttling applies (never without a CPU limit)."
	planNoteUnchanged    = "Nothing changed: the step already has these values."
	planNoteMonthly      = "The list price of the capacity the plan frees, not a saving: the cluster's bill falls only when nodes or disks are removed; until then the freed capacity is idle the cluster still pays for."
	planNoteNodesMonthly = "The list price of the nodes the plan suggests removing: the bill falls by it only once they are gone and the pool stays smaller, and by less with Savings Plans, reservations or committed-use discounts."
	planNoteDependsOn    = "Computed on the requests the live right-sizing plan leaves: today's requests free fewer nodes, so the pool step waits until that plan's steps are applied."
	maxPlanStepsShown    = 20
	maxPlanExcludedNamed = 10
)

func summarizePlanOutcome(out CreatePlanOutcome) map[string]interface{} {
	switch {
	case out.Created != nil:
		m := summarizePlan(*out.Created)
		m["outcome"] = "created"
		m["note"] = planNoteCreated
		return m
	case out.Existing != nil:
		m := summarizePlan(*out.Existing)
		m["outcome"] = "existing_live_plan"
		m["note"] = planNoteExisting
		if out.ExistingReason == ExistingSameScope {
			m["note"] = planNoteSameScope
		}
		return m
	case out.Empty != nil:
		m := summarizePlan(*out.Empty)
		delete(m, "status")
		delete(m, "expiresAt")
		m["outcome"] = "nothing_to_plan"
		m["note"] = planNoteEmpty
		return m
	}
	return map[string]interface{}{"error": "no plan"}
}

func summarizePlan(p kobiplan.Plan) map[string]interface{} {
	steps := make([]map[string]interface{}, 0, len(p.Steps))
	for i, s := range p.Steps {
		if i == maxPlanStepsShown {
			break
		}
		if s.Action == kobiplan.ActionRemoveNodes {
			steps = append(steps, poolStepRow(s))
			continue
		}
		// No ids, of the plan or of a step: what Kobi does not see it cannot
		// show the person (it put «pln_09b4…, paso s2» in an action card).
		row := map[string]interface{}{
			"workload": s.Target.Namespace + "/" + strings.TrimSuffix(s.Target.Type, "s") + "/" + s.Target.Name,
			"change":   describeSetResources(s),
			"current":  describeCurrentResources(s),
			"usage":    describeStepUsage(s),
			"risk":     s.Risk,
			"owner":    ownerLabel(s.Target.Owner),
			"frees":    unitsLabel(s.Impact.Estimated.CPUMilli, s.Impact.Estimated.MemBytes),
			"status":   s.Status,
		}
		if r := s.Verification.Result; r != nil && !r.Passed {
			row["checksFailed"] = r.Detail
		}
		// What the risk reasons rest on (plans-13, plans-14), in words: the
		// code alone left Kobi to guess the webhook or the request.
		if w := describeWebhooks(s); w != "" {
			row["webhooks"] = w
		}
		if q := describeQoS(s); q != "" {
			row["qos"] = q
		}
		if len(s.RiskReasons) > 0 {
			row["riskReasons"] = s.RiskReasons
		}
		if m := s.Impact.Estimated.Money; m != nil {
			row["monthly"] = moneyLabel(*m)
		}
		steps = append(steps, row)
	}

	byReason := map[string]int{}
	var named []string
	for _, x := range p.Excluded {
		byReason[string(x.Reason)]++
		if len(named) < maxPlanExcludedNamed {
			s := x.Target.Name + ": " + string(x.Reason)
			if x.Target.Namespace != "" {
				s = x.Target.Namespace + "/" + s
			}
			if x.Detail != "" {
				s += " (" + x.Detail + ")"
			}
			named = append(named, s)
		}
	}

	est := map[string]interface{}{"frees": unitsLabel(p.Impact.Estimated.CPUMilli, p.Impact.Estimated.MemBytes)}
	if p.Kind == kobiplan.KindConsolidation {
		est = map[string]interface{}{"capacityRemoved": unitsLabel(p.Impact.Estimated.CPUMilli, p.Impact.Estimated.MemBytes)}
	}
	if m := p.Impact.Estimated.Money; m != nil {
		est["monthly"] = moneyLabel(*m)
		est["monthlyNote"] = planNoteMonthly
		if p.Kind == kobiplan.KindConsolidation {
			est["monthlyNote"] = planNoteNodesMonthly
		}
	} else if p.Impact.Estimated.MoneyUnavailable != "" {
		est["moneyUnavailable"] = "no OpenCost prices for this cluster, so no money figure"
	}

	m := map[string]interface{}{
		"kind":      p.Kind,
		"status":    p.Status,
		"title":     p.Title,
		"expiresAt": p.ExpiresAt,
		"engine": map[string]interface{}{
			"name": p.Engine.Name, "windowDays": p.Engine.WindowDays, "lowConfidence": p.Engine.LowConfidence,
		},
		"estimated": est,
		"stepCount": len(p.Steps),
		"steps":     steps,
		"excluded":  map[string]interface{}{"byReason": byReason, "examples": named},
	}
	if len(p.DependsOn) > 0 {
		m["dependsOn"] = planNoteDependsOn
	}
	if len(p.Steps) > maxPlanStepsShown {
		m["stepsNote"] = fmt.Sprintf("first %d of %d steps, in the plan's order", maxPlanStepsShown, len(p.Steps))
	}
	if len(p.Notes) > 0 {
		notes := make([]string, 0, len(p.Notes))
		for _, n := range p.Notes {
			notes = append(notes, n.Text)
		}
		m["notes"] = notes
	}
	return m
}

// describeSetResources says a step's change as "cpu request 500m → 120m".
func describeSetResources(s kobiplan.Step) string {
	params, err := kobiplan.DecodeSetResources(s.Params)
	if err != nil || len(params.Containers) == 0 {
		return string(s.Action)
	}
	var before kobiplan.SetResourcesParams
	_ = json.Unmarshal(s.Before, &before)
	prev := map[string]kobiplan.ContainerResources{}
	for _, c := range before.Containers {
		prev[c.Container] = c
	}
	var parts []string
	for _, c := range params.Containers {
		was := prev[c.Container]
		add := func(field, res string, now, old *string) {
			if now == nil {
				return
			}
			from := "unset"
			if old != nil {
				from = *old
			}
			parts = append(parts, fmt.Sprintf("%s %s %s → %s", res, field, from, *now))
		}
		add("request", "cpu", c.Requests.CPUValue(), was.Requests.CPUValue())
		add("request", "memory", c.Requests.MemoryValue(), was.Requests.MemoryValue())
		add("limit", "cpu", c.Limits.CPUValue(), was.Limits.CPUValue())
		add("limit", "memory", c.Limits.MemoryValue(), was.Limits.MemoryValue())
	}
	return strings.Join(parts, "; ")
}

// describeStepUsage is the usage the step's value comes from: the engine's
// P95 over the plan's history, for the whole workload. Without it Kobi went
// to workload metrics, whose one-minute peaks summed over the pods read
// higher than typical load, and argued a step's value from them (29m against
// the engine's 7m for coredns; Kobi Plans live test, 25-nov).
func describeStepUsage(s kobiplan.Step) string {
	var ev struct {
		CPU  struct{ P95 int64 } `json:"cpu"`
		Mem  struct{ P95 int64 } `json:"mem"`
		Pods int                 `json:"pods"`
	}
	if json.Unmarshal(s.Evidence, &ev) != nil || ev.Pods == 0 {
		return ""
	}
	pods := fmt.Sprintf("%d pods", ev.Pods)
	if ev.Pods == 1 {
		pods = "1 pod"
	}
	return fmt.Sprintf("P95 over the plan's history, whole workload (%s): cpu %dm, memory %dMi",
		pods, ev.CPU.P95, ev.Mem.P95/(1024*1024))
}

// describeWebhooks: «serves 4 admission webhooks that fail closed (10 s
// timeout): kyverno-svc.kyverno.svc, …».
func describeWebhooks(s kobiplan.Step) string {
	var ev struct {
		Webhooks []kobiplan.WebhookFacts `json:"webhooks"`
	}
	if json.Unmarshal(s.Evidence, &ev) != nil || len(ev.Webhooks) == 0 {
		return ""
	}
	names := make([]string, 0, len(ev.Webhooks))
	timeout := ev.Webhooks[0].TimeoutSeconds
	for _, w := range ev.Webhooks {
		names = append(names, w.Name)
		if w.TimeoutSeconds < timeout {
			timeout = w.TimeoutSeconds
		}
	}
	return fmt.Sprintf("serves %d admission webhooks that fail closed (%d s timeout): %s", len(names), timeout, strings.Join(names, ", "))
}

// describeQoS: «Guaranteed → Burstable; a cpu request of 80m too keeps it
// Guaranteed».
func describeQoS(s kobiplan.Step) string {
	var ev struct {
		QoS *kobiplan.QoSChange `json:"qos"`
	}
	if json.Unmarshal(s.Evidence, &ev) != nil || ev.QoS == nil {
		return ""
	}
	out := ev.QoS.Before + " → " + ev.QoS.After
	if k := ev.QoS.KeepWith; k != nil {
		var parts []string
		if k.CPU != nil {
			parts = append(parts, "a cpu request of "+*k.CPU)
		}
		if k.Memory != nil {
			parts = append(parts, "a memory request of "+*k.Memory)
		}
		out += "; " + strings.Join(parts, " and ") + " too keeps it " + ev.QoS.Before
	}
	return out
}

// describeCurrentResources is the container's requests and limits today, as
// "cpu request 100m, no cpu limit; memory request 128Mi, memory limit 384Mi".
// Without it Kobi saw only what changes and filled the rest in: «memory
// requests are already 0», «watch for throttling» on a container with no CPU
// limit (Kobi Plans live test, 25-nov).
func describeCurrentResources(s kobiplan.Step) string {
	var before kobiplan.SetResourcesParams
	if json.Unmarshal(s.Before, &before) != nil || len(before.Containers) == 0 {
		return ""
	}
	var parts []string
	for _, c := range before.Containers {
		var dims []string
		for _, d := range []struct {
			name       string
			req, limit *string
		}{
			{"cpu", c.Requests.CPUValue(), c.Limits.CPUValue()},
			{"memory", c.Requests.MemoryValue(), c.Limits.MemoryValue()},
		} {
			req := "no " + d.name + " request"
			if d.req != nil {
				req = d.name + " request " + *d.req
			}
			limit := "no " + d.name + " limit"
			if d.limit != nil {
				limit = d.name + " limit " + *d.limit
			}
			dims = append(dims, req+", "+limit)
		}
		line := strings.Join(dims, "; ")
		if len(before.Containers) > 1 {
			line = c.Container + ": " + line
		}
		parts = append(parts, line)
	}
	return strings.Join(parts, " | ")
}

func ownerLabel(o *kobiplan.Owner) string {
	if o == nil {
		return string(kobiplan.OwnerUnknown)
	}
	if o.Ref == "" {
		return string(o.Kind)
	}
	return string(o.Kind) + ":" + o.Ref
}

// unitsLabel names only what is freed: a CPU-only step does not say "0Mi
// memory", which Kobi then repeated to the person.
func unitsLabel(cpuMilli, memBytes int64) string {
	var parts []string
	if cpuMilli > 0 {
		parts = append(parts, fmt.Sprintf("%dm CPU", cpuMilli))
	}
	if mi := memBytes / (1024 * 1024); mi > 0 {
		parts = append(parts, fmt.Sprintf("%dMi memory", mi))
	}
	if len(parts) == 0 {
		return "nothing (a raised limit frees no capacity)"
	}
	return strings.Join(parts, ", ")
}

func moneyLabel(m kobiplan.Money) string {
	return fmt.Sprintf("%.2f %s/month", m.Monthly, strings.ToUpper(m.Currency))
}

// poolStepRow says a consolidation step in words: the pool, which nodes go,
// why they are left over and what keeps the size where it is declared.
func poolStepRow(s kobiplan.Step) map[string]interface{} {
	row := map[string]interface{}{
		"pool":            s.Target.Name,
		"risk":            s.Risk,
		"status":          s.Status,
		"capacityRemoved": unitsLabel(s.Impact.Estimated.CPUMilli, s.Impact.Estimated.MemBytes),
		"owner":           "who sets the pool's size (Terraform, eksctl, a pipeline, the console) is not visible from the cluster",
	}
	if o := s.Target.Owner; o != nil && o.Kind == kobiplan.OwnerOperator && o.Ref == "karpenter" {
		row["owner"] = "Karpenter sizes this pool"
	}
	if len(s.RiskReasons) > 0 {
		row["riskReasons"] = s.RiskReasons
	}
	if m := s.Impact.Estimated.Money; m != nil {
		row["monthly"] = moneyLabel(*m)
	}
	params, err := kobiplan.DecodeRemoveNodes(s.Params)
	if err != nil {
		return row
	}
	nodes := make([]string, 0, len(params.Nodes))
	for _, n := range params.Nodes {
		where := strings.TrimSpace(strings.Join([]string{n.Zone, n.InstanceType}, ", "))
		if where = strings.Trim(where, ", "); where != "" {
			nodes = append(nodes, n.Name+" ("+where+")")
		} else {
			nodes = append(nodes, n.Name)
		}
	}
	row["change"] = fmt.Sprintf("%d → %d nodes: removes %s", params.NodesBefore, params.NodesAfter, strings.Join(nodes, ", "))
	if len(s.NodesCordoned) > 0 && s.Status == kobiplan.StepPending {
		row["emptying"] = "being emptied (cordoned): " + strings.Join(s.NodesCordoned, ", ")
	}
	if len(s.NodesGone) > 0 && s.Status == kobiplan.StepPending {
		row["progress"] = fmt.Sprintf("%d of %d nodes already removed (%s); the step is applied once the pool is at %d",
			len(s.NodesGone), len(params.Nodes), strings.Join(s.NodesGone, ", "), params.NodesAfter)
	}
	switch params.Autoscaler {
	case kobiplan.AutoscalerClusterAutoscaler:
		row["autoscaler"] = "the Cluster Autoscaler runs in the cluster: it removes such a node on its own once its requests stay under its threshold (50% by default)"
	case kobiplan.AutoscalerKarpenter:
		row["autoscaler"] = "Karpenter removes such a node on its own when its NodePool consolidates underutilized nodes"
	default:
		row["autoscaler"] = "no autoscaler: nobody removes it unless a person does"
	}
	var ev kobiplan.ConsolidationEvidence
	if json.Unmarshal(s.Evidence, &ev) == nil {
		why := fmt.Sprintf("its pods fit on the other %d nodes under %.0f%% of their allocatable, counting each pod's larger of request and P95 use, and the pool can still lose one node",
			params.NodesAfter, ev.Ceiling*100)
		if ev.Scenario == kobiplan.ScenarioPlanned {
			why += fmt.Sprintf("; only with the requests the live right-sizing plan leaves (today's requests free %d)", ev.RemovableToday)
		}
		row["why"] = why
		if len(ev.Blocked) > 0 {
			kept := make([]string, 0, len(ev.Blocked))
			for _, b := range ev.Blocked {
				kept = append(kept, kobiplan.BlockWords(b))
			}
			row["nodesKept"] = kept
		}
	}
	return row
}
