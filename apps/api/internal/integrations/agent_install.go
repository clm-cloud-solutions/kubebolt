package integrations

import (
	"context"
	"encoding/json"
	"fmt"
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
// install wizard. Deliberately narrow — covers the common case, not
// every knob the Helm chart exposes. The chart remains the path for
// power users who need affinity, priorityClassName, custom mTLS
// mounts, and so on.
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

	// Container image knobs. Leaving ImageTag empty uses "latest"
	// which is fine for first installs; operators pin via the UI
	// for reproducibility later.
	ImageRepo string `json:"imageRepo,omitempty"`
	ImageTag  string `json:"imageTag,omitempty"`

	// Log level — debug/info/warn/error.
	LogLevel string `json:"logLevel,omitempty"`
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
	agentClusterRole     = "kubebolt-agent-reader"
	agentClusterBinding  = "kubebolt-agent"
	agentLeaderRole      = "kubebolt-agent-leader"
	agentLeaderBinding   = "kubebolt-agent-leader"
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

	// Build + apply manifests in order. RBAC before the workload so
	// pods never come up without the perms they need.
	steps := []func() error{
		func() error { return ensureNamespace(ctx, cs, ns) },
		func() error { return ensureServiceAccount(ctx, cs, ns) },
		func() error { return ensureClusterRole(ctx, cs) },
		func() error { return ensureClusterRoleBinding(ctx, cs, ns) },
		func() error { return ensureLeaderRole(ctx, cs, ns) },
		func() error { return ensureLeaderRoleBinding(ctx, cs, ns) },
		func() error { return ensureDaemonSet(ctx, cs, ns, cfg, hubbleEnabled) },
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

// Uninstall deletes everything labeled as managed-by=kubebolt in the
// agent's namespace, plus the two cluster-scoped RBAC objects whose
// names are well-known. External Helm installs (labeled
// managed-by=Helm) are untouched by design.
func (a *agentProvider) Uninstall(ctx context.Context, cs kubernetes.Interface) error {
	// Find a namespace that hosts resources we own. We can't rely
	// on the install-time namespace because the UI may call
	// Uninstall without re-prompting.
	ds, ns, err := findAgentDaemonSet(ctx, cs)
	if err != nil {
		return fmt.Errorf("locating agent: %w", err)
	}
	if ds == nil || !managedByUs(ds.Labels) {
		// Nothing of ours to remove. If something exists but isn't
		// labeled as ours, we leave it alone — matches the "Helm
		// install stays intact" rule.
		return nil
	}

	// Delete namespaced resources in the resolved namespace. We
	// list-then-delete (rather than DeleteCollection) because the
	// Kubernetes fake client used by tests doesn't implement
	// DeleteCollection label filtering reliably, and the small
	// overhead of an extra list call doesn't matter here — uninstall
	// is rare and the resource set is small (single-digit objects).
	listOpts := metav1.ListOptions{LabelSelector: ManagedByLabel + "=" + ManagedByValue}
	dp := metav1.DeletePropagationForeground
	deleteOpts := metav1.DeleteOptions{PropagationPolicy: &dp}

	if dss, err := cs.AppsV1().DaemonSets(ns).List(ctx, listOpts); err != nil {
		return fmt.Errorf("list DaemonSets: %w", err)
	} else {
		for _, obj := range dss.Items {
			if err := cs.AppsV1().DaemonSets(ns).Delete(ctx, obj.Name, deleteOpts); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("delete DaemonSet %s: %w", obj.Name, err)
			}
		}
	}
	if rbs, err := cs.RbacV1().RoleBindings(ns).List(ctx, listOpts); err != nil {
		return fmt.Errorf("list RoleBindings: %w", err)
	} else {
		for _, obj := range rbs.Items {
			if err := cs.RbacV1().RoleBindings(ns).Delete(ctx, obj.Name, deleteOpts); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("delete RoleBinding %s: %w", obj.Name, err)
			}
		}
	}
	if rs, err := cs.RbacV1().Roles(ns).List(ctx, listOpts); err != nil {
		return fmt.Errorf("list Roles: %w", err)
	} else {
		for _, obj := range rs.Items {
			if err := cs.RbacV1().Roles(ns).Delete(ctx, obj.Name, deleteOpts); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("delete Role %s: %w", obj.Name, err)
			}
		}
	}
	if sas, err := cs.CoreV1().ServiceAccounts(ns).List(ctx, listOpts); err != nil {
		return fmt.Errorf("list ServiceAccounts: %w", err)
	} else {
		for _, obj := range sas.Items {
			if err := cs.CoreV1().ServiceAccounts(ns).Delete(ctx, obj.Name, deleteOpts); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("delete ServiceAccount %s: %w", obj.Name, err)
			}
		}
	}

	// Cluster-scoped RBAC. Guard each delete on the managed-by
	// label so we never rip out RBAC we didn't install.
	if err := deleteClusterRoleBindingIfOurs(ctx, cs, agentClusterBinding); err != nil {
		return err
	}
	if err := deleteClusterRoleIfOurs(ctx, cs, agentClusterRole); err != nil {
		return err
	}

	// Namespace itself: intentionally NOT deleted. It may hold
	// unrelated Secrets / ConfigMaps the admin put there. Leaving
	// it empty is safer than deleting it.
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

