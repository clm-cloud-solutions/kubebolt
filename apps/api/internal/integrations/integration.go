// Package integrations models the pluggable "things you can connect
// to the cluster to unlock extra KubeBolt features" — starting with
// the kubebolt-agent and designed to host future adapters (Istio,
// Linkerd, conntrack collector, etc.) behind a uniform REST
// surface.
//
// The package is deliberately narrow in this first cut: Providers
// only report read-only state (Detect). Install / Configure /
// Uninstall land in a follow-up; the Provider interface will grow
// when they do.
package integrations

import (
	"context"
	"encoding/json"

	"k8s.io/client-go/kubernetes"
)

// Status is what the UI shows on the integration card.
type Status string

const (
	// StatusNotInstalled — nothing matching the integration's
	// detection signature lives in the cluster.
	StatusNotInstalled Status = "not_installed"

	// StatusInstalled — detected and healthy enough to be considered
	// operational (for the agent: desired == ready).
	StatusInstalled Status = "installed"

	// StatusDegraded — detected but unhealthy. For the agent this is
	// "DaemonSet exists but some pods aren't Ready". For other
	// integrations it may mean "present but config looks wrong".
	StatusDegraded Status = "degraded"

	// StatusUnknown — we couldn't tell. Most commonly a permissions
	// issue (the user's kubeconfig doesn't allow listing the
	// workloads that back this integration). Distinct from
	// NotInstalled so the UI can render the right message.
	StatusUnknown Status = "unknown"
)

// FeatureFlag is one toggleable sub-feature of an integration. Maps
// to an env var, ConfigMap key, or annotation on the underlying
// workload. For the agent these are things like "Hubble flow
// collector on/off".
type FeatureFlag struct {
	// Key is the stable identifier used in PUT /config and in URLs.
	// Not displayed as-is to end users.
	Key string `json:"key"`

	// Label is the user-facing short name. E.g. "Hubble flow
	// collector".
	Label string `json:"label"`

	// Description explains what turning this on/off actually does.
	Description string `json:"description,omitempty"`

	// Enabled is the currently-observed state (not the desired
	// state — Detect reports what's running). For boolean flags
	// this is the only signal the UI needs; for multi-state flags
	// (see Value below) Enabled doubles as "is this in any non-
	// default state?" so the UI can pick a green vs grey pill.
	Enabled bool `json:"enabled"`

	// Value carries non-boolean state for flags that aren't a
	// simple on/off — e.g. the agent's RBAC mode is metrics /
	// reader / operator, not a yes/no. When Value is non-empty the
	// UI renders it instead of the on/off pill. Empty means the
	// flag is binary (drop the field from JSON to keep payloads
	// tight).
	Value string `json:"value,omitempty"`

	// Requires lists logical preconditions the UI can surface as a
	// "needs Cilium installed" hint. Opaque strings — the UI
	// interprets them.
	Requires []string `json:"requires,omitempty"`
}

// Health is the runtime snapshot the UI shows under status. Fields
// are nullable because not every integration has workloads (e.g.
// future Prometheus remote adapters might not).
type Health struct {
	// Workloads present / healthy. For the agent DaemonSet:
	// PodsReady = numberReady, PodsDesired = desiredNumberScheduled.
	PodsReady   int `json:"podsReady"`
	PodsDesired int `json:"podsDesired"`

	// Message is a human-readable one-liner for when status isn't
	// obvious from the numbers. Empty for the healthy case.
	Message string `json:"message,omitempty"`
}

// Integration is the object the list/detail handlers serialize to
// the UI. Combines static metadata (what this integration IS) with
// dynamic cluster state (what Detect found).
type Integration struct {
	// ID is stable across versions — the URL segment in
	// /integrations/:id and the key used in admin actions.
	ID string `json:"id"`

	// Name is the display name shown on the card.
	Name string `json:"name"`

	// Description is one or two sentences; shown on the card and
	// in the detail panel header.
	Description string `json:"description"`

	// DocsURL points at the operator-facing docs for this
	// integration. Rendered as a "Learn more" link.
	DocsURL string `json:"docsUrl,omitempty"`

	// Capabilities advertises what data streams this integration
	// contributes so the UI can cross-reference ("Traffic map
	// requires the 'flows' capability; install an integration that
	// provides it").
	Capabilities []string `json:"capabilities,omitempty"`

	// ─── Dynamic fields — populated by Detect ───────────
	Status    Status        `json:"status"`
	Version   string        `json:"version,omitempty"`
	Namespace string        `json:"namespace,omitempty"`
	Features  []FeatureFlag `json:"features,omitempty"`
	Health    *Health       `json:"health,omitempty"`

	// Managed reports whether the detected workload carries the
	// managed-by=kubebolt label — i.e. KubeBolt installed it.
	// False for workloads installed via Helm, kubectl apply, or
	// any other out-of-band path. The UI uses this to decide
	// whether to expose Uninstall / Configure actions; KubeBolt
	// will not mutate workloads it didn't create.
	// Meaningful only when Status is Installed or Degraded.
	Managed bool `json:"managed"`
}

