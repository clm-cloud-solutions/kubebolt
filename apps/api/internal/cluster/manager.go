package cluster

import (
	"context"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/kubebolt/kubebolt/apps/api/internal/insights"
	"github.com/kubebolt/kubebolt/apps/api/internal/metrics"
	"github.com/kubebolt/kubebolt/apps/api/internal/models"
	"github.com/kubebolt/kubebolt/apps/api/internal/websocket"
)

// Manager handles multiple cluster connections and switching between them.
type Manager struct {
	mu              sync.RWMutex
	kubeconfigPath  string
	kubeConfig      *clientcmdapi.Config
	inCluster       bool // true when running inside Kubernetes (no kubeconfig file)
	activeContext   string
	connector       *Connector
	collector       *metrics.Collector
	engine          *insights.Engine
	wsHub           *websocket.Hub
	metricInterval  time.Duration
	insightInterval time.Duration
	cancelFn        context.CancelFunc
	connErr         error // set when the active context failed to connect
}

// ClusterInfo represents a cluster available in the kubeconfig.
type ClusterInfo struct {
	Name     string `json:"name"`
	Context  string `json:"context"`
	Server   string `json:"server"`
	Active   bool   `json:"active"`
	Status   string `json:"status"`          // "connected", "disconnected", "error"
	Error    string `json:"error,omitempty"`  // connection error message
}

// NewManager creates a new cluster manager.
func NewManager(kubeconfigPath string, wsHub *websocket.Hub, metricInterval, insightInterval time.Duration) (*Manager, error) {
	// Try loading kubeconfig file first; fall back to in-cluster config
	kubeConfig, err := clientcmd.LoadFromFile(kubeconfigPath)
	if err != nil {
		// Check if running inside Kubernetes (ServiceAccount token available)
		if _, inClusterErr := rest.InClusterConfig(); inClusterErr == nil {
			log.Printf("No kubeconfig file found, using in-cluster configuration")
			m := &Manager{
				inCluster: true,
				kubeConfig: &clientcmdapi.Config{
					Contexts: map[string]*clientcmdapi.Context{
						"in-cluster": {Cluster: "in-cluster"},
					},
					Clusters: map[string]*clientcmdapi.Cluster{
						"in-cluster": {Server: "https://kubernetes.default.svc"},
					},
					CurrentContext: "in-cluster",
				},
				activeContext:   "in-cluster",
				wsHub:           wsHub,
				metricInterval:  metricInterval,
				insightInterval: insightInterval,
			}

			go func() {
				m.mu.Lock()
				defer m.mu.Unlock()
				if err := m.connectToContextLocked("in-cluster"); err != nil {
					log.Printf("Warning: in-cluster connection failed: %v — staying in disconnected state", err)
					m.connErr = err
				}
			}()

			return m, nil
		}
		return nil, fmt.Errorf("loading kubeconfig: %w", err)
	}

	m := &Manager{
		kubeconfigPath:  kubeconfigPath,
		kubeConfig:      kubeConfig,
		activeContext:    kubeConfig.CurrentContext,
		wsHub:           wsHub,
		metricInterval:  metricInterval,
		insightInterval: insightInterval,
	}

	// Connect asynchronously so the HTTP server can bind immediately.
	// The manager starts in disconnected state; the UI will see 503s until ready.
	go func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if err := m.connectToContextLocked(kubeConfig.CurrentContext); err != nil {
			log.Printf("Warning: initial connection to context %q failed: %v — staying in disconnected state", kubeConfig.CurrentContext, err)
			m.connErr = err
		}
	}()

	return m, nil
}

// ListClusters returns all available clusters from the kubeconfig.
func (m *Manager) ListClusters() []ClusterInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var clusters []ClusterInfo
	for ctxName, ctx := range m.kubeConfig.Contexts {
		server := ""
		if cl, ok := m.kubeConfig.Clusters[ctx.Cluster]; ok {
			server = cl.Server
		}
		isActive := ctxName == m.activeContext
		status := "disconnected"
		connErrMsg := ""
		if isActive {
			if m.connector != nil {
				status = "connected"
			} else if m.connErr != nil {
				status = "error"
				connErrMsg = m.connErr.Error()
			}
		}
		clusters = append(clusters, ClusterInfo{
			Name:    ctx.Cluster,
			Context: ctxName,
			Server:  server,
			Active:  isActive,
			Status:  status,
			Error:   connErrMsg,
		})
	}
	sort.Slice(clusters, func(i, j int) bool {
		return clusters[i].Context < clusters[j].Context
	})
	return clusters
}

// SwitchCluster switches the active cluster to the given context name.
func (m *Manager) SwitchCluster(contextName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Allow retry if currently disconnected from this context
	if contextName == m.activeContext && m.connector != nil {
		return nil
	}

	// Verify context exists
	if _, ok := m.kubeConfig.Contexts[contextName]; !ok {
		return fmt.Errorf("context %q not found in kubeconfig", contextName)
	}

	// Stop existing connector and immediately mark new context as active.
	// The user explicitly chose this cluster, so we stay on it even if unreachable.
	m.stopCurrent()
	m.activeContext = contextName

	// Connect to new context; on failure, stay disconnected on contextName (no fallback).
	if err := m.connectToContextLocked(contextName); err != nil {
		m.connErr = err
		log.Printf("Failed to connect to context %s: %v — staying disconnected", contextName, err)
		return err
	}

	log.Printf("Switched to cluster context: %s", contextName)
	return nil
}

