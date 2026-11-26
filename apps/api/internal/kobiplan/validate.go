package kobiplan

import (
	"fmt"
	"strings"
)

// ValidationError lists every problem of a plan at once, so the backend that
// builds it (and a test) sees all of them, not the first.
type ValidationError struct {
	Problems []string
}

func (e *ValidationError) Error() string {
	return "invalid plan: " + strings.Join(e.Problems, "; ")
}

// setResourceableTypes mirrors the set-resources endpoint
// (api/actions_resources.go): the only types it accepts.
var setResourceableTypes = map[string]bool{
	"deployments":  true,
	"statefulsets": true,
	"daemonsets":   true,
}

var (
	validStatuses   = set(StatusDraft, StatusExported, StatusVerified, StatusExpired, StatusApproved, StatusExecuting, StatusFailed)
	validExpired    = set(ExpiredUnexported, ExpiredDiscarded, ExpiredChanged)
	validStepStatus = set(StepPending, StepApplied, StepVerified, StepExpired)
	validInitiators = set(InitiatorChat, InitiatorInsight, InitiatorFinding, InitiatorAutopilot, InitiatorMandate)
	validFormats    = set(ExportDiff, ExportCommands, ExportPR, ExportApply)
	validOwners     = set(OwnerProvider, OwnerOperator, OwnerHelm, OwnerGitOps, OwnerNone, OwnerUnknown)
	validRisks      = set(RiskLow, RiskMedium, RiskHigh)
	validReasons    = set(ReasonMultiContainer, ReasonHPATarget, ReasonPodsVaried, ReasonNoEngineValue,
		ReasonProviderManaged, ReasonGitOpsUnknown, ReasonOperatorManaged, ReasonOwnerUnknown, ReasonInLivePlan,
		ReasonPoolMinNodes, ReasonPoolHeadroom, ReasonPoolBlocked)
)

func set[T comparable](vs ...T) map[T]bool {
	m := make(map[T]bool, len(vs))
	for _, v := range vs {
		m[v] = true
	}
	return m
}

// Validate refuses what the schema does not allow. It checks shape and
// consistency, never the cluster: whether a value is still right is the
// precondition's job, at export and at verify.
func (p *Plan) Validate() error {
	var v validator
	v.plan(p)
	if len(v.problems) == 0 {
		return nil
	}
	return &ValidationError{Problems: v.problems}
}

type validator struct{ problems []string }

func (v *validator) add(format string, args ...any) {
	v.problems = append(v.problems, fmt.Sprintf(format, args...))
}

func (v *validator) plan(p *Plan) {
	if p.ID == "" {
		v.add("no id")
	}
	if p.ClusterID == "" {
		v.add("no cluster")
	}
	if p.Title == "" {
		v.add("no title")
	}
	if _, ok := kindActions[p.Kind]; !ok {
		v.add("unknown kind %q", p.Kind)
	}
	if p.Version < 1 {
		v.add("version %d: versions start at 1", p.Version)
	}
	v.status(p)
	v.source(p.Source)
	if p.CreatedAt.IsZero() {
		v.add("no creation time")
	}
	if !p.ExpiresAt.IsZero() && !p.ExpiresAt.After(p.CreatedAt) {
		v.add("expires before it was created")
	}
	if p.Engine.Name == "" {
		v.add("no engine: every number of a plan comes from one")
	}
	v.exports(p)
	v.savings("plan", p.Impact.Estimated, true)
	if o := p.Impact.Observed; o != nil && o.ClusterCost != nil && o.ClusterCost.Attributed {
		v.add("the cluster's cost is context and never attributed to the plan")
	}
	if p.Baseline != nil && p.ExportedAt == nil {
		v.add("a baseline before any export: it is captured at the first one")
	}

	ids := map[string]bool{}
	for i, s := range p.Steps {
		if s.Order != i+1 {
			v.add("step %q: order %d, want %d (steps are listed in order, from 1)", s.ID, s.Order, i+1)
		}
		if s.ID == "" {
			v.add("step %d: no id", i+1)
		} else if ids[s.ID] {
			v.add("step %q: id used twice", s.ID)
		}
		ids[s.ID] = true
		v.step(p.Kind, s)
	}
	for i, e := range p.Excluded {
		where := fmt.Sprintf("excluded %d", i+1)
		v.target(where, e.Target)
		if !validReasons[e.Reason] {
			v.add("%s: unknown reason %q", where, e.Reason)
		}
	}
	for _, n := range p.Notes {
		if n.Version < 1 || n.Version > p.Version {
			v.add("note of version %d on a plan at version %d", n.Version, p.Version)
		}
	}
	if p.ContentHash != "" && p.ContentHash != ContentHash(*p) {
		v.add("contentHash does not match the content")
	}
}

func (v *validator) status(p *Plan) {
	if !validStatuses[p.Status] {
		v.add("unknown status %q", p.Status)
		return
	}
	switch {
	case p.Status == StatusExpired && !validExpired[p.ExpiredReason]:
		v.add("expired with reason %q: want unexported, discarded or changed", p.ExpiredReason)
	case p.Status != StatusExpired && p.ExpiredReason != "":
		v.add("an expired reason on a %s plan", p.Status)
	}
}

