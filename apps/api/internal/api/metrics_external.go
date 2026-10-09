package api

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/golang/snappy"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"google.golang.org/protobuf/encoding/protowire"

	"github.com/kubebolt/kubebolt/apps/api/internal/opsmetrics"
)

// The external watcher (doc #67, O4). Every alert KubeBolt raises about itself
// runs inside the API and reads VictoriaMetrics, so the one failure it can
// never report is its own: the API down, or the store with it. A second copy
// of the API's health series goes to an outside Prometheus — Grafana Cloud —
// through standard remote_write, where an alert on their absence and HTTP
// checks against the public API watch from outside.
//
// Only the platform's own health crosses: series without tenant_id or
// cluster_id, from an allowlist of families. No customer identifier, nothing
// of Kobi, a few hundred series per replica. Off unless
// KUBEBOLT_EXTERNAL_METRICS_URL is set.

// ExternalMetricsConfig is where and how the API pushes its health series.
type ExternalMetricsConfig struct {
	URL      string            // remote_write endpoint, e.g. https://prometheus-…grafana.net/api/prom/push
	User     string            // basic-auth user (Grafana Cloud: the Prometheus instance id)
	Token    string            // basic-auth password (an access-policy token with metrics:write)
	Labels   map[string]string // extra labels on every series, e.g. env=prod — tells deployments apart in one stack
	Interval time.Duration     // 60 s by default: one data point per minute per series
}

