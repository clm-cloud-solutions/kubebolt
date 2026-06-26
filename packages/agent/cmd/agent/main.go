// kubebolt-agent — Sprint 1 Phase B.
//
// Pipeline (per node):
//
//   [kubelet /stats/summary] --collect 15s--> ┐
//                                             ├── enrich (pods cache) ──> [ring buffer] ──> [shipper] ──> gRPC stream
//   [kubelet /pods] --refresh 30s--> cache ───┘
//
// See internal/kubebolt-agent-technical-spec.md for the full design.
package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	_ "net/http/pprof" // registers /debug/pprof handlers on http.DefaultServeMux when the endpoint is enabled via KUBEBOLT_AGENT_PPROF_ADDR
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/kubebolt/kubebolt/packages/agent/internal/buffer"
	"github.com/kubebolt/kubebolt/packages/agent/internal/collector"
	"github.com/kubebolt/kubebolt/packages/agent/internal/flows"
	"github.com/kubebolt/kubebolt/packages/agent/internal/kubelet"
	"github.com/kubebolt/kubebolt/packages/agent/internal/promread"
	"github.com/kubebolt/kubebolt/packages/agent/internal/proxy"
	"github.com/kubebolt/kubebolt/packages/agent/internal/self"
	"github.com/kubebolt/kubebolt/packages/agent/internal/shipper"
	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"
)

// agentVersion is reported in the gRPC Hello and as the
// `agent_version` label of `kubebolt_agent_info`. Bump on schema-
// affecting changes — the backend uses semver comparison against
// MinAgentVersion to warn when an older agent connects (legacy
// schema = empty dashboards).
const agentVersion = "1.0.0"

