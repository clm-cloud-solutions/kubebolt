package insights

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

// This file is #44's step 1 (PR 1.3 of the lifecycle plan): the hardcoded
// thresholds leave rules.go and become tunable per org through the
// rule_policies table. The catalog below is the single place that knows, for
// every shipped rule, WHICH knobs it has:
//
//   - class (D-3 partition, 15 malfunctions / 9 expectations): a malfunction
//     is broken-is-broken — its severity is LOCKED and it can never be turned
//     off by policy (the only full silence for one resource is a mute, #54);
//     an expectation is violated on purpose in dev — its severity is the
//     tunable knob, `off` included.
//   - threshold: only the six numeric rules have a bar to move. Moving the
//     bar keeps the rule meaning what it says (#44 §3.1); the rest are
//     boolean and have no dial.
//
// Fase 1 wires the GLOBAL layer only (category "global" — one override per
// org across all clusters). Fase 4 adds the per-environment-category matrix
// on the same table; the schema carries `category` from day one so nothing
// migrates later.

// RuleClass partitions the catalog per finding #44 (amended by D-3).
type RuleClass string

const (
	ClassMalfunction RuleClass = "malfunction"
	ClassExpectation RuleClass = "expectation"
)

// SeverityOff is the policy value that stops an expectation from being
// emitted at all. Only expectations accept it; a suppressed rule stays
// countable ("hidden by profile", Fase 4).
const SeverityOff = "off"

// PolicyDefault is one rule's shipped policy: its class and its default
// knob values. Threshold is meaningful only when HasThreshold.
type PolicyDefault struct {
	Class          RuleClass
	Severity       string  // shipped base severity (mirrors Rule.Severity)
	Threshold      float64 // shipped numeric bar
	HasThreshold   bool
	ThresholdLabel string // human unit for the UI: "restarts", "ratio of limit", "days"
	// Description: one operator-facing sentence of what the rule detects —
	// the hub shows it under the id (in-vivo 01-sep: an id alone assumes the
	// reader already knows all 24 rules).
	Description string
}

// PolicyDescriptions — one sentence per rule, kept OUT of the literal table
// above so the knobs stay scannable. A test pins full coverage.
var PolicyDescriptions = map[string]string{
	"crash-loop":                 "A container is stuck in a restart loop — it starts, crashes, and Kubernetes backs off before trying again.",
	"frequent-restarts":          "A container restarts often without a full crash loop — flapping under load, OOM edges, or an unstable dependency.",
	"oom-killed":                 "A container was killed for exceeding its memory limit.",
	"image-pull-backoff":         "A pod cannot pull its image — wrong tag, missing registry credentials, or an unreachable registry.",
	"pvc-pending":                "A PersistentVolumeClaim is stuck Pending — nothing can provision the storage it asks for.",
	"node-not-ready":             "A node stopped reporting Ready — its workloads are unreachable or about to be evicted.",
	"zero-replicas":              "A workload that should be running has zero available replicas.",
	"progress-deadline-exceeded": "A rollout stalled past its progress deadline — the new pods never became ready.",
	"evicted-pods":               "Pods were evicted from a node — usually memory or disk pressure on the node itself.",
	"readiness-probe-failing":    "Pods run but fail their readiness probe, so Services send them no traffic.",
	"liveness-probe-failing":     "The liveness probe keeps failing — Kubernetes restarts the container again and again.",
	"missing-config-dependency":  "A pod references a ConfigMap or Secret that does not exist.",
	"helm-release-failed":        "A Helm release is in a failed state — its last install or upgrade did not complete.",
	"helm-release-hook-pending":  "A Helm release is stuck on pending hooks — an install or upgrade never finished.",
	"argocd-out-of-sync":         "An Argo CD application drifted from its desired state in Git.",
	"pdb-no-match":               "A PodDisruptionBudget matches no pods — it protects nothing during drains or node maintenance.",
	"policy-no-match":            "A NetworkPolicy's podSelector matches no pods — the declared traffic intent is unverifiable.",
	"policy-orphan":              "A namespace has running pods but no NetworkPolicy at all.",
	"resource-underrequest":      "CPU requests sit far below real usage — the scheduler is planning with wrong numbers.",
	"cpu-throttle-risk":          "CPU usage is near the limit — the container is being throttled, or is about to be.",
	"memory-pressure":            "Memory usage is near the limit — the container risks an OOM kill.",
	"hpa-maxed-out":              "An autoscaler is pinned at its maximum replicas — no headroom left to absorb load.",
	"cert-expiring":              "A certificate is approaching its expiry date.",
	"service-no-endpoints":       "A Service has no ready endpoints — its selector matches nothing, or nothing behind it is ready.",
}

