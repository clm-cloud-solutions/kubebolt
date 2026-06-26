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

// ingestTokenLister is the minimal slice of the ingest-token store the
// provider needs (ingest tokens live in their own store now, not inlined
// in the tenant record).
type ingestTokenLister interface {
	ListByTenant(ctx context.Context, tenantID string) ([]auth.IngestToken, error)
}

// currentClusterIDFn returns the kube-system namespace UID of the
// currently-active cluster (or "" when the active session reaches
// the apiserver via direct kubeconfig and we haven't resolved the
// UID). Used to filter ingest tokens scoped to other clusters out
// of this card's signal.
//
// Defined as a function rather than an interface so callers can
// pass a method-bound closure from the cluster manager without the
// manager having to satisfy a new interface — the dependency stays
// one-way (integrations → cluster, never the reverse).
type currentClusterIDFn func() string

// promSamplesProbeFn checks whether VictoriaMetrics holds any
// Prometheus-originated samples tagged with the given cluster UID.
// Returns (true, nil) when samples are present, (false, nil) when
// the cluster has no Prom data flowing, and (false, err) on
// transport errors — callers treat err as "couldn't tell" and fall
// back to the heartbeat-only logic, so the integration card never
// goes blank because of a transient VM hiccup.
//
// The probe is the truth signal that closes the gap left by the
// token-based heartbeat: a legacy unscoped token (ClusterID == "")
// passes the cluster-scope filter in every cluster, but only ONE
// cluster is actually receiving its samples. Without this check,
// the integration card would falsely report "Streaming" on every
// cluster the operator switches to. With it, the card only claims
// "Streaming" when VM confirms the data is actually here.
//
// Pass a nil promSampleProbe to disable this check entirely —
// the provider then falls back to the token heartbeat alone (the
// pre-check behaviour). Tests use nil; production wires the
// closure in main.go pointing at the same VM the metrics-query
// proxy uses.
type promSamplesProbeFn func(ctx context.Context, clusterID string) (bool, error)

// prometheusProvider implements Provider for the customer's
// existing Prometheus (Phase 3 remote_write receiver). Unlike the
// agent, Prometheus is not installed by KubeBolt and lives entirely
// outside our control surface — Detect therefore only reports
// *evidence of activity* (ingest token usage) rather than workload
// state. Stateless; safe to register once at startup.
type prometheusProvider struct {
	tenants         tenantsLister
	ingestTokens    ingestTokenLister
	currentCluster  currentClusterIDFn
	promSampleProbe promSamplesProbeFn
}

