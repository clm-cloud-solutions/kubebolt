package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestAgentInstall_FreshCluster(t *testing.T) {
	cs := fake.NewSimpleClientset()
	cfg := AgentInstallConfig{
		BackendURL:  "kubebolt.kubebolt.svc:9090",
		ClusterName: "dev",
	}
	raw, _ := json.Marshal(cfg)

	if err := NewAgent().(Installable).Install(context.Background(), cs, raw); err != nil {
		t.Fatalf("install failed: %v", err)
	}

	// Every resource should exist and carry the managed-by label.
	ns, _ := cs.CoreV1().Namespaces().Get(context.Background(), agentNamespace, metav1.GetOptions{})
	if !managedByUs(ns.Labels) {
		t.Errorf("namespace missing managed-by label, got %v", ns.Labels)
	}

	sa, _ := cs.CoreV1().ServiceAccounts(agentNamespace).Get(context.Background(), agentSAName, metav1.GetOptions{})
	if !managedByUs(sa.Labels) {
		t.Error("serviceaccount missing managed-by label")
	}

	cr, _ := cs.RbacV1().ClusterRoles().Get(context.Background(), agentMetricsClusterRole, metav1.GetOptions{})
	if !managedByUs(cr.Labels) {
		t.Error("clusterrole missing managed-by label")
	}
	if len(cr.Rules) < 3 {
		t.Errorf("expected >=3 rules on ClusterRole, got %d", len(cr.Rules))
	}

	crb, _ := cs.RbacV1().ClusterRoleBindings().Get(context.Background(), agentMetricsClusterBinding, metav1.GetOptions{})
	if crb.RoleRef.Name != agentMetricsClusterRole {
		t.Errorf("binding references %q, want %q", crb.RoleRef.Name, agentMetricsClusterRole)
	}

	role, _ := cs.RbacV1().Roles(agentNamespace).Get(context.Background(), agentLeaderRole, metav1.GetOptions{})
	if !managedByUs(role.Labels) {
		t.Error("leader role missing managed-by label")
	}

	ds, err := cs.AppsV1().DaemonSets(agentNamespace).Get(context.Background(), agentDSName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("daemonset not created: %v", err)
	}
	if !managedByUs(ds.Labels) {
		t.Error("daemonset missing managed-by label")
	}

	// Env assertions: backendUrl set, hubble on (default), cluster
	// name propagated.
	env := map[string]string{}
	for _, e := range ds.Spec.Template.Spec.Containers[0].Env {
		if e.ValueFrom == nil {
			env[e.Name] = e.Value
		}
	}
	if env["KUBEBOLT_BACKEND_URL"] != cfg.BackendURL {
		t.Errorf("backend URL = %q, want %q", env["KUBEBOLT_BACKEND_URL"], cfg.BackendURL)
	}
	if env["KUBEBOLT_AGENT_CLUSTER_NAME"] != "dev" {
		t.Errorf("cluster name = %q, want dev", env["KUBEBOLT_AGENT_CLUSTER_NAME"])
	}
	if env["KUBEBOLT_HUBBLE_ENABLED"] != "true" {
		t.Errorf("hubble enabled = %q, want true (default)", env["KUBEBOLT_HUBBLE_ENABLED"])
	}
}

func TestAgentInstall_HubbleDisabled(t *testing.T) {
	cs := fake.NewSimpleClientset()
	hubbleOff := false
	cfg := AgentInstallConfig{BackendURL: "x:9090", HubbleEnabled: &hubbleOff}
	raw, _ := json.Marshal(cfg)

	if err := NewAgent().(Installable).Install(context.Background(), cs, raw); err != nil {
		t.Fatalf("install failed: %v", err)
	}
	ds, _ := cs.AppsV1().DaemonSets(agentNamespace).Get(context.Background(), agentDSName, metav1.GetOptions{})
	for _, e := range ds.Spec.Template.Spec.Containers[0].Env {
		if e.Name == "KUBEBOLT_HUBBLE_ENABLED" && e.Value != "false" {
			t.Errorf("hubble enabled env = %q, want false", e.Value)
		}
	}
}

func TestAgentInstall_MissingBackendURL(t *testing.T) {
	cs := fake.NewSimpleClientset()
	raw, _ := json.Marshal(AgentInstallConfig{})

	err := NewAgent().(Installable).Install(context.Background(), cs, raw)
	if err == nil {
		t.Fatal("expected error for missing backendUrl, got nil")
	}
}

