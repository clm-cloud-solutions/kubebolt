package api

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/expfmt"
)

// SelfWriteMetricsToVM is the small goroutine that pushes the
// backend's own Prometheus counters into VictoriaMetrics on a fixed
// interval. Spec #09 V2 Item 5b architecture decision: the
// /admin/ingest-activity panel queries VM via PromQL like every
// other dashboard in KubeBolt; we just close the loop ourselves
// instead of relying on an external scraper.
//
// Why API-writes-to-VM beats both alternatives we considered:
//
//   - vs vmagent-scrapes-backend (the original V2 design): the
//     scraper would need to reach the backend's /metrics endpoint,
//     which in SaaS topologies crosses customer-cluster ↔ KubeBolt-
//     hosting-network boundaries — latency + firewalls + one more
//     moving part. The WRITE direction stays local: API and VM are
//     co-located in the same Helm release / same private network in
//     every production topology.
//
//   - vs in-process ring buffer (intermediate exploration): VM is
//     purpose-built for time-series; reinventing a ring buffer +
//     custom JSON shape was duplicating its job. VM also persists
//     history across backend restarts; the buffer didn't. Frontend
//     stays consistent with Capacity/Reliability pages that use the
//     same PromQL path.
//
// Cadence: 30 seconds matches what an external Prometheus scraper
// would default to. Bandwidth is trivial — current backend metrics
// total ~30 series at ~3 tenants × 30s = ~1 KiB/min to VM, dwarfed
// by the agent sample stream.
//
// Failure handling: write errors are logged at WARN and the goroutine
// continues. A transient VM outage means the dashboard sees a gap
// rather than a permanent loss; counters keep accumulating in
// process memory and the next successful write captures the new
// cumulative total. This is the same behavior a Prometheus scraper
// would exhibit during a VM outage.
func SelfWriteMetricsToVM(ctx context.Context, gatherer prometheus.Gatherer, vmURL string) {
	if gatherer == nil || vmURL == "" {
		return
	}
	client := &http.Client{Timeout: 10 * time.Second}
	endpoint := vmURL + "/api/v1/import/prometheus"

	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()

	// Write immediately on startup so the dashboard's PromQL queries
	// have at least one data point within seconds of boot — not
	// 30 seconds. Without this the first tick of refetchInterval=30s
	// on the page would land on an empty VM.
	if err := pushMetricsOnce(ctx, client, gatherer, endpoint); err != nil {
		slog.Warn("self-write metrics to VM failed on startup",
			slog.String("error", err.Error()))
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if err := pushMetricsOnce(ctx, client, gatherer, endpoint); err != nil {
				slog.Warn("self-write metrics to VM failed",
					slog.String("error", err.Error()))
			}
		}
	}
}

// pushMetricsOnce renders the gatherer's current state as Prometheus
// text format and POSTs it to VM's /api/v1/import/prometheus endpoint
// (which accepts the text format natively — no protobuf conversion).
//
// Uses expfmt with the standard text-format MIME type. The Gatherer's
// output is already deduplicated + sorted by Prometheus client library
// conventions; VM's importer handles the rest.
func pushMetricsOnce(ctx context.Context, client *http.Client, gatherer prometheus.Gatherer, endpoint string) error {
	mfs, err := gatherer.Gather()
	if err != nil {
		return fmt.Errorf("gather: %w", err)
	}
	if len(mfs) == 0 {
		return nil
	}

	var buf bytes.Buffer
	enc := expfmt.NewEncoder(&buf, expfmt.NewFormat(expfmt.TypeTextPlain))
	for _, mf := range mfs {
		if err := enc.Encode(mf); err != nil {
			// Continue on per-family encode error so a single malformed
			// metric doesn't poison the whole batch. The error is
			// logged inline rather than returned because the next
			// tick has a fresh chance.
			slog.Debug("encode metric family failed",
				slog.String("metric", mf.GetName()),
				slog.String("error", err.Error()))
			continue
		}
	}
	if buf.Len() == 0 {
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &buf)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "text/plain")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("post: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}
