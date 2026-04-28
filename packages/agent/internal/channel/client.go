// Package channel owns the agent-side state machine for one AgentChannel
// session: open the stream, send Hello, wait for Welcome, then drain
// BackendMessages on the read loop while a writer goroutine drains the
// local sample buffer into Metrics batches and emits Heartbeats.
//
// The package is deliberately decoupled from auth + reconnect logic.
// Callers (shipper) build a *grpc.ClientConn with auth credentials,
// then construct one Client per session. Reconnect = build a new
// Client.
//
// Sprint A.5 commit 4 plugs the K8s API proxy in via the Handler
// interface — KubeRequest dispatches to KubeAPIProxy.HandleRequest /
// HandleWatch, and the responses go back over the same stream via
// Client.Send.
package channel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"
)

// HelloInfo is the metadata the agent reports at handshake time. The
// Client builds a Hello message from it on Run().
type HelloInfo struct {
	NodeName         string
	AgentVersion     string
	KernelVersion    string
	ContainerRuntime string
	CgroupVersion    string
	KubeletVersion   string
	ClusterHint      string
	Capabilities     []string
	Labels           map[string]string
}

// SamplesProvider is what the Client polls to drain the agent's local
// buffer into MetricBatch payloads. Decoupled from the concrete
// buffer.Ring type so tests can stub it.
type SamplesProvider interface {
	PopBatch(n int) []*agentv2.Sample
}

// Handler hooks the Client up to the rest of the agent. Each method
// fires for one BackendMessage kind; nil-method behavior:
//
//   - HandleHeartbeatAck    nil → ack is silently dropped
//   - HandleConfigUpdate    nil → config update is ignored
//   - HandleDisconnect      nil → returns nil; Run still terminates
//   - HandleKubeRequest     nil → request is logged + dropped (the
//     "kube-proxy" capability shouldn't have been advertised in
//     Hello if the agent can't service requests)
//
// Implementations may invoke Client.Send to reply (KubeProxyResponse,
// KubeProxyWatchEvent). Send is safe to call concurrently from
// multiple handler goroutines — it serializes writes internally.
type Handler interface {
	HandleHeartbeatAck(*agentv2.HeartbeatAck)
	HandleConfigUpdate(*agentv2.ConfigUpdate)
	// HandleDisconnect runs synchronously on the read loop. Returning
	// a non-nil error becomes the Run() return value; nil keeps the
	// generic "backend asked to disconnect" message.
	HandleDisconnect(*agentv2.Disconnect) error
	// HandleKubeRequest dispatches in a fresh goroutine — the read
	// loop must not block. The handler is responsible for replying via
	// Client.Send and correlating with requestID.
	HandleKubeRequest(client *Client, requestID string, req *agentv2.KubeProxyRequest)
}

// NoopHandler implements Handler with no-ops. Useful for the Sprint A.5
// commit 3 → 4 transition where the proxy isn't wired yet but the
// shipper still needs a non-nil handler value.
type NoopHandler struct{}

func (NoopHandler) HandleHeartbeatAck(*agentv2.HeartbeatAck)                       {}
func (NoopHandler) HandleConfigUpdate(*agentv2.ConfigUpdate)                       {}
func (NoopHandler) HandleDisconnect(*agentv2.Disconnect) error                     { return nil }
func (NoopHandler) HandleKubeRequest(*Client, string, *agentv2.KubeProxyRequest)   {}

// Client owns one bidi stream with the backend.
type Client struct {
	conn    *grpc.ClientConn
	samples SamplesProvider
	hello   HelloInfo
	handler Handler

	// Tunables. Defaults in NewClient; tests override directly.
	BatchSize      int
	FlushEvery     time.Duration
	HeartbeatEvery time.Duration

	// Set after the handshake completes.
	mu        sync.RWMutex
	agentID   string
	clusterID string

	// Live stream while Run is executing. nil before/after.
	streamMu sync.RWMutex
	stream   agentv2.AgentChannel_ChannelClient

	// gRPC client streams aren't safe for concurrent Send. Serialize.
	sendMu sync.Mutex
}

// NewClient builds a Client for one session. handler may be nil — the
// Client falls back to NoopHandler.
func NewClient(conn *grpc.ClientConn, samples SamplesProvider, hello HelloInfo, handler Handler) *Client {
	if handler == nil {
		handler = NoopHandler{}
	}
	return &Client{
		conn:           conn,
		samples:        samples,
		hello:          hello,
		handler:        handler,
		BatchSize:      500,
		FlushEvery:     time.Second,
		HeartbeatEvery: 30 * time.Second,
	}
}

// AgentID returns the id assigned by the backend at handshake. Empty
// before Welcome arrives.
func (c *Client) AgentID() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.agentID
}

// ClusterID returns the canonical cluster identifier from Welcome.
func (c *Client) ClusterID() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.clusterID
}

// ErrNotRunning is returned by Send when no session is active. Handlers
// that race the close should treat it as terminal.
var ErrNotRunning = errors.New("channel client: not running")

// Send writes one AgentMessage to the active stream. Safe for concurrent
// callers (KubeProxy handlers spawn goroutines that all funnel through
// here). Returns ErrNotRunning when no session is active.
func (c *Client) Send(msg *agentv2.AgentMessage) error {
	c.streamMu.RLock()
	s := c.stream
	c.streamMu.RUnlock()
	if s == nil {
		return ErrNotRunning
	}
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	return s.Send(msg)
}

