package agent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"
)

// The same series must reach VictoriaMetrics as the same text on every
// write: VM keys its storage/tsid cache on that text, so a label order
// that changes from one write to the next turns each write into a cache
// miss and an indexdb lookup.
func TestVMWriter_WritesLabelsSortedByName(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/import/prometheus" {
			t.Errorf("path = %q, want /api/v1/import/prometheus", r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	ts := timestamppb.New(time.UnixMilli(1_700_000_000_000))
	sample := func() *agentv2.Sample {
		return &agentv2.Sample{
			MetricName: "container_cpu_usage_seconds_total",
			Value:      1.5,
			Timestamp:  ts,
			Labels: map[string]string{
				"tenant_id":     "t1",
				"cluster_id":    "c1",
				"node":          "node-a",
				"namespace":     "shop",
				"pod":           "web-7d9f8-abcde",
				"container":     "app",
				"workload":      "web",
				"workload_kind": "Deployment",
				"note":          `say "hi"`,
			},
		}
	}
	want := `container_cpu_usage_seconds_total{cluster_id="c1",container="app",namespace="shop",node="node-a",note="say \"hi\"",pod="web-7d9f8-abcde",tenant_id="t1",workload="web",workload_kind="Deployment"} 1.5 1700000000000`

	w := NewVMWriter(srv.URL)
	const writes = 20
	for i := 0; i < writes; i++ {
		batch := []*agentv2.Sample{sample(), sample(), {MetricName: "up", Value: 1, Timestamp: ts}}
		if err := w.Write(context.Background(), batch); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	if len(bodies) != writes {
		t.Fatalf("got %d requests, want %d", len(bodies), writes)
	}
	for i, body := range bodies {
		lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
		if len(lines) != 3 {
			t.Fatalf("write %d: got %d lines, want 3:\n%s", i, len(lines), body)
		}
		for j, line := range lines[:2] {
			if line != want {
				t.Fatalf("write %d, line %d:\n got %s\nwant %s", i, j, line, want)
			}
		}
		if lines[2] != "up 1 1700000000000" {
			t.Fatalf("write %d: unlabelled sample = %q", i, lines[2])
		}
	}
}
