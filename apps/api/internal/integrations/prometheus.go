package integrations

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
)

// Prometheus identity. Detection blends two signals:
//
//   1. (PRIMARY) Workload presence in the active cluster — pods
//      labeled `app.kubernetes.io/name=prometheus`. The vast
//      majority of installs put Prom in the same cluster they
//      monitor (kube-prometheus-stack default), so this is the
//      right signal for the 80% case.
//
//   2. (SECONDARY) Heartbeat via ingest token usage — for the
//      advanced case where Prom lives in a different cluster
//      (federation, AMP/Azure managed Prom pushing into KubeBolt
//      from outside).
//
// The combination matrix lives in Detect's comments.
const (
	PrometheusID   = "prometheus"
	PrometheusName = "Prometheus"

	// promLabelSelector matches every kube-prometheus-stack release
	// and the standalone prometheus-community chart — they all set
	// the standard `app.kubernetes.io/name=prometheus` label.
	// Stateful-set / Deployment-based installs are both covered
	// because we list at the pod level.
	promLabelSelector = "app.kubernetes.io/name=prometheus"

	// promVersionLabel is the standardized label kube-prometheus
	// charts (and most others following the K8s recommended labels)
	// set on the workload. When present it carries the Prom version
	// without an image-tag parse step.
	promVersionLabel = "app.kubernetes.io/version"

	// Heartbeat tiers — how long since the last accepted sample
	// recorded against any active ingest token. Widths chosen to
	// match typical Prom scrape intervals (15-60s) plus a generous
	// operator-reaction grace period before flagging cold.
	prometheusFreshWindow = 1 * time.Minute   // streaming
	prometheusStaleWindow = 15 * time.Minute  // stale
	// > stale → cold (StatusDegraded)
)

// tenantsLister is the minimal slice of auth.TenantsStore the
// provider needs. Defining it locally lets tests inject a fake
// without spinning up BoltDB.
type tenantsLister interface {
	ListTenants() ([]auth.Tenant, error)
}

// prometheusProvider implements Provider for the customer's
// existing Prometheus (Phase 3 remote_write receiver). Unlike the
// agent, Prometheus is not installed by KubeBolt and lives entirely
// outside our control surface — Detect therefore only reports
// *evidence of activity* (ingest token usage) rather than workload
// state. Stateless; safe to register once at startup.
type prometheusProvider struct {
	tenants tenantsLister
}

// NewPrometheus constructs the Prometheus integration provider.
// The TenantsStore is the source of truth for which bearer tokens
// have been issued and when each one was last used — together those
// form the heartbeat signal Detect reads.
func NewPrometheus(tenants tenantsLister) Provider {
	return &prometheusProvider{tenants: tenants}
}

func (p *prometheusProvider) Meta() Integration {
	return Integration{
		ID:          PrometheusID,
		Name:        PrometheusName,
		Description: "External Prometheus pushing samples to KubeBolt via remote_write. Detected automatically from ingest-token usage — per-tenant bearer auth + rate limit + cardinality cap gate every batch.",
		DocsURL:     "https://github.com/clm-cloud-solutions/kubebolt/blob/main/docs/integrations/prometheus.md",
		Capabilities: []string{
			"metrics.scraped",
			"metrics.historical",
		},
	}
}

// promWorkload captures the slim slice of in-cluster Prom state
// the card renders. Empty Namespace signals "no workload found"
// (RBAC denied or genuinely absent), distinct from "found 0 pods"
// which would also produce a non-empty Namespace if the StatefulSet
// scaled to 0 — we don't surface that distinction in the card
// because the operator-actionable next step is the same.
type promWorkload struct {
	Namespace   string
	PodsReady   int
	PodsDesired int
	Version     string
}

