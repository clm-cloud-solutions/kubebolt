// Package kobiplan is the Kobi Plan entity: a reviewable, ordered set of
// changes to one cluster, each with its evidence, verification, rollback and
// precondition, and what the plan is expected to save and did save.
//
// One entity for everyone (decision d24): Kobi creates it from a conversation,
// and Autopilot will execute the same entity later. The v0 fills every axis
// with a single value (kind rightsizing, action set_resources, export as diff
// or commands, a person in the chat) but the schema leaves the doors open, so
// the next plan type, a pull request or Autopilot arrive without migrating it
// (Kobi Plans spec, «Puertas abiertas»; decisions P15–P21, d81–d83).
//
// The package holds the types, their validation and the two hashes. Kobi never
// writes a number into a plan: the backend builds it from deterministic
// engines (rightsizing, OpenCost), and Validate refuses what the schema does
// not allow. Storage is PlanStore (copilot); building is create_plan.
//
// It is called kobiplan, not plan: «plan» is already the billing plan
// (auth.PlanCatalog), which is also why the tables are kobi_plans_* (P7).
package kobiplan

import (
	"encoding/json"
	"time"
)

// Kind is the plan's type: what changes. One plan, one kind (P15); a program
// groups plans of several kinds.
type Kind string

const (
	KindRightsizing Kind = "rightsizing"
)

// Kinds are the plan kinds create_plan builds.
var Kinds = []Kind{KindRightsizing, KindConsolidation}

// Status of a plan.
type Status string

const (
	StatusDraft    Status = "draft"
	StatusExported Status = "exported"
	StatusVerified Status = "verified"
	StatusExpired  Status = "expired"
	// Reserved for Autopilot (d24). Valid in the schema; nothing sets them in v0.
	StatusApproved  Status = "approved"
	StatusExecuting Status = "executing"
	StatusFailed    Status = "failed"
)

// Live reports whether a plan still counts against the Free plan's limit of
// one live plan per cluster (P21): a draft or an exported plan.
func (s Status) Live() bool { return s == StatusDraft || s == StatusExported }

// ExpiredReason says why a plan reached StatusExpired.
type ExpiredReason string

const (
	// ExpiredUnexported: a draft nobody exported in 7 days (P5).
	ExpiredUnexported ExpiredReason = "unexported"
	// ExpiredDiscarded: a person discarded it, e.g. to start another on a Free
	// cluster (P21).
	ExpiredDiscarded ExpiredReason = "discarded"
	// ExpiredChanged: every step's workload changed (or went away) before
	// the step was applied, so nothing in the plan is left to do (plans-07).
	ExpiredChanged ExpiredReason = "changed"
)

// StepStatus of one step. A step expires, not the plan.
type StepStatus string

const (
	StepPending  StepStatus = "pending"
	StepApplied  StepStatus = "applied"
	StepVerified StepStatus = "verified"
	StepExpired  StepStatus = "expired"
)

// Initiator says who asked for the plan.
type Initiator string

const (
	InitiatorChat      Initiator = "chat"
	InitiatorInsight   Initiator = "insight"
	InitiatorFinding   Initiator = "finding"
	InitiatorAutopilot Initiator = "autopilot"
	InitiatorMandate   Initiator = "mandate"
)

// Source is where the plan came from. The ids that apply depend on the
// initiator: a person in the chat has a user and a conversation; Autopilot an
// incident; a mandate its own id.
type Source struct {
	Initiator Initiator `json:"initiator"`
	UserID    string    `json:"userId,omitempty"`
	// UserName is the person's name as it was when the plan was made, for the
	// plan's view («created by …»); the id is what identifies them.
	UserName       string `json:"userName,omitempty"`
	ConversationID string `json:"conversationId,omitempty"`
	IncidentID     string `json:"incidentId,omitempty"`
	MandateID      string `json:"mandateId,omitempty"`
}

// Scope is what the plan looked at. Empty lists mean the whole cluster.
type Scope struct {
	Namespaces []string `json:"namespaces"`
	// Workloads as namespace/kind/name (rightsizing.Key).
	Workloads []string `json:"workloads"`
}

