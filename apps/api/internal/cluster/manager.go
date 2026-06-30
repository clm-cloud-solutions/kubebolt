package cluster

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/kubebolt/kubebolt/apps/api/internal/agent/channel"
	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/helm"
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
	connErr         error        // set when the active context failed to connect
	storage         ClusterStore // optional — nil when auth disabled; drives user-uploaded contexts and display names. W1 seam: BoltDB *Storage (OSS), Postgres (EE).

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

	// insightStore persists insight identities across restarts (Sprint 0).
	// Set by main.go via SetInsightStore; nil when the BoltDB store is
	// unavailable, in which case engines run in-memory-only. tenantID
	// scopes every persisted record ("default" in OSS single-tenant).
	insightStore insights.InsightStore
	tenantID     string

	// cacheSyncTimeoutFn returns the live informer cache-sync deadline for a
	// cold connect, read per-connect so a Settings → General change takes
	// effect on the next switch with no restart. nil → connector uses its
	// built-in default. Wired from main.go to settingsRuntime.General().
	cacheSyncTimeoutFn func() time.Duration

	// runtimes holds per-(tenant,cluster) runtimes for NON-active clusters
	// (W2). The active cluster's runtime stays in the fields above
	// (m.connector/collector/engine/cancelFn); lazy spin-up into this pool
	// lands in A.3b. nil until first use — reads of a nil map are safe.
	runtimes map[poolKey]*clusterRuntime

	// poolIdleTimeout / poolMaxRuntimes bound the parked-runtime pool (A.3d).
	// Each pooled runtime keeps live informers (memory + apiserver watches),
	// so the pool can't grow without bound: the reaper evicts runtimes idle
	// longer than poolIdleTimeout, and parking evicts the LRU when the pool
	// would exceed poolMaxRuntimes. Zero disables the respective limit (the
	// active runtime is never pooled, so it's never evicted). reaperStop stops
	// the reaper goroutine from Stop().
	poolIdleTimeout time.Duration
	poolMaxRuntimes int
	reaperStop      chan struct{}

	// activeGate is the broadcast gate of the CURRENTLY active runtime (the one
	// in m.connector/m.engine). Held true while active; parking flips it false
	// before stashing the runtime in the pool, and promoting flips the target's
	// gate true. Lets parked runtimes keep their informers + eval loop alive
	// (instant switch-back) without leaking WS events for a cluster nobody is
	// viewing (A.4 OSS hole). nil when nothing is connected.
	activeGate *atomic.Bool
}

// poolKey identifies a pooled runtime. tenant is "default" in OSS.
type poolKey struct{ tenant, cluster string }

// clusterRuntime bundles the live machinery for one (tenant,cluster): the
// connector (informers), metrics collector, insights engine, and the cancel
// that tears down their goroutines. The active cluster's runtime currently
// lives in the Manager's own fields; pooled runtimes for non-active clusters
// (EE/multi-tenant) populate Manager.runtimes in A.3b.
// Design: internal/kubebolt-w2-connector-pool-design.md.
type clusterRuntime struct {
	connector *Connector
	collector *metrics.Collector
	engine    *insights.Engine
	cancelFn  context.CancelFunc
	connErr   error
	// gate is the shared active/parked broadcast switch for this runtime's
	// connector + engine (A.4 OSS hole). true = broadcasting (active), false =
	// parked (informers + eval keep running, WS broadcasts suppressed). The
	// manager flips it on park/promote; the connector and engine read it.
	gate *atomic.Bool
	// lastUsed is the wall-clock time this runtime was last parked or served
	// from the pool (A.3d). The idle reaper evicts pooled runtimes whose
	// lastUsed predates poolIdleTimeout; the cap evicts the LRU (oldest
	// lastUsed). Zero/unused for the active runtime — it's never pooled.
	// Guarded by Manager.mu.
	lastUsed time.Time
	// ready is closed when a pooled runtime finishes building (success or
	// failure), so concurrent callers single-flight on the same spin instead
	// of each launching a connector. nil for the active runtime (built
	// synchronously under the lock).
	ready chan struct{}
}

