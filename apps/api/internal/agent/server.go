// Package agent hosts the backend-side of the kubebolt-agent wire contract:
// the gRPC AgentChannel service (Sprint A.5) and its metrics storage writer.
//
// Wire format: a single bidi RPC `Channel(stream AgentMessage) returns
// (stream BackendMessage)` multiplexes EVERYTHING the agent and backend
// exchange — Hello/Welcome handshake, heartbeat, metrics push, and (in
// commit 5+ of this sprint) the K8s API proxy (kube_request /
// kube_response / kube_event).
//
// Sprint A's auth + TLS + rate limiter from the interceptor (apps/api/
// internal/agent/auth_interceptor.go) all apply unchanged — they hook the
// gRPC service registration path, not the proto.
//
// Persistence (agents bucket, ULID identifiers) lands in Sprint B.
package agent

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/kubebolt/kubebolt/apps/api/internal/agent/channel"
	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"
)

// MetricsWriter is the contract between the ingest server and whatever TSDB
// is underneath. VMWriter implements it; tests use a capture stub.
type MetricsWriter interface {
	Write(ctx context.Context, samples []*agentv2.Sample) error
}

// Server implements agentv2.AgentChannelServer.
//
// registry is optional. When non-nil the handler registers each connected
// agent under its cluster_id so other parts of the backend (the
// AgentProxyTransport in commit 5+, admin REST handlers, etc.) can locate
// the live channel. nil keeps the legacy stand-alone behavior — useful
// for unit tests that exercise only the auth/proto path.
type Server struct {
	agentv2.UnimplementedAgentChannelServer
	writer   MetricsWriter
	registry *channel.AgentRegistry

	// clusterRegistrar bridges to cluster.Manager so agent-proxy
	// clusters can be added/removed in lockstep with agent
	// registration. nil when the wiring isn't enabled (e.g. unit
	// tests that exercise only the auth/proto path).
	clusterRegistrar ClusterRegistrar
	// autoRegisterClusters gates the auto-register behavior. Defaults
	// to false so single-cluster self-hosted setups don't surprise
	// operators with extra clusters appearing in the UI.
	autoRegisterClusters bool
}

// Option configures a Server. Functional-options pattern keeps NewServer
// backward-compatible while allowing the registry to be plugged in by
// main.go without breaking call sites that don't need it (tests).
type Option func(*Server)

// WithRegistry attaches the AgentRegistry the handler uses to track
// the lifecycle of each connected agent. nil is tolerated.
func WithRegistry(r *channel.AgentRegistry) Option {
	return func(s *Server) { s.registry = r }
}

// WithClusterRegistrar plugs the cluster.Manager-shaped registrar so
// the handler can call AddAgentProxyCluster / RemoveAgentProxyCluster
// in step with each agent's Hello / disconnect. nil tolerated.
func WithClusterRegistrar(r ClusterRegistrar) Option {
	return func(s *Server) { s.clusterRegistrar = r }
}

// WithAutoRegisterClusters toggles agent-proxy cluster auto-discovery.
// false (the default) means an agent that advertises the kube-proxy
// capability connects but its cluster does NOT appear in the manager
// — the operator must register it explicitly. true means every
// kube-proxy capable Hello triggers AddAgentProxyCluster.
func WithAutoRegisterClusters(enabled bool) Option {
	return func(s *Server) { s.autoRegisterClusters = enabled }
}

func NewServer(writer MetricsWriter, opts ...Option) *Server {
	s := &Server{writer: writer}
	for _, o := range opts {
		o(s)
	}
	return s
}

// streamSender adapts a server-side gRPC stream to the channel.Sender
// interface so the Agent can serialize all outbound writes through a
// single mutex. The underlying stream itself is NOT safe for
// concurrent Send; the mutex inside Agent.sendMu ensures serialization
// across the heartbeat-ack path (read loop) and the
// AgentProxyTransport.RoundTrip path.
type streamSender struct {
	stream agentv2.AgentChannel_ChannelServer
}

func (s streamSender) Send(msg *agentv2.BackendMessage) error {
	return s.stream.Send(msg)
}

