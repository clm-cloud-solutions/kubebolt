package cluster

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// vmStub stands in for VictoriaMetrics: it returns a count() vector whose
// scalar is `count`, and records the last query it was asked. count<0 makes
// it 500 so error paths can be exercised.
func vmStub(t *testing.T, count int64, lastQuery *atomic.Value, hits *atomic.Int64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		lastQuery.Store(r.URL.Query().Get("query"))
		if count < 0 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if count == 0 {
			// VM returns an empty result vector when no series match.
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
			return
		}
		fmt.Fprintf(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1782853845,"%d"]}]}}`, count)
	}))
}

// waitFor polls until cond() or the deadline — the refresh runs async, so a
// fresh() read won't reflect the new verdict on the same call.
func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within deadline")
}

func TestMetricsFreshness_FreshWhenSeriesPresent(t *testing.T) {
	var lastQuery atomic.Value
	var hits atomic.Int64
	srv := vmStub(t, 12, &lastQuery, &hits)
	defer srv.Close()
	t.Setenv("KUBEBOLT_METRICS_STORAGE_URL", srv.URL)

	c := newMetricsFreshnessCache()

	// First read is a cache miss → false now, kicks the async probe.
	if c.fresh("", "cid-1") {
		t.Fatal("expected false on first (cold) read")
	}
	// After the probe completes, the cluster reads fresh.
	waitFor(t, time.Second, func() bool { return c.fresh("", "cid-1") })

	// The probe scoped by cluster_id (and only cluster_id — no tenant scope).
	q, _ := lastQuery.Load().(string)
	if !strings.Contains(q, `cluster_id="cid-1"`) {
		t.Fatalf("query not scoped by cluster_id: %q", q)
	}
	if strings.Contains(q, "tenant_id") {
		t.Fatalf("query should not carry tenant_id (cluster_id is unique): %q", q)
	}
	if !strings.HasPrefix(q, "count(") {
		t.Fatalf("expected a count() probe, got %q", q)
	}
}

func TestMetricsFreshness_NotFreshWhenNoSeries(t *testing.T) {
	var lastQuery atomic.Value
	var hits atomic.Int64
	srv := vmStub(t, 0, &lastQuery, &hits) // empty result vector
	defer srv.Close()
	t.Setenv("KUBEBOLT_METRICS_STORAGE_URL", srv.URL)

	c := newMetricsFreshnessCache()
	c.fresh("", "cid-empty") // kick probe
	// Give the probe time to run, then confirm it settled on not-fresh.
	waitFor(t, time.Second, func() bool { return hits.Load() > 0 })
	time.Sleep(20 * time.Millisecond)
	if c.fresh("", "cid-empty") {
		t.Fatal("expected not-fresh when VM returns zero series")
	}
}

func TestMetricsFreshness_CachesAndDeduplicates(t *testing.T) {
	var lastQuery atomic.Value
	var hits atomic.Int64
	srv := vmStub(t, 5, &lastQuery, &hits)
	defer srv.Close()
	t.Setenv("KUBEBOLT_METRICS_STORAGE_URL", srv.URL)

	c := newMetricsFreshnessCache()
	c.fresh("", "cid-2")
	waitFor(t, time.Second, func() bool { return c.fresh("", "cid-2") })
	hitsAfterFirst := hits.Load()
	if hitsAfterFirst < 1 {
		t.Fatalf("expected at least one VM hit, got %d", hitsAfterFirst)
	}

	// Within the TTL, repeated reads serve from cache — no new VM hits.
	for i := 0; i < 20; i++ {
		if !c.fresh("", "cid-2") {
			t.Fatal("expected cached fresh=true within TTL")
		}
	}
	if got := hits.Load(); got != hitsAfterFirst {
		t.Fatalf("reads within TTL hit VM again: before=%d after=%d", hitsAfterFirst, got)
	}
}

func TestMetricsFreshness_NilAndEmptySafe(t *testing.T) {
	// A direct-struct test Manager has a nil cache — must not panic.
	var nilCache *metricsFreshnessCache
	if nilCache.fresh("", "x") {
		t.Fatal("nil cache should read not-fresh")
	}
	c := newMetricsFreshnessCache()
	if c.fresh("", "") {
		t.Fatal("empty clusterID should read not-fresh")
	}
}

func TestMetricsFreshness_ErrorKeepsPreviousVerdict(t *testing.T) {
	// Probe succeeds (fresh) once, then VM starts erroring: the cached
	// verdict must persist rather than flap to false on a transient error.
	var lastQuery atomic.Value
	var hits atomic.Int64
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		lastQuery.Store(r.URL.Query().Get("query"))
		if fail.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1782853845,"7"]}]}}`))
	}))
	defer srv.Close()
	t.Setenv("KUBEBOLT_METRICS_STORAGE_URL", srv.URL)

	c := newMetricsFreshnessCache()
	c.fresh("", "cid-3")
	waitFor(t, time.Second, func() bool { return c.fresh("", "cid-3") })

	// Force the cached entry stale so the next read re-probes, and make VM fail.
	fail.Store(true)
	c.mu.Lock()
	c.entries[freshKey("", "cid-3")] = metricsFreshEntry{fresh: true, at: time.Now().Add(-2 * metricsFreshnessTTL)}
	c.mu.Unlock()

	hitsBefore := hits.Load()
	c.fresh("", "cid-3") // stale → kicks a probe that will error
	waitFor(t, time.Second, func() bool { return hits.Load() > hitsBefore })
	time.Sleep(20 * time.Millisecond)
	if !c.fresh("", "cid-3") {
		t.Fatal("expected previous fresh verdict to survive a transient VM error")
	}
}

