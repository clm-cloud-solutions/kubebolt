package seriesgate

import (
	"bufio"
	"os"
	"strings"
	"testing"

	"github.com/kubebolt/kubebolt/packages/shared/metriccatalog"
)

// Everything production stored on 2026-11-21 classifies as core: the names the
// clusters sent (the shared catalog's testdata) and the platform's own series
// stored with a tenant, which only the server writes.
func TestEverySeriesStoredInProductionIsCore(t *testing.T) {
	f, err := os.Open("../../../../packages/shared/metriccatalog/testdata/observed-2026-11-21.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	names := []string{
		"kubebolt_agent_grpc_dropped_series_total",
		"kubebolt_agent_grpc_samples_received_total",
		"kubebolt_agent_grpc_streams_total",
		"kubebolt_cluster_team_info",
		"kubebolt_kobi_copilot_cost_usd_total",
		"kubebolt_kobi_autopilot_incidents_total",
		"kubebolt_prom_write_active_series",
	}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if n := strings.TrimSpace(sc.Text()); n != "" && !strings.HasPrefix(n, "#") {
			names = append(names, n)
		}
	}
	if len(names) < 100 {
		t.Fatalf("read only %d names", len(names))
	}
	for _, n := range names {
		if c := ClassifyName(n); c != metriccatalog.Core {
			t.Errorf("%s was stored in production but classifies as %s", n, c)
		}
	}
}

// A core prefix alone does not make a name core.
func TestClassifyNameDoesNotTrustACorePrefix(t *testing.T) {
	for _, n := range []string{"kube_myapp_requests_total", "container_myapp_x", "gitlab_sql_duration_seconds_bucket"} {
		if c := ClassifyName(n); c != metriccatalog.Unknown {
			t.Errorf("ClassifyName(%q) = %s, want unknown", n, c)
		}
	}
}