// Engine is the deterministic engine every number of the plan came from.
type Engine struct {
	Name            string    `json:"name"`
	Percentile      float64   `json:"percentile,omitempty"`
	WindowDays      float64   `json:"windowDays,omitempty"`
	MinHistoryHours int       `json:"minHistoryHours,omitempty"`
	LowConfidence   bool      `json:"lowConfidence"`
	ComputedAt      time.Time `json:"computedAt"`
}

// Money is an amount per month in one currency.
type Money struct {
	Monthly  float64 `json:"monthly"`
	Currency string  `json:"currency"`
	// Source of the prices, e.g. "opencost".
	Source string `json:"source,omitempty"`
}

// Why money is missing from an estimate.
const (
	MoneyNoCostData = "no_cost_data"
)

// Savings is an estimate: always in units, in money when OpenCost sends
// prices. Raising a limit saves nothing and adds nothing.
type Savings struct {
	CPUMilli int64  `json:"cpuMilli"`
	MemBytes int64  `json:"memBytes"`
	Money    *Money `json:"money"`
	// MoneyUnavailable says why Money is nil (MoneyNoCostData). Empty when
	// Money is set.
	MoneyUnavailable string `json:"moneyUnavailable,omitempty"`
}

// CostDelta is a monthly cost before and after, in one currency.
type CostDelta struct {
	Before   float64 `json:"before"`
	After    float64 `json:"after"`
	Currency string  `json:"currency"`
}

// ClusterCostDelta is the cluster's cost over the same two windows. It is
// context and never attributed to the plan (P20): it only falls if nodes are
// removed afterwards.
type ClusterCostDelta struct {
	CostDelta
	Attributed bool `json:"attributed"`
}

// Observed is the measured impact, 7 days after a step is applied, against
// the baseline captured at export (P20).
type Observed struct {
	MeasuredAt    time.Time `json:"measuredAt"`
	WindowDays    int       `json:"windowDays"`
	CPUMilliFreed int64     `json:"cpuMilliFreed"`
	MemBytesFreed int64     `json:"memBytesFreed"`
	// AllocatedCost is what OpenCost assigns to the workload: the larger of
	// request and usage, times the node's price. Nil without OpenCost.
	AllocatedCost *CostDelta `json:"allocatedCost,omitempty"`
	// ClusterCost is set on the plan, never on a step.
	ClusterCost *ClusterCostDelta `json:"clusterCost,omitempty"`
}

// Impact is what a plan or a step is expected to bring and what it brought.
// An object rather than a savings figure: a security plan closes findings and
// saves no money, and those fields arrive here without a migration.
type Impact struct {
	Estimated Savings   `json:"estimated"`
	Observed  *Observed `json:"observed"`
}

// Baseline is captured at the first export and never changes after (P20).
// A step keeps its allocated cost; the plan keeps the cluster's cost.
// WindowDays is how much data the amounts average: up to 7, less on a younger
// cluster (3.1 when it has 3.1 days), none without amounts.
type Baseline struct {
	CapturedAt    time.Time `json:"capturedAt"`
	WindowDays    float64   `json:"windowDays,omitempty"`
	AllocatedCost *Money    `json:"allocatedCost,omitempty"`
	ClusterCost   *Money    `json:"clusterCost,omitempty"`
	// ClusterCostCovers is what ClusterCost adds up: CoversBill for the
	// cluster's bill at list prices (nodes, disks, load balancers, the
	// management fee); empty for the nodes alone, the baseline of a plan
	// exported before 26-nov. The cost after is measured on the same parts.
	ClusterCostCovers string `json:"clusterCostCovers,omitempty"`
}

// CoversBill marks a cluster cost that is the cluster's bill, not its nodes
// alone. Freeing requests lowers neither: the bill falls only when a node, a
// disk or a load balancer goes.
const CoversBill = "bill"

// ExportFormat is how a plan leaves KubeBolt.
type ExportFormat string

const (
	ExportDiff     ExportFormat = "diff"
	ExportCommands ExportFormat = "commands"
	// Reserved: the pull request and apply-now channels.
	ExportPR    ExportFormat = "pr"
	ExportApply ExportFormat = "apply"
)

// Export records one export.
type Export struct {
	Format ExportFormat `json:"format"`
	At     time.Time    `json:"at"`
	By     string       `json:"by,omitempty"`
}

// OwnerKind is who owns the object a step changes, which decides whether the
// step can be executed and through which channel.
type OwnerKind string

