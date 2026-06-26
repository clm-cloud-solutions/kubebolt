package api

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
)

type fakePoolStats struct{ s cluster.PoolStats }

func (f fakePoolStats) PoolStats() cluster.PoolStats { return f.s }

func TestPoolMetricsCollector(t *testing.T) {
	reg := prometheus.NewRegistry()
	NewPoolMetricsCollector(reg, fakePoolStats{cluster.PoolStats{Active: 1, Parked: 3, Building: 1}})

	want := `
# HELP kubebolt_api_runtimes Cluster runtimes resident in the connector pool, by state (always-on, W2 §10).
# TYPE kubebolt_api_runtimes gauge
kubebolt_api_runtimes{state="active"} 1
kubebolt_api_runtimes{state="building"} 1
kubebolt_api_runtimes{state="parked"} 3
`
	if err := testutil.CollectAndCompare(reg, strings.NewReader(want), "kubebolt_api_runtimes"); err != nil {
		t.Fatal(err)
	}
}
