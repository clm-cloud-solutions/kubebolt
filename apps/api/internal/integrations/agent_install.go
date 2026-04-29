package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// AgentInstallConfig is the shape the backend accepts from the
// install wizard. Covers every production-shaped knob of the Helm
// chart except affinity (still Helm-only — the nested selector
// term shape is unwieldy in a wizard UI).
//
// Pointer for HubbleEnabled so "field omitted" (→ default on)
// differs from "explicitly false". Plain bools can't express that.
type AgentInstallConfig struct {
	// Namespace to install into. Defaults to "kubebolt-system".
	// Created if it doesn't exist.
	Namespace string `json:"namespace,omitempty"`

	// Where the agent ships samples. Required — no sane default
	// depends on the deployment topology.
	BackendURL string `json:"backendUrl"`

	// Human-readable cluster label. Optional; cluster_id is
	// auto-discovered from the kube-system UID regardless.
	ClusterName string `json:"clusterName,omitempty"`

	// Kill-switch for the Hubble flow collector. nil → default (on).
	HubbleEnabled *bool `json:"hubbleEnabled,omitempty"`

	// K8s API proxy (Sprint A.5). Sets KUBEBOLT_AGENT_PROXY_ENABLED
	// in the agent's env so it advertises the kube-proxy capability
	// and accepts SPDY tunnel requests from the backend. Required
	// for SaaS multi-cluster topology. Auto-derived from RBACMode
	// (off for metrics, on for reader/operator) unless explicitly
	// overridden — most callers should leave this nil and let the
	// resolution happen from RBACMode.
	ProxyEnabled *bool `json:"proxyEnabled,omitempty"`

	// RBACMode picks the agent ServiceAccount's permission tier:
	//
	//   - "metrics"  : narrow (kubelet stats + pods list/watch +
	//                  namespaces). Proxy stays OFF — only metrics +
	//                  Hubble flows ship to the backend. The
	//                  privacy-conscious default for clusters that
	//                  don't want any apiserver call to leave their
	//                  perimeter.
	//   - "reader"   : cluster-wide get/list/watch on `*/*`. Proxy
	//                  REQUIRED — the backend uses the tunnel to
	//                  read inventory/yaml/logs/etc. Mutations via
	//                  proxy come back 403 because the SA has no
	//                  write verbs. The typical install when the
	//                  cluster's apiserver isn't reachable directly.
	//   - "operator" : wildcard read+write on `*/*`. Proxy REQUIRED.
	//                  Auth REQUIRED. Effectively cluster-admin
	//                  scoped to the agent's SA — exec, scale,
	//                  restart, delete, YAML edit all work through
	//                  the proxy. Without auth, anyone who can dial
	//                  the backend's gRPC port pivots to cluster-
	//                  admin in this cluster.
	//
	// Empty falls back via legacy fields (ProxyOperatorRBAC=true →
	// operator; ProxyEnabled=true → reader; else metrics). New
	// callers should always set RBACMode explicitly.
	RBACMode AgentRBACMode `json:"rbacMode,omitempty"`

	// ProxyOperatorRBAC is the previous binary toggle (now superseded
	// by RBACMode). Kept for wire-compat with older clients; the
	// resolution logic in Install/Configure folds it into RBACMode
	// when RBACMode is empty.
	ProxyOperatorRBAC *bool `json:"proxyOperatorRbac,omitempty"`

	// ─── Auth ──────────────────────────────────────────────
	// Authentication mode the agent uses against the backend's
	// gRPC channel. Set to match the backend's
	// KUBEBOLT_AGENT_AUTH_MODE — when the backend runs in
	// `enforced` or `permissive` mode, this MUST be set or the
	// agent connects without credentials and gets rejected with
	// "unknown auth mode" on first Welcome.
	//
	// Empty (default) leaves auth off — only valid when the
	// backend runs in `disabled` mode (Sprint A migration default).
	//   - "ingest-token"  long-lived bearer token (typical SaaS).
	//                     Requires AuthTokenSecret pointing at a
	//                     pre-existing Secret in the agent's
	//                     namespace with a `token` key.
	//   - "tokenreview"   projected ServiceAccount token; the
	//                     backend validates via apiserver
	//                     TokenReview API. Requires backend to
	//                     run in the same cluster as the agent.
	//                     (Wizard support: future — for now use
	//                     the dev manifest or Helm directly.)
	AuthMode string `json:"authMode,omitempty"`

	// Name of an existing Secret in the agent's namespace whose
	// `token` key holds the ingest token. Required when AuthMode
	// is "ingest-token". The wizard's helper text instructs the
	// user to generate the token on the Agent Tokens admin page
	// then `kubectl create secret generic <name>
	// --from-literal=token=<paste>`.
	AuthTokenSecret string `json:"authTokenSecret,omitempty"`

	// ─── Image ─────────────────────────────────────────────
	ImageRepo       string `json:"imageRepo,omitempty"`
	ImageTag        string `json:"imageTag,omitempty"`
	ImagePullPolicy string `json:"imagePullPolicy,omitempty"` // Always | IfNotPresent | Never

	// ─── Hubble relay ──────────────────────────────────────
	// Override the default relay target (hubble-relay.kube-system.svc:80).
	HubbleRelayAddress string `json:"hubbleRelayAddress,omitempty"`
	// mTLS / TLS material. ExistingSecret must already exist in
	// the target namespace with keys ca.crt (+ optional tls.crt /
	// tls.key for mTLS). Install fails fast when the Secret isn't
	// found — better than a DaemonSet that crash-loops on mount.
	HubbleRelayTLS *HubbleRelayTLSConfig `json:"hubbleRelayTls,omitempty"`

	// ─── Scheduling ────────────────────────────────────────
	NodeSelector      map[string]string `json:"nodeSelector,omitempty"`
	PriorityClassName string            `json:"priorityClassName,omitempty"`

	// ─── Resources ─────────────────────────────────────────
	// Kubernetes quantity strings (e.g. "100m", "128Mi"). Empty
	// fields fall back to the chart defaults.
	Resources *AgentResourceConfig `json:"resources,omitempty"`

	// Log level — debug/info/warn/error.
	LogLevel string `json:"logLevel,omitempty"`
}

