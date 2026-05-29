package insights

import (
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"

	"github.com/kubebolt/kubebolt/apps/api/internal/helm"
	"github.com/kubebolt/kubebolt/apps/api/internal/models"
)

// Rule defines an insight evaluation rule.
type Rule struct {
	ID       string
	Name     string
	Severity string // "critical", "warning", "info"
	Evaluate func(state *ClusterState) []models.Insight
}

// ClusterState holds all informer data needed for insight evaluation.
type ClusterState struct {
	Pods            []*corev1.Pod
	Deployments     []*appsv1.Deployment
	Nodes           []*corev1.Node
	HPAs            []*autoscalingv1.HorizontalPodAutoscaler
	PVCs            []*corev1.PersistentVolumeClaim
	Events          []*corev1.Event
	Services        []*corev1.Service
	EndpointSlices  []*discoveryv1.EndpointSlice
	NetworkPolicies []*networkingv1.NetworkPolicy
	PDBs            []*policyv1.PodDisruptionBudget
	HelmReleases    []helm.Release
	PodMetrics      map[string]*models.MetricPoint
	NodeMetrics     map[string]*models.MetricPoint
}