const (
	// OwnerProvider: AKS, EKS, GKE or the addon manager reconcile it. Never a
	// step: it is excluded with ReasonProviderManaged.
	OwnerProvider OwnerKind = "provider"
	// OwnerOperator: an operator's own resource owns it; the change goes there.
	OwnerOperator OwnerKind = "operator"
	// OwnerHelm: the next helm upgrade reverts a manual change.
	OwnerHelm OwnerKind = "helm"
	// OwnerGitOps: Argo CD or Flux self-heal reverts it; only a PR sticks.
	OwnerGitOps OwnerKind = "gitops"
	// OwnerNone: nobody reconciles it; applying it now sticks.
	OwnerNone OwnerKind = "none"
	// OwnerUnknown: not detected. The step goes in with a warning (P16).
	OwnerUnknown OwnerKind = "unknown"
)

// Owner of a step's target.
type Owner struct {
	Kind OwnerKind `json:"kind"`
	// Ref names the owner when known: "aks", a Helm release, an Argo CD
	// application, an operator resource.
	Ref string `json:"ref,omitempty"`
	// Namespace is where the owner lives when it is not the workload's: a
	// Helm release's namespace (meta.helm.sh/release-namespace), which a
	// helm command needs.
	Namespace string `json:"namespace,omitempty"`
}