// HubbleRelayTLSConfig refers to a pre-existing Secret in the
// agent's namespace. We don't accept raw cert material over the API
// — that would force cert bytes through request logs and browser
// history. Secrets are the K8s-native way to pass this.
type HubbleRelayTLSConfig struct {
	ExistingSecret string `json:"existingSecret"`
	ServerName     string `json:"serverName,omitempty"`
}

// AgentResourceConfig mirrors the four CPU/memory fields of the
// Helm chart's resources block. Anything unset keeps the chart
// default.
type AgentResourceConfig struct {
	CPURequest    string `json:"cpuRequest,omitempty"`
	CPULimit      string `json:"cpuLimit,omitempty"`
	MemoryRequest string `json:"memoryRequest,omitempty"`
	MemoryLimit   string `json:"memoryLimit,omitempty"`
}

// AgentRBACMode picks the ServiceAccount permission tier. See the
// AgentInstallConfig.RBACMode comment for the full semantics of each
// value, and project_agent_rbac_modes.md in the user-memory folder
// for the rationale behind the 3-tier split.
type AgentRBACMode string

const (
	RBACModeMetrics  AgentRBACMode = "metrics"
	RBACModeReader   AgentRBACMode = "reader"
	RBACModeOperator AgentRBACMode = "operator"
)

// IsValid reports whether m is one of the recognized modes. The
// empty string is NOT valid here — callers should resolve a default
// (via legacy fields or "reader" for SaaS-style installs) before
// validating.
func (m AgentRBACMode) IsValid() bool {
	switch m {
	case RBACModeMetrics, RBACModeReader, RBACModeOperator:
		return true
	}
	return false
}

// resolveModeAndProxy folds the legacy ProxyOperatorRBAC + ProxyEnabled
// fields into the new RBACMode + a final proxyEnabled bool. Resolution
// rules (precedence top-down):
//
//   1. cfg.RBACMode if set wins. proxyEnabled defaults to ON for
//      reader/operator (the proxy is the path) and OFF for metrics
//      (the agent ships metrics+flows directly, no apiserver calls).
//      An explicit cfg.ProxyEnabled override is honored only for
//      mode=metrics — reader/operator without proxy is invalid and
//      surfaces as an error.
//   2. cfg.ProxyOperatorRBAC=true (legacy) → mode=operator.
//   3. cfg.ProxyEnabled=true (legacy) → mode=reader.
//   4. Default → mode=metrics, proxy=off.
//
// Validation:
//   - Unknown mode value → error.
//   - reader/operator without proxy → error (architectural invariant).
//   - operator without auth → error (cluster-admin without auth = pivot).
func resolveModeAndProxy(cfg AgentInstallConfig) (AgentRBACMode, bool, error) {
	mode := cfg.RBACMode
	if mode == "" {
		switch {
		case cfg.ProxyOperatorRBAC != nil && *cfg.ProxyOperatorRBAC:
			mode = RBACModeOperator
		case cfg.ProxyEnabled != nil && *cfg.ProxyEnabled:
			mode = RBACModeReader
		default:
			mode = RBACModeMetrics
		}
	}
	if !mode.IsValid() {
		return "", false, fmt.Errorf("rbacMode %q not recognized; expected one of: metrics, reader, operator", mode)
	}

	proxyEnabled := mode == RBACModeReader || mode == RBACModeOperator
	if cfg.ProxyEnabled != nil {
		// Honor explicit override only when it doesn't violate the
		// architectural rule (reader/operator REQUIRE proxy).
		if mode == RBACModeMetrics {
			proxyEnabled = *cfg.ProxyEnabled
		} else if !*cfg.ProxyEnabled {
			return "", false, fmt.Errorf("rbacMode=%s requires proxyEnabled=true (proxy is the only path to apiserver via the agent)", mode)
		}
	}

	if mode == RBACModeOperator && cfg.AuthMode == "" {
		return "", false, fmt.Errorf("rbacMode=operator requires authMode (cluster-admin scoped to the agent SA cannot be exposed without authentication — pick ingest-token or tokenreview)")
	}

	return mode, proxyEnabled, nil
}

// Defaults applied when the wizard leaves fields empty. Kept in one
// place so Install, the UI wizard, and tests all agree on the same
// values.
const (
	agentDefaultImage    = "ghcr.io/clm-cloud-solutions/kubebolt/agent"
	agentDefaultImageTag = "latest"
	agentDefaultLogLevel = "info"
	agentSAName          = "kubebolt-agent"
	agentDSName          = "kubebolt-agent"
	agentLeaderRole      = "kubebolt-agent-leader"
	agentLeaderBinding   = "kubebolt-agent-leader"

	// Metrics tier — narrow (kubelet stats + pods + namespaces).
	// This was historically named "kubebolt-agent-reader"; renamed
	// here so the new mode-based naming reads correctly. The legacy
	// CR name is cleaned up on first apply (see migrateLegacyRBAC).
	agentMetricsClusterRole    = "kubebolt-agent-metrics"
	agentMetricsClusterBinding = "kubebolt-agent-metrics"

	// Reader tier — cluster-wide get/list/watch on every resource.
	// New in v0.2.0. The CR name `kubebolt-agent-reader` was
	// previously used for the narrow metrics rules; on migration we
	// either overwrite the CR's rules (when mode=reader) or delete
	// it (when mode=metrics/operator) — see migrateLegacyRBAC.
	agentReaderClusterRole    = "kubebolt-agent-reader"
	agentReaderClusterBinding = "kubebolt-agent-reader"

	// Legacy ClusterRoleBinding name from pre-0.2.0 installs that
	// bound the SA to the narrow CR. Always deleted on apply (the
	// new metrics-tier Binding replaces it).
	legacyAgentClusterBinding = "kubebolt-agent"
)