// Run owns the bidi stream until ctx ends or the stream errors. Returns
// nil on EOF / ctx-cancelled, error otherwise. One Client per session —
// the caller (shipper) handles reconnect by building a new Client.
func (c *Client) Run(ctx context.Context) error {
	grpcClient := agentv2.NewAgentChannelClient(c.conn)
	streamCtx, streamCancel := context.WithCancel(ctx)
	defer streamCancel()

	stream, err := grpcClient.Channel(streamCtx)
	if err != nil {
		return fmt.Errorf("open channel: %w", err)
	}

	c.streamMu.Lock()
	c.stream = stream
	c.streamMu.Unlock()
	defer func() {
		c.streamMu.Lock()
		c.stream = nil
		c.streamMu.Unlock()
	}()

	if err := c.sendOnStream(stream, &agentv2.AgentMessage{
		Kind: &agentv2.AgentMessage_Hello{Hello: helloProto(c.hello)},
	}); err != nil {
		return fmt.Errorf("send hello: %w", err)
	}

	first, err := stream.Recv()
	if err != nil {
		return fmt.Errorf("recv welcome: %w", err)
	}
	welcome := first.GetWelcome()
	if welcome == nil {
		return fmt.Errorf("expected Welcome, got %T", first.GetKind())
	}
	c.mu.Lock()
	c.agentID = welcome.GetAgentId()
	c.clusterID = welcome.GetClusterId()
	c.mu.Unlock()
	slog.Info("channel registered",
		slog.String("agent_id", c.agentID),
		slog.String("cluster_id", c.clusterID),
	)

	// Writer goroutine — drains buffer + heartbeat ticks. Errors in
	// Send propagate via streamCancel(); the reader loop wakes up with
	// ErrCanceled and returns the upstream error.
	writerErr := make(chan error, 1)
	go func() { writerErr <- c.writeLoop(streamCtx, stream) }()

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
		switch k := msg.GetKind().(type) {
		case *agentv2.BackendMessage_HeartbeatAck:
			c.handler.HandleHeartbeatAck(k.HeartbeatAck)
		case *agentv2.BackendMessage_ConfigUpdate:
			c.handler.HandleConfigUpdate(k.ConfigUpdate)
		case *agentv2.BackendMessage_KubeRequest:
			// Dispatch in a goroutine so the read loop keeps draining;
			// HandleKubeRequest is responsible for replying via Send.
			rid := msg.GetRequestId()
			go c.handler.HandleKubeRequest(c, rid, k.KubeRequest)
		case *agentv2.BackendMessage_Disconnect:
			hErr := c.handler.HandleDisconnect(k.Disconnect)
			streamCancel()
			<-writerErr
			if hErr != nil {
				return hErr
			}
			return fmt.Errorf("backend asked to disconnect: %s", k.Disconnect.GetReason())
		case *agentv2.BackendMessage_Welcome:
			streamCancel()
			<-writerErr
			return fmt.Errorf("received second Welcome on established stream")
		}
	}

	streamCancel()
	<-writerErr
	return nil
}

// writeLoop drains the buffer into Metrics batches every FlushEvery and
// emits a Heartbeat every HeartbeatEvery. Returns when ctx ends or a
// Send fails.
func (c *Client) writeLoop(ctx context.Context, stream agentv2.AgentChannel_ChannelClient) error {
	flushTicker := time.NewTicker(c.FlushEvery)
	defer flushTicker.Stop()
	heartbeatTicker := time.NewTicker(c.HeartbeatEvery)
	defer heartbeatTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			_ = stream.CloseSend()
			return nil
		case <-flushTicker.C:
			if err := c.flushOnce(stream); err != nil {
				return err
			}
		case <-heartbeatTicker.C:
			if err := c.sendOnStream(stream, &agentv2.AgentMessage{
				Kind: &agentv2.AgentMessage_Heartbeat{
					Heartbeat: &agentv2.Heartbeat{SentAt: timestamppb.Now()},
				},
			}); err != nil {
				return fmt.Errorf("send heartbeat: %w", err)
			}
		}
	}
}

// flushOnce drains one batch (if any) and sends it.
func (c *Client) flushOnce(stream agentv2.AgentChannel_ChannelClient) error {
	if c.samples == nil {
		return nil
	}
	samples := c.samples.PopBatch(c.BatchSize)
	if len(samples) == 0 {
		return nil
	}
	return c.sendOnStream(stream, &agentv2.AgentMessage{
		Kind: &agentv2.AgentMessage_Metrics{
			Metrics: &agentv2.MetricBatch{
				AgentId: c.AgentID(),
				SentAt:  timestamppb.Now(),
				Samples: samples,
			},
		},
	})
}

// sendOnStream serializes Send across goroutines. Used by writeLoop and
// by Send (the public method) — same lock.
func (c *Client) sendOnStream(stream agentv2.AgentChannel_ChannelClient, msg *agentv2.AgentMessage) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	return stream.Send(msg)
}

func helloProto(h HelloInfo) *agentv2.Hello {
	return &agentv2.Hello{
		NodeName:         h.NodeName,
		AgentVersion:     h.AgentVersion,
		KernelVersion:    h.KernelVersion,
		ContainerRuntime: h.ContainerRuntime,
		CgroupVersion:    h.CgroupVersion,
		KubeletVersion:   h.KubeletVersion,
		ClusterHint:      h.ClusterHint,
		Capabilities:     h.Capabilities,
		Labels:           h.Labels,
	}
}
