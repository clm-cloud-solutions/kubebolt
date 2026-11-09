package copilot

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// A bucketed point is stamped at the END of its bucket. On 2026-11-09 Kobi
// read a 7-day CPU peak stamped 6 Nov 05:25 as its time; it happened on 5 Nov
// at 18:14. peakAtQuery asks for the minute over the same per-minute
// expression the buckets take their peaks of.
func TestPromBuilder_PeakAtQuery(t *testing.T) {
	b := promBuilder{
		kind: "Deployment", namespace: "default", name: "api", clusterUID: "uid-abc",
		rateWindow: 15 * time.Minute, bucket: 14 * time.Hour,
	}
	span := 7*24*time.Hour + 14*time.Hour
	cases := map[MetricKind]string{
		MetricMemory: `tmax_over_time(sum(max_over_time(container_memory_working_set_bytes{cluster_id="uid-abc",namespace="default",pod=~"api-[a-z0-9]+-[a-z0-9]+",pod_uid!=""}[1m]))[182h:1m])`,
		MetricCPU:    `tmax_over_time(sum(rate(container_cpu_usage_seconds_total{cluster_id="uid-abc",namespace="default",pod=~"api-[a-z0-9]+-[a-z0-9]+",pod_uid!=""}[1m]))[182h:1m])`,
	}
	for m, want := range cases {
		got, ok := b.peakAtQuery(m, span)
		if !ok || got != want {
			t.Errorf("%s peakAtQuery:\n  want: %s\n  got:  %s (ok=%v)", m, want, got, ok)
		}
	}
	// Same expression the trend takes its peaks of: the query is the trend's
	// with max_over_time → tmax_over_time and the bucket → the span.
	if trend := b.buildMemory(); !strings.Contains(trend, strings.TrimSuffix(strings.TrimPrefix(cases[MetricMemory], "tmax_over_time("), "[182h:1m])")) {
		t.Errorf("peakAtQuery and the trend read different expressions:\n  trend: %s", trend)
	}

	node := promBuilder{kind: "Node", name: "worker-1", clusterUID: "uid-abc", rateWindow: 15 * time.Minute, bucket: 14 * time.Hour}
	if got, _ := node.peakAtQuery(MetricMemory, span); got != `tmax_over_time(sum(max_over_time(node_memory_working_set_bytes{cluster_id="uid-abc",node="worker-1"}[1m]))[182h:1m])` {
		t.Errorf("node memory peakAtQuery: %s", got)
	}

	if _, ok := b.peakAtQuery(MetricNetworkRX, span); ok {
		t.Error("network has no bucketed peak, so no peak time query")
	}
	unbucketed := b
	unbucketed.bucket = 0
	if _, ok := unbucketed.peakAtQuery(MetricCPU, span); ok {
		t.Error("an unbucketed range's points carry their own time; no query needed")
	}
}

// fakePeakVM answers the range query with `values` and the instant query
// (tmax_over_time) with `peakAt`, or a 500 when peakAt is 0. It records the
// instant queries it saw.
func fakePeakVM(t *testing.T, values string, peakAt int64) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var instant []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/api/v1/query_range") {
			_, _ = fmt.Fprintf(w, `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":%s}]}}`, values)
			return
		}
		mu.Lock()
		instant = append(instant, r.URL.Query().Get("query")+" @"+r.URL.Query().Get("time"))
		mu.Unlock()
		if peakAt == 0 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = fmt.Fprintf(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,"%d"]}]}}`, peakAt)
	}))
	t.Cleanup(srv.Close)
	return srv, &instant
}

// Bucketed: maxAt is the minute VictoriaMetrics reports, not the stamp of
// the bucket that holds the max — and the query covers exactly what the
// points cover, from the first bucket's start to the last point.
func TestRunMetric_BucketedMaxAtIsThePeakMinute(t *testing.T) {
	// Three 14h buckets ending at 1700100800 (the max, 0.031, is the second).
	srv, instant := fakePeakVM(t, `[[1700000000,"0.005"],[1700050400,"0.031"],[1700100800,"0.004"]]`, 1700020440)
	b := promBuilder{
		kind: "Deployment", namespace: "default", name: "api", clusterUID: "uid-abc",
		rateWindow: 15 * time.Minute, bucket: 14 * time.Hour,
	}
	withFakeVM(t, srv, func() {
		end := time.Unix(1700100800, 0)
		mr, err := runMetric(context.Background(), b, MetricCPU, end.Add(-28*time.Hour), end, 14*time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if mr.Summary.Max != 0.031 {
			t.Errorf("max: %v", mr.Summary.Max)
		}
		if mr.Summary.MaxAt == nil || !mr.Summary.MaxAt.Equal(time.Unix(1700020440, 0)) {
			t.Errorf("maxAt: got %v, want the peak minute %v (not the bucket stamp %v)",
				mr.Summary.MaxAt, time.Unix(1700020440, 0).UTC(), time.Unix(1700050400, 0).UTC())
		}
	})
	if len(*instant) != 1 {
		t.Fatalf("instant queries: %v", *instant)
	}
	// Points span 28h, plus the first bucket's 14h before it: 42h, at the last point.
	if q := (*instant)[0]; !strings.HasPrefix(q, "tmax_over_time(") || !strings.HasSuffix(q, "[42h:1m]) @1700100800") {
		t.Errorf("peak query: %s", q)
	}
}

// When the minute cannot be read, the tool still answers: no maxAt, and
// never the bucket's stamp in its place.
func TestRunMetric_BucketedMaxAtAbsentWhenTheQueryFails(t *testing.T) {
	srv, _ := fakePeakVM(t, `[[1700000000,"10"],[1700050400,"20"]]`, 0)
	b := promBuilder{kind: "Node", name: "worker-1", clusterUID: "uid-abc", rateWindow: 15 * time.Minute, bucket: 14 * time.Hour}
	withFakeVM(t, srv, func() {
		end := time.Unix(1700050400, 0)
		mr, err := runMetric(context.Background(), b, MetricMemory, end.Add(-14*time.Hour), end, 14*time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if mr.Summary.Max != 20 || mr.Summary.MaxAt != nil {
			t.Errorf("summary: %+v", mr.Summary)
		}
	})
}

// Unbucketed: each point is its own sample, so maxAt is the max point's time
// and no extra query is spent.
func TestRunMetric_UnbucketedMaxAtIsTheMaxPoint(t *testing.T) {
	srv, instant := fakePeakVM(t, `[[1700000000,"0.1"],[1700000025,"0.4"],[1700000050,"0.2"]]`, 1)
	b := promBuilder{kind: "Deployment", namespace: "default", name: "api", clusterUID: "uid-abc", rateWindow: time.Minute}
	withFakeVM(t, srv, func() {
		end := time.Unix(1700000050, 0)
		mr, err := runMetric(context.Background(), b, MetricCPU, end.Add(-5*time.Minute), end, 25*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if mr.Summary.MaxAt == nil || !mr.Summary.MaxAt.Equal(time.Unix(1700000025, 0)) {
			t.Errorf("maxAt: %v", mr.Summary.MaxAt)
		}
	})
	if len(*instant) != 0 {
		t.Errorf("unbucketed range spent instant queries: %v", *instant)
	}
}