// ConnError returns the last connection error, or nil if connected.
func (m *Manager) ConnError() error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.connErr
}

// ActiveContext returns the name of the currently active context.
func (m *Manager) ActiveContext() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.activeContext
}

// Connector returns the active cluster connector.
func (m *Manager) Connector() *Connector {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.connector
}

// Collector returns the active metrics collector.
func (m *Manager) Collector() *metrics.Collector {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.collector
}

// Engine returns the insights engine.
func (m *Manager) Engine() *insights.Engine {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.engine
}

// Stop stops the active connector and collector.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopCurrent()
}

func (m *Manager) stopCurrent() {
	if m.cancelFn != nil {
		m.cancelFn()
		m.cancelFn = nil
	}
	if m.connector != nil {
		m.connector.Stop()
		m.connector = nil
	}
	m.collector = nil
	m.engine = nil
	m.connErr = nil
}

func (m *Manager) connectToContext(contextName string) error {
	return m.connectToContextLocked(contextName)
}

func (m *Manager) connectToContextLocked(contextName string) error {
	var connector *Connector
	var err error

	if m.inCluster {
		connector, err = NewConnectorInCluster(m.wsHub)
	} else {
		connector, err = NewConnectorForContext(m.kubeconfigPath, contextName, m.wsHub)
	}
	if err != nil {
		return fmt.Errorf("connecting to context %s: %w", contextName, err)
	}
	if err := connector.Start(); err != nil {
		connector.Stop()
		return fmt.Errorf("starting connector for context %s: %w", contextName, err)
	}

	collector := metrics.NewCollector(connector.MetricsClient(), m.metricInterval, connector.Permissions().ScopedNamespaces())
	connector.SetCollector(collector)

	ctx, cancel := context.WithCancel(context.Background())

	// Synchronous initial poll so metrics are available before first API request
	collector.Poll(ctx)

	go collector.Start(ctx)

	engine := insights.NewEngine(m.wsHub)

	// Start insight evaluation ticker
	go func() {
		ticker := time.NewTicker(m.insightInterval)
		defer ticker.Stop()
		evaluateInsights(connector, collector, engine)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				evaluateInsights(connector, collector, engine)
			}
		}
	}()

	m.connector = connector
	m.collector = collector
	m.engine = engine
	m.cancelFn = cancel
	m.activeContext = contextName
	m.connErr = nil

	return nil
}

func evaluateInsights(connector *Connector, collector *metrics.Collector, engine *insights.Engine) {
	state := &insights.ClusterState{
		Pods:        connector.GetPods(),
		Deployments: connector.GetDeployments(),
		Nodes:       connector.GetNodes(),
		HPAs:        connector.GetHPAs(),
		PVCs:        connector.GetPVCs(),
		Events:      connector.GetEventsRaw(),
		PodMetrics:  collector.GetAllPodMetrics(),
		NodeMetrics: collector.GetAllNodeMetrics(),
	}
	engine.Evaluate(state)
}

// ReloadKubeconfig reloads the kubeconfig file to pick up new contexts.
func (m *Manager) ReloadKubeconfig() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	kubeConfig, err := clientcmd.LoadFromFile(m.kubeconfigPath)
	if err != nil {
		return fmt.Errorf("reloading kubeconfig: %w", err)
	}
	m.kubeConfig = kubeConfig
	return nil
}

// NewConnectorForContext creates a connector for a specific kubeconfig context.
func NewConnectorForContext(kubeconfigPath, contextName string, wsHub *websocket.Hub) (*Connector, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	rules.ExplicitPath = kubeconfigPath

	overrides := &clientcmd.ConfigOverrides{CurrentContext: contextName}
	clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides)

	restConfig, err := clientConfig.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("building config for context %s: %w", contextName, err)
	}

	return newConnectorFromConfig(restConfig, contextName, wsHub)
}

// NewConnectorInCluster creates a connector using in-cluster ServiceAccount credentials.
func NewConnectorInCluster(wsHub *websocket.Hub) (*Connector, error) {
	restConfig, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("building in-cluster config: %w", err)
	}
	return newConnectorFromConfig(restConfig, "in-cluster", wsHub)
}

// GetClusterInfoForContext returns models.ClusterInfo for a specific context.
func (m *Manager) GetClusterInfoForContext(contextName string) *models.ClusterInfoResponse {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ctx, ok := m.kubeConfig.Contexts[contextName]
	if !ok {
		return nil
	}
	server := ""
	if cl, ok := m.kubeConfig.Clusters[ctx.Cluster]; ok {
		server = cl.Server
	}
	return &models.ClusterInfoResponse{
		Name:    ctx.Cluster,
		Context: contextName,
		Server:  server,
		Active:  contextName == m.activeContext,
	}
}
