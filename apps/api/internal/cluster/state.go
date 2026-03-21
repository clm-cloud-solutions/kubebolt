package cluster

import (
	"log"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	corev1 "k8s.io/api/core/v1"
)

// GetPods returns all pods from the cache.
func (c *Connector) GetPods() []*corev1.Pod {
	pods, err := c.podLister.List(everythingSelector())
	if err != nil {
		log.Printf("Error listing pods: %v", err)
		return nil
	}
	return pods
}

// GetDeployments returns all deployments from the cache.
func (c *Connector) GetDeployments() []*appsv1.Deployment {
	deployments, err := c.deploymentLister.List(everythingSelector())
	if err != nil {
		log.Printf("Error listing deployments: %v", err)
		return nil
	}
	return deployments
}

// GetNodes returns all nodes from the cache.
func (c *Connector) GetNodes() []*corev1.Node {
	nodes, err := c.nodeLister.List(everythingSelector())
	if err != nil {
		log.Printf("Error listing nodes: %v", err)
		return nil
	}
	return nodes
}

// GetHPAs returns all HPAs from the cache.
func (c *Connector) GetHPAs() []*autoscalingv1.HorizontalPodAutoscaler {
	hpas, err := c.hpaLister.List(everythingSelector())
	if err != nil {
		log.Printf("Error listing HPAs: %v", err)
		return nil
	}
	return hpas
}

// GetPVCs returns all PVCs from the cache.
func (c *Connector) GetPVCs() []*corev1.PersistentVolumeClaim {
	pvcs, err := c.pvcLister.List(everythingSelector())
	if err != nil {
		log.Printf("Error listing PVCs: %v", err)
		return nil
	}
	return pvcs
}

// GetEventsRaw returns all events from the cache.
func (c *Connector) GetEventsRaw() []*corev1.Event {
	events, err := c.eventLister.List(everythingSelector())
	if err != nil {
		log.Printf("Error listing events: %v", err)
		return nil
	}
	return events
}
