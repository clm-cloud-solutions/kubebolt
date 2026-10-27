package insights

import (
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	storagev1 "k8s.io/api/storage/v1"

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
	// Workload templates beyond Deployments: pdb-no-match and policy-no-match
	// tell a selector that matches no workload (a typo, a renamed or deleted
	// workload) from one whose workload is scaled to 0 or between CronJob runs.
	StatefulSets []*appsv1.StatefulSet
	DaemonSets   []*appsv1.DaemonSet
	Jobs         []*batchv1.Job
	CronJobs     []*batchv1.CronJob
	// StorageClasses: pvc-pending reads the class's volumeBindingMode.
	StorageClasses []*storagev1.StorageClass
	// Cilium's own policies, for policy-orphan: CiliumNetworkPolicies per
	// namespace and the CiliumClusterwideNetworkPolicy count. Known is false
	// when they could not be read — never taken as "none".
	CiliumPolicies            map[string]int
	CiliumClusterwidePolicies int
	CiliumPoliciesKnown       bool
	HelmReleases              []helm.Release
	// Optional dynamic CRDs (Sprint 3), as generic maps from the dynamic
	// lister — present only when the CRD is installed.
	Certificates []map[string]interface{}
	ArgoApps     []map[string]interface{}
	PodMetrics   map[string]*models.MetricPoint

	// Policies is the org-effective rule policy for THIS evaluation (#44
	// step 1) — attached by Engine.Evaluate from the policy source. nil is
	// always safe: rules fall back to shipped defaults via stateThreshold.
	Policies    *PolicySnapshot
	NodeMetrics map[string]*models.MetricPoint
}
