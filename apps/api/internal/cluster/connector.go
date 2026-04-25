package cluster

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"context"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	appslisters "k8s.io/client-go/listers/apps/v1"
	autoscalinglisters "k8s.io/client-go/listers/autoscaling/v1"
	batchlisters "k8s.io/client-go/listers/batch/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	discoverylisters "k8s.io/client-go/listers/discovery/v1"
	networkinglisters "k8s.io/client-go/listers/networking/v1"
	rbaclisters "k8s.io/client-go/listers/rbac/v1"
	storagelisters "k8s.io/client-go/listers/storage/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	rest "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	metricsv "k8s.io/metrics/pkg/client/clientset/versioned"

	"github.com/kubebolt/kubebolt/apps/api/internal/models"
	"github.com/kubebolt/kubebolt/apps/api/internal/websocket"
	sigsyaml "sigs.k8s.io/yaml"
)

// stripManagedFieldsOption removes managedFields from cached objects to reduce memory.
// managedFields is server-side apply metadata not used by KubeBolt.
// The YAML endpoint uses the dynamic client directly, so full objects are still available there.
var stripManagedFieldsOption = informers.WithTransform(func(obj interface{}) (interface{}, error) {
	if accessor, ok := obj.(metav1.ObjectMetaAccessor); ok {
		accessor.GetObjectMeta().SetManagedFields(nil)
	}
	return obj, nil
})

// Connector manages the Kubernetes cluster connection and informers.
type Connector struct {
	restConfig    *rest.Config
	clientset     kubernetes.Interface
	dynamicClient dynamic.Interface
	metricsClient metricsv.Interface
	factory       informers.SharedInformerFactory
	nsFactories   []informers.SharedInformerFactory // per-namespace factories for namespace-scoped access
	graph         *TopologyGraph
	wsHub         *websocket.Hub
	stopCh        chan struct{}
	mu            sync.RWMutex
	clusterName    string
	clusterUID     string // kube-system namespace UID, used to scope VM queries per cluster
	collector      metricsCollector
	topologyTimer  *time.Timer
	permissions    ResourcePermissions

	// Listers
	podLister            corelisters.PodLister
	nodeLister           corelisters.NodeLister
	namespaceLister      corelisters.NamespaceLister
	serviceLister        corelisters.ServiceLister
	endpointSliceLister  discoverylisters.EndpointSliceLister
	configMapLister      corelisters.ConfigMapLister
	secretLister         corelisters.SecretLister
	pvcLister            corelisters.PersistentVolumeClaimLister
	pvLister             corelisters.PersistentVolumeLister
	eventLister          corelisters.EventLister
	deploymentLister     appslisters.DeploymentLister
	statefulSetLister    appslisters.StatefulSetLister
	daemonSetLister      appslisters.DaemonSetLister
	replicaSetLister     appslisters.ReplicaSetLister
	jobLister            batchlisters.JobLister
	cronJobLister        batchlisters.CronJobLister
	ingressLister        networkinglisters.IngressLister
	hpaLister            autoscalinglisters.HorizontalPodAutoscalerLister
	storageClassLister   storagelisters.StorageClassLister
	roleLister           rbaclisters.RoleLister
	clusterRoleLister    rbaclisters.ClusterRoleLister
	roleBindingLister    rbaclisters.RoleBindingLister
	clusterRoleBindingLister rbaclisters.ClusterRoleBindingLister
}

// metricsCollector is an interface for getting metrics data.
type metricsCollector interface {
	GetAllPodMetrics() map[string]*models.MetricPoint
	GetAllNodeMetrics() map[string]*models.MetricPoint
	IsAvailable() bool
}

// NewConnector creates a new cluster connector using the default kubeconfig context.
func NewConnector(kubeconfigPath string, wsHub *websocket.Hub) (*Connector, error) {
	restConfig, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("building kubeconfig: %w", err)
	}

	// Extract cluster name from kubeconfig context
	clusterName := "kubernetes"
	kubeConfig, err := clientcmd.LoadFromFile(kubeconfigPath)
	if err == nil && kubeConfig.CurrentContext != "" {
		clusterName = kubeConfig.CurrentContext
	}

	return newConnectorFromConfig(restConfig, clusterName, wsHub)
}

// newConnectorFromConfig creates a connector from an existing rest.Config.
func newConnectorFromConfig(restConfig *rest.Config, clusterName string, wsHub *websocket.Hub) (*Connector, error) {
	// Bound individual K8s API calls so the server doesn't hang if a cluster
	// becomes unreachable mid-session (e.g. laptop closed, VPN dropped).
	restConfig.Timeout = 15 * time.Second

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("creating kubernetes client: %w", err)
	}

	dynClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		log.Printf("Warning: dynamic client creation failed: %v", err)
	}

	metricsClient, err := metricsv.NewForConfig(restConfig)
	if err != nil {
		log.Printf("Warning: metrics client creation failed: %v", err)
	}

	factory := informers.NewSharedInformerFactoryWithOptions(clientset, 30*time.Second, stripManagedFieldsOption)

	c := &Connector{
		restConfig:    restConfig,
		clientset:     clientset,
		dynamicClient: dynClient,
		metricsClient: metricsClient,
		factory:       factory,
		graph:         NewTopologyGraph(),
		wsHub:         wsHub,
		stopCh:        make(chan struct{}),
		clusterName:   clusterName,
	}

	// Read kube-system namespace UID to scope VM queries. Same value
	// that the kubebolt-agent uses as `cluster_id` on every sample,
	// so the backend can filter VM PromQL to just this cluster's
	// series. 15s matches rest.Config.Timeout — EKS in particular can
	// take several seconds on the first call when aws-iam-authenticator
	// is exec'd cold, and a too-short timeout here leaves the connector
	// with an empty UID, which previously caused unscoped queries to
	// leak data from other clusters in the same VM.
	if uidCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second); true {
		defer cancel()
		if ns, err := clientset.CoreV1().Namespaces().Get(uidCtx, "kube-system", metav1.GetOptions{}); err == nil {
			c.clusterUID = string(ns.UID)
		} else {
			log.Printf("Warning: failed to read kube-system UID for cluster %q: %v — VM queries for this cluster will return empty results until reconnect", clusterName, err)
		}
	}

	c.permissions = probePermissions(clientset)
	c.setupInformers()
	return c, nil
}

// ClusterUID returns the kube-system namespace UID, unique per
// Kubernetes cluster. Empty when the connector couldn't reach the
// apiserver to discover it (dev-mode or transient failure) — callers
// should treat empty as "no scoping available" and fall back to
// unscoped queries rather than blocking.
func (c *Connector) ClusterUID() string {
	return c.clusterUID
}

// Permissions returns the probed resource permissions for this cluster.
func (c *Connector) Permissions() ResourcePermissions {
	return c.permissions
}

// RestConfig returns the Kubernetes REST config for this cluster connection.
func (c *Connector) RestConfig() *rest.Config {
	return c.restConfig
}

// Clientset returns the Kubernetes clientset for this cluster connection.
func (c *Connector) Clientset() kubernetes.Interface {
	return c.clientset
}

// SetCollector sets the metrics collector reference for use in GetOverview.
func (c *Connector) SetCollector(collector metricsCollector) {
	c.collector = collector
}

// MetricsClient returns the metrics client for use by the metrics collector.
func (c *Connector) MetricsClient() metricsv.Interface {
	return c.metricsClient
}

func (c *Connector) setupInformers() {
	can := c.permissions.CanListWatch
	isNS := func(key string) bool {
		if p, ok := c.permissions[key]; ok {
			return p.NamespaceScoped
		}
		return false
	}

	// Determine if we need namespace-scoped factories
	var nsFactories map[string]informers.SharedInformerFactory
	var nsNamespaces []string
	for _, p := range c.permissions {
		if p.NamespaceScoped && len(p.Namespaces) > 0 {
			nsNamespaces = p.Namespaces
			break
		}
	}
	if len(nsNamespaces) > 0 {
		nsFactories = make(map[string]informers.SharedInformerFactory, len(nsNamespaces))
		for _, ns := range nsNamespaces {
			nsFactories[ns] = informers.NewSharedInformerFactoryWithOptions(
				c.clientset, 30*time.Second, informers.WithNamespace(ns), stripManagedFieldsOption,
			)
		}
	}

	// Add event handlers for topology updates and WebSocket broadcasts
	handler := cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.onResourceChange("add", obj)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			c.onResourceChange("update", newObj)
		},
		DeleteFunc: func(obj interface{}) {
			c.onResourceChange("delete", obj)
		},
	}

	// Core v1
	if can("pods") {
		if isNS("pods") {
			var listers []corelisters.PodLister
			for _, f := range nsFactories {
				listers = append(listers, f.Core().V1().Pods().Lister())
				f.Core().V1().Pods().Informer().AddEventHandler(handler)
			}
			c.podLister = &multiPodLister{listers: listers}
		} else {
			c.podLister = c.factory.Core().V1().Pods().Lister()
			c.factory.Core().V1().Pods().Informer().AddEventHandler(handler)
		}
	}
	if can("nodes") {
		c.nodeLister = c.factory.Core().V1().Nodes().Lister()
		c.factory.Core().V1().Nodes().Informer().AddEventHandler(handler)
	}
	if can("namespaces") {
		c.namespaceLister = c.factory.Core().V1().Namespaces().Lister()
		c.factory.Core().V1().Namespaces().Informer().AddEventHandler(handler)
	}
	if can("services") {
		if isNS("services") {
			var listers []corelisters.ServiceLister
			for _, f := range nsFactories { listers = append(listers, f.Core().V1().Services().Lister()); f.Core().V1().Services().Informer().AddEventHandler(handler) }
			c.serviceLister = &multiServiceLister{listers: listers}
		} else {
			c.serviceLister = c.factory.Core().V1().Services().Lister()
			c.factory.Core().V1().Services().Informer().AddEventHandler(handler)
		}
	}
	if can("endpointslices") {
		if isNS("endpointslices") {
			var listers []discoverylisters.EndpointSliceLister
			for _, f := range nsFactories { listers = append(listers, f.Discovery().V1().EndpointSlices().Lister()); f.Discovery().V1().EndpointSlices().Informer().AddEventHandler(handler) }
			c.endpointSliceLister = &multiEndpointSliceLister{listers: listers}
		} else {
			c.endpointSliceLister = c.factory.Discovery().V1().EndpointSlices().Lister()
			c.factory.Discovery().V1().EndpointSlices().Informer().AddEventHandler(handler)
		}
	}
	if can("configmaps") {
		if isNS("configmaps") {
			var listers []corelisters.ConfigMapLister
			for _, f := range nsFactories { listers = append(listers, f.Core().V1().ConfigMaps().Lister()); f.Core().V1().ConfigMaps().Informer().AddEventHandler(handler) }
			c.configMapLister = &multiConfigMapLister{listers: listers}
		} else {
			c.configMapLister = c.factory.Core().V1().ConfigMaps().Lister()
			c.factory.Core().V1().ConfigMaps().Informer().AddEventHandler(handler)
		}
	}
	if can("secrets") {
		if isNS("secrets") {
			var listers []corelisters.SecretLister
			for _, f := range nsFactories { listers = append(listers, f.Core().V1().Secrets().Lister()); f.Core().V1().Secrets().Informer().AddEventHandler(handler) }
			c.secretLister = &multiSecretLister{listers: listers}
		} else {
			c.secretLister = c.factory.Core().V1().Secrets().Lister()
			c.factory.Core().V1().Secrets().Informer().AddEventHandler(handler)
		}
	}
	if can("pvcs") {
		if isNS("pvcs") {
			var listers []corelisters.PersistentVolumeClaimLister
			for _, f := range nsFactories { listers = append(listers, f.Core().V1().PersistentVolumeClaims().Lister()); f.Core().V1().PersistentVolumeClaims().Informer().AddEventHandler(handler) }
			c.pvcLister = &multiPVCLister{listers: listers}
		} else {
			c.pvcLister = c.factory.Core().V1().PersistentVolumeClaims().Lister()
			c.factory.Core().V1().PersistentVolumeClaims().Informer().AddEventHandler(handler)
		}
	}
	if can("pvs") {
		c.pvLister = c.factory.Core().V1().PersistentVolumes().Lister()
		c.factory.Core().V1().PersistentVolumes().Informer().AddEventHandler(handler)
	}
	if can("events") {
		if isNS("events") {
			var listers []corelisters.EventLister
			for _, f := range nsFactories { listers = append(listers, f.Core().V1().Events().Lister()); f.Core().V1().Events().Informer().AddEventHandler(handler) }
			c.eventLister = &multiEventLister{listers: listers}
		} else {
			c.eventLister = c.factory.Core().V1().Events().Lister()
			c.factory.Core().V1().Events().Informer().AddEventHandler(handler)
		}
	}

	// Apps v1
	if can("deployments") {
		if isNS("deployments") {
			var listers []appslisters.DeploymentLister
			for _, f := range nsFactories { listers = append(listers, f.Apps().V1().Deployments().Lister()); f.Apps().V1().Deployments().Informer().AddEventHandler(handler) }
			c.deploymentLister = &multiDeploymentLister{listers: listers}
		} else {
			c.deploymentLister = c.factory.Apps().V1().Deployments().Lister()
			c.factory.Apps().V1().Deployments().Informer().AddEventHandler(handler)
		}
	}
	if can("statefulsets") {
		if isNS("statefulsets") {
			var listers []appslisters.StatefulSetLister
			for _, f := range nsFactories { listers = append(listers, f.Apps().V1().StatefulSets().Lister()); f.Apps().V1().StatefulSets().Informer().AddEventHandler(handler) }
			c.statefulSetLister = &multiStatefulSetLister{listers: listers}
		} else {
			c.statefulSetLister = c.factory.Apps().V1().StatefulSets().Lister()
			c.factory.Apps().V1().StatefulSets().Informer().AddEventHandler(handler)
		}
	}
	if can("daemonsets") {
		if isNS("daemonsets") {
			var listers []appslisters.DaemonSetLister
			for _, f := range nsFactories { listers = append(listers, f.Apps().V1().DaemonSets().Lister()); f.Apps().V1().DaemonSets().Informer().AddEventHandler(handler) }
			c.daemonSetLister = &multiDaemonSetLister{listers: listers}
		} else {
			c.daemonSetLister = c.factory.Apps().V1().DaemonSets().Lister()
			c.factory.Apps().V1().DaemonSets().Informer().AddEventHandler(handler)
		}
	}
	if can("replicasets") {
		if isNS("replicasets") {
			var listers []appslisters.ReplicaSetLister
			for _, f := range nsFactories { listers = append(listers, f.Apps().V1().ReplicaSets().Lister()); f.Apps().V1().ReplicaSets().Informer().AddEventHandler(handler) }
			c.replicaSetLister = &multiReplicaSetLister{listers: listers}
		} else {
			c.replicaSetLister = c.factory.Apps().V1().ReplicaSets().Lister()
			c.factory.Apps().V1().ReplicaSets().Informer().AddEventHandler(handler)
		}
	}

	// Batch v1
	if can("jobs") {
		if isNS("jobs") {
			var listers []batchlisters.JobLister
			for _, f := range nsFactories { listers = append(listers, f.Batch().V1().Jobs().Lister()); f.Batch().V1().Jobs().Informer().AddEventHandler(handler) }
			c.jobLister = &multiJobLister{listers: listers}
		} else {
			c.jobLister = c.factory.Batch().V1().Jobs().Lister()
			c.factory.Batch().V1().Jobs().Informer().AddEventHandler(handler)
		}
	}
	if can("cronjobs") {
		if isNS("cronjobs") {
			var listers []batchlisters.CronJobLister
			for _, f := range nsFactories { listers = append(listers, f.Batch().V1().CronJobs().Lister()); f.Batch().V1().CronJobs().Informer().AddEventHandler(handler) }
			c.cronJobLister = &multiCronJobLister{listers: listers}
		} else {
			c.cronJobLister = c.factory.Batch().V1().CronJobs().Lister()
			c.factory.Batch().V1().CronJobs().Informer().AddEventHandler(handler)
		}
	}

	// Networking v1
	if can("ingresses") {
		if isNS("ingresses") {
			var listers []networkinglisters.IngressLister
			for _, f := range nsFactories { listers = append(listers, f.Networking().V1().Ingresses().Lister()); f.Networking().V1().Ingresses().Informer().AddEventHandler(handler) }
			c.ingressLister = &multiIngressLister{listers: listers}
		} else {
			c.ingressLister = c.factory.Networking().V1().Ingresses().Lister()
			c.factory.Networking().V1().Ingresses().Informer().AddEventHandler(handler)
		}
	}

	// Autoscaling v1
	if can("hpas") {
		if isNS("hpas") {
			var listers []autoscalinglisters.HorizontalPodAutoscalerLister
			for _, f := range nsFactories { listers = append(listers, f.Autoscaling().V1().HorizontalPodAutoscalers().Lister()); f.Autoscaling().V1().HorizontalPodAutoscalers().Informer().AddEventHandler(handler) }
			c.hpaLister = &multiHPALister{listers: listers}
		} else {
			c.hpaLister = c.factory.Autoscaling().V1().HorizontalPodAutoscalers().Lister()
			c.factory.Autoscaling().V1().HorizontalPodAutoscalers().Informer().AddEventHandler(handler)
		}
	}

	// Storage v1 (cluster-scoped only)
	if can("storageclasses") {
		c.storageClassLister = c.factory.Storage().V1().StorageClasses().Lister()
		c.factory.Storage().V1().StorageClasses().Informer().AddEventHandler(handler)
	}

	// RBAC v1
	if can("roles") {
		if isNS("roles") {
			var listers []rbaclisters.RoleLister
			for _, f := range nsFactories { listers = append(listers, f.Rbac().V1().Roles().Lister()); f.Rbac().V1().Roles().Informer().AddEventHandler(handler) }
			c.roleLister = &multiRoleLister{listers: listers}
		} else {
			c.roleLister = c.factory.Rbac().V1().Roles().Lister()
			c.factory.Rbac().V1().Roles().Informer().AddEventHandler(handler)
		}
	}
	if can("clusterroles") {
		c.clusterRoleLister = c.factory.Rbac().V1().ClusterRoles().Lister()
		c.factory.Rbac().V1().ClusterRoles().Informer().AddEventHandler(handler)
	}
	if can("rolebindings") {
		if isNS("rolebindings") {
			var listers []rbaclisters.RoleBindingLister
			for _, f := range nsFactories { listers = append(listers, f.Rbac().V1().RoleBindings().Lister()); f.Rbac().V1().RoleBindings().Informer().AddEventHandler(handler) }
			c.roleBindingLister = &multiRoleBindingLister{listers: listers}
		} else {
			c.roleBindingLister = c.factory.Rbac().V1().RoleBindings().Lister()
			c.factory.Rbac().V1().RoleBindings().Informer().AddEventHandler(handler)
		}
	}
	if can("clusterrolebindings") {
		c.clusterRoleBindingLister = c.factory.Rbac().V1().ClusterRoleBindings().Lister()
		c.factory.Rbac().V1().ClusterRoleBindings().Informer().AddEventHandler(handler)
	}

	// Store ns factories for Start/Stop
	for _, f := range nsFactories {
		c.nsFactories = append(c.nsFactories, f)
	}
}