func (p *prometheusProvider) Detect(ctx context.Context, cs kubernetes.Interface) (Integration, error) {
	meta := p.Meta()

	tenants, err := p.tenants.ListTenants()
	if err != nil {
		// Can't read the tokens store — distinct from "no traffic"
		// (which is NotInstalled). The UI renders Unknown with the
		// reason so the operator can fix the cause (permissions,
		// boltdb corruption) rather than assume the integration is
		// silently broken.
		meta.Status = StatusUnknown
		meta.Health = &Health{Message: fmt.Sprintf("could not list tenants: %v", err)}
		return meta, nil
	}

	// Signal A — in-cluster Prom workload (PRIMARY).
	workload := detectPromWorkload(ctx, cs)

	// Signal B — heartbeat via ingest token usage (SECONDARY).
	now := time.Now()
	var (
		mostRecent      time.Time
		mostRecentLabel string
		activeTokens    int
		freshSenders    int
	)
	for _, tenant := range tenants {
		for i := range tenant.IngestTokens {
			tok := &tenant.IngestTokens[i]
			if !tok.Active(now) {
				continue
			}
			activeTokens++
			if tok.LastUsedAt == nil {
				continue
			}
			// Two trackers, distinct windows. mostRecent never filters
			// by freshness — it's what drives the status tier
			// (Streaming/Stale/Cold needs to see "all the way back" to
			// detect a Cold token. freshSenders only counts tokens
			// within the stale window so the "active senders" suffix
			// reflects who's pushing now, not historical activity.
			if mostRecentLabel == "" || tok.LastUsedAt.After(mostRecent) {
				mostRecent = *tok.LastUsedAt
				mostRecentLabel = tenant.Name + "/" + tok.Label
			}
			if now.Sub(*tok.LastUsedAt) < prometheusStaleWindow {
				freshSenders++
			}
		}
	}

	hasWorkload := workload.Namespace != ""
	hasHeartbeat := mostRecentLabel != ""

	// Carry workload coords to the card regardless of status — the
	// operator wants to see ns/version even on degraded states.
	if hasWorkload {
		meta.Namespace = workload.Namespace
		meta.Version = workload.Version
	}

	// Combination matrix — see Detect's package-level comment.
	switch {
	case hasWorkload && hasHeartbeat:
		// Common case: Prom in this cluster, configured to push to
		// KubeBolt. Status tier blends pod health + heartbeat
		// freshness; the worst of the two wins so a healthy pod
		// count doesn't paper over a Cold stream.
		age := now.Sub(mostRecent)
		podHealthy := workload.PodsReady > 0 && workload.PodsReady == workload.PodsDesired
		streamHealthy := age < prometheusStaleWindow
		switch {
		case podHealthy && streamHealthy:
			meta.Status = StatusInstalled
		default:
			meta.Status = StatusDegraded
		}
		meta.Health = &Health{
			PodsReady:   workload.PodsReady,
			PodsDesired: workload.PodsDesired,
			Message:     formatStreamMessage(age, mostRecentLabel, freshSenders),
		}

	case hasWorkload && !hasHeartbeat:
		// Prom is here but nobody's pushing — typical for a fresh
		// install that hasn't been wired to KubeBolt yet. The
		// actionable next step is in the message.
		meta.Status = StatusDegraded
		meta.Health = &Health{
			PodsReady:   workload.PodsReady,
			PodsDesired: workload.PodsDesired,
			Message:     "Prometheus running in cluster but not configured for KubeBolt. Add a remote_write block pointing at /api/v1/prom/write.",
		}

	case !hasWorkload && hasHeartbeat:
		// Advanced case: no Prom workload visible in this cluster
		// but something is pushing samples to our receiver. Could be
		// Prom federation, AMP, or just RBAC denying us the pod
		// list. The heartbeat is the truth-signal here — go by it.
		age := now.Sub(mostRecent)
		switch {
		case age < prometheusFreshWindow:
			meta.Status = StatusInstalled
			meta.Health = &Health{Message: fmt.Sprintf("Streaming from external source · last sample %s ago from %s%s", humanDuration(age), mostRecentLabel, formatSendersSuffix(freshSenders))}
		case age < prometheusStaleWindow:
			meta.Status = StatusInstalled
			meta.Health = &Health{Message: fmt.Sprintf("Stale · last sample %s ago from %s%s", humanDuration(age), mostRecentLabel, formatSendersSuffix(freshSenders))}
		default:
			meta.Status = StatusDegraded
			meta.Health = &Health{Message: fmt.Sprintf("Cold · last sample %s ago from %s — has Prom stopped pushing?%s", humanDuration(age), mostRecentLabel, formatSendersSuffix(freshSenders))}
		}

	default:
		// Neither workload nor heartbeat — Prometheus simply isn't
		// involved with this cluster. The disambiguating clue is
		// whether the operator has at least issued a token already
		// (intent to configure) vs nothing at all (greenfield).
		meta.Status = StatusNotInstalled
		switch {
		case activeTokens == 0:
			meta.Health = &Health{Message: "No Prometheus detected in cluster. Install one (kube-prometheus-stack is the most common path) or point an existing remote_write at /api/v1/prom/write."}
		default:
			meta.Health = &Health{Message: fmt.Sprintf("%d ingest token(s) issued but no Prometheus pushing yet. Configure remote_write with the bearer token to populate this card.", activeTokens)}
		}
	}

	return meta, nil
}

