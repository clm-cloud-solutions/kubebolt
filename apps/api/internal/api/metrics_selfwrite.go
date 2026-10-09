package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/expfmt"

	"github.com/kubebolt/kubebolt/apps/api/internal/opsmetrics"
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
	client := opsmetrics.VMClient("selfwrite", 10*time.Second)
	endpoint := selfWriteEndpoint(vmURL)
	job := opsmetrics.NewJob("selfwrite", 30*time.Second)
	push := func() error {
		run := job.Start()
		defer run.End()
		err := pushMetricsOnce(ctx, client, gatherer, endpoint)
		if err == nil {
			run.OK()
		}
		return err
	}

	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()

	// Write immediately on startup so the dashboard's PromQL queries
	// have at least one data point within seconds of boot — not
	// 30 seconds. Without this the first tick of refetchInterval=30s
	// on the page would land on an empty VM.
	if err := push(); err != nil {
		slog.Warn("self-write metrics to VM failed on startup",
			slog.String("error", err.Error()))
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if err := push(); err != nil {
				slog.Warn("self-write metrics to VM failed",
					slog.String("error", err.Error()))
			}
		}
	}
}

// selfWriteEndpoint is VictoriaMetrics' import URL with the labels every
// pushed series carries:
//
//   - instance=<this replica>. The push has no timestamps and the series no
//     other per-replica label, so with two replicas each one wrote the SAME
//     series with its own counter value and VictoriaMetrics interleaved them —
//     rate() saw a reset at every flip.
//   - run_id=<this process>. A restarted process keeps its hostname (a
//     container restart inside the same pod, an OOMKill; the same machine in
//     dev) and its counters start again from zero. VictoriaMetrics only sees a
//     reset when the value drops; a sparse counter that comes back above its
//     old value hides it — 26,816 cache tokens, a restart, a session of 27,082:
//     counted 266. With run_id every process writes its own series, which
//     start at zero. Opaque on purpose: it names the process, nothing else.
//   - job=kubebolt-api, which tells the API's go_*/process_* apart from the
//     ones VictoriaMetrics scrapes from itself (job="victoria-metrics").
//
// Counters are summed across run_id: every process's increments happened.
// A gauge is read as the live process reports it (liveProcess in the web
// app's utils/promql.ts: of the series that differ only in run_id, the one
// written last), because the dead process's last value stays readable for the
// 5-minute lookback.
func selfWriteEndpoint(vmURL string) string {
	return vmURL + "/api/v1/import/prometheus?" + url.Values{"extra_label": {
		"instance=" + selfWriteInstance(), "run_id=" + selfWriteRunID(), "job=" + selfWriteJob,
	}}.Encode()
}

// selfWriteJob is the job label on everything the API pushes about itself.
const selfWriteJob = "kubebolt-api"

// selfWriteInstance names this replica: the hostname, which in Kubernetes is
// the pod name.
func selfWriteInstance() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "kubebolt-api"
}

// selfWriteRunID names this process: eight random hex characters, drawn once.
// crypto/rand.Read does not fail (since Go 1.24 it aborts the process instead).
var selfWriteRunID = sync.OnceValue(func() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
})

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