// Install applies the agent's manifests to the active cluster.
// Idempotent for the agent's own resources (re-running the same
// install is a no-op when nothing drifted); errors with a
// ConflictError when anything already exists but wasn't put there
// by us.
func (a *agentProvider) Install(ctx context.Context, cs kubernetes.Interface, configJSON json.RawMessage) error {
	var cfg AgentInstallConfig
	if len(configJSON) > 0 {
		if err := json.Unmarshal(configJSON, &cfg); err != nil {
			return fmt.Errorf("invalid install config: %w", err)
		}
	}

	// Resolve defaults.
	ns := cfg.Namespace
	if ns == "" {
		ns = agentNamespace
	}
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

	mode, proxyEnabled, err := resolveModeAndProxy(cfg)
	if err != nil {
		return err
	}

	// Auth mode validation: when set, must be a recognized value
	// AND the matching token Secret must exist. We don't try to
	// auto-detect the backend's enforcement mode here — the wizard
	// is the source of truth for what the operator wants to deploy
	// against. Mismatches surface at agent boot via the
	// "unknown auth mode" / "invalid credentials" gRPC errors.
	switch cfg.AuthMode {
	case "":
		// no auth — fine when backend runs disabled.
	case "ingest-token":
		if cfg.AuthTokenSecret == "" {
			return fmt.Errorf("authMode=ingest-token requires authTokenSecret to name an existing Secret in namespace %q", ns)
		}
	case "tokenreview":
		// tokenreview uses projected SA token, no Secret reference
		// from the user — the agent's pod spec already mounts one
		// via the standard token volume. Future wizard work can
		// surface the audience knob.
	default:
		return fmt.Errorf("authMode %q not recognized; expected one of: ingest-token, tokenreview, or empty", cfg.AuthMode)
	}

	// Validate resource quantity strings up front so a bad value
	// turns into a clear 400 instead of a partial install.
	if _, _, err := resolveResources(cfg.Resources); err != nil {
		return err
	}

	// Build + apply manifests in order. RBAC before the workload so
	// pods never come up without the perms they need. Namespace
	// first so the Secret check below runs against an existing ns.
	if err := ensureNamespace(ctx, cs, ns); err != nil {
		return err
	}

	// Hubble TLS Secret must exist before we point the DaemonSet at
	// it — otherwise pods crash-loop on mount and the admin has no
	// obvious feedback.
	if cfg.HubbleRelayTLS != nil && cfg.HubbleRelayTLS.ExistingSecret != "" {
		if _, err := cs.CoreV1().Secrets(ns).Get(ctx, cfg.HubbleRelayTLS.ExistingSecret, metav1.GetOptions{}); err != nil {
			if apierrors.IsNotFound(err) {
				return fmt.Errorf("hubble TLS secret %q not found in namespace %q — create it before install", cfg.HubbleRelayTLS.ExistingSecret, ns)
			}
			return fmt.Errorf("verify hubble TLS secret: %w", err)
		}
	}

	// Same pre-flight for the auth token Secret: fail fast with a
	// clear error before we create RBAC + DS that will crash-loop
	// on mount.
	if cfg.AuthMode == "ingest-token" {
		sec, err := cs.CoreV1().Secrets(ns).Get(ctx, cfg.AuthTokenSecret, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return fmt.Errorf("auth token secret %q not found in namespace %q — create it before install (kubectl create secret generic %s -n %s --from-literal=token=<paste-token-from-Agent-Tokens-admin-page>)", cfg.AuthTokenSecret, ns, cfg.AuthTokenSecret, ns)
			}
			return fmt.Errorf("verify auth token secret: %w", err)
		}
		if _, ok := sec.Data["token"]; !ok {
			return fmt.Errorf("auth token secret %q has no `token` key — recreate with --from-literal=token=<paste>", cfg.AuthTokenSecret)
		}
	}

	steps := []func() error{
		func() error { return ensureServiceAccount(ctx, cs, ns) },
		func() error { return applyRBACForMode(ctx, cs, ns, mode) },
		func() error { return ensureLeaderRole(ctx, cs, ns) },
		func() error { return ensureLeaderRoleBinding(ctx, cs, ns) },
		func() error { return ensureDaemonSet(ctx, cs, ns, cfg, hubbleEnabled, proxyEnabled) },
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

// resolveResources turns the optional user config into concrete
// ResourceList values, falling back to the chart defaults for any
// field left empty. Returns an error when a string doesn't parse as
// a Kubernetes quantity.
func resolveResources(cfg *AgentResourceConfig) (corev1.ResourceList, corev1.ResourceList, error) {
	req := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("10m"),
		corev1.ResourceMemory: resource.MustParse("30Mi"),
	}
	lim := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("100m"),
		corev1.ResourceMemory: resource.MustParse("80Mi"),
	}
	if cfg == nil {
		return req, lim, nil
	}
	parse := func(field, value string, into corev1.ResourceList, key corev1.ResourceName) error {
		if value == "" {
			return nil
		}
		q, err := resource.ParseQuantity(value)
		if err != nil {
			return fmt.Errorf("invalid resources.%s=%q: %w", field, value, err)
		}
		into[key] = q
		return nil
	}
	if err := parse("cpuRequest", cfg.CPURequest, req, corev1.ResourceCPU); err != nil {
		return nil, nil, err
	}
	if err := parse("memoryRequest", cfg.MemoryRequest, req, corev1.ResourceMemory); err != nil {
		return nil, nil, err
	}
	if err := parse("cpuLimit", cfg.CPULimit, lim, corev1.ResourceCPU); err != nil {
		return nil, nil, err
	}
	if err := parse("memoryLimit", cfg.MemoryLimit, lim, corev1.ResourceMemory); err != nil {
		return nil, nil, err
	}
	return req, lim, nil
}

