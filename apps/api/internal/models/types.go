package models

import "time"

// ClusterOverview is the top-level summary returned by GET /cluster/overview
type ClusterOverview struct {
	ClusterName        string              `json:"clusterName"`
	KubernetesVersion  string              `json:"kubernetesVersion"`
	Platform           string              `json:"platform"`
	Nodes              ResourceCount       `json:"nodes"`
	Pods               ResourceCount       `json:"pods"`
	Namespaces         ResourceCount       `json:"namespaces"`
	Services           ResourceCount       `json:"services"`
	Deployments        ResourceCount       `json:"deployments"`
	StatefulSets       ResourceCount       `json:"statefulSets"`
	DaemonSets         ResourceCount       `json:"daemonSets"`
	Jobs               ResourceCount       `json:"jobs"`
	CPU                ResourceUsage       `json:"cpu"`
	Memory             ResourceUsage       `json:"memory"`
	Health             ClusterHealth       `json:"health"`
	Events             []KubeEvent         `json:"events"`
	NamespaceWorkloads []NamespaceWorkload `json:"namespaceWorkloads"`
}

// ResourceCount tracks totals and health for a resource type.
type ResourceCount struct {
	Total    int `json:"total"`
	Ready    int `json:"ready"`
	NotReady int `json:"notReady"`
	Warning  int `json:"warning"`
}

// ResourceUsage tracks requests, limits, usage and capacity for CPU/Memory.
type ResourceUsage struct {
	Used             int64   `json:"used"`
	Requested        int64   `json:"requested"`
	Limit            int64   `json:"limit"`
	Allocatable      int64   `json:"allocatable"`
	PercentUsed      float64 `json:"percentUsed"`
	PercentRequested float64 `json:"percentRequested"`
}

// ClusterHealth is the overall health status.
type ClusterHealth struct {
	Status   string        `json:"status"` // "healthy", "warning", "critical"
	Score    int           `json:"score"`
	Insights InsightCount  `json:"insights"`
	Checks   []HealthCheck `json:"checks"`
}

// HealthCheck represents a single health check result.
type HealthCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"` // pass, warn, fail
	Message string `json:"message"`
}

// InsightCount counts insights by severity.
type InsightCount struct {
	Critical int `json:"critical"`
	Warning  int `json:"warning"`
	Info     int `json:"info"`
}

// KubeEvent represents a Kubernetes event for the frontend.
type KubeEvent struct {
	Type      string `json:"type"` // Normal, Warning
	Reason    string `json:"reason"`
	Message   string `json:"message"`
	Object    string `json:"object"`
	Namespace string `json:"namespace"`
	Timestamp string `json:"timestamp"`
	Count     int32  `json:"count"`
}

// NamespaceWorkload groups workloads by namespace.
type NamespaceWorkload struct {
	Namespace string            `json:"namespace"`
	Workloads []WorkloadSummary `json:"workloads"`
}

// WorkloadSummary represents a single workload with its pods.
type WorkloadSummary struct {
	Name          string        `json:"name"`
	Kind          string        `json:"kind"`
	Namespace     string        `json:"namespace"`
	Replicas      int32         `json:"replicas"`
	ReadyReplicas int32         `json:"readyReplicas"`
	Status        string        `json:"status"`
	CPU           ResourceUsage `json:"cpu"`
	Memory        ResourceUsage `json:"memory"`
	Pods          []PodSummary  `json:"pods"`
}

// PodSummary is a brief summary of a pod.
type PodSummary struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Ready  bool   `json:"ready"`
}

// MetricPoint represents a single metrics sample for a resource.
type MetricPoint struct {
	Timestamp time.Time `json:"timestamp"`
	Resource  string    `json:"resource"`
	CPUUsage  int64     `json:"cpuUsage"`   // millicores
	MemUsage  int64     `json:"memUsage"`   // bytes
	CPULimit  int64     `json:"cpuLimit,omitempty"`
	MemLimit  int64     `json:"memLimit,omitempty"`
}

// Insight represents a diagnostic finding from the insights engine.
type Insight struct {
	ID         string     `json:"id"`
	Severity   string     `json:"severity"`
	Category   string     `json:"category"`
	Resource   string     `json:"resource"`
	Namespace  string     `json:"namespace"`
	Title      string     `json:"title"`
	Message    string     `json:"message"`
	Suggestion string     `json:"suggestion"`
	FirstSeen  time.Time  `json:"firstSeen"`
	LastSeen   time.Time  `json:"lastSeen"`
	Resolved   bool       `json:"resolved"`
	ResolvedAt *time.Time `json:"resolvedAt,omitempty"`
}

// TopologyNode is a vertex in the cluster topology graph.
type TopologyNode struct {
	ID        string            `json:"id"`
	Type      string            `json:"type"`
	Name      string            `json:"name"`
	Label     string            `json:"label"`
	Namespace string            `json:"namespace"`
	Status    string            `json:"status"`
	Kind      string            `json:"kind"`
	Metrics   *ResourceMetrics  `json:"metrics,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	CPU       *ResourceUsage    `json:"cpu,omitempty"`
	Memory    *ResourceUsage    `json:"memory,omitempty"`
	Pods      []PodSummary      `json:"pods,omitempty"`
}

// TopologyEdge is a directed edge in the cluster topology graph.
type TopologyEdge struct {
	ID       string `json:"id"`
	Source   string `json:"source"`
	Target   string `json:"target"`
	Type     string `json:"type"`
	Label    string `json:"label,omitempty"`
	Animated bool   `json:"animated,omitempty"`
}

// ResourceMetrics holds computed metrics for a topology node.
type ResourceMetrics struct {
	CPUPercent float64 `json:"cpuPercent"`
	MemPercent float64 `json:"memPercent"`
	PodCount   int     `json:"podCount"`
	PodReady   int     `json:"podReady"`
	Restarts   int     `json:"restarts"`
}

// Topology is the full topology graph.
type Topology struct {
	Nodes []TopologyNode `json:"nodes"`
	Edges []TopologyEdge `json:"edges"`
}

// ResourceList is a list of resources.
type ResourceList struct {
	Kind  string                   `json:"kind"`
	Items []map[string]interface{} `json:"items"`
	Total int                      `json:"total"`
}

// WSMessage is a WebSocket message envelope.
type WSMessage struct {
	Type string      `json:"type"`
	Data interface{} `json:"data"`
}

// ClusterInfoResponse represents a cluster entry returned by the clusters API.
type ClusterInfoResponse struct {
	Name    string `json:"name"`
	Context string `json:"context"`
	Server  string `json:"server"`
	Active  bool   `json:"active"`
}