// PolicyCatalog maps every shipped rule ID to its policy defaults. A test
// pins this against AllRules() — a rule added without a catalog entry (or
// vice versa) fails the build's tests, so the partition can't drift silently.
var PolicyCatalog = map[string]PolicyDefault{
	// ── malfunctions (15) — broken is broken, anywhere ──────────────────
	"crash-loop":                 {Class: ClassMalfunction, Severity: "critical", Threshold: 3, HasThreshold: true, ThresholdLabel: "restarts"},
	"frequent-restarts":          {Class: ClassMalfunction, Severity: "warning", Threshold: 5, HasThreshold: true, ThresholdLabel: "restarts"},
	"oom-killed":                 {Class: ClassMalfunction, Severity: "critical"},
	"image-pull-backoff":         {Class: ClassMalfunction, Severity: "critical"},
	"pvc-pending":                {Class: ClassMalfunction, Severity: "warning"},
	"node-not-ready":             {Class: ClassMalfunction, Severity: "critical"},
	"zero-replicas":              {Class: ClassMalfunction, Severity: "critical"},
	"progress-deadline-exceeded": {Class: ClassMalfunction, Severity: "critical"},
	"evicted-pods":               {Class: ClassMalfunction, Severity: "warning"},
	"readiness-probe-failing":    {Class: ClassMalfunction, Severity: "warning"},
	"liveness-probe-failing":     {Class: ClassMalfunction, Severity: "warning"},
	"missing-config-dependency":  {Class: ClassMalfunction, Severity: "critical"},
	"helm-release-failed":        {Class: ClassMalfunction, Severity: "critical"},
	"helm-release-hook-pending":  {Class: ClassMalfunction, Severity: "warning"},
	"argocd-out-of-sync":         {Class: ClassMalfunction, Severity: "warning"},

	// ── expectations (9) — violated on purpose in a dev cluster ─────────
	"pdb-no-match":          {Class: ClassExpectation, Severity: "warning"},
	"policy-no-match":       {Class: ClassExpectation, Severity: "warning"},
	"policy-orphan":         {Class: ClassExpectation, Severity: "info"},
	"resource-underrequest": {Class: ClassExpectation, Severity: "info", Threshold: 0.40, HasThreshold: true, ThresholdLabel: "request/usage ratio"},
	"cpu-throttle-risk":     {Class: ClassExpectation, Severity: "warning", Threshold: 0.80, HasThreshold: true, ThresholdLabel: "usage/limit ratio"},
	"memory-pressure":       {Class: ClassExpectation, Severity: "warning", Threshold: 0.85, HasThreshold: true, ThresholdLabel: "usage/limit ratio"},
	"hpa-maxed-out":         {Class: ClassExpectation, Severity: "warning"},
	"cert-expiring":         {Class: ClassExpectation, Severity: "warning", Threshold: 14, HasThreshold: true, ThresholdLabel: "days before expiry"},
	// Reclassified from malfunction by D-3 (2026-08-31): 101/274 of the
	// DIPRES noise, no numeric knob, and routinely intentional in dev.
	"service-no-endpoints": {Class: ClassExpectation, Severity: "warning"},
}

// StoredRulePolicy is one persisted override row (sparse: only what an org
// changed exists in the table; everything else falls back to the catalog).
type StoredRulePolicy struct {
	RuleID    string    `json:"ruleId"`
	Class     RuleClass `json:"class"`
	Category  string    `json:"category"` // "global" in Fase 1; env categories in Fase 4
	Threshold *float64  `json:"threshold,omitempty"`
	Severity  *string   `json:"severity,omitempty"`
	UpdatedBy string    `json:"updatedBy,omitempty"`
	UpdatedAt time.Time `json:"updatedAt,omitempty"`
}