// SetInsightStore wires the persistent insights store + tenant scope. The
// initial cluster connection is async (kicked off inside NewManager), so the
// engine may ALREADY exist by the time main.go calls this — created with a nil
// store (the boot race). Future engines pick up m.insightStore; the live one
// is updated in place via engine.SetStore so it persists + serves history this
// session too. tenantID defaults to "default" (OSS single-tenant) when empty.
func (m *Manager) SetInsightStore(store insights.InsightStore, tenantID string) {
	m.mu.Lock()
	m.insightStore = store
	m.tenantID = tenantID
	eng := m.engine // capture under lock; call SetStore outside to avoid lock-ordering
	// Generalize the boot-race fix to the pool (A.3d): pooled (parked) engines
	// built before the store was wired must pick it up too, else a cluster you
	// switched away from stops persisting insights. In OSS this list is empty
	// at boot (SetInsightStore runs before any switch parks a runtime), so it's
	// a no-op there — but it keeps the pool correct under EE multi-tenant.
	var pooled []*insights.Engine
	for _, rt := range m.runtimes {
		if rt.engine != nil {
			pooled = append(pooled, rt.engine)
		}
	}
	m.mu.Unlock()
	if eng != nil {
		eng.SetStore(store, tenantID)
	}
	for _, e := range pooled {
		e.SetStore(store, tenantID)
	}
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

// bindInsightHooks wires this manager's notification dispatchers onto one engine,
// stamping the given cluster context into every call. Always-on (W2 §10): EVERY
// runtime — active or parked — gets the hook, so an insight on ANY connected
// cluster notifies (parked engines keep evaluating). Assumes m.mu is held (reads
// m.onNewInsight/m.onResolvedInsight).
func (m *Manager) bindInsightHooks(eng *insights.Engine, ctxName string) {
	if eng == nil {
		return
	}
	if m.onNewInsight != nil {
		hook := m.onNewInsight
		eng.SetOnNewInsight(func(insight models.Insight) {
			// Called with engine lock held — keep fast; the notification manager
			// dispatches async.
			hook(ctxName, insight)
		})
	}
	if m.onResolvedInsight != nil {
		hook := m.onResolvedInsight
		eng.SetOnResolvedInsight(func(insight models.Insight) {
			hook(ctxName, insight)
		})
	}
}

// wireInsightHookLocked (re)attaches the notification hooks to EVERY runtime's
// engine: the active one (stamped with m.activeContext) and all parked pool
// runtimes (each stamped with its own context). Always-on (W2 §10) — parked
// clusters notify too, routed by context/tenant downstream. Assumes m.mu held.
func (m *Manager) wireInsightHookLocked() {
	m.bindInsightHooks(m.engine, m.activeContext)
	for pk, rt := range m.runtimes {
		m.bindInsightHooks(rt.engine, pk.cluster)
	}
}

// SetCacheSyncTimeoutProvider wires a provider for the informer cache-sync
// deadline, read fresh on every cold connect (Settings → General changes apply
// with no restart). Pass nil to fall back to the connector's built-in default.
func (m *Manager) SetCacheSyncTimeoutProvider(fn func() time.Duration) {
	m.mu.Lock()
	m.cacheSyncTimeoutFn = fn
	m.mu.Unlock()
}

// resolveCacheSyncTimeoutLocked returns the configured cache-sync deadline, or
// 0 (connector falls back to its default) when no provider is wired. Caller
// holds m.mu. The provider reads settingsRuntime, which has its own lock — no
// lock-ordering issue.
func (m *Manager) resolveCacheSyncTimeoutLocked() time.Duration {
	if m.cacheSyncTimeoutFn != nil {
		return m.cacheSyncTimeoutFn()
	}
	return 0
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
		_ = m.storage.SetDisplayName(m.storeCtx(), contextName, displayName)
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
	// Always-on (W2 §10): eager-spin this cluster's runtime into the pool as soon
	// as its agent connects, so it monitors (evaluates + notifies) even if nobody
	// opens it in the UI — and so monitoring resumes after a backend restart
	// without a manual view. The active context is covered by the retry above;
	// here we cover the non-active connected clusters. Skip if already pooled.
	if contextName != m.activeContext {
		if _, ok := m.runtimes[poolKey{tenant: m.tenantID, cluster: contextName}]; !ok {
			go m.eagerSpinPooledRuntime(contextName, clusterID)
		}
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
	// The agent can land in the registry just AFTER the proxy-cluster hook that
	// spawned us, so the first CountByCluster can briefly read 0 ("no agent
	// connected yet") even though the agent is alive. A single shot then strands
	// the cluster forever — there's no later trigger — which is fatal for an
	// install with ONE cluster (no other cluster to switch to and re-fire a
	// connect). So retry a few times with a short backoff, releasing the lock
	// between tries, until the registration settles. Only the registration race
	// is retried; a real connect error (cache-sync, etc.) returns immediately.
	//
	// CRITICAL (isolation): spin the runtime OUTSIDE the global lock. getOrSpinPooled
	// runs the slow connector.Start()/WaitForCacheSync (~20s) WITHOUT holding m.mu, then
	// we promote the built runtime to active under a BRIEF lock. The old path called
	// connectToContextLocked UNDER m.mu for the whole sync, so a single reconnecting
	// agent — or a metrics→operator upgrade — froze the API for every other cluster until
	// the sync finished.
	const maxAttempts = 12
	for attempt := 0; attempt < maxAttempts; attempt++ {
		m.mu.RLock()
		stillNeeded := contextName == m.activeContext && m.connector == nil
		m.mu.RUnlock()
		if !stillNeeded {
			return // switched away, or another goroutine already recovered it
		}
		if attempt == 0 {
			slog.Info("retrying connector after agent registered",
				slog.String("cluster_id", clusterID),
				slog.String("context", contextName),
			)
		}
		// Slow build, lock-free — never blocks other clusters.
		rt := m.getOrSpinPooled(m.tenantID, contextName)
		if rt == nil || rt.connector == nil {
			// Registration race (agent not visible in the registry yet) or a transient
			// connect error — both worth a short wait + retry; a hard error settles to
			// nil and the loop gives up after maxAttempts.
			time.Sleep(500 * time.Millisecond)
			continue
		}
		// Promote the spun runtime to active under a BRIEF lock — fast, no I/O held.
		// Mirrors SwitchCluster's pooled-promotion path.
		m.mu.Lock()
		if contextName == m.activeContext && m.connector == nil {
			delete(m.runtimes, poolKey{tenant: m.tenantID, cluster: contextName})
			m.connector = rt.connector
			m.collector = rt.collector
			m.engine = rt.engine
			m.cancelFn = rt.cancelFn
			m.activeGate = rt.gate
			if rt.gate != nil {
				rt.gate.Store(true) // promoted → resume broadcasting
			}
			m.activeContext = contextName
			m.connErr = nil
			m.wireInsightHookLocked()
		}
		m.mu.Unlock()
		slog.Info("connector recovered for agent-proxy cluster",
			slog.String("context", contextName),
			slog.Int("attempt", attempt+1),
		)
		// Push the recovery to connected UIs so they invalidate `['clusters']` +
		// `['cluster-overview']` immediately instead of waiting on the 30s refetch.
		if m.wsHub != nil {
			m.wsHub.Broadcast(websocket.ClusterConnected, map[string]string{
				"context":   contextName,
				"clusterId": clusterID,
			})
		}
		return
	}
	slog.Warn("connector retry exhausted — agent never became visible in the registry",
		slog.String("cluster_id", clusterID),
		slog.String("context", contextName),
	)
}

// RemoveAgentProxyCluster removes the agent-proxy registration for
// clusterID. If the manager is currently switched to it, disconnects
// first. No-op if the cluster wasn't registered.
func (m *Manager) RemoveAgentProxyCluster(clusterID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	contextName := AgentProxyContextName(clusterID)

	// Persistent cleanup — runs even when the in-memory context isn't registered,
	// so a disconnected cluster's lingering display name + cached UID can still be
	// deleted.
	if m.storage != nil {
		m.storage.DeleteDisplayName(m.storeCtx(), contextName)
		_ = m.storage.SetClusterUID(m.storeCtx(), contextName, "") // "" → delete the cached UID row
	}

	// In-memory teardown — nothing to do if the context isn't registered.
	if _, ok := m.agentProxyContexts[contextName]; !ok {
		return
	}
	if m.activeContext == contextName {
		m.stopCurrent()
		m.activeContext = ""
	}
	m.evictPooledContextLocked(contextName, "cluster removed")
	delete(m.agentProxyContexts, contextName)
	delete(m.kubeConfig.Contexts, contextName)
	delete(m.kubeConfig.Clusters, contextName)
	slog.Info("removed agent-proxy cluster", slog.String("cluster_id", clusterID))
}

// SetStorage attaches a cluster storage to the manager. This must be called
// after NewManager but before the HTTP router starts serving. After attaching,
// the manager merges any user-uploaded kubeconfigs into its in-memory config.
func (m *Manager) SetStorage(s ClusterStore) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.storage = s
	return m.reloadUploadedContextsLocked()
}

// Storage returns the attached storage, or nil if none was set.
func (m *Manager) Storage() ClusterStore {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.storage
}

// storeCtx returns the context the manager threads into ClusterStore calls.
// Boot-time and manager-internal store access has no request org, so it carries
// the manager's default-org tenant (m.tenantID — the default tenant UUID in EE,
// "default"/empty in OSS) via auth.WithTenantID. The EE Postgres store reads
// this off the ctx and runs each query inside eedb.WithOrg so single-org cluster
// loading keeps working under RLS. The Bolt store ignores the ctx entirely.
// Falls back to context.Background() when m.tenantID is empty.
func (m *Manager) storeCtx() context.Context {
	if m.tenantID == "" {
		return context.Background()
	}
	return auth.WithTenantID(context.Background(), m.tenantID)
}

// reloadUploadedContextsLocked merges kubeconfigs from BoltDB into the in-memory
// config. Called on startup and after CRUD operations. Assumes m.mu is held.
func (m *Manager) reloadUploadedContextsLocked() error {
	if m.storage == nil {
		return nil
	}
	configs, err := m.storage.ListKubeconfigs(m.storeCtx())
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

	// ClusterID is the kube-system namespace UID — the same value the
	// agent stamps on every sample's `cluster_id` label. Currently
	// known only for agent-proxy contexts (the Hello message carries
	// it on registration). Empty for direct-kubeconfig contexts that
	// we haven't probed at boot.
	//
	// Surfaced here so the admin UI can scope ingest tokens to a
	// specific cluster at issue-time (5a.1.a — Prometheus integration
	// per-cluster filtering).
	ClusterID string `json:"clusterId,omitempty"`
}

// Pool bounds (A.3d). A parked runtime keeps live informers, so the pool is
// capped and idle-evicted. Defaults suit OSS (a handful of clusters in the
// switcher); EE tunes them per-org. Switching back to an evicted cluster pays
// the cold connect again — acceptable, since eviction only hits clusters left
// untouched for poolIdleTimeout.
const (
	defaultPoolIdleTimeout = 30 * time.Minute
	defaultPoolMaxRuntimes = 8
)

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
				poolIdleTimeout: defaultPoolIdleTimeout,
				poolMaxRuntimes: defaultPoolMaxRuntimes,
				reaperStop:      make(chan struct{}),
			}
			m.startPoolReaper()

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
		activeContext:   kubeConfig.CurrentContext,
		wsHub:           wsHub,
		metricInterval:  metricInterval,
		insightInterval: insightInterval,
		poolIdleTimeout: defaultPoolIdleTimeout,
		poolMaxRuntimes: defaultPoolMaxRuntimes,
		reaperStop:      make(chan struct{}),
	}
	m.startPoolReaper()

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

	// Pre-compute uploaded context names, display names, and
	// cached cluster UIDs (single DB read each — bulk lookup beats
	// per-context queries in the loop below).
	uploadedContexts := make(map[string]bool)
	displayNames := make(map[string]string)
	cachedUIDs := make(map[string]string)
	if m.storage != nil {
		sctx := m.storeCtx()
		if configs, err := m.storage.ListKubeconfigs(sctx); err == nil {
			for _, c := range configs {
				uploadedContexts[c.Context] = true
			}
		}
		if names, err := m.storage.AllDisplayNames(sctx); err == nil {
			displayNames = names
		}
		if uids, err := m.storage.AllClusterUIDs(sctx); err == nil {
			cachedUIDs = uids
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
		// Resolve ClusterID with a three-source preference:
		//   1. agent-proxy context — UID from the Hello message,
		//      available before any connection happens.
		//   2. live Connector for the active context — fresh from
		//      the kube-system namespace lookup we just did.
		//   3. cached UID from a past connection (5a.1.e) — lets
		//      direct-kubeconfig contexts the operator visited
		//      previously keep their UID visible without re-connecting.
		// Empty otherwise; the admin UI treats empty as "unknown,
		// pick Any cluster".
		clusterID := m.agentProxyContexts[ctxName]
		if clusterID == "" && isActive && m.connector != nil {
			clusterID = m.connector.ClusterUID()
		}
		if clusterID == "" {
			clusterID = cachedUIDs[ctxName]
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
			ClusterID:   clusterID,
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

	// Park the CURRENT active runtime in the pool instead of tearing it down,
	// so switching BACK to it later is instant (no reconnect). Its informers,
	// collector, and insight ticker stay live in the background.
	m.parkActiveLocked()

	// If the target is already pooled (previously connected, informers still
	// live), promote it — an INSTANT switch with no WaitForCacheSync. This is
	// the whole point: re-selecting a cluster you've already connected to
	// should be immediate, not a 20s reconnect every time.
	pk := poolKey{tenant: m.tenantID, cluster: contextName}
	if rt, ok := m.runtimes[pk]; ok && rt.connector != nil {
		delete(m.runtimes, pk)
		m.connector = rt.connector
		m.collector = rt.collector
		m.engine = rt.engine
		m.cancelFn = rt.cancelFn
		m.activeGate = rt.gate
		if rt.gate != nil {
			rt.gate.Store(true) // promoted → resume broadcasting
		}
		m.activeContext = contextName
		m.connErr = nil
		m.wireInsightHookLocked()
		slog.Info("switched cluster context (pooled, instant)", slog.String("context", contextName))
		return nil
	}

	// First connect to this cluster — the only slow path (~20s WaitForCacheSync).
	// CRITICAL (isolation): do NOT hold m.mu across the sync. Claim the active context,
	// RELEASE the lock, spin the runtime via getOrSpinPooled (which runs connector.Start()
	// lock-free), then re-acquire briefly to promote it. Holding m.mu across the ~20s
	// connect froze every OTHER cluster during a switch. The defer'd Unlock balances the
	// Lock re-acquired below.
	m.activeContext = contextName
	m.mu.Unlock()

	rt := m.getOrSpinPooled(m.tenantID, contextName)

	m.mu.Lock()
	if rt == nil || rt.connector == nil {
		if m.activeContext == contextName {
			m.connErr = fmt.Errorf("could not connect to cluster %q — agent not connected yet, or cache sync failed", contextName)
		}
		slog.Warn("failed to connect to context, staying disconnected",
			slog.String("context", contextName))
		return fmt.Errorf("connecting to context %q failed", contextName)
	}
	// Promote the spun runtime to active only if this switch still owns the active
	// context and nothing connected it first.
	if m.activeContext == contextName && m.connector == nil {
		delete(m.runtimes, pk)
		m.connector = rt.connector
		m.collector = rt.collector
		m.engine = rt.engine
		m.cancelFn = rt.cancelFn
		m.activeGate = rt.gate
		if rt.gate != nil {
			rt.gate.Store(true) // promoted → resume broadcasting
		}
		m.connErr = nil
		m.wireInsightHookLocked()
	}
	slog.Info("switched cluster context (fresh connect)", slog.String("context", contextName))
	return nil
}

// parkActiveLocked moves the current active runtime (if any) into the pool,
// keyed by its context, so a later SwitchCluster back to it reuses the live,
// already-synced runtime instead of reconnecting. The runtime keeps running.
// Caller holds m.mu. Pool growth is bounded by the cap (enforced here) and the
// idle reaper (A.3d).
func (m *Manager) parkActiveLocked() {
	if m.connector == nil || m.activeContext == "" {
		return
	}
	if m.runtimes == nil {
		m.runtimes = map[poolKey]*clusterRuntime{}
	}
	m.runtimes[poolKey{tenant: m.tenantID, cluster: m.activeContext}] = &clusterRuntime{
		connector: m.connector,
		collector: m.collector,
		engine:    m.engine,
		cancelFn:  m.cancelFn,
		gate:      m.activeGate,
		lastUsed:  time.Now(),
	}
	// Parked → stop broadcasting (informers + eval keep running for instant
	// switch-back, but no WS noise for a cluster nobody is viewing).
	if m.activeGate != nil {
		m.activeGate.Store(false)
	}
	m.connector = nil
	m.collector = nil
	m.engine = nil
	m.cancelFn = nil
	m.activeGate = nil
	m.connErr = nil
	// Bound the pool: evict the LRU parked runtime(s) if we just went over cap.
	// The one we parked above has the freshest lastUsed, so it's never the
	// victim of its own park.
	m.enforcePoolCapLocked()
}

// evictPoolEntryLocked tears down one pooled runtime and removes it from the
// pool. Caller holds m.mu. Skips the goroutine teardown for placeholders still
// building (connector nil) but still deletes the map entry so a removed cluster
// doesn't linger. Never call this for the active runtime — it isn't pooled.
func (m *Manager) evictPoolEntryLocked(pk poolKey, rt *clusterRuntime, reason string) {
	if rt.cancelFn != nil {
		rt.cancelFn()
	}
	if rt.connector != nil {
		rt.connector.Stop()
	}
	delete(m.runtimes, pk)
	slog.Info("evicted pooled cluster runtime",
		slog.String("cluster", pk.cluster),
		slog.String("reason", reason))
}

// enforcePoolCapLocked evicts the least-recently-used pooled runtime(s) until
// the pool is within poolMaxRuntimes. Caller holds m.mu. Placeholders that are
// still building (connector nil) are not eligible victims — they have no
// informers to reclaim yet and a concurrent caller is blocked on their ready
// channel.
func (m *Manager) enforcePoolCapLocked() {
	if m.poolMaxRuntimes <= 0 {
		return
	}
	for {
		// Count only fully-built runtimes against the cap, and track the LRU
		// among them. Placeholders still building (connector nil) are exempt:
		// they hold no informers yet and a caller is blocked on their ready.
		built := 0
		var victimKey poolKey
		var victim *clusterRuntime
		for pk, rt := range m.runtimes {
			if rt.connector == nil {
				continue
			}
			built++
			// Always-on (W2 §10): connected agent-proxy runtimes are pinned — never
			// the LRU victim. Bounded by the per-org cluster cap, not the pool cap;
			// if every built runtime is pinned, no victim is chosen.
			if m.runtimeIsConnectedProxyLocked(pk) {
				continue
			}
			if victim == nil || rt.lastUsed.Before(victim.lastUsed) {
				victimKey, victim = pk, rt
			}
		}
		if built <= m.poolMaxRuntimes || victim == nil {
			return
		}
		m.evictPoolEntryLocked(victimKey, victim, "pool cap")
	}
}

// evictPooledContextLocked evicts the pooled runtime for contextName, if one
// exists, so removing a cluster doesn't leak its parked informers. Caller holds
// m.mu. No-op when the context isn't pooled.
func (m *Manager) evictPooledContextLocked(contextName, reason string) {
	if m.runtimes == nil {
		return
	}
	pk := poolKey{tenant: m.tenantID, cluster: contextName}
	if rt, ok := m.runtimes[pk]; ok {
		m.evictPoolEntryLocked(pk, rt, reason)
	}
}

// runtimeIsConnectedProxyLocked reports whether the pooled runtime at key pk is an
// agent-proxy cluster whose agent is currently registered. Always-on (W2 §10):
// such runtimes are PINNED — kept resident (no idle reap, no LRU evict) so they
// keep evaluating + notifying. They become reapable again the moment the agent
// disconnects (CountByCluster drops to 0), so a dead proxy runtime never leaks.
// Assumes m.mu held.
func (m *Manager) runtimeIsConnectedProxyLocked(pk poolKey) bool {
	if m.agentRegistry == nil {
		return false
	}
	cid, ok := m.agentProxyContexts[pk.cluster]
	if !ok {
		return false // local-kubeconfig context — normal idle/LRU eviction applies
	}
	return m.agentRegistry.CountByCluster(cid) > 0
}

// eagerSpinPooledRuntime spins (and keeps) the pooled runtime for a freshly-
// connected agent-proxy cluster, so always-on monitoring starts at connect time
// rather than at first UI view (W2 §10). It waits out the register/visibility
// race cheaply (the agent can land in the registry just after this hook fires —
// same race as retryAgentProxyConnect), then spins ONCE: a real connect error
// shouldn't be retried for ~20s × 12. No lock held across the spin. Idempotent —
// getOrSpinPooled returns the existing runtime if a concurrent view already spun
// it. The reaper keeps the runtime resident while the agent stays connected.
func (m *Manager) eagerSpinPooledRuntime(contextName, clusterID string) {
	const maxWait = 12
	for attempt := 0; attempt < maxWait; attempt++ {
		m.mu.RLock()
		cid := m.agentProxyContexts[contextName]
		visible := m.agentRegistry != nil && cid != "" && m.agentRegistry.CountByCluster(cid) > 0
		m.mu.RUnlock()
		if visible {
			break
		}
		if attempt == maxWait-1 {
			return // agent never became visible — a later view or re-Hello will spin it
		}
		time.Sleep(500 * time.Millisecond)
	}
	if rt := m.getOrSpinPooled(m.tenantID, contextName); rt != nil && rt.connErr == nil {
		slog.Info("eager-spun agent-proxy runtime for always-on",
			slog.String("context", contextName),
			slog.String("cluster_id", clusterID),
		)
	}
}

// reapIdlePooledRuntimes evicts pooled runtimes left untouched longer than
// poolIdleTimeout. Runs on the reaper goroutine; takes m.mu itself.
func (m *Manager) reapIdlePooledRuntimes() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.poolIdleTimeout <= 0 || len(m.runtimes) == 0 {
		return
	}
	cutoff := time.Now().Add(-m.poolIdleTimeout)
	for pk, rt := range m.runtimes {
		if rt.connector == nil {
			continue // still building
		}
		// Always-on (W2 §10): a connected agent-proxy runtime is pinned — kept
		// resident so it keeps evaluating + notifying even when nobody's viewing
		// it. Reapable again the instant its agent disconnects.
		if m.runtimeIsConnectedProxyLocked(pk) {
			continue
		}
		if rt.lastUsed.Before(cutoff) {
			m.evictPoolEntryLocked(pk, rt, "idle timeout")
		}
	}
}

// startPoolReaper launches the background goroutine that idle-evicts pooled
// runtimes. No-op when idle eviction is disabled (poolIdleTimeout <= 0) or the
// stop channel was never created (directly-constructed Managers in tests). The
// ticker fires at a quarter of the idle window, floored at 1m.
func (m *Manager) startPoolReaper() {
	if m.poolIdleTimeout <= 0 || m.reaperStop == nil {
		return
	}
	interval := m.poolIdleTimeout / 4
	if interval < time.Minute {
		interval = time.Minute
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-m.reaperStop:
				return
			case <-ticker.C:
				m.reapIdlePooledRuntimes()
			}
		}
	}()
}