func (c *Connector) onResourceChange(action string, obj interface{}) {
	if action == "delete" {
		c.wsHub.Broadcast(websocket.ResourceDeleted, obj)
	} else {
		c.wsHub.Broadcast(websocket.ResourceUpdated, obj)
	}
	// Debounced topology rebuild — coalesce rapid changes
	c.scheduleTopologyRebuild()
}

func (c *Connector) scheduleTopologyRebuild() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.topologyTimer != nil {
		c.topologyTimer.Stop()
	}
	c.topologyTimer = time.AfterFunc(2*time.Second, func() {
		c.rebuildTopology()
	})
}

func (c *Connector) rebuildTopology() {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Rebuild all nodes
	c.graph = NewTopologyGraph()
	c.buildTopologyNodes()
	edges := c.BuildEdges()
	c.graph.SetEdges(edges)
}

func (c *Connector) buildTopologyNodes() {
	if c.podLister != nil {
		pods, _ := c.podLister.List(everythingSelector())
		for _, pod := range pods {
			c.graph.AddNode(models.TopologyNode{
				ID:        nodeID("Pod", pod.Namespace, pod.Name),
				Type:      "Pod",
				Name:      pod.Name,
				Namespace: pod.Namespace,
				Status:    string(pod.Status.Phase),
			})
		}
	}

	if c.nodeLister != nil {
		nodes, _ := c.nodeLister.List(everythingSelector())
		for _, node := range nodes {
			status := "NotReady"
			for _, cond := range node.Status.Conditions {
				if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
					status = "Ready"
					break
				}
			}
			c.graph.AddNode(models.TopologyNode{
				ID:     nodeID("Node", "", node.Name),
				Type:   "Node",
				Name:   node.Name,
				Status: status,
			})
		}
	}

	if c.deploymentLister != nil {
		deployments, _ := c.deploymentLister.List(everythingSelector())
		for _, d := range deployments {
			c.graph.AddNode(models.TopologyNode{
				ID:        nodeID("Deployment", d.Namespace, d.Name),
				Type:      "Deployment",
				Name:      d.Name,
				Namespace: d.Namespace,
				Status:    deploymentStatus(d),
			})
		}
	}

	if c.serviceLister != nil {
		services, _ := c.serviceLister.List(everythingSelector())
		for _, svc := range services {
			c.graph.AddNode(models.TopologyNode{
				ID:        nodeID("Service", svc.Namespace, svc.Name),
				Type:      "Service",
				Name:      svc.Name,
				Namespace: svc.Namespace,
				Status:    string(svc.Spec.Type),
			})
		}
	}

	if c.statefulSetLister != nil {
		statefulSets, _ := c.statefulSetLister.List(everythingSelector())
		for _, ss := range statefulSets {
			c.graph.AddNode(models.TopologyNode{
				ID:        nodeID("StatefulSet", ss.Namespace, ss.Name),
				Type:      "StatefulSet",
				Name:      ss.Name,
				Namespace: ss.Namespace,
				Status:    fmt.Sprintf("%d/%d", ss.Status.ReadyReplicas, ss.Status.Replicas),
			})
		}
	}

	if c.daemonSetLister != nil {
		daemonSets, _ := c.daemonSetLister.List(everythingSelector())
		for _, ds := range daemonSets {
			c.graph.AddNode(models.TopologyNode{
				ID:        nodeID("DaemonSet", ds.Namespace, ds.Name),
				Type:      "DaemonSet",
				Name:      ds.Name,
				Namespace: ds.Namespace,
				Status:    fmt.Sprintf("%d/%d", ds.Status.NumberReady, ds.Status.DesiredNumberScheduled),
			})
		}
	}

	if c.ingressLister != nil {
		ingresses, _ := c.ingressLister.List(everythingSelector())
		for _, ing := range ingresses {
			c.graph.AddNode(models.TopologyNode{
				ID:        nodeID("Ingress", ing.Namespace, ing.Name),
				Type:      "Ingress",
				Name:      ing.Name,
				Namespace: ing.Namespace,
				Status:    "Active",
			})
		}
	}

	if c.replicaSetLister != nil {
		replicaSets, _ := c.replicaSetLister.List(everythingSelector())
		for _, rs := range replicaSets {
			c.graph.AddNode(models.TopologyNode{
				ID:        nodeID("ReplicaSet", rs.Namespace, rs.Name),
				Type:      "ReplicaSet",
				Name:      rs.Name,
				Namespace: rs.Namespace,
				Status:    fmt.Sprintf("%d/%d", rs.Status.ReadyReplicas, rs.Status.Replicas),
			})
		}
	}

	if c.jobLister != nil {
		jobs, _ := c.jobLister.List(everythingSelector())
		for _, job := range jobs {
			status := "Running"
			if job.Status.Succeeded > 0 {
				status = "Complete"
			} else if job.Status.Failed > 0 {
				status = "Failed"
			}
			c.graph.AddNode(models.TopologyNode{
				ID:        nodeID("Job", job.Namespace, job.Name),
				Type:      "Job",
				Name:      job.Name,
				Namespace: job.Namespace,
				Status:    status,
			})
		}
	}

	if c.cronJobLister != nil {
		cronJobs, _ := c.cronJobLister.List(everythingSelector())
		for _, cj := range cronJobs {
			c.graph.AddNode(models.TopologyNode{
				ID:        nodeID("CronJob", cj.Namespace, cj.Name),
				Type:      "CronJob",
				Name:      cj.Name,
				Namespace: cj.Namespace,
				Status:    "Scheduled",
			})
		}
	}

	if c.pvcLister != nil {
		pvcs, _ := c.pvcLister.List(everythingSelector())
		for _, pvc := range pvcs {
			c.graph.AddNode(models.TopologyNode{
				ID:        nodeID("PersistentVolumeClaim", pvc.Namespace, pvc.Name),
				Type:      "PersistentVolumeClaim",
				Name:      pvc.Name,
				Namespace: pvc.Namespace,
				Status:    string(pvc.Status.Phase),
			})
		}
	}

	if c.pvLister != nil {
		pvs, _ := c.pvLister.List(everythingSelector())
		for _, pv := range pvs {
			c.graph.AddNode(models.TopologyNode{
				ID:     nodeID("PersistentVolume", "", pv.Name),
				Type:   "PersistentVolume",
				Name:   pv.Name,
				Status: string(pv.Status.Phase),
			})
		}
	}

	if c.hpaLister != nil {
		hpas, _ := c.hpaLister.List(everythingSelector())
		for _, hpa := range hpas {
			c.graph.AddNode(models.TopologyNode{
				ID:        nodeID("HPA", hpa.Namespace, hpa.Name),
				Type:      "HPA",
				Name:      hpa.Name,
				Namespace: hpa.Namespace,
				Status:    "Active",
			})
		}
	}

	// Gateway API resources (dynamic, with timeout)
	c.addGatewayTopologyNodes()
}

func (c *Connector) addGatewayTopologyNodes() {
	if c.dynamicClient == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	gtwGVR := schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways"}
	gtwList, err := c.dynamicClient.Resource(gtwGVR).List(ctx, metav1.ListOptions{})
	if err == nil {
		for _, item := range gtwList.Items {
			status := "Active"
			if st, ok := item.Object["status"].(map[string]interface{}); ok {
				if conds, ok := st["conditions"].([]interface{}); ok {
					for _, cond := range conds {
						if cm, ok := cond.(map[string]interface{}); ok {
							if cm["type"] == "Programmed" && cm["status"] == "True" {
								status = "Programmed"
							}
						}
					}
				}
			}
			c.graph.AddNode(models.TopologyNode{
				ID:        nodeID("Gateway", item.GetNamespace(), item.GetName()),
				Type:      "Gateway",
				Name:      item.GetName(),
				Namespace: item.GetNamespace(),
				Status:    status,
			})
		}
	}

	hrGVR := schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}
	hrList, err := c.dynamicClient.Resource(hrGVR).List(ctx, metav1.ListOptions{})
	if err == nil {
		for _, item := range hrList.Items {
			c.graph.AddNode(models.TopologyNode{
				ID:        nodeID("HTTPRoute", item.GetNamespace(), item.GetName()),
				Type:      "HTTPRoute",
				Name:      item.GetName(),
				Namespace: item.GetNamespace(),
				Status:    "Accepted",
			})
		}
	}
}