func main() {
	backendURL := flag.String("backend", envOr("KUBEBOLT_BACKEND_URL", "localhost:9090"), "Backend gRPC address (host:port)")
	nodeName := flag.String("node", envOr("KUBEBOLT_AGENT_NODE_NAME", hostname()), "Node name (falls back to hostname)")
	nodeIP := flag.String("node-ip", envOr("KUBEBOLT_AGENT_NODE_IP", ""), "Node IP the kubelet listens on (downward API status.hostIP)")
	statsInterval := flag.Duration("stats-interval", 15*time.Second, "How often to poll kubelet /stats/summary")
	podsInterval := flag.Duration("pods-interval", 30*time.Second, "How often to refresh the pods metadata cache")
	// Default 50k: a multi-node Mode C poll can produce 5-7k samples
	// per matcher across 4-6 matchers — per-matcher pushes (see
	// promread/leader.go) keep individual pushes small, but 50k gives
	// the shipper headroom to drain between bursts even when the
	// backend is briefly slow. Bumped from 10k after the S1 multi-node
	// smoke surfaced silent drops of node_load*/node_cpu_*/node_memory_*
	// (alphabetic middle of matcher 3, dropped by Ring.Push overflow
	// when the original 10k cap was exceeded).
	bufferSize := flag.Int("buffer", 50_000, "Max samples buffered in memory before oldest are dropped")
	logLevel := flag.String("log-level", envOr("KUBEBOLT_AGENT_LOG_LEVEL", "info"), "Log level: debug|info|warn|error")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: parseLevel(*logLevel)})))

	// KUBEBOLT_AGENT_MODE chooses which collector pipelines run inside
	// this pod. 1.13 topology split: DaemonSet pods own Mode A
	// (kubelet/cAdvisor/Hubble + KubeBolt-named samples for the UI's
	// curated panels); a single Deployment(replicas=1) pod owns Mode C
	// (promread reading from the customer's central Prom, with
	// cluster-wide sample volume that would dwarf a DaemonSet pod's
	// shared buffer if both ran together — see S1 multi-node smoke
	// 2026-05-26).
	//
	//   "daemonset" → Mode A collectors only, NO promread
	//   "promread"  → promread only (+ self-metrics), NO kubelet collectors
	//   unset or "both" → everything (legacy / single-pod dev compat)
	//
	// The chart sets this explicitly on each template; existing
	// installs that haven't bumped the chart yet keep the legacy
	// "everything" behavior.
	agentMode := strings.ToLower(strings.TrimSpace(os.Getenv("KUBEBOLT_AGENT_MODE")))
	runModeA := agentMode != "promread"   // Mode A on for daemonset/both/legacy
	runModeC := agentMode != "daemonset"  // Mode C on for promread/both/legacy
	slog.Info("agent mode resolved",
		slog.String("mode", agentMode),
		slog.Bool("run_mode_a", runModeA),
		slog.Bool("run_mode_c", runModeC),
	)

	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigChan
		slog.Info("signal received, shutting down", slog.String("signal", sig.String()))
		rootCancel()
	}()

	kc := kubelet.New(*nodeIP)
	slog.Info("kubelet target", slog.String("url", kc.BaseURL()))

	clusterID, clusterName := resolveClusterIdent(rootCtx)
	slog.Info("cluster identity",
		slog.String("cluster_id", clusterID),
		slog.String("cluster_name", clusterName),
	)

	pods := collector.NewPods(kc)
	// KUBEBOLT_AGENT_DEFER_NODE_NETWORK=true tells the kubelet stats
	// collector to skip emitting node_network_*_bytes_total. The
	// helm chart auto-sets this when the vmagent sidecar is
	// configured to scrape node-exporter — node-exporter emits the
	// same metric name with identical labels from the same kernel
	// counters, so a single source-of-truth avoids the 3× overcount
	// surfaced during Phase 2 in-vivo validation. Other node_*
	// metrics (CPU/memory/filesystem) keep emitting because their
	// names diverge from node-exporter's.
	deferNodeNetwork := strings.EqualFold(os.Getenv("KUBEBOLT_AGENT_DEFER_NODE_NETWORK"), "true")
	// KUBEBOLT_AGENT_DEFER_NODE_STRESS=true disables the NodeStress
	// collector (load + PSI from /proc). Same dedup pattern as
	// deferNodeNetwork: when a node-exporter scrape (kube-prom-stack,
	// PodMonitoring CR) already provides node_load* and
	// node_pressure_* with overlapping labels, the agent steps aside.
	// Default false because the GMP-on-GKE case has no node-exporter
	// out of the box and the Node Monitor panels would stay empty.
	deferNodeStress := strings.EqualFold(os.Getenv("KUBEBOLT_AGENT_DEFER_NODE_STRESS"), "true")
	// tenantID (Phase 3 Day 4.2) — operator-provisioned via helm
	// value `tenant.id` and templated into KUBEBOLT_TENANT_ID. Empty
	// is fine: collectors skip the tenant_id label and the backend
	// receiver auto-stamps from the bearer token's tenant (Day 4.1
	// fallback). Day 4.3 will require this in enforced mode.
	tenantID := os.Getenv("KUBEBOLT_TENANT_ID")
	if tenantID != "" {
		slog.Info("tenant identity", slog.String("tenant_id", tenantID))
	}
	stats := collector.NewStats(kc, clusterID, clusterName, *nodeName, tenantID,
		collector.WithDeferNodeNetwork(deferNodeNetwork))
	cadvisor := collector.NewCadvisor(kc, clusterID, clusterName, *nodeName, tenantID)
	// NodeStress reads /proc/loadavg + /proc/pressure/* directly.
	// procPath defaults to "/proc" — system-wide files aren't PID-
	// namespaced so reading from inside the container returns host
	// values without any hostPath mount.
	nodeStress := collector.NewNodeStress(clusterID, clusterName, *nodeName, tenantID, "",
		collector.WithDeferNodeStress(deferNodeStress))
	buf := buffer.New(*bufferSize)
	// Pod cache + Hubble aggregator sizes are plumbed into self-metrics
	// so kubebolt_agent_pods_cache_size + kubebolt_agent_aggregator_keys
	// gauges attribute working-set growth to a specific subsystem during
	// memory investigations. Aggregator is leader-only — the closure
	// asks flows.ActiveAggregator() which returns nil on non-leader pods,
	// and self.Collector skips emission in that case.
	selfC := self.New(buf, clusterID, clusterName, *nodeName, agentVersion, tenantID,
		self.WithPodsCache(pods),
		self.WithAggregator(liveAggregatorSizer{}),
	)

	// promread (Mode C — 1.13 Phase 6 of the Universal Data Plane).
	// Reads from the customer's Prometheus via /api/v1/query_range
	// instead of scraping targets directly. Skipped silently when
	// KUBEBOLT_AGENT_PROMREAD_ENABLED is unset or false.
	//
	// Mutual exclusion with scrape.enabled is enforced at chart
	// render time (hard-fail in deploy/helm/kubebolt-agent — lands
	// with S1 chunk 3); here we just construct what env asks for.
	promCfg, err := promread.LoadConfigFromEnv()
	if err != nil {
		slog.Error("promread config invalid", slog.String("error", err.Error()))
		os.Exit(1)
	}
	promCfg.ClusterID = clusterID
	promCfg.ClusterName = clusterName
	promCfg.TenantID = tenantID

	var promReader *promread.Reader
	var promNodeIdx *promread.K8sNodeIndex
	var promKubeClient kubernetes.Interface
	if promCfg.Enabled {
		// K8sNodeIndex needs a kube client to list nodes — required
		// for the `node=<k8s-node-name>` label enrichment on node_*
		// series (UI parity with Mode A's cadvisor stamping). The
		// agent doesn't keep a long-lived kube.Interface elsewhere;
		// promread creates its own only when enabled so disabled
		// installs pay zero apiserver overhead.
		//
		// Same client also drives the Lease-based leader election so
		// only one agent pod polls the customer's Prom at a time
		// (otherwise N nodes → N× query cost on AMP/Azure/GMP).
		kubeCfg, err := rest.InClusterConfig()
		if err != nil {
			slog.Error("promread enabled but in-cluster config unavailable",
				slog.String("error", err.Error()))
			os.Exit(1)
		}
		kc, err := kubernetes.NewForConfig(kubeCfg)
		if err != nil {
			slog.Error("promread enabled but kube client init failed",
				slog.String("error", err.Error()))
			os.Exit(1)
		}
		promKubeClient = kc
		promNodeIdx = promread.NewK8sNodeIndex(promKubeClient, promread.DefaultNodeRefreshInterval)

		pr, err := promread.NewReader(promCfg, promread.WithNodeIndex(promNodeIdx))
		if err != nil {
			slog.Error("promread init failed", slog.String("error", err.Error()))
			os.Exit(1)
		}
		promReader = pr
		slog.Info("promread enabled",
			slog.String("url", promCfg.URL),
			slog.String("auth_mode", string(promCfg.Auth.Mode)),
			slog.Duration("poll_interval", promReader.PollInterval()),
			slog.Int("matcher_count", len(promCfg.Matchers)),
		)
	}

	// pprof endpoint — opt-in via env. When set, exposes /debug/pprof
	// on the chosen address (default empty = OFF). Use 127.0.0.1:6060
	// so it's only reachable through `kubectl port-forward`; setting
	// 0.0.0.0:6060 would expose the heap profile cluster-wide which is
	// useful only on a dev kind cluster.
	if pprofAddr := os.Getenv("KUBEBOLT_AGENT_PPROF_ADDR"); pprofAddr != "" {
		go func() {
			slog.Info("agent pprof endpoint listening",
				slog.String("addr", pprofAddr),
				slog.String("hint", "kubectl port-forward <pod> 6060 && go tool pprof http://localhost:6060/debug/pprof/heap"),
			)
			// Dedicated server (vs http.ListenAndServe with nil handler)
			// so we can give it its own timeouts — slow heap profiles
			// shouldn't be killed by an aggressive default.
			srv := &http.Server{
				Addr:              pprofAddr,
				ReadHeaderTimeout: 5 * time.Second,
			}
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				slog.Error("agent pprof endpoint failed", slog.String("error", err.Error()))
			}
		}()
	}

	// Auth + TLS config from helm-injected env vars. Half-set
	// combinations fail loud here so misconfigurations don't silently
	// keep an agent running unauthenticated.
	authOpts := shipper.LoadAuthFromEnv()
	if err := authOpts.Validate(); err != nil {
		slog.Error("agent auth configuration invalid", slog.String("error", err.Error()))
		os.Exit(1)
	}
	switch {
	case !authOpts.HasAuth():
		slog.Warn("agent dialing backend WITHOUT credentials (Sprint 0 migration window)")
	case authOpts.HasAuth() && !authOpts.TLSEnabled:
		slog.Warn("agent has auth credentials but TLS is disabled — bearer token will travel in plaintext",
			slog.String("auth_mode", string(authOpts.Mode)),
		)
	}
	shipperOpts := []shipper.Option{
		shipper.WithAuth(authOpts),
		shipper.WithClusterIdent(clusterID, clusterName),
		shipper.WithAgentMode(agentMode),
	}

	// Sprint A.5: optional K8s API proxy. When enabled, the agent
	// advertises the "kube-proxy" capability in Hello and dispatches
	// kube_request payloads from the backend against the local
	// in-cluster apiserver. Default off — operators opt-in via
	// KUBEBOLT_AGENT_PROXY_ENABLED=true (Helm value lands in
	// commit 9 as proxy.enabled).
	if envBool("KUBEBOLT_AGENT_PROXY_ENABLED", false) {
		cfg, err := rest.InClusterConfig()
		if err != nil {
			slog.Error("agent proxy requested but in-cluster config unavailable",
				slog.String("error", err.Error()))
			os.Exit(1)
		}
		kp, err := proxy.New(cfg)
		if err != nil {
			slog.Error("agent proxy: build failed", slog.String("error", err.Error()))
			os.Exit(1)
		}
		shipperOpts = append(shipperOpts,
			shipper.WithHandler(proxy.NewHandler(kp)),
			shipper.WithCapabilities("metrics", "kube-proxy"),
		)
		slog.Info("agent proxy enabled — backend can issue kube_request via this agent")
	}

	ship := shipper.New(*backendURL, *nodeName, agentVersion, buf, shipperOpts...)

	var wg sync.WaitGroup

	// Mode A goroutines — pods refresh, kubelet /stats/summary, kubelet
	// /metrics/cadvisor. Gated on runModeA so a promread-only Deployment
	// pod (1.13 topology split) doesn't run these — its kubelet is local
	// but the cluster-wide samples come from the customer's Prom via
	// promread anyway, so per-pod kubelet scraping would just bloat the
	// buffer for no UI gain.
	if runModeA {
		// Pods metadata refresher.
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Initial refresh so the first stats batch has enrichment data.
			if err := pods.Refresh(rootCtx); err != nil {
				slog.Warn("initial pods refresh failed", slog.String("error", err.Error()))
			} else {
				slog.Info("pods cache primed", slog.Int("pods", pods.Size()))
			}
			tick := time.NewTicker(*podsInterval)
			defer tick.Stop()
			for {
				select {
				case <-rootCtx.Done():
					return
				case <-tick.C:
					if err := pods.Refresh(rootCtx); err != nil {
						slog.Warn("pods refresh failed", slog.String("error", err.Error()))
					}
				}
			}
		}()

		// Stats collector.
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Send a first batch immediately so VM has data within seconds.
			collectAndBuffer(rootCtx, stats, pods, buf)
			tick := time.NewTicker(*statsInterval)
			defer tick.Stop()
			for {
				select {
				case <-rootCtx.Done():
					return
				case <-tick.C:
					collectAndBuffer(rootCtx, stats, pods, buf)
				}
			}
		}()

		// cAdvisor network collector — runs on the same cadence as stats.
		// Complements /stats/summary for kubelets that don't populate the
		// pod-level network block (e.g. docker-desktop).
		wg.Add(1)
		go func() {
			defer wg.Done()
			collectAndBuffer(rootCtx, cadvisor, pods, buf)
			tick := time.NewTicker(*statsInterval)
			defer tick.Stop()
			for {
				select {
				case <-rootCtx.Done():
					return
				case <-tick.C:
					collectAndBuffer(rootCtx, cadvisor, pods, buf)
				}
			}
		}()

		// NodeStress collector — emits node_load{1,5,15} +
		// node_pressure_{cpu,memory,io}_waiting_seconds_total from
		// the host's /proc on the same cadence as kubelet stats.
		// No pod enrichment needed (node-scoped metrics only), so
		// collect directly into the buffer instead of going through
		// the pods-cache path that collectAndBuffer takes.
		wg.Add(1)
		go func() {
			defer wg.Done()
			collectNodeStress(rootCtx, nodeStress, buf)
			tick := time.NewTicker(*statsInterval)
			defer tick.Stop()
			for {
				select {
				case <-rootCtx.Done():
					return
				case <-tick.C:
					collectNodeStress(rootCtx, nodeStress, buf)
				}
			}
		}()
	}

	// Agent self-metrics collector — emits kubebolt_agent_* every stats
	// tick so VM has fresh self-observability for KubeBolt's own
	// dashboards (buffer occupancy, drop rate, memory). Cardinality is
	// fixed (7 series per agent), so cost is negligible. The samples go
	// through pods.Enrich for label uniformity but the enrichment is a
	// no-op (no pod_uid label present).
	wg.Add(1)
	go func() {
		defer wg.Done()
		collectAndBuffer(rootCtx, selfC, pods, buf)
		tick := time.NewTicker(*statsInterval)
		defer tick.Stop()
		for {
			select {
			case <-rootCtx.Done():
				return
			case <-tick.C:
				collectAndBuffer(rootCtx, selfC, pods, buf)
			}
		}
	}()

	// K8sNodeIndex refresh loop — paired with promReader. Runs on its
	// own ticker (5min default) listing nodes to refresh the IP→name
	// map. Skipped when promRead is disabled (no nodeIdx instance).
	if runModeC && promNodeIdx != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			promNodeIdx.Run(rootCtx)
		}()
	}

	// Customer Prom reader (Mode C — 1.13) behind a Kubernetes Lease so
	// only ONE agent pod cluster-wide polls the customer's Prom at a
	// time. Without the election, every DaemonSet pod would query the
	// same central Prom independently — N nodes = N× query cost on
	// AMP/Azure/GMP, N× shipper bandwidth, and possible drift in the
	// timestamps that confuses rate()/increase() in the UI. Same
	// Lease-based pattern as the Hubble flows collector.
	//
	// Non-leader pods still emit a kubebolt_promread_leader=0 heartbeat
	// every 30s so dashboards see who's standing by; the leader's
	// gauge flips to 1 on lease acquisition.
	if runModeC && promReader != nil {
		leaseNs, err := promread.ResolveLeaseNamespace()
		if err != nil {
			slog.Error("promread: cannot resolve lease namespace, refusing to start",
				slog.String("error", err.Error()))
			os.Exit(1)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			promread.RunLeaderElectedReader(
				rootCtx, promReader, buf, pods, promKubeClient,
				clusterID, clusterName, *nodeName, leaseNs, tenantID,
			)
		}()
	}

	// Shipper — reconnects internally on failure.
	wg.Add(1)
	go func() {
		defer wg.Done()
		ship.Run(rootCtx)
	}()

	// Hubble flow collector (Phase 2.1 Level 2). Elects a single-pod
	// leader via a Lease in the agent's own namespace and only that pod
	// streams from Hubble Relay; other pods stand by. Silent no-op when
	// we're not in-cluster (dev runs on host) or when the cluster
	// doesn't have Cilium installed.
	//
	// Operator kill-switch: KUBEBOLT_HUBBLE_ENABLED=false turns this
	// feature off entirely without code changes. Useful when running on
	// a Cilium cluster where you don't want the extra flow telemetry,
	// or to cut dependency on the relay while debugging.
	if !runModeA {
		slog.Info("hubble: flow collector skipped (KUBEBOLT_AGENT_MODE=promread)")
	} else if !envBool("KUBEBOLT_HUBBLE_ENABLED", true) {
		slog.Info("hubble: flow collector disabled via KUBEBOLT_HUBBLE_ENABLED=false")
	} else if leaseNs, err := flows.ResolveLeaseNamespace(); err == nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			flows.RunLeaderElectedCollector(rootCtx, buf, clusterID, clusterName, *nodeName, leaseNs, tenantID)
		}()
	} else {
		slog.Debug("hubble: skipping flow collector (no lease namespace)",
			slog.String("reason", err.Error()))
	}

	// Periodic buffer stats log (every minute) — lets you see drops if they happen.
	wg.Add(1)
	go func() {
		defer wg.Done()
		tick := time.NewTicker(time.Minute)
		defer tick.Stop()
		for {
			select {
			case <-rootCtx.Done():
				return
			case <-tick.C:
				collected, dropped, current, capacity := buf.Stats()
				slog.Info("buffer stats",
					slog.Uint64("collected_total", collected),
					slog.Uint64("dropped_total", dropped),
					slog.Int("current", current),
					slog.Int("capacity", capacity),
					slog.Int("pods_cached", pods.Size()),
				)
			}
		}
	}()

	<-rootCtx.Done()
	slog.Info("waiting for goroutines to drain")
	wg.Wait()
	slog.Info("agent stopped")
}

