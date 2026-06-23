package models

import "time"

// ClusterOverview is the top-level summary returned by GET /cluster/overview
type ClusterOverview struct {
	ClusterName          string        `json:"clusterName"`
	ClusterUID           string        `json:"clusterUID,omitempty"` // kube-system namespace UID; used by the UI to scope metric queries
	KubernetesVersion    string        `json:"kubernetesVersion"`
	Platform             string        `json:"platform"`
	Nodes                ResourceCount `json:"nodes"`
	Pods                 ResourceCount `json:"pods"`
	Namespaces           ResourceCount `json:"namespaces"`
	Services             ResourceCount `json:"services"`
	Deployments          ResourceCount `json:"deployments"`
	StatefulSets         ResourceCount `json:"statefulSets"`
	DaemonSets           ResourceCount `json:"daemonSets"`
	Jobs                 ResourceCount `json:"jobs"`
	CronJobs             ResourceCount `json:"cronJobs"`
	Ingresses            ResourceCount `json:"ingresses"`
	NetworkPolicies      ResourceCount `json:"networkPolicies"`
	PodDisruptionBudgets ResourceCount `json:"podDisruptionBudgets"`
	// Gateways / HTTPRoutes — present when Gateway API CRDs are
	// installed. Zero on clusters that haven't installed the CRDs;
	// the sidebar hides the counter chip when the value is zero
	// (matches the existing behavior for other declarative kinds).
	Gateways   ResourceCount `json:"gateways"`
	HTTPRoutes ResourceCount `json:"httpRoutes"`
	// ServiceAccounts (core) + the optional CRDs (cert-manager / ArgoCD /
	// VPA). Same zero-on-absent semantics as Gateways — the sidebar shows
	// the counter once the type is present.
	ServiceAccounts                  ResourceCount `json:"serviceAccounts"`
	Certificates                     ResourceCount `json:"certificates"`
	ArgoCDApps                       ResourceCount `json:"argocdApps"`
	VPAs                             ResourceCount `json:"vpas"`
	CiliumNetworkPolicies            ResourceCount `json:"ciliumNetworkPolicies"`
	CiliumClusterwideNetworkPolicies ResourceCount `json:"ciliumClusterwideNetworkPolicies"`
	// HelmReleases — distinct Helm releases (counted from the owner=helm
	// Secret labels, no payload decode). Powers the Applications counter.
	HelmReleases ResourceCount `json:"helmReleases"`
	// Endpoints — backed by EndpointSlices (KubeBolt's `endpoints`
	// resource type lists one row per EndpointSlice, not per legacy
	// Endpoints object). Count matches what the list endpoint returns.
	Endpoints          ResourceCount       `json:"endpoints"`
	ConfigMaps         ResourceCount       `json:"configMaps"`
	Secrets            ResourceCount       `json:"secrets"`
	PVCs               ResourceCount       `json:"pvcs"`
	PVs                ResourceCount       `json:"pvs"`
	HPAs               ResourceCount       `json:"hpas"`
	CPU                ResourceUsage       `json:"cpu"`
	Memory             ResourceUsage       `json:"memory"`
	Health             ClusterHealth       `json:"health"`
	Events             []KubeEvent         `json:"events"`
	NamespaceWorkloads []NamespaceWorkload `json:"namespaceWorkloads"`
	Permissions        map[string]bool     `json:"permissions,omitempty"`
	// AbsentResources are optional-CRD keys that RBAC grants but the cluster
	// doesn't have installed. They're CanList=false in Permissions (UI shows them
	// "not available") but must be excluded from the limited-access banner — a
	// missing optional CRD is not a permission restriction.
	AbsentResources []string `json:"absentResources,omitempty"`
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

// DeployEvent represents a single rollout: a Deployment / StatefulSet
// / DaemonSet got a new pod template and the controller spun up a new
// revision. Used by the Capacity dashboard to overlay markers on the
// time-series charts so the user can correlate metric shifts with
// "what changed in the cluster" at a glance.
//
// Today the connector derives these from ReplicaSet creation
// timestamps (every new ReplicaSet a Deployment owns is, by
// definition, a rollout). StatefulSet / DaemonSet rollouts are
// surfaced as soon as a ControllerRevision lister is wired in;
// the JSON shape stays the same.
type DeployEvent struct {
	Namespace  string    `json:"namespace"`
	Kind       string    `json:"kind"` // "Deployment" | "StatefulSet" | "DaemonSet"
	Name       string    `json:"name"`
	DeployedAt time.Time `json:"deployedAt"`
	Image      string    `json:"image,omitempty"` // first container's image, for the marker tooltip
}

// MetricPoint represents a single metrics sample for a resource.
type MetricPoint struct {
	Timestamp time.Time `json:"timestamp"`
	Resource  string    `json:"resource"`
	CPUUsage  int64     `json:"cpuUsage"` // millicores
	MemUsage  int64     `json:"memUsage"` // bytes
	CPULimit  int64     `json:"cpuLimit,omitempty"`
	MemLimit  int64     `json:"memLimit,omitempty"`
}

// Insight represents a diagnostic finding from the insights engine.
//
// Identity (Sprint 0): ID carries the current occurrence id (what
// consumers reference — the insight→Kobi trigger, ActionProposal
// provenance, Autopilot trigger_source_ref). Fingerprint is the stable
// cross-restart/cross-recurrence identity = sha256(tenant|cluster|ruleId|
// resource). RuleID/TenantID/ClusterID are stamped by the engine at
// evaluation time. In OSS, TenantID is always "default".
type Insight struct {
	ID          string     `json:"id"`
	Fingerprint string     `json:"fingerprint,omitempty"`
	RuleID      string     `json:"ruleId,omitempty"`
	TenantID    string     `json:"tenantId,omitempty"`
	ClusterID   string     `json:"clusterId,omitempty"`
	Severity    string     `json:"severity"`
	Category    string     `json:"category"`
	Resource    string     `json:"resource"`
	Namespace   string     `json:"namespace"`
	Title       string     `json:"title"`
	Message     string     `json:"message"`
	Suggestion  string     `json:"suggestion"`
	FirstSeen   time.Time  `json:"firstSeen"`
	LastSeen    time.Time  `json:"lastSeen"`
	Resolved    bool       `json:"resolved"`
	ResolvedAt  *time.Time `json:"resolvedAt,omitempty"`
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
	Kind      string                   `json:"kind"`
	Items     []map[string]interface{} `json:"items"`
	Total     int                      `json:"total"`
	Forbidden bool                     `json:"forbidden,omitempty"`
}

// WSMessage is a WebSocket message envelope. Tenant/Cluster scope the event to
// the runtime that produced it (A.4): the hub delivers a scoped message only to
// clients viewing that (tenant, cluster). Empty = global/unscoped — delivered
// to everyone (the OSS-degenerate case, and cluster-management events like
// clusters.changed that aren't tied to one cluster).
type WSMessage struct {
	Type    string      `json:"type"`
	Data    interface{} `json:"data"`
	Tenant  string      `json:"tenant,omitempty"`
	Cluster string      `json:"cluster,omitempty"`
}

// ClusterInfoResponse represents a cluster entry returned by the clusters API.
type ClusterInfoResponse struct {
	Name    string `json:"name"`
	Context string `json:"context"`
	Server  string `json:"server"`
	Active  bool   `json:"active"`
}