// PoolStats is a snapshot of the connector-runtime pool for observability
// (always-on, W2 §10.3): how many cluster runtimes are resident.
type PoolStats struct {
	Active   int // the global active runtime (0 or 1)
	Parked   int // built, pooled (non-active) runtimes — incl. eager-spun/pinned
	Building int // placeholders mid-spin (no informers yet)
}

// PoolStats returns a live snapshot of the runtime pool. Cheap (counts under the
// read lock); safe to call on every /metrics scrape.
func (m *Manager) PoolStats() PoolStats {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var s PoolStats
	if m.connector != nil {
		s.Active = 1
	}
	for _, rt := range m.runtimes {
		if rt.connector == nil {
			s.Building++
		} else {
			s.Parked++
		}
	}
	return s
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

// resolveRuntime returns the runtime for the request's (tenant,cluster),
// read from ctx's RuntimeKey (W2 A.1).
//
//   - Empty cluster, or the active context → the active runtime (today's
//     single-cluster behavior, unchanged). This is the only branch OSS
//     ever takes: the OSS frontend doesn't send X-KubeBolt-Cluster.
//   - A non-active cluster (EE/multi-tenant) → the pooled runtime, lazily
//     spun up on first request via single-flight (getOrSpinPooled).
//
// Returns nil when there is no runtime (startup before first connect, an
// unknown context, or a failed spin). Callers use the snapshot lock-free.
func (m *Manager) resolveRuntime(ctx context.Context) *clusterRuntime {
	key := RuntimeKeyFromContext(ctx)
	m.mu.RLock()
	active := key.Cluster == "" || key.Cluster == m.activeContext
	if active {
		rt := m.activeRuntimeLocked()
		m.mu.RUnlock()
		return rt
	}
	m.mu.RUnlock()
	return m.getOrSpinPooled(key.Tenant, key.Cluster)
}

// activeRuntimeLocked snapshots the active cluster's runtime. Caller holds
// m.mu. Returns nil when nothing is connected yet.
func (m *Manager) activeRuntimeLocked() *clusterRuntime {
	if m.connector == nil {
		return nil
	}
	return &clusterRuntime{
		connector: m.connector,
		collector: m.collector,
		engine:    m.engine,
		cancelFn:  m.cancelFn,
		connErr:   m.connErr,
	}
}

// getOrSpinPooled returns the pooled runtime for a non-active
// (tenant,cluster), lazily spinning it up on first request. Single-flight:
// the first caller reserves a placeholder and builds the connector OUTSIDE
// m.mu (Start can take ~20s); concurrent callers for the same key block on
// the placeholder's ready channel instead of launching a second connector.
// Returns nil on unknown context, no-agent-yet, or a failed build.
//
// NOTE (Fase B / EE): WS broadcasts are now (tenant,cluster)-tagged (A.4 seam)
// and the hub filters by client scope, but pooled runtimes are still gated-off
// in OSS (parkActiveLocked) and pooled engines don't get the notification hook
// (wireInsightHookLocked). EE flips this: pooled runtimes broadcast scoped (gate
// on), the frontend declares each client's active (tenant,cluster), and pooled
// engines notify. Harmless in OSS — the pool is only populated by a switch and
// Autopilot is single-cluster for now (internal/kubebolt-w2-connector-pool-design.md §4b).
func (m *Manager) getOrSpinPooled(tenant, contextName string) *clusterRuntime {
	pk := poolKey{tenant: tenant, cluster: contextName}

	m.mu.Lock()
	if m.runtimes == nil {
		m.runtimes = map[poolKey]*clusterRuntime{}
	}
	if rt, ok := m.runtimes[pk]; ok {
		rt.lastUsed = time.Now() // touch for LRU/idle accounting before releasing the lock
		m.mu.Unlock()
		<-rt.ready // wait if a concurrent caller is still building
		if rt.connErr != nil {
			return nil
		}
		return rt
	}
	// Same fast-fail as the active path: agent-proxy cluster with no agent.
	if cid, ok := m.agentProxyContexts[contextName]; ok && m.agentRegistry != nil && m.agentRegistry.CountByCluster(cid) == 0 {
		m.mu.Unlock()
		return nil
	}
	access := m.accessForContextLocked(contextName)
	if access == nil {
		m.mu.Unlock()
		return nil
	}
	// Snapshot the mutable bits startRuntime needs, reserve the placeholder.
	agentProxyCID := m.agentProxyContexts[contextName]
	insightStore := m.insightStore
	tenantID := m.tenantID
	cacheSyncTimeout := m.resolveCacheSyncTimeoutLocked()
	placeholder := &clusterRuntime{ready: make(chan struct{})}
	m.runtimes[pk] = placeholder
	m.mu.Unlock()

	// Build outside the lock (connector.Start ~20s).
	built, err := m.startRuntime(access, contextName, agentProxyCID, insightStore, tenantID, cacheSyncTimeout)
	if err != nil {
		m.mu.Lock()
		delete(m.runtimes, pk) // drop the failed placeholder so a retry rebuilds
		m.mu.Unlock()
		placeholder.connErr = err
		close(placeholder.ready)
		return nil
	}
	placeholder.connector = built.connector
	placeholder.collector = built.collector
	placeholder.engine = built.engine
	placeholder.cancelFn = built.cancelFn
	placeholder.gate = built.gate
	// A pooled (non-active) runtime doesn't broadcast in the OSS-degenerate
	// world — only the active cluster's events reach the shared hub. Full
	// per-(tenant,cluster) WS delivery for pooled runtimes is A.4.
	if built.gate != nil {
		built.gate.Store(false)
	}
	// Always-on (W2 §10): wire the notification hook onto this pooled (parked)
	// runtime's engine so an insight on this cluster notifies even though it's not
	// the active one. Re-read the manager hooks under the lock.
	m.mu.Lock()
	m.bindInsightHooks(placeholder.engine, contextName)
	m.mu.Unlock()
	placeholder.lastUsed = time.Now()
	close(placeholder.ready)
	return placeholder
}

// Connector returns the *Connector for the request's (tenant,cluster).
func (m *Manager) Connector(ctx context.Context) *Connector {
	if rt := m.resolveRuntime(ctx); rt != nil {
		return rt.connector
	}
	return nil
}

// Collector returns the metrics collector for the request's (tenant,cluster).
func (m *Manager) Collector(ctx context.Context) *metrics.Collector {
	if rt := m.resolveRuntime(ctx); rt != nil {
		return rt.collector
	}
	return nil
}

// Engine returns the insights engine for the request's (tenant,cluster).
func (m *Manager) Engine(ctx context.Context) *insights.Engine {
	if rt := m.resolveRuntime(ctx); rt != nil {
		return rt.engine
	}
	return nil
}

// Stop stops the active connector and collector, and tears down every
// pooled (non-active) runtime.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Stop the idle reaper. Guard against a double Stop() (and the
	// directly-constructed Managers in tests, which have a nil reaperStop).
	if m.reaperStop != nil {
		select {
		case <-m.reaperStop: // already closed
		default:
			close(m.reaperStop)
		}
	}
	m.stopCurrent()
	for pk, rt := range m.runtimes {
		if rt.cancelFn != nil {
			rt.cancelFn()
		}
		if rt.connector != nil {
			rt.connector.Stop()
		}
		delete(m.runtimes, pk)
	}
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
	m.activeGate = nil
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
	if configs, err := m.storage.ListKubeconfigs(m.storeCtx()); err == nil {
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
		if err := m.storage.SaveKubeconfig(m.storeCtx(), stored); err != nil {
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

	stored, err := m.storage.GetKubeconfig(m.storeCtx(), contextName)
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
	// Drop any parked runtime for it so its informers don't linger in the pool.
	m.evictPooledContextLocked(contextName, "cluster removed")

	// Remove from BoltDB
	if err := m.storage.DeleteKubeconfig(m.storeCtx(), contextName); err != nil {
		return err
	}

	// Remove from in-memory config. Only remove the context entry;
	// shared clusters/authInfos may still be referenced by others.
	delete(m.kubeConfig.Contexts, contextName)

	// Also remove any display name override
	m.storage.DeleteDisplayName(m.storeCtx(), contextName)

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
	return m.storage.SetDisplayName(m.storeCtx(), contextName, displayName)
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
		// m.tenantID is the runtime's org (default tenant UUID today; the
		// per-request org once the multi-org pooled threading lands). It
		// flows into the proxy transport so the registry's tenant guard
		// (W4) only resolves agents this org owns.
		return NewAgentProxyAccess(m.tenantID, cid, m.agentRegistry)
	}
	if m.inCluster && contextName == "in-cluster" {
		return NewInClusterAccess()
	}
	if m.kubeConfig != nil {
		if _, ok := m.kubeConfig.Contexts[contextName]; ok {
			return NewLocalAccess(m.kubeconfigPath, contextName)
		}
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
	// Build the active runtime. Holding m.mu across startRuntime keeps the
	// prior behavior (the ~20s connector.Start runs under the lock for a
	// switch). Pooled spins (getOrSpinPooled) call startRuntime OUTSIDE the
	// lock instead.
	rt, err := m.startRuntime(access, contextName, m.agentProxyContexts[contextName], m.insightStore, m.tenantID, m.resolveCacheSyncTimeoutLocked())
	if err != nil {
		return err
	}
	m.connector = rt.connector
	m.collector = rt.collector
	m.engine = rt.engine
	m.cancelFn = rt.cancelFn
	m.activeGate = rt.gate // active runtime broadcasts (gate already true)
	m.activeContext = contextName
	m.connErr = nil

	// Wire notification hook if one was registered before this connection
	// was established (or if we just switched clusters).
	m.wireInsightHookLocked()

	return nil
}

// startRuntime connects to contextName and returns a fully-started runtime
// (connector + collector + engine + the cancel that tears down their
// goroutines). It takes NO lock and reads only effectively-immutable
// Manager fields (wsHub, metricInterval, insightInterval, storage) plus the
// snapshot params the caller resolves under m.mu — so the slow
// connector.Start (~20s WaitForCacheSync) can run outside the lock for
// pooled spins. The active path calls it while holding m.mu (unchanged).
//
// Mirrors what connectToContextLocked used to inline — keep the two in sync.
func (m *Manager) startRuntime(access *ClusterAccess, contextName, agentProxyCID string, insightStore insights.InsightStore, tenantID string, cacheSyncTimeout time.Duration) (*clusterRuntime, error) {
	connector, err := NewConnectorFromAccess(access, m.wsHub)
	if err != nil {
		return nil, fmt.Errorf("connecting to context %s: %w", contextName, err)
	}
	// Apply the live cache-sync deadline before Start() (default when 0).
	connector.SetCacheSyncTimeout(cacheSyncTimeout)
	if err := connector.Start(); err != nil {
		connector.Stop()
		return nil, fmt.Errorf("starting connector for context %s: %w", contextName, err)
	}

	// Shared broadcast gate for this runtime — starts enabled. The caller
	// (connectToContextLocked / getOrSpinPooled) decides whether this runtime
	// is active (keep enabled) or pooled (disable), and park/promote flip it.
	gate := &atomic.Bool{}
	gate.Store(true)
	connector.SetBroadcastGate(gate)
	// A.4: tag this runtime's WS broadcasts with (tenant, context) so EE
	// clients only receive their own cluster's events. OSS clients carry no
	// scope, so this is inert there.
	connector.SetWSScope(tenantID, contextName)

	collector := metrics.NewCollector(connector.MetricsClient(), m.metricInterval, connector.Permissions().ScopedNamespaces())
	connector.SetCollector(collector)

	ctx, cancel := context.WithCancel(context.Background())
	collector.Poll(ctx) // synchronous initial poll so metrics are ready first
	go collector.Start(ctx)

	// Stable clusterID for persisted insights: kube-system UID, else the
	// agent-proxy cluster_id, else the context name.
	engineClusterID := connector.ClusterUID()
	if engineClusterID == "" {
		if agentProxyCID != "" {
			engineClusterID = agentProxyCID
		} else {
			engineClusterID = contextName
		}
	}
	engine := insights.NewEngine(m.wsHub, insightStore, engineClusterID, tenantID)
	engine.SetBroadcastGate(gate)
	engine.SetWSScope(tenantID, contextName)

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

	// Persist the resolved kube-system UID so ListClusters can populate
	// ClusterID without a re-connect. Skip on empty (lookup failed) rather
	// than overwriting a previously-good value.
	if m.storage != nil {
		if uid := connector.ClusterUID(); uid != "" {
			_ = m.storage.SetClusterUID(m.storeCtx(), contextName, uid)
		}
	}

	return &clusterRuntime{connector: connector, collector: collector, engine: engine, cancelFn: cancel, gate: gate}, nil
}

func evaluateInsights(connector *Connector, collector *metrics.Collector, engine *insights.Engine) {
	// Helm releases aren't informer-backed (decoded on demand from storage
	// Secrets) — fetch them here so the helm-release insight rules can run.
	// A single label-selected Secret list per tick; cheap and best-effort.
	var helmReleases []helm.Release
	if secrets, err := connector.ListHelmReleaseSecrets(context.Background()); err == nil {
		helmReleases = helm.DecodeReleases(secrets)
	}
	state := &insights.ClusterState{
		Pods:            connector.GetPods(),
		Deployments:     connector.GetDeployments(),
		Nodes:           connector.GetNodes(),
		HPAs:            connector.GetHPAs(),
		PVCs:            connector.GetPVCs(),
		Events:          connector.GetEventsRaw(),
		Services:        connector.GetServices(),
		EndpointSlices:  connector.GetEndpointSlices(),
		NetworkPolicies: connector.GetNetworkPolicies(),
		PDBs:            connector.GetPodDisruptionBudgets(),
		HelmReleases:    helmReleases,
		Certificates:    connector.listOptionalCRD("certificates", ""),
		ArgoApps:        connector.listOptionalCRD("argocdapps", ""),
		PodMetrics:      collector.GetAllPodMetrics(),
		NodeMetrics:     collector.GetAllNodeMetrics(),
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