func TestAgentInstall_Idempotent(t *testing.T) {
	cs := fake.NewSimpleClientset()
	cfg := AgentInstallConfig{BackendURL: "x:9090"}
	raw, _ := json.Marshal(cfg)
	install := NewAgent().(Installable)

	if err := install.Install(context.Background(), cs, raw); err != nil {
		t.Fatalf("first install failed: %v", err)
	}
	// Second install with the same config should succeed — the
	// UI's re-click on "Install" after refresh must not error.
	if err := install.Install(context.Background(), cs, raw); err != nil {
		t.Fatalf("second install failed: %v", err)
	}
}

func TestAgentInstall_ConflictWithExternal(t *testing.T) {
	// Pre-populate a DaemonSet that isn't labeled as ours (simulates
	// a Helm install that preceded KubeBolt's install button).
	external := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      agentDSName,
			Namespace: agentNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "Helm",
			},
		},
	}
	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: agentNamespace}},
		external,
	)
	cfg := AgentInstallConfig{BackendURL: "x:9090"}
	raw, _ := json.Marshal(cfg)

	err := NewAgent().(Installable).Install(context.Background(), cs, raw)
	if err == nil {
		t.Fatal("expected ConflictError, got nil")
	}
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("expected *ConflictError, got %T: %v", err, err)
	}
	if conflict.Kind != "DaemonSet" {
		t.Errorf("conflict.Kind = %q, want DaemonSet", conflict.Kind)
	}
}

func TestAgentUninstall_RemovesOwnResources(t *testing.T) {
	cs := fake.NewSimpleClientset()
	raw, _ := json.Marshal(AgentInstallConfig{BackendURL: "x:9090"})
	inst := NewAgent().(Installable)
	if err := inst.Install(context.Background(), cs, raw); err != nil {
		t.Fatalf("install failed: %v", err)
	}

	if err := inst.Uninstall(context.Background(), cs, UninstallOptions{}); err != nil {
		t.Fatalf("uninstall failed: %v", err)
	}

	// DaemonSet + RBAC should be gone; namespace intentionally kept.
	if _, err := cs.AppsV1().DaemonSets(agentNamespace).Get(context.Background(), agentDSName, metav1.GetOptions{}); err == nil {
		t.Error("DaemonSet still present after uninstall")
	}
	if _, err := cs.RbacV1().ClusterRoles().Get(context.Background(), agentMetricsClusterRole, metav1.GetOptions{}); err == nil {
		t.Error("ClusterRole still present after uninstall")
	}
	if _, err := cs.RbacV1().ClusterRoleBindings().Get(context.Background(), agentMetricsClusterBinding, metav1.GetOptions{}); err == nil {
		t.Error("ClusterRoleBinding still present after uninstall")
	}
	if _, err := cs.CoreV1().Namespaces().Get(context.Background(), agentNamespace, metav1.GetOptions{}); err != nil {
		t.Errorf("namespace was deleted (should have been kept): %v", err)
	}
}

func TestAgentUninstall_LeavesExternalInstallAlone(t *testing.T) {
	// A DaemonSet labeled managed-by=Helm (external install). Plus
	// a ClusterRole that happens to share the same name as ours but
	// is also managed by Helm.
	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: agentNamespace}},
		&appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: agentDSName, Namespace: agentNamespace,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "Helm",
					"app.kubernetes.io/name":       "kubebolt-agent",
				},
			},
		},
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: agentMetricsClusterRole,
				Labels: map[string]string{"app.kubernetes.io/managed-by": "Helm"},
			},
		},
	)
	err := NewAgent().(Installable).Uninstall(context.Background(), cs, UninstallOptions{})
	// Default (non-force) uninstall reports this explicitly so the
	// UI can render the "confirm force uninstall" flow.
	var notManaged *NotManagedError
	if !errors.As(err, &notManaged) {
		t.Fatalf("expected *NotManagedError, got %T: %v", err, err)
	}
	// Everything should still be there.
	if _, err := cs.AppsV1().DaemonSets(agentNamespace).Get(context.Background(), agentDSName, metav1.GetOptions{}); err != nil {
		t.Error("external DaemonSet was deleted — should have been preserved")
	}
	if _, err := cs.RbacV1().ClusterRoles().Get(context.Background(), agentMetricsClusterRole, metav1.GetOptions{}); err != nil {
		t.Error("external ClusterRole was deleted — should have been preserved")
	}
}

func TestAgentUninstall_NoOpWhenNothingInstalled(t *testing.T) {
	cs := fake.NewSimpleClientset()
	if err := NewAgent().(Installable).Uninstall(context.Background(), cs, UninstallOptions{}); err != nil {
		t.Fatalf("uninstall should be no-op on empty cluster, got: %v", err)
	}
}