func ensureClusterRole(ctx context.Context, cs kubernetes.Interface) error {
	cr := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: agentClusterRole, Labels: managedLabels()},
		Rules: []rbacv1.PolicyRule{
			{APIGroups: []string{""}, Resources: []string{"nodes/stats", "nodes/proxy", "nodes/metrics"}, Verbs: []string{"get"}},
			{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"list", "watch"}},
			{APIGroups: []string{""}, Resources: []string{"namespaces"}, Verbs: []string{"get"}},
		},
	}
	existing, err := cs.RbacV1().ClusterRoles().Get(ctx, agentClusterRole, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = cs.RbacV1().ClusterRoles().Create(ctx, cr, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	if !managedByUs(existing.Labels) {
		return &ConflictError{Kind: "ClusterRole", Name: agentClusterRole, Reason: "already exists and was not installed by KubeBolt"}
	}
	// Keep the rule set fresh even for our own resource — covers
	// upgrades that grow the rule list.
	existing.Rules = cr.Rules
	_, err = cs.RbacV1().ClusterRoles().Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

func ensureClusterRoleBinding(ctx context.Context, cs kubernetes.Interface, ns string) error {
	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: agentClusterBinding, Labels: managedLabels()},
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: agentClusterRole},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: agentSAName, Namespace: ns}},
	}
	existing, err := cs.RbacV1().ClusterRoleBindings().Get(ctx, agentClusterBinding, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = cs.RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	if !managedByUs(existing.Labels) {
		return &ConflictError{Kind: "ClusterRoleBinding", Name: agentClusterBinding, Reason: "already exists and was not installed by KubeBolt"}
	}
	existing.Subjects = crb.Subjects
	existing.RoleRef = crb.RoleRef
	_, err = cs.RbacV1().ClusterRoleBindings().Update(ctx, existing, metav1.UpdateOptions{})
	return err
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

func ensureDaemonSet(ctx context.Context, cs kubernetes.Interface, ns string, cfg AgentInstallConfig, hubbleEnabled bool) error {
	ds := buildAgentDaemonSet(ns, cfg, hubbleEnabled)
	existing, err := cs.AppsV1().DaemonSets(ns).Get(ctx, agentDSName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = cs.AppsV1().DaemonSets(ns).Create(ctx, ds, metav1.CreateOptions{})
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
	return err
}

func buildAgentDaemonSet(ns string, cfg AgentInstallConfig, hubbleEnabled bool) *appsv1.DaemonSet {
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
	}
	if cfg.ClusterName != "" {
		env = append(env, corev1.EnvVar{Name: "KUBEBOLT_AGENT_CLUSTER_NAME", Value: cfg.ClusterName})
	}

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
					Tolerations: []corev1.Toleration{{Operator: corev1.TolerationOpExists}},
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot: &trueVal,
						RunAsUser:    &runAsUser,
					},
					Containers: []corev1.Container{{
						Name:  "agent",
						Image: cfg.ImageRepo + ":" + cfg.ImageTag,
						Env:   env,
						SecurityContext: &corev1.SecurityContext{
							ReadOnlyRootFilesystem:   &trueVal,
							AllowPrivilegeEscalation: &falseVal,
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
						},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("10m"),
								corev1.ResourceMemory: resource.MustParse("30Mi"),
							},
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("100m"),
								corev1.ResourceMemory: resource.MustParse("80Mi"),
							},
						},
					}},
				},
			},
		},
	}
}

// deleteClusterRoleIfOurs is a tiny helper that checks the
// managed-by label before deleting. Cluster-scoped deletes can't
// use DeleteCollection by label selector reliably across all client
// versions, so we Get + check + Delete.
func deleteClusterRoleIfOurs(ctx context.Context, cs kubernetes.Interface, name string) error {
	cr, err := cs.RbacV1().ClusterRoles().Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get ClusterRole %s: %w", name, err)
	}
	if !managedByUs(cr.Labels) {
		return nil
	}
	if err := cs.RbacV1().ClusterRoles().Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete ClusterRole %s: %w", name, err)
	}
	return nil
}

func deleteClusterRoleBindingIfOurs(ctx context.Context, cs kubernetes.Interface, name string) error {
	crb, err := cs.RbacV1().ClusterRoleBindings().Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get ClusterRoleBinding %s: %w", name, err)
	}
	if !managedByUs(crb.Labels) {
		return nil
	}
	if err := cs.RbacV1().ClusterRoleBindings().Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete ClusterRoleBinding %s: %w", name, err)
	}
	return nil
}