func (v *validator) source(s Source) {
	if !validInitiators[s.Initiator] {
		v.add("unknown initiator %q", s.Initiator)
		return
	}
	switch s.Initiator {
	case InitiatorChat:
		if s.UserID == "" || s.ConversationID == "" {
			v.add("a plan from the chat needs its user and conversation")
		}
	case InitiatorMandate:
		if s.MandateID == "" {
			v.add("a plan from a mandate needs the mandate")
		}
	}
}

func (v *validator) exports(p *Plan) {
	for i, e := range p.Exports {
		if !validFormats[e.Format] {
			v.add("export %d: unknown format %q", i+1, e.Format)
		}
		if e.At.IsZero() {
			v.add("export %d: no time", i+1)
		}
	}
	exported := len(p.Exports) > 0
	if exported != (p.ExportedAt != nil) {
		v.add("exportedAt and exports disagree")
	}
	switch p.Status {
	case StatusDraft:
		if exported {
			v.add("a draft that was exported")
		}
	case StatusExported, StatusVerified:
		if !exported {
			v.add("%s without an export", p.Status)
		}
	}
}

// savings checks an estimate. A plan says why money is missing; a step may
// simply have none.
func (v *validator) savings(where string, s Savings, needReason bool) {
	if s.CPUMilli < 0 || s.MemBytes < 0 {
		v.add("%s: negative savings", where)
	}
	if s.Money == nil {
		if needReason && s.MoneyUnavailable == "" {
			v.add("%s: no money and no reason", where)
		}
		return
	}
	if s.MoneyUnavailable != "" {
		v.add("%s: money and a reason it is missing", where)
	}
	if s.Money.Currency == "" {
		v.add("%s: money without a currency", where)
	}
	if s.Money.Monthly < 0 {
		v.add("%s: negative money", where)
	}
}

func (v *validator) target(where string, t Target) {
	if t.Type == "" || t.Name == "" {
		v.add("%s: target needs a type and a name", where)
	}
	if t.Owner != nil && !validOwners[t.Owner.Kind] {
		v.add("%s: unknown owner %q", where, t.Owner.Kind)
	}
}

func (v *validator) step(kind Kind, s Step) {
	where := fmt.Sprintf("step %q", s.ID)
	v.target(where, s.Target)
	switch {
	case s.Target.Owner == nil:
		v.add("%s: no owner (unknown is a value)", where)
	case s.Target.Owner.Kind == OwnerProvider:
		v.add("%s: the provider reconciles %s/%s; it is excluded (provider_managed), not a step", where, s.Target.Namespace, s.Target.Name)
	}
	if !validRisks[s.Risk] {
		v.add("%s: unknown risk %q", where, s.Risk)
	}
	if !validStepStatus[s.Status] {
		v.add("%s: unknown status %q", where, s.Status)
	}
	v.savings(where, s.Impact.Estimated, false)
	if o := s.Impact.Observed; o != nil && o.ClusterCost != nil {
		v.add("%s: the cluster's cost belongs to the plan, not a step", where)
	}
	if s.Rollback != nil && !isAction(s.Rollback.Action) {
		v.add("%s: rollback with unknown action %q", where, s.Rollback.Action)
	}

	if s.Action == ActionRemoveNodes {
		v.removeNodes(where, kind, s)
		return
	}
	if s.ExportOnly {
		if s.ExportOnlyReason == "" {
			v.add("%s: export-only without a reason", where)
		}
		if s.Action == "" {
			v.add("%s: no action", where)
		}
		return
	}
	if s.ExportOnlyReason != "" {
		v.add("%s: a reason for export-only on a step that is not", where)
	}
	switch {
	case !isAction(s.Action):
		v.add("%s: %q is not one of Kobi's verbs; make it export-only", where, s.Action)
		return
	case !kindAllows(kind, s.Action):
		v.add("%s: a %s plan does not %s", where, kind, s.Action)
		return
	}
	if !strings.HasPrefix(s.Precondition.SpecHash, hashPrefix) {
		v.add("%s: no spec hash to check before applying", where)
	}
	if len(s.Params) == 0 {
		v.add("%s: no params", where)
		return
	}
	if s.Action == ActionSetResources {
		if !setResourceableTypes[s.Target.Type] {
			v.add("%s: set_resources does not apply to %s", where, s.Target.Type)
		}
		params, err := DecodeSetResources(s.Params)
		if err != nil {
			v.add("%s: %v", where, err)
			return
		}
		for _, pr := range params.validate() {
			v.add("%s: %s", where, pr)
		}
	}
}

// removeNodes checks a consolidation step: export-only, on a pool, with the
// nodes it removes.
func (v *validator) removeNodes(where string, kind Kind, s Step) {
	if kind != KindConsolidation {
		v.add("%s: a %s plan does not remove nodes", where, kind)
	}
	if !s.ExportOnly || s.ExportOnlyReason == "" {
		v.add("%s: removing nodes is export-only, with its reason", where)
	}
	if s.Target.Type != NodePoolType {
		v.add("%s: remove_nodes applies to a node pool, not %s", where, s.Target.Type)
	}
	if !strings.HasPrefix(s.Precondition.SpecHash, hashPrefix) {
		v.add("%s: no pool hash to check before applying", where)
	}
	params, err := DecodeRemoveNodes(s.Params)
	if err != nil {
		v.add("%s: %v", where, err)
		return
	}
	for _, pr := range params.validate() {
		v.add("%s: %s", where, pr)
	}
}