// Uninstall removes the agent's DaemonSet, RBAC, and ServiceAccount.
// Default behavior refuses to touch anything without the
// managed-by=kubebolt label — returning NotManagedError so the UI
// can render guidance.
//
// opts.Force=true bypasses the label check and deletes by well-known
// name. This exists so an admin can still uninstall an agent that
// was originally laid down by `helm install` or raw `kubectl apply`
// — KubeBolt is the single place where "remove the agent" belongs,
// regardless of how it got there. The UI collects explicit
// confirmation before sending force=true, so the safety property
// ("don't silently clobber external installs") still holds; the
// operator just has to opt in consciously.
//
// Namespace is intentionally NOT deleted — it may hold unrelated
// Secrets / ConfigMaps the admin put there.
func (a *agentProvider) Uninstall(ctx context.Context, cs kubernetes.Interface, opts UninstallOptions) error {
	ds, ns, err := findAgentDaemonSet(ctx, cs)
	if err != nil {
		return fmt.Errorf("locating agent: %w", err)
	}
	if ds == nil {
		// Nothing to do — agent isn't in the cluster at all. Not
		// an error; the UI treats this as the happy "already
		// uninstalled" state.
		return nil
	}
	if !managedByUs(ds.Labels) && !opts.Force {
		return &NotManagedError{
			Kind:      "DaemonSet",
			Namespace: ns,
			Name:      ds.Name,
		}
	}

	// Delete by well-known name. Works uniformly for both cases:
	// managed installs carry the label AND the standard names;
	// external installs (force path) have the same standard names.
	// Each delete tolerates NotFound so partial installs clean up.
	return deleteAgentResources(ctx, cs, ns)
}

func deleteAgentResources(ctx context.Context, cs kubernetes.Interface, ns string) error {
	dp := metav1.DeletePropagationForeground
	opts := metav1.DeleteOptions{PropagationPolicy: &dp}

	if err := cs.AppsV1().DaemonSets(ns).Delete(ctx, agentDSName, opts); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete DaemonSet: %w", err)
	}
	if err := cs.RbacV1().RoleBindings(ns).Delete(ctx, agentLeaderBinding, opts); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete RoleBinding: %w", err)
	}
	if err := cs.RbacV1().Roles(ns).Delete(ctx, agentLeaderRole, opts); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete Role: %w", err)
	}
	if err := cs.CoreV1().ServiceAccounts(ns).Delete(ctx, agentSAName, opts); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete ServiceAccount: %w", err)
	}
	// Tear down all three RBAC tiers + the legacy Binding name. Each
	// delete tolerates NotFound so a partial / mode-mixed install
	// cleans up regardless of which tiers were ever applied.
	clusterBindings := []string{
		agentMetricsClusterBinding,
		agentReaderClusterBinding,
		agentOperatorClusterBinding,
		legacyAgentClusterBinding,
	}
	for _, name := range clusterBindings {
		if err := cs.RbacV1().ClusterRoleBindings().Delete(ctx, name, opts); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete ClusterRoleBinding %q: %w", name, err)
		}
	}
	clusterRoles := []string{
		agentMetricsClusterRole,
		agentReaderClusterRole,
		agentOperatorClusterRole,
	}
	for _, name := range clusterRoles {
		if err := cs.RbacV1().ClusterRoles().Delete(ctx, name, opts); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete ClusterRole %q: %w", name, err)
		}
	}
	return nil
}

// ─── Builders + apply helpers ─────────────────────────────

func managedLabels() map[string]string {
	return map[string]string{
		ManagedByLabel:           ManagedByValue,
		"app.kubernetes.io/name": AgentName,
	}
}

func managedByUs(labels map[string]string) bool {
	return labels[ManagedByLabel] == ManagedByValue
}

func ensureNamespace(ctx context.Context, cs kubernetes.Interface, name string) error {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: managedLabels()},
	}
	_, err := cs.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	if err == nil || apierrors.IsAlreadyExists(err) {
		return nil
	}
	return fmt.Errorf("create namespace: %w", err)
}

func ensureServiceAccount(ctx context.Context, cs kubernetes.Interface, ns string) error {
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      agentSAName,
			Namespace: ns,
			Labels:    managedLabels(),
		},
	}
	existing, err := cs.CoreV1().ServiceAccounts(ns).Get(ctx, agentSAName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = cs.CoreV1().ServiceAccounts(ns).Create(ctx, sa, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	if !managedByUs(existing.Labels) {
		return &ConflictError{Kind: "ServiceAccount", Namespace: ns, Name: agentSAName, Reason: "already exists and was not installed by KubeBolt"}
	}
	return nil
}

// metricsClusterRoleRules is the narrow rule set the agent's SA
// always carries — kubelet stats + the small set of resources the
// agent itself reads at startup (pods for metric enrichment,
// namespaces for cluster_id discovery). Mode=metrics installs ONLY
// this; mode=reader/operator add their tier on top.
func metricsClusterRoleRules() []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{
		{APIGroups: []string{""}, Resources: []string{"nodes/stats", "nodes/proxy", "nodes/metrics"}, Verbs: []string{"get"}},
		{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"list", "watch"}},
		{APIGroups: []string{""}, Resources: []string{"namespaces"}, Verbs: []string{"get"}},
	}
}

// readerClusterRoleRules grants cluster-wide get/list/watch on every
// resource — the read-only tier exposed to the backend through the
// SPDY proxy. Subresources don't match the wildcard (apiserver
// quirk), so pods/log + the proxy variants are enumerated.
func readerClusterRoleRules() []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{
		{APIGroups: []string{"*"}, Resources: []string{"*"}, Verbs: []string{"get", "list", "watch"}},
		{APIGroups: []string{""}, Resources: []string{"pods/log"}, Verbs: []string{"get", "list", "watch"}},
		{APIGroups: []string{""}, Resources: []string{"pods/proxy", "services/proxy", "nodes/proxy"}, Verbs: []string{"get"}},
		// Discovery — client-go hits these on every connection.
		{NonResourceURLs: []string{"*"}, Verbs: []string{"get"}},
	}
}

