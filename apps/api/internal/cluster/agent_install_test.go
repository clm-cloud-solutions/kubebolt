package cluster

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func agentLabels(release string) map[string]string {
	return map[string]string{"app.kubernetes.io/name": "kubebolt-agent", "app.kubernetes.io/instance": release}
}

func withEnv(env ...corev1.EnvVar) corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "agent", Env: env}}}}
}

func ds(release, ns string, env ...corev1.EnvVar) *appsv1.DaemonSet {
	return &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: release + "-kubebolt-agent", Namespace: ns, Labels: agentLabels(release)},
		Spec: appsv1.DaemonSetSpec{Template: withEnv(env...)}}
}

func dep(release, ns string, env ...corev1.EnvVar) *appsv1.Deployment {
	return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: release + "-promread", Namespace: ns, Labels: agentLabels(release)},
		Spec: appsv1.DeploymentSpec{Template: withEnv(env...)}}
}

// The two installs seen live on 2026-11-21, whose upgrade the default command
// could not do as written.
func TestAgentInstallFrom_TheReleaseTheClusterHas(t *testing.T) {
	// Production AKS: release kb-agent-promread, promRead.cost, no exporter.
	aks := agentInstallFrom(
		[]*appsv1.DaemonSet{ds("kb-agent-promread", "kubebolt-system")},
		[]*appsv1.Deployment{dep("kb-agent-promread", "kubebolt-system", corev1.EnvVar{Name: "KUBEBOLT_AGENT_PROMREAD_COST", Value: "true"})},
	)
	if aks == nil || aks.Release != "kb-agent-promread" || aks.Namespace != "kubebolt-system" || aks.TwoCostSources {
		t.Fatalf("Production AKS = %+v", aks)
	}
	// yagan: release kubebolt-agent-ee in kubebolt-agent, the bundled OpenCost
	// wired as an exporter AND promRead.cost — the chart refuses that from 1.4.10.
	exp := corev1.EnvVar{Name: "KUBEBOLT_AGENT_EXPORTERS", Value: "opencost=http://kubebolt-agent-ee-opencost.kubebolt-agent.svc:9003/metrics"}
	yagan := agentInstallFrom(
		[]*appsv1.DaemonSet{ds("kubebolt-agent-ee", "kubebolt-agent", exp)},
		[]*appsv1.Deployment{dep("kubebolt-agent-ee", "kubebolt-agent", exp, corev1.EnvVar{Name: "KUBEBOLT_AGENT_PROMREAD_COST", Value: "true"})},
	)
	if yagan == nil || yagan.Release != "kubebolt-agent-ee" || yagan.Namespace != "kubebolt-agent" || !yagan.TwoCostSources {
		t.Fatalf("yagan = %+v", yagan)
	}
}

// Nothing to name: no agent, no release label, or two releases (which one to
// upgrade is not ours to guess) — the command keeps the defaults.
func TestAgentInstallFrom_NothingToName(t *testing.T) {
	if got := agentInstallFrom(nil, nil); got != nil {
		t.Errorf("no agent = %+v", got)
	}
	unlabeled := ds("x", "ns")
	unlabeled.Labels = map[string]string{"app.kubernetes.io/name": "kubebolt-agent"}
	if got := agentInstallFrom([]*appsv1.DaemonSet{unlabeled}, nil); got != nil {
		t.Errorf("no release label = %+v", got)
	}
	if got := agentInstallFrom([]*appsv1.DaemonSet{ds("a", "ns1"), ds("b", "ns2")}, nil); got != nil {
		t.Errorf("two releases = %+v", got)
	}
}
