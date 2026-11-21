package cluster

import (
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/kubebolt/kubebolt/apps/api/internal/agentversion"
)

// agentInstallLocked reads how a cluster's agent was installed from that
// cluster's live informer cache: the release and namespace its upgrade command
// must name, and whether it reads OpenCost twice. No new connection — nil when
// this process has no live connector for the cluster, or the agent's
// workloads cannot be told. m.mu held (read).
func (m *Manager) agentInstallLocked(ctxName string, isActive bool) *agentversion.Install {
	conn := m.resolveConnectorLocked(ctxName, isActive)
	if conn == nil {
		return nil
	}
	return conn.agentInstall()
}

func (c *Connector) agentInstall() *agentversion.Install {
	sel := labels.SelectorFromSet(labels.Set{"app.kubernetes.io/name": "kubebolt-agent"})
	var dss []*appsv1.DaemonSet
	var deps []*appsv1.Deployment
	if c.daemonSetLister != nil {
		dss, _ = c.daemonSetLister.List(sel)
	}
	if c.deploymentLister != nil {
		deps, _ = c.deploymentLister.List(sel)
	}
	return agentInstallFrom(dss, deps)
}

// agentInstallFrom names the agent's Helm release — its workloads'
// `app.kubernetes.io/instance` — and namespace. Seen live: Production AKS's
// release is kb-agent-promread, yagan's kubebolt-agent-ee in kubebolt-agent;
// the wizard's default names fit neither. nil when there is no agent, or more
// than one release: which one to upgrade is not this function's to guess.
//
// TwoCostSources is the combination the chart refuses from 1.4.10: the
// promread Deployment reads cost from Prometheus (KUBEBOLT_AGENT_PROMREAD_COST)
// while the agent also scrapes OpenCost itself (an `opencost=` exporter).
func agentInstallFrom(dss []*appsv1.DaemonSet, deps []*appsv1.Deployment) *agentversion.Install {
	type key struct{ release, namespace string }
	found := map[key]bool{}
	exporter := map[key]bool{}
	promReadCost := map[key]bool{}
	note := func(objLabels map[string]string, namespace string, containers []corev1.Container) {
		k := key{objLabels["app.kubernetes.io/instance"], namespace}
		if k.release == "" {
			return
		}
		found[k] = true
		for _, c := range containers {
			for _, e := range c.Env {
				switch e.Name {
				case "KUBEBOLT_AGENT_EXPORTERS":
					for _, entry := range strings.Split(e.Value, ",") {
						if strings.HasPrefix(strings.TrimSpace(entry), "opencost=") {
							exporter[k] = true
						}
					}
				case "KUBEBOLT_AGENT_PROMREAD_COST":
					if strings.EqualFold(strings.TrimSpace(e.Value), "true") {
						promReadCost[k] = true
					}
				}
			}
		}
	}
	for _, ds := range dss {
		note(ds.Labels, ds.Namespace, ds.Spec.Template.Spec.Containers)
	}
	for _, d := range deps {
		note(d.Labels, d.Namespace, d.Spec.Template.Spec.Containers)
	}
	if len(found) != 1 {
		return nil
	}
	for k := range found {
		return &agentversion.Install{Release: k.release, Namespace: k.namespace, TwoCostSources: exporter[k] && promReadCost[k]}
	}
	return nil
}
