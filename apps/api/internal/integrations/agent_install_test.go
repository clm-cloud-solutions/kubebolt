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

	cr, _ := cs.RbacV1().ClusterRoles().Get(context.Background(), agentClusterRole, metav1.GetOptions{})
	if !managedByUs(cr.Labels) {
		t.Error("clusterrole missing managed-by label")
	}
	if len(cr.Rules) < 3 {
		t.Errorf("expected >=3 rules on ClusterRole, got %d", len(cr.Rules))
	}

	crb, _ := cs.RbacV1().ClusterRoleBindings().Get(context.Background(), agentClusterBinding, metav1.GetOptions{})
	if crb.RoleRef.Name != agentClusterRole {
		t.Errorf("binding references %q, want %q", crb.RoleRef.Name, agentClusterRole)
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

	if err := inst.Uninstall(context.Background(), cs); err != nil {
		t.Fatalf("uninstall failed: %v", err)
	}

	// DaemonSet + RBAC should be gone; namespace intentionally kept.
	if _, err := cs.AppsV1().DaemonSets(agentNamespace).Get(context.Background(), agentDSName, metav1.GetOptions{}); err == nil {
		t.Error("DaemonSet still present after uninstall")
	}
	if _, err := cs.RbacV1().ClusterRoles().Get(context.Background(), agentClusterRole, metav1.GetOptions{}); err == nil {
		t.Error("ClusterRole still present after uninstall")
	}
	if _, err := cs.RbacV1().ClusterRoleBindings().Get(context.Background(), agentClusterBinding, metav1.GetOptions{}); err == nil {
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
				Name: agentClusterRole,
				Labels: map[string]string{"app.kubernetes.io/managed-by": "Helm"},
			},
		},
	)
	if err := NewAgent().(Installable).Uninstall(context.Background(), cs); err != nil {
		t.Fatalf("uninstall failed: %v", err)
	}
	// Everything should still be there.
	if _, err := cs.AppsV1().DaemonSets(agentNamespace).Get(context.Background(), agentDSName, metav1.GetOptions{}); err != nil {
		t.Error("external DaemonSet was deleted — should have been preserved")
	}
	if _, err := cs.RbacV1().ClusterRoles().Get(context.Background(), agentClusterRole, metav1.GetOptions{}); err != nil {
		t.Error("external ClusterRole was deleted — should have been preserved")
	}
}

func TestAgentUninstall_NoOpWhenNothingInstalled(t *testing.T) {
	cs := fake.NewSimpleClientset()
	if err := NewAgent().(Installable).Uninstall(context.Background(), cs); err != nil {
		t.Fatalf("uninstall should be no-op on empty cluster, got: %v", err)
	}
}
