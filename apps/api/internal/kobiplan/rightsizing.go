package kobiplan

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/kubebolt/kubebolt/apps/api/internal/costmodel"
	"github.com/kubebolt/kubebolt/apps/api/internal/rightsizing"
)

// The right-sizing plan builder (plans-03): the engine's recommendations, the
// workloads as the cluster has them and the node prices go in; a plan comes
// out whose every number came from them. Pure: the caller reads the cluster,
// the metrics store and the exchange rate, this decides.

// PlanTTL is how long a plan nobody exported stays a draft (P5).
const PlanTTL = 7 * 24 * time.Hour

// Verification checks of a right-sizing step, run 24 h after it is applied.
var rightsizingChecks = []string{"rollout_complete", "no_oom_killed", "restarts_not_up", "p95_under_80pct_of_limit"}

// Risk reasons. Codes, so the screens can name them in any language.
const (
	RiskReasonStatefulSet   = "statefulset"
	RiskReasonLowConfidence = "low_confidence"
	RiskReasonImposed       = "imposed"
	RiskReasonLowersMemory  = "lowers_memory_request"
	RiskReasonSinglePod     = "single_pod"
	// RiskReasonAdmissionWebhook: it serves an admission webhook that fails
	// closed (failurePolicy Fail, plans-13): short of CPU under load it can
	// miss the timeout, and the API server then rejects what it guards.
	RiskReasonAdmissionWebhook = "admission_webhook"
	// RiskReasonQoSChange: the step takes the pod out of the Guaranteed QoS
	// class (plans-14); under memory pressure it is then killed among the
	// first. The evidence says the request that keeps the class.
	RiskReasonQoSChange = "qos_change"
	RiskReasonDaemonSet = "daemonset"
)

// WorkloadFacts is one workload as the cluster has it now.
type WorkloadFacts struct {
	// Type is the resource type of the API paths: deployments, statefulsets
	// or daemonsets.
	Type       string
	Namespace  string
	Name       string
	Generation int64
	Template   corev1.PodSpec
	Object     ObjectFacts
	// Pods is the number of pods the engine summed for this workload.
	Pods int
	// PodsStable: the pod count held at Pods through the history window.
	PodsStable bool
	// HPA names the HorizontalPodAutoscaler that scales it, if any.
	HPA string
	// Webhooks are the admission webhooks its pods serve (plans-13): the
	// webhook's Service selects them.
	Webhooks []WebhookFacts
}

// WebhookFacts is one admission webhook a workload serves. FailurePolicy
// Fail means the API server rejects the requests the webhook guards when it
// does not answer within TimeoutSeconds.
type WebhookFacts struct {
	Name           string `json:"name"`
	FailurePolicy  string `json:"failurePolicy"`
	TimeoutSeconds int32  `json:"timeoutSeconds"`
}

// Pricing is how units become money.
type Pricing struct {
	// Rates are OpenCost's node prices; none means no money figure.
	Rates costmodel.NodeRates
	// Factor converts OpenCost's currency into Currency (1 when they are the
	// same or when no rate was found and the amount stays unconverted).
	Factor   float64
	Currency string
	Source   string
}

// RightsizingInput is everything a right-sizing plan is built from.
type RightsizingInput struct {
	ID        string
	OrgID     string
	ClusterID string
	Title     string
	Source    Source
	Scope     Scope
	Result    rightsizing.Result
	// ComputedAt is when the engine computed Result.
	ComputedAt time.Time
	// Workloads by rightsizing.Key(namespace, kind, name).
	Workloads map[string]WorkloadFacts
	Pricing   Pricing
	// Planned: workloads that already have a pending step in another live
	// plan, by TargetKey, with that plan's title. They are excluded.
	Planned map[string]string
	// Notes go into the plan as they are (e.g. money left unconverted).
	Notes []string
	Now   time.Time
}

// kindTypes maps the engine's workload kinds to resource types.
var kindTypes = map[string]string{
	"Deployment":  "deployments",
	"StatefulSet": "statefulsets",
	"DaemonSet":   "daemonsets",
}

// ErrInsufficientHistory: the engine has not seen enough history to
// recommend anything, so there is no plan to build (finding #74).
var ErrInsufficientHistory = fmt.Errorf("the cluster does not have enough usage history for right-sizing yet")

