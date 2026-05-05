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

	"github.com/kubebolt/kubebolt/apps/api/internal/agent/channel"
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

	// agentRegistry is the live registry of connected kubebolt-agents.
	// Set by main.go via SetAgentRegistry once the gRPC server is up.
	// nil when the agent channel is disabled — agent-proxy clusters
	// then refuse to register.
	agentRegistry *channel.AgentRegistry
	// agentProxyContexts maps the synthetic contextName under which an
	// agent-proxy cluster is exposed in ListClusters → cluster_id.
	// Lookup-on-connect lets the manager pick the right ClusterAccess
	// without bolting a new field onto every kubeconfig entry.
	agentProxyContexts map[string]string

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

// SetAgentRegistry attaches the live AgentRegistry to the manager so
// agent-proxy clusters can resolve their access at connect time. nil
// is tolerated (agent channel disabled) — AddAgentProxyCluster then
// returns an error.
func (m *Manager) SetAgentRegistry(r *channel.AgentRegistry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.agentRegistry = r
}

// AgentRegistry returns the registry the manager was wired with, or
// nil. Used by handlers that surface live-agent state (admin endpoints
// in commit 8) without needing a separate plumbing path.
func (m *Manager) AgentRegistry() *channel.AgentRegistry {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.agentRegistry
}

