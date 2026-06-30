package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// metricsFreshnessTTL bounds how stale a cached freshness verdict may be before
// a background refresh is kicked. ListClusters reads the cache on the request
// path and never blocks on VM; the refresh runs in a goroutine.
const metricsFreshnessTTL = 20 * time.Second

// metricsFreshnessQueryTimeout caps a single VM probe so a slow/down VM can't
// pile up goroutines.
const metricsFreshnessQueryTimeout = 5 * time.Second

// metricsFreshEntry is one cluster's last freshness verdict + when it was taken.
type metricsFreshEntry struct {
	fresh bool
	at    time.Time
}

// metricsFreshnessCache answers "has this metrics-only cluster shipped any
// sample into VictoriaMetrics recently?" WITHOUT consulting the AgentChannel
// registry.
//
// Why this exists: a metrics-only agent ships over vmagent's HTTP remote_write,
// which is independent of the gRPC AgentChannel. So the channel can be down (or
// flapping between backend pods after a restart) while metrics still flow into
// VM. Basing "connected" on the live-channel registry (CountByCluster) is
// therefore fragile — it shows OFFLINE the moment the channel blips even though
// the backend is happily ingesting. If the backend is accepting samples for the
// cluster, it IS connected, by definition — that's the signal this cache gives.
//
// Stale-while-revalidate: fresh() reads the cache and, when the entry is older
// than the TTL, kicks an async refresh and returns the last verdict. The request
// path (ListClusters) never makes a VM round-trip. A DEDICATED mutex (never the
// manager's hot m.mu) keeps a slow VM probe from contending the connect/list
// lock — the same lock whose writer-starvation caused the agent-proxy hang.
type metricsFreshnessCache struct {
	mu       sync.Mutex
	entries  map[string]metricsFreshEntry
	inFlight map[string]bool // de-dupe concurrent refreshes per cluster
	client   *http.Client
}

func newMetricsFreshnessCache() *metricsFreshnessCache {
	return &metricsFreshnessCache{
		entries:  make(map[string]metricsFreshEntry),
		inFlight: make(map[string]bool),
		client:   &http.Client{Timeout: metricsFreshnessQueryTimeout},
	}
}

// fresh reports the cached verdict for clusterID and, when stale, schedules an
// async refresh. Returns false until the first probe completes (a brand-new
// metrics-only cluster reads OFFLINE for one TTL, then flips once VM confirms
// ingest — acceptable, and self-heals on the UI's 30s refetch). Nil-safe so
// direct-struct test managers don't panic.
func (c *metricsFreshnessCache) fresh(clusterID string) bool {
	if c == nil || clusterID == "" {
		return false
	}
	c.mu.Lock()
	e, ok := c.entries[clusterID]
	stale := !ok || time.Since(e.at) > metricsFreshnessTTL
	if stale && !c.inFlight[clusterID] {
		c.inFlight[clusterID] = true
		go c.refresh(clusterID)
	}
	c.mu.Unlock()
	return ok && e.fresh
}

// refresh runs one VM instant probe for clusterID and stores the verdict.
func (c *metricsFreshnessCache) refresh(clusterID string) {
	defer func() {
		c.mu.Lock()
		delete(c.inFlight, clusterID)
		c.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), metricsFreshnessQueryTimeout)
	defer cancel()
	fresh, err := c.probe(ctx, clusterID)
	if err != nil {
		slog.Debug("metrics-freshness probe failed",
			slog.String("cluster_id", clusterID), slog.String("error", err.Error()))
		// Keep the previous verdict but bump the timestamp so we don't hot-loop
		// retries against a persistently-down VM; the next read after TTL retries.
		c.mu.Lock()
		prev := c.entries[clusterID]
		c.entries[clusterID] = metricsFreshEntry{fresh: prev.fresh, at: time.Now()}
		c.mu.Unlock()
		return
	}
	c.mu.Lock()
	c.entries[clusterID] = metricsFreshEntry{fresh: fresh, at: time.Now()}
	c.mu.Unlock()
}

// probe issues the VM instant query and reports whether the cluster has any
// recently-active series.
//
// count({cluster_id="X"}) returns >0 only when at least one series for the
// cluster carries a sample inside VM's staleness window (~5m) — i.e. the cluster
// is actively shipping. cluster_id is the globally-unique kube cluster UID, so
// this can't match another org's data; no tenant scope is needed for correctness
// (and adding one would false-negative if the agent self-stamped a different
// tenant_id than the viewer's org).
func (c *metricsFreshnessCache) probe(ctx context.Context, clusterID string) (bool, error) {
	q := fmt.Sprintf(`count({cluster_id=%q})`, clusterID)
	endpoint := metricsStorageURLForFreshness() + "/api/v1/query?" + url.Values{"query": {q}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("VM query status %d", resp.StatusCode)
	}
	var parsed struct {
		Data struct {
			Result []struct {
				Value [2]json.RawMessage `json:"value"` // [unix_ts, "<count>"]
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return false, err
	}
	if len(parsed.Data.Result) == 0 {
		return false, nil // metric absent → no series → not shipping
	}
	var valStr string
	if err := json.Unmarshal(parsed.Data.Result[0].Value[1], &valStr); err != nil {
		return false, nil
	}
	n, err := strconv.ParseFloat(valStr, 64)
	if err != nil {
		return false, nil
	}
	return n > 0, nil
}

// metricsFresh reports whether a metrics-only cluster is actively shipping
// samples into VM (independent of its AgentChannel session). Off the request
// path: a cache read that schedules an async VM probe when stale.
func (m *Manager) metricsFresh(clusterID string) bool {
	return m.metricsFreshness.fresh(clusterID)
}

// metricsStorageURLForFreshness mirrors api.metricsStorageURL() — the cluster
// package can't import api (cycle), so the env contract is replicated here.
func metricsStorageURLForFreshness() string {
	if u := os.Getenv("KUBEBOLT_METRICS_STORAGE_URL"); u != "" {
		return strings.TrimRight(u, "/")
	}
	return "http://localhost:8428"
}
