package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang/snappy"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestExternalMetricsConfigFromEnv(t *testing.T) {
	t.Setenv("KUBEBOLT_EXTERNAL_METRICS_URL", " https://prom.example/api/prom/push ")
	t.Setenv("KUBEBOLT_EXTERNAL_METRICS_USER", "12345")
	t.Setenv("KUBEBOLT_EXTERNAL_METRICS_TOKEN", "secret")
	t.Setenv("KUBEBOLT_EXTERNAL_METRICS_LABELS", "env=prod, region = westeurope ,broken")
	t.Setenv("KUBEBOLT_EXTERNAL_METRICS_INTERVAL", "5s") // below the floor: ignored
	c := ExternalMetricsConfigFromEnv()
	if c.URL != "https://prom.example/api/prom/push" || c.User != "12345" || c.Token != "secret" {
		t.Fatalf("config = %+v", c)
	}
	if c.Labels["env"] != "prod" || c.Labels["region"] != "westeurope" || len(c.Labels) != 2 {
		t.Fatalf("labels = %v", c.Labels)
	}
	if c.Interval != 60*time.Second {
		t.Fatalf("interval = %v, want the 60s default", c.Interval)
	}
}

// What crosses: the allowlisted health families, histograms expanded, and
// never a series that names an org or a cluster.
func TestExternalSeries_OnlyPlatformHealthCrosses(t *testing.T) {
	reg := prometheus.NewRegistry()
	http5xx := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "kubebolt_http_requests_total"}, []string{"group", "code"})
	vmSecs := prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "kubebolt_vm_request_seconds", Buckets: []float64{0.1, 1}}, []string{"op"})
	kobi := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "kubebolt_kobi_copilot_sessions_total"}, []string{"tenant_id"})
	jobsByOrg := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "kubebolt_job_runs_total"}, []string{"name", "tenant_id"})
	gc := prometheus.NewGauge(prometheus.GaugeOpts{Name: "go_memstats_heap_alloc_bytes"})
	reg.MustRegister(http5xx, vmSecs, kobi, jobsByOrg, gc)
	http5xx.WithLabelValues("resources", "5xx").Add(3)
	vmSecs.WithLabelValues("query").Observe(0.5)
	kobi.WithLabelValues("org-a").Inc()
	jobsByOrg.WithLabelValues("retention", "org-a").Inc()
	gc.Set(1)

	mfs, _ := reg.Gather()
	got := map[string]int{}
	for _, s := range externalSeries(mfs, map[string]string{"env": "prod"}, 1) {
		name, env := "", ""
		for i, l := range s.labels {
			if i > 0 && s.labels[i-1][0] > l[0] {
				t.Fatalf("labels not sorted: %v", s.labels)
			}
			switch l[0] {
			case "__name__":
				name = l[1]
			case "env":
				env = l[1]
			case "tenant_id", "cluster_id":
				t.Fatalf("%s crossed with %s", name, l[0])
			}
		}
		if env != "prod" {
			t.Fatalf("%s lacks the deployment label", name)
		}
		got[name]++
	}
	want := map[string]int{
		"kubebolt_http_requests_total":       1,
		"kubebolt_vm_request_seconds_bucket": 3, // 0.1, 1, +Inf
		"kubebolt_vm_request_seconds_sum":    1,
		"kubebolt_vm_request_seconds_count":  1,
	}
	if len(got) != len(want) {
		t.Fatalf("crossed %v, want %v", got, want)
	}
	for k, n := range want {
		if got[k] != n {
			t.Errorf("%s: %d series, want %d", k, got[k], n)
		}
	}
}

// One push, end to end: snappy protobuf with the remote_write headers and
// basic auth, decodable as a WriteRequest.
func TestPushExternalOnce(t *testing.T) {
	var gotNames []string
	var user, pass string
	var headersOK bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, _ = r.BasicAuth()
		headersOK = r.Header.Get("Content-Encoding") == "snappy" &&
			r.Header.Get("Content-Type") == "application/x-protobuf" &&
			r.Header.Get("X-Prometheus-Remote-Write-Version") == "0.1.0"
		body, _ := io.ReadAll(r.Body)
		decoded, err := snappy.Decode(nil, body)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		for rem := decoded; len(rem) > 0; {
			num, typ, n := protowire.ConsumeTag(rem)
			rem = rem[n:]
			if num == 1 && typ == protowire.BytesType {
				ts, m := protowire.ConsumeBytes(rem)
				rem = rem[m:]
				name, samples := inspectTimeSeries(ts)
				if samples != 1 {
					t.Errorf("%s has %d samples, want 1", name, samples)
				}
				gotNames = append(gotNames, name)
				continue
			}
			rem = rem[protowire.ConsumeFieldValue(num, typ, rem):]
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	reg := prometheus.NewRegistry()
	g := prometheus.NewGauge(prometheus.GaugeOpts{Name: "kubebolt_build_info", ConstLabels: prometheus.Labels{"version": "test"}})
	reg.MustRegister(g)
	g.Set(1)
	cfg := ExternalMetricsConfig{URL: srv.URL, User: "12345", Token: "secret"}
	if err := pushExternalOnce(context.Background(), srv.Client(), reg, cfg, map[string]string{"job": "kubebolt-api"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if user != "12345" || pass != "secret" || !headersOK {
		t.Fatalf("auth %q/%q, remote_write headers ok=%v", user, pass, headersOK)
	}
	if len(gotNames) != 1 || gotNames[0] != "kubebolt_build_info" {
		t.Fatalf("pushed %v", gotNames)
	}
}