// Provider is what each concrete integration implements. The
// registry dispatches REST calls to the provider whose ID matches.
type Provider interface {
	// Meta returns the static metadata (name, description, docs,
	// capabilities). Must not touch the cluster — called from the
	// list handler's fast path even when the cluster is unavailable.
	Meta() Integration

	// Detect queries the active cluster and returns the Integration
	// with dynamic fields (Status, Version, Health, Features)
	// populated. The returned Integration embeds Meta() so callers
	// get a complete snapshot.
	//
	// Read-only. Errors indicate "could not determine state" — the
	// handler surfaces these as StatusUnknown, not as HTTP errors.
	Detect(ctx context.Context, cs kubernetes.Interface) (Integration, error)
}

// Installable is implemented by integrations that can be installed
// and removed via the backend. Optional — integrations that only
// consume external state (e.g. a Prometheus remote-read adapter)
// can omit this interface, in which case the install/uninstall
// handlers return 405 Method Not Allowed.
//
// Each provider decodes its own per-integration config shape from
// the JSON payload so the interface stays uniform while future
// integrations can collect wildly different values from the UI.
type Installable interface {
	Provider

	// Install lays down the workloads this integration needs. Must
	// be idempotent to the extent that running it twice with the
	// same config doesn't mutate cluster state. Returning a
	// ConflictError signals "something exists that wasn't put there
	// by us" — the handler surfaces that to the UI so the admin can
	// either reconcile or cancel.
	Install(ctx context.Context, cs kubernetes.Interface, configJSON json.RawMessage) error

	// Uninstall removes everything this integration put in the
	// cluster. Default behavior (Force=false) refuses to touch
	// resources that don't carry the managed-by=kubebolt label,
	// returning NotManagedError. Force=true bypasses that check —
	// the caller takes responsibility for deleting resources put
	// there by another tool (helm, raw kubectl).
	// Returns nil when nothing exists (already gone).
	Uninstall(ctx context.Context, cs kubernetes.Interface, opts UninstallOptions) error
}

// UninstallOptions controls the uninstall's blast radius. Force is
// the operator-confirmed escape hatch from the managed-by safety
// check; it exists so an admin can still remove an agent installed
// via helm / kubectl through KubeBolt's UI, not just via the
// original install tool.
type UninstallOptions struct {
	Force bool `json:"force,omitempty"`
}

// Configurable is implemented by integrations whose settings can be
// edited in place, without uninstall + reinstall. Optional —
// integrations that only install/remove can omit this, in which
// case the config endpoints return 405.
//
// The interface pair (GetConfig / Configure) keeps read and write
// symmetric: the UI calls GetConfig to pre-populate its editor and
// PUTs the edited document back through Configure.
//
// Scope: Configure only edits an existing managed install. If the
// workload isn't there we return a NotInstalledError; if it exists
// but wasn't installed by KubeBolt we return NotManagedError.
// Those map to distinct HTTP status codes so the UI can guide the
// operator to Install / Force uninstall instead.
type Configurable interface {
	Provider

	// GetConfig reads the live cluster state and returns the
	// current configuration as a JSON document in the provider's
	// own schema. Used to pre-populate the configure form.
	GetConfig(ctx context.Context, cs kubernetes.Interface) (json.RawMessage, error)

	// Configure applies the given config to the existing install.
	// The payload must be a full, valid config (not a diff) — the
	// UI always reads the current config first, edits in memory,
	// then sends the whole thing back.
	Configure(ctx context.Context, cs kubernetes.Interface, configJSON json.RawMessage) error
}

// NotInstalledError signals that a configure/modify operation was
// called on an integration that isn't present. Distinct from
// NotManagedError (present but external) and from a plain "missing"
// state (absent — which for Uninstall is a happy no-op).
type NotInstalledError struct {
	IntegrationID string
}

func (e *NotInstalledError) Error() string {
	return "integration " + e.IntegrationID + " is not installed"
}

// ConflictError signals that an install encountered a resource it
// did not create and is unwilling to overwrite. The handler
// translates this into HTTP 409 so the UI can render a targeted
// reconcile dialog instead of a generic error.
type ConflictError struct {
	// Kind/Namespace/Name identify the conflicting object.
	Kind      string
	Namespace string
	Name      string
	// Reason is a human-readable one-liner explaining why the
	// conflict stopped the install. E.g. "already exists and was
	// not installed by KubeBolt".
	Reason string
}

func (e *ConflictError) Error() string {
	if e.Namespace != "" {
		return e.Kind + " " + e.Namespace + "/" + e.Name + ": " + e.Reason
	}
	return e.Kind + " " + e.Name + ": " + e.Reason
}

// NotManagedError signals that the operation refused to touch an
// existing workload because KubeBolt didn't install it. The handler
// maps this to HTTP 409 so the UI distinguishes "nothing happened"
// (silent no-op) from "we stopped because it isn't ours" (needs
// operator action via helm/kubectl).
type NotManagedError struct {
	Kind      string
	Namespace string
	Name      string
}

func (e *NotManagedError) Error() string {
	return e.Kind + " " + e.Namespace + "/" + e.Name + " exists but was not installed by KubeBolt; remove it with helm or kubectl"
}

// Management label applied to every resource created by the backend
// install flow. Uninstall lists and deletes by this label so we
// never touch resources put there by helm, kubectl apply, or
// another KubeBolt-like tool.
const (
	ManagedByLabel = "app.kubernetes.io/managed-by"
	ManagedByValue = "kubebolt"
)
