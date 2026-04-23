// Package flows reads pod-to-pod flow data from Cilium Hubble Relay and
// aggregates it into the unified pod_flow_* metric schema.
//
// This is the Level 2 traffic observability source from SPEC §2.1 — no
// service mesh required, no agent modifications needed. Detection is
// by env var for the walking skeleton; automatic service discovery
// lands in a follow-up commit.
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
// Relay is typically reached over an in-cluster service or port-forward
// without TLS termination in dev. Production deployments with mTLS will
// need additional wiring.
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

// Ping verifies the relay is reachable and returns its version info.
func (h *HubbleClient) Ping(ctx context.Context) (*observerpb.ServerStatusResponse, error) {
	return h.client.ServerStatus(ctx, &observerpb.ServerStatusRequest{})
}

// Stream opens a follow=true GetFlows stream and pushes every flow into
// out. Blocks until ctx is cancelled or the stream errors.
//
// The channel is never closed by this method — the caller controls its
// lifecycle so multiple producers can share it if needed.
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