func TestAgentUninstall_ForceRemovesExternalInstall(t *testing.T) {
	// The agent is installed via Helm. Real Helm-managed agents
	// carry both app.kubernetes.io/name=kubebolt-agent (matches our
	// detection selector) AND app.kubernetes.io/managed-by=Helm
	// (distinct from our managed-by=kubebolt).
	// With force=true, KubeBolt should still remove its workloads —
	// the admin has explicitly confirmed they know what they're doing.
	helmLabels := map[string]string{
		"app.kubernetes.io/name":       "kubebolt-agent",
		"app.kubernetes.io/managed-by": "Helm",
	}
	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: agentNamespace}},
		&appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: agentDSName, Namespace: agentNamespace,
				Labels: helmLabels,
			},
		},
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{Name: agentMetricsClusterRole, Labels: helmLabels},
		},
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: agentMetricsClusterBinding, Labels: helmLabels},
		},
	)

	if err := NewAgent().(Installable).Uninstall(context.Background(), cs, UninstallOptions{Force: true}); err != nil {
		t.Fatalf("force uninstall failed: %v", err)
	}

	// DS + cluster-scoped RBAC all gone despite the external label.
	if _, err := cs.AppsV1().DaemonSets(agentNamespace).Get(context.Background(), agentDSName, metav1.GetOptions{}); err == nil {
		t.Error("DaemonSet still present after force uninstall")
	}
	if _, err := cs.RbacV1().ClusterRoles().Get(context.Background(), agentMetricsClusterRole, metav1.GetOptions{}); err == nil {
		t.Error("ClusterRole still present after force uninstall")
	}
	if _, err := cs.RbacV1().ClusterRoleBindings().Get(context.Background(), agentMetricsClusterBinding, metav1.GetOptions{}); err == nil {
		t.Error("ClusterRoleBinding still present after force uninstall")
	}
}

func TestAgentInstall_NodeSelectorAndPriorityClass(t *testing.T) {
	cs := fake.NewSimpleClientset()
	cfg := AgentInstallConfig{
		BackendURL:        "x:9090",
		NodeSelector:      map[string]string{"node-role.kubernetes.io/worker": "true"},
		PriorityClassName: "system-cluster-critical",
	}
	raw, _ := json.Marshal(cfg)
	if err := NewAgent().(Installable).Install(context.Background(), cs, raw); err != nil {
		t.Fatalf("install failed: %v", err)
	}
	ds, _ := cs.AppsV1().DaemonSets(agentNamespace).Get(context.Background(), agentDSName, metav1.GetOptions{})
	if ds.Spec.Template.Spec.NodeSelector["node-role.kubernetes.io/worker"] != "true" {
		t.Errorf("nodeSelector not applied, got %v", ds.Spec.Template.Spec.NodeSelector)
	}
	if ds.Spec.Template.Spec.PriorityClassName != "system-cluster-critical" {
		t.Errorf("priorityClassName = %q, want system-cluster-critical", ds.Spec.Template.Spec.PriorityClassName)
	}
}

func TestAgentInstall_ResourcesOverride(t *testing.T) {
	cs := fake.NewSimpleClientset()
	cfg := AgentInstallConfig{
		BackendURL: "x:9090",
		Resources: &AgentResourceConfig{
			CPURequest:    "50m",
			MemoryRequest: "64Mi",
			CPULimit:      "500m",
			MemoryLimit:   "256Mi",
		},
	}
	raw, _ := json.Marshal(cfg)
	if err := NewAgent().(Installable).Install(context.Background(), cs, raw); err != nil {
		t.Fatalf("install failed: %v", err)
	}
	ds, _ := cs.AppsV1().DaemonSets(agentNamespace).Get(context.Background(), agentDSName, metav1.GetOptions{})
	res := ds.Spec.Template.Spec.Containers[0].Resources
	if res.Requests.Cpu().String() != "50m" {
		t.Errorf("cpu request = %s, want 50m", res.Requests.Cpu())
	}
	if res.Requests.Memory().String() != "64Mi" {
		t.Errorf("mem request = %s, want 64Mi", res.Requests.Memory())
	}
	if res.Limits.Cpu().String() != "500m" {
		t.Errorf("cpu limit = %s, want 500m", res.Limits.Cpu())
	}
	if res.Limits.Memory().String() != "256Mi" {
		t.Errorf("mem limit = %s, want 256Mi", res.Limits.Memory())
	}
}

func TestAgentInstall_InvalidResourceQuantity(t *testing.T) {
	cs := fake.NewSimpleClientset()
	cfg := AgentInstallConfig{
		BackendURL: "x:9090",
		Resources:  &AgentResourceConfig{CPURequest: "not-a-quantity"},
	}
	raw, _ := json.Marshal(cfg)
	err := NewAgent().(Installable).Install(context.Background(), cs, raw)
	if err == nil {
		t.Fatal("expected parse error for invalid CPU request, got nil")
	}
	// Install must not have created anything when validation fails.
	if _, err := cs.AppsV1().DaemonSets(agentNamespace).Get(context.Background(), agentDSName, metav1.GetOptions{}); err == nil {
		t.Error("DaemonSet was created despite validation error")
	}
}

