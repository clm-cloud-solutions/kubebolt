package opsmetrics

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestVMOp(t *testing.T) {
	for path, want := range map[string]string{
		"/api/v1/query":                     "query",
		"/select/0/prometheus/api/v1/query": "query",
		"/api/v1/query_range":               "query_range",
		"/api/v1/series":                    "metadata",
		"/api/v1/label/__name__/values":     "metadata",
		"/api/v1/export":                    "export",
		"/api/v1/import/prometheus":         "import",
		"/api/v1/write":                     "write",
		"/health":                           "other",
	} {
		if got := vmOp(path); got != want {
			t.Errorf("vmOp(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestVMTransport_CountsResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("query") {
		case "bad":
			w.WriteHeader(http.StatusBadRequest)
		case "down":
			w.WriteHeader(http.StatusServiceUnavailable)
		case "slow":
			time.Sleep(200 * time.Millisecond)
		}
	}))
	defer srv.Close()
	c := VMClient("test", 50*time.Millisecond)
	get := func(q string) {
		resp, err := c.Get(srv.URL + "/api/v1/query?query=" + q)
		if err == nil {
			resp.Body.Close()
		}
	}
	count := func(result string) float64 {
		return testutil.ToFloat64(vmRequests.WithLabelValues("test", "query", result))
	}
	// The counters are package-global: compare against what was there before,
	// so the test holds under -count=N.
	results := []string{"ok", "http_4xx", "http_5xx", "timeout"}
	before := map[string]float64{}
	for _, r := range results {
		before[r] = count(r)
	}
	get("up")
	get("bad")
	get("down")
	get("slow")
	for _, result := range results {
		if got, want := count(result)-before[result], 1.0; got != want {
			t.Errorf("%s = %v, want %v", result, got, want)
		}
	}
}

func TestVMResult_Canceled(t *testing.T) {
	if got := vmResult(nil, context.Canceled, nil); got != "canceled" {
		t.Fatalf("canceled = %q", got)
	}
	if got := vmResult(nil, errors.New("connection refused"), nil); got != "error" {
		t.Fatalf("refused = %q", got)
	}
}

func TestJob_RunWithoutOKIsAnError(t *testing.T) {
	// A name of its own per run: the counters are package-global, and a fresh
	// series keeps the test true under -count=N.
	name := fmt.Sprintf("test_job_%d", time.Now().UnixNano())
	job := NewJob(name, time.Minute)
	if got := testutil.ToFloat64(jobInterval.WithLabelValues(name)); got != 60 {
		t.Fatalf("interval = %v, want 60", got)
	}

	run := job.Start()
	run.End()
	if testutil.ToFloat64(jobRuns.WithLabelValues(name, "error")) != 1 {
		t.Fatal("a run that never reached OK was not counted as an error")
	}
	if testutil.ToFloat64(jobLastSuccess.WithLabelValues(name)) != 0 {
		t.Fatal("a failed run set the last success")
	}

	run = job.Start()
	run.OK()
	run.End()
	if testutil.ToFloat64(jobRuns.WithLabelValues(name, "ok")) != 1 {
		t.Fatal("a successful run was not counted")
	}
	if ts := testutil.ToFloat64(jobLastSuccess.WithLabelValues(name)); time.Since(time.Unix(int64(ts), 0)) > time.Minute {
		t.Fatalf("last success = %v, want now", ts)
	}

	// A nil job measures nothing and does not panic.
	var none *Job
	r := none.Start()
	r.OK()
	r.End()
}

func TestStatusClass(t *testing.T) {
	for status, want := range map[int]string{0: "2xx", 101: "1xx", 204: "2xx", 304: "3xx", 404: "4xx", 500: "5xx", 504: "5xx", 503: "503"} {
		if got := statusClass(status); got != want {
			t.Errorf("statusClass(%d) = %q, want %q", status, got, want)
		}
	}
}

// The self-push stamps job, instance and run_id on every series it writes
// (metrics_selfwrite.go), overwriting a label of the same name: a job label
// once turned every background job into one series named "kubebolt-api".
func TestSeries_DoNotUseLabelsThePushOverwrites(t *testing.T) {
	ObserveHTTP("resources", 200, time.Millisecond, false)
	WSDropped("queue_full")
	run := NewJob("label_check", time.Minute).Start()
	run.OK()
	run.End()
	vmRequests.WithLabelValues("test", "query", "ok").Inc()
	vmSeconds.WithLabelValues("query").Observe(0.1)

	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, mf := range mfs {
		if !strings.HasPrefix(mf.GetName(), "kubebolt_") {
			continue
		}
		seen++
		for _, m := range mf.GetMetric() {
			for _, lp := range m.GetLabel() {
				switch lp.GetName() {
				case "job", "instance", "run_id":
					t.Errorf("%s carries a %q label, which the self-push overwrites", mf.GetName(), lp.GetName())
				}
			}
		}
	}
	if seen < 8 {
		t.Fatalf("only %d kubebolt_ families gathered; the check is not seeing the ops series", seen)
	}
}
