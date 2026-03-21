package cluster

import (
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	corev1 "k8s.io/api/core/v1"
)

// GetPods returns all pods from the cache.
func (c *Connector) GetPods() []*corev1.Pod {
	pods, _ := c.podLister.List(everythingSelector())
	return pods
}

// GetDeployments returns all deployments from the cache.
func (c *Connector) GetDeployments() []*appsv1.Deployment {
	deployments, _ := c.deploymentLister.List(everythingSelector())
	return deployments
}

// GetNodes returns all nodes from the cache.
func (c *Connector) GetNodes() []*corev1.Node {
	nodes, _ := c.nodeLister.List(everythingSelector())
	return nodes
}

// GetHPAs returns all HPAs from the cache.
func (c *Connector) GetHPAs() []*autoscalingv1.HorizontalPodAutoscaler {
	hpas, _ := c.hpaLister.List(everythingSelector())
	return hpas
}

// GetPVCs returns all PVCs from the cache.
func (c *Connector) GetPVCs() []*corev1.PersistentVolumeClaim {
	pvcs, _ := c.pvcLister.List(everythingSelector())
	return pvcs
}

// GetEventsRaw returns all events from the cache.
func (c *Connector) GetEventsRaw() []*corev1.Event {
	events, _ := c.eventLister.List(everythingSelector())
	return events
}
