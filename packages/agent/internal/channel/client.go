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
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

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

// bufferStatser is the optional buffer introspection the stall watchdog needs.
// buffer.Ring implements it; a stub that only implements PopBatch simply skips
// the watchdog (the type assertion in Run fails, no-op).
type bufferStatser interface {
	Stats() (collected, dropped uint64, current, capacity int)
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
// KubeProxyWatchEvent, KubeStreamData, KubeStreamAck). Send is safe
// to call concurrently from multiple handler goroutines — it
// serializes writes internally.
//
// HandleKubeRequest receives a context bound to the live stream — when
// Run() returns, the ctx is cancelled and any in-flight watches /
// requests on the apiserver wake up + exit cleanly.
type Handler interface {
	HandleHeartbeatAck(*agentv2.HeartbeatAck)
	HandleConfigUpdate(*agentv2.ConfigUpdate)
	// HandleDisconnect runs synchronously on the read loop. Returning
	// a non-nil error becomes the Run() return value; nil keeps the
	// generic "backend asked to disconnect" message.
	HandleDisconnect(*agentv2.Disconnect) error
	// HandleKubeRequest dispatches in a fresh goroutine — the read
	// loop must not block. The handler is responsible for replying via
	// Client.Send and correlating with requestID. ctx is cancelled
	// when the stream ends.
	HandleKubeRequest(ctx context.Context, client *Client, requestID string, req *agentv2.KubeProxyRequest)

	// Tunnel messages (Sprint A.5 §0.7-§0.9). Both methods MUST
	// return quickly — they run on the read loop and a slow handler
	// blocks all incoming traffic on this agent's bidi channel. The
	// proxy.Handler implementation routes each message to the
	// per-request_id tunnel session (started by an earlier
	// HandleKubeRequest with Upgrade headers) via a buffered chan;
	// the per-session goroutine does the actual work.
	HandleKubeStreamData(requestID string, data *agentv2.KubeStreamData)
	HandleKubeStreamAck(requestID string, ack *agentv2.KubeStreamAck)
}

// NoopHandler implements Handler with no-ops. Useful for the Sprint A.5
// commit 3 → 4 transition where the proxy isn't wired yet but the
// shipper still needs a non-nil handler value.
type NoopHandler struct{}

func (NoopHandler) HandleHeartbeatAck(*agentv2.HeartbeatAck)                                       {}
func (NoopHandler) HandleConfigUpdate(*agentv2.ConfigUpdate)                                       {}
func (NoopHandler) HandleDisconnect(*agentv2.Disconnect) error                                     { return nil }
func (NoopHandler) HandleKubeRequest(context.Context, *Client, string, *agentv2.KubeProxyRequest) {}
func (NoopHandler) HandleKubeStreamData(string, *agentv2.KubeStreamData)                          {}
func (NoopHandler) HandleKubeStreamAck(string, *agentv2.KubeStreamAck)                            {}

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

	// flushInFlightNanos is the unix-nano when the current metric flush's
	// stream.Send began, or 0 when no flush is in flight. The stall watchdog
	// reads it to detect a Send WEDGED on backend backpressure — a Send that
	// never returns (the slow-flush log can't catch that; only a live monitor
	// can). Diagnostic instrumentation for finding #11.
	flushInFlightNanos atomic.Int64
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

	// Stall watchdog — forces a reconnect when the buffer stays saturated
	// because stream.Send is wedged on backend backpressure. The transport
	// keepalive can't catch this (the connection is alive, only the data stream
	// is stuck), so without this the agent silently drops until a manual
	// restart. See docs/11-agent-shipper-backpressure-silent-drop.md.
	if st, ok := c.samples.(bufferStatser); ok {
		slog.Info("stall watchdog started",
			slog.Duration("interval", stallWatchdogInterval),
			slog.Duration("timeout", stallWatchdogTimeout),
		)
		go c.stallWatchdog(streamCtx, streamCancel, st)
	} else {
		slog.Error("stall watchdog NOT started — sample buffer has no Stats(); backpressure/saturation will go UNDETECTED",
			slog.String("samples_type", fmt.Sprintf("%T", c.samples)),
		)
	}

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
			// The ctx propagates streamCancel — when the stream ends,
			// in-flight kube calls / watches wake up and exit.
			rid := msg.GetRequestId()
			go c.handler.HandleKubeRequest(streamCtx, c, rid, k.KubeRequest)
		case *agentv2.BackendMessage_KubeStreamData:
			// Tunnel data inbound (backend→agent direction). MUST be
			// dispatched synchronously from the read loop — the
			// handler routes to a buffered chan; reordering across
			// messages on the same request_id would corrupt exec
			// streams.
			c.handler.HandleKubeStreamData(msg.GetRequestId(), k.KubeStreamData)
		case *agentv2.BackendMessage_KubeStreamAck:
			// Backend ack-ing bytes the agent sent. Routes to the
			// per-session credit tracker — no goroutine spawn here.
			c.handler.HandleKubeStreamAck(msg.GetRequestId(), k.KubeStreamAck)
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
	// The read loop only breaks on io.EOF — the BACKEND closed the stream (a
	// network drop, a backend restart, or the backend's stuck-agent force-close
	// that EXPECTS us to reconnect). Return a non-nil error so the shipper's
	// reconnect loop rebuilds the session; it stops only when the agent's own ctx
	// is cancelled (a real shutdown). Returning nil here was the ROOT CAUSE of
	// finding #11: a backend-closed stream made the agent stop shipping FOREVER
	// (the backend's "force-closing channel to trigger reconnect" backfired — the
	// agent treated the close as a clean shutdown instead of reconnecting), and
	// with the shipper stopped there was no session and no watchdog when the
	// buffer later saturated.
	if ctx.Err() != nil {
		return nil // agent is shutting down — clean stop, no reconnect
	}
	return fmt.Errorf("backend closed the stream (EOF) — reconnecting")
}