// sanity: the probe endpoint is well-formed (URL-encoded query), with both
// scope labels surviving the encoding.
func TestMetricsFreshness_QueryEncoding(t *testing.T) {
	q := fmt.Sprintf(`count({tenant_id=%q,cluster_id=%q})`, "org-1", "abc-123")
	enc := url.Values{"query": {q}}.Encode()
	for _, want := range []string{"cluster_id", "tenant_id"} {
		if !strings.Contains(enc, want) {
			t.Fatalf("encoded query lost %s: %s", want, enc)
		}
	}
}

// The probe must ask VM for THIS org's series, and the cache must remember the
// answer per org — both halves are the boundary.
//
// cluster_id is the kube cluster UID, so connecting the same physical cluster to
// a second org (install the agent in another namespace) yields one cluster_id
// under two tenant_ids. Unscoped, an org receiving nothing was told "connected"
// because the OTHER org's agent was shipping: a green cluster, no data, and no
// reason to go looking.
func TestMetricsFreshness_ProbeIsScopedToTheOrg(t *testing.T) {
	var lastQuery atomic.Value
	var hits atomic.Int64
	srv := vmStub(t, 1, &lastQuery, &hits)
	defer srv.Close()
	t.Setenv("KUBEBOLT_METRICS_STORAGE_URL", srv.URL)

	c := newMetricsFreshnessCache()
	c.fresh("org-a", "cid-shared")
	waitFor(t, time.Second, func() bool { return c.fresh("org-a", "cid-shared") })

	q, _ := lastQuery.Load().(string)
	if !strings.Contains(q, `tenant_id="org-a"`) {
		t.Errorf("probe reads across organisations — no tenant in query: %s", q)
	}
	if !strings.Contains(q, `cluster_id="cid-shared"`) {
		t.Errorf("probe lost the cluster pin: %s", q)
	}
}

// A verdict computed for one org must not be served to another from cache. The
// key is part of the isolation: keyed on cluster alone, the filter above would
// be defeated by memory.
func TestMetricsFreshness_CacheIsNotSharedBetweenOrgs(t *testing.T) {
	var lastQuery atomic.Value
	var hits atomic.Int64
	srv := vmStub(t, 1, &lastQuery, &hits)
	defer srv.Close()
	t.Setenv("KUBEBOLT_METRICS_STORAGE_URL", srv.URL)

	c := newMetricsFreshnessCache()
	c.fresh("org-a", "cid-shared")
	waitFor(t, time.Second, func() bool { return c.fresh("org-a", "cid-shared") })
	before := hits.Load()

	// Same cluster, different org: must NOT be answered from org-a's entry.
	if c.fresh("org-b", "cid-shared") {
		t.Fatal("org-b was served org-a's cached verdict for the same cluster")
	}
	waitFor(t, time.Second, func() bool { return hits.Load() > before })

	q, _ := lastQuery.Load().(string)
	if !strings.Contains(q, `tenant_id="org-b"`) {
		t.Errorf("org-b's refresh did not query its own tenant: %s", q)
	}
}

// OSS stamps no tenant_id, so scoping there would match nothing and read every
// cluster OFFLINE. The empty org means "do not filter", never "filter on empty".
func TestMetricsFreshness_UnscopedWhenNoTenantLabel(t *testing.T) {
	var lastQuery atomic.Value
	var hits atomic.Int64
	srv := vmStub(t, 1, &lastQuery, &hits)
	defer srv.Close()
	t.Setenv("KUBEBOLT_METRICS_STORAGE_URL", srv.URL)

	c := newMetricsFreshnessCache()
	c.fresh("", "cid-oss")
	waitFor(t, time.Second, func() bool { return c.fresh("", "cid-oss") })

	q, _ := lastQuery.Load().(string)
	if strings.Contains(q, "tenant_id") {
		t.Errorf("single-tenant probe filters on a label its series do not carry: %s", q)
	}
}
