package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// GetConfig reads the current agent DaemonSet and reconstructs the
// AgentInstallConfig a configure form would need to show. All
// user-visible fields are surfaced; fields the UI can't edit
// (downward API refs, securityContext) are intentionally dropped so
// Configure stays confined to the same surface Install uses.
//
// Returns NotInstalledError when no agent is present, and
// NotManagedError when one exists but wasn't installed by KubeBolt
// — matching the Configure symmetry.
func (a *agentProvider) GetConfig(ctx context.Context, cs kubernetes.Interface) (json.RawMessage, error) {
	ds, ns, err := findAgentDaemonSet(ctx, cs)
	if err != nil {
		return nil, fmt.Errorf("locating agent: %w", err)
	}
	if ds == nil {
		return nil, &NotInstalledError{IntegrationID: AgentID}
	}
	if !managedByUs(ds.Labels) {
		return nil, &NotManagedError{Kind: "DaemonSet", Namespace: ns, Name: ds.Name}
	}

	cfg := extractConfigFromDaemonSet(ds, ns)
	// ProxyOperatorRBAC isn't on the DaemonSet — it's a ClusterRole
	// living outside the namespace. Best-effort lookup so the
	// configure dialog can render the correct toggle state. Errors
	// (RBAC denied) leave the field nil → UI shows the conservative
	// default (off).
	if hasOperatorClusterRole(ctx, cs) {
		t := true
		cfg.ProxyOperatorRBAC = &t
	}
	return json.Marshal(cfg)
}