// Start begins the shared informer factory. Returns an error if cache sync
// does not complete within 20 seconds (e.g. cluster is unreachable).
func (c *Connector) Start() error {
	c.factory.Start(c.stopCh)
	for _, f := range c.nsFactories {
		f.Start(c.stopCh)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for _, ok := range c.factory.WaitForCacheSync(ctx.Done()) {
		if !ok {
			return fmt.Errorf("timed out waiting for cache sync — cluster may be unreachable")
		}
	}
	for _, f := range c.nsFactories {
		for _, ok := range f.WaitForCacheSync(ctx.Done()) {
			if !ok {
				return fmt.Errorf("timed out waiting for namespace cache sync")
			}
		}
	}
	log.Println("Informer caches synced")
	c.rebuildTopology()
	return nil
}

// Stop shuts down informers and cancels pending timers.
func (c *Connector) Stop() {
	c.mu.Lock()
	if c.topologyTimer != nil {
		c.topologyTimer.Stop()
		c.topologyTimer = nil
	}
	c.mu.Unlock()
	close(c.stopCh) // stops both factory and nsFactories since they share stopCh
}

// GetOverview aggregates counts from listers.
func (c *Connector) GetOverview() models.ClusterOverview {
	overview := models.ClusterOverview{}

	// Cluster info
	overview.ClusterName = c.clusterName
	overview.ClusterUID = c.clusterUID
	serverVersion, err := c.clientset.Discovery().ServerVersion()
	if err == nil {
		overview.KubernetesVersion = serverVersion.GitVersion
		overview.Platform = detectPlatform(serverVersion.GitVersion)
	}

	// Nodes
	var nodes []*corev1.Node
	if c.nodeLister != nil {
		nodes, _ = c.nodeLister.List(everythingSelector())
	}
	overview.Nodes.Total = len(nodes)
	for _, node := range nodes {
		ready := false
		for _, cond := range node.Status.Conditions {
			if cond.Type == corev1.NodeReady {
				if cond.Status == corev1.ConditionTrue {
					ready = true
				}
				break
			}
		}
		if ready {
			overview.Nodes.Ready++
		} else {
			overview.Nodes.NotReady++
		}
		// Aggregate allocatable
		overview.CPU.Allocatable += node.Status.Allocatable.Cpu().MilliValue()
		overview.Memory.Allocatable += node.Status.Allocatable.Memory().Value()
	}

	// Pods
	var pods []*corev1.Pod
	if c.podLister != nil {
		pods, _ = c.podLister.List(everythingSelector())
	}
	overview.Pods.Total = len(pods)
	for _, pod := range pods {
		switch pod.Status.Phase {
		case corev1.PodRunning:
			allReady := true
			for _, cs := range pod.Status.ContainerStatuses {
				if !cs.Ready {
					allReady = false
					break
				}
			}
			if allReady {
				overview.Pods.Ready++
			} else {
				overview.Pods.Warning++
			}
		case corev1.PodSucceeded:
			overview.Pods.Ready++
		case corev1.PodFailed:
			overview.Pods.NotReady++
		case corev1.PodPending:
			overview.Pods.Warning++
		default:
			overview.Pods.Warning++
		}
		// Aggregate requests/limits
		for _, container := range pod.Spec.Containers {
			overview.CPU.Requested += container.Resources.Requests.Cpu().MilliValue()
			overview.CPU.Limit += container.Resources.Limits.Cpu().MilliValue()
			overview.Memory.Requested += container.Resources.Requests.Memory().Value()
			overview.Memory.Limit += container.Resources.Limits.Memory().Value()
		}
	}

	// Used CPU/Memory from metrics collector
	if c.collector != nil {
		for _, m := range c.collector.GetAllPodMetrics() {
			overview.CPU.Used += m.CPUUsage
			overview.Memory.Used += m.MemUsage
		}
	}

	if overview.CPU.Allocatable > 0 {
		overview.CPU.PercentUsed = float64(overview.CPU.Used) / float64(overview.CPU.Allocatable) * 100
		overview.CPU.PercentRequested = float64(overview.CPU.Requested) / float64(overview.CPU.Allocatable) * 100
	}
	if overview.Memory.Allocatable > 0 {
		overview.Memory.PercentUsed = float64(overview.Memory.Used) / float64(overview.Memory.Allocatable) * 100
		overview.Memory.PercentRequested = float64(overview.Memory.Requested) / float64(overview.Memory.Allocatable) * 100
	}

	// Namespaces
	var namespaces []*corev1.Namespace
	if c.namespaceLister != nil {
		namespaces, _ = c.namespaceLister.List(everythingSelector())
	}
	overview.Namespaces.Total = len(namespaces)
	for _, ns := range namespaces {
		if ns.Status.Phase == corev1.NamespaceActive {
			overview.Namespaces.Ready++
		} else {
			overview.Namespaces.NotReady++
		}
	}

	// Services
	var svcs []*corev1.Service
	if c.serviceLister != nil {
		svcs, _ = c.serviceLister.List(everythingSelector())
	}
	overview.Services.Total = len(svcs)
	overview.Services.Ready = len(svcs)

	// Deployments
	var deployments []*appsv1.Deployment
	if c.deploymentLister != nil {
		deployments, _ = c.deploymentLister.List(everythingSelector())
	}
	overview.Deployments.Total = len(deployments)
	for _, d := range deployments {
		if d.Status.AvailableReplicas == d.Status.Replicas && d.Status.Replicas > 0 {
			overview.Deployments.Ready++
		} else if d.Status.AvailableReplicas == 0 && d.Status.Replicas > 0 {
			overview.Deployments.NotReady++
		} else if d.Status.AvailableReplicas < d.Status.Replicas {
			overview.Deployments.Warning++
		} else {
			overview.Deployments.Ready++
		}
	}

	// StatefulSets
	var statefulSets []*appsv1.StatefulSet
	if c.statefulSetLister != nil {
		statefulSets, _ = c.statefulSetLister.List(everythingSelector())
	}
	overview.StatefulSets.Total = len(statefulSets)
	for _, ss := range statefulSets {
		if ss.Status.ReadyReplicas == ss.Status.Replicas {
			overview.StatefulSets.Ready++
		} else {
			overview.StatefulSets.Warning++
		}
	}

	// DaemonSets
	var daemonSets []*appsv1.DaemonSet
	if c.daemonSetLister != nil {
		daemonSets, _ = c.daemonSetLister.List(everythingSelector())
	}
	overview.DaemonSets.Total = len(daemonSets)
	for _, ds := range daemonSets {
		if ds.Status.NumberReady == ds.Status.DesiredNumberScheduled {
			overview.DaemonSets.Ready++
		} else {
			overview.DaemonSets.Warning++
		}
	}

	// Jobs
	var jobs []*batchv1.Job
	if c.jobLister != nil {
		jobs, _ = c.jobLister.List(everythingSelector())
	}
	overview.Jobs.Total = len(jobs)
	for _, job := range jobs {
		if job.Status.Succeeded > 0 {
			overview.Jobs.Ready++
		} else if job.Status.Failed > 0 {
			overview.Jobs.NotReady++
		} else {
			overview.Jobs.Warning++
		}
	}

	// CronJobs
	var cronJobs []*batchv1.CronJob
	if c.cronJobLister != nil {
		cronJobs, _ = c.cronJobLister.List(everythingSelector())
	}
	overview.CronJobs.Total = len(cronJobs)
	overview.CronJobs.Ready = len(cronJobs)

	// Ingresses
	var ingresses []*networkingv1.Ingress
	if c.ingressLister != nil {
		ingresses, _ = c.ingressLister.List(everythingSelector())
	}
	overview.Ingresses.Total = len(ingresses)
	overview.Ingresses.Ready = len(ingresses)

	// ConfigMaps
	var configMaps []*corev1.ConfigMap
	if c.configMapLister != nil {
		configMaps, _ = c.configMapLister.List(everythingSelector())
	}
	overview.ConfigMaps.Total = len(configMaps)
	overview.ConfigMaps.Ready = len(configMaps)

	// Secrets
	var secrets []*corev1.Secret
	if c.secretLister != nil {
		secrets, _ = c.secretLister.List(everythingSelector())
	}
	overview.Secrets.Total = len(secrets)
	overview.Secrets.Ready = len(secrets)

	// PVCs
	var pvcs []*corev1.PersistentVolumeClaim
	if c.pvcLister != nil {
		pvcs, _ = c.pvcLister.List(everythingSelector())
	}
	overview.PVCs.Total = len(pvcs)
	for _, pvc := range pvcs {
		if pvc.Status.Phase == corev1.ClaimBound {
			overview.PVCs.Ready++
		} else {
			overview.PVCs.NotReady++
		}
	}

	// PVs
	var pvs []*corev1.PersistentVolume
	if c.pvLister != nil {
		pvs, _ = c.pvLister.List(everythingSelector())
	}
	overview.PVs.Total = len(pvs)
	overview.PVs.Ready = len(pvs)

	// HPAs
	var hpas []*autoscalingv1.HorizontalPodAutoscaler
	if c.hpaLister != nil {
		hpas, _ = c.hpaLister.List(everythingSelector())
	}
	overview.HPAs.Total = len(hpas)
	overview.HPAs.Ready = len(hpas)

	// Events (recent 20)
	overview.Events = c.getRecentEvents(20)

	// Health
	overview.Health = c.buildHealth()

	// Namespace workloads
	overview.NamespaceWorkloads = c.buildNamespaceWorkloads(pods, deployments, statefulSets, daemonSets)

	// Permissions
	if c.permissions != nil {
		overview.Permissions = make(map[string]bool, len(c.permissions))
		for key, perm := range c.permissions {
			overview.Permissions[key] = perm.CanList && perm.CanWatch
		}
	}

	return overview
}

func detectPlatform(gitVersion string) string {
	v := strings.ToLower(gitVersion)
	switch {
	case strings.Contains(v, "gke"):
		return "GKE"
	case strings.Contains(v, "eks"):
		return "EKS"
	case strings.Contains(v, "aks"):
		return "AKS"
	case strings.Contains(v, "k3s"):
		return "k3s"
	case strings.Contains(v, "rke"):
		return "RKE"
	case strings.Contains(v, "rancher"):
		return "Rancher"
	default:
		return "Kubernetes"
	}
}

func (c *Connector) getRecentEvents(limit int) []models.KubeEvent {
	if c.eventLister == nil {
		return nil
	}
	events, _ := c.eventLister.List(everythingSelector())
	// Sort by last timestamp desc
	sort.Slice(events, func(i, j int) bool {
		ti := events[i].LastTimestamp.Time
		tj := events[j].LastTimestamp.Time
		if ti.IsZero() {
			ti = events[i].CreationTimestamp.Time
		}
		if tj.IsZero() {
			tj = events[j].CreationTimestamp.Time
		}
		return ti.After(tj)
	})
	if limit > 0 && len(events) > limit {
		events = events[:limit]
	}
	result := make([]models.KubeEvent, 0, len(events))
	for _, e := range events {
		ts := e.LastTimestamp.Time
		if ts.IsZero() {
			ts = e.CreationTimestamp.Time
		}
		result = append(result, models.KubeEvent{
			Type:      e.Type,
			Reason:    e.Reason,
			Message:   e.Message,
			Object:    e.InvolvedObject.Kind + "/" + e.InvolvedObject.Name,
			Namespace: e.Namespace,
			Timestamp: ts.Format(time.RFC3339),
			Count:     e.Count,
		})
	}
	return result
}

func (c *Connector) buildHealth() models.ClusterHealth {
	health := models.ClusterHealth{
		Status: "healthy",
		Score:  100,
		Checks: []models.HealthCheck{},
	}

	// Check nodes
	var nodes []*corev1.Node
	if c.nodeLister != nil {
		nodes, _ = c.nodeLister.List(everythingSelector())
	}
	allNodesReady := true
	for _, node := range nodes {
		ready := false
		for _, cond := range node.Status.Conditions {
			if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
				ready = true
				break
			}
		}
		if !ready {
			allNodesReady = false
		}
	}
	if allNodesReady {
		health.Checks = append(health.Checks, models.HealthCheck{Name: "nodes", Status: "pass", Message: "All nodes are ready"})
	} else {
		health.Checks = append(health.Checks, models.HealthCheck{Name: "nodes", Status: "fail", Message: "One or more nodes are not ready"})
		health.Score -= 30
	}

	// Check control plane
	_, err := c.clientset.Discovery().ServerVersion()
	if err == nil {
		health.Checks = append(health.Checks, models.HealthCheck{Name: "api-server", Status: "pass", Message: "API server is responsive"})
	} else {
		health.Checks = append(health.Checks, models.HealthCheck{Name: "api-server", Status: "fail", Message: "API server unreachable"})
		health.Score -= 40
	}

	// Check metrics
	metricsAvailable := c.collector != nil && c.collector.IsAvailable()
	if metricsAvailable {
		health.Checks = append(health.Checks, models.HealthCheck{Name: "metrics", Status: "pass", Message: "Metrics server is available"})
	} else {
		health.Checks = append(health.Checks, models.HealthCheck{Name: "metrics", Status: "warn", Message: "Metrics server not available"})
		health.Score -= 10
	}

	// Check for failing pods
	var pods []*corev1.Pod
	if c.podLister != nil {
		pods, _ = c.podLister.List(everythingSelector())
	}
	failingPods := 0
	for _, pod := range pods {
		if pod.Status.Phase == corev1.PodFailed {
			failingPods++
		}
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.State.Waiting != nil && cs.State.Waiting.Reason == "CrashLoopBackOff" {
				failingPods++
			}
		}
	}
	if failingPods == 0 {
		health.Checks = append(health.Checks, models.HealthCheck{Name: "pods", Status: "pass", Message: "No failing pods detected"})
	} else {
		health.Checks = append(health.Checks, models.HealthCheck{Name: "pods", Status: "warn", Message: fmt.Sprintf("%d failing or crash-looping pods", failingPods)})
		health.Score -= min(20, failingPods*5)
	}

	if health.Score < 0 {
		health.Score = 0
	}
	if health.Score < 50 {
		health.Status = "critical"
	} else if health.Score < 80 {
		health.Status = "warning"
	}

	return health
}

func (c *Connector) buildNamespaceWorkloads(
	pods []*corev1.Pod,
	deployments []*appsv1.Deployment,
	statefulSets []*appsv1.StatefulSet,
	daemonSets []*appsv1.DaemonSet,
) []models.NamespaceWorkload {
	// Build pod lookup: namespace -> pod name -> pod
	podByNS := make(map[string][]*corev1.Pod)
	for _, pod := range pods {
		podByNS[pod.Namespace] = append(podByNS[pod.Namespace], pod)
	}

	// Build replicaset lookup for deployment->pod resolution
	var replicaSets []*appsv1.ReplicaSet
	if c.replicaSetLister != nil {
		replicaSets, _ = c.replicaSetLister.List(everythingSelector())
	}
	// Map deployment name -> replicaset names
	deployRS := make(map[string][]string) // key: "ns/deployName"
	rsNames := make(map[string]bool)
	for _, rs := range replicaSets {
		for _, ref := range rs.OwnerReferences {
			if ref.Kind == "Deployment" {
				key := rs.Namespace + "/" + ref.Name
				deployRS[key] = append(deployRS[key], rs.Name)
				rsNames[rs.Namespace+"/"+rs.Name] = true
			}
		}
	}

	// Get pod metrics for CPU/memory usage
	var podMetrics map[string]*models.MetricPoint
	if c.collector != nil {
		podMetrics = c.collector.GetAllPodMetrics()
	}

	nsWorkloads := make(map[string][]models.WorkloadSummary)

	// Helper to add a pod's resource data to a workload summary
	addPodToWorkload := func(ws *models.WorkloadSummary, pod *corev1.Pod) {
		ready := isPodReady(pod)
		ws.Pods = append(ws.Pods, models.PodSummary{
			Name:   pod.Name,
			Status: string(pod.Status.Phase),
			Ready:  ready,
		})
		// Accumulate requests/limits from containers
		for _, c := range pod.Spec.Containers {
			ws.CPU.Requested += c.Resources.Requests.Cpu().MilliValue()
			ws.CPU.Limit += c.Resources.Limits.Cpu().MilliValue()
			ws.Memory.Requested += c.Resources.Requests.Memory().Value()
			ws.Memory.Limit += c.Resources.Limits.Memory().Value()
		}
		// Accumulate used from metrics
		if podMetrics != nil {
			key := pod.Namespace + "/" + pod.Name
			if m, ok := podMetrics[key]; ok {
				ws.CPU.Used += m.CPUUsage
				ws.Memory.Used += m.MemUsage
			}
		}
	}

	// Get total cluster capacity for fallback when no limits/requests
	var nodes []*corev1.Node
	if c.nodeLister != nil {
		nodes, _ = c.nodeLister.List(everythingSelector())
	}
	var totalCPUCapacity, totalMemCapacity int64
	for _, node := range nodes {
		totalCPUCapacity += node.Status.Allocatable.Cpu().MilliValue()
		totalMemCapacity += node.Status.Allocatable.Memory().Value()
	}

	// Compute percentages for a workload after all pods are added
	finalizeWorkload := func(ws *models.WorkloadSummary) {
		// CPU: use limit, then requested, then cluster capacity as denominator
		cpuDenom := ws.CPU.Limit
		if cpuDenom == 0 {
			cpuDenom = ws.CPU.Requested
		}
		if cpuDenom == 0 {
			cpuDenom = totalCPUCapacity
		}
		ws.CPU.Allocatable = cpuDenom
		if cpuDenom > 0 {
			ws.CPU.PercentUsed = float64(ws.CPU.Used) / float64(cpuDenom) * 100
			if ws.CPU.Requested > 0 {
				ws.CPU.PercentRequested = float64(ws.CPU.Requested) / float64(cpuDenom) * 100
			}
		}
		// Memory: same logic
		memDenom := ws.Memory.Limit
		if memDenom == 0 {
			memDenom = ws.Memory.Requested
		}
		if memDenom == 0 {
			memDenom = totalMemCapacity
		}
		ws.Memory.Allocatable = memDenom
		if memDenom > 0 {
			ws.Memory.PercentUsed = float64(ws.Memory.Used) / float64(memDenom) * 100
			if ws.Memory.Requested > 0 {
				ws.Memory.PercentRequested = float64(ws.Memory.Requested) / float64(memDenom) * 100
			}
		}
	}

	// Deployments
	for _, d := range deployments {
		ws := models.WorkloadSummary{
			Name:          d.Name,
			Kind:          "Deployment",
			Namespace:     d.Namespace,
			Replicas:      d.Status.Replicas,
			ReadyReplicas: d.Status.ReadyReplicas,
			Status:        deploymentStatus(d),
			Pods:          []models.PodSummary{},
		}
		rsKey := d.Namespace + "/" + d.Name
		rsNameList := deployRS[rsKey]
		for _, pod := range podByNS[d.Namespace] {
			for _, ref := range pod.OwnerReferences {
				if ref.Kind == "ReplicaSet" {
					for _, rsName := range rsNameList {
						if ref.Name == rsName {
							addPodToWorkload(&ws, pod)
						}
					}
				}
			}
		}
		finalizeWorkload(&ws)
		nsWorkloads[d.Namespace] = append(nsWorkloads[d.Namespace], ws)
	}

	// StatefulSets
	for _, ss := range statefulSets {
		ws := models.WorkloadSummary{
			Name:          ss.Name,
			Kind:          "StatefulSet",
			Namespace:     ss.Namespace,
			Replicas:      ss.Status.Replicas,
			ReadyReplicas: ss.Status.ReadyReplicas,
			Status:        fmt.Sprintf("%d/%d", ss.Status.ReadyReplicas, ss.Status.Replicas),
			Pods:          []models.PodSummary{},
		}
		for _, pod := range podByNS[ss.Namespace] {
			for _, ref := range pod.OwnerReferences {
				if ref.Kind == "StatefulSet" && ref.Name == ss.Name {
					addPodToWorkload(&ws, pod)
				}
			}
		}
		finalizeWorkload(&ws)
		nsWorkloads[ss.Namespace] = append(nsWorkloads[ss.Namespace], ws)
	}

	// DaemonSets
	for _, ds := range daemonSets {
		ws := models.WorkloadSummary{
			Name:          ds.Name,
			Kind:          "DaemonSet",
			Namespace:     ds.Namespace,
			Replicas:      ds.Status.DesiredNumberScheduled,
			ReadyReplicas: ds.Status.NumberReady,
			Status:        fmt.Sprintf("%d/%d", ds.Status.NumberReady, ds.Status.DesiredNumberScheduled),
			Pods:          []models.PodSummary{},
		}
		for _, pod := range podByNS[ds.Namespace] {
			for _, ref := range pod.OwnerReferences {
				if ref.Kind == "DaemonSet" && ref.Name == ds.Name {
					addPodToWorkload(&ws, pod)
				}
			}
		}
		finalizeWorkload(&ws)
		nsWorkloads[ds.Namespace] = append(nsWorkloads[ds.Namespace], ws)
	}

	// Convert map to sorted slice
	result := make([]models.NamespaceWorkload, 0, len(nsWorkloads))
	for ns, workloads := range nsWorkloads {
		result = append(result, models.NamespaceWorkload{
			Namespace: ns,
			Workloads: workloads,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Namespace < result[j].Namespace
	})
	return result
}

func isPodReady(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if !cs.Ready {
			return false
		}
	}
	return true
}

// GetHealth returns the cluster health status.
func (c *Connector) GetHealth(metricsAvailable bool, insights []models.Insight) models.ClusterHealth {
	health := c.buildHealth()

	for _, insight := range insights {
		if insight.Resolved {
			continue
		}
		switch insight.Severity {
		case "critical":
			health.Insights.Critical++
		case "warning":
			health.Insights.Warning++
		case "info":
			health.Insights.Info++
		}
	}

	if health.Insights.Critical > 0 {
		health.Status = "critical"
		if health.Score > 50 {
			health.Score = 50
		}
	} else if health.Insights.Warning > 0 && health.Status == "healthy" {
		health.Status = "warning"
		if health.Score > 80 {
			health.Score = 80
		}
	}

	return health
}