// Collector is the minimal interface satisfied by stats and cadvisor
// collectors. Lets collectAndBuffer work with any source of samples.
type Collector interface {
	Name() string
	Collect(ctx context.Context) ([]*agentv2.Sample, error)
}

func collectAndBuffer(ctx context.Context, c Collector, pods *collector.PodsCache, buf *buffer.Ring) {
	samples, err := c.Collect(ctx)
	if err != nil {
		slog.Warn("collect failed", slog.String("collector", c.Name()), slog.String("error", err.Error()))
		return
	}
	pods.Enrich(samples)
	buf.Push(samples)
	// Info-level so the Phase B / Phase C bring-up is observable without
	// flipping log level. Gets noisy on steady state; revisit when we have
	// more than two collectors.
	slog.Info("samples collected", slog.String("collector", c.Name()), slog.Int("count", len(samples)))
}

// collectNodeStress is the node-scoped variant of collectAndBuffer —
// NodeStress emits only node-level metrics (no pod/container labels),
// so the pods.Enrich step is unnecessary. Skipping it avoids a needless
// map lookup per sample and keeps the hot path predictable for the
// node monitor panel.
func collectNodeStress(ctx context.Context, c Collector, buf *buffer.Ring) {
	samples, err := c.Collect(ctx)
	if err != nil {
		slog.Warn("collect failed", slog.String("collector", c.Name()), slog.String("error", err.Error()))
		return
	}
	if len(samples) == 0 {
		// Disabled via WithDeferNodeStress (operator opted out because
		// node-exporter is already shipping). Stay silent — info-line
		// would just be noise every statsInterval.
		return
	}
	buf.Push(samples)
	slog.Info("samples collected", slog.String("collector", c.Name()), slog.Int("count", len(samples)))
}

func parseLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// envBool reads a truthy/falsy env var. Empty/unset returns fallback.
// Accepts the same tokens as strconv.ParseBool so operators can use
// 1/0, true/false, yes/no (case-insensitive) — whichever feels most
// natural in their deployment tooling. Unrecognized values fall back
// instead of silently defaulting to false, so a typo doesn't turn off
// a feature the operator expected on.
func envBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	switch strings.ToLower(v) {
	case "1", "t", "true", "y", "yes", "on":
		return true
	case "0", "f", "false", "n", "no", "off":
		return false
	default:
		slog.Warn("ignoring unrecognized boolean env var",
			slog.String("key", key),
			slog.String("value", v),
			slog.Bool("using_default", fallback))
		return fallback
	}
}

func hostname() string {
	if n, err := os.Hostname(); err == nil {
		return n
	}
	if out, err := exec.Command("uname", "-n").Output(); err == nil {
		return string(out)
	}
	return "unknown-node"
}

// resolveClusterIdent determines the (cluster_id, cluster_name) pair
// that every sample this agent emits gets tagged with. The ID is the
// cornerstone of multi-cluster correctness — two agents running in
// different clusters must have different IDs, otherwise VM sums their
// samples together and dashboards lie.
//
// Priority for cluster_id:
//  1. KUBEBOLT_AGENT_CLUSTER_ID env var (operator override, e.g. to
//     migrate legacy installs that used "local" before this feature
//     existed).
//  2. Auto-discover: read the `kube-system` namespace UID from the
//     apiserver. Every K8s cluster has a unique, immutable UID there,
//     so no two clusters can ever collide.
//  3. Fallback to "local" when we can't reach the apiserver (e.g.
//     dev-mode host run without in-cluster credentials). Emits a
//     warn-level log so the operator notices.
//
// cluster_name is a pure display label, set via
// KUBEBOLT_AGENT_CLUSTER_NAME, empty when not configured. The UI uses
// whatever the backend knows from kubeconfig context instead, so this
// is mostly for operators who query VM directly.
func resolveClusterIdent(ctx context.Context) (clusterID, clusterName string) {
	clusterName = os.Getenv("KUBEBOLT_AGENT_CLUSTER_NAME")

	if override := os.Getenv("KUBEBOLT_AGENT_CLUSTER_ID"); override != "" {
		return override, clusterName
	}

	cfg, err := rest.InClusterConfig()
	if err != nil {
		slog.Warn("cluster_id: no in-cluster config, falling back to 'local'",
			slog.String("error", err.Error()))
		return "local", clusterName
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		slog.Warn("cluster_id: kube client init failed, falling back to 'local'",
			slog.String("error", err.Error()))
		return "local", clusterName
	}
	// Retry the UID read for up to ~30s before the "local" fallback. A freshly
	// booting control plane (kind on a cold start, a managed cluster mid-
	// provision, or an agent that races the apiserver) may not answer on the
	// first try — and a single 5s timeout there would permanently mislabel the
	// cluster as "local", which then mismatches insight routing / ownership.
	// A ready apiserver answers the first attempt, so this adds no startup delay
	// in the normal case.
	deadline := time.Now().Add(30 * time.Second)
	var lastErr error
	for {
		getCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		ns, err := client.CoreV1().Namespaces().Get(getCtx, "kube-system", metav1.GetOptions{})
		cancel()
		if err == nil {
			return string(ns.UID), clusterName
		}
		lastErr = err
		if time.Now().After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return "local", clusterName
		case <-time.After(3 * time.Second):
		}
	}
	slog.Warn("cluster_id: failed to read kube-system UID after retrying ~30s, falling back to 'local'",
		slog.String("error", lastErr.Error()))
	return "local", clusterName
}

// liveAggregatorSizer adapts the package-level flows.ActiveAggregator()
// to self.AggregatorSizer. Looking up the aggregator at Sizes() time
// (instead of capturing a pointer at agent startup) handles two cases:
//
//   1. Non-leader pods never run RunCollector, so the aggregator is
//      always nil here — emission is correctly skipped.
//   2. Leadership transitions: when the agent re-acquires the lease
//      after a flap, RunCollector publishes a fresh aggregator. The
//      next Sizes() call picks it up automatically; we don't get
//      stuck reading a stale pointer.
//
// Returns nil (not an empty map) when no aggregator is active — the
// self.Collector for-ranges over a nil map cleanly, emitting nothing.
type liveAggregatorSizer struct{}

func (liveAggregatorSizer) Sizes() map[string]int {
	if agg := flows.ActiveAggregator(); agg != nil {
		return agg.Sizes()
	}
	return nil
}