// Configure applies a new config to the existing managed install.
// Uses the same ensure* helpers as Install so the update path is
// literally the same code — the only difference is the preflight
// check that demands a managed install is already present.
func (a *agentProvider) Configure(ctx context.Context, cs kubernetes.Interface, configJSON json.RawMessage) error {
	ds, ns, err := findAgentDaemonSet(ctx, cs)
	if err != nil {
		return fmt.Errorf("locating agent: %w", err)
	}
	if ds == nil {
		return &NotInstalledError{IntegrationID: AgentID}
	}
	if !managedByUs(ds.Labels) {
		return &NotManagedError{Kind: "DaemonSet", Namespace: ns, Name: ds.Name}
	}

	var cfg AgentInstallConfig
	if len(configJSON) > 0 {
		if err := json.Unmarshal(configJSON, &cfg); err != nil {
			return fmt.Errorf("invalid config: %w", err)
		}
	}

	// Pin the namespace to the one the agent currently lives in —
	// Configure never moves the workload. If the operator wants a
	// different namespace they have to uninstall + reinstall.
	cfg.Namespace = ns

	// Surface what landed on the wire so operators can correlate
	// a "values didn't persist" report with the actual payload — the
	// most common culprit historically has been UI-side caching, not
	// the apply path.
	slog.Info("agent integration: Configure received",
		slog.String("namespace", ns),
		slog.String("backendUrl", cfg.BackendURL),
		slog.String("clusterName", cfg.ClusterName),
		slog.String("authMode", cfg.AuthMode),
		slog.Bool("proxyEnabled", cfg.ProxyEnabled != nil && *cfg.ProxyEnabled),
		slog.Bool("proxyOperatorRbac", cfg.ProxyOperatorRBAC != nil && *cfg.ProxyOperatorRBAC),
		slog.Bool("hubbleEnabled", cfg.HubbleEnabled == nil || *cfg.HubbleEnabled),
		slog.String("imageTag", cfg.ImageTag),
		slog.String("logLevel", cfg.LogLevel),
		slog.Int("payloadBytes", len(configJSON)),
	)

	// Apply the same defaults and validation the Install path uses.
	if strings.TrimSpace(cfg.BackendURL) == "" {
		return fmt.Errorf("backendUrl is required")
	}
	if cfg.ImageRepo == "" {
		cfg.ImageRepo = agentDefaultImage
	}
	if cfg.ImageTag == "" {
		cfg.ImageTag = agentDefaultImageTag
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = agentDefaultLogLevel
	}
	hubbleEnabled := true
	if cfg.HubbleEnabled != nil {
		hubbleEnabled = *cfg.HubbleEnabled
	}
	proxyEnabled := false
	if cfg.ProxyEnabled != nil {
		proxyEnabled = *cfg.ProxyEnabled
	}
	proxyOperatorRBAC := false
	if cfg.ProxyOperatorRBAC != nil {
		proxyOperatorRBAC = *cfg.ProxyOperatorRBAC
	}
	if proxyOperatorRBAC && !proxyEnabled {
		return fmt.Errorf("proxyOperatorRbac requires proxyEnabled=true")
	}
	if _, _, err := resolveResources(cfg.Resources); err != nil {
		return err
	}

	// mTLS Secret presence check — same as Install.
	if cfg.HubbleRelayTLS != nil && cfg.HubbleRelayTLS.ExistingSecret != "" {
		if _, err := cs.CoreV1().Secrets(ns).Get(ctx, cfg.HubbleRelayTLS.ExistingSecret, metav1.GetOptions{}); err != nil {
			return fmt.Errorf("hubble TLS secret %q: %w", cfg.HubbleRelayTLS.ExistingSecret, err)
		}
	}

	// Re-apply every resource. The ensure* helpers detect existing
	// managed resources and Update them; new fields (e.g. newly
	// mounted TLS secret) propagate via rolling restart of the DS.
	// Operator-tier RBAC is added or removed based on the toggle —
	// flipping it off cleanly revokes the cluster-admin grant.
	steps := []func() error{
		func() error { return ensureServiceAccount(ctx, cs, ns) },
		func() error { return ensureClusterRole(ctx, cs) },
		func() error { return ensureClusterRoleBinding(ctx, cs, ns) },
		func() error { return ensureLeaderRole(ctx, cs, ns) },
		func() error { return ensureLeaderRoleBinding(ctx, cs, ns) },
		func() error { return ensureOperatorClusterRole(ctx, cs, proxyOperatorRBAC) },
		func() error { return ensureOperatorClusterRoleBinding(ctx, cs, ns, proxyOperatorRBAC) },
		func() error { return ensureDaemonSet(ctx, cs, ns, cfg, hubbleEnabled, proxyEnabled) },
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

// extractConfigFromDaemonSet reconstructs AgentInstallConfig from
// the DS's PodSpec. Fields that can't be represented in the config
// (e.g. resource quantities that changed externally to odd values)
// are still best-effort-rendered as strings.
func extractConfigFromDaemonSet(ds *appsv1.DaemonSet, ns string) AgentInstallConfig {
	cfg := AgentInstallConfig{Namespace: ns}

	var container *corev1.Container
	for i := range ds.Spec.Template.Spec.Containers {
		if ds.Spec.Template.Spec.Containers[i].Name == "agent" {
			container = &ds.Spec.Template.Spec.Containers[i]
			break
		}
	}
	if container == nil {
		return cfg
	}

	// Image → repo + tag. Defensive split: ghcr.io/path/image:tag
	// has a colon inside the path for ports (ghcr.io:443/...), but
	// we only split on the LAST colon which is always the tag
	// separator.
	if i := strings.LastIndex(container.Image, ":"); i > 0 {
		cfg.ImageRepo = container.Image[:i]
		cfg.ImageTag = container.Image[i+1:]
	} else {
		cfg.ImageRepo = container.Image
	}
	cfg.ImagePullPolicy = string(container.ImagePullPolicy)

	// Env vars → named config fields. Downward-API sourced env vars
	// are skipped (they belong to the pod spec, not the config).
	env := map[string]string{}
	for _, e := range container.Env {
		if e.ValueFrom != nil {
			continue
		}
		env[e.Name] = e.Value
	}
	cfg.BackendURL = env["KUBEBOLT_BACKEND_URL"]
	cfg.ClusterName = env["KUBEBOLT_AGENT_CLUSTER_NAME"]
	cfg.LogLevel = env["KUBEBOLT_AGENT_LOG_LEVEL"]
	cfg.HubbleRelayAddress = env["KUBEBOLT_HUBBLE_RELAY_ADDR"]
	if v, ok := env["KUBEBOLT_HUBBLE_ENABLED"]; ok {
		b := envBoolDefault(map[string]string{"K": v}, "K", true)
		cfg.HubbleEnabled = &b
	}
	if v, ok := env["KUBEBOLT_AGENT_PROXY_ENABLED"]; ok {
		b := envBoolDefault(map[string]string{"K": v}, "K", false)
		cfg.ProxyEnabled = &b
	}
	// Auth mode + token Secret reconstruction. The mode comes from
	// the env directly; the Secret name comes from the volume that
	// backs /var/run/secrets/kubebolt (matching where Install
	// mounts ingest-token). Missing volume → user installed
	// without ingest auth (disabled or tokenreview).
	if v, ok := env["KUBEBOLT_AGENT_AUTH_MODE"]; ok {
		cfg.AuthMode = v
	}
	if cfg.AuthMode == "ingest-token" {
		for _, vm := range container.VolumeMounts {
			if vm.MountPath != "/var/run/secrets/kubebolt" {
				continue
			}
			for _, v := range ds.Spec.Template.Spec.Volumes {
				if v.Name == vm.Name && v.Secret != nil {
					cfg.AuthTokenSecret = v.Secret.SecretName
				}
			}
		}
	}
	// ProxyOperatorRBAC isn't in the DaemonSet — it's a separate
	// ClusterRole. Detection is best-effort by ClusterRole presence;
	// caller (Configure handler) populates this field after looking
	// it up via the Kubernetes API. Leaving nil here means "unknown
	// from DS alone".

	// mTLS reconstruction: the agent always gets CA_FILE / CERT_FILE
	// / KEY_FILE pointed at /etc/hubble-tls/*. If that volume is
	// mounted, figure out which Secret backs it so the UI can
	// pre-populate the "existingSecret" field.
	for _, vm := range container.VolumeMounts {
		if vm.MountPath != "/etc/hubble-tls" {
			continue
		}
		for _, v := range ds.Spec.Template.Spec.Volumes {
			if v.Name == vm.Name && v.Secret != nil {
				cfg.HubbleRelayTLS = &HubbleRelayTLSConfig{
					ExistingSecret: v.Secret.SecretName,
					ServerName:     env["KUBEBOLT_HUBBLE_RELAY_SERVER_NAME"],
				}
			}
		}
	}

	// Scheduling
	if len(ds.Spec.Template.Spec.NodeSelector) > 0 {
		cfg.NodeSelector = map[string]string{}
		for k, v := range ds.Spec.Template.Spec.NodeSelector {
			cfg.NodeSelector[k] = v
		}
	}
	cfg.PriorityClassName = ds.Spec.Template.Spec.PriorityClassName

	// Resources — surface whatever's there so the UI can show the
	// live numbers rather than re-asserting the defaults.
	res := container.Resources
	if !res.Requests.Cpu().IsZero() || !res.Requests.Memory().IsZero() || !res.Limits.Cpu().IsZero() || !res.Limits.Memory().IsZero() {
		cfg.Resources = &AgentResourceConfig{
			CPURequest:    res.Requests.Cpu().String(),
			CPULimit:      res.Limits.Cpu().String(),
			MemoryRequest: res.Requests.Memory().String(),
			MemoryLimit:   res.Limits.Memory().String(),
		}
	}

	return cfg
}
