package cluster

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"

	"github.com/kubebolt/kubebolt/apps/api/internal/models"
)

// BuildEdges computes all topology edges from the current informer caches.
func (c *Connector) BuildEdges() []models.TopologyEdge {
	var edges []models.TopologyEdge
	seen := make(map[string]bool)

	addEdge := func(source, target, edgeType string) {
		key := source + "|" + target + "|" + edgeType
		if seen[key] {
			return
		}
		seen[key] = true
		edges = append(edges, models.TopologyEdge{
			ID:     fmt.Sprintf("%s-%s-%s", source, target, edgeType),
			Source: source,
			Target: target,
			Type:   edgeType,
		})
	}

	// ensureRefNode adds a minimal TopologyNode for a resource referenced
	// by an edge but not listed by the primary informers — ConfigMap and
	// Secret today. Without this the edge would be dropped client-side
	// because its target doesn't resolve to any node on the map. AddNode
	// is idempotent so calling it repeatedly for the same ID is fine.
	ensureRefNode := func(kind, namespace, name string) {
		c.graph.AddNode(models.TopologyNode{
			ID:        nodeID(kind, namespace, name),
			Type:      kind,
			Name:      name,
			Namespace: namespace,
		})
	}

	var pods []*corev1.Pod
	if c.podLister != nil {
		pods, _ = c.podLister.List(everythingSelector())
	}
	var services []*corev1.Service
	if c.serviceLister != nil {
		services, _ = c.serviceLister.List(everythingSelector())
	}
	var deployments []*appsv1.Deployment
	if c.deploymentLister != nil {
		deployments, _ = c.deploymentLister.List(everythingSelector())
	}
	var replicaSets []*appsv1.ReplicaSet
	if c.replicaSetLister != nil {
		replicaSets, _ = c.replicaSetLister.List(everythingSelector())
	}
	var statefulSets []*appsv1.StatefulSet
	if c.statefulSetLister != nil {
		statefulSets, _ = c.statefulSetLister.List(everythingSelector())
	}
	var daemonSets []*appsv1.DaemonSet
	if c.daemonSetLister != nil {
		daemonSets, _ = c.daemonSetLister.List(everythingSelector())
	}
	var jobs []*batchv1.Job
	if c.jobLister != nil {
		jobs, _ = c.jobLister.List(everythingSelector())
	}
	var cronJobs []*batchv1.CronJob
	if c.cronJobLister != nil {
		cronJobs, _ = c.cronJobLister.List(everythingSelector())
	}
	var ingresses []*networkingv1.Ingress
	if c.ingressLister != nil {
		ingresses, _ = c.ingressLister.List(everythingSelector())
	}
	var hpas []*autoscalingv1.HorizontalPodAutoscaler
	if c.hpaLister != nil {
		hpas, _ = c.hpaLister.List(everythingSelector())
	}

	// Pod ownerReferences
	for _, pod := range pods {
		podID := nodeID("Pod", pod.Namespace, pod.Name)
		for _, ref := range pod.OwnerReferences {
			ownerID := nodeID(ref.Kind, pod.Namespace, ref.Name)
			addEdge(ownerID, podID, "owns")
		}
		// Pod -> PVC
		for _, vol := range pod.Spec.Volumes {
			if vol.PersistentVolumeClaim != nil {
				pvcID := nodeID("PersistentVolumeClaim", pod.Namespace, vol.PersistentVolumeClaim.ClaimName)
				addEdge(podID, pvcID, "uses")
			}
			// Pod -> ConfigMap via volumes
			if vol.ConfigMap != nil {
				ensureRefNode("ConfigMap", pod.Namespace, vol.ConfigMap.Name)
				cmID := nodeID("ConfigMap", pod.Namespace, vol.ConfigMap.Name)
				addEdge(podID, cmID, "mounts")
			}
			// Pod -> Secret via volumes
			if vol.Secret != nil {
				ensureRefNode("Secret", pod.Namespace, vol.Secret.SecretName)
				secID := nodeID("Secret", pod.Namespace, vol.Secret.SecretName)
				addEdge(podID, secID, "mounts")
			}
			// Pod -> ConfigMap/Secret via projected sources (K8s 1.24+ mounts the
			// default ServiceAccount token + CA cert this way — a modern pod has
			// no top-level vol.ConfigMap/vol.Secret at all).
			if vol.Projected != nil {
				for _, src := range vol.Projected.Sources {
					if src.ConfigMap != nil {
						ensureRefNode("ConfigMap", pod.Namespace, src.ConfigMap.Name)
						cmID := nodeID("ConfigMap", pod.Namespace, src.ConfigMap.Name)
						addEdge(podID, cmID, "mounts")
					}
					if src.Secret != nil {
						ensureRefNode("Secret", pod.Namespace, src.Secret.Name)
						secID := nodeID("Secret", pod.Namespace, src.Secret.Name)
						addEdge(podID, secID, "mounts")
					}
				}
			}
		}
		// Pod -> ConfigMap/Secret via envFrom (whole-resource import) and
		// env[].valueFrom (single-key import). Both patterns are common in
		// the wild; we treat them the same kind of edge since the
		// distinction rarely matters on a cluster map.
		for _, container := range pod.Spec.Containers {
			for _, envFrom := range container.EnvFrom {
				if envFrom.ConfigMapRef != nil {
					ensureRefNode("ConfigMap", pod.Namespace, envFrom.ConfigMapRef.Name)
					cmID := nodeID("ConfigMap", pod.Namespace, envFrom.ConfigMapRef.Name)
					addEdge(podID, cmID, "envFrom")
				}
				if envFrom.SecretRef != nil {
					ensureRefNode("Secret", pod.Namespace, envFrom.SecretRef.Name)
					secID := nodeID("Secret", pod.Namespace, envFrom.SecretRef.Name)
					addEdge(podID, secID, "envFrom")
				}
			}
			for _, env := range container.Env {
				if env.ValueFrom == nil {
					continue
				}
				if env.ValueFrom.ConfigMapKeyRef != nil {
					ensureRefNode("ConfigMap", pod.Namespace, env.ValueFrom.ConfigMapKeyRef.Name)
					cmID := nodeID("ConfigMap", pod.Namespace, env.ValueFrom.ConfigMapKeyRef.Name)
					addEdge(podID, cmID, "envFrom")
				}
				if env.ValueFrom.SecretKeyRef != nil {
					ensureRefNode("Secret", pod.Namespace, env.ValueFrom.SecretKeyRef.Name)
					secID := nodeID("Secret", pod.Namespace, env.ValueFrom.SecretKeyRef.Name)
					addEdge(podID, secID, "envFrom")
				}
			}
		}
		// Pod -> Secret via imagePullSecrets
		for _, ips := range pod.Spec.ImagePullSecrets {
			ensureRefNode("Secret", pod.Namespace, ips.Name)
			secID := nodeID("Secret", pod.Namespace, ips.Name)
			addEdge(podID, secID, "imagePull")
		}
	}

	// ReplicaSet ownerReferences (Deployment -> ReplicaSet)
	for _, rs := range replicaSets {
		rsID := nodeID("ReplicaSet", rs.Namespace, rs.Name)
		for _, ref := range rs.OwnerReferences {
			ownerID := nodeID(ref.Kind, rs.Namespace, ref.Name)
			addEdge(ownerID, rsID, "owns")
		}
	}

	// Job ownerReferences (CronJob -> Job)
	for _, job := range jobs {
		jobID := nodeID("Job", job.Namespace, job.Name)
		for _, ref := range job.OwnerReferences {
			ownerID := nodeID(ref.Kind, job.Namespace, ref.Name)
			addEdge(ownerID, jobID, "owns")
		}
	}

	// Service -> Pod (selector match)
	for _, svc := range services {
		if len(svc.Spec.Selector) == 0 {
			continue
		}
		svcID := nodeID("Service", svc.Namespace, svc.Name)
		for _, pod := range pods {
			if pod.Namespace != svc.Namespace {
				continue
			}
			if matchLabels(svc.Spec.Selector, pod.Labels) {
				podID := nodeID("Pod", pod.Namespace, pod.Name)
				addEdge(svcID, podID, "selects")
			}
		}
	}

	// Ingress -> Service
	for _, ing := range ingresses {
		ingID := nodeID("Ingress", ing.Namespace, ing.Name)
		for _, rule := range ing.Spec.Rules {
			if rule.HTTP == nil {
				continue
			}
			for _, path := range rule.HTTP.Paths {
				if path.Backend.Service != nil {
					svcID := nodeID("Service", ing.Namespace, path.Backend.Service.Name)
					addEdge(ingID, svcID, "routes")
				}
			}
		}
	}

	// PVC -> PV
	var pvcs []*corev1.PersistentVolumeClaim
	if c.pvcLister != nil {
		pvcs, _ = c.pvcLister.List(everythingSelector())
	}
	for _, pvc := range pvcs {
		if pvc.Spec.VolumeName != "" {
			pvcID := nodeID("PersistentVolumeClaim", pvc.Namespace, pvc.Name)
			pvID := nodeID("PersistentVolume", "", pvc.Spec.VolumeName)
			addEdge(pvcID, pvID, "bound")
		}
	}

	// HPA -> target
	for _, hpa := range hpas {
		hpaID := nodeID("HPA", hpa.Namespace, hpa.Name)
		targetID := nodeID(hpa.Spec.ScaleTargetRef.Kind, hpa.Namespace, hpa.Spec.ScaleTargetRef.Name)
		addEdge(hpaID, targetID, "hpa")
	}

	// Gateway API edges (dynamic)
	c.buildGatewayEdges(addEdge)

	// Reference deployments, statefulSets, daemonSets, cronJobs to suppress unused warnings
	_ = deployments
	_ = statefulSets
	_ = daemonSets
	_ = cronJobs

	return edges
}