// Channel handles the bidi stream from a single agent. Lifecycle:
//
//  1. Read first AgentMessage. MUST be Hello — anything else is a
//     protocol violation (auth was already validated by the interceptor,
//     but the protocol contract is still ours to enforce).
//  2. Resolve agent_id + cluster_id from the auth identity (Sprint A
//     commit 4) plus the node_name in Hello. Send Welcome.
//  3. Loop:
//     - Heartbeat       → log + reply HeartbeatAck.
//     - Metrics batch   → forward to MetricsWriter.
//     - kube_response / kube_event / stream_closed
//                       → unsolicited until commit 5 wires the proxy
//                         dispatcher; log at debug + drop.
//     - second Hello    → protocol violation, close the stream.
//  4. EOF or error → return.
func (s *Server) Channel(stream agentv2.AgentChannel_ChannelServer) error {
	ctx := stream.Context()
	id := auth.AgentIdentityFromContext(ctx)

	first, err := stream.Recv()
	if err == io.EOF {
		return nil
	}
	if err != nil {
		return err
	}
	hello := first.GetHello()
	if hello == nil {
		return status.Error(codes.InvalidArgument, "first message must be Hello")
	}

	agentID, clusterID := resolveAgentID(id, hello.GetNodeName())

	logAttrs := []any{
		slog.String("agent_id", agentID),
		slog.String("cluster_id", clusterID),
		slog.String("node_name", hello.GetNodeName()),
		slog.String("agent_version", hello.GetAgentVersion()),
		slog.String("kernel", hello.GetKernelVersion()),
		slog.String("runtime", hello.GetContainerRuntime()),
		slog.Any("capabilities", hello.GetCapabilities()),
	}
	if id != nil {
		logAttrs = append(logAttrs,
			slog.String("auth_mode", string(id.Mode)),
			slog.String("tenant_id", id.TenantID),
			slog.Bool("tls_verified", id.TLSVerified),
		)
	}
	slog.Info("agent registered", logAttrs...)

	// Build an Agent that wraps this stream as its Sender. All
	// outbound BackendMessages funnel through agent.Send so concurrent
	// writers (heartbeat ack on the read loop + AgentProxyTransport
	// from commit 5+) coordinate via a single mutex inside the Agent.
	registeredAgent := channel.NewAgent(clusterID, agentID, hello.GetNodeName(), id, streamSender{stream})

	// Defer ordering matters here. LIFO means later-registered defers
	// fire FIRST. The teardown chain we want at execution time:
	//   1. Unregister(agent)             — removes this agent from the registry
	//   2. agent.Close()                 — cancels in-flight RoundTrips
	//   3. RemoveAgentProxyCluster(...)  — only if no peers remain (count == 0)
	// To get that order we register them in reverse: cluster cleanup
	// FIRST (runs last), then Close, then Unregister.
	if maybeAutoRegisterCluster(s.clusterRegistrar, s.registry, s.autoRegisterClusters,
		clusterID, autoRegisterDisplayName(hello, clusterID), hello.GetCapabilities()) {
		defer maybeAutoUnregisterCluster(s.clusterRegistrar, s.registry, clusterID)
	}
	defer registeredAgent.Close()
	if s.registry != nil {
		if evicted := s.registry.Register(registeredAgent); evicted != nil {
			slog.Info("evicting prior agent for cluster",
				slog.String("cluster_id", clusterID),
				slog.String("evicted_agent_id", evicted.AgentID),
			)
			evicted.Close()
		}
		defer s.registry.Unregister(registeredAgent)
	}

	if err := registeredAgent.Send(&agentv2.BackendMessage{
		Kind: &agentv2.BackendMessage_Welcome{
			Welcome: &agentv2.Welcome{
				AgentId:   agentID,
				ClusterId: clusterID,
				Config: &agentv2.AgentConfig{
					SampleIntervalSeconds: 15,
					BatchSize:             500,
					BatchFlushSeconds:     5,
				},
			},
		},
	}); err != nil {
		return err
	}

	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}

		switch k := msg.Kind.(type) {
		case *agentv2.AgentMessage_Heartbeat:
			stats := k.Heartbeat.GetStats()
			if stats != nil {
				hbAttrs := []any{
					slog.String("agent_id", agentID),
					slog.Uint64("samples_sent", stats.GetSamplesSentTotal()),
					slog.Uint64("samples_dropped", stats.GetSamplesDroppedTotal()),
					slog.Uint64("buffer_size", stats.GetBufferSizeCurrent()),
				}
				if id != nil {
					hbAttrs = append(hbAttrs, slog.String("tenant_id", id.TenantID))
				}
				slog.Info("agent heartbeat", hbAttrs...)
			}
			if err := registeredAgent.Send(&agentv2.BackendMessage{
				Kind: &agentv2.BackendMessage_HeartbeatAck{
					HeartbeatAck: &agentv2.HeartbeatAck{ReceivedAt: timestamppb.Now()},
				},
			}); err != nil {
				return err
			}

		case *agentv2.AgentMessage_Metrics:
			batch := k.Metrics
			batchAttrs := []any{
				slog.String("agent_id", agentID),
				slog.Int("samples", len(batch.GetSamples())),
			}
			if id != nil {
				batchAttrs = append(batchAttrs, slog.String("tenant_id", id.TenantID))
			}
			slog.Info("received metric batch", batchAttrs...)
			if werr := s.writer.Write(ctx, batch.GetSamples()); werr != nil {
				// v1 surfaced rejections via IngestAck. v2 omits the ack —
				// the agent's buffer + heartbeat already give the operator
				// the signals they need; we just log here.
				slog.Error("metrics write failed", slog.String("error", werr.Error()))
			}

		case *agentv2.AgentMessage_KubeResponse,
			*agentv2.AgentMessage_KubeEvent,
			*agentv2.AgentMessage_StreamClosed,
			*agentv2.AgentMessage_KubeStreamData,
			*agentv2.AgentMessage_KubeStreamAck:
			// Route to the agent's Multiplexor. The AgentProxyTransport
			// (commit 5+) issues kube_requests and registers request_ids
			// in advance; without a matching slot, Deliver no-ops.
			//
			// KubeStreamData / KubeStreamAck (Sprint A.5 §0.7-§0.9
			// SPDY tunneling) ride the same dispatch — TunnelConn's
			// demuxLoop on the backend side reads them via the same
			// per-request-id slot. Missing this case is what caused
			// the "tunnel hangs after 101" bug observed during smoke
			// test: bytes from apiserver→backend never reached
			// TunnelConn, so SPDY framing on the backend stalled
			// waiting for SETTINGS frame forever.
			_ = k
			registeredAgent.Pending.Deliver(msg)

		case *agentv2.AgentMessage_Hello:
			return status.Error(codes.InvalidArgument, "Hello sent twice on the same stream")
		}
	}
}