// PolicyCategoryGlobal is the Fase-1 layer: one override per org, all
// clusters. The env categories joined in Fase 4 and LAYER on top of it:
// effective = env category override > global override > shipped default,
// knob by knob (an env threshold composes with a global severity).
const PolicyCategoryGlobal = "global"

// PolicyCategories are the four CLOSED environment categories (#44/#14 —
// closed because the same field prices the node-hour; inventable categories
// would be inventable discounts). A cluster maps to exactly one; an
// unclassified cluster evaluates with the global layer only.
var PolicyCategories = []string{"production", "staging", "testing", "development"}

// ValidatePolicyCategory accepts the global layer or one of the four closed
// env categories.
func ValidatePolicyCategory(category string) error {
	if category == PolicyCategoryGlobal {
		return nil
	}
	for _, c := range PolicyCategories {
		if category == c {
			return nil
		}
	}
	return fmt.Errorf("category must be global|%s, got %q", strings.Join(PolicyCategories, "|"), category)
}

// ValidatePolicyChange enforces the class contract for one override before
// it is stored — the same rules the #44 editors encode visually:
// malfunctions move only their bar (and only if they have one); expectations
// move only their severity, `off` allowed. Shared by the HTTP handler and
// any future caller so the contract can't fork.
func ValidatePolicyChange(ruleID string, threshold *float64, severity *string) error {
	def, ok := PolicyCatalog[ruleID]
	if !ok {
		return fmt.Errorf("unknown rule %q", ruleID)
	}
	if threshold == nil && severity == nil {
		return fmt.Errorf("nothing to change: provide threshold or severity")
	}
	if threshold != nil {
		if !def.HasThreshold {
			return fmt.Errorf("rule %q has no numeric threshold to move", ruleID)
		}
		if *threshold <= 0 {
			return fmt.Errorf("threshold must be > 0")
		}
	}
	if severity != nil {
		if def.Class != ClassExpectation {
			return fmt.Errorf("rule %q is a malfunction — its severity is locked (broken is broken); use a per-resource mute for an accepted deviation", ruleID)
		}
		switch *severity {
		case "critical", "warning", "info", SeverityOff:
		default:
			return fmt.Errorf("severity must be critical|warning|info|off, got %q", *severity)
		}
	}
	return nil
}

// PolicySnapshot is the org-effective policy the engine consults during one
// evaluation: sparse override maps, catalog defaults for everything absent.
// The zero value means "shipped defaults" and is always safe.
type PolicySnapshot struct {
	Thresholds map[string]float64
	Severities map[string]string
}

// ThresholdFor returns the effective numeric bar for a rule.
func (p PolicySnapshot) ThresholdFor(ruleID string, def float64) float64 {
	if v, ok := p.Thresholds[ruleID]; ok && v > 0 {
		return v
	}
	return def
}

// ─── policy source seam ────────────────────────────────────────────────────
// Engines are created per cluster runtime deep inside the cluster manager;
// threading a store handle through that construction would touch every call
// site for what is one process-wide seam. Same pattern as the agent's
// ActiveAggregator: a package-level atomic the EE wiring sets at boot. OSS
// never sets it and every engine evaluates with shipped defaults.

// The source is CLUSTER-aware since Fase 4: the resolver maps the cluster to
// its environment category and layers that category's overrides on top of
// the global ones. An engine only ever knows (tenant, cluster) — the env
// mapping is billing metadata it must not carry.
type policySourceFn func(tenant, cluster string) PolicySnapshot

var policySource atomic.Value // policySourceFn

// SetPolicySource installs the per-(tenant, cluster) policy resolver (EE
// wiring). Passing nil resets to shipped defaults (used by tests).
func SetPolicySource(fn func(tenant, cluster string) PolicySnapshot) {
	policySource.Store(policySourceFn(fn))
}

func snapshotFor(tenant, cluster string) PolicySnapshot {
	if fn, ok := policySource.Load().(policySourceFn); ok && fn != nil {
		return fn(tenant, cluster)
	}
	return PolicySnapshot{}
}

// stateThreshold is the nil-safe helper the numeric rules call: the
// evaluation's snapshot if the engine attached one, the shipped default
// otherwise. Rules never know where the number came from.
func stateThreshold(state *ClusterState, ruleID string, def float64) float64 {
	if state == nil || state.Policies == nil {
		return def
	}
	return state.Policies.ThresholdFor(ruleID, def)
}