// Stall-watchdog + slow-flush tunables (vars so tests can shrink them).
var (
	stallWatchdogInterval = 10 * time.Second
	// How long the buffer may stay saturated + dropping before we force a
	// reconnect. Long enough to ride out a brief collector burst, short enough
	// that a wedged Send doesn't bleed data for minutes.
	stallWatchdogTimeout = 45 * time.Second
	// A healthy metric flush returns in milliseconds; a multi-second one means
	// the backend is reading slowly — the precursor to a full stall.
	slowFlushThreshold = 3 * time.Second
)

// stallWatchdog forces a stream reconnect when the buffer stays saturated AND
// dropping past stallWatchdogTimeout — the signature of a stream.Send blocked on
// gRPC flow-control backpressure. Cancelling the stream ctx unblocks the wedged
// Send → writeLoop returns → the shipper builds a fresh session with a fresh
// flow-control window. Loud logs so operators aren't blind (finding #11).
func (c *Client) stallWatchdog(ctx context.Context, cancel context.CancelFunc, st bufferStatser) {
	ticker := time.NewTicker(stallWatchdogInterval)
	defer ticker.Stop()
	_, lastDropped, _, _ := st.Stats()
	var saturatedSince time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, dropped, current, capacity := st.Stats()
			dropping := dropped > lastDropped
			lastDropped = dropped
			// Diagnostic (finding #11): log exactly what the watchdog sees each
			// tick, plus how long the current metric flush's Send has been in
			// flight — a climbing flush_in_flight = a Send WEDGED on backpressure
			// (the backend stopped reading this stream while the conn stays alive).
			var flushInFlight time.Duration
			if fn := c.flushInFlightNanos.Load(); fn != 0 {
				flushInFlight = time.Since(time.Unix(0, fn))
			}
			slog.Info("stall watchdog tick",
				slog.Int("current", current),
				slog.Int("capacity", capacity),
				slog.Uint64("dropped_total", dropped),
				slog.Bool("dropping", dropping),
				slog.Bool("saturated", capacity > 0 && current >= capacity),
				slog.Duration("flush_in_flight", flushInFlight.Round(time.Second)),
			)
			if flushInFlight > slowFlushThreshold {
				slog.Warn("metric flush WEDGED — stream.Send has not returned; the backend is not reading this agent's stream",
					slog.Duration("wedged_for", flushInFlight.Round(time.Second)),
				)
			}
			if capacity > 0 && current >= capacity && dropping {
				if saturatedSince.IsZero() {
					saturatedSince = time.Now()
					slog.Warn("metric buffer saturated — shipper stalled on backend backpressure; will force reconnect if it persists",
						slog.Int("current", current),
						slog.Int("capacity", capacity),
						slog.Uint64("dropped_total", dropped),
					)
					continue
				}
				if time.Since(saturatedSince) >= stallWatchdogTimeout {
					slog.Error("metric buffer saturated + dropping too long — forcing stream reconnect (backpressure watchdog)",
						slog.Duration("stalled_for", time.Since(saturatedSince).Round(time.Second)),
						slog.Uint64("dropped_total", dropped),
					)
					cancel() // unblocks the wedged Send → session ends → shipper reconnects
					return
				}
			} else if !saturatedSince.IsZero() {
				slog.Info("metric buffer recovered — shipper draining again",
					slog.Int("current", current),
				)
				saturatedSince = time.Time{}
			}
		}
	}
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
	sanitizeSamples(samples)
	start := time.Now()
	// Mark the Send in-flight so the watchdog can see a wedged (never-returning)
	// Send. Cleared to 0 after Send returns.
	c.flushInFlightNanos.Store(start.UnixNano())
	err := c.sendOnStream(stream, &agentv2.AgentMessage{
		Kind: &agentv2.AgentMessage_Metrics{
			Metrics: &agentv2.MetricBatch{
				AgentId: c.AgentID(),
				SentAt:  timestamppb.Now(),
				Samples: samples,
			},
		},
	})
	c.flushInFlightNanos.Store(0)
	// Surface backpressure early: a healthy flush returns in milliseconds; a
	// multi-second send means the backend is reading slowly (precursor to the
	// full stall the watchdog force-reconnects on).
	if d := time.Since(start); d > slowFlushThreshold {
		slog.Warn("slow metric flush — backend backpressure",
			slog.Duration("send_duration", d.Round(time.Millisecond)),
			slog.Int("batch", len(samples)),
		)
	}
	return err
}

