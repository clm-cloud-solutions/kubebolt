package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// Install then GetConfig should round-trip the values we put in.
// Anything dropped here is a gap in the reconstruction logic the
// UI would see as a blank field on first load.
func TestAgentGetConfig_RoundTripAfterInstall(t *testing.T) {
	cs := fake.NewSimpleClientset()
	hubbleOff := false
	in := AgentInstallConfig{
		BackendURL:        "host.docker.internal:9090",
		ClusterName:       "kind-dev",
		HubbleEnabled:     &hubbleOff,
		ImageRepo:         "kubebolt-agent",
		ImageTag:          "dev-1",
		ImagePullPolicy:   "Never",
		LogLevel:          "debug",
		PriorityClassName: "system-cluster-critical",
		NodeSelector:      map[string]string{"disktype": "ssd"},
		Resources: &AgentResourceConfig{
			CPURequest: "50m", MemoryRequest: "64Mi",
			CPULimit: "500m", MemoryLimit: "256Mi",
		},
	}
	raw, _ := json.Marshal(in)
	if err := NewAgent().(Installable).Install(context.Background(), cs, raw); err != nil {
		t.Fatalf("install failed: %v", err)
	}

	out, err := NewAgent().(Configurable).GetConfig(context.Background(), cs)
	if err != nil {
		t.Fatalf("getConfig failed: %v", err)
	}
	var got AgentInstallConfig
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.BackendURL != in.BackendURL {
		t.Errorf("backendUrl: got %q want %q", got.BackendURL, in.BackendURL)
	}
	if got.ClusterName != in.ClusterName {
		t.Errorf("clusterName: got %q want %q", got.ClusterName, in.ClusterName)
	}
	if got.HubbleEnabled == nil || *got.HubbleEnabled != false {
		t.Errorf("hubbleEnabled: got %v want false", got.HubbleEnabled)
	}
	if got.ImageRepo != in.ImageRepo || got.ImageTag != in.ImageTag {
		t.Errorf("image: got %s:%s want %s:%s", got.ImageRepo, got.ImageTag, in.ImageRepo, in.ImageTag)
	}
	if got.ImagePullPolicy != in.ImagePullPolicy {
		t.Errorf("imagePullPolicy: got %q want %q", got.ImagePullPolicy, in.ImagePullPolicy)
	}
	if got.LogLevel != in.LogLevel {
		t.Errorf("logLevel: got %q want %q", got.LogLevel, in.LogLevel)
	}
	if got.PriorityClassName != in.PriorityClassName {
		t.Errorf("priorityClassName: got %q want %q", got.PriorityClassName, in.PriorityClassName)
	}
	if got.NodeSelector["disktype"] != "ssd" {
		t.Errorf("nodeSelector: got %v", got.NodeSelector)
	}
	if got.Resources == nil || got.Resources.CPURequest != "50m" || got.Resources.MemoryLimit != "256Mi" {
		t.Errorf("resources not preserved: %+v", got.Resources)
	}
	if got.Namespace != agentNamespace {
		t.Errorf("namespace: got %q want %q", got.Namespace, agentNamespace)
	}
}

func TestAgentGetConfig_NotInstalled(t *testing.T) {
	cs := fake.NewSimpleClientset()
	_, err := NewAgent().(Configurable).GetConfig(context.Background(), cs)
	var ni *NotInstalledError
	if !errors.As(err, &ni) {
		t.Fatalf("expected *NotInstalledError, got %T: %v", err, err)
	}
}

func TestAgentGetConfig_RefusesExternal(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: agentNamespace}},
		&appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: agentDSName, Namespace: agentNamespace,
				Labels: map[string]string{
					"app.kubernetes.io/name":       "kubebolt-agent",
					"app.kubernetes.io/managed-by": "Helm",
				},
			},
		},
	)
	_, err := NewAgent().(Configurable).GetConfig(context.Background(), cs)
	var nm *NotManagedError
	if !errors.As(err, &nm) {
		t.Fatalf("expected *NotManagedError, got %T: %v", err, err)
	}
}

