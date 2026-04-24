package flows

import (
	"context"
	"log/slog"
	"time"

	flowpb "github.com/cilium/cilium/api/v1/flow"

	"github.com/kubebolt/kubebolt/packages/agent/internal/buffer"
)

// RunCollector runs the Hubble ingest + aggregation loop until ctx is
// cancelled. Stream errors retry with exponential backoff without
// crashing the agent — Hubble Relay might be temporarily unreachable
// during Cilium upgrades or node reboots.
func RunCollector(ctx context.Context, relayAddr string, buf *buffer.Ring, clusterID, clusterName, node string) {
	agg := NewAggregator(buf, clusterID, clusterName, node)

	// Flush loop independent of the stream so samples still accumulate
	// and ship even when the relay connection is flapping.
	flushCtx, flushCancel := context.WithCancel(ctx)
	defer flushCancel()
	go agg.RunFlushLoop(flushCtx, 5*time.Second)

	backoff := time.Second
	const backoffMax = 60 * time.Second

	for {
		if ctx.Err() != nil {
			return
		}
		if err := streamOnce(ctx, relayAddr, agg); err != nil && ctx.Err() == nil {
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

func streamOnce(ctx context.Context, relayAddr string, agg *Aggregator) error {
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
