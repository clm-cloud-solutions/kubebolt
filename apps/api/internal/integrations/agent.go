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
// app.kubernetes.io/name=kubebolt-agent and its default namespace is
// kubebolt-system.
const (
	AgentID        = "agent"
	AgentName      = "kubebolt-agent"
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

// agentProvider implements Provider for the kubebolt-agent.
type agentProvider struct{}

// NewAgent returns the kubebolt-agent Provider. Stateless — safe to
// register once at startup.
func NewAgent() Provider { return &agentProvider{} }

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

	return meta, nil
}

// findAgentDaemonSet looks for the agent DaemonSet across the cluster.
// Preference order:
//  1. Labeled with the Helm convention (app.kubernetes.io/name=kubebolt-agent).
//  2. Labeled with the dev convention (app=kubebolt-agent).
//
// In both cases the standard namespace (kubebolt-system) is checked
// first — a dramatic short-circuit when the agent is installed the
// default way, which is almost always.
//
// Returns (nil, "", nil) when the agent isn't found anywhere. Errors
// indicate "couldn't ask" (API failure, permission denied on the
// apps group).
func findAgentDaemonSet(ctx context.Context, cs kubernetes.Interface) (*appsv1.DaemonSet, string, error) {
	type lookup struct {
		ns    string
		label string
	}
	// Most specific → most general. First hit wins.
	attempts := []lookup{
		{agentNamespace, agentLabelHelm},
		{agentNamespace, agentLabelDev},
		{metav1.NamespaceAll, agentLabelHelm},
		{metav1.NamespaceAll, agentLabelDev},
	}
	for _, at := range attempts {
		list, err := cs.AppsV1().DaemonSets(at.ns).List(ctx, metav1.ListOptions{LabelSelector: at.label})
		if err != nil {
			return nil, "", err
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
// The operator-tier RBAC check is best-effort: if listing
// ClusterRoles is denied, we report the proxy feature without the
// "operator" qualifier and the UI shows the conservative default.
func extractFeatures(ctx context.Context, cs kubernetes.Interface, ds *appsv1.DaemonSet) []FeatureFlag {
	envs := agentContainerEnv(ds)

	proxyEnabled := envBoolDefault(envs, envProxyEnabled, false)
	operatorRBAC := hasOperatorClusterRole(ctx, cs)

	proxyDescription := "SPDY tunneling lets the backend reach the cluster's apiserver " +
		"through the agent's outbound channel. Required for SaaS multi-cluster — when " +
		"enabled, pod terminal / file browser / port-forward / kubectl-style mutations " +
		"work via the agent. Default off; operators in single-cluster self-hosted " +
		"setups (backend has direct kubeconfig) leave it that way."
	if proxyEnabled && !operatorRBAC {
		proxyDescription += " ⚠️ Operator-tier RBAC NOT detected — the agent's SA only has " +
			"metrics-only ClusterRole, so dashboard reads via proxy will surface " +
			"\"No access\" for most resources. Apply " +
			"deploy/agent/kubebolt-agent-rbac-operator.yaml (or run " +
			"`make agent-rbac-operator`) to unlock full UI through the proxy."
	}

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
			Key:         "proxy",
			Label:       "K8s API proxy (SPDY tunneling)",
			Description: proxyDescription,
			Enabled:     proxyEnabled,
		},
		{
			Key:   "proxy-operator-rbac",
			Label: "Operator-tier RBAC for proxy",
			Description: "ClusterRole `kubebolt-agent-operator` granting wildcard " +
				"read+write to the agent's ServiceAccount. Required for full " +
				"UI access (terminal/files/portforward/restart/scale/delete) " +
				"through the proxy. Effectively cluster-admin for the agent " +
				"pod — opt-in by design (apply " +
				"`deploy/agent/kubebolt-agent-rbac-operator.yaml`).",
			Enabled:  operatorRBAC,
			Requires: []string{"proxy"},
		},
	}
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