// AddAgentProxyCluster registers a cluster reachable only via the
// kubebolt-agent's outbound channel. The cluster shows up in
// ListClusters alongside kubeconfig-backed contexts under the
// synthetic name returned by AgentProxyContextName(clusterID), so
// the existing SwitchCluster + ListClusters flow keeps working with
// no special-casing in handlers. Idempotent — re-adding the same
// clusterID with a different displayName updates the display name
// only.
func (m *Manager) AddAgentProxyCluster(clusterID, displayName string) (string, error) {
	if clusterID == "" {
		return "", fmt.Errorf("clusterID is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.agentRegistry == nil {
		return "", fmt.Errorf("agent registry is not configured")
	}
	contextName := AgentProxyContextName(clusterID)
	if m.agentProxyContexts == nil {
		m.agentProxyContexts = make(map[string]string)
	}
	m.agentProxyContexts[contextName] = clusterID
	if m.kubeConfig.Contexts == nil {
		m.kubeConfig.Contexts = map[string]*clientcmdapi.Context{}
	}
	if m.kubeConfig.Clusters == nil {
		m.kubeConfig.Clusters = map[string]*clientcmdapi.Cluster{}
	}
	m.kubeConfig.Clusters[contextName] = &clientcmdapi.Cluster{Server: agentProxyAPIServerURL(clusterID)}
	m.kubeConfig.Contexts[contextName] = &clientcmdapi.Context{Cluster: contextName}
	if displayName != "" && m.storage != nil {
		_ = m.storage.SetDisplayName(contextName, displayName)
	}
	slog.Info("registered agent-proxy cluster",
		slog.String("cluster_id", clusterID),
		slog.String("context", contextName),
		slog.String("display_name", displayName),
	)

	// Auto-retry on register: boot-restore + agent-reconnect race.
	// When the API boots before the agent reconnects, the user's UI may
	// auto-switch to this cluster and Connector.Start() times out on
	// cache sync because no agent is listening yet. Once an agent
	// finally registers (this very call site, via auto_register.go),
	// we have a live channel — re-attempt the connector so the cluster
	// recovers without forcing the user to manually re-switch (which
	// isn't even reachable when this is the only cluster in the
	// dropdown). Skipped at boot-restore time because m.activeContext
	// is empty there. Skipped on subsequent Hellos when the connector
	// is already healthy. The retry runs in a goroutine so we don't
	// hold mu across ~5-20s of cache-sync I/O while every other agent
	// register and ListClusters call piles up behind it.
	if contextName == m.activeContext && m.connector == nil {
		go m.retryAgentProxyConnect(contextName, clusterID)
	}
	return contextName, nil
}

// retryAgentProxyConnect re-runs connectToContextLocked for an agent-
// proxy context whose first connect failed (typically with cache-sync
// timeout) before any agent had registered. Called from
// AddAgentProxyCluster's goroutine after a fresh agent registration.
//
// Re-checks state under the lock because by the time the goroutine
// runs, the user could have switched away or another concurrent
// register-triggered retry could have already recovered the connector.
func (m *Manager) retryAgentProxyConnect(contextName, clusterID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if contextName != m.activeContext || m.connector != nil {
		return
	}
	slog.Info("retrying connector after agent registered",
		slog.String("cluster_id", clusterID),
		slog.String("context", contextName),
	)
	if err := m.connectToContextLocked(contextName); err != nil {
		m.connErr = err
		slog.Warn("connector retry still failed",
			slog.String("context", contextName),
			slog.String("error", err.Error()),
		)
		return
	}
	m.connErr = nil
	slog.Info("connector recovered for agent-proxy cluster",
		slog.String("context", contextName),
	)
	// Push the recovery to connected UIs so they invalidate
	// `['clusters']` + `['cluster-overview']` immediately, instead of
	// waiting up to 30s for TanStack Query's refetch tick. Without
	// this nudge a user who saw the "Cluster unreachable" page right
	// after boot keeps seeing it long after the backend recovered,
	// and concludes the fix didn't work.
	if m.wsHub != nil {
		m.wsHub.Broadcast(websocket.ClusterConnected, map[string]string{
			"context":   contextName,
			"clusterId": clusterID,
		})
	}
}

// RemoveAgentProxyCluster removes the agent-proxy registration for
// clusterID. If the manager is currently switched to it, disconnects
// first. No-op if the cluster wasn't registered.
func (m *Manager) RemoveAgentProxyCluster(clusterID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	contextName := AgentProxyContextName(clusterID)
	if _, ok := m.agentProxyContexts[contextName]; !ok {
		return
	}
	if m.activeContext == contextName {
		m.stopCurrent()
		m.activeContext = ""
	}
	delete(m.agentProxyContexts, contextName)
	delete(m.kubeConfig.Contexts, contextName)
	delete(m.kubeConfig.Clusters, contextName)
	if m.storage != nil {
		m.storage.DeleteDisplayName(contextName)
	}
	slog.Info("removed agent-proxy cluster", slog.String("cluster_id", clusterID))
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
		switch {
		case m.agentProxyContexts[ctxName] != "":
			source = "agent-proxy"
		case m.inCluster && ctxName == "in-cluster":
			source = "in-cluster"
		case uploadedContexts[ctxName]:
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

// ActiveAgentProxyClusterID returns the cluster_id when the active
// session reaches its apiserver via agent-proxy (i.e. the only path
// to that cluster is through the connected kubebolt-agent). Empty
// string when the active session goes via kubeconfig / in-cluster /
// no active session at all.
//
// Used by destructive admin operations (Uninstall agent, force
// rolling restart) to detect "the action is about to sever the
// only path to the cluster I'm operating on" and gate behind an
// explicit force confirmation.
func (m *Manager) ActiveAgentProxyClusterID() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.agentProxyContexts[m.activeContext]
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

// accessForContextLocked picks the right ClusterAccess for contextName.
// Agent-proxy registrations win over kubeconfig contexts so a cluster
// reached via agent-proxy can never be silently shadowed by a stale
// kubeconfig entry of the same name. Returns nil when the context is
// unknown.
func (m *Manager) accessForContextLocked(contextName string) *ClusterAccess {
	if cid, ok := m.agentProxyContexts[contextName]; ok {
		return NewAgentProxyAccess(cid, m.agentRegistry)
	}
	if m.inCluster && contextName == "in-cluster" {
		return NewInClusterAccess()
	}
	if _, ok := m.kubeConfig.Contexts[contextName]; ok {
		return NewLocalAccess(m.kubeconfigPath, contextName)
	}
	return nil
}

func (m *Manager) connectToContextLocked(contextName string) error {
	// Fast-fail for agent-proxy contexts when no agent is connected.
	// Without this short-circuit, Connector.Start() spends the full
	// WaitForCacheSync(20s) timeout listing resources that all return
	// "channel: no agent connected" — burning 20s of UI lag on a
	// switch attempt that was always going to fail. The auto-retry
	// hook in AddAgentProxyCluster brings the connector up as soon as
	// an agent dials in (typically <5s after the first failure here).
	if cid, ok := m.agentProxyContexts[contextName]; ok && m.agentRegistry != nil && m.agentRegistry.CountByCluster(cid) == 0 {
		return fmt.Errorf("no agent connected yet for cluster %q — waiting for agent to register", cid)
	}
	access := m.accessForContextLocked(contextName)
	if access == nil {
		return fmt.Errorf("context %q is not registered", contextName)
	}
	connector, err := NewConnectorFromAccess(access, m.wsHub)
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

// NewConnectorForContext creates a connector for a specific kubeconfig
// context. Thin wrapper around the access-aware entry point; kept for
// callers that haven't migrated to ClusterAccess yet.
func NewConnectorForContext(kubeconfigPath, contextName string, wsHub *websocket.Hub) (*Connector, error) {
	return NewConnectorFromAccess(NewLocalAccess(kubeconfigPath, contextName), wsHub)
}

// NewConnectorInCluster creates a connector using in-cluster ServiceAccount credentials.
func NewConnectorInCluster(wsHub *websocket.Hub) (*Connector, error) {
	return NewConnectorFromAccess(NewInClusterAccess(), wsHub)
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