// BuildRightsizing builds the plan. It may have no steps: everything the
// engine flagged in scope can be excluded, and the caller then stores
// nothing and tells why.
func BuildRightsizing(in RightsizingInput) (Plan, error) {
	if in.Result.InsufficientHistory {
		return Plan{}, ErrInsufficientHistory
	}
	p := Plan{
		ID: in.ID, OrgID: in.OrgID, ClusterID: in.ClusterID, Title: in.Title,
		Kind:      KindRightsizing,
		Scope:     normalizeScope(in.Scope),
		Version:   1,
		Status:    StatusDraft,
		CreatedAt: in.Now,
		Source:    in.Source,
		ExpiresAt: in.Now.Add(PlanTTL),
		Exports:   []Export{},
		Engine: Engine{
			Name:            "rightsizing",
			Percentile:      0.95,
			MinHistoryHours: in.Result.MinHistoryHours,
			LowConfidence:   in.Result.LowConfidence,
			ComputedAt:      in.ComputedAt,
		},
		Steps:    []Step{},
		Excluded: []Exclusion{},
		Notes:    []Note{},
	}
	if in.Result.WindowDays != nil {
		p.Engine.WindowDays = math.Round(*in.Result.WindowDays*10) / 10
	}
	for _, text := range in.Notes {
		p.Notes = append(p.Notes, Note{Version: 1, At: in.Now, Text: text})
	}

	type candidate struct {
		step       Step
		nearLimit  bool
		engineRank int
	}
	var cands []candidate
	for rank, rec := range in.Result.Recs {
		if !InScope(p.Scope, rec.Namespace, rec.Kind, rec.Name) {
			continue
		}
		w, ok := in.Workloads[rightsizing.Key(rec.Namespace, rec.Kind, rec.Name)]
		if !ok {
			// The engine read the same cache moments ago; a workload gone
			// since has nothing to change.
			continue
		}
		step, nearLimit, excl := rightsizingStep(rec, w, in.Result.LowConfidence)
		if excl == nil {
			if title, ok := in.Planned[TargetKey(step.Target.Type, step.Target.Namespace, step.Target.Name)]; ok {
				excl = &Exclusion{Target: step.Target, Reason: ReasonInLivePlan, Detail: fmt.Sprintf("the live plan %q already has a pending step for it", title)}
			}
		}
		if excl != nil {
			p.Excluded = append(p.Excluded, *excl)
			continue
		}
		cands = append(cands, candidate{step: step, nearLimit: nearLimit, engineRank: rank})
	}

	// Near-limit first: it is the urgent signal. Then by risk, low first; the
	// engine's order (severity, then waste) breaks ties.
	riskOrder := map[RiskLevel]int{RiskLow: 0, RiskMedium: 1, RiskHigh: 2}
	sort.SliceStable(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if a.nearLimit != b.nearLimit {
			return a.nearLimit
		}
		if a.nearLimit {
			return a.engineRank < b.engineRank
		}
		if riskOrder[a.step.Risk] != riskOrder[b.step.Risk] {
			return riskOrder[a.step.Risk] < riskOrder[b.step.Risk]
		}
		return a.engineRank < b.engineRank
	})

	var money float64
	for i, c := range cands {
		s := c.step
		s.Order = i + 1
		s.ID = fmt.Sprintf("s%d", i+1)
		if in.Pricing.Rates.Available() {
			v := costmodel.Round2(costmodel.MonthlySavings(s.Impact.Estimated.CPUMilli, s.Impact.Estimated.MemBytes, in.Pricing.Rates) * in.Pricing.Factor)
			// The total is the sum of the amounts the steps show, so the two
			// add up on screen (rounding the total apart drifted a cent).
			money += v
			s.Impact.Estimated.Money = &Money{Monthly: v, Currency: in.Pricing.Currency, Source: in.Pricing.Source}
		}
		p.Impact.Estimated.CPUMilli += s.Impact.Estimated.CPUMilli
		p.Impact.Estimated.MemBytes += s.Impact.Estimated.MemBytes
		p.Steps = append(p.Steps, s)
	}
	if in.Pricing.Rates.Available() {
		p.Impact.Estimated.Money = &Money{Monthly: costmodel.Round2(money), Currency: in.Pricing.Currency, Source: in.Pricing.Source}
	} else {
		p.Impact.Estimated.MoneyUnavailable = MoneyNoCostData
	}

	sort.SliceStable(p.Excluded, func(i, j int) bool {
		a, b := p.Excluded[i], p.Excluded[j]
		if a.Reason != b.Reason {
			return a.Reason < b.Reason
		}
		if a.Target.Namespace != b.Target.Namespace {
			return a.Target.Namespace < b.Target.Namespace
		}
		return a.Target.Name < b.Target.Name
	})
	p.ContentHash = ContentHash(p)
	return p, p.Validate()
}