// NewPrometheus constructs the Prometheus integration provider.
// The TenantsStore is the source of truth for which bearer tokens
// have been issued and when each one was last used — together those
// form the heartbeat signal Detect reads.
//
// currentCluster is called on every Detect to resolve the active
// cluster's UID. Tokens whose ClusterID matches the active cluster
// — OR whose ClusterID is empty ("any cluster", the legacy default)
// — are considered relevant signal. Tokens scoped to other clusters
// are filtered out before tier / heartbeat counts so the card
// reflects only what's relevant to the cluster the operator is
// currently viewing.
//
// promSampleProbe is an optional VM probe that confirms Prom-
// originated samples are actually reaching this cluster's view.
// Without it, a legacy unscoped token surfaces a misleading
// "Streaming" on every cluster the operator visits. With it, the
// card downgrades to "no samples reaching this cluster" when VM
// confirms there's no data.
//
// Pass a nil currentCluster or promSampleProbe to disable each
// check independently. Tests use nils to exercise the heartbeat
// branches in isolation.
func NewPrometheus(
	tenants tenantsLister,
	ingestTokens ingestTokenLister,
	currentCluster currentClusterIDFn,
	promSampleProbe promSamplesProbeFn,
) Provider {
	if currentCluster == nil {
		currentCluster = func() string { return "" }
	}
	return &prometheusProvider{
		tenants:         tenants,
		ingestTokens:    ingestTokens,
		currentCluster:  currentCluster,
		promSampleProbe: promSampleProbe,
	}
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
	// Cluster-scope filter: a token's ClusterID must either match
	// the active cluster's UID or be empty (legacy unscoped). When
	// the provider has no clusterID resolution (e.g. tests injected
	// a function returning ""), the filter passes everything —
	// equivalent to the pre-5a.1 behaviour for tests built against
	// the older constructor signature.
	currentClusterID := p.currentCluster()
	now := time.Now()
	// Org-scope: ListTenants() has no ctx, so it returns EVERY org's tenant
	// (it can't be RLS-scoped). Counting all of them made a cluster-less org
	// see "Streaming" whenever ANY other org pushed Prometheus. Resolve the
	// caller's org from ctx and, in multi-tenant, only count ITS tokens. OSS /
	// default tenant keeps iterating the single tenant unchanged.
	callerOrg := auth.TenantIDFromContext(ctx)
	scoped := callerOrg != "" && callerOrg != auth.DefaultTenantName
	// Tenant-count drives source-label format below — when there's
	// only one tenant in scope (single-tenant self-hosted, or a scoped
	// per-org view) the "tenant_name/" prefix is pure noise and gets dropped.
	multiTenant := len(tenants) > 1 && !scoped
	var (
		mostRecent       time.Time
		mostRecentTenant string
		mostRecentTokLab string
		activeTokens     int
		freshSenders     int
	)
	for _, tenant := range tenants {
		if scoped && tenant.ID != callerOrg {
			continue // per-org detection: another org's senders don't count
		}
		toks, _ := p.ingestTokens.ListByTenant(ctx, tenant.ID)
		for i := range toks {
			tok := &toks[i]
			if !tok.Active(now) {
				continue
			}
			// Skip tokens scoped to a *different* cluster. Unscoped
			// tokens (ClusterID == "") still pass — legacy
			// backward-compat AND the explicit "match any cluster"
			// semantic the admin can pick at issue-time.
			if currentClusterID != "" && tok.ClusterID != "" && tok.ClusterID != currentClusterID {
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
			if mostRecentTokLab == "" || tok.LastUsedAt.After(mostRecent) {
				mostRecent = *tok.LastUsedAt
				mostRecentTenant = tenant.Name
				mostRecentTokLab = tok.Label
			}
			if now.Sub(*tok.LastUsedAt) < prometheusStaleWindow {
				freshSenders++
			}
		}
	}
	// hasHeartbeat tracks whether we found any used token in the
	// filtered set. Independent of the label string so the branching
	// below stays correct even if an admin issues a token with an
	// empty label (the store doesn't allow that today but defensive
	// is cheap).
	hasHeartbeat := mostRecentTokLab != "" || (mostRecentTenant != "" && !mostRecent.IsZero())
	// Compose the user-facing source label. Single-tenant gets the
	// bare token label (looks less like a K8s path, less ambiguous).
	// Multi-tenant keeps the prefix because the same token label can
	// exist under different tenants and the operator needs to know
	// which one is pushing.
	mostRecentLabel := mostRecentTokLab
	if multiTenant && mostRecentTenant != "" {
		mostRecentLabel = mostRecentTenant + "/" + mostRecentTokLab
	}

	hasWorkload := workload.Namespace != ""
	// Carry workload coords to the card regardless of status — the
	// operator wants to see ns/version even on degraded states.
	if hasWorkload {
		meta.Namespace = workload.Namespace
		meta.Version = workload.Version
	}

	// Sample-presence probe — closes the gap left by legacy
	// unscoped tokens. A token with ClusterID == "" passes the
	// cluster-scope filter in every cluster, but only the cluster
	// actually receiving its samples should claim "Streaming". The
	// probe asks VM whether Prom-originated samples exist for the
	// current cluster's UID; when the answer is "no", we override
	// the heartbeat narrative regardless of how fresh the token's
	// LastUsedAt looks.
	//
	// Skipped entirely (treated as confirmed) when there's no probe
	// wired (tests, OSS dev without VM), or when there's no
	// resolvable current cluster (the scope filter is already a
	// no-op in that case so nothing to validate).
	samplesConfirmed := true
	if p.promSampleProbe != nil && currentClusterID != "" && hasHeartbeat {
		ok, err := p.promSampleProbe(ctx, currentClusterID)
		if err == nil {
			samplesConfirmed = ok
		}
		// On error, leave samplesConfirmed = true so a transient VM
		// blip doesn't downgrade a working integration. The probe is
		// a positive signal — its absence shouldn't trump the
		// heartbeat we already have.
	}

	// Combination matrix — see Detect's package-level comment.
	switch {
	case hasWorkload && hasHeartbeat && !samplesConfirmed:
		// Legacy unscoped token matches in this cluster, but no
		// samples are actually reaching it. Usually means the token
		// was issued in a different cluster's session and we're
		// inheriting the heartbeat via the "any cluster" backward-
		// compat semantic. Tell the operator without claiming
		// "Streaming" we can't substantiate.
		meta.Status = StatusDegraded
		meta.Health = &Health{
			PodsReady:   workload.PodsReady,
			PodsDesired: workload.PodsDesired,
			Message:     "Prometheus running in cluster and an ingest token is active, but no samples are reaching this cluster's view. Verify the token's cluster scope and that remote_write points at this KubeBolt instance.",
		}

	case !hasWorkload && hasHeartbeat && !samplesConfirmed:
		// No local workload, no samples for this cluster either —
		// the token activity belongs to a different cluster's view.
		// Card collapses to "not installed here" with a hint.
		meta.Status = StatusNotInstalled
		meta.Health = &Health{
			Message: "An ingest token is active under this tenant but no Prometheus samples are reaching this cluster's view. The token may be scoped to (or used by) a different cluster.",
		}

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
			Message:     formatStreamMessage(age, mostRecentLabel, freshSenders, true /* hasWorkload */),
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
			meta.Health = &Health{Message: fmt.Sprintf("Streaming from external source · last sample %s ago from %s%s", humanDuration(age), mostRecentLabel, formatSendersSuffix(freshSenders, false))}
		case age < prometheusStaleWindow:
			meta.Status = StatusInstalled
			meta.Health = &Health{Message: fmt.Sprintf("Stale · last sample %s ago from %s%s", humanDuration(age), mostRecentLabel, formatSendersSuffix(freshSenders, false))}
		default:
			meta.Status = StatusDegraded
			meta.Health = &Health{Message: fmt.Sprintf("Cold · last sample %s ago from %s — has Prom stopped pushing?%s", humanDuration(age), mostRecentLabel, formatSendersSuffix(freshSenders, false))}
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
//
// hasWorkload toggles the "additional senders" label — when an
// in-cluster Prom is detected, surplus senders are framed as
// "additional" (implying "beyond the local workload"). In the
// cross-cluster branch (!hasWorkload), every sender is "active"
// because there's no local point of reference.
func formatStreamMessage(age time.Duration, sourceLabel string, freshSenders int, hasWorkload bool) string {
	switch {
	case age < prometheusFreshWindow:
		return fmt.Sprintf("Streaming · last sample %s ago from %s%s", humanDuration(age), sourceLabel, formatSendersSuffix(freshSenders, hasWorkload))
	case age < prometheusStaleWindow:
		return fmt.Sprintf("Stale · last sample %s ago from %s%s", humanDuration(age), sourceLabel, formatSendersSuffix(freshSenders, hasWorkload))
	default:
		return fmt.Sprintf("Cold · last sample %s ago from %s — has Prom stopped pushing?%s", humanDuration(age), sourceLabel, formatSendersSuffix(freshSenders, hasWorkload))
	}
}

// formatSendersSuffix renders the "· N <kind> senders" suffix only
// when there are 2+ to count. A single sender is implied by the
// "from <label>" portion of the message — adding "· 1 active sender"
// would be redundant noise. The label kind switches on
// hasWorkload: "additional" when a local workload is the implicit
// first sender, "active" otherwise (cross-cluster path).
func formatSendersSuffix(freshSenders int, hasWorkload bool) string {
	if freshSenders <= 1 {
		return ""
	}
	if hasWorkload {
		// Subtract 1 to express "additional beyond the local
		// workload" — the local Prom counts as the first sender,
		// so 2 freshSenders means 1 additional, not 2.
		return fmt.Sprintf(" · %d additional sender%s", freshSenders-1, plural(freshSenders-1))
	}
	return fmt.Sprintf(" · %d active senders", freshSenders)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
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