func TestAgentInstall_HubbleMTLS(t *testing.T) {
	// Pre-create the Secret so install passes its existence check.
	cs := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "hubble-client-tls", Namespace: agentNamespace},
	})
	cfg := AgentInstallConfig{
		BackendURL: "x:9090",
		HubbleRelayTLS: &HubbleRelayTLSConfig{
			ExistingSecret: "hubble-client-tls",
			ServerName:     "*.hubble-relay.cilium.io",
		},
		HubbleRelayAddress: "hubble-relay.kube-system:4245",
	}
	raw, _ := json.Marshal(cfg)
	if err := NewAgent().(Installable).Install(context.Background(), cs, raw); err != nil {
		t.Fatalf("install failed: %v", err)
	}
	ds, _ := cs.AppsV1().DaemonSets(agentNamespace).Get(context.Background(), agentDSName, metav1.GetOptions{})

	// Volume + mount wired up.
	foundVol := false
	for _, v := range ds.Spec.Template.Spec.Volumes {
		if v.Name == "hubble-tls" && v.Secret != nil && v.Secret.SecretName == "hubble-client-tls" {
			foundVol = true
		}
	}
	if !foundVol {
		t.Error("hubble-tls volume not wired to the Secret")
	}
	foundMount := false
	for _, vm := range ds.Spec.Template.Spec.Containers[0].VolumeMounts {
		if vm.Name == "hubble-tls" && vm.MountPath == "/etc/hubble-tls" {
			foundMount = true
		}
	}
	if !foundMount {
		t.Error("hubble-tls mount missing from agent container")
	}

	// Env vars point at the mounted files + override address/SNI.
	env := map[string]string{}
	for _, e := range ds.Spec.Template.Spec.Containers[0].Env {
		if e.ValueFrom == nil {
			env[e.Name] = e.Value
		}
	}
	for _, k := range []string{"KUBEBOLT_HUBBLE_RELAY_CA_FILE", "KUBEBOLT_HUBBLE_RELAY_CERT_FILE", "KUBEBOLT_HUBBLE_RELAY_KEY_FILE"} {
		if env[k] == "" {
			t.Errorf("env %s not set", k)
		}
	}
	if env["KUBEBOLT_HUBBLE_RELAY_SERVER_NAME"] != "*.hubble-relay.cilium.io" {
		t.Errorf("relay SNI = %q, want *.hubble-relay.cilium.io", env["KUBEBOLT_HUBBLE_RELAY_SERVER_NAME"])
	}
	if env["KUBEBOLT_HUBBLE_RELAY_ADDR"] != "hubble-relay.kube-system:4245" {
		t.Errorf("relay address = %q, want hubble-relay.kube-system:4245", env["KUBEBOLT_HUBBLE_RELAY_ADDR"])
	}
}

func TestAgentInstall_HubbleMTLS_MissingSecretFailsFast(t *testing.T) {
	// No secret pre-created — install should fail before creating
	// the DaemonSet so pods don't crash-loop on mount.
	cs := fake.NewSimpleClientset()
	cfg := AgentInstallConfig{
		BackendURL:     "x:9090",
		HubbleRelayTLS: &HubbleRelayTLSConfig{ExistingSecret: "missing-secret"},
	}
	raw, _ := json.Marshal(cfg)
	err := NewAgent().(Installable).Install(context.Background(), cs, raw)
	if err == nil {
		t.Fatal("expected missing-secret error, got nil")
	}
	if _, err := cs.AppsV1().DaemonSets(agentNamespace).Get(context.Background(), agentDSName, metav1.GetOptions{}); err == nil {
		t.Error("DaemonSet was created despite missing TLS Secret")
	}
}

func TestAgentInstall_ImagePullPolicy(t *testing.T) {
	cs := fake.NewSimpleClientset()
	cfg := AgentInstallConfig{BackendURL: "x:9090", ImagePullPolicy: "Never"}
	raw, _ := json.Marshal(cfg)
	if err := NewAgent().(Installable).Install(context.Background(), cs, raw); err != nil {
		t.Fatalf("install failed: %v", err)
	}
	ds, _ := cs.AppsV1().DaemonSets(agentNamespace).Get(context.Background(), agentDSName, metav1.GetOptions{})
	if ds.Spec.Template.Spec.Containers[0].ImagePullPolicy != corev1.PullNever {
		t.Errorf("image pull policy = %q, want Never", ds.Spec.Template.Spec.Containers[0].ImagePullPolicy)
	}
}