// rightsizingStep turns one recommendation into a step, or says why not.
func rightsizingStep(rec rightsizing.Recommendation, w WorkloadFacts, lowConfidence bool) (Step, bool, *Exclusion) {
	owner, marker := DetectOwner(w.Object)
	target := Target{Type: w.Type, Namespace: w.Namespace, Name: w.Name, Owner: &owner}
	exclude := func(reason ExclusionReason, detail string) (Step, bool, *Exclusion) {
		return Step{}, false, &Exclusion{Target: target, Reason: reason, Detail: detail}
	}
	switch owner.Kind {
	case OwnerProvider:
		return exclude(ReasonProviderManaged, marker)
	case OwnerOperator:
		return exclude(ReasonOperatorManaged, owner.Ref+" reconciles it; the change belongs in that resource")
	}
	if !actionable(rec.CPU.State) && !actionable(rec.Mem.State) {
		return exclude(ReasonNoEngineValue, noValueDetail(rec))
	}
	if w.HPA != "" {
		return exclude(ReasonHPATarget, "HorizontalPodAutoscaler "+w.HPA+" scales it on utilization, which a new request moves")
	}

	containers := len(w.Template.Containers)
	for _, ic := range w.Template.InitContainers {
		// A native sidecar runs beside the main container and its usage is
		// in the P95, so it counts as a container.
		if ic.RestartPolicy != nil && *ic.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			containers++
		}
	}
	change, skip := rightsizing.PerPod(rec, w.Pods, containers, w.PodsStable)
	switch skip {
	case rightsizing.SkipMultiContainer:
		return exclude(ReasonMultiContainer, fmt.Sprintf("%d containers per pod; the engine measures them together", containers))
	case rightsizing.SkipPodsVaried:
		return exclude(ReasonPodsVaried, "the number of pods changed during the window")
	case rightsizing.SkipNoEngineValue:
		return exclude(ReasonNoEngineValue, noValueDetail(rec))
	}

	c := w.Template.Containers[0]
	cr := ContainerResources{Container: c.Name}
	before := ContainerResources{Container: c.Name, Requests: resourcesOf(c.Resources.Requests), Limits: resourcesOf(c.Resources.Limits)}
	rollback := ContainerResources{Container: c.Name}
	var cpuSaved, memSaved int64
	nearLimit := false
	var lowersMemory bool
	var limitNote, requestNote bool

	apply := func(v *rightsizing.PerPodValue, name corev1.ResourceName) {
		if v == nil {
			return
		}
		var current resource.Quantity
		var ok bool
		if v.Field == rightsizing.FieldRequests {
			current, ok = c.Resources.Requests[name]
		} else {
			current, ok = c.Resources.Limits[name]
		}
		if !ok {
			// The pods got the value from a LimitRange, not the spec: there
			// is nothing in the workload to roll back to.
			return
		}
		now := current.Value()
		if name == corev1.ResourceCPU {
			now = current.MilliValue()
		}
		if v.Field == rightsizing.FieldRequests && v.Value >= now {
			return
		}
		if v.Field == rightsizing.FieldLimits && v.Value <= now {
			return
		}
		newQ, oldQ := quantity(name, v.Value), current.String()
		set := func(r **Resources, val string) {
			if *r == nil {
				*r = &Resources{}
			}
			if name == corev1.ResourceCPU {
				(*r).CPU = &val
			} else {
				(*r).Memory = &val
			}
		}
		if v.Field == rightsizing.FieldRequests {
			set(&cr.Requests, newQ)
			set(&rollback.Requests, oldQ)
			saved := (now - v.Value) * int64(w.Pods)
			if name == corev1.ResourceCPU {
				cpuSaved = saved
			} else {
				memSaved = saved
				lowersMemory = true
			}
			requestNote = true
		} else {
			set(&cr.Limits, newQ)
			set(&rollback.Limits, oldQ)
			nearLimit = true
			limitNote = true
		}
	}
	apply(change.CPU, corev1.ResourceCPU)
	apply(change.Mem, corev1.ResourceMemory)
	if cr.Requests == nil && cr.Limits == nil {
		return exclude(ReasonNoEngineValue, "the values come from a LimitRange or already match, so the spec has nothing to change")
	}

	params, _ := json.Marshal(SetResourcesParams{Containers: []ContainerResources{cr}})
	beforeJSON, _ := json.Marshal(SetResourcesParams{Containers: []ContainerResources{before}})
	rollbackJSON, _ := json.Marshal(SetResourcesParams{Containers: []ContainerResources{rollback}})
	ev := map[string]any{
		"cpu":                findingEvidence(rec.CPU),
		"mem":                findingEvidence(rec.Mem),
		"pods":               w.Pods,
		"podsStableInWindow": w.PodsStable,
	}
	if closed := failClosed(w.Webhooks); len(closed) > 0 {
		ev["webhooks"] = closed
	}
	qos := qosChange(w.Template, cr)
	if qos != nil {
		ev["qos"] = qos
	}
	evidence, _ := json.Marshal(ev)

	risk, reasons := rightsizingRisk(w, lowConfidence, lowersMemory, qos != nil && qos.Before == QoSGuaranteed)
	var how []string
	if requestNote {
		how = append(how, fmt.Sprintf("request ×%s", decimal(rightsizing.RequestHeadroom)))
	}
	if limitNote {
		how = append(how, fmt.Sprintf("limit ×%s", decimal(rightsizing.LimitHeadroom)))
	}
	pods := fmt.Sprintf("%d pods", w.Pods)
	if w.Pods == 1 {
		pods = "1 pod"
	}
	// The engine's window is 7 days, as the Capacity screen says; how much of
	// it a younger cluster has is engine.windowDays, and the plan keeps it there.
	provenance := fmt.Sprintf("rightsizing · P95 7 d · %s · %s", strings.Join(how, " · "), pods)

	return Step{
		Action:       ActionSetResources,
		Target:       target,
		Params:       params,
		Before:       beforeJSON,
		Evidence:     evidence,
		Precondition: Precondition{SpecHash: SpecHash(w.Template), Generation: w.Generation},
		Verification: Verification{Window: "24h", Checks: append([]string(nil), rightsizingChecks...)},
		Rollback:     &Rollback{Action: ActionSetResources, Params: rollbackJSON},
		Risk:         risk,
		RiskReasons:  reasons,
		Impact:       Impact{Estimated: Savings{CPUMilli: cpuSaved, MemBytes: memSaved}},
		Provenance:   provenance,
		Status:       StepPending,
	}, nearLimit, nil
}