func (c *Connector) buildGatewayEdges(addEdge func(source, target, edgeType string)) {
	if c.dynamicClient == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	hrGVR := schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}
	hrList, err := c.dynamicClient.Resource(hrGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return
	}
	for _, item := range hrList.Items {
		hrID := nodeID("HTTPRoute", item.GetNamespace(), item.GetName())
		spec, _ := item.Object["spec"].(map[string]interface{})
		if spec == nil {
			continue
		}
		// HTTPRoute -> Gateway (parentRef)
		if parentRefs, ok := spec["parentRefs"].([]interface{}); ok {
			for _, pr := range parentRefs {
				if prm, ok := pr.(map[string]interface{}); ok {
					gwName, _ := prm["name"].(string)
					gwNS, _ := prm["namespace"].(string)
					if gwNS == "" {
						gwNS = item.GetNamespace()
					}
					if gwName != "" {
						gwID := nodeID("Gateway", gwNS, gwName)
						addEdge(gwID, hrID, "routes")
					}
				}
			}
		}
		// HTTPRoute -> Service (backendRefs)
		if rules, ok := spec["rules"].([]interface{}); ok {
			for _, rule := range rules {
				if rm, ok := rule.(map[string]interface{}); ok {
					if brs, ok := rm["backendRefs"].([]interface{}); ok {
						for _, br := range brs {
							if brm, ok := br.(map[string]interface{}); ok {
								svcName, _ := brm["name"].(string)
								if svcName != "" {
									svcID := nodeID("Service", item.GetNamespace(), svcName)
									addEdge(hrID, svcID, "routes")
								}
							}
						}
					}
				}
			}
		}
	}
}

func nodeID(kind, namespace, name string) string {
	if namespace == "" {
		return fmt.Sprintf("%s/%s", kind, name)
	}
	return fmt.Sprintf("%s/%s/%s", kind, namespace, name)
}

func matchLabels(selector, labels map[string]string) bool {
	for k, v := range selector {
		if labels[k] != v {
			return false
		}
	}
	return true
}

// Compile-time check that types are used (suppresses import errors).
var (
	_ *appsv1.Deployment
	_ *appsv1.StatefulSet
	_ *appsv1.DaemonSet
	_ *batchv1.Job
	_ *batchv1.CronJob
	_ *corev1.Pod
	_ *corev1.Service
	_ *corev1.PersistentVolumeClaim
	_ *networkingv1.Ingress
	_ *autoscalingv1.HorizontalPodAutoscaler
)
