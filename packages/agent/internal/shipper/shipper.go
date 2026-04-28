// Package shipper owns the gRPC connection to the backend and runs the
// forever-loop that drives the AgentChannel bidi stream.
//
// Sprint A.5 wire change: the v1 unary Register + server-streaming
// StreamMetrics + unary Heartbeat are gone. Everything multiplexes on
// a single bidi `Channel(stream AgentMessage) returns (stream
// BackendMessage)`. The shipper:
//
//  1. Opens the bidi stream.
//  2. Sends Hello, waits for Welcome (replaces v1 Register).
//  3. Spawns a writer goroutine that drains the buffer into Metrics
//     batches and emits Heartbeats periodically.
//  4. Drains incoming BackendMessages on the main loop. HeartbeatAck
//     is observational; KubeProxyRequest dispatch lands in commit 4 of
//     this sprint.
//
// Reconnect: exponential backoff 1s → 60s on session end. Each new
// session re-issues Hello.
package shipper

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"runtime"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/kubebolt/kubebolt/packages/agent/internal/buffer"
	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"
)

type Shipper struct {
	backendURL   string
	buf          *buffer.Ring
	nodeName     string
	agentVersion string
	batchSize    int
	auth         AuthOptions

	// Populated on every successful Hello → Welcome handshake.
	agentID string
}

// Option mutates a Shipper at construction time. Keeps New
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
// handshake. Empty until the first connection succeeds.
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

// runSession opens a fresh AgentChannel stream, completes the handshake,
// drives Heartbeat + Metrics on a writer goroutine, and reads the
// backend's responses on the main loop until the stream errors or ctx
// is cancelled.
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

	client := agentv2.NewAgentChannelClient(conn)

	streamCtx, streamCancel := context.WithCancel(ctx)
	defer streamCancel()
	stream, err := client.Channel(streamCtx)
	if err != nil {
		return fmt.Errorf("open channel: %w", err)
	}

	if err := stream.Send(&agentv2.AgentMessage{
		Kind: &agentv2.AgentMessage_Hello{
			Hello: &agentv2.Hello{
				NodeName:         s.nodeName,
				KernelVersion:    runtime.GOOS + "/" + runtime.GOARCH,
				ContainerRuntime: "phaseB",
				CgroupVersion:    "n/a",
				KubeletVersion:   "n/a",
				AgentVersion:     s.agentVersion,
				Capabilities:     []string{"metrics"},
			},
		},
	}); err != nil {
		return fmt.Errorf("send hello: %w", err)
	}

	// Wait for Welcome.
	first, err := stream.Recv()
	if err != nil {
		return fmt.Errorf("recv welcome: %w", err)
	}
	welcome := first.GetWelcome()
	if welcome == nil {
		return fmt.Errorf("expected Welcome, got %T", first.Kind)
	}
	s.agentID = welcome.GetAgentId()
	slog.Info("registered",
		slog.String("agent_id", s.agentID),
		slog.String("cluster_id", welcome.GetClusterId()),
	)

	// Writer: drains buffer + emits heartbeats. Errors propagate via
	// streamCancel which makes Recv() return below.
	writerErr := make(chan error, 1)
	go func() {
		writerErr <- s.writeLoop(streamCtx, stream)
	}()

	// Reader: drains BackendMessages until EOF / error / ctx done.
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			streamCancel()
			<-writerErr
			return fmt.Errorf("recv: %w", err)
		}
		switch k := msg.Kind.(type) {
		case *agentv2.BackendMessage_HeartbeatAck:
			// Observational only. We could track liveness against the
			// last-seen timestamp here in a future iteration.
			_ = k
		case *agentv2.BackendMessage_Disconnect:
			streamCancel()
			<-writerErr
			return fmt.Errorf("backend asked to disconnect: %s", k.Disconnect.GetReason())
		case *agentv2.BackendMessage_ConfigUpdate:
			// Hot-reload lands in a future commit; for now log + drop.
			slog.Debug("ignoring ConfigUpdate (not yet handled)")
		case *agentv2.BackendMessage_KubeRequest:
			// Sprint A.5 commit 4 wires KubeAPIProxy to handle these.
			// Until then, the agent doesn't advertise the "kube-proxy"
			// capability in Hello, so the backend won't issue these.
			slog.Warn("dropped unexpected KubeRequest (kube-proxy capability not advertised)")
		case *agentv2.BackendMessage_Welcome:
			// A second Welcome on an established stream is a protocol
			// violation. Bail loud.
			streamCancel()
			<-writerErr
			return fmt.Errorf("received second Welcome on established stream")
		}
	}

	streamCancel()
	<-writerErr
	return nil
}

// writeLoop drains the buffer into Metrics batches every second and
// emits a Heartbeat every 30s. Returns when ctx is cancelled or the
// stream send errors.
func (s *Shipper) writeLoop(ctx context.Context, stream agentv2.AgentChannel_ChannelClient) error {
	flushTicker := time.NewTicker(time.Second)
	defer flushTicker.Stop()
	heartbeatTicker := time.NewTicker(30 * time.Second)
	defer heartbeatTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			_ = stream.CloseSend()
			return nil
		case <-flushTicker.C:
			if err := s.flushOnce(stream); err != nil {
				return err
			}
		case <-heartbeatTicker.C:
			if err := stream.Send(&agentv2.AgentMessage{
				Kind: &agentv2.AgentMessage_Heartbeat{
					Heartbeat: &agentv2.Heartbeat{SentAt: timestamppb.Now()},
				},
			}); err != nil {
				return fmt.Errorf("send heartbeat: %w", err)
			}
		}
	}
}

// flushOnce drains one batch (if any) from the buffer and sends it.
// No-op when the buffer is empty. Unlike v1, there's no per-batch ack
// — gRPC stream delivery + the outer reader loop handle errors.
func (s *Shipper) flushOnce(stream agentv2.AgentChannel_ChannelClient) error {
	samples := s.buf.PopBatch(s.batchSize)
	if len(samples) == 0 {
		return nil
	}
	if err := stream.Send(&agentv2.AgentMessage{
		Kind: &agentv2.AgentMessage_Metrics{
			Metrics: &agentv2.MetricBatch{
				AgentId: s.agentID,
				SentAt:  timestamppb.Now(),
				Samples: samples,
			},
		},
	}); err != nil {
		return fmt.Errorf("send batch: %w", err)
	}
	slog.Debug("batch flushed", slog.Int("samples", len(samples)))
	return nil
}