// rightsizingRisk applies the spec's fixed rules: high for a StatefulSet, an
// imposed value or low confidence; medium for a lower memory request, a
// single pod or a DaemonSet; low otherwise. Every reason that applies is
// listed, not only the one that set the level.
func rightsizingRisk(w WorkloadFacts, lowConfidence, lowersMemory, leavesGuaranteed bool) (RiskLevel, []string) {
	var high, medium []string
	if leavesGuaranteed {
		high = append(high, RiskReasonQoSChange)
	}
	if w.Type == "statefulsets" {
		high = append(high, RiskReasonStatefulSet)
	}
	if lowConfidence {
		high = append(high, RiskReasonLowConfidence)
	}
	if len(failClosed(w.Webhooks)) > 0 {
		high = append(high, RiskReasonAdmissionWebhook)
	}
	if lowersMemory {
		medium = append(medium, RiskReasonLowersMemory)
	}
	if w.Pods == 1 {
		medium = append(medium, RiskReasonSinglePod)
	}
	if w.Type == "daemonsets" {
		medium = append(medium, RiskReasonDaemonSet)
	}
	reasons := append(append([]string{}, high...), medium...)
	switch {
	case len(high) > 0:
		return RiskHigh, reasons
	case len(medium) > 0:
		return RiskMedium, reasons
	}
	return RiskLow, reasons
}

