package collector

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/packages/agent/internal/buffer"
)

func TestParseExporterTargets(t *testing.T) {
	t.Run("empty is disabled", func(t *testing.T) {
		targets, err := ParseExporterTargets("")
		if err != nil || targets != nil {
			t.Fatalf("empty env must be (nil, nil), got %v, %v", targets, err)
		}
	})

	t.Run("single and multiple with spaces", func(t *testing.T) {
		targets, err := ParseExporterTargets(" opencost=http://opencost.opencost.svc:9003/metrics , other=https://x.svc/metrics ")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(targets) != 2 || targets[0].Name != "opencost" || targets[1].Name != "other" {
			t.Fatalf("targets = %+v", targets)
		}
		if targets[0].URL != "http://opencost.opencost.svc:9003/metrics" {
			t.Errorf("url not trimmed: %q", targets[0].URL)
		}
	})

	t.Run("malformed entries error out", func(t *testing.T) {
		for _, bad := range []string{"opencost", "=http://x", "opencost=", "opencost=ftp://x"} {
			if _, err := ParseExporterTargets(bad); err == nil {
				t.Errorf("%q should be rejected", bad)
			}
		}
	})
}

// openCostExposition mimics a slice of OpenCost's /metrics output:
// payload families + the Go runtime noise every exporter emits + an
// attempt to spoof reserved identity labels.
const openCostExposition = `# HELP node_total_hourly_cost Total node cost per hour
# TYPE node_total_hourly_cost gauge
node_total_hourly_cost{instance="node-a",node="node-a"} 0.336
node_total_hourly_cost{instance="node-b",node="node-b"} 0.672
container_cpu_allocation{namespace="production",pod="postgres-prod-0",container="postgres"} 1.5
container_memory_allocation_bytes{namespace="production",pod="postgres-prod-0",container="postgres"} 4.294967296e+09
pv_hourly_cost{persistentvolume="pvc-123",cluster_id="spoofed-cluster",tenant_id="spoofed-tenant"} 0.01
go_goroutines 42
process_cpu_seconds_total 12.5
promhttp_metric_handler_requests_total{code="200"} 100
`

func TestExporterCollect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(openCostExposition))
	}))
	defer srv.Close()

	e := NewExporter(ExporterTarget{Name: "opencost", URL: srv.URL}, "uid-123", "prod-us", "tenant-9", nil)
	samples, err := e.Collect(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 5 payload rows; go_/process_/promhttp_ dropped.
	if len(samples) != 5 {
		t.Fatalf("samples = %d, want 5 (runtime families must be dropped)", len(samples))
	}
	byMetric := map[string]int{}
	for _, s := range samples {
		byMetric[s.MetricName]++
		if s.Labels["source"] != "opencost" {
			t.Errorf("%s: source = %q, want opencost", s.MetricName, s.Labels["source"])
		}
		if s.Labels["cluster_id"] != "uid-123" || s.Labels["cluster_name"] != "prod-us" || s.Labels["tenant_id"] != "tenant-9" {
			t.Errorf("%s: identity labels wrong: %v", s.MetricName, s.Labels)
		}
	}
	if byMetric["node_total_hourly_cost"] != 2 || byMetric["container_cpu_allocation"] != 1 {
		t.Errorf("family counts wrong: %v", byMetric)
	}

	// Reserved-label spoof: the pv_hourly_cost row tried to stamp its
	// own cluster_id/tenant_id — the agent's identity must win.
	for _, s := range samples {
		if s.MetricName == "pv_hourly_cost" {
			if s.Labels["cluster_id"] != "uid-123" || s.Labels["tenant_id"] != "tenant-9" {
				t.Errorf("spoofed identity labels leaked through: %v", s.Labels)
			}
			if s.Labels["persistentvolume"] != "pvc-123" {
				t.Errorf("legit exporter label dropped: %v", s.Labels)
			}
		}
	}
}

func TestExporterCollect_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	e := NewExporter(ExporterTarget{Name: "opencost", URL: srv.URL}, "uid", "", "", nil)
	if _, err := e.Collect(context.Background()); err == nil {
		t.Fatal("5xx must surface as an error (the scrape loop logs and retries next tick)")
	}
}

func TestScrapeLoop_PushesToRing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("node_total_hourly_cost{node=\"a\"} 0.5\n"))
	}))
	defer srv.Close()

	buf := buffer.New(100)
	e := NewExporter(ExporterTarget{Name: "opencost", URL: srv.URL}, "uid", "", "", nil)

	// The loop scrapes immediately on entry; cancel right after so the
	// test doesn't wait a full tick.
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()
	scrapeLoop(ctx, []*Exporter{e}, buf, nil, time.Hour)

	if got := buf.Len(); got != 1 {
		t.Fatalf("ring has %d samples, want 1 from the immediate first scrape", got)
	}
}
