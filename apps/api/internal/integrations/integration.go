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
	// state — Detect reports what's running).
	Enabled bool `json:"enabled"`

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
	// cluster. Implementations identify their own resources via a
	// management label — anything not labeled that way is left
	// alone, so an external Helm install isn't clobbered.
	// Returns nil when nothing we own exists (already gone).
	Uninstall(ctx context.Context, cs kubernetes.Interface) error
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

// Management label applied to every resource created by the backend
// install flow. Uninstall lists and deletes by this label so we
// never touch resources put there by helm, kubectl apply, or
// another KubeBolt-like tool.
const (
	ManagedByLabel = "app.kubernetes.io/managed-by"
	ManagedByValue = "kubebolt"
)