// failClosed is the webhooks that fail closed, each name once: the ones the
// risk rests on (a configuration often registers the same webhook several
// times; one that fails open is no risk).
func failClosed(hooks []WebhookFacts) []WebhookFacts {
	var out []WebhookFacts
	seen := map[string]bool{}
	for _, h := range hooks {
		if h.FailurePolicy != "Fail" || seen[h.Name] {
			continue
		}
		seen[h.Name] = true
		out = append(out, h)
	}
	return out
}

func actionable(s rightsizing.State) bool { return s == rightsizing.Over || s == rightsizing.NearLimit }

func findingEvidence(f rightsizing.Finding) map[string]any {
	out := map[string]any{"state": f.State, "request": f.Request, "limit": f.Limit, "p95": f.P95}
	if f.Suggest > 0 {
		out["suggest"] = f.Suggest
	}
	return out
}

func noValueDetail(rec rightsizing.Recommendation) string {
	if rec.CPU.State == rightsizing.NoSpecs || rec.Mem.State == rightsizing.NoSpecs {
		return "no requests or limits are set, so the engine has no value to write"
	}
	return "the per-pod value rounds to what the pod already has"
}

func resourcesOf(rl corev1.ResourceList) *Resources {
	var r Resources
	if q, ok := rl[corev1.ResourceCPU]; ok {
		s := q.String()
		r.CPU = &s
	}
	if q, ok := rl[corev1.ResourceMemory]; ok {
		s := q.String()
		r.Memory = &s
	}
	if r.CPU == nil && r.Memory == nil {
		return nil
	}
	return &r
}

// quantity writes millicores or bytes the way a person would.
func quantity(name corev1.ResourceName, v int64) string {
	if name == corev1.ResourceCPU {
		return resource.NewMilliQuantity(v, resource.DecimalSI).String()
	}
	return resource.NewQuantity(v, resource.BinarySI).String()
}

func decimal(v float64) string {
	s := fmt.Sprintf("%.1f", v)
	return strings.TrimSuffix(s, ".0")
}

// TargetKey names a workload across plans: type/namespace/name.
func TargetKey(typ, namespace, name string) string { return typ + "/" + namespace + "/" + name }

// SameScope says whether two plans look at the same workloads: the same
// namespaces and the same workloads, in any order.
func SameScope(a, b Scope) bool {
	x, y := normalizeScope(a), normalizeScope(b)
	return slices.Equal(x.Namespaces, y.Namespaces) && slices.Equal(x.Workloads, y.Workloads)
}

// HasPendingStep says whether something in the plan is still to be done.
func (p Plan) HasPendingStep() bool {
	for _, s := range p.Steps {
		if s.Status == StepPending {
			return true
		}
	}
	return false
}

func normalizeScope(s Scope) Scope {
	out := Scope{Namespaces: append([]string{}, s.Namespaces...), Workloads: append([]string{}, s.Workloads...)}
	sort.Strings(out.Namespaces)
	sort.Strings(out.Workloads)
	return out
}

// InScope: an empty scope is the whole cluster. A workload is
// namespace/kind/name; the kind may be left out (namespace/name).
func InScope(s Scope, namespace, kind, name string) bool {
	if len(s.Namespaces) == 0 && len(s.Workloads) == 0 {
		return true
	}
	for _, ns := range s.Namespaces {
		if ns == namespace {
			return true
		}
	}
	for _, w := range s.Workloads {
		parts := strings.Split(w, "/")
		switch len(parts) {
		case 2:
			if parts[0] == namespace && parts[1] == name {
				return true
			}
		case 3:
			if parts[0] == namespace && kindMatches(parts[1], kind) && parts[2] == name {
				return true
			}
		}
	}
	return false
}

// kindMatches accepts the kind as the engine writes it (Deployment) or as a
// resource type (deployments, deployment), in any case.
func kindMatches(given, kind string) bool {
	g := strings.ToLower(given)
	k := strings.ToLower(kind)
	return g == k || g == k+"s"
}

// TypeForKind is the resource type of an engine workload kind, "" when the
// kind is not one right-sizing treats.
func TypeForKind(kind string) string { return kindTypes[kind] }