// Target is the object a step changes or an exclusion names. Type is the
// resource type of the API paths (deployments, persistentvolumeclaims…), so a
// target can be any resource, not only a workload.
type Target struct {
	Type      string `json:"type"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	Owner     *Owner `json:"owner,omitempty"`
}

// RiskLevel of a step, assigned by fixed backend rules.
type RiskLevel string

const (
	RiskLow    RiskLevel = "low"
	RiskMedium RiskLevel = "medium"
	RiskHigh   RiskLevel = "high"
)

// Precondition is what must still hold for the step to apply as written.
type Precondition struct {
	// SpecHash is SpecHash of the pod template when the step was created.
	SpecHash   string `json:"specHash"`
	Generation int64  `json:"generation,omitempty"`
}

// Verification is how a step is checked after it is applied.
type Verification struct {
	Window string   `json:"window"`
	Checks []string `json:"checks"`
	// Result is the last time the checks ran (plans-07), nil before.
	Result *VerificationResult `json:"result,omitempty"`
}

// VerificationResult is one run of a step's checks: each check's outcome
// (CheckOK, CheckFailed, CheckNotApplicable, CheckUnknown) and, when one
// failed, what failed in words.
type VerificationResult struct {
	CheckedAt time.Time         `json:"checkedAt"`
	Passed    bool              `json:"passed"`
	Checks    map[string]string `json:"checks"`
	Detail    string            `json:"detail,omitempty"`
}

// Rollback is the step that undoes this one.
type Rollback struct {
	Action Action          `json:"action"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Step is one change. Params, Before and Evidence depend on the action and on
// the engine, so they stay raw here and are checked by kind in Validate: the
// next verb or engine adds its own shape without changing Step.
type Step struct {
	ID     string `json:"id"`
	Order  int    `json:"order"`
	Action Action `json:"action"`
	Target Target `json:"target"`
	// ExportOnly: the action has no Kobi verb yet (a PDB, a securityContext,
	// a Savings Plan), so the step can be exported but never executed.
	ExportOnly       bool            `json:"exportOnly"`
	ExportOnlyReason string          `json:"exportOnlyReason,omitempty"`
	Params           json.RawMessage `json:"params,omitempty"`
	Before           json.RawMessage `json:"before,omitempty"`
	Evidence         json.RawMessage `json:"evidence,omitempty"`
	Precondition     Precondition    `json:"precondition"`
	Verification     Verification    `json:"verification"`
	Rollback         *Rollback       `json:"rollback,omitempty"`
	Risk             RiskLevel       `json:"risk"`
	RiskReasons      []string        `json:"riskReasons"`
	// Imposed: the value came from the person, not the engine (revise_plan,
	// or a step applied with other values, d85). EngineParams then keeps the
	// engine's own values, for reference.
	Imposed      bool            `json:"imposed"`
	EngineParams json.RawMessage `json:"engineParams,omitempty"`
	Impact       Impact          `json:"impact"`
	Baseline     *Baseline       `json:"baseline"`
	Provenance   string          `json:"provenance,omitempty"`
	Status       StepStatus      `json:"status"`
	// AppliedAt is when the step was applied, or found applied (plans-07);
	// its checks run VerifyAfter later and its impact is measured ImpactAfter
	// later. VerifiedAt is when it passed them.
	AppliedAt  *time.Time `json:"appliedAt,omitempty"`
	VerifiedAt *time.Time `json:"verifiedAt,omitempty"`
	// NodesGone are a consolidation step's nodes already removed (plans-16):
	// the export removes them one at a time, and the step is applied only
	// when the pool is at its new size. Not part of what the plan proposes.
	NodesGone []string `json:"nodesGone,omitempty"`
	// NodesCordoned are its nodes still in the pool but cordoned: being
	// emptied, the step under way.
	NodesCordoned []string `json:"nodesCordoned,omitempty"`
}

// ExclusionReason says why something the engine flagged is not a step.
type ExclusionReason string

const (
	ReasonMultiContainer ExclusionReason = "multi_container"
	ReasonHPATarget      ExclusionReason = "hpa_target"
	ReasonPodsVaried     ExclusionReason = "pods_varied"
	ReasonNoEngineValue  ExclusionReason = "no_engine_value"
	// ReasonProviderManaged: the provider reconciles the object (AKS
	// kubernetes.azure.com/managedby, EKS eks.amazonaws.com/component or the
	// eks field manager, GKE components.gke.io/*, addonmanager Reconcile).
	ReasonProviderManaged ExclusionReason = "provider_managed"
	// ReasonOperatorManaged: an operator's resource is the object's controller
	// and reverts a change made to it; the change belongs in that resource.
	ReasonOperatorManaged ExclusionReason = "operator_managed"
	// ReasonInLivePlan: another live plan of the cluster has a pending step
	// for the workload (plans-06). Changed there, so its saving is not
	// counted twice.
	ReasonInLivePlan ExclusionReason = "in_live_plan"
	// Reserved.
	ReasonGitOpsUnknown ExclusionReason = "gitops_unknown"
	ReasonOwnerUnknown  ExclusionReason = "owner_unknown"
)

// Exclusion is something the engine flagged that the plan does not treat.
type Exclusion struct {
	Target       Target          `json:"target"`
	Reason       ExclusionReason `json:"reason"`
	Detail       string          `json:"detail,omitempty"`
	ReevaluateAt *time.Time      `json:"reevaluateAt,omitempty"`
}

// Note records something that happened to the plan, such as a rejected
// change with the data that justified the rejection.
type Note struct {
	Version int       `json:"version"`
	At      time.Time `json:"at"`
	Text    string    `json:"text"`
}

// Plan is the entity. Every version is kept; ContentHash compares them.
type Plan struct {
	ID        string `json:"id"`
	OrgID     string `json:"orgId"`
	ClusterID string `json:"clusterId"`
	Title     string `json:"title"`
	Kind      Kind   `json:"kind"`
	Scope     Scope  `json:"scope"`
	Version   int    `json:"version"`
	Status    Status `json:"status"`
	// ExpiredReason is set with StatusExpired.
	ExpiredReason ExpiredReason `json:"expiredReason,omitempty"`
	CreatedAt     time.Time     `json:"createdAt"`
	// UpdatedAt is set by the store on every write; retention counts from it.
	UpdatedAt time.Time `json:"updatedAt"`
	Source    Source    `json:"source"`
	// ProgramID is reserved (P19): a program groups plans; it starts as a
	// report and becomes an entity when Autopilot executes plans in order.
	ProgramID   *string     `json:"programId"`
	ExpiresAt   time.Time   `json:"expiresAt"`
	ExportedAt  *time.Time  `json:"exportedAt"`
	Exports     []Export    `json:"exports"`
	Engine      Engine      `json:"engine"`
	Impact      Impact      `json:"impact"`
	Baseline    *Baseline   `json:"baseline"`
	Steps       []Step      `json:"steps"`
	Excluded    []Exclusion `json:"excluded"`
	Notes       []Note      `json:"notes"`
	ContentHash string      `json:"contentHash"`
	// DependsOn are the plans whose changes this one counts on (plans-16):
	// a consolidation plan computed on the requests a live right-sizing plan
	// leaves. Its steps wait until those changes are in the cluster.
	DependsOn []string `json:"dependsOn,omitempty"`
}