// sendOnStream serializes Send across goroutines. Used by writeLoop and
// by Send (the public method) — same lock.
func (c *Client) sendOnStream(stream agentv2.AgentChannel_ChannelClient, msg *agentv2.AgentMessage) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	return stream.Send(msg)
}

// sanitizeSamples rewrites any invalid-UTF-8 bytes (with U+FFFD) in a sample's
// metric_name AND its label keys/values before the batch hits the wire. A single
// invalid byte in ANY proto3 string field fails the gRPC marshal of the whole
// MetricBatch and tears down the AgentChannel session; because the bad sample
// stays buffered, every reconnect re-sends it and the agent never recovers.
// metric_name is a dedicated field (proto #2) separate from the labels map (#4) —
// both must be scrubbed. External sources (pod metadata, kubelet/cadvisor fields,
// the customer's Prometheus metric names) can carry arbitrary bytes, so this is
// the last line of defense across every collector. The offender is logged.
func sanitizeSamples(samples []*agentv2.Sample) {
	for _, s := range samples {
		if s == nil {
			continue
		}
		if !utf8.ValidString(s.MetricName) {
			slog.Warn("agent: sanitized invalid UTF-8 in sample metric_name",
				slog.String("metric_name", fmt.Sprintf("%q", s.MetricName)))
			s.MetricName = strings.ToValidUTF8(s.MetricName, "�")
		}
		bad := false
		for k, v := range s.Labels {
			if !utf8.ValidString(k) || !utf8.ValidString(v) {
				bad = true
				break
			}
		}
		if !bad {
			continue
		}
		clean := make(map[string]string, len(s.Labels))
		logged := false
		for k, v := range s.Labels {
			ck := strings.ToValidUTF8(k, "�")
			cv := strings.ToValidUTF8(v, "�")
			if (ck != k || cv != v) && !logged {
				slog.Warn("agent: sanitized invalid UTF-8 in sample label",
					slog.String("metric", s.MetricName),
					slog.String("key", fmt.Sprintf("%q", k)),
					slog.String("value", fmt.Sprintf("%q", v)))
				logged = true
			}
			clean[ck] = cv
		}
		s.Labels = clean
	}
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
