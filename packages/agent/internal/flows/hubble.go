// Package flows contains the agent-side Cilium Hubble adapter. This is
// Phase 2.1 Level 2 of the Traffic Observability ladder, but implemented
// as an agent responsibility rather than a backend-side collector so
// KubeBolt's SaaS deployment model works: the agent bridges cluster-internal
// sources (Hubble Relay here, potentially Prometheus / mesh metrics later)
// and pushes normalized samples out over the existing StreamMetrics gRPC
// channel. The customer never exposes Hubble itself.
//
// Only one instance of this collector runs per cluster at a time — see
// leader.go — because Hubble Relay is cluster-wide and having every
// agent pod scrape it would duplicate data.
package flows

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	flowpb "github.com/cilium/cilium/api/v1/flow"
	observerpb "github.com/cilium/cilium/api/v1/observer"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// HubbleClient is a thin gRPC wrapper around Observer.GetFlows.
type HubbleClient struct {
	addr   string
	conn   *grpc.ClientConn
	client observerpb.ObserverClient
}

// NewHubble dials the relay at addr. Uses insecure transport — Hubble
// Relay is typically reached over an in-cluster Service without TLS
// termination in the default Cilium install. Production deployments with
// mTLS will need cert material wired in (Phase 2.1 follow-up).
func NewHubble(addr string) (*HubbleClient, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("dial hubble relay %s: %w", addr, err)
	}
	return &HubbleClient{
		addr:   addr,
		conn:   conn,
		client: observerpb.NewObserverClient(conn),
	}, nil
}

func (h *HubbleClient) Ping(ctx context.Context) (*observerpb.ServerStatusResponse, error) {
	return h.client.ServerStatus(ctx, &observerpb.ServerStatusRequest{})
}

// Stream opens a follow=true GetFlows stream and pushes every flow into
// out. Blocks until ctx is cancelled or the stream errors. The channel is
// never closed by this method — the caller controls its lifecycle.
func (h *HubbleClient) Stream(ctx context.Context, out chan<- *flowpb.Flow) error {
	req := &observerpb.GetFlowsRequest{Follow: true}
	stream, err := h.client.GetFlows(ctx, req)
	if err != nil {
		return fmt.Errorf("open flows stream: %w", err)
	}
	slog.Info("hubble: streaming flows", slog.String("relay", h.addr))
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("recv flow: %w", err)
		}
		f := resp.GetFlow()
		if f == nil {
			continue
		}
		select {
		case out <- f:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (h *HubbleClient) Close() error {
	return h.conn.Close()
}
