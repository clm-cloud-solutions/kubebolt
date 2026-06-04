package cluster

import (
	"log"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
)

// GetPods returns all pods from the cache.
func (c *Connector) GetPods() []*corev1.Pod {
	if c.podLister == nil {
		return nil
	}
	pods, err := c.podLister.List(everythingSelector())
	if err != nil {
		log.Printf("Error listing pods: %v", err)
		return nil
	}
	return pods
}

// GetDeployments returns all deployments from the cache.
func (c *Connector) GetDeployments() []*appsv1.Deployment {
	if c.deploymentLister == nil {
		return nil
	}
	deployments, err := c.deploymentLister.List(everythingSelector())
	if err != nil {
		log.Printf("Error listing deployments: %v", err)
		return nil
	}
	return deployments
}

// GetNodes returns all nodes from the cache.
func (c *Connector) GetNodes() []*corev1.Node {
	if c.nodeLister == nil {
		return nil
	}
	nodes, err := c.nodeLister.List(everythingSelector())
	if err != nil {
		log.Printf("Error listing nodes: %v", err)
		return nil
	}
	return nodes
}

// GetHPAs returns all HPAs from the cache.
func (c *Connector) GetHPAs() []*autoscalingv1.HorizontalPodAutoscaler {
	if c.hpaLister == nil {
		return nil
	}
	hpas, err := c.hpaLister.List(everythingSelector())
	if err != nil {
		log.Printf("Error listing HPAs: %v", err)
		return nil
	}
	return hpas
}

// GetPVCs returns all PVCs from the cache.
func (c *Connector) GetPVCs() []*corev1.PersistentVolumeClaim {
	if c.pvcLister == nil {
		return nil
	}
	pvcs, err := c.pvcLister.List(everythingSelector())
	if err != nil {
		log.Printf("Error listing PVCs: %v", err)
		return nil
	}
	return pvcs
}

// GetEventsRaw returns all events from the cache.
func (c *Connector) GetEventsRaw() []*corev1.Event {
	if c.eventLister == nil {
		return nil
	}
	events, err := c.eventLister.List(everythingSelector())
	if err != nil {
		log.Printf("Error listing events: %v", err)
		return nil
	}
	return events
}

// GetServices returns all Services from the cache.
func (c *Connector) GetServices() []*corev1.Service {
	if c.serviceLister == nil {
		return nil
	}
	services, err := c.serviceLister.List(everythingSelector())
	if err != nil {
		log.Printf("Error listing services: %v", err)
		return nil
	}
	return services
}

// GetEndpointSlices returns all EndpointSlices from the cache.
func (c *Connector) GetEndpointSlices() []*discoveryv1.EndpointSlice {
	if c.endpointSliceLister == nil {
		return nil
	}
	slices, err := c.endpointSliceLister.List(everythingSelector())
	if err != nil {
		log.Printf("Error listing endpoint slices: %v", err)
		return nil
	}
	return slices
}

// GetNetworkPolicies returns all NetworkPolicies from the cache.
// Used by the insights engine to evaluate policy-coverage rules
// (policy-no-match, policy-orphan).
func (c *Connector) GetNetworkPolicies() []*networkingv1.NetworkPolicy {
	if c.networkPolicyLister == nil {
		return nil
	}
	policies, err := c.networkPolicyLister.List(everythingSelector())
	if err != nil {
		log.Printf("Error listing network policies: %v", err)
		return nil
	}
	return policies
}

// GetPodDisruptionBudgets returns all PDBs from the informer cache (Sprint 3).
func (c *Connector) GetPodDisruptionBudgets() []*policyv1.PodDisruptionBudget {
	if c.pdbLister == nil {
		return nil
	}
	pdbs, err := c.pdbLister.List(everythingSelector())
	if err != nil {
		log.Printf("Error listing pod disruption budgets: %v", err)
		return nil
	}
	return pdbs
}
