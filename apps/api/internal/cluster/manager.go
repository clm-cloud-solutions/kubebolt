package cluster

import (
	"context"
	"fmt"
	"log/slog"
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
	storage         *Storage // optional — nil when auth disabled; drives user-uploaded contexts and display names
	// onNewInsight is invoked for each newly detected insight; wired to the
	// notifications manager from main.go. Nil when notifications are disabled.
	onNewInsight func(clusterContext string, insight models.Insight)
	// onResolvedInsight is invoked when an insight transitions to resolved.
	// Nil when notifications are disabled or includeResolved is false.
	onResolvedInsight func(clusterContext string, insight models.Insight)
}

// SetOnNewInsight registers a callback invoked (asynchronously) for every new
// insight detected in the active cluster. Wire this to a notifications manager
// in main.go. The clusterContext passed to the callback is m.activeContext at
// the time the insight was emitted.
func (m *Manager) SetOnNewInsight(fn func(clusterContext string, insight models.Insight)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onNewInsight = fn
	// If an engine is already running, wire the hook immediately.
	if m.engine != nil {
		m.wireInsightHookLocked()
	}
}

// SetOnResolvedInsight registers a callback invoked when an insight in the
// active cluster transitions to resolved. Same wiring semantics as
// SetOnNewInsight.
func (m *Manager) SetOnResolvedInsight(fn func(clusterContext string, insight models.Insight)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onResolvedInsight = fn
	if m.engine != nil {
		m.wireInsightHookLocked()
	}
}

// wireInsightHookLocked attaches m.onNewInsight and m.onResolvedInsight to
// the current engine. Assumes m.mu is held.
func (m *Manager) wireInsightHookLocked() {
	if m.engine == nil {
		return
	}
	activeCtx := m.activeContext
	if m.onNewInsight != nil {
		hook := m.onNewInsight
		m.engine.SetOnNewInsight(func(insight models.Insight) {
			// Called with engine lock held — keep this fast, the notification
			// manager already dispatches async.
			hook(activeCtx, insight)
		})
	}
	if m.onResolvedInsight != nil {
		hook := m.onResolvedInsight
		m.engine.SetOnResolvedInsight(func(insight models.Insight) {
			hook(activeCtx, insight)
		})
	}
}

// SetStorage attaches a cluster storage to the manager. This must be called
// after NewManager but before the HTTP router starts serving. After attaching,
// the manager merges any user-uploaded kubeconfigs into its in-memory config.
func (m *Manager) SetStorage(s *Storage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.storage = s
	return m.reloadUploadedContextsLocked()
}

// Storage returns the attached storage, or nil if none was set.
func (m *Manager) Storage() *Storage {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.storage
}

// reloadUploadedContextsLocked merges kubeconfigs from BoltDB into the in-memory
// config. Called on startup and after CRUD operations. Assumes m.mu is held.
func (m *Manager) reloadUploadedContextsLocked() error {
	if m.storage == nil {
		return nil
	}
	configs, err := m.storage.ListKubeconfigs()
	if err != nil {
		return fmt.Errorf("listing stored kubeconfigs: %w", err)
	}

	for _, stored := range configs {
		uploaded, err := clientcmd.Load(stored.Kubeconfig)
		if err != nil {
			slog.Warn("stored kubeconfig is invalid",
				slog.String("context", stored.Context),
				slog.String("error", err.Error()))
			continue
		}
		m.mergeKubeconfigLocked(uploaded)
	}
	return nil
}

// mergeKubeconfigLocked merges the contexts, clusters, and authInfos from src
// into m.kubeConfig. Existing entries with the same name are overwritten.
// Assumes m.mu is held.
func (m *Manager) mergeKubeconfigLocked(src *clientcmdapi.Config) {
	if m.kubeConfig.Contexts == nil {
		m.kubeConfig.Contexts = make(map[string]*clientcmdapi.Context)
	}
	if m.kubeConfig.Clusters == nil {
		m.kubeConfig.Clusters = make(map[string]*clientcmdapi.Cluster)
	}
	if m.kubeConfig.AuthInfos == nil {
		m.kubeConfig.AuthInfos = make(map[string]*clientcmdapi.AuthInfo)
	}
	for name, ctx := range src.Contexts {
		m.kubeConfig.Contexts[name] = ctx
	}
	for name, cl := range src.Clusters {
		m.kubeConfig.Clusters[name] = cl
	}
	for name, auth := range src.AuthInfos {
		m.kubeConfig.AuthInfos[name] = auth
	}
}

// ClusterInfo represents a cluster available in the kubeconfig.
type ClusterInfo struct {
	Name        string `json:"name"`
	Context     string `json:"context"`
	Server      string `json:"server"`
	Active      bool   `json:"active"`
	Status      string `json:"status"`                // "connected", "disconnected", "error"
	Error       string `json:"error,omitempty"`       // connection error message
	DisplayName string `json:"displayName,omitempty"` // optional user-defined friendly name
	Source      string `json:"source"`                // "file" (kubeconfig on disk), "uploaded" (added via UI), "in-cluster"
}

// NewManager creates a new cluster manager.
func NewManager(kubeconfigPath string, wsHub *websocket.Hub, metricInterval, insightInterval time.Duration) (*Manager, error) {
	// Try loading kubeconfig file first; fall back to in-cluster config
	kubeConfig, err := clientcmd.LoadFromFile(kubeconfigPath)
	if err != nil {
		// Check if running inside Kubernetes (ServiceAccount token available)
		if _, inClusterErr := rest.InClusterConfig(); inClusterErr == nil {
			slog.Info("no kubeconfig file found, using in-cluster configuration")
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
					slog.Warn("in-cluster connection failed, staying disconnected",
						slog.String("error", err.Error()))
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
			slog.Warn("initial cluster connection failed, staying disconnected",
				slog.String("context", kubeConfig.CurrentContext),
				slog.String("error", err.Error()))
			m.connErr = err
		}
	}()

	return m, nil
}

