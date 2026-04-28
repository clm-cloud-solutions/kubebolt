// Package shipper owns the gRPC connection to the backend and runs the
// forever-loop that drains the buffer into the StreamMetrics stream.
//
// Design choices:
//   - One connection for the whole process; recreated on disconnect.
//   - Exponential backoff 1s -> 60s cap between reconnect attempts.
//   - Register is called once per session (each reconnect = new agent_id).
//   - The ship loop polls the buffer every 1s. Low-latency enough for
//     Phase B; Phase C can switch to a condvar-based wait.
package shipper

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/kubebolt/kubebolt/packages/agent/internal/buffer"
	agentv1 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v1"
)

type Shipper struct {
	backendURL   string
	buf          *buffer.Ring
	nodeName     string
	agentVersion string
	batchSize    int
	auth         AuthOptions

	// Populated on every successful Register.
	agentID string
}

// Option mutates a Shipper at construction time. Used to keep New
// backward-compatible while adding new optional dependencies.
type Option func(*Shipper)

// WithAuth attaches credentials to the shipper. The zero AuthOptions
// (no Mode, no TLS) keeps the legacy plaintext-no-token behavior.
func WithAuth(opts AuthOptions) Option {
	return func(s *Shipper) { s.auth = opts }
}

func New(backendURL, nodeName, agentVersion string, buf *buffer.Ring, opts ...Option) *Shipper {
	s := &Shipper{
		backendURL:   backendURL,
		buf:          buf,
		nodeName:     nodeName,
		agentVersion: agentVersion,
		batchSize:    500,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// AgentID is the id assigned by the backend on the latest successful
// Register. Empty until the first connection succeeds.
func (s *Shipper) AgentID() string { return s.agentID }

// Run owns the reconnect loop and returns only when ctx is cancelled.
func (s *Shipper) Run(ctx context.Context) {
	backoff := time.Second
	const backoffMax = 60 * time.Second

	for {
		if ctx.Err() != nil {
			return
		}
		err := s.runSession(ctx)
		if err == nil || ctx.Err() != nil {
			return
		}
		slog.Warn("shipper session ended, will reconnect",
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
	}
}

// runSession opens a fresh connection, registers, and drains the buffer
// until the stream errors or ctx is cancelled.
func (s *Shipper) runSession(ctx context.Context) error {
	slog.Info("dialing backend",
		slog.String("addr", s.backendURL),
		slog.Bool("tls", s.auth.TLSEnabled),
		slog.String("auth_mode", string(s.auth.Mode)),
	)
	transport, err := BuildTransportCredentials(s.auth)
	if err != nil {
		return fmt.Errorf("transport credentials: %w", err)
	}
	dialOpts := []grpc.DialOption{grpc.WithTransportCredentials(transport)}
	if creds := NewTokenCreds(s.auth); creds != nil {
		dialOpts = append(dialOpts, grpc.WithPerRPCCredentials(creds))
	}
	conn, err := grpc.NewClient(s.backendURL, dialOpts...)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	client := agentv1.NewAgentIngestClient(conn)

	regCtx, regCancel := context.WithTimeout(ctx, 10*time.Second)
	regResp, err := client.Register(regCtx, &agentv1.RegisterRequest{
		NodeName:         s.nodeName,
		KernelVersion:    runtime.GOOS + "/" + runtime.GOARCH,
		ContainerRuntime: "phaseB",
		CgroupVersion:    "n/a",
		KubeletVersion:   "n/a",
		AgentVersion:     s.agentVersion,
	})
	regCancel()
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}
	s.agentID = regResp.GetAgentId()
	slog.Info("registered",
		slog.String("agent_id", s.agentID),
		slog.String("cluster_id", regResp.GetClusterId()),
	)

	stream, err := client.StreamMetrics(ctx)
	if err != nil {
		return fmt.Errorf("open stream: %w", err)
	}

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			_ = stream.CloseSend()
			return nil
		case <-ticker.C:
			if err := s.flushOnce(stream); err != nil {
				return err
			}
		}
	}
}

// flushOnce drains one batch (if any) from the buffer and sends it.
// No-op when the buffer is empty.
func (s *Shipper) flushOnce(stream agentv1.AgentIngest_StreamMetricsClient) error {
	samples := s.buf.PopBatch(s.batchSize)
	if len(samples) == 0 {
		return nil
	}
	if err := stream.Send(&agentv1.MetricBatch{
		AgentId: s.agentID,
		SentAt:  timestamppb.Now(),
		Samples: samples,
	}); err != nil {
		return fmt.Errorf("send batch: %w", err)
	}
	ack, err := stream.Recv()
	if err != nil {
		return fmt.Errorf("recv ack: %w", err)
	}
	if ack.GetSamplesRejected() > 0 {
		slog.Warn("backend rejected samples",
			slog.Uint64("rejected", uint64(ack.GetSamplesRejected())),
			slog.Any("reasons", ack.GetRejectionReasons()),
		)
	}
	slog.Debug("batch flushed",
		slog.Int("samples", len(samples)),
		slog.Uint64("accepted", uint64(ack.GetSamplesAccepted())),
	)
	return nil
}
