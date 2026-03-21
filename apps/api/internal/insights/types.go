package insights

import (
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	corev1 "k8s.io/api/core/v1"

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
	Pods        []*corev1.Pod
	Deployments []*appsv1.Deployment
	Nodes       []*corev1.Node
	HPAs        []*autoscalingv1.HorizontalPodAutoscaler
	PVCs        []*corev1.PersistentVolumeClaim
	Events      []*corev1.Event
	PodMetrics  map[string]*models.MetricPoint
	NodeMetrics map[string]*models.MetricPoint
}