// ExternalMetricsConfigFromEnv reads KUBEBOLT_EXTERNAL_METRICS_*. An empty URL
// means off.
func ExternalMetricsConfigFromEnv() ExternalMetricsConfig {
	c := ExternalMetricsConfig{
		URL:      strings.TrimSpace(os.Getenv("KUBEBOLT_EXTERNAL_METRICS_URL")),
		User:     strings.TrimSpace(os.Getenv("KUBEBOLT_EXTERNAL_METRICS_USER")),
		Token:    strings.TrimSpace(os.Getenv("KUBEBOLT_EXTERNAL_METRICS_TOKEN")),
		Labels:   map[string]string{},
		Interval: 60 * time.Second,
	}
	for _, kv := range strings.Split(os.Getenv("KUBEBOLT_EXTERNAL_METRICS_LABELS"), ",") {
		if k, v, ok := strings.Cut(strings.TrimSpace(kv), "="); ok && k != "" {
			c.Labels[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	if d, err := time.ParseDuration(os.Getenv("KUBEBOLT_EXTERNAL_METRICS_INTERVAL")); err == nil && d >= 15*time.Second {
		c.Interval = d
	}
	return c
}

// externalFamilies are the families that cross: names or name prefixes.
var externalFamilies = []string{
	"kubebolt_build_info",
	"kubebolt_http_",
	"kubebolt_job_",
	"kubebolt_vm_",
	"kubebolt_ws_",
	"kubebolt_pg_pool_",
	"kubebolt_platform_",
	"kubebolt_api_runtimes",
	"kubebolt_agent_channels",
	"kubebolt_agent_kube_request_seconds",
	"process_resident_memory_bytes",
	"process_cpu_seconds_total",
	"process_start_time_seconds",
	"go_goroutines",
}

func externalFamily(name string) bool {
	for _, f := range externalFamilies {
		if name == f || (strings.HasSuffix(f, "_") && strings.HasPrefix(name, f)) {
			return true
		}
	}
	return false
}

// PushMetricsExternally sends the API's health series to cfg.URL every
// cfg.Interval until ctx ends. Failures are logged and counted (job
// "external_push"); the next tick tries again.
func PushMetricsExternally(ctx context.Context, gatherer prometheus.Gatherer, cfg ExternalMetricsConfig) {
	if cfg.URL == "" || gatherer == nil {
		return
	}
	base := map[string]string{"job": selfWriteJob, "instance": selfWriteInstance(), "run_id": selfWriteRunID()}
	for k, v := range cfg.Labels {
		base[k] = v
	}
	client := &http.Client{Timeout: 15 * time.Second}
	job := opsmetrics.NewJob("external_push", cfg.Interval)
	slog.Info("external metrics push on", slog.String("url", cfg.URL), slog.Duration("interval", cfg.Interval))
	push := func() {
		run := job.Start()
		defer run.End()
		if err := pushExternalOnce(ctx, client, gatherer, cfg, base, time.Now()); err != nil {
			slog.Warn("external metrics push failed", slog.String("error", err.Error()))
			return
		}
		run.OK()
	}
	push()
	t := time.NewTicker(cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			push()
		}
	}
}

func pushExternalOnce(ctx context.Context, client *http.Client, gatherer prometheus.Gatherer, cfg ExternalMetricsConfig, base map[string]string, now time.Time) error {
	mfs, err := gatherer.Gather()
	if err != nil {
		return fmt.Errorf("gather: %w", err)
	}
	series := externalSeries(mfs, base, now.UnixMilli())
	if len(series) == 0 {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL, bytes.NewReader(snappy.Encode(nil, encodeWriteRequest(series))))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Encoding", "snappy")
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("X-Prometheus-Remote-Write-Version", "0.1.0")
	if cfg.User != "" || cfg.Token != "" {
		req.SetBasicAuth(cfg.User, cfg.Token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("post: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// remoteSeries is one series of a remote_write request: sorted labels
// (__name__ included) and a single sample.
type remoteSeries struct {
	labels [][2]string
	value  float64
	ts     int64
}

// externalSeries turns the allowlisted families into remote_write series,
// expanding histograms (_bucket, _sum, _count) and summaries the way a
// Prometheus scrape would. A series carrying tenant_id or cluster_id never
// crosses, whatever its family.
func externalSeries(mfs []*dto.MetricFamily, base map[string]string, ts int64) []remoteSeries {
	var out []remoteSeries
	add := func(name string, m *dto.Metric, extra [2]string, v float64) {
		labels := map[string]string{}
		for k, val := range base {
			labels[k] = val
		}
		for _, lp := range m.GetLabel() {
			labels[lp.GetName()] = lp.GetValue()
		}
		if extra[0] != "" {
			labels[extra[0]] = extra[1]
		}
		labels["__name__"] = name
		ls := make([][2]string, 0, len(labels))
		for k, val := range labels {
			ls = append(ls, [2]string{k, val})
		}
		sort.Slice(ls, func(i, j int) bool { return ls[i][0] < ls[j][0] })
		out = append(out, remoteSeries{labels: ls, value: v, ts: ts})
	}
	for _, mf := range mfs {
		name := mf.GetName()
		if !externalFamily(name) {
			continue
		}
	metrics:
		for _, m := range mf.GetMetric() {
			for _, lp := range m.GetLabel() {
				if lp.GetName() == "tenant_id" || lp.GetName() == "cluster_id" {
					continue metrics
				}
			}
			switch mf.GetType() {
			case dto.MetricType_COUNTER:
				add(name, m, [2]string{}, m.GetCounter().GetValue())
			case dto.MetricType_GAUGE:
				add(name, m, [2]string{}, m.GetGauge().GetValue())
			case dto.MetricType_UNTYPED:
				add(name, m, [2]string{}, m.GetUntyped().GetValue())
			case dto.MetricType_HISTOGRAM:
				h := m.GetHistogram()
				for _, b := range h.GetBucket() {
					add(name+"_bucket", m, [2]string{"le", remoteWriteFloat(b.GetUpperBound())}, float64(b.GetCumulativeCount()))
				}
				add(name+"_bucket", m, [2]string{"le", "+Inf"}, float64(h.GetSampleCount()))
				add(name+"_sum", m, [2]string{}, h.GetSampleSum())
				add(name+"_count", m, [2]string{}, float64(h.GetSampleCount()))
			case dto.MetricType_SUMMARY:
				s := m.GetSummary()
				for _, q := range s.GetQuantile() {
					add(name, m, [2]string{"quantile", remoteWriteFloat(q.GetQuantile())}, q.GetValue())
				}
				add(name+"_sum", m, [2]string{}, s.GetSampleSum())
				add(name+"_count", m, [2]string{}, float64(s.GetSampleCount()))
			}
		}
	}
	return out
}

func remoteWriteFloat(f float64) string {
	if math.IsInf(f, 1) {
		return "+Inf"
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// encodeWriteRequest builds a Prometheus remote_write WriteRequest (v1):
// field 1 TimeSeries{1 Label{1 name, 2 value}, 2 Sample{1 value, 2 timestamp}}.
func encodeWriteRequest(series []remoteSeries) []byte {
	var wr []byte
	for _, s := range series {
		var ts []byte
		for _, l := range s.labels {
			var lb []byte
			lb = protowire.AppendTag(lb, 1, protowire.BytesType)
			lb = protowire.AppendString(lb, l[0])
			lb = protowire.AppendTag(lb, 2, protowire.BytesType)
			lb = protowire.AppendString(lb, l[1])
			ts = protowire.AppendTag(ts, 1, protowire.BytesType)
			ts = protowire.AppendBytes(ts, lb)
		}
		var sb []byte
		sb = protowire.AppendTag(sb, 1, protowire.Fixed64Type)
		sb = protowire.AppendFixed64(sb, math.Float64bits(s.value))
		sb = protowire.AppendTag(sb, 2, protowire.VarintType)
		sb = protowire.AppendVarint(sb, uint64(s.ts))
		ts = protowire.AppendTag(ts, 2, protowire.BytesType)
		ts = protowire.AppendBytes(ts, sb)
		wr = protowire.AppendTag(wr, 1, protowire.BytesType)
		wr = protowire.AppendBytes(wr, ts)
	}
	return wr
}
