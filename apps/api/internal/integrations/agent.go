package integrations

import (
	"context"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Agent IDs and detection knobs. Chosen to match what the Helm chart
// produces: the agent's DaemonSet is labeled
// app.kubernetes.io/name=kubebolt-agent and its default install
// namespace is `kubebolt` (1.13+ convention; older installs used
// `kubebolt-system` and we still probe it as a fallback).
const (
	AgentID   = "agent"
	AgentName = "kubebolt-agent"

	// agentNamespace is the legacy default install namespace. Kept for
	// compatibility with installs that predate the 1.13 convention shift
	// — install/uninstall paths still target it. New detection-side
	// callers should iterate agentPreferredNamespaces instead.
	agentNamespace = "kubebolt-system"

	// Labels the chart and dev manifest attach to the DaemonSet. We
	// try both so detection works for both the Helm install and the
	// legacy dev manifest without forcing operators to migrate.
	agentLabelHelm = "app.kubernetes.io/name=kubebolt-agent"
	agentLabelDev  = "app=kubebolt-agent"

	// Env var keys the agent reads — mirrored here so the feature
	// flags we report back match what the agent actually consults.
	envHubbleEnabled = "KUBEBOLT_HUBBLE_ENABLED"
	envProxyEnabled  = "KUBEBOLT_AGENT_PROXY_ENABLED"

	// Capabilities the agent advertises to the backend on Hello.
	// "kube-proxy" gates SPDY tunneling for exec/portforward/files.
	capabilityKubeProxy = "kube-proxy"

	// Operator-tier RBAC ClusterRole that, when bound to the agent's
	// SA, unlocks full read+write proxy access (mutating ops via
	// agent-proxy: restart/scale/delete/exec/portforward). Detected
	// for UI display so operators see whether they applied
	// kubebolt-agent-rbac-operator.yaml or not.
	operatorClusterRole = "kubebolt-agent-operator"
)

// agentPreferredNamespaces is the lookup order findAgentDaemonSet
// tries before falling back to a cluster-wide list. Order matters —
// the first hit short-circuits.
//
//   - `kubebolt`        1.13+ default (our docs + chart NOTES use it
//                       everywhere)
//   - `kubebolt-agent`  natural ns name when an operator follows the
//                       chart's release-name convention
//                       (`helm install kubebolt-agent ... -n kubebolt-agent`)
//   - `kubebolt-system` legacy default — yagan-prod / sucal-uat
//                       installs predate the 1.13 convention shift
//
// Operators who install in a custom ns (monitoring, observability,
// kb-prod, anything else) are caught by the cluster-wide fallback
// findAgentDaemonSet runs after this list — they're NOT forced into
// one of these names. The list exists purely to short-circuit the
// 95% case and avoid an unnecessary cluster-wide list when the
// agent is somewhere conventional.
//
// Edge case the cluster-wide fallback still doesn't cover: a backend
// whose ServiceAccount is RBAC-restricted to namespace-scoped
// listing (no `list daemonsets` at cluster scope). If such a
// deployment also installs the agent in a custom ns, detection
// fails. That combination is rare in OSS and self-hosted-self
// installs; for SaaS multi-tenant with strict scoping, the agent's
// in-cluster ns would be one of the few the backend SA is granted,
// which by convention is one of the above names anyway.
var agentPreferredNamespaces = []string{"kubebolt", "kubebolt-agent", "kubebolt-system"}

// agentSamplesProbeFn checks whether THIS backend's VictoriaMetrics
// holds at least one `kubebolt_agent_info` sample for the given
// cluster_id — i.e. the agent installed in that cluster is actually
// shipping to THIS backend (not a different one). Returns
// (true, nil) when samples are present, (false, nil) when absent,
// (false, err) on transport errors — callers treat err as "couldn't
// tell" and fall back to DaemonSet-presence-only logic so a transient
// VM blip doesn't downgrade a working integration.
//
// Symmetric to promSamplesProbeFn / promreadActiveProbeFn — same
// shape, different metric. See session 11-A v3 finding A
// (project_agent_cross_backend_false_positive) for the operator-
// facing symptom this probe addresses: an operator's LOCAL kubebolt
// that has another cluster's kubeconfig context can SEE the agent
// DaemonSet via cs.AppsV1().DaemonSets().List(), but that agent is
// configured to ship to a DIFFERENT backend (e.g. the SaaS) — without
// the probe the card falsely reads "Installed" even though no agent
// data is reaching THIS backend's VM.
type agentSamplesProbeFn func(ctx context.Context, clusterID string) (bool, error)

// agentProvider implements Provider for the kubebolt-agent.
type agentProvider struct {
	currentCluster currentClusterIDFn
	samplesProbe   agentSamplesProbeFn
}

// NewAgent returns the kubebolt-agent Provider. Stateless — safe to
// register once at startup.
//
// currentCluster is called on every Detect to resolve the active
// cluster's UID. Combined with samplesProbe it closes the
// "agent visible in the cluster but shipping samples elsewhere"
// false-positive that surfaces in topologies where an operator's
// kubeconfig points at clusters whose agent is configured for a
// DIFFERENT backend (very common in multi-backend SaaS / dev setups).
//
// Pass nil currentCluster or nil samplesProbe to disable the probe
// path independently — the provider then falls back to the legacy
// DaemonSet-presence-only detection (the pre-Fix #12 behavior).
// Tests use nils to exercise the legacy branches in isolation.
func NewAgent(currentCluster currentClusterIDFn, samplesProbe agentSamplesProbeFn) Provider {
	if currentCluster == nil {
		currentCluster = func() string { return "" }
	}
	return &agentProvider{
		currentCluster: currentCluster,
		samplesProbe:   samplesProbe,
	}
}

func (a *agentProvider) Meta() Integration {
	return Integration{
		ID:          AgentID,
		Name:        "KubeBolt Agent",
		Description: "Per-node DaemonSet that ships kubelet stats, cAdvisor metrics, and Cilium Hubble flows to KubeBolt. Optionally acts as the cluster's K8s API gateway for SaaS multi-cluster — exec / port-forward / files routed through the agent's outbound channel when proxy is enabled.",
		DocsURL:     "https://github.com/clm-cloud-solutions/kubebolt/tree/main/deploy/helm/kubebolt-agent",
		Capabilities: []string{
			"metrics.historical",
			"metrics.network",
			"flows",      // L4 + L7 HTTP via Hubble when Cilium is present
			"kube-proxy", // SPDY tunneling for exec/portforward/files (Sprint A.5)
		},
	}
}

func (a *agentProvider) Detect(ctx context.Context, cs kubernetes.Interface) (Integration, error) {
	meta := a.Meta()

	ds, ns, err := findAgentDaemonSet(ctx, cs)
	if err != nil {
		// Real error (RBAC, API unavailable) — surface as Unknown so
		// the UI can distinguish "couldn't tell" from "not there".
		meta.Status = StatusUnknown
		meta.Health = &Health{Message: fmt.Sprintf("detection failed: %v", err)}
		return meta, nil
	}
	if ds == nil {
		meta.Status = StatusNotInstalled
		return meta, nil
	}

	meta.Namespace = ns
	meta.Version = extractImageVersion(ds)
	meta.Features = extractFeatures(ctx, cs, ds)
	// Managed = "we installed this and still own it". Anything
	// lacking the label came from Helm, kubectl apply, or an
	// external operator — KubeBolt leaves it alone.
	meta.Managed = ds.Labels[ManagedByLabel] == ManagedByValue

	ready := int(ds.Status.NumberReady)
	desired := int(ds.Status.DesiredNumberScheduled)
	meta.Health = &Health{PodsReady: ready, PodsDesired: desired}

	switch {
	case desired == 0:
		// DaemonSet exists but matches no nodes yet (e.g. nodeSelector
		// filters everything). Technically installed, operationally
		// useless — flag as degraded.
		meta.Status = StatusDegraded
		meta.Health.Message = "DaemonSet scheduled on zero nodes"
	case ready < desired:
		meta.Status = StatusDegraded
		meta.Health.Message = fmt.Sprintf("%d of %d pods ready", ready, desired)
	default:
		meta.Status = StatusInstalled
	}

	// Sample-presence cross-check: the DaemonSet exists in the cluster
	// AND its pods are ready, but is it actually shipping samples to
	// THIS backend? In multi-backend topologies (operator's kubeconfig
	// points at a cluster whose agent is configured for a different
	// KUBEBOLT_BACKEND_URL — common in SaaS dev setups), the legacy
	// DaemonSet-presence-only logic would falsely report "Installed"
	// because cs.AppsV1().DaemonSets().List() sees the DS via
	// kubeconfig, with zero info about where the agent's gRPC stream
	// actually terminates. The probe asks VM "do you have
	// kubebolt_agent_info samples tagged with this cluster's UID?" —
	// if no, the agent ships to a DIFFERENT backend.
	//
	// Skipped (treated as confirmed) when there's no probe wired
	// (tests, OSS dev without VM) or when there's no resolvable
	// current cluster (probe can't form a safe scoped query).
	// Discovered session 11-A v3 — see project_agent_cross_backend_false_positive.
	if meta.Status == StatusInstalled && a.samplesProbe != nil {
		clusterID := a.currentCluster()
		if clusterID != "" {
			arrives, probeErr := a.samplesProbe(ctx, clusterID)
			if probeErr == nil && !arrives {
				meta.Status = StatusDegraded
				meta.Health.Message = "DaemonSet present in cluster but no agent samples reaching this backend — the agent is likely shipping to a different KUBEBOLT_BACKEND_URL"
			}
			// probeErr != nil: transient VM blip, keep StatusInstalled.
			// The probe is a positive signal — its absence shouldn't
			// trump the DaemonSet evidence we already have.
		}
	}

	return meta, nil
}

// findAgentDaemonSet looks for the agent DaemonSet across the cluster.
// Preference order:
//  1. agentPreferredNamespaces × {helm, dev label} — fast path when
//     the agent is installed at the conventional location.
//  2. Cluster-wide (NamespaceAll) × {helm, dev label} — covers
//     non-standard installs and the partial-RBAC case where the
//     namespace-scoped list 403s but cluster-wide works.
//
// Returns (nil, "", nil) when the agent isn't found anywhere.
// Errors indicate "couldn't ask" — but we only surface an error if
// EVERY attempt failed. Transient errors on the first attempts
// (tunnel hiccup, RBAC-denied on a specific namespace, etc.) used to
// abort the whole probe; now they're skipped and the next attempt
// runs. See session 11-A `project_chart_agentingest_nodeport_ignored`
// fellow finding for the operator-facing symptom: agent card stuck
// in UNKNOWN status with a transport error message because the very
// first list call failed and the cluster-wide fallback never ran.
func findAgentDaemonSet(ctx context.Context, cs kubernetes.Interface) (*appsv1.DaemonSet, string, error) {
	type lookup struct {
		ns    string
		label string
	}
	// Build the lookup grid: preferred namespaces × labels, then
	// cluster-wide × labels as last-resort fallbacks.
	labels := []string{agentLabelHelm, agentLabelDev}
	attempts := make([]lookup, 0, (len(agentPreferredNamespaces)+1)*len(labels))
	for _, ns := range agentPreferredNamespaces {
		for _, lab := range labels {
			attempts = append(attempts, lookup{ns, lab})
		}
	}
	for _, lab := range labels {
		attempts = append(attempts, lookup{metav1.NamespaceAll, lab})
	}

	var lastErr error
	for _, at := range attempts {
		list, err := cs.AppsV1().DaemonSets(at.ns).List(ctx, metav1.ListOptions{LabelSelector: at.label})
		if err != nil {
			lastErr = err
			continue
		}
		if len(list.Items) == 0 {
			continue
		}
		// First match. If multiple (shouldn't happen in practice),
		// the order is whatever the apiserver returned; stable
		// enough for UI needs.
		ds := list.Items[0]
		return &ds, ds.Namespace, nil
	}
	// All attempts exhausted. If at least one returned a transport /
	// permission error, surface it so the UI shows "detection failed"
	// (StatusUnknown) instead of "not installed" — there's a real
	// difference between "we couldn't ask" and "we asked and got no".
	if lastErr != nil {
		return nil, "", lastErr
	}
	return nil, "", nil
}

// extractImageVersion pulls a human-readable version from the agent
// container's image reference. The Helm chart sets image tags like
// "1.2.3"; the dev manifest uses "dev". We just return whatever sits
// after the last ":" — anything more structured belongs in the
// release pipeline, not detection.
func extractImageVersion(ds *appsv1.DaemonSet) string {
	for _, c := range ds.Spec.Template.Spec.Containers {
		if c.Name != "agent" {
			continue
		}
		if idx := strings.LastIndex(c.Image, ":"); idx != -1 {
			return c.Image[idx+1:]
		}
		return c.Image
	}
	return ""
}

// extractFeatures reads the agent container's env (and the cluster's
// RBAC) to surface the observed feature-flag states. We only report
// flags the UI knows how to present — the agent has other env vars
// (log level, backend URL) that aren't user-facing toggles.
//
// Two surfaced flags:
//   - hubble:           binary on/off
//   - permission-tier:  multi-state (metrics | reader | operator),
//                       reads `Value` instead of `Enabled` for the
//                       displayed label. Enabled is set true for
//                       reader/operator (i.e. "anything beyond
//                       the privacy-conscious default") so the UI's
//                       green-pill heuristic still works.
//
// The mode lookup is best-effort: if listing ClusterRoles is denied,
// detectInstalledRBACMode falls back to "metrics" (the conservative
// default).
func extractFeatures(ctx context.Context, cs kubernetes.Interface, ds *appsv1.DaemonSet) []FeatureFlag {
	envs := agentContainerEnv(ds)

	mode := detectInstalledRBACMode(ctx, cs)
	proxyEnabled := envBoolDefault(envs, envProxyEnabled, false)

	tierLabel, tierDesc := tierDisplay(mode, proxyEnabled)

	return []FeatureFlag{
		{
			Key:   "hubble",
			Label: "Hubble flow collector",
			Description: "Streams L4 + L7 HTTP + DNS flows from Cilium's Hubble " +
				"Relay so the Traffic layout shows live pod-to-pod edges. " +
				"Silent no-op when Cilium isn't installed.",
			Enabled:  envBoolDefault(envs, envHubbleEnabled, true),
			Requires: []string{"cilium"},
		},
		{
			Key:         "permission-tier",
			Label:       "Permission tier",
			Description: tierDesc,
			Enabled:     mode != RBACModeMetrics,
			Value:       tierLabel,
		},
	}
}

// tierDisplay maps the (mode, proxy) pair to a human label + a
// descriptive paragraph for the integration detail panel. Surfaces
// drift (proxy off in reader/operator mode, etc.) inline so a
// misconfigured install is visible without the operator having to
// inspect env vars.
func tierDisplay(mode AgentRBACMode, proxyEnabled bool) (label, description string) {
	switch mode {
	case RBACModeMetrics:
		label = "Metrics only"
		description = "Narrow ClusterRole — kubelet stats + pods list/watch + " +
			"namespaces. The agent ships kubelet metrics + Hubble flows; nothing " +
			"else leaves the cluster. Privacy-conscious default; switch to reader " +
			"or operator if you want the dashboard to render inventory through this " +
			"agent's tunnel."
	case RBACModeReader:
		label = "Cluster-wide read"
		description = "Cluster-wide get/list/watch on `*/*`. Backend reads inventory, " +
			"YAML, describe output, and pod logs through the agent's SPDY tunnel. " +
			"Mutations come back 403 — switch to operator if you need exec / scale / " +
			"restart / delete / YAML edit through the dashboard."
		if !proxyEnabled {
			description += " ⚠️ Proxy disabled in env — reader RBAC is wasted without " +
				"the proxy capability. Re-run install/configure to fix."
		}
	case RBACModeOperator:
		label = "Cluster-wide read + write"
		description = "Wildcard read+write on `*/*` — effectively cluster-admin scoped " +
			"to the agent's ServiceAccount. Full UI parity through the dashboard: " +
			"exec, scale, restart, delete, YAML edit, file write. Auth on the " +
			"backend's gRPC channel is the only thing keeping a network attacker " +
			"from pivoting to admin in this cluster."
		if !proxyEnabled {
			description += " ⚠️ Proxy disabled in env — operator RBAC is wasted without " +
				"the proxy capability. Re-run install/configure to fix."
		}
	default:
		label = string(mode)
		description = "Unknown RBAC mode — the agent's ClusterRole set doesn't match " +
			"any of the recognized tiers. Re-run install/configure to recover."
	}
	return label, description
}

// hasOperatorClusterRole returns true when the operator-tier
// ClusterRole exists in the cluster. It does NOT verify the
// ClusterRoleBinding — the manifest ships both as a unit, and
// checking just the ClusterRole keeps the call cheap. RBAC denied →
// returns false (we couldn't tell, conservative default).
func hasOperatorClusterRole(ctx context.Context, cs kubernetes.Interface) bool {
	_, err := cs.RbacV1().ClusterRoles().Get(ctx, operatorClusterRole, metav1.GetOptions{})
	return err == nil
}

// agentContainerEnv returns the env vars declared on the "agent"
// container as a map. fieldRef-sourced vars are skipped — we only
// care about literal values for feature detection.
func agentContainerEnv(ds *appsv1.DaemonSet) map[string]string {
	out := map[string]string{}
	for _, c := range ds.Spec.Template.Spec.Containers {
		if c.Name != "agent" {
			continue
		}
		for _, e := range c.Env {
			if e.ValueFrom != nil {
				continue
			}
			out[e.Name] = e.Value
		}
		return out
	}
	return out
}

// envBoolDefault parses an env value the same way the agent itself
// does (see packages/agent/cmd/agent/main.go's envBool). Keep this
// in sync if the agent's parsing ever gets stricter or looser.
func envBoolDefault(envs map[string]string, key string, fallback bool) bool {
	v, ok := envs[key]
	if !ok || v == "" {
		return fallback
	}
	switch strings.ToLower(v) {
	case "1", "t", "true", "y", "yes", "on":
		return true
	case "0", "f", "false", "n", "no", "off":
		return false
	default:
		return fallback
	}
}