// ensureMetricsClusterRole applies the always-present narrow CR
// (renamed from "kubebolt-agent-reader" in v0.2.0). The companion
// migrateLegacyRBAC removes the old name on first apply.
func ensureMetricsClusterRole(ctx context.Context, cs kubernetes.Interface) error {
	cr := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: agentMetricsClusterRole, Labels: managedLabels()},
		Rules:      metricsClusterRoleRules(),
	}
	existing, err := cs.RbacV1().ClusterRoles().Get(ctx, agentMetricsClusterRole, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = cs.RbacV1().ClusterRoles().Create(ctx, cr, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	if !managedByUs(existing.Labels) {
		return &ConflictError{Kind: "ClusterRole", Name: agentMetricsClusterRole, Reason: "already exists and was not installed by KubeBolt"}
	}
	existing.Rules = cr.Rules
	_, err = cs.RbacV1().ClusterRoles().Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

func ensureMetricsClusterRoleBinding(ctx context.Context, cs kubernetes.Interface, ns string) error {
	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: agentMetricsClusterBinding, Labels: managedLabels()},
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: agentMetricsClusterRole},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: agentSAName, Namespace: ns}},
	}
	existing, err := cs.RbacV1().ClusterRoleBindings().Get(ctx, agentMetricsClusterBinding, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = cs.RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	if !managedByUs(existing.Labels) {
		return &ConflictError{Kind: "ClusterRoleBinding", Name: agentMetricsClusterBinding, Reason: "already exists and was not installed by KubeBolt"}
	}
	existing.Subjects = crb.Subjects
	existing.RoleRef = crb.RoleRef
	_, err = cs.RbacV1().ClusterRoleBindings().Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

// ensureReaderClusterRole applies (grant=true) or removes
// (grant=false) the cluster-wide read CR. Same shape as the operator
// helpers — symmetric so applyRBACForMode just toggles based on the
// requested mode.
//
// Note: the CR name "kubebolt-agent-reader" was reused from the
// pre-0.2.0 metrics-tier. When mode=reader, the rules under that name
// are REPLACED by the new wildcard read set. The old narrow rules
// migrate to the renamed kubebolt-agent-metrics CR via
// ensureMetricsClusterRole.
func ensureReaderClusterRole(ctx context.Context, cs kubernetes.Interface, grant bool) error {
	if !grant {
		return removeManagedClusterRole(ctx, cs, agentReaderClusterRole)
	}
	cr := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: agentReaderClusterRole,
			Labels: mergeLabels(managedLabels(), map[string]string{
				"kubebolt.dev/rbac-tier": "reader",
			}),
		},
		Rules: readerClusterRoleRules(),
	}
	existing, err := cs.RbacV1().ClusterRoles().Get(ctx, agentReaderClusterRole, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = cs.RbacV1().ClusterRoles().Create(ctx, cr, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	// Adopt CRs that came from the same shipped manifest (or from a
	// previous install). Since we're reusing the legacy name with new
	// rules, BOTH legacy-managed CRs and signature-labeled new CRs
	// are owned by us — overwrite rules in either case. Foreign CRs
	// (no managed-by, no signature) still error out.
	if !managedByUs(existing.Labels) {
		if existing.Labels["kubebolt.dev/rbac-tier"] != "reader" {
			return &ConflictError{Kind: "ClusterRole", Name: agentReaderClusterRole, Reason: "already exists and was not installed by KubeBolt"}
		}
		slog.Info("agent integration: adopting pre-existing reader ClusterRole",
			slog.String("name", agentReaderClusterRole),
		)
	}
	existing.Rules = cr.Rules
	existing.Labels = cr.Labels
	_, err = cs.RbacV1().ClusterRoles().Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

func ensureReaderClusterRoleBinding(ctx context.Context, cs kubernetes.Interface, ns string, grant bool) error {
	if !grant {
		return removeManagedClusterRoleBinding(ctx, cs, agentReaderClusterBinding)
	}
	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: agentReaderClusterBinding,
			Labels: mergeLabels(managedLabels(), map[string]string{
				"kubebolt.dev/rbac-tier": "reader",
			}),
		},
		RoleRef:  rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: agentReaderClusterRole},
		Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: agentSAName, Namespace: ns}},
	}
	existing, err := cs.RbacV1().ClusterRoleBindings().Get(ctx, agentReaderClusterBinding, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = cs.RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	if !managedByUs(existing.Labels) {
		if existing.Labels["kubebolt.dev/rbac-tier"] != "reader" {
			return &ConflictError{Kind: "ClusterRoleBinding", Name: agentReaderClusterBinding, Reason: "already exists and was not installed by KubeBolt"}
		}
		slog.Info("agent integration: adopting pre-existing reader ClusterRoleBinding",
			slog.String("name", agentReaderClusterBinding),
		)
	}
	existing.Subjects = crb.Subjects
	existing.RoleRef = crb.RoleRef
	existing.Labels = crb.Labels
	_, err = cs.RbacV1().ClusterRoleBindings().Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

// migrateLegacyRBAC handles the pre-0.2.0 → 0.2.0 transition. The
// legacy install put metrics rules under name "kubebolt-agent-reader"
// + bound them via "kubebolt-agent" Binding. v0.2.0 splits these:
// metrics rules → "kubebolt-agent-metrics" (new), reader rules
// (cluster-wide) → "kubebolt-agent-reader" (rules replaced).
//
// We handle:
//   - Legacy Binding "kubebolt-agent" — always delete (replaced by
//     "kubebolt-agent-metrics" Binding).
//   - Legacy CR "kubebolt-agent-reader" with metrics rules — left
//     for the mode-driven flow to handle: when mode=reader its rules
//     get overwritten with cluster-wide read; when mode!=reader it
//     gets deleted by ensureReaderClusterRole(grant=false).
//
// Idempotent — safe to run on already-migrated installs.
func migrateLegacyRBAC(ctx context.Context, cs kubernetes.Interface) error {
	existing, err := cs.RbacV1().ClusterRoleBindings().Get(ctx, legacyAgentClusterBinding, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check legacy ClusterRoleBinding: %w", err)
	}
	if !managedByUs(existing.Labels) {
		// Foreign Binding with the same name — leave it alone.
		return nil
	}
	if err := cs.RbacV1().ClusterRoleBindings().Delete(ctx, legacyAgentClusterBinding, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete legacy ClusterRoleBinding: %w", err)
	}
	slog.Info("agent integration: deleted legacy ClusterRoleBinding",
		slog.String("name", legacyAgentClusterBinding),
	)
	return nil
}

// applyRBACForMode is the single entry point for RBAC apply. Always
// installs the metrics tier; conditionally adds reader/operator on
// top based on mode. Switching modes (e.g. operator → reader)
// cleanly removes the previously-applied tier.
func applyRBACForMode(ctx context.Context, cs kubernetes.Interface, ns string, mode AgentRBACMode) error {
	if err := migrateLegacyRBAC(ctx, cs); err != nil {
		return err
	}
	if err := ensureMetricsClusterRole(ctx, cs); err != nil {
		return err
	}
	if err := ensureMetricsClusterRoleBinding(ctx, cs, ns); err != nil {
		return err
	}
	grantReader := mode == RBACModeReader
	if err := ensureReaderClusterRole(ctx, cs, grantReader); err != nil {
		return err
	}
	if err := ensureReaderClusterRoleBinding(ctx, cs, ns, grantReader); err != nil {
		return err
	}
	grantOperator := mode == RBACModeOperator
	if err := ensureOperatorClusterRole(ctx, cs, grantOperator); err != nil {
		return err
	}
	if err := ensureOperatorClusterRoleBinding(ctx, cs, ns, grantOperator); err != nil {
		return err
	}
	return nil
}

// agentOperatorClusterRole is the ClusterRole granting wildcard
// read+write to the agent SA — required for the dashboard to render
// fully through the SPDY proxy. Off by default; opt-in via
// AgentInstallConfig.ProxyOperatorRBAC. Kept as a parallel resource
// (NOT merging into kubebolt-agent-reader) so revoking it stays a
// single delete instead of a rule diff.
const (
	agentOperatorClusterRole    = "kubebolt-agent-operator"
	agentOperatorClusterBinding = "kubebolt-agent-operator"
)

// ensureOperatorClusterRole applies (when grant=true) or removes
// (when grant=false) the operator-tier ClusterRole. Removal is
// idempotent and only touches resources we own (managed-by label).
func ensureOperatorClusterRole(ctx context.Context, cs kubernetes.Interface, grant bool) error {
	if !grant {
		return removeManagedClusterRole(ctx, cs, agentOperatorClusterRole)
	}
	cr := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: agentOperatorClusterRole,
			Labels: mergeLabels(managedLabels(), map[string]string{
				"kubebolt.dev/rbac-tier": "operator",
			}),
		},
		Rules: []rbacv1.PolicyRule{
			// Wildcard read+write on every resource. Verbose alternative
			// (enumerated verbs/resources) ages poorly when new dashboard
			// features land — see deploy/agent/kubebolt-agent-rbac-operator.yaml
			// for the same rationale shipped to manual installers.
			{APIGroups: []string{"*"}, Resources: []string{"*"}, Verbs: []string{"get", "list", "watch", "create", "update", "patch", "delete", "deletecollection"}},
			// Subresources don't match the wildcard above — enumerated.
			{APIGroups: []string{""}, Resources: []string{"pods/exec", "pods/portforward", "pods/log", "pods/eviction", "pods/proxy", "services/proxy", "nodes/proxy"}, Verbs: []string{"get", "create"}},
			{APIGroups: []string{"apps"}, Resources: []string{"deployments/scale", "statefulsets/scale", "replicasets/scale"}, Verbs: []string{"get", "update", "patch"}},
			// Non-resource URLs (/api, /apis, /healthz). client-go's
			// discovery touches these on every connection.
			{NonResourceURLs: []string{"*"}, Verbs: []string{"get"}},
		},
	}
	existing, err := cs.RbacV1().ClusterRoles().Get(ctx, agentOperatorClusterRole, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = cs.RbacV1().ClusterRoles().Create(ctx, cr, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	// Adopt CRs that came from our shipped manifest
	// (deploy/agent/kubebolt-agent-rbac-operator.yaml) — they
	// carry our `kubebolt.dev/rbac-tier=operator` signature. Older
	// versions of that manifest didn't stamp the managed-by label,
	// so a clean kubectl-apply leaves it un-adoptable. Detecting the
	// signature lets the UI toggle take ownership safely without an
	// extra "delete it manually first" step. Anything without the
	// signature is genuinely foreign — refuse.
	if !managedByUs(existing.Labels) {
		if existing.Labels["kubebolt.dev/rbac-tier"] != "operator" {
			return &ConflictError{Kind: "ClusterRole", Name: agentOperatorClusterRole, Reason: "already exists and was not installed by KubeBolt"}
		}
		slog.Info("agent integration: adopting pre-existing operator ClusterRole",
			slog.String("name", agentOperatorClusterRole),
		)
	}
	existing.Rules = cr.Rules
	existing.Labels = cr.Labels
	_, err = cs.RbacV1().ClusterRoles().Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

// ensureOperatorClusterRoleBinding applies/removes the binding to
// the agent's SA. Mirrors ensureOperatorClusterRole's grant flag.
func ensureOperatorClusterRoleBinding(ctx context.Context, cs kubernetes.Interface, ns string, grant bool) error {
	if !grant {
		return removeManagedClusterRoleBinding(ctx, cs, agentOperatorClusterBinding)
	}
	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: agentOperatorClusterBinding,
			Labels: mergeLabels(managedLabels(), map[string]string{
				"kubebolt.dev/rbac-tier": "operator",
			}),
		},
		RoleRef:  rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: agentOperatorClusterRole},
		Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: agentSAName, Namespace: ns}},
	}
	existing, err := cs.RbacV1().ClusterRoleBindings().Get(ctx, agentOperatorClusterBinding, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = cs.RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	// Same adoption rule as ensureOperatorClusterRole: the shipped
	// manifest stamps `kubebolt.dev/rbac-tier=operator` on the
	// binding, so the toggle can take ownership of a manually-applied
	// install. Foreign bindings (no signature) still error out.
	if !managedByUs(existing.Labels) {
		if existing.Labels["kubebolt.dev/rbac-tier"] != "operator" {
			return &ConflictError{Kind: "ClusterRoleBinding", Name: agentOperatorClusterBinding, Reason: "already exists and was not installed by KubeBolt"}
		}
		slog.Info("agent integration: adopting pre-existing operator ClusterRoleBinding",
			slog.String("name", agentOperatorClusterBinding),
		)
	}
	existing.Subjects = crb.Subjects
	existing.RoleRef = crb.RoleRef
	existing.Labels = crb.Labels
	_, err = cs.RbacV1().ClusterRoleBindings().Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

// removeManagedClusterRole deletes the named ClusterRole if it
// exists AND carries our managed-by label. Returns nil when the
// resource isn't there (idempotent revoke); errors when an
// unmanaged resource has the same name (caller can keep going).
func removeManagedClusterRole(ctx context.Context, cs kubernetes.Interface, name string) error {
	existing, err := cs.RbacV1().ClusterRoles().Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !managedByUs(existing.Labels) {
		// Not ours — leave it alone; warn via ConflictError so the
		// caller can decide.
		return &ConflictError{Kind: "ClusterRole", Name: name, Reason: "exists but not managed by KubeBolt — leaving in place"}
	}
	return cs.RbacV1().ClusterRoles().Delete(ctx, name, metav1.DeleteOptions{})
}

func removeManagedClusterRoleBinding(ctx context.Context, cs kubernetes.Interface, name string) error {
	existing, err := cs.RbacV1().ClusterRoleBindings().Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !managedByUs(existing.Labels) {
		return &ConflictError{Kind: "ClusterRoleBinding", Name: name, Reason: "exists but not managed by KubeBolt — leaving in place"}
	}
	return cs.RbacV1().ClusterRoleBindings().Delete(ctx, name, metav1.DeleteOptions{})
}

// mergeLabels combines two label maps, second wins on key collision.
// Pulled out so the operator-tier resources can extend managedLabels()
// with their tier marker without copy-pasting boilerplate.
func mergeLabels(a, b map[string]string) map[string]string {
	out := make(map[string]string, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func ensureLeaderRole(ctx context.Context, cs kubernetes.Interface, ns string) error {
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: agentLeaderRole, Namespace: ns, Labels: managedLabels()},
		Rules: []rbacv1.PolicyRule{
			{APIGroups: []string{"coordination.k8s.io"}, Resources: []string{"leases"}, Verbs: []string{"get", "list", "watch", "create", "update", "patch"}},
		},
	}
	existing, err := cs.RbacV1().Roles(ns).Get(ctx, agentLeaderRole, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = cs.RbacV1().Roles(ns).Create(ctx, role, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	if !managedByUs(existing.Labels) {
		return &ConflictError{Kind: "Role", Namespace: ns, Name: agentLeaderRole, Reason: "already exists and was not installed by KubeBolt"}
	}
	existing.Rules = role.Rules
	_, err = cs.RbacV1().Roles(ns).Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

func ensureLeaderRoleBinding(ctx context.Context, cs kubernetes.Interface, ns string) error {
	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: agentLeaderBinding, Namespace: ns, Labels: managedLabels()},
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: agentLeaderRole},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: agentSAName, Namespace: ns}},
	}
	existing, err := cs.RbacV1().RoleBindings(ns).Get(ctx, agentLeaderBinding, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = cs.RbacV1().RoleBindings(ns).Create(ctx, rb, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	if !managedByUs(existing.Labels) {
		return &ConflictError{Kind: "RoleBinding", Namespace: ns, Name: agentLeaderBinding, Reason: "already exists and was not installed by KubeBolt"}
	}
	existing.Subjects = rb.Subjects
	existing.RoleRef = rb.RoleRef
	_, err = cs.RbacV1().RoleBindings(ns).Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

func ensureDaemonSet(ctx context.Context, cs kubernetes.Interface, ns string, cfg AgentInstallConfig, hubbleEnabled, proxyEnabled bool) error {
	ds := buildAgentDaemonSet(ns, cfg, hubbleEnabled, proxyEnabled)
	existing, err := cs.AppsV1().DaemonSets(ns).Get(ctx, agentDSName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = cs.AppsV1().DaemonSets(ns).Create(ctx, ds, metav1.CreateOptions{})
		logAgentDSEnv("Create", ds, err)
		return err
	}
	if err != nil {
		return err
	}
	if !managedByUs(existing.Labels) {
		return &ConflictError{Kind: "DaemonSet", Namespace: ns, Name: agentDSName, Reason: "already exists and was not installed by KubeBolt"}
	}
	// Preserve resourceVersion so Update conflicts surface rather
	// than being silently overwritten.
	ds.ResourceVersion = existing.ResourceVersion
	_, err = cs.AppsV1().DaemonSets(ns).Update(ctx, ds, metav1.UpdateOptions{})
	logAgentDSEnv("Update", ds, err)
	return err
}

// logAgentDSEnv emits the env vars we just wrote to the DaemonSet
// so a "values didn't persist" report is decidable from logs alone:
// either the expected vars are here (UI/cache bug) or they aren't
// (Configure handler / build path bug).
func logAgentDSEnv(op string, ds *appsv1.DaemonSet, err error) {
	if err != nil {
		slog.Error("agent integration: DaemonSet "+op+" failed",
			slog.String("error", err.Error()),
		)
		return
	}
	envs := agentContainerEnv(ds)
	slog.Info("agent integration: DaemonSet "+op+" applied",
		slog.String("KUBEBOLT_AGENT_PROXY_ENABLED", envs["KUBEBOLT_AGENT_PROXY_ENABLED"]),
		slog.String("KUBEBOLT_HUBBLE_ENABLED", envs["KUBEBOLT_HUBBLE_ENABLED"]),
		slog.String("KUBEBOLT_AGENT_AUTH_MODE", envs["KUBEBOLT_AGENT_AUTH_MODE"]),
		slog.String("KUBEBOLT_AGENT_CLUSTER_NAME", envs["KUBEBOLT_AGENT_CLUSTER_NAME"]),
		slog.String("KUBEBOLT_BACKEND_URL", envs["KUBEBOLT_BACKEND_URL"]),
	)
}

func buildAgentDaemonSet(ns string, cfg AgentInstallConfig, hubbleEnabled, proxyEnabled bool) *appsv1.DaemonSet {
	selector := map[string]string{"app.kubernetes.io/name": AgentName}
	podLabels := map[string]string{
		"app.kubernetes.io/name": AgentName,
		ManagedByLabel:           ManagedByValue,
	}

	env := []corev1.EnvVar{
		{Name: "KUBEBOLT_BACKEND_URL", Value: cfg.BackendURL},
		{Name: "KUBEBOLT_AGENT_LOG_LEVEL", Value: cfg.LogLevel},
		{Name: "KUBEBOLT_AGENT_NODE_NAME", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "spec.nodeName"}}},
		{Name: "KUBEBOLT_AGENT_NODE_IP", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "status.hostIP"}}},
		{Name: "POD_NAME", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"}}},
		{Name: "POD_NAMESPACE", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"}}},
		{Name: "KUBEBOLT_HUBBLE_ENABLED", Value: strconv.FormatBool(hubbleEnabled)},
		{Name: "KUBEBOLT_AGENT_PROXY_ENABLED", Value: strconv.FormatBool(proxyEnabled)},
	}
	if cfg.ClusterName != "" {
		env = append(env, corev1.EnvVar{Name: "KUBEBOLT_AGENT_CLUSTER_NAME", Value: cfg.ClusterName})
	}
	// Auth wiring — mirrors deploy/agent/kubebolt-agent-dev-auth.yaml.
	// For ingest-token: mount the user-supplied Secret at a fixed
	// path and point KUBEBOLT_AGENT_TOKEN_FILE at it. For
	// tokenreview: just set the mode (the agent uses its projected
	// SA token via the default token volume).
	if cfg.AuthMode != "" {
		env = append(env, corev1.EnvVar{Name: "KUBEBOLT_AGENT_AUTH_MODE", Value: cfg.AuthMode})
	}
	if cfg.AuthMode == "ingest-token" && cfg.AuthTokenSecret != "" {
		env = append(env, corev1.EnvVar{Name: "KUBEBOLT_AGENT_TOKEN_FILE", Value: "/var/run/secrets/kubebolt/token"})
	}
	if cfg.HubbleRelayAddress != "" {
		env = append(env, corev1.EnvVar{Name: "KUBEBOLT_HUBBLE_RELAY_ADDR", Value: cfg.HubbleRelayAddress})
	}
	// Hubble TLS: mounted Secret keys map to the env paths the
	// agent expects. ca.crt alone enables TLS; tls.crt + tls.key
	// enable mTLS — the agent's buildRelayCredentials branches on
	// which files exist.
	var volumes []corev1.Volume
	var volumeMounts []corev1.VolumeMount
	// Mount the auth token Secret read-only.
	if cfg.AuthMode == "ingest-token" && cfg.AuthTokenSecret != "" {
		volumes = append(volumes, corev1.Volume{
			Name: "kubebolt-agent-token",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{SecretName: cfg.AuthTokenSecret},
			},
		})
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name: "kubebolt-agent-token", MountPath: "/var/run/secrets/kubebolt", ReadOnly: true,
		})
	}

	if cfg.HubbleRelayTLS != nil && cfg.HubbleRelayTLS.ExistingSecret != "" {
		env = append(env,
			corev1.EnvVar{Name: "KUBEBOLT_HUBBLE_RELAY_CA_FILE", Value: "/etc/hubble-tls/ca.crt"},
			corev1.EnvVar{Name: "KUBEBOLT_HUBBLE_RELAY_CERT_FILE", Value: "/etc/hubble-tls/tls.crt"},
			corev1.EnvVar{Name: "KUBEBOLT_HUBBLE_RELAY_KEY_FILE", Value: "/etc/hubble-tls/tls.key"},
		)
		if cfg.HubbleRelayTLS.ServerName != "" {
			env = append(env, corev1.EnvVar{Name: "KUBEBOLT_HUBBLE_RELAY_SERVER_NAME", Value: cfg.HubbleRelayTLS.ServerName})
		}
		volumes = append(volumes, corev1.Volume{
			Name: "hubble-tls",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{SecretName: cfg.HubbleRelayTLS.ExistingSecret},
			},
		})
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name: "hubble-tls", MountPath: "/etc/hubble-tls", ReadOnly: true,
		})
	}

	req, lim, _ := resolveResources(cfg.Resources) // validated in Install

	pullPolicy := corev1.PullPolicy(cfg.ImagePullPolicy)
	// Leaving ImagePullPolicy empty defers to K8s' built-in default
	// (Always for :latest, IfNotPresent otherwise), which matches
	// what a plain `kubectl apply` would produce.

	runAsUser := int64(65532)
	trueVal := true
	falseVal := false

	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      agentDSName,
			Namespace: ns,
			Labels:    managedLabels(),
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: selector},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: podLabels},
				Spec: corev1.PodSpec{
					ServiceAccountName: agentSAName,
					// Land on every node, including control-plane,
					// so the agent sees each kubelet. Matches the
					// Helm chart default.
					Tolerations:       []corev1.Toleration{{Operator: corev1.TolerationOpExists}},
					NodeSelector:      cfg.NodeSelector,
					PriorityClassName: cfg.PriorityClassName,
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot: &trueVal,
						RunAsUser:    &runAsUser,
					},
					Volumes: volumes,
					Containers: []corev1.Container{{
						Name:            "agent",
						Image:           cfg.ImageRepo + ":" + cfg.ImageTag,
						ImagePullPolicy: pullPolicy,
						Env:             env,
						VolumeMounts:    volumeMounts,
						SecurityContext: &corev1.SecurityContext{
							ReadOnlyRootFilesystem:   &trueVal,
							AllowPrivilegeEscalation: &falseVal,
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
						},
						Resources: corev1.ResourceRequirements{Requests: req, Limits: lim},
					}},
				},
			},
		},
	}
}