// GetResources returns a paginated, filtered, sorted list of resources.
func (c *Connector) GetResources(resourceType, namespace, search, status, sortBy, order string, page, limit int) models.ResourceList {
	// Check permission for this resource type (gateway types use dynamic client, skip check)
	permKey := resourceType
	if permKey == "persistentvolumeclaims" {
		permKey = "pvcs"
	} else if permKey == "persistentvolumes" {
		permKey = "pvs"
	} else if permKey == "horizontalpodautoscalers" {
		permKey = "hpas"
	}
	if permKey != "gateways" && permKey != "httproutes" && !c.permissions.CanListWatch(permKey) {
		return models.ResourceList{Kind: resourceType, Items: []map[string]interface{}{}, Total: 0, Forbidden: true}
	}

	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 50
	}

	var items []map[string]interface{}

	switch resourceType {
	case "pods":
		items = c.listPods(namespace)
	case "deployments":
		items = c.listDeployments(namespace)
	case "statefulsets":
		items = c.listStatefulSets(namespace)
	case "daemonsets":
		items = c.listDaemonSets(namespace)
	case "replicasets":
		items = c.listReplicaSets(namespace)
	case "jobs":
		items = c.listJobs(namespace)
	case "cronjobs":
		items = c.listCronJobs(namespace)
	case "services":
		items = c.listServices(namespace)
	case "ingresses":
		items = c.listIngresses(namespace)
	case "gateways":
		items = c.listGatewayResources("gateways", namespace)
	case "httproutes":
		items = c.listGatewayResources("httproutes", namespace)
	case "configmaps":
		items = c.listConfigMaps(namespace)
	case "secrets":
		items = c.listSecrets(namespace)
	case "pvcs", "persistentvolumeclaims":
		items = c.listPVCs(namespace)
	case "pvs", "persistentvolumes":
		items = c.listPVs()
	case "storageclasses":
		items = c.listStorageClasses()
	case "nodes":
		items = c.listNodes()
	case "namespaces":
		items = c.listNamespaces()
	case "hpas", "horizontalpodautoscalers":
		items = c.listHPAs(namespace)
	case "events":
		items = c.listEventsAsResources(namespace)
	case "roles":
		items = c.listRoles(namespace)
	case "clusterroles":
		items = c.listClusterRoles()
	case "rolebindings":
		items = c.listRoleBindings(namespace)
	case "clusterrolebindings":
		items = c.listClusterRoleBindings()
	default:
		return models.ResourceList{Kind: resourceType, Items: []map[string]interface{}{}, Total: 0}
	}

	// Filter by search
	if search != "" {
		search = strings.ToLower(search)
		filtered := items[:0]
		for _, item := range items {
			name, _ := item["name"].(string)
			ns, _ := item["namespace"].(string)
			if strings.Contains(strings.ToLower(name), search) || strings.Contains(strings.ToLower(ns), search) {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}

	// Filter by status
	if status != "" {
		status = strings.ToLower(status)
		filtered := items[:0]
		for _, item := range items {
			s, _ := item["status"].(string)
			if strings.ToLower(s) == status {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}

	// Sort
	if sortBy != "" {
		sort.Slice(items, func(i, j int) bool {
			vi := fmt.Sprintf("%v", items[i][sortBy])
			vj := fmt.Sprintf("%v", items[j][sortBy])
			if order == "desc" {
				return vi > vj
			}
			return vi < vj
		})
	}

	total := len(items)

	// Paginate
	start := (page - 1) * limit
	if start > total {
		start = total
	}
	end := start + limit
	if end > total {
		end = total
	}
	items = items[start:end]

	if items == nil {
		items = []map[string]interface{}{}
	}

	return models.ResourceList{
		Kind:  resourceType,
		Items: items,
		Total: total,
	}
}

// GetResourceDetail returns a single resource by type, namespace, and name.
func (c *Connector) GetResourceDetail(resourceType, namespace, name string) (map[string]interface{}, error) {
	permKey := resourceType
	if permKey == "persistentvolumeclaims" {
		permKey = "pvcs"
	} else if permKey == "persistentvolumes" {
		permKey = "pvs"
	} else if permKey == "horizontalpodautoscalers" {
		permKey = "hpas"
	}
	if permKey != "gateways" && permKey != "httproutes" && !c.permissions.CanListWatch(permKey) {
		return nil, &PermissionDeniedError{Resource: resourceType}
	}

	switch resourceType {
	case "pods":
		pod, err := c.podLister.Pods(namespace).Get(name)
		if err != nil {
			return nil, err
		}
		return podToMap(pod), nil
	case "deployments":
		d, err := c.deploymentLister.Deployments(namespace).Get(name)
		if err != nil {
			return nil, err
		}
		return deploymentToMap(d), nil
	case "services":
		svc, err := c.serviceLister.Services(namespace).Get(name)
		if err != nil {
			return nil, err
		}
		return serviceToMap(svc), nil
	case "nodes":
		node, err := c.nodeLister.Get(name)
		if err != nil {
			return nil, err
		}
		return nodeToMap(node), nil
	case "namespaces":
		ns, err := c.namespaceLister.Get(name)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"name":        ns.Name,
			"namespace":   "",
			"status":      string(ns.Status.Phase),
			"labels":      safeLabels(ns.Labels),
			"annotations": safeAnnotations(ns.Annotations),
			"createdAt":   ns.CreationTimestamp.Time.Format(time.RFC3339),
			"age":         formatAge(ns.CreationTimestamp.Time),
		}, nil
	case "statefulsets":
		ss, err := c.statefulSetLister.StatefulSets(namespace).Get(name)
		if err != nil {
			return nil, err
		}
		return statefulSetToMap(ss), nil
	case "daemonsets":
		ds, err := c.daemonSetLister.DaemonSets(namespace).Get(name)
		if err != nil {
			return nil, err
		}
		return daemonSetToMap(ds), nil
	case "jobs":
		job, err := c.jobLister.Jobs(namespace).Get(name)
		if err != nil {
			return nil, err
		}
		return jobToMap(job), nil
	case "cronjobs":
		cj, err := c.cronJobLister.CronJobs(namespace).Get(name)
		if err != nil {
			return nil, err
		}
		return cronJobToMap(cj), nil
	case "ingresses":
		ing, err := c.ingressLister.Ingresses(namespace).Get(name)
		if err != nil {
			return nil, err
		}
		return ingressToMap(ing), nil
	case "configmaps":
		cm, err := c.configMapLister.ConfigMaps(namespace).Get(name)
		if err != nil {
			return nil, err
		}
		return configMapToMap(cm), nil
	case "secrets":
		sec, err := c.secretLister.Secrets(namespace).Get(name)
		if err != nil {
			return nil, err
		}
		return secretToMap(sec), nil
	case "pvcs", "persistentvolumeclaims":
		pvc, err := c.pvcLister.PersistentVolumeClaims(namespace).Get(name)
		if err != nil {
			return nil, err
		}
		return pvcToMap(pvc), nil
	case "pvs", "persistentvolumes":
		pv, err := c.pvLister.Get(name)
		if err != nil {
			return nil, err
		}
		return pvToMap(pv), nil
	case "hpas", "horizontalpodautoscalers":
		hpa, err := c.hpaLister.HorizontalPodAutoscalers(namespace).Get(name)
		if err != nil {
			return nil, err
		}
		return hpaToMap(hpa), nil
	case "storageclasses":
		sc, err := c.storageClassLister.Get(name)
		if err != nil {
			return nil, err
		}
		reclaimPolicy := ""
		if sc.ReclaimPolicy != nil {
			reclaimPolicy = string(*sc.ReclaimPolicy)
		}
		volumeBindingMode := ""
		if sc.VolumeBindingMode != nil {
			volumeBindingMode = string(*sc.VolumeBindingMode)
		}
		return map[string]interface{}{
			"name":              sc.Name,
			"namespace":         "",
			"status":            "Active",
			"provisioner":       sc.Provisioner,
			"reclaimPolicy":     reclaimPolicy,
			"volumeBindingMode": volumeBindingMode,
			"labels":            safeLabels(sc.Labels),
			"annotations":       safeAnnotations(sc.Annotations),
			"createdAt":         sc.CreationTimestamp.Time.Format(time.RFC3339),
			"age":               formatAge(sc.CreationTimestamp.Time),
		}, nil
	case "replicasets":
		rs, err := c.replicaSetLister.ReplicaSets(namespace).Get(name)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"name":             rs.Name,
			"namespace":        rs.Namespace,
			"status":           fmt.Sprintf("%d/%d", rs.Status.ReadyReplicas, *rs.Spec.Replicas),
			"replicas":         rs.Status.Replicas,
			"readyReplicas":    rs.Status.ReadyReplicas,
			"availableReplicas": rs.Status.AvailableReplicas,
			"labels":           safeLabels(rs.Labels),
			"annotations":      safeAnnotations(rs.Annotations),
			"ownerReferences":  ownerRefsToSlice(rs.OwnerReferences),
			"createdAt":        rs.CreationTimestamp.Time.Format(time.RFC3339),
			"age":              formatAge(rs.CreationTimestamp.Time),
		}, nil
	case "endpoints":
		// EndpointSlices don't map 1:1 to a single "endpoint" resource;
		// return the slice matching the given name.
		slices, err := c.endpointSliceLister.EndpointSlices(namespace).List(everythingSelector())
		if err != nil {
			return nil, err
		}
		for _, es := range slices {
			if es.Name == name {
				var endpoints []string
				for _, ep := range es.Endpoints {
					endpoints = append(endpoints, ep.Addresses...)
				}
				var ports []map[string]interface{}
				for _, p := range es.Ports {
					ports = append(ports, map[string]interface{}{
						"name":     ptrStr(p.Name),
						"port":     *p.Port,
						"protocol": string(*p.Protocol),
					})
				}
				return map[string]interface{}{
					"name":        es.Name,
					"namespace":   es.Namespace,
					"status":      "Active",
					"addresses":   endpoints,
					"ports":       ports,
					"labels":      safeLabels(es.Labels),
					"annotations": safeAnnotations(es.Annotations),
					"createdAt":   es.CreationTimestamp.Time.Format(time.RFC3339),
					"age":         formatAge(es.CreationTimestamp.Time),
				}, nil
			}
		}
		return nil, fmt.Errorf("endpoint %s/%s not found", namespace, name)
	case "gateways", "httproutes":
		items := c.listGatewayResources(resourceType, namespace)
		for _, item := range items {
			if item["name"] == name {
				return item, nil
			}
		}
		return nil, fmt.Errorf("%s %s/%s not found", resourceType, namespace, name)
	default:
		return nil, fmt.Errorf("unsupported resource type: %s", resourceType)
	}
}

// GetTopology returns the current topology.
func (c *Connector) GetTopology() models.Topology {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.graph.GetTopology()
}

// GetEvents returns filtered events.
func (c *Connector) GetEvents(eventType, namespace, involvedKind, involvedName string, limit int) models.ResourceList {
	events, _ := c.eventLister.List(everythingSelector())
	var items []map[string]interface{}
	for _, event := range events {
		if namespace != "" && event.Namespace != namespace {
			continue
		}
		if eventType != "" && event.Type != eventType {
			continue
		}
		if involvedKind != "" && event.InvolvedObject.Kind != involvedKind {
			continue
		}
		if involvedName != "" && event.InvolvedObject.Name != involvedName {
			continue
		}
		source := ""
		if event.Source.Component != "" {
			source = event.Source.Component
			if event.Source.Host != "" {
				source += "/" + event.Source.Host
			}
		}
		items = append(items, map[string]interface{}{
			"name":      event.Name,
			"namespace": event.Namespace,
			"type":      event.Type,
			"reason":    event.Reason,
			"message":   event.Message,
			"object":    event.InvolvedObject.Kind + "/" + event.InvolvedObject.Name,
			"count":     event.Count,
			"source":    source,
			"firstSeen": event.FirstTimestamp.Time,
			"lastSeen":  event.LastTimestamp.Time,
		})
	}

	// Sort by lastSeen descending
	sort.Slice(items, func(i, j int) bool {
		ti, _ := items[i]["lastSeen"].(time.Time)
		tj, _ := items[j]["lastSeen"].(time.Time)
		return ti.After(tj)
	})

	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	if items == nil {
		items = []map[string]interface{}{}
	}

	return models.ResourceList{
		Kind:  "events",
		Items: items,
		Total: len(items),
	}
}

// GetNamespaces returns all namespaces.
func (c *Connector) GetNamespaces() models.ResourceList {
	return c.GetResources("namespaces", "", "", "", "name", "asc", 1, 1000)
}


func everythingSelector() labels.Selector {
	return labels.Everything()
}

func deploymentStatus(d *appsv1.Deployment) string {
	if d.Status.AvailableReplicas == 0 && d.Status.Replicas > 0 {
		return "Error"
	}
	if d.Status.AvailableReplicas < d.Status.Replicas {
		return "Warning"
	}
	return "Running"
}

// --- Resource to map converters ---

func formatAge(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func safeLabels(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func safeAnnotations(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	filtered := make(map[string]string, len(m))
	for k, v := range m {
		if k == "kubectl.kubernetes.io/last-applied-configuration" {
			continue
		}
		filtered[k] = v
	}
	return filtered
}

func podToMap(pod *corev1.Pod) map[string]interface{} {
	restarts := int32(0)
	ready := 0
	total := len(pod.Status.ContainerStatuses)
	for _, cs := range pod.Status.ContainerStatuses {
		restarts += cs.RestartCount
		if cs.Ready {
			ready++
		}
	}
	status := string(pod.Status.Phase)
	// Check for waiting container states
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
			status = cs.State.Waiting.Reason
			break
		}
	}
	startTime := ""
	if pod.Status.StartTime != nil {
		startTime = pod.Status.StartTime.Time.Format(time.RFC3339)
	}
	return map[string]interface{}{
		"name":            pod.Name,
		"namespace":       pod.Namespace,
		"uid":             string(pod.UID),
		"status":          status,
		"ready":           fmt.Sprintf("%d/%d", ready, total),
		"restarts":        restarts,
		"nodeName":        pod.Spec.NodeName,
		"labels":          safeLabels(pod.Labels),
		"annotations":     safeAnnotations(pod.Annotations),
		"createdAt":       pod.CreationTimestamp.Time.Format(time.RFC3339),
		"age":             formatAge(pod.CreationTimestamp.Time),
		"ip":              pod.Status.PodIP,
		"hostIP":          pod.Status.HostIP,
		"startTime":       startTime,
		"ownerReferences": ownerRefsToSlice(pod.OwnerReferences),
		"conditions":      podConditionsToSlice(pod.Status.Conditions),
		"volumes":         volumesToSlice(pod.Spec.Volumes),
		"containers":      containerSpecs(pod),
	}
}

func containerSpecs(pod *corev1.Pod) []map[string]interface{} {
	var containers []map[string]interface{}
	for _, c := range pod.Spec.Containers {
		stateInfo := containerStateFromStatus(c.Name, pod.Status.ContainerStatuses)
		isReady, _ := stateInfo["ready"].(bool)
		containers = append(containers, map[string]interface{}{
			"name":            c.Name,
			"image":           c.Image,
			"ports":           c.Ports,
			"imagePullPolicy": string(c.ImagePullPolicy),
			"resources":       containerResourcesToMap(c.Resources),
			"volumeMounts":    volumeMountsToSlice(c.VolumeMounts),
			"state":           stateInfo,
			"ready":           isReady,
		})
	}
	return containers
}

func deploymentToMap(d *appsv1.Deployment) map[string]interface{} {
	return map[string]interface{}{
		"name":              d.Name,
		"namespace":         d.Namespace,
		"status":            deploymentStatus(d),
		"replicas":          d.Status.Replicas,
		"specReplicas":      int32PtrOrZero(d.Spec.Replicas),
		"readyReplicas":     d.Status.ReadyReplicas,
		"availableReplicas": d.Status.AvailableReplicas,
		"updatedReplicas":   d.Status.UpdatedReplicas,
		"labels":            safeLabels(d.Labels),
		"annotations":       safeAnnotations(d.Annotations),
		"selector":          d.Spec.Selector,
		"strategy":          string(d.Spec.Strategy.Type),
		"createdAt":         d.CreationTimestamp.Time.Format(time.RFC3339),
		"age":               formatAge(d.CreationTimestamp.Time),
		"ownerReferences":   ownerRefsToSlice(d.OwnerReferences),
		"conditions":        deploymentConditionsToSlice(d.Status.Conditions),
		"containers":        templateContainerSpecs(d.Spec.Template.Spec.Containers),
	}
}

func serviceToMap(svc *corev1.Service) map[string]interface{} {
	var ports []map[string]interface{}
	for _, p := range svc.Spec.Ports {
		ports = append(ports, map[string]interface{}{
			"name":       p.Name,
			"port":       p.Port,
			"targetPort": p.TargetPort.String(),
			"protocol":   string(p.Protocol),
		})
	}
	return map[string]interface{}{
		"name":            svc.Name,
		"namespace":       svc.Namespace,
		"status":          string(svc.Spec.Type),
		"type":            string(svc.Spec.Type),
		"clusterIP":       svc.Spec.ClusterIP,
		"ports":           ports,
		"selector":        svc.Spec.Selector,
		"labels":          safeLabels(svc.Labels),
		"annotations":     safeAnnotations(svc.Annotations),
		"createdAt":       svc.CreationTimestamp.Time.Format(time.RFC3339),
		"age":             formatAge(svc.CreationTimestamp.Time),
		"ownerReferences": ownerRefsToSlice(svc.OwnerReferences),
	}
}

func nodeToMap(node *corev1.Node) map[string]interface{} {
	status := "NotReady"
	for _, cond := range node.Status.Conditions {
		if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
			status = "Ready"
			break
		}
	}
	return map[string]interface{}{
		"name":              node.Name,
		"namespace":         "",
		"status":            status,
		"labels":            safeLabels(node.Labels),
		"annotations":       safeAnnotations(node.Annotations),
		"createdAt":         node.CreationTimestamp.Time.Format(time.RFC3339),
		"age":               formatAge(node.CreationTimestamp.Time),
		"kubeletVersion":    node.Status.NodeInfo.KubeletVersion,
		"osImage":           node.Status.NodeInfo.OSImage,
		"containerRuntime":  node.Status.NodeInfo.ContainerRuntimeVersion,
		"cpuCapacity":       node.Status.Capacity.Cpu().MilliValue(),
		"memoryCapacity":    node.Status.Capacity.Memory().Value(),
		"cpuAllocatable":    node.Status.Allocatable.Cpu().MilliValue(),
		"memoryAllocatable": node.Status.Allocatable.Memory().Value(),
		"conditions":        nodeConditionsToSlice(node.Status.Conditions),
	}
}