// agentProxySuffix differentiates auto-registered agent-proxy
// entries from kubeconfig-backed contexts in the cluster dropdown
// when both happen to point at the same physical cluster (single-
// cluster self-hosted dev: laptop has both kubeconfig direct AND
// agent in the cluster). Without it the operator sees two entries
// titled identically and has to read the URL to tell them apart.
//
// The suffix is also a clear signal in the SaaS multi-cluster case:
// every cluster reached via agent-proxy carries the marker so an
// operator looking at a fleet listing knows which path serves a
// given cluster. Frontend can later replace this with a badge/icon
// driven by `source: "agent-proxy"`; until then the textual suffix
// is the cheapest disambiguator.
const agentProxySuffix = " (via agent)"

// autoRegisterDisplayName picks the friendly label for the
// agent-proxy cluster entry in the manager's listing. Today it
// honors a `kubebolt.io/cluster-name` label in Hello.Labels (set
// agent-side from KUBEBOLT_AGENT_CLUSTER_NAME) and falls back to
// the cluster_id itself. Either way the result carries the
// agentProxySuffix marker. Operators can always override via
// Manager.SetClusterDisplayName.
func autoRegisterDisplayName(hello *agentv2.Hello, clusterID string) string {
	base := clusterID
	if hello != nil {
		if name := hello.GetLabels()["kubebolt.io/cluster-name"]; name != "" {
			base = name
		}
	}
	return base + agentProxySuffix
}

// resolveAgentID returns a stable agent identifier. With an authenticated
// identity it derives sha256(tenant|cluster|node)[:16] so reconnects land
// on the same id without persistence (Sprint B replaces with a ULID stored
// in the agents bucket). Without an identity (auth disabled) it falls back
// to a fresh UUID.
func resolveAgentID(id *auth.AgentIdentity, nodeName string) (agentID, clusterID string) {
	if id == nil || id.Mode == auth.ModeDisabled || id.TenantID == "" {
		return uuid.NewString(), "local"
	}
	cluster := id.ClusterID
	if cluster == "" {
		cluster = "local"
	}
	return auth.DeriveAgentID(id.TenantID, cluster, nodeName), cluster
}

// ListenOptions configures Listen. Auth.Enforcement="" defaults to
// EnforcementDisabled with a warning at startup. TLS=nil runs plaintext.
type ListenOptions struct {
	Auth AuthConfig
	TLS  *TLSConfig
}

// Listen binds a gRPC listener at addr and serves AgentChannel on it
// until ctx is cancelled. Blocks until the server exits.
func Listen(ctx context.Context, addr string, srv *Server, opts ListenOptions) error {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("agent gRPC listen %s: %w", addr, err)
	}
	if opts.Auth.Enforcement == "" {
		opts.Auth.Enforcement = EnforcementDisabled
	}
	LogStartupMode(opts.Auth)

	serverOpts := []grpc.ServerOption{
		grpc.UnaryInterceptor(UnaryAuthInterceptor(opts.Auth)),
		grpc.StreamInterceptor(StreamAuthInterceptor(opts.Auth)),
	}
	if opts.TLS != nil && opts.TLS.Config != nil {
		serverOpts = append(serverOpts, grpc.Creds(credentials.NewTLS(opts.TLS.Config)))
		slog.Info("agent gRPC TLS enabled",
			slog.Bool("require_mtls", opts.TLS.RequireMTLS),
		)
	} else {
		slog.Warn("agent gRPC server running plaintext (no TLS configured)")
	}

	grpcSrv := grpc.NewServer(serverOpts...)
	agentv2.RegisterAgentChannelServer(grpcSrv, srv)
	slog.Info("agent gRPC server listening", slog.String("addr", addr))

	go func() {
		<-ctx.Done()
		slog.Info("agent gRPC server stopping")
		grpcSrv.GracefulStop()
	}()

	if err := grpcSrv.Serve(lis); err != nil {
		return fmt.Errorf("agent gRPC serve: %w", err)
	}
	return nil
}
