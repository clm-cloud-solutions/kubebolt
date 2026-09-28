package cluster

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/kubebolt/kubebolt/apps/api/internal/models"
)

// NamespaceWorkloads is the overview's per-namespace workload list — kind,
// name, and the summed CPU / memory requests and limits of its pods — without
// building the rest of the overview. Right-sizing needs exactly this and
// nothing else. Read from the informer caches; a lister missing for want of
// RBAC contributes nothing rather than failing.
func (c *Connector) NamespaceWorkloads() []models.NamespaceWorkload {
	var (
		pods         []*corev1.Pod
		deployments  []*appsv1.Deployment
		statefulSets []*appsv1.StatefulSet
		daemonSets   []*appsv1.DaemonSet
	)
	if c.podLister != nil {
		pods, _ = c.podLister.List(everythingSelector())
	}
	if c.deploymentLister != nil {
		deployments, _ = c.deploymentLister.List(everythingSelector())
	}
	if c.statefulSetLister != nil {
		statefulSets, _ = c.statefulSetLister.List(everythingSelector())
	}
	if c.daemonSetLister != nil {
		daemonSets, _ = c.daemonSetLister.List(everythingSelector())
	}
	return c.buildNamespaceWorkloads(pods, deployments, statefulSets, daemonSets)
}