func statefulSetToMap(ss *appsv1.StatefulSet) map[string]interface{} {
	return map[string]interface{}{
		"name":            ss.Name,
		"namespace":       ss.Namespace,
		"status":          fmt.Sprintf("%d/%d", ss.Status.ReadyReplicas, ss.Status.Replicas),
		"replicas":        ss.Status.Replicas,
		"specReplicas":    int32PtrOrZero(ss.Spec.Replicas),
		"readyReplicas":   ss.Status.ReadyReplicas,
		"labels":          safeLabels(ss.Labels),
		"annotations":     safeAnnotations(ss.Annotations),
		"createdAt":       ss.CreationTimestamp.Time.Format(time.RFC3339),
		"age":             formatAge(ss.CreationTimestamp.Time),
		"ownerReferences": ownerRefsToSlice(ss.OwnerReferences),
		"containers":      templateContainerSpecs(ss.Spec.Template.Spec.Containers),
	}
}

func daemonSetToMap(ds *appsv1.DaemonSet) map[string]interface{} {
	return map[string]interface{}{
		"name":            ds.Name,
		"namespace":       ds.Namespace,
		"status":          fmt.Sprintf("%d/%d", ds.Status.NumberReady, ds.Status.DesiredNumberScheduled),
		"desired":         ds.Status.DesiredNumberScheduled,
		"specReplicas":    ds.Status.DesiredNumberScheduled,
		"ready":           ds.Status.NumberReady,
		"numberAvailable": ds.Status.NumberAvailable,
		"labels":          safeLabels(ds.Labels),
		"annotations":     safeAnnotations(ds.Annotations),
		"createdAt":       ds.CreationTimestamp.Time.Format(time.RFC3339),
		"age":             formatAge(ds.CreationTimestamp.Time),
		"ownerReferences": ownerRefsToSlice(ds.OwnerReferences),
		"containers":      templateContainerSpecs(ds.Spec.Template.Spec.Containers),
	}
}

func jobToMap(job *batchv1.Job) map[string]interface{} {
	status := "Running"
	if job.Status.Succeeded > 0 {
		status = "Complete"
	} else if job.Status.Failed > 0 {
		status = "Failed"
	}
	return map[string]interface{}{
		"name":            job.Name,
		"namespace":       job.Namespace,
		"status":          status,
		"succeeded":       job.Status.Succeeded,
		"failed":          job.Status.Failed,
		"active":          job.Status.Active,
		"labels":          safeLabels(job.Labels),
		"annotations":     safeAnnotations(job.Annotations),
		"createdAt":       job.CreationTimestamp.Time.Format(time.RFC3339),
		"age":             formatAge(job.CreationTimestamp.Time),
		"ownerReferences": ownerRefsToSlice(job.OwnerReferences),
		"conditions":      jobConditionsToSlice(job.Status.Conditions),
	}
}

func cronJobToMap(cj *batchv1.CronJob) map[string]interface{} {
	var lastSchedule *time.Time
	if cj.Status.LastScheduleTime != nil {
		t := cj.Status.LastScheduleTime.Time
		lastSchedule = &t
	}
	return map[string]interface{}{
		"name":             cj.Name,
		"namespace":        cj.Namespace,
		"status":           "Scheduled",
		"schedule":         cj.Spec.Schedule,
		"lastScheduleTime": lastSchedule,
		"suspend":          cj.Spec.Suspend != nil && *cj.Spec.Suspend,
		"activeJobs":       len(cj.Status.Active),
		"labels":           safeLabels(cj.Labels),
		"annotations":      safeAnnotations(cj.Annotations),
		"createdAt":        cj.CreationTimestamp.Time.Format(time.RFC3339),
		"age":              formatAge(cj.CreationTimestamp.Time),
		"ownerReferences":  ownerRefsToSlice(cj.OwnerReferences),
	}
}

func ingressToMap(ing *networkingv1.Ingress) map[string]interface{} {
	var hosts []string
	for _, rule := range ing.Spec.Rules {
		if rule.Host != "" {
			hosts = append(hosts, rule.Host)
		}
	}
	return map[string]interface{}{
		"name":            ing.Name,
		"namespace":       ing.Namespace,
		"status":          "Active",
		"hosts":           hosts,
		"labels":          safeLabels(ing.Labels),
		"annotations":     safeAnnotations(ing.Annotations),
		"createdAt":       ing.CreationTimestamp.Time.Format(time.RFC3339),
		"age":             formatAge(ing.CreationTimestamp.Time),
		"ownerReferences": ownerRefsToSlice(ing.OwnerReferences),
	}
}

func configMapToMap(cm *corev1.ConfigMap) map[string]interface{} {
	keys := make([]string, 0, len(cm.Data))
	for k := range cm.Data {
		keys = append(keys, k)
	}
	return map[string]interface{}{
		"name":            cm.Name,
		"namespace":       cm.Namespace,
		"status":          "Active",
		"keys":            keys,
		"dataCount":       len(cm.Data),
		"labels":          safeLabels(cm.Labels),
		"annotations":     safeAnnotations(cm.Annotations),
		"createdAt":       cm.CreationTimestamp.Time.Format(time.RFC3339),
		"age":             formatAge(cm.CreationTimestamp.Time),
		"ownerReferences": ownerRefsToSlice(cm.OwnerReferences),
	}
}

// secretToMap intentionally does NOT expose secret values.
func secretToMap(sec *corev1.Secret) map[string]interface{} {
	keys := make([]string, 0, len(sec.Data))
	for k := range sec.Data {
		keys = append(keys, k)
	}
	return map[string]interface{}{
		"name":            sec.Name,
		"namespace":       sec.Namespace,
		"status":          "Active",
		"type":            string(sec.Type),
		"keys":            keys,
		"dataCount":       len(sec.Data),
		"labels":          safeLabels(sec.Labels),
		"annotations":     safeAnnotations(sec.Annotations),
		"createdAt":       sec.CreationTimestamp.Time.Format(time.RFC3339),
		"age":             formatAge(sec.CreationTimestamp.Time),
		"ownerReferences": ownerRefsToSlice(sec.OwnerReferences),
	}
}

func pvcToMap(pvc *corev1.PersistentVolumeClaim) map[string]interface{} {
	storage := ""
	if pvc.Spec.Resources.Requests != nil {
		if q, ok := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
			storage = q.String()
		}
	}
	return map[string]interface{}{
		"name":            pvc.Name,
		"namespace":       pvc.Namespace,
		"status":          string(pvc.Status.Phase),
		"volumeName":      pvc.Spec.VolumeName,
		"storageClass":    ptrStr(pvc.Spec.StorageClassName),
		"capacity":        storage,
		"accessModes":     pvc.Spec.AccessModes,
		"labels":          safeLabels(pvc.Labels),
		"annotations":     safeAnnotations(pvc.Annotations),
		"createdAt":       pvc.CreationTimestamp.Time.Format(time.RFC3339),
		"age":             formatAge(pvc.CreationTimestamp.Time),
		"ownerReferences": ownerRefsToSlice(pvc.OwnerReferences),
	}
}

func pvToMap(pv *corev1.PersistentVolume) map[string]interface{} {
	return map[string]interface{}{
		"name":          pv.Name,
		"namespace":     "",
		"status":        string(pv.Status.Phase),
		"capacity":      pv.Spec.Capacity.StorageEphemeral().String(),
		"storageClass":  pv.Spec.StorageClassName,
		"accessModes":   pv.Spec.AccessModes,
		"reclaimPolicy": string(pv.Spec.PersistentVolumeReclaimPolicy),
		"labels":        safeLabels(pv.Labels),
		"annotations":   safeAnnotations(pv.Annotations),
		"createdAt":     pv.CreationTimestamp.Time.Format(time.RFC3339),
		"age":           formatAge(pv.CreationTimestamp.Time),
	}
}

func hpaToMap(hpa *autoscalingv1.HorizontalPodAutoscaler) map[string]interface{} {
	return map[string]interface{}{
		"name":            hpa.Name,
		"namespace":       hpa.Namespace,
		"status":          fmt.Sprintf("%d/%d", hpa.Status.CurrentReplicas, hpa.Spec.MaxReplicas),
		"minReplicas":     ptrInt32(hpa.Spec.MinReplicas),
		"maxReplicas":     hpa.Spec.MaxReplicas,
		"currentReplicas": hpa.Status.CurrentReplicas,
		"desiredReplicas": hpa.Status.DesiredReplicas,
		"targetRef":       hpa.Spec.ScaleTargetRef.Kind + "/" + hpa.Spec.ScaleTargetRef.Name,
		"labels":          safeLabels(hpa.Labels),
		"annotations":     safeAnnotations(hpa.Annotations),
		"createdAt":       hpa.CreationTimestamp.Time.Format(time.RFC3339),
		"age":             formatAge(hpa.CreationTimestamp.Time),
		"ownerReferences": ownerRefsToSlice(hpa.OwnerReferences),
	}
}

func ptrStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func ptrInt32(p *int32) int32 {
	if p == nil {
		return 0
	}
	return *p
}

// --- Detail helper functions ---

func ownerRefsToSlice(refs []metav1.OwnerReference) []map[string]interface{} {
	result := make([]map[string]interface{}, 0, len(refs))
	for _, ref := range refs {
		result = append(result, map[string]interface{}{
			"apiVersion": ref.APIVersion,
			"kind":       ref.Kind,
			"name":       ref.Name,
			"uid":        string(ref.UID),
		})
	}
	return result
}

func podConditionsToSlice(conditions []corev1.PodCondition) []map[string]interface{} {
	result := make([]map[string]interface{}, 0, len(conditions))
	for _, c := range conditions {
		result = append(result, map[string]interface{}{
			"type":               string(c.Type),
			"status":             string(c.Status),
			"lastTransitionTime": c.LastTransitionTime.Time.Format(time.RFC3339),
			"reason":             c.Reason,
			"message":            c.Message,
		})
	}
	return result
}

func deploymentConditionsToSlice(conditions []appsv1.DeploymentCondition) []map[string]interface{} {
	result := make([]map[string]interface{}, 0, len(conditions))
	for _, c := range conditions {
		result = append(result, map[string]interface{}{
			"type":               string(c.Type),
			"status":             string(c.Status),
			"lastTransitionTime": c.LastTransitionTime.Time.Format(time.RFC3339),
			"reason":             c.Reason,
			"message":            c.Message,
		})
	}
	return result
}

func nodeConditionsToSlice(conditions []corev1.NodeCondition) []map[string]interface{} {
	result := make([]map[string]interface{}, 0, len(conditions))
	for _, c := range conditions {
		result = append(result, map[string]interface{}{
			"type":               string(c.Type),
			"status":             string(c.Status),
			"lastTransitionTime": c.LastTransitionTime.Time.Format(time.RFC3339),
			"reason":             c.Reason,
			"message":            c.Message,
		})
	}
	return result
}

func jobConditionsToSlice(conditions []batchv1.JobCondition) []map[string]interface{} {
	result := make([]map[string]interface{}, 0, len(conditions))
	for _, c := range conditions {
		result = append(result, map[string]interface{}{
			"type":               string(c.Type),
			"status":             string(c.Status),
			"lastTransitionTime": c.LastTransitionTime.Time.Format(time.RFC3339),
			"reason":             c.Reason,
			"message":            c.Message,
		})
	}
	return result
}

func volumesToSlice(volumes []corev1.Volume) []map[string]interface{} {
	result := make([]map[string]interface{}, 0, len(volumes))
	for _, v := range volumes {
		volType := "Unknown"
		details := ""
		switch {
		case v.ConfigMap != nil:
			volType = "ConfigMap"
			details = v.ConfigMap.Name
		case v.Secret != nil:
			volType = "Secret"
			details = v.Secret.SecretName
		case v.PersistentVolumeClaim != nil:
			volType = "PVC"
			details = v.PersistentVolumeClaim.ClaimName
		case v.EmptyDir != nil:
			volType = "EmptyDir"
			details = string(v.EmptyDir.Medium)
			if details == "" {
				details = "default"
			}
		case v.HostPath != nil:
			volType = "HostPath"
			details = v.HostPath.Path
		case v.Projected != nil:
			volType = "Projected"
			var sources []string
			for _, src := range v.Projected.Sources {
				switch {
				case src.ServiceAccountToken != nil:
					sources = append(sources, "ServiceAccountToken")
				case src.ConfigMap != nil:
					sources = append(sources, "ConfigMap:"+src.ConfigMap.Name)
				case src.Secret != nil:
					sources = append(sources, "Secret:"+src.Secret.Name)
				case src.DownwardAPI != nil:
					sources = append(sources, "DownwardAPI")
				}
			}
			details = strings.Join(sources, ", ")
		case v.DownwardAPI != nil:
			volType = "DownwardAPI"
		case v.CSI != nil:
			volType = "CSI"
			details = v.CSI.Driver
		case v.NFS != nil:
			volType = "NFS"
			details = v.NFS.Server + ":" + v.NFS.Path
		}
		result = append(result, map[string]interface{}{
			"name":    v.Name,
			"type":    volType,
			"details": details,
		})
	}
	return result
}

func volumeMountsToSlice(mounts []corev1.VolumeMount) []map[string]interface{} {
	result := make([]map[string]interface{}, 0, len(mounts))
	for _, m := range mounts {
		result = append(result, map[string]interface{}{
			"name":      m.Name,
			"mountPath": m.MountPath,
			"readOnly":  m.ReadOnly,
			"subPath":   m.SubPath,
		})
	}
	return result
}

func containerStateFromStatus(name string, statuses []corev1.ContainerStatus) map[string]interface{} {
	for _, cs := range statuses {
		if cs.Name != name {
			continue
		}
		result := map[string]interface{}{
			"ready":        cs.Ready,
			"restartCount": int(cs.RestartCount),
		}
		switch {
		case cs.State.Running != nil:
			result["state"] = "running"
			result["startedAt"] = cs.State.Running.StartedAt.Time.Format(time.RFC3339)
		case cs.State.Waiting != nil:
			result["state"] = "waiting"
			if cs.State.Waiting.Reason != "" {
				result["reason"] = cs.State.Waiting.Reason
			}
		case cs.State.Terminated != nil:
			result["state"] = "terminated"
			if cs.State.Terminated.Reason != "" {
				result["reason"] = cs.State.Terminated.Reason
			}
		default:
			result["state"] = "unknown"
		}
		return result
	}
	return map[string]interface{}{"state": "unknown", "ready": false, "restartCount": 0}
}

func containerResourcesToMap(r corev1.ResourceRequirements) map[string]interface{} {
	return map[string]interface{}{
		"cpuRequest":    r.Requests.Cpu().MilliValue(),
		"cpuLimit":      r.Limits.Cpu().MilliValue(),
		"memoryRequest": r.Requests.Memory().Value(),
		"memoryLimit":   r.Limits.Memory().Value(),
	}
}

// templateContainerSpecs turns a pod-template container list into the same
// shape the frontend already knows from pod details (name, image, resources).
// No status — templates don't have runtime state.
func templateContainerSpecs(cs []corev1.Container) []map[string]interface{} {
	if len(cs) == 0 {
		return nil
	}
	out := make([]map[string]interface{}, 0, len(cs))
	for _, c := range cs {
		out = append(out, map[string]interface{}{
			"name":            c.Name,
			"image":           c.Image,
			"imagePullPolicy": string(c.ImagePullPolicy),
			"resources":       containerResourcesToMap(c.Resources),
			"ports":           c.Ports,
		})
	}
	return out
}