// ListClusters returns all available clusters from the kubeconfig.
func (m *Manager) ListClusters() []ClusterInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Pre-compute uploaded context names and display names (single DB read).
	uploadedContexts := make(map[string]bool)
	displayNames := make(map[string]string)
	if m.storage != nil {
		if configs, err := m.storage.ListKubeconfigs(); err == nil {
			for _, c := range configs {
				uploadedContexts[c.Context] = true
			}
		}
		if names, err := m.storage.AllDisplayNames(); err == nil {
			displayNames = names
		}
	}

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
		source := "file"
		if m.inCluster {
			source = "in-cluster"
		} else if uploadedContexts[ctxName] {
			source = "uploaded"
		}
		clusters = append(clusters, ClusterInfo{
			Name:        ctx.Cluster,
			Context:     ctxName,
			Server:      server,
			Active:      isActive,
			Status:      status,
			Error:       connErrMsg,
			DisplayName: displayNames[ctxName],
			Source:      source,
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
		slog.Warn("failed to connect to context, staying disconnected",
			slog.String("context", contextName),
			slog.String("error", err.Error()))
		return err
	}

	slog.Info("switched cluster context", slog.String("context", contextName))
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

// AddKubeconfig persists a user-uploaded kubeconfig and merges it into the
// in-memory config. Each context in the uploaded kubeconfig is saved as a
// separate StoredKubeconfig entry. Returns the context names that were added.
// Returns an error if storage is not configured or the kubeconfig is invalid.
func (m *Manager) AddKubeconfig(rawYAML []byte, uploadedBy string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.storage == nil {
		return nil, fmt.Errorf("cluster persistence is not available (auth may be disabled)")
	}
	if m.inCluster {
		return nil, fmt.Errorf("cannot add clusters in in-cluster mode")
	}

	parsed, err := clientcmd.Load(rawYAML)
	if err != nil {
		return nil, fmt.Errorf("invalid kubeconfig: %w", err)
	}
	if len(parsed.Contexts) == 0 {
		return nil, fmt.Errorf("kubeconfig contains no contexts")
	}

	// Check for name collisions with contexts already in the in-memory config
	// that come from sources OTHER than the uploaded store (i.e., file).
	existingUploaded := make(map[string]bool)
	if configs, err := m.storage.ListKubeconfigs(); err == nil {
		for _, c := range configs {
			existingUploaded[c.Context] = true
		}
	}
	for ctxName := range parsed.Contexts {
		if _, existsInMemory := m.kubeConfig.Contexts[ctxName]; existsInMemory && !existingUploaded[ctxName] {
			return nil, fmt.Errorf("context %q already exists in the kubeconfig file — rename it before uploading", ctxName)
		}
	}

	var added []string
	now := time.Now().UTC()
	for ctxName := range parsed.Contexts {
		// Persist one entry per context with the same raw YAML
		stored := &StoredKubeconfig{
			Context:    ctxName,
			Kubeconfig: rawYAML,
			UploadedAt: now,
			UploadedBy: uploadedBy,
		}
		if err := m.storage.SaveKubeconfig(stored); err != nil {
			return nil, fmt.Errorf("persisting context %q: %w", ctxName, err)
		}
		added = append(added, ctxName)
	}

	// Merge into in-memory config
	m.mergeKubeconfigLocked(parsed)
	slog.Info("added cluster contexts from uploaded kubeconfig",
		slog.Int("count", len(added)),
		slog.Any("contexts", added))
	return added, nil
}

// RemoveUploadedContext deletes a user-uploaded context. Contexts originating
// from the kubeconfig file cannot be removed (we never touch the user's file).
// If the removed context is currently active, the manager disconnects.
func (m *Manager) RemoveUploadedContext(contextName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.storage == nil {
		return fmt.Errorf("cluster persistence is not available")
	}

	stored, err := m.storage.GetKubeconfig(contextName)
	if err != nil {
		return fmt.Errorf("lookup failed: %w", err)
	}
	if stored == nil {
		return fmt.Errorf("context %q was not added via the UI (cannot delete file-based contexts)", contextName)
	}

	// If this context is currently active, stop the connector
	if m.activeContext == contextName {
		m.stopCurrent()
		m.activeContext = ""
	}

	// Remove from BoltDB
	if err := m.storage.DeleteKubeconfig(contextName); err != nil {
		return err
	}

	// Remove from in-memory config. Only remove the context entry;
	// shared clusters/authInfos may still be referenced by others.
	delete(m.kubeConfig.Contexts, contextName)

	// Also remove any display name override
	m.storage.DeleteDisplayName(contextName)

	slog.Info("removed uploaded cluster context", slog.String("context", contextName))
	return nil
}

// SetClusterDisplayName sets or clears a human-friendly display name
// for any context (file-based or uploaded). Pass an empty string to clear.
func (m *Manager) SetClusterDisplayName(contextName, displayName string) error {
	m.mu.RLock()
	_, exists := m.kubeConfig.Contexts[contextName]
	m.mu.RUnlock()
	if !exists {
		return fmt.Errorf("context %q not found", contextName)
	}
	if m.storage == nil {
		return fmt.Errorf("cluster persistence is not available")
	}
	return m.storage.SetDisplayName(contextName, displayName)
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

	// Wire notification hook if one was registered before this connection
	// was established (or if we just switched clusters).
	m.wireInsightHookLocked()

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
