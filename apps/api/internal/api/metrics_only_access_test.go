package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/agent/channel"
	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
)

// Finding #72: a metrics-only cluster that was NEVER agent-proxy must be
// reachable. Its agent:<uid> context lives in metricsOnlyContexts, not
// agentProxyContexts, so ClusterIDForContext returns "". requireClusterAccess
// must NOT read that as "no agent connected yet" and 503 — the cluster is a
// valid, connector-less one whose Overview the handler serves from VM.
//
// The original bug slipped past the tests because getClusterOverview's
// metrics-only branch was unit-tested in isolation while the middleware in front
// of it was never exercised with a metrics-only context. So this test goes
// THROUGH requireClusterAccess, the seam the bug lived in.
func TestRequireClusterAccess_MetricsOnlyClusterIsReachable(t *testing.T) {
	m := newIdleManager(t)
	m.SetAgentRegistry(channel.NewAgentRegistry())
	if _, err := m.AddMetricsOnlyCluster("uid-metrics", "metrics-cluster"); err != nil {
		t.Fatal(err)
	}
	h := &handlers{manager: m}

	passed := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		passed = true
		w.WriteHeader(http.StatusOK)
	})

	serve := func(ctxName string) *httptest.ResponseRecorder {
		passed = false
		req := httptest.NewRequest(http.MethodGet, "/api/v1/cluster/overview", nil)
		req.Header.Set(ClusterHeader, ctxName)
		rec := httptest.NewRecorder()
		h.requireClusterAccess(next).ServeHTTP(rec, req)
		return rec
	}

	// The metrics-only cluster must pass the middleware (no 503).
	rec := serve(cluster.AgentProxyContextName("uid-metrics")) // "agent:uid-metrics"
	if rec.Code == http.StatusServiceUnavailable {
		t.Fatalf("requireClusterAccess 503'd a metrics-only cluster (finding #72): %s", rec.Body.String())
	}
	if !passed {
		t.Fatalf("requireClusterAccess did not pass a metrics-only cluster through: %d %s", rec.Code, rec.Body.String())
	}

	// A genuinely unknown agent-proxy context still 503s — the guard's real job.
	if rec := serve("agent:uid-unknown"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("an unknown agent-proxy context must still 503 'waiting for agent', got %d", rec.Code)
	}
}