func int32PtrOrZero(p *int32) int32 {
	if p == nil {
		return 0
	}
	return *p
}

// resourceTypeToGVR maps a resource type string to its GroupVersionResource.
func resourceTypeToGVR(resourceType string) (schema.GroupVersionResource, bool) {
	m := map[string]schema.GroupVersionResource{
		"pods":                  {Group: "", Version: "v1", Resource: "pods"},
		"nodes":                 {Group: "", Version: "v1", Resource: "nodes"},
		"namespaces":            {Group: "", Version: "v1", Resource: "namespaces"},
		"services":              {Group: "", Version: "v1", Resource: "services"},
		"configmaps":            {Group: "", Version: "v1", Resource: "configmaps"},
		"secrets":               {Group: "", Version: "v1", Resource: "secrets"},
		"persistentvolumeclaims": {Group: "", Version: "v1", Resource: "persistentvolumeclaims"},
		"pvcs":                  {Group: "", Version: "v1", Resource: "persistentvolumeclaims"},
		"persistentvolumes":     {Group: "", Version: "v1", Resource: "persistentvolumes"},
		"pvs":                   {Group: "", Version: "v1", Resource: "persistentvolumes"},
		"events":                {Group: "", Version: "v1", Resource: "events"},
		"deployments":           {Group: "apps", Version: "v1", Resource: "deployments"},
		"statefulsets":          {Group: "apps", Version: "v1", Resource: "statefulsets"},
		"daemonsets":            {Group: "apps", Version: "v1", Resource: "daemonsets"},
		"replicasets":           {Group: "apps", Version: "v1", Resource: "replicasets"},
		"jobs":                  {Group: "batch", Version: "v1", Resource: "jobs"},
		"cronjobs":              {Group: "batch", Version: "v1", Resource: "cronjobs"},
		"ingresses":             {Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"},
		"hpas":                  {Group: "autoscaling", Version: "v1", Resource: "horizontalpodautoscalers"},
		"horizontalpodautoscalers": {Group: "autoscaling", Version: "v1", Resource: "horizontalpodautoscalers"},
		"storageclasses":        {Group: "storage.k8s.io", Version: "v1", Resource: "storageclasses"},
		"roles":                 {Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "roles"},
		"clusterroles":          {Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"},
		"rolebindings":          {Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "rolebindings"},
		"clusterrolebindings":   {Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"},
		"gateways":              {Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways"},
		"httproutes":            {Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"},
	}
	gvr, ok := m[resourceType]
	return gvr, ok
}

// isClusterScoped returns true for resource types that are not namespaced.
// looksLikeSensitiveKey returns true if a ConfigMap key name suggests it holds a secret value.
func looksLikeSensitiveKey(key string) bool {
	k := strings.ToLower(key)
	sensitive := []string{
		"password", "passwd", "pass_", "_pass",
		"secret", "token", "api_key", "apikey",
		"access_key", "private_key", "public_key",
		"client_secret", "client_id",
		"credentials", "credential",
		"authorization",
		"connection_string", "dsn",
	}
	for _, s := range sensitive {
		if strings.Contains(k, s) {
			return true
		}
	}
	// Exact suffix matches (e.g., CRON_PASS, DB_PASS)
	suffixes := []string{"_pass", "_pwd", "_key"}
	for _, s := range suffixes {
		if strings.HasSuffix(k, s) {
			return true
		}
	}
	return false
}

// looksLikeSensitiveValue returns true if a value looks like it contains a secret,
// regardless of the key name. Detects AWS keys, long base64, connection strings, etc.
func looksLikeSensitiveValue(value string) bool {
	v := strings.TrimSpace(value)
	if len(v) == 0 {
		return false
	}
	// AWS access key IDs (AKIA...)
	if strings.HasPrefix(v, "AKIA") && len(v) == 20 {
		return true
	}
	// Long base64-encoded values (likely keys/certs) — 200+ chars of base64 alphabet
	if len(v) > 200 {
		isBase64 := true
		for _, c := range v {
			if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '+' || c == '/' || c == '=' || c == '\n') {
				isBase64 = false
				break
			}
		}
		if isBase64 {
			return true
		}
	}
	return false
}

func isClusterScoped(resourceType string) bool {
	switch resourceType {
	case "nodes", "namespaces", "persistentvolumes", "pvs", "storageclasses", "clusterroles", "clusterrolebindings":
		return true
	}
	return false
}

// AggregateWorkloadMetrics sums pod metrics for a workload (deployment, statefulset, daemonset, job).
func (c *Connector) AggregateWorkloadMetrics(resourceType, namespace, name string, col metricsCollector) map[string]interface{} {
	pods, _ := c.podLister.List(everythingSelector())
	podMetrics := col.GetAllPodMetrics()

	// For deployments, pods are owned by ReplicaSets which are owned by the Deployment.
	// For statefulsets/daemonsets, pods are directly owned.
	isOwned := func(pod *corev1.Pod) bool {
		if pod.Namespace != namespace {
			return false
		}
		for _, ref := range pod.OwnerReferences {
			switch resourceType {
			case "deployments":
				// Pod → ReplicaSet → Deployment: check if the ReplicaSet is owned by this Deployment
				if ref.Kind == "ReplicaSet" {
					rs, err := c.replicaSetLister.ReplicaSets(namespace).Get(ref.Name)
					if err == nil {
						for _, rsRef := range rs.OwnerReferences {
							if rsRef.Kind == "Deployment" && rsRef.Name == name {
								return true
							}
						}
					}
				}
			case "statefulsets":
				if ref.Kind == "StatefulSet" && ref.Name == name {
					return true
				}
			case "daemonsets":
				if ref.Kind == "DaemonSet" && ref.Name == name {
					return true
				}
			case "jobs":
				if ref.Kind == "Job" && ref.Name == name {
					return true
				}
			case "cronjobs":
				// Pod → Job → CronJob
				if ref.Kind == "Job" {
					job, err := c.jobLister.Jobs(namespace).Get(ref.Name)
					if err == nil {
						for _, jobRef := range job.OwnerReferences {
							if jobRef.Kind == "CronJob" && jobRef.Name == name {
								return true
							}
						}
					}
				}
			}
		}
		return false
	}

	var cpuUsed, memUsed, cpuReq, cpuLim, memReq, memLim int64
	for _, pod := range pods {
		if !isOwned(pod) {
			continue
		}
		for _, cont := range pod.Spec.Containers {
			cpuReq += cont.Resources.Requests.Cpu().MilliValue()
			cpuLim += cont.Resources.Limits.Cpu().MilliValue()
			memReq += cont.Resources.Requests.Memory().Value()
			memLim += cont.Resources.Limits.Memory().Value()
		}
		key := pod.Namespace + "/" + pod.Name
		if pm, ok := podMetrics[key]; ok {
			cpuUsed += pm.CPUUsage
			memUsed += pm.MemUsage
		}
	}

	if cpuUsed == 0 && memUsed == 0 {
		return nil
	}

	result := map[string]interface{}{
		"cpuUsage":    cpuUsed,
		"memoryUsage": memUsed,
	}
	cpuDenom := cpuLim
	if cpuDenom == 0 {
		cpuDenom = cpuReq
	}
	if cpuDenom > 0 {
		result["cpuPercent"] = float64(cpuUsed) / float64(cpuDenom) * 100
	}
	memDenom := memLim
	if memDenom == 0 {
		memDenom = memReq
	}
	if memDenom > 0 {
		result["memoryPercent"] = float64(memUsed) / float64(memDenom) * 100
	}
	return result
}

// GetPodLogs returns the tail of logs for a specific pod container.
// When sinceSeconds > 0, only log entries newer than that many seconds ago
// are returned (sinceSeconds is applied in addition to tailLines by the API).
func (c *Connector) GetPodLogs(namespace, name, container string, tailLines, sinceSeconds int64) (string, error) {
	opts := &corev1.PodLogOptions{
		TailLines: &tailLines,
	}
	if container != "" {
		opts.Container = container
	}
	if sinceSeconds > 0 {
		opts.SinceSeconds = &sinceSeconds
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req := c.clientset.CoreV1().Pods(namespace).GetLogs(name, opts)
	stream, err := req.Stream(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get logs: %w", err)
	}
	defer stream.Close()
	buf := new(bytes.Buffer)
	_, err = io.Copy(buf, stream)
	if err != nil {
		return "", fmt.Errorf("failed to read logs: %w", err)
	}
	return buf.String(), nil
}

// GetResourceYAML fetches a single resource via the dynamic client and returns its YAML representation.
func (c *Connector) GetResourceYAML(resourceType, namespace, name string) ([]byte, error) {
	if c.dynamicClient == nil {
		return nil, fmt.Errorf("dynamic client not available")
	}
	gvr, ok := resourceTypeToGVR(resourceType)
	if !ok {
		return nil, fmt.Errorf("unsupported resource type: %s", resourceType)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var obj *unstructured.Unstructured
	var err error
	if isClusterScoped(resourceType) {
		obj, err = c.dynamicClient.Resource(gvr).Get(ctx, name, metav1.GetOptions{})
	} else {
		obj, err = c.dynamicClient.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	}
	if err != nil {
		return nil, fmt.Errorf("fetching resource: %w", err)
	}

	// Strip managedFields and last-applied-configuration — internal metadata that adds noise
	if metadata, ok := obj.Object["metadata"].(map[string]interface{}); ok {
		delete(metadata, "managedFields")
		// Remove last-applied-configuration — it's verbose and for secrets it contains unredacted data
		if annotations, ok := metadata["annotations"].(map[string]interface{}); ok {
			delete(annotations, "kubectl.kubernetes.io/last-applied-configuration")
		}
	}

	// Redact secret data values
	if resourceType == "secrets" {
		if data, ok := obj.Object["data"].(map[string]interface{}); ok {
			for k := range data {
				data[k] = "REDACTED"
			}
		}
	}

	// Redact sensitive-looking values in ConfigMap data
	if resourceType == "configmaps" {
		if data, ok := obj.Object["data"].(map[string]interface{}); ok {
			for k, v := range data {
				if s, ok := v.(string); ok && len(s) > 0 {
					if looksLikeSensitiveKey(k) || looksLikeSensitiveValue(s) {
						data[k] = "REDACTED"
					}
				}
			}
		}
	}

	yamlBytes, err := sigsyaml.Marshal(obj.Object)
	if err != nil {
		return nil, fmt.Errorf("marshalling to YAML: %w", err)
	}
	return yamlBytes, nil
}

// DeleteResource deletes a resource using the dynamic client.
func (c *Connector) DeleteResource(resourceType, namespace, name string, propagation metav1.DeletionPropagation, gracePeriod *int64) error {
	if c.dynamicClient == nil {
		return fmt.Errorf("dynamic client not available")
	}
	gvr, ok := resourceTypeToGVR(resourceType)
	if !ok {
		return fmt.Errorf("unsupported resource type: %s", resourceType)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	opts := metav1.DeleteOptions{PropagationPolicy: &propagation}
	if gracePeriod != nil {
		opts.GracePeriodSeconds = gracePeriod
	}
	if isClusterScoped(resourceType) {
		return c.dynamicClient.Resource(gvr).Delete(ctx, name, opts)
	}
	return c.dynamicClient.Resource(gvr).Namespace(namespace).Delete(ctx, name, opts)
}

// ApplyResourceYAML updates a resource from raw YAML using the dynamic client.
func (c *Connector) ApplyResourceYAML(resourceType, namespace, name string, yamlData []byte) error {
	if c.dynamicClient == nil {
		return fmt.Errorf("dynamic client not available")
	}
	gvr, ok := resourceTypeToGVR(resourceType)
	if !ok {
		return fmt.Errorf("unsupported resource type: %s", resourceType)
	}

	// Parse YAML to unstructured object
	var rawObj map[string]interface{}
	if err := sigsyaml.Unmarshal(yamlData, &rawObj); err != nil {
		return fmt.Errorf("invalid YAML: %w", err)
	}

	obj := &unstructured.Unstructured{Object: rawObj}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if isClusterScoped(resourceType) {
		_, err := c.dynamicClient.Resource(gvr).Update(ctx, obj, metav1.UpdateOptions{})
		if err != nil {
			return err
		}
	} else {
		_, err := c.dynamicClient.Resource(gvr).Namespace(namespace).Update(ctx, obj, metav1.UpdateOptions{})
		if err != nil {
			return err
		}
	}

	return nil
}

// --- List helpers ---

func (c *Connector) listPods(namespace string) []map[string]interface{} {
	pods, _ := c.podLister.List(everythingSelector())
	var podMetrics map[string]*models.MetricPoint
	if c.collector != nil {
		podMetrics = c.collector.GetAllPodMetrics()
	}
	var items []map[string]interface{}
	for _, pod := range pods {
		if namespace != "" && pod.Namespace != namespace {
			continue
		}
		m := podToMap(pod)
		// Inject CPU/MEM metrics
		var cpuReq, cpuLim, memReq, memLim int64
		for _, cont := range pod.Spec.Containers {
			cpuReq += cont.Resources.Requests.Cpu().MilliValue()
			cpuLim += cont.Resources.Limits.Cpu().MilliValue()
			memReq += cont.Resources.Requests.Memory().Value()
			memLim += cont.Resources.Limits.Memory().Value()
		}
		m["cpuRequest"] = cpuReq
		m["cpuLimit"] = cpuLim
		m["memoryRequest"] = memReq
		m["memoryLimit"] = memLim
		if podMetrics != nil {
			key := pod.Namespace + "/" + pod.Name
			if pm, ok := podMetrics[key]; ok {
				m["cpuUsage"] = pm.CPUUsage
				m["memoryUsage"] = pm.MemUsage
				if cpuLim > 0 {
					m["cpuPercent"] = float64(pm.CPUUsage) / float64(cpuLim) * 100
				} else if cpuReq > 0 {
					m["cpuPercent"] = float64(pm.CPUUsage) / float64(cpuReq) * 100
				}
				if memLim > 0 {
					m["memoryPercent"] = float64(pm.MemUsage) / float64(memLim) * 100
				} else if memReq > 0 {
					m["memoryPercent"] = float64(pm.MemUsage) / float64(memReq) * 100
				}
			}
		}
		items = append(items, m)
	}
	return items
}

func (c *Connector) listDeployments(namespace string) []map[string]interface{} {
	list, _ := c.deploymentLister.List(everythingSelector())
	// Build RS lookup for pod resolution
	replicaSets, _ := c.replicaSetLister.List(everythingSelector())
	deployRS := make(map[string][]string)
	for _, rs := range replicaSets {
		for _, ref := range rs.OwnerReferences {
			if ref.Kind == "Deployment" {
				key := rs.Namespace + "/" + ref.Name
				deployRS[key] = append(deployRS[key], rs.Name)
			}
		}
	}
	pods, _ := c.podLister.List(everythingSelector())
	podsByNS := make(map[string][]*corev1.Pod)
	for _, pod := range pods {
		podsByNS[pod.Namespace] = append(podsByNS[pod.Namespace], pod)
	}
	var podMetrics map[string]*models.MetricPoint
	if c.collector != nil {
		podMetrics = c.collector.GetAllPodMetrics()
	}

	var items []map[string]interface{}
	for _, d := range list {
		if namespace != "" && d.Namespace != namespace {
			continue
		}
		m := deploymentToMap(d)
		// Aggregate CPU/MEM from deployment's pods
		var cpuUsed, memUsed, cpuReq, cpuLim, memReq, memLim int64
		rsKey := d.Namespace + "/" + d.Name
		rsNames := deployRS[rsKey]
		for _, pod := range podsByNS[d.Namespace] {
			for _, ref := range pod.OwnerReferences {
				if ref.Kind == "ReplicaSet" {
					for _, rsName := range rsNames {
						if ref.Name == rsName {
							for _, cont := range pod.Spec.Containers {
								cpuReq += cont.Resources.Requests.Cpu().MilliValue()
								cpuLim += cont.Resources.Limits.Cpu().MilliValue()
								memReq += cont.Resources.Requests.Memory().Value()
								memLim += cont.Resources.Limits.Memory().Value()
							}
							if podMetrics != nil {
								key := pod.Namespace + "/" + pod.Name
								if pm, ok := podMetrics[key]; ok {
									cpuUsed += pm.CPUUsage
									memUsed += pm.MemUsage
								}
							}
						}
					}
				}
			}
		}
		m["cpuUsage"] = cpuUsed
		m["memoryUsage"] = memUsed
		m["cpuRequest"] = cpuReq
		m["cpuLimit"] = cpuLim
		m["memoryRequest"] = memReq
		m["memoryLimit"] = memLim
		cpuDenom := cpuLim
		if cpuDenom == 0 {
			cpuDenom = cpuReq
		}
		if cpuDenom > 0 {
			m["cpuPercent"] = float64(cpuUsed) / float64(cpuDenom) * 100
		}
		memDenom := memLim
		if memDenom == 0 {
			memDenom = memReq
		}
		if memDenom > 0 {
			m["memoryPercent"] = float64(memUsed) / float64(memDenom) * 100
		}
		items = append(items, m)
	}
	return items
}

func (c *Connector) listStatefulSets(namespace string) []map[string]interface{} {
	list, _ := c.statefulSetLister.List(everythingSelector())
	pods, _ := c.podLister.List(everythingSelector())
	var podMetrics map[string]*models.MetricPoint
	if c.collector != nil {
		podMetrics = c.collector.GetAllPodMetrics()
	}
	var items []map[string]interface{}
	for _, ss := range list {
		if namespace != "" && ss.Namespace != namespace {
			continue
		}
		m := statefulSetToMap(ss)
		var cpuUsed, memUsed, cpuReq, cpuLim, memReq, memLim int64
		for _, pod := range pods {
			if pod.Namespace != ss.Namespace {
				continue
			}
			for _, ref := range pod.OwnerReferences {
				if ref.Kind == "StatefulSet" && ref.Name == ss.Name {
					for _, cont := range pod.Spec.Containers {
						cpuReq += cont.Resources.Requests.Cpu().MilliValue()
						cpuLim += cont.Resources.Limits.Cpu().MilliValue()
						memReq += cont.Resources.Requests.Memory().Value()
						memLim += cont.Resources.Limits.Memory().Value()
					}
					if podMetrics != nil {
						if pm, ok := podMetrics[pod.Namespace+"/"+pod.Name]; ok {
							cpuUsed += pm.CPUUsage
							memUsed += pm.MemUsage
						}
					}
				}
			}
		}
		m["cpuUsage"] = cpuUsed
		m["memoryUsage"] = memUsed
		m["cpuRequest"] = cpuReq
		m["memoryRequest"] = memReq
		denom := cpuLim
		if denom == 0 { denom = cpuReq }
		if denom > 0 { m["cpuPercent"] = float64(cpuUsed) / float64(denom) * 100 }
		denom = memLim
		if denom == 0 { denom = memReq }
		if denom > 0 { m["memoryPercent"] = float64(memUsed) / float64(denom) * 100 }
		items = append(items, m)
	}
	return items
}

func (c *Connector) listDaemonSets(namespace string) []map[string]interface{} {
	list, _ := c.daemonSetLister.List(everythingSelector())
	pods, _ := c.podLister.List(everythingSelector())
	var podMetrics map[string]*models.MetricPoint
	if c.collector != nil {
		podMetrics = c.collector.GetAllPodMetrics()
	}
	var items []map[string]interface{}
	for _, ds := range list {
		if namespace != "" && ds.Namespace != namespace {
			continue
		}
		m := daemonSetToMap(ds)
		var cpuUsed, memUsed int64
		for _, pod := range pods {
			if pod.Namespace != ds.Namespace {
				continue
			}
			for _, ref := range pod.OwnerReferences {
				if ref.Kind == "DaemonSet" && ref.Name == ds.Name {
					if podMetrics != nil {
						if pm, ok := podMetrics[pod.Namespace+"/"+pod.Name]; ok {
							cpuUsed += pm.CPUUsage
							memUsed += pm.MemUsage
						}
					}
				}
			}
		}
		m["cpuUsage"] = cpuUsed
		m["memoryUsage"] = memUsed
		items = append(items, m)
	}
	return items
}

func (c *Connector) listReplicaSets(namespace string) []map[string]interface{} {
	list, _ := c.replicaSetLister.List(everythingSelector())
	var items []map[string]interface{}
	for _, rs := range list {
		if namespace != "" && rs.Namespace != namespace {
			continue
		}
		items = append(items, map[string]interface{}{
			"name":          rs.Name,
			"namespace":     rs.Namespace,
			"status":        fmt.Sprintf("%d/%d", rs.Status.ReadyReplicas, rs.Status.Replicas),
			"replicas":      rs.Status.Replicas,
			"readyReplicas": rs.Status.ReadyReplicas,
			"labels":        safeLabels(rs.Labels),
			"annotations":   safeAnnotations(rs.Annotations),
			"createdAt":     rs.CreationTimestamp.Time.Format(time.RFC3339),
			"age":           formatAge(rs.CreationTimestamp.Time),
		})
	}
	return items
}

func (c *Connector) listJobs(namespace string) []map[string]interface{} {
	list, _ := c.jobLister.List(everythingSelector())
	var items []map[string]interface{}
	for _, job := range list {
		if namespace != "" && job.Namespace != namespace {
			continue
		}
		items = append(items, jobToMap(job))
	}
	return items
}

func (c *Connector) listCronJobs(namespace string) []map[string]interface{} {
	list, _ := c.cronJobLister.List(everythingSelector())
	var items []map[string]interface{}
	for _, cj := range list {
		if namespace != "" && cj.Namespace != namespace {
			continue
		}
		items = append(items, cronJobToMap(cj))
	}
	return items
}

func (c *Connector) listServices(namespace string) []map[string]interface{} {
	list, _ := c.serviceLister.List(everythingSelector())
	var items []map[string]interface{}
	for _, svc := range list {
		if namespace != "" && svc.Namespace != namespace {
			continue
		}
		items = append(items, serviceToMap(svc))
	}
	return items
}

func (c *Connector) listIngresses(namespace string) []map[string]interface{} {
	list, _ := c.ingressLister.List(everythingSelector())
	var items []map[string]interface{}
	for _, ing := range list {
		if namespace != "" && ing.Namespace != namespace {
			continue
		}
		items = append(items, ingressToMap(ing))
	}
	return items
}

func (c *Connector) listGatewayResources(resource, namespace string) []map[string]interface{} {
	if c.dynamicClient == nil {
		return nil
	}
	gvr := schema.GroupVersionResource{
		Group:    "gateway.networking.k8s.io",
		Version:  "v1",
		Resource: resource,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var list *unstructured.UnstructuredList
	var err error
	if namespace != "" {
		list, err = c.dynamicClient.Resource(gvr).Namespace(namespace).List(ctx, metav1.ListOptions{})
	} else {
		list, err = c.dynamicClient.Resource(gvr).List(ctx, metav1.ListOptions{})
	}
	if err != nil {
		// Gateway API not installed in this cluster — return empty
		return nil
	}
	var items []map[string]interface{}
	for _, item := range list.Items {
		m := map[string]interface{}{
			"name":        item.GetName(),
			"namespace":   item.GetNamespace(),
			"labels":      safeLabels(item.GetLabels()),
			"annotations": safeAnnotations(item.GetAnnotations()),
			"createdAt":   item.GetCreationTimestamp().Time.Format(time.RFC3339),
			"age":         formatAge(item.GetCreationTimestamp().Time),
		}
		spec, _ := item.Object["spec"].(map[string]interface{})
		status, _ := item.Object["status"].(map[string]interface{})

		if resource == "gateways" {
			m["status"] = "Active"
			if gatewayClass, ok := spec["gatewayClassName"].(string); ok {
				m["class"] = gatewayClass
			}
			// Address
			if addrs, ok := status["addresses"].([]interface{}); ok && len(addrs) > 0 {
				if addr, ok := addrs[0].(map[string]interface{}); ok {
					m["address"] = addr["value"]
				}
			}
			// Listeners
			if listeners, ok := spec["listeners"].([]interface{}); ok {
				var ports []string
				for _, l := range listeners {
					if lm, ok := l.(map[string]interface{}); ok {
						port, _ := lm["port"].(int64)
						if port == 0 {
							if pf, ok := lm["port"].(float64); ok {
								port = int64(pf)
							}
						}
						proto, _ := lm["protocol"].(string)
						name, _ := lm["name"].(string)
						if port > 0 {
							ports = append(ports, fmt.Sprintf("%s:%d/%s", name, port, proto))
						}
					}
				}
				m["listeners"] = strings.Join(ports, ", ")
			}
			// Programmed condition
			if conditions, ok := status["conditions"].([]interface{}); ok {
				for _, cond := range conditions {
					if cm, ok := cond.(map[string]interface{}); ok {
						if cm["type"] == "Programmed" {
							if cm["status"] == "True" {
								m["status"] = "Programmed"
							} else {
								m["status"] = "NotProgrammed"
							}
						}
					}
				}
			}
		} else if resource == "httproutes" {
			m["status"] = "Accepted"
			// Hostnames
			if hostnames, ok := spec["hostnames"].([]interface{}); ok {
				var hn []string
				for _, h := range hostnames {
					if s, ok := h.(string); ok {
						hn = append(hn, s)
					}
				}
				m["hostnames"] = strings.Join(hn, ", ")
			}
			// Backend refs from rules
			if rules, ok := spec["rules"].([]interface{}); ok {
				var backends []string
				for _, rule := range rules {
					if rm, ok := rule.(map[string]interface{}); ok {
						if brs, ok := rm["backendRefs"].([]interface{}); ok {
							for _, br := range brs {
								if brm, ok := br.(map[string]interface{}); ok {
									name, _ := brm["name"].(string)
									port, _ := brm["port"].(float64)
									if name != "" {
										if int(port) > 0 {
											backends = append(backends, fmt.Sprintf("%s:%d", name, int(port)))
										} else {
											backends = append(backends, name)
										}
									}
								}
							}
						}
					}
				}
				m["backends"] = strings.Join(backends, ", ")
			}
			// Parent gateway
			if parentRefs, ok := spec["parentRefs"].([]interface{}); ok && len(parentRefs) > 0 {
				if pr, ok := parentRefs[0].(map[string]interface{}); ok {
					m["gateway"] = pr["name"]
					if gwNs, ok := pr["namespace"]; ok {
						m["gatewayNamespace"] = gwNs
					}
				}
			}
			// Check accepted condition
			if parents, ok := status["parents"].([]interface{}); ok {
				for _, p := range parents {
					if pm, ok := p.(map[string]interface{}); ok {
						if conditions, ok := pm["conditions"].([]interface{}); ok {
							for _, cond := range conditions {
								if cm, ok := cond.(map[string]interface{}); ok {
									if cm["type"] == "Accepted" && cm["status"] != "True" {
										m["status"] = "NotAccepted"
									}
								}
							}
						}
					}
				}
			}
		}
		items = append(items, m)
	}
	return items
}

func (c *Connector) listConfigMaps(namespace string) []map[string]interface{} {
	list, _ := c.configMapLister.List(everythingSelector())
	var items []map[string]interface{}
	for _, cm := range list {
		if namespace != "" && cm.Namespace != namespace {
			continue
		}
		items = append(items, configMapToMap(cm))
	}
	return items
}

func (c *Connector) listSecrets(namespace string) []map[string]interface{} {
	list, _ := c.secretLister.List(everythingSelector())
	var items []map[string]interface{}
	for _, sec := range list {
		if namespace != "" && sec.Namespace != namespace {
			continue
		}
		items = append(items, secretToMap(sec))
	}
	return items
}

func (c *Connector) listPVCs(namespace string) []map[string]interface{} {
	list, _ := c.pvcLister.List(everythingSelector())
	var items []map[string]interface{}
	for _, pvc := range list {
		if namespace != "" && pvc.Namespace != namespace {
			continue
		}
		items = append(items, pvcToMap(pvc))
	}
	return items
}

func (c *Connector) listPVs() []map[string]interface{} {
	list, _ := c.pvLister.List(everythingSelector())
	var items []map[string]interface{}
	for _, pv := range list {
		items = append(items, pvToMap(pv))
	}
	return items
}

func (c *Connector) listStorageClasses() []map[string]interface{} {
	list, _ := c.storageClassLister.List(everythingSelector())
	var items []map[string]interface{}
	for _, sc := range list {
		items = append(items, map[string]interface{}{
			"name":      sc.Name,
			"namespace": "",
			"status":    "Active",
			"provisioner": sc.Provisioner,
			"reclaimPolicy": func() string {
				if sc.ReclaimPolicy != nil {
					return string(*sc.ReclaimPolicy)
				}
				return ""
			}(),
			"volumeBindingMode": func() string {
				if sc.VolumeBindingMode != nil {
					return string(*sc.VolumeBindingMode)
				}
				return ""
			}(),
			"labels":      safeLabels(sc.Labels),
			"annotations": safeAnnotations(sc.Annotations),
			"createdAt":   sc.CreationTimestamp.Time.Format(time.RFC3339),
			"age":         formatAge(sc.CreationTimestamp.Time),
		})
	}
	return items
}

func (c *Connector) listNodes() []map[string]interface{} {
	if c.nodeLister == nil {
		return nil
	}
	list, _ := c.nodeLister.List(everythingSelector())
	// Count pods per node
	var pods []*corev1.Pod
	if c.podLister != nil {
		pods, _ = c.podLister.List(everythingSelector())
	}
	podCountByNode := make(map[string]int)
	for _, pod := range pods {
		if pod.Spec.NodeName != "" && pod.Status.Phase == corev1.PodRunning {
			podCountByNode[pod.Spec.NodeName]++
		}
	}
	var nodeMetrics map[string]*models.MetricPoint
	if c.collector != nil {
		nodeMetrics = c.collector.GetAllNodeMetrics()
	}
	var items []map[string]interface{}
	for _, node := range list {
		m := nodeToMap(node)
		m["podCount"] = podCountByNode[node.Name]
		m["podCapacity"] = node.Status.Allocatable.Pods().Value()
		if nodeMetrics != nil {
			if nm, ok := nodeMetrics[node.Name]; ok {
				m["cpuUsage"] = nm.CPUUsage
				m["memoryUsage"] = nm.MemUsage
				cpuAlloc := node.Status.Allocatable.Cpu().MilliValue()
				memAlloc := node.Status.Allocatable.Memory().Value()
				if cpuAlloc > 0 {
					m["cpuPercent"] = float64(nm.CPUUsage) / float64(cpuAlloc) * 100
				}
				if memAlloc > 0 {
					m["memoryPercent"] = float64(nm.MemUsage) / float64(memAlloc) * 100
				}
			}
		}
		items = append(items, m)
	}
	return items
}

func (c *Connector) listNamespaces() []map[string]interface{} {
	list, _ := c.namespaceLister.List(everythingSelector())
	// Count resources per namespace
	pods, _ := c.podLister.List(everythingSelector())
	deploys, _ := c.deploymentLister.List(everythingSelector())
	svcs, _ := c.serviceLister.List(everythingSelector())
	ssets, _ := c.statefulSetLister.List(everythingSelector())
	dsets, _ := c.daemonSetLister.List(everythingSelector())
	cms, _ := c.configMapLister.List(everythingSelector())

	podCount := make(map[string]int)
	deployCount := make(map[string]int)
	svcCount := make(map[string]int)
	ssetCount := make(map[string]int)
	dsetCount := make(map[string]int)
	cmCount := make(map[string]int)
	for _, p := range pods { podCount[p.Namespace]++ }
	for _, d := range deploys { deployCount[d.Namespace]++ }
	for _, s := range svcs { svcCount[s.Namespace]++ }
	for _, s := range ssets { ssetCount[s.Namespace]++ }
	for _, d := range dsets { dsetCount[d.Namespace]++ }
	for _, c := range cms { cmCount[c.Namespace]++ }

	var items []map[string]interface{}
	for _, ns := range list {
		items = append(items, map[string]interface{}{
			"name":            ns.Name,
			"namespace":       "",
			"status":          string(ns.Status.Phase),
			"labels":          safeLabels(ns.Labels),
			"annotations":     safeAnnotations(ns.Annotations),
			"createdAt":       ns.CreationTimestamp.Time.Format(time.RFC3339),
			"age":             formatAge(ns.CreationTimestamp.Time),
			"podCount":        podCount[ns.Name],
			"deploymentCount": deployCount[ns.Name],
			"serviceCount":    svcCount[ns.Name],
			"statefulSetCount": ssetCount[ns.Name],
			"daemonSetCount":  dsetCount[ns.Name],
			"configMapCount":  cmCount[ns.Name],
		})
	}
	return items
}

func (c *Connector) listHPAs(namespace string) []map[string]interface{} {
	list, _ := c.hpaLister.List(everythingSelector())
	var items []map[string]interface{}
	for _, hpa := range list {
		if namespace != "" && hpa.Namespace != namespace {
			continue
		}
		items = append(items, hpaToMap(hpa))
	}
	return items
}

func (c *Connector) listEventsAsResources(namespace string) []map[string]interface{} {
	events, _ := c.eventLister.List(everythingSelector())
	var items []map[string]interface{}
	for _, event := range events {
		if namespace != "" && event.Namespace != namespace {
			continue
		}
		ts := event.LastTimestamp.Time
		if ts.IsZero() {
			ts = event.CreationTimestamp.Time
		}
		items = append(items, map[string]interface{}{
			"name":        event.Name,
			"namespace":   event.Namespace,
			"status":      event.Type,
			"reason":      event.Reason,
			"message":     event.Message,
			"object":      event.InvolvedObject.Kind + "/" + event.InvolvedObject.Name,
			"count":       event.Count,
			"labels":      map[string]string{},
			"annotations": map[string]string{},
			"createdAt":   ts.Format(time.RFC3339),
			"age":         formatAge(ts),
		})
	}
	return items
}

func (c *Connector) listRoles(namespace string) []map[string]interface{} {
	list, _ := c.roleLister.List(everythingSelector())
	var items []map[string]interface{}
	for _, r := range list {
		if namespace != "" && r.Namespace != namespace {
			continue
		}
		items = append(items, map[string]interface{}{
			"name":        r.Name,
			"namespace":   r.Namespace,
			"status":      "Active",
			"rules":       len(r.Rules),
			"labels":      safeLabels(r.Labels),
			"annotations": safeAnnotations(r.Annotations),
			"createdAt":   r.CreationTimestamp.Time.Format(time.RFC3339),
			"age":         formatAge(r.CreationTimestamp.Time),
		})
	}
	return items
}

func (c *Connector) listClusterRoles() []map[string]interface{} {
	list, _ := c.clusterRoleLister.List(everythingSelector())
	var items []map[string]interface{}
	for _, cr := range list {
		items = append(items, map[string]interface{}{
			"name":        cr.Name,
			"namespace":   "",
			"status":      "Active",
			"rules":       len(cr.Rules),
			"labels":      safeLabels(cr.Labels),
			"annotations": safeAnnotations(cr.Annotations),
			"createdAt":   cr.CreationTimestamp.Time.Format(time.RFC3339),
			"age":         formatAge(cr.CreationTimestamp.Time),
		})
	}
	return items
}

func (c *Connector) listRoleBindings(namespace string) []map[string]interface{} {
	list, _ := c.roleBindingLister.List(everythingSelector())
	var items []map[string]interface{}
	for _, rb := range list {
		if namespace != "" && rb.Namespace != namespace {
			continue
		}
		items = append(items, map[string]interface{}{
			"name":        rb.Name,
			"namespace":   rb.Namespace,
			"status":      "Active",
			"roleRef":     rb.RoleRef.Kind + "/" + rb.RoleRef.Name,
			"subjects":    len(rb.Subjects),
			"labels":      safeLabels(rb.Labels),
			"annotations": safeAnnotations(rb.Annotations),
			"createdAt":   rb.CreationTimestamp.Time.Format(time.RFC3339),
			"age":         formatAge(rb.CreationTimestamp.Time),
		})
	}
	return items
}

func (c *Connector) listClusterRoleBindings() []map[string]interface{} {
	list, _ := c.clusterRoleBindingLister.List(everythingSelector())
	var items []map[string]interface{}
	for _, crb := range list {
		items = append(items, map[string]interface{}{
			"name":        crb.Name,
			"namespace":   "",
			"status":      "Active",
			"roleRef":     crb.RoleRef.Kind + "/" + crb.RoleRef.Name,
			"subjects":    len(crb.Subjects),
			"labels":      safeLabels(crb.Labels),
			"annotations": safeAnnotations(crb.Annotations),
			"createdAt":   crb.CreationTimestamp.Time.Format(time.RFC3339),
			"age":         formatAge(crb.CreationTimestamp.Time),
		})
	}
	return items
}

// GetDeploymentPods returns all pods owned by a deployment (via ReplicaSets).
func (c *Connector) GetDeploymentPods(namespace, deploymentName string) []map[string]interface{} {
	// 1. Get all ReplicaSets in the namespace
	replicaSets, _ := c.replicaSetLister.List(everythingSelector())
	// 2. Find which ReplicaSets are owned by this deployment
	rsNames := make(map[string]bool)
	for _, rs := range replicaSets {
		if rs.Namespace != namespace {
			continue
		}
		for _, ref := range rs.OwnerReferences {
			if ref.Kind == "Deployment" && ref.Name == deploymentName {
				rsNames[rs.Name] = true
			}
		}
	}

	// 3. Get all pods in the namespace
	pods, _ := c.podLister.List(everythingSelector())
	var podMetrics map[string]*models.MetricPoint
	if c.collector != nil {
		podMetrics = c.collector.GetAllPodMetrics()
	}

	var items []map[string]interface{}
	for _, pod := range pods {
		if pod.Namespace != namespace {
			continue
		}
		// 4. Find which pods are owned by those ReplicaSets
		owned := false
		for _, ref := range pod.OwnerReferences {
			if ref.Kind == "ReplicaSet" && rsNames[ref.Name] {
				owned = true
				break
			}
		}
		if !owned {
			continue
		}

		// 5. Convert pod using podToMap
		m := podToMap(pod)

		// 6. Inject pod metrics (same pattern as listPods)
		var cpuReq, cpuLim, memReq, memLim int64
		for _, cont := range pod.Spec.Containers {
			cpuReq += cont.Resources.Requests.Cpu().MilliValue()
			cpuLim += cont.Resources.Limits.Cpu().MilliValue()
			memReq += cont.Resources.Requests.Memory().Value()
			memLim += cont.Resources.Limits.Memory().Value()
		}
		m["cpuRequest"] = cpuReq
		m["cpuLimit"] = cpuLim
		m["memoryRequest"] = memReq
		m["memoryLimit"] = memLim
		if podMetrics != nil {
			key := pod.Namespace + "/" + pod.Name
			if pm, ok := podMetrics[key]; ok {
				m["cpuUsage"] = pm.CPUUsage
				m["memoryUsage"] = pm.MemUsage
				if cpuLim > 0 {
					m["cpuPercent"] = float64(pm.CPUUsage) / float64(cpuLim) * 100
				} else if cpuReq > 0 {
					m["cpuPercent"] = float64(pm.CPUUsage) / float64(cpuReq) * 100
				}
				if memLim > 0 {
					m["memoryPercent"] = float64(pm.MemUsage) / float64(memLim) * 100
				} else if memReq > 0 {
					m["memoryPercent"] = float64(pm.MemUsage) / float64(memReq) * 100
				}
			}
		}
		items = append(items, m)
	}
	return items
}

// GetDeploymentHistory returns the revision history of a deployment via its ReplicaSets.
func (c *Connector) GetDeploymentHistory(namespace, deploymentName string) []map[string]interface{} {
	replicaSets, _ := c.replicaSetLister.List(everythingSelector())

	var items []map[string]interface{}
	for _, rs := range replicaSets {
		if rs.Namespace != namespace {
			continue
		}
		ownedByDeployment := false
		for _, ref := range rs.OwnerReferences {
			if ref.Kind == "Deployment" && ref.Name == deploymentName {
				ownedByDeployment = true
				break
			}
		}
		if !ownedByDeployment {
			continue
		}

		revision := ""
		if rs.Annotations != nil {
			revision = rs.Annotations["deployment.kubernetes.io/revision"]
		}

		image := ""
		if len(rs.Spec.Template.Spec.Containers) > 0 {
			image = rs.Spec.Template.Spec.Containers[0].Image
		}

		item := map[string]interface{}{
			"revision":      revision,
			"name":          rs.Name,
			"namespace":     rs.Namespace,
			"replicas":      rs.Status.Replicas,
			"readyReplicas": rs.Status.ReadyReplicas,
			"createdAt":     rs.CreationTimestamp.Format(time.RFC3339),
			"age":           formatAge(rs.CreationTimestamp.Time),
			"image":         image,
			"active":        rs.Status.Replicas > 0,
		}
		items = append(items, item)
	}

	// Sort by revision number descending (highest/newest first)
	sort.Slice(items, func(i, j int) bool {
		ri, _ := strconv.Atoi(items[i]["revision"].(string))
		rj, _ := strconv.Atoi(items[j]["revision"].(string))
		return ri > rj
	})

	return items
}

// GetStatefulSetPods returns all pods owned by a statefulset (direct ownership).
func (c *Connector) GetStatefulSetPods(namespace, stsName string) []map[string]interface{} {
	pods, _ := c.podLister.List(everythingSelector())
	var podMetrics map[string]*models.MetricPoint
	if c.collector != nil {
		podMetrics = c.collector.GetAllPodMetrics()
	}

	var items []map[string]interface{}
	for _, pod := range pods {
		if pod.Namespace != namespace {
			continue
		}
		owned := false
		for _, ref := range pod.OwnerReferences {
			if ref.Kind == "StatefulSet" && ref.Name == stsName {
				owned = true
				break
			}
		}
		if !owned {
			continue
		}

		m := podToMap(pod)
		var cpuReq, cpuLim, memReq, memLim int64
		for _, cont := range pod.Spec.Containers {
			cpuReq += cont.Resources.Requests.Cpu().MilliValue()
			cpuLim += cont.Resources.Limits.Cpu().MilliValue()
			memReq += cont.Resources.Requests.Memory().Value()
			memLim += cont.Resources.Limits.Memory().Value()
		}
		m["cpuRequest"] = cpuReq
		m["cpuLimit"] = cpuLim
		m["memoryRequest"] = memReq
		m["memoryLimit"] = memLim
		if podMetrics != nil {
			key := pod.Namespace + "/" + pod.Name
			if pm, ok := podMetrics[key]; ok {
				m["cpuUsage"] = pm.CPUUsage
				m["memoryUsage"] = pm.MemUsage
				if cpuLim > 0 {
					m["cpuPercent"] = float64(pm.CPUUsage) / float64(cpuLim) * 100
				} else if cpuReq > 0 {
					m["cpuPercent"] = float64(pm.CPUUsage) / float64(cpuReq) * 100
				}
				if memLim > 0 {
					m["memoryPercent"] = float64(pm.MemUsage) / float64(memLim) * 100
				} else if memReq > 0 {
					m["memoryPercent"] = float64(pm.MemUsage) / float64(memReq) * 100
				}
			}
		}
		items = append(items, m)
	}
	return items
}

// GetDaemonSetPods returns all pods owned by a daemonset (direct ownership).
func (c *Connector) GetDaemonSetPods(namespace, dsName string) []map[string]interface{} {
	pods, _ := c.podLister.List(everythingSelector())
	var podMetrics map[string]*models.MetricPoint
	if c.collector != nil {
		podMetrics = c.collector.GetAllPodMetrics()
	}

	var items []map[string]interface{}
	for _, pod := range pods {
		if pod.Namespace != namespace {
			continue
		}
		owned := false
		for _, ref := range pod.OwnerReferences {
			if ref.Kind == "DaemonSet" && ref.Name == dsName {
				owned = true
				break
			}
		}
		if !owned {
			continue
		}

		m := podToMap(pod)
		var cpuReq, cpuLim, memReq, memLim int64
		for _, cont := range pod.Spec.Containers {
			cpuReq += cont.Resources.Requests.Cpu().MilliValue()
			cpuLim += cont.Resources.Limits.Cpu().MilliValue()
			memReq += cont.Resources.Requests.Memory().Value()
			memLim += cont.Resources.Limits.Memory().Value()
		}
		m["cpuRequest"] = cpuReq
		m["cpuLimit"] = cpuLim
		m["memoryRequest"] = memReq
		m["memoryLimit"] = memLim
		if podMetrics != nil {
			key := pod.Namespace + "/" + pod.Name
			if pm, ok := podMetrics[key]; ok {
				m["cpuUsage"] = pm.CPUUsage
				m["memoryUsage"] = pm.MemUsage
				if cpuLim > 0 {
					m["cpuPercent"] = float64(pm.CPUUsage) / float64(cpuLim) * 100
				} else if cpuReq > 0 {
					m["cpuPercent"] = float64(pm.CPUUsage) / float64(cpuReq) * 100
				}
				if memLim > 0 {
					m["memoryPercent"] = float64(pm.MemUsage) / float64(memLim) * 100
				} else if memReq > 0 {
					m["memoryPercent"] = float64(pm.MemUsage) / float64(memReq) * 100
				}
			}
		}
		items = append(items, m)
	}
	return items
}

// GetJobPods returns all pods owned by a job (direct ownership).
func (c *Connector) GetJobPods(namespace, jobName string) []map[string]interface{} {
	pods, _ := c.podLister.List(everythingSelector())
	var podMetrics map[string]*models.MetricPoint
	if c.collector != nil {
		podMetrics = c.collector.GetAllPodMetrics()
	}

	var items []map[string]interface{}
	for _, pod := range pods {
		if pod.Namespace != namespace {
			continue
		}
		owned := false
		for _, ref := range pod.OwnerReferences {
			if ref.Kind == "Job" && ref.Name == jobName {
				owned = true
				break
			}
		}
		if !owned {
			continue
		}

		m := podToMap(pod)
		var cpuReq, cpuLim, memReq, memLim int64
		for _, cont := range pod.Spec.Containers {
			cpuReq += cont.Resources.Requests.Cpu().MilliValue()
			cpuLim += cont.Resources.Limits.Cpu().MilliValue()
			memReq += cont.Resources.Requests.Memory().Value()
			memLim += cont.Resources.Limits.Memory().Value()
		}
		m["cpuRequest"] = cpuReq
		m["cpuLimit"] = cpuLim
		m["memoryRequest"] = memReq
		m["memoryLimit"] = memLim
		if podMetrics != nil {
			key := pod.Namespace + "/" + pod.Name
			if pm, ok := podMetrics[key]; ok {
				m["cpuUsage"] = pm.CPUUsage
				m["memoryUsage"] = pm.MemUsage
				if cpuLim > 0 {
					m["cpuPercent"] = float64(pm.CPUUsage) / float64(cpuLim) * 100
				} else if cpuReq > 0 {
					m["cpuPercent"] = float64(pm.CPUUsage) / float64(cpuReq) * 100
				}
				if memLim > 0 {
					m["memoryPercent"] = float64(pm.MemUsage) / float64(memLim) * 100
				} else if memReq > 0 {
					m["memoryPercent"] = float64(pm.MemUsage) / float64(memReq) * 100
				}
			}
		}
		items = append(items, m)
	}
	return items
}

// GetWorkloadHistory returns the revision history of a StatefulSet or DaemonSet via ControllerRevisions.
func (c *Connector) GetWorkloadHistory(resourceType, namespace, name string) []map[string]interface{} {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	revisions, err := c.clientset.AppsV1().ControllerRevisions(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil
	}

	var kind string
	switch resourceType {
	case "statefulsets":
		kind = "StatefulSet"
	case "daemonsets":
		kind = "DaemonSet"
	default:
		return nil
	}

	var items []map[string]interface{}
	for _, rev := range revisions.Items {
		owned := false
		for _, ref := range rev.OwnerReferences {
			if ref.Kind == kind && ref.Name == name {
				owned = true
				break
			}
		}
		if !owned {
			continue
		}

		items = append(items, map[string]interface{}{
			"revision":  rev.Revision,
			"name":      rev.Name,
			"createdAt": rev.CreationTimestamp.Format(time.RFC3339),
			"age":       formatAge(rev.CreationTimestamp.Time),
		})
	}

	// Sort by revision descending (newest first)
	sort.Slice(items, func(i, j int) bool {
		ri, _ := items[i]["revision"].(int64)
		rj, _ := items[j]["revision"].(int64)
		return ri > rj
	})

	return items
}

// GetCronJobJobs returns all Jobs owned by a CronJob.
func (c *Connector) GetCronJobJobs(namespace, cronJobName string) []map[string]interface{} {
	if c.jobLister == nil {
		return nil
	}
	jobs, _ := c.jobLister.List(everythingSelector())
	var items []map[string]interface{}
	for _, job := range jobs {
		if job.Namespace != namespace {
			continue
		}
		owned := false
		for _, ref := range job.OwnerReferences {
			if ref.Kind == "CronJob" && ref.Name == cronJobName {
				owned = true
				break
			}
		}
		if !owned {
			continue
		}
		items = append(items, jobToMap(job))
	}
	// Sort by creation time descending (newest first)
	sort.Slice(items, func(i, j int) bool {
		ti, _ := items[i]["createdAt"].(string)
		tj, _ := items[j]["createdAt"].(string)
		return ti > tj
	})
	return items
}

// Ensure all imported types are used.
var (
	_ *storagev1.StorageClass
	_ *rbacv1.Role
	_ *rbacv1.ClusterRole
	_ *rbacv1.RoleBinding
	_ *rbacv1.ClusterRoleBinding
)
