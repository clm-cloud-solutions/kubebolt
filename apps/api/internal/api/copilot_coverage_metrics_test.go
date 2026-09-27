package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The tool pipeline is GET /metrics/query's: a tenant_id / cluster_id the
// caller wrote is stripped, the server's are injected. Without the strip a
// selector already carrying tenant_id is left alone by the injector — a
// caller-chosen org.
func TestMetricsQuerySource_ServerScopeWins(t *testing.T) {
	var got string
	vm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query().Get("query")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"pod":"a","tenant_id":"x","cluster_id":"y"},"value":[1700000000,"2"]}]}}`))
	}))
	defer vm.Close()
	t.Setenv("KUBEBOLT_METRICS_STORAGE_URL", vm.URL)

	src := metricsQuerySource{h: &handlers{}}
	now := time.Now()
	series, err := src.Query(context.Background(), `up{tenant_id="victim-org",cluster_id="victim-cluster",job="x"}`, now, now, 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "victim-org") || strings.Contains(got, "victim-cluster") {
		t.Errorf("the caller's scope reached the store: %s", got)
	}
	if !strings.Contains(got, `job="x"`) {
		t.Errorf("the caller's own matcher was lost: %s", got)
	}
	if !strings.Contains(got, noClusterUIDSentinel) {
		t.Errorf("no cluster resolved, so the query must be pinned to the sentinel: %s", got)
	}
	if len(series) != 1 || series[0].Labels["tenant_id"] != "" || series[0].Labels["cluster_id"] != "" || series[0].Labels["pod"] != "a" {
		t.Errorf("scope labels must not come back, signal labels must: %+v", series)
	}
}