// formatStreamMessage builds the Health.Message for the
// hasWorkload+hasHeartbeat case. Separated to keep Detect's
// branches at one purpose each.
func formatStreamMessage(age time.Duration, sourceLabel string, freshSenders int) string {
	switch {
	case age < prometheusFreshWindow:
		return fmt.Sprintf("Streaming · last sample %s ago from %s%s", humanDuration(age), sourceLabel, formatSendersSuffix(freshSenders))
	case age < prometheusStaleWindow:
		return fmt.Sprintf("Stale · last sample %s ago from %s%s", humanDuration(age), sourceLabel, formatSendersSuffix(freshSenders))
	default:
		return fmt.Sprintf("Cold · last sample %s ago from %s — has Prom stopped pushing?%s", humanDuration(age), sourceLabel, formatSendersSuffix(freshSenders))
	}
}

// formatSendersSuffix renders the "· N active senders" suffix only
// when there are 2+ to count. A single sender is implied by the
// "from <label>" portion of the message — adding "· 1 active sender"
// would be redundant noise.
func formatSendersSuffix(freshSenders int) string {
	if freshSenders > 1 {
		return fmt.Sprintf(" · %d active senders", freshSenders)
	}
	return ""
}

// detectPromWorkload scans the cluster for pods that look like
// Prometheus servers. Returns the empty struct on any error
// (RBAC denied, API unavailable, no cluster) — callers treat that
// as "no workload found" and fall through to the heartbeat-only
// branch. Errors are intentionally swallowed: the alternative is
// returning StatusUnknown for half the matrix every time pod-list
// fails, which makes the integration card useless in any cluster
// where KubeBolt has restricted RBAC.
//
// nil clientset is also tolerated (returns empty) so unit tests
// can drive Detect without standing up a fake clientset just to
// exercise the heartbeat branches.
func detectPromWorkload(ctx context.Context, cs kubernetes.Interface) promWorkload {
	if cs == nil {
		return promWorkload{}
	}
	pods, err := cs.CoreV1().Pods("").List(ctx, metav1.ListOptions{
		LabelSelector: promLabelSelector,
		Limit:         32, // Cap the list — we only need to know "yes, present" + first pod's labels.
	})
	if err != nil || len(pods.Items) == 0 {
		return promWorkload{}
	}

	// All matched pods should share the same Prom install (single
	// kube-prometheus-stack release). When operators run multiple
	// releases, we take the first — same convention the agent
	// provider uses for its DaemonSet lookup.
	first := pods.Items[0]
	result := promWorkload{
		Namespace: first.Namespace,
		Version:   first.Labels[promVersionLabel],
	}

	// Count ready vs desired in this namespace. Counting across all
	// matched pods (not just first.Namespace) would mix releases
	// from different namespaces, which would misrepresent the ratio
	// for the card.
	for _, pod := range pods.Items {
		if pod.Namespace != result.Namespace {
			continue
		}
		result.PodsDesired++
		if isPodReady(&pod) {
			result.PodsReady++
		}
	}
	return result
}

// isPodReady returns true when the pod has a PodReady condition
// set to True. Mirrors the convention used by deployments / DaemonSets
// for the "ready" tally — purely about whether traffic should reach
// the pod, not about phase=Running which includes pods still failing
// readiness probes.
func isPodReady(pod *corev1.Pod) bool {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}

// humanDuration renders a time.Duration in the compact form the
// Integration card consumes (12s, 5m, 2h, 3d). Tuned for the typical
// scale of "last sample" displays — sub-minute in seconds, sub-hour
// in minutes, sub-day in hours, then days.
func humanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