// Configure on a managed install updates env + scheduling. Verifies
// the "fill in missing cluster name" case the user ran into.
func TestAgentConfigure_UpdatesEnv(t *testing.T) {
	cs := fake.NewSimpleClientset()
	// Install with no cluster name set — matches the bug the user
	// hit when the UI wizard left the field blank.
	raw, _ := json.Marshal(AgentInstallConfig{BackendURL: "x:9090"})
	if err := NewAgent().(Installable).Install(context.Background(), cs, raw); err != nil {
		t.Fatalf("install failed: %v", err)
	}

	// Now configure: set the cluster name and switch pull policy.
	update := AgentInstallConfig{
		BackendURL:      "x:9090",
		ClusterName:     "kind-kubebolt-dev",
		ImagePullPolicy: "Never",
	}
	raw, _ = json.Marshal(update)
	if err := NewAgent().(Configurable).Configure(context.Background(), cs, raw); err != nil {
		t.Fatalf("configure failed: %v", err)
	}

	ds, _ := cs.AppsV1().DaemonSets(agentNamespace).Get(context.Background(), agentDSName, metav1.GetOptions{})
	env := map[string]string{}
	for _, e := range ds.Spec.Template.Spec.Containers[0].Env {
		if e.ValueFrom == nil {
			env[e.Name] = e.Value
		}
	}
	if env["KUBEBOLT_AGENT_CLUSTER_NAME"] != "kind-kubebolt-dev" {
		t.Errorf("cluster name not updated: %q", env["KUBEBOLT_AGENT_CLUSTER_NAME"])
	}
	if ds.Spec.Template.Spec.Containers[0].ImagePullPolicy != corev1.PullNever {
		t.Errorf("pull policy not updated: %q", ds.Spec.Template.Spec.Containers[0].ImagePullPolicy)
	}
}

func TestAgentConfigure_RequiresInstalled(t *testing.T) {
	cs := fake.NewSimpleClientset()
	raw, _ := json.Marshal(AgentInstallConfig{BackendURL: "x:9090"})
	err := NewAgent().(Configurable).Configure(context.Background(), cs, raw)
	var ni *NotInstalledError
	if !errors.As(err, &ni) {
		t.Fatalf("expected *NotInstalledError, got %T: %v", err, err)
	}
}

func TestAgentConfigure_RefusesExternal(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: agentNamespace}},
		&appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: agentDSName, Namespace: agentNamespace,
				Labels: map[string]string{
					"app.kubernetes.io/name":       "kubebolt-agent",
					"app.kubernetes.io/managed-by": "Helm",
				},
			},
		},
	)
	raw, _ := json.Marshal(AgentInstallConfig{BackendURL: "x:9090"})
	err := NewAgent().(Configurable).Configure(context.Background(), cs, raw)
	var nm *NotManagedError
	if !errors.As(err, &nm) {
		t.Fatalf("expected *NotManagedError, got %T: %v", err, err)
	}
}

// Configure requires backendUrl just like Install; empty string is
// rejected with a validation error rather than silently clearing
// the env var on the DaemonSet.
func TestAgentConfigure_RequiresBackendURL(t *testing.T) {
	cs := fake.NewSimpleClientset()
	raw, _ := json.Marshal(AgentInstallConfig{BackendURL: "x:9090"})
	if err := NewAgent().(Installable).Install(context.Background(), cs, raw); err != nil {
		t.Fatalf("install failed: %v", err)
	}
	// Submit a configure payload without backendUrl.
	raw, _ = json.Marshal(AgentInstallConfig{ClusterName: "foo"})
	err := NewAgent().(Configurable).Configure(context.Background(), cs, raw)
	if err == nil {
		t.Fatal("expected error for missing backendUrl, got nil")
	}
}
