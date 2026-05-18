package flows

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	flowpb "github.com/cilium/cilium/api/v1/flow"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/kubebolt/kubebolt/packages/agent/internal/buffer"
	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"
)

// activeAggregator is the currently-running aggregator instance for
// this process. Set when RunCollector starts (only on the leader pod
// after leader election); cleared when it returns. Other packages
// reach it via ActiveAggregator() so they can plumb size gauges into
// kubebolt_agent_* observability without RunCollector having to grow
// a returns-the-aggregator API.
//
// Atomic pointer (Go 1.19+) — lockless read, single-writer store/clear.
// Tests that construct an aggregator via NewAggregator directly do NOT
// touch this global, so they don't interfere with self collector wiring.
var activeAggregator atomic.Pointer[Aggregator]

// ActiveAggregator returns the aggregator currently running in this
// process, or nil if none (non-leader pod, Hubble disabled, or
// pre-startup). Callers should treat nil as "no Hubble state to
// report" and skip emission.
func ActiveAggregator() *Aggregator {
	return activeAggregator.Load()
}

// RunCollector runs the Hubble ingest + aggregation loop until ctx is
// cancelled. Stream errors retry with exponential backoff without
// crashing the agent — Hubble Relay might be temporarily unreachable
// during Cilium upgrades or node reboots.
//
// Also emits a `hubble_collector_up` gauge on a 30s heartbeat so the
// UI can distinguish "Cilium working" from "Cilium unreachable" even
// between flow samples. The gauge lives on the leader only — non-leader
// pods don't run the collector at all.
func RunCollector(ctx context.Context, relayAddr string, buf *buffer.Ring, clusterID, clusterName, node, tenantID string) {
	agg := NewAggregator(buf, clusterID, clusterName, node, tenantID)
	// Publish for self-metrics (kubebolt_agent_aggregator_keys). Clear
	// on return so a future re-run after a relay-failure restart
	// re-publishes a fresh instance. Non-leader code paths never run
	// this function so activeAggregator stays nil on those pods.
	activeAggregator.Store(agg)
	defer activeAggregator.Store(nil)

	// Flush loop independent of the stream so samples still accumulate
	// and ship even when the relay connection is flapping.
	flushCtx, flushCancel := context.WithCancel(ctx)
	defer flushCancel()
	go agg.RunFlushLoop(flushCtx, 5*time.Second)

	// Connection state: atomic bool so the heartbeat goroutine can read
	// while streamOnce writes without a lock. Starts at 0 (down) since
	// we haven't connected yet.
	var connected atomic.Bool
	go runStatusHeartbeat(flushCtx, buf, clusterID, clusterName, node, &connected)

	backoff := time.Second
	const backoffMax = 60 * time.Second

	for {
		if ctx.Err() != nil {
			return
		}
		if err := streamOnce(ctx, relayAddr, agg, &connected); err != nil && ctx.Err() == nil {
			connected.Store(false)
			// Emit immediately on disconnect so the UI reflects the
			// change without waiting for the next heartbeat tick.
			emitCollectorStatus(buf, clusterID, clusterName, node, false)
			slog.Warn("hubble: stream ended, will retry",
				slog.String("relay", relayAddr),
				slog.String("error", err.Error()),
				slog.Duration("backoff", backoff),
			)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff *= 2
			if backoff > backoffMax {
				backoff = backoffMax
			}
			continue
		}
		return
	}
}

// runStatusHeartbeat emits a `hubble_collector_up` sample every 30s so
// VM never sees the gauge fall out of its 5-minute staleness window.
// Cheap — one sample per tick per leader pod.
func runStatusHeartbeat(ctx context.Context, buf *buffer.Ring, clusterID, clusterName, node string, connected *atomic.Bool) {
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			emitCollectorStatus(buf, clusterID, clusterName, node, connected.Load())
		}
	}
}

// emitCollectorStatus writes one sample with value 1 (streaming) or 0
// (disconnected / never connected). Labels match the flow samples so
// dashboards can join on (cluster_id, node) without extra config.
func emitCollectorStatus(buf *buffer.Ring, clusterID, clusterName, node string, up bool) {
	labels := map[string]string{
		"cluster_id": clusterID,
		"node":       node,
		"source":     "hubble",
	}
	if clusterName != "" {
		labels["cluster_name"] = clusterName
	}
	value := 0.0
	if up {
		value = 1.0
	}
	buf.Push([]*agentv2.Sample{{
		Timestamp:  timestamppb.Now(),
		MetricName: "hubble_collector_up",
		Value:      value,
		Labels:     labels,
	}})
}

func streamOnce(ctx context.Context, relayAddr string, agg *Aggregator, connected *atomic.Bool) error {
	client, err := NewHubble(relayAddr)
	if err != nil {
		return err
	}
	defer client.Close()

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	status, err := client.Ping(pingCtx)
	cancel()
	if err != nil {
		return err
	}
	slog.Info("hubble: connected",
		slog.String("relay", relayAddr),
		slog.String("version", status.GetVersion()),
		slog.Int("num_flows_buffered", int(status.GetNumFlows())),
	)
	connected.Store(true)
	// Immediate emission so the UI flips to "up" without waiting for
	// the next heartbeat tick.
	emitCollectorStatus(agg.buffer, agg.clusterID, agg.clusterName, agg.node, true)

	flows := make(chan *flowpb.Flow, 1024)
	errCh := make(chan error, 1)
	go func() {
		errCh <- client.Stream(ctx, flows)
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case f := <-flows:
			agg.Record(f)
		case err := <-errCh:
			return err
		}
	}
}
