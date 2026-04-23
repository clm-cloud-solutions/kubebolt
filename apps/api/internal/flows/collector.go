package flows

import (
	"context"
	"log/slog"
	"time"

	flowpb "github.com/cilium/cilium/api/v1/flow"
)

// RunHubbleCollector wires a HubbleClient to an Aggregator and runs the
// ingest + periodic flush loop until ctx is cancelled. Reconnects with
// exponential backoff on stream errors; exits cleanly on ctx done.
//
// The caller supplies the VictoriaMetrics URL and the relay address.
// Flush cadence is fixed at 5s — Hubble flows arrive quickly and the
// write cost to VM is tiny.
func RunHubbleCollector(ctx context.Context, relayAddr, vmURL string) {
	agg := NewAggregator(vmURL)

	// Flush loop runs independently of the stream so we still persist
	// whatever we've collected even if the relay connection flaps.
	flushCtx, flushCancel := context.WithCancel(ctx)
	defer flushCancel()

	go func() {
		tick := time.NewTicker(5 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-flushCtx.Done():
				return
			case <-tick.C:
				if err := agg.Flush(flushCtx); err != nil {
					slog.Warn("hubble: flush failed", slog.String("error", err.Error()))
				}
			}
		}
	}()

	// Log pair count periodically so bring-up is visible.
	go func() {
		tick := time.NewTicker(30 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-flushCtx.Done():
				return
			case <-tick.C:
				slog.Info("hubble: flow pairs tracked", slog.Int("pairs", agg.Size()))
			}
		}
	}()

	backoff := time.Second
	const backoffMax = 60 * time.Second

	for {
		if ctx.Err() != nil {
			return
		}
		if err := streamOnce(ctx, relayAddr, agg); err != nil && ctx.Err() == nil {
			slog.Warn("hubble: stream ended, will retry",
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
		// Clean return (ctx cancelled) — exit.
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
