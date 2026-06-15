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
	"golang.org/x/mod/semver"
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

// MinAgentVersion is the lowest agent release that emits the Prom-canonical
// schema (Phase 1 of the Universal Data Plane Plan, commits 373ef20..9dc6dc6).
// Older agents still connect — fail-soft preserves visibility during a rolling
// chart upgrade — but the dashboards consult v1.0 metric/label names so they
// render empty for the cluster the legacy agent is reporting from. The
// warning logged on registration tells the operator exactly what to do.
const MinAgentVersion = "1.0.0"

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
	//
	// Spec #09 V2 — wrapped in a getter func so the value can be
	// resolved per-registration (hot-reload from the settings runtime).
	// nil means "fall back to false"; main.go plugs in a closure that
	// reads `settingsRuntime.IngestChannel().AgentAutoRegisterClusters`.
	autoRegisterClusters func() bool
	// selfClusterID is the kube-system namespace UID of the cluster
	// the backend itself runs in (when running in-cluster), as
	// discovered by DiscoverClusterID at boot. Empty when running
	// out-of-cluster (kubeconfig-on-disk dev path) or when in-cluster
	// discovery failed. When non-empty, agent-proxy auto-registration
	// short-circuits for any agent reporting a matching cluster_id,
	// so the backend's own cluster doesn't show up TWICE in the
	// UI selector (once as in-cluster, once as agent-proxy) — see
	// cluster-validation BUG-2.
	selfClusterID string
	// metrics records per-tenant stream + samples counters powering
	// the /admin/ingest-activity panel (spec #09 V2 Item 5b). nil
	// when WithGRPCIngestMetrics wasn't passed — all RecordX methods
	// nil-guard so call sites stay terse.
	metrics *GRPCIngestMetrics
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
//
// Static variant — boot-time decision. For hot-reload from a settings
// runtime, use WithAutoRegisterClustersFunc instead.
func WithAutoRegisterClusters(enabled bool) Option {
	return func(s *Server) {
		s.autoRegisterClusters = func() bool { return enabled }
	}
}

// WithAutoRegisterClustersFunc plugs a getter func that resolves the
// flag at each agent-registration call. Spec #09 V2 — lets main.go
// route the read through `settingsRuntime.IngestChannel()` so the
// UI can flip the toggle without restart. Operators can switch from
// "manual register" to "auto" the moment a fleet rollout starts.
func WithAutoRegisterClustersFunc(getter func() bool) Option {
	return func(s *Server) { s.autoRegisterClusters = getter }
}

// WithGRPCIngestMetrics plugs the Prometheus counter set that powers
// the /admin/ingest-activity panel. nil is tolerated — when not set,
// all metric record calls become no-ops via the GRPCIngestMetrics
// nil-receiver guards. Spec #09 V2 Item 5b.
func WithGRPCIngestMetrics(m *GRPCIngestMetrics) Option {
	return func(s *Server) { s.metrics = m }
}

// resolveAutoRegister centralizes the "is the flag enabled right now"
// check. nil getter → false (the safe default for the option-not-set
// case in tests + the auth-disabled boot path).
func (s *Server) resolveAutoRegister() bool {
	if s.autoRegisterClusters == nil {
		return false
	}
	return s.autoRegisterClusters()
}

// WithSelfClusterID configures the cluster_id the backend itself runs
// in (the kube-system namespace UID, as discovered by
// DiscoverClusterID at boot). Used by the auto-register path to skip
// any agent that reports the same cluster_id — that cluster is
// already exposed via the in-cluster kubeconfig context, so a second
// registration would duplicate the row in the UI selector.
//
// Empty (the default) gates the self-skip OFF — the auto-register
// path proceeds for every agent regardless of whether its cluster_id
// matches the backend's own. Pass the discovered UID explicitly when
// the backend runs in-cluster.
func WithSelfClusterID(id string) Option {
	return func(s *Server) { s.selfClusterID = id }
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

	agentID, clusterID := resolveAgentID(id, hello.GetNodeName(), hello.GetClusterHint(), agentRoleFromHello(hello))

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

	// Warn (don't reject) when the agent reports a version below the
	// minimum that emits the v1.0 Prom-canonical schema. Older agents
	// connect fine and ship samples, but those samples carry the legacy
	// label/metric names that the UI no longer queries — the operator's
	// dashboards will look "empty" until the helm chart is bumped.
	// Empty / unparseable AgentVersion is silent: not all clients set
	// it (test mocks, pre-Hello-version forks). semver wants a leading
	// "v" so we add one before comparing.
	if av := hello.GetAgentVersion(); av != "" {
		want := "v" + MinAgentVersion
		got := av
		if got[0] != 'v' {
			got = "v" + got
		}
		if semver.IsValid(got) && semver.Compare(got, want) < 0 {
			slog.Warn("agent below minimum version — legacy schema",
				slog.String("agent_id", agentID),
				slog.String("cluster_id", clusterID),
				slog.String("agent_version", av),
				slog.String("min_agent_version", MinAgentVersion),
				slog.String("hint", "upgrade kubebolt-agent helm chart to >=1.0.0; v0.x emits the legacy schema and dashboards will render empty"),
			)
		}
	}

	// Build an Agent that wraps this stream as its Sender. All
	// outbound BackendMessages funnel through agent.Send so concurrent
	// writers (heartbeat ack on the read loop + AgentProxyTransport
	// from commit 5+) coordinate via a single mutex inside the Agent.
	registeredAgent := channel.NewAgent(clusterID, agentID, hello.GetNodeName(), id, hello.GetCapabilities(), streamSender{stream})

	// Defer ordering matters here. LIFO means later-registered defers
	// fire FIRST. The teardown chain we want at execution time:
	//   1. Unregister(agent)             — removes this agent from the registry
	//   2. agent.Close()                 — cancels in-flight RoundTrips
	//   3. RemoveAgentProxyCluster(...)  — only if no peers remain (count == 0)
	// To get that order we register them in reverse: cluster cleanup
	// FIRST (runs last), then Close, then Unregister.
	if maybeAutoRegisterCluster(s.clusterRegistrar, s.registry, s.resolveAutoRegister(),
		clusterID, autoRegisterDisplayName(hello, clusterID), hello.GetCapabilities(), s.selfClusterID) {
		defer maybeAutoUnregisterCluster(s.clusterRegistrar, s.registry, clusterID)
	}
	defer registeredAgent.Close()

	// Send Welcome FIRST, before registering the agent in the registry.
	// Protocol contract: the agent's reader expects Welcome as the very
	// first BackendMessage and bails with a 1-minute backoff if anything
	// else (e.g. KubeRequest) arrives first. Once the agent is in the
	// registry it becomes visible to the kube_request multiplexor and
	// any in-flight backend request can be routed to it — if that
	// happens during the brief window before Welcome is sent, we lose
	// the channel. Sending Welcome before Register closes that window.
	// Visible symptom of the original ordering: a manager-triggered
	// connector retry firing on the FIRST agent's register sends
	// kube_requests through the multiplexor while the SECOND DaemonSet
	// pod is mid-handshake → its Recv loop sees KubeRequest before
	// Welcome → drops the channel for 60s. Reordering eliminates the
	// race entirely; defers below stay in their LIFO teardown order.
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

	if s.registry != nil {
		// Stash the Hello metadata so the registry can include it in
		// the persisted record. Has to happen BEFORE Register so the
		// upsert sees it. The registry clears it on Unregister.
		s.registry.SetHelloMeta(clusterID, agentID, channel.HelloMeta{
			Capabilities: hello.GetCapabilities(),
			DisplayName:  autoRegisterDisplayName(hello, clusterID),
			AgentVersion: hello.GetAgentVersion(),
		})
		if evicted := s.registry.Register(registeredAgent); evicted != nil {
			slog.Info("evicting prior agent for cluster",
				slog.String("cluster_id", clusterID),
				slog.String("evicted_agent_id", evicted.AgentID),
			)
			evicted.Close()
		}
		defer s.registry.Unregister(registeredAgent)
	}

	// Spec #09 V2 Item 5b — emit stream lifecycle counters that the
	// /admin/ingest-activity panel queries via PromQL. Connected fires
	// once per accepted stream (after Welcome + Register); disconnected
	// runs on any return path from this handler. The auth-rejected
	// status is recorded in auth_interceptor.go on the failure path
	// (we never reach this point if auth rejected the stream).
	tenantIDLabel := ""
	if id != nil {
		tenantIDLabel = id.TenantID
	}
	s.metrics.RecordStreamEvent(tenantIDLabel, GRPCIngestStreamConnected)
	defer s.metrics.RecordStreamEvent(tenantIDLabel, GRPCIngestStreamDisconnected)

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
			// Anti-spoofing gate (W3a) — mirror the remote_write tenant
			// check (prom_write.go): a sample asserting a tenant_id that
			// isn't this agent's authenticated tenant is a cross-tenant
			// write attempt. Reject the batch AND tear the stream down (a
			// spoofing agent has no business staying connected); absent
			// labels are stamped authoritatively from id.TenantID. No-op
			// when unauthenticated (OSS / disabled mode → id nil/empty).
			if id != nil && id.TenantID != "" {
				if asserted, ok := enforceTenantLabel(batch.GetSamples(), id.TenantID); !ok {
					slog.Warn("agent gRPC tenant_id mismatch — closing stream",
						slog.String("agent_id", agentID),
						slog.String("cluster_id", clusterID),
						slog.String("asserted", asserted),
						slog.String("bearer_tenant", id.TenantID))
					s.metrics.RecordStreamEvent(tenantIDLabel, GRPCIngestStreamTenantMismatch)
					return status.Errorf(codes.PermissionDenied,
						"tenant_id label %q does not match authenticated tenant", asserted)
				}
			}
			// Spec #09 V2 Item 5b — per-tenant samples counter that the
			// /admin/ingest-activity panel renders as a rate-of-receive
			// sparkline (`rate(kubebolt_agent_grpc_samples_received_total
			// [5m])`). Recorded BEFORE writer.Write so a downstream VM
			// outage still increments the counter — that way the panel
			// shows ingest IS arriving even when downstream storage is
			// down, which is useful diagnostically.
			s.metrics.RecordSamplesReceived(tenantIDLabel, len(batch.GetSamples()))
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
//
// cluster_id precedence (both branches):
//  1. id.ClusterID — operator-asserted via tokenreview / mTLS startup config.
//     Trumps everything because it's bound to the gRPC server's deployment
//     and can't be spoofed by the client.
//  2. clusterHint  — best-effort identifier the agent populates in its Hello
//     (auto-detected from kube-system namespace UID, or supplied via
//     KUBEBOLT_AGENT_CLUSTER_ID / helm `cluster.id`). Honored when no auth
//     identity carries one — covers ingest-token (which doesn't bind a
//     cluster), disabled mode, and the historical OSS multi-cluster topology
//     "one backend, agents from N clusters" that was silently broken by the
//     prior `"local"` hardcode.
//  3. "local"      — last-resort sentinel, used only when both the auth
//     identity AND the agent itself failed to report a cluster_id (e.g.
//     agent's kube-system UID lookup raced with startup permissions).
//
// Pre-fix code dropped clusterHint on the floor and collapsed every agent
// without an auth-set ClusterID to "local", which is why two distinct kind
// clusters registered under the same id in cluster-validation Test 3.
func resolveAgentID(id *auth.AgentIdentity, nodeName, clusterHint, role string) (agentID, clusterID string) {
	cluster := clusterHint
	if id != nil && id.ClusterID != "" {
		// Auth-supplied cluster wins. Currently only tokenreview/mTLS
		// startup config populate this; bearer/ingest-token leaves it
		// empty and falls through to the hint.
		cluster = id.ClusterID
	}
	if cluster == "" {
		cluster = "local"
	}
	if id == nil || id.Mode == auth.ModeDisabled || id.TenantID == "" {
		// No auth identity → can't derive a stable agent_id, fresh UUID
		// each connect. The cluster label is still honored from the hint
		// so multi-cluster OSS deployments don't collapse to one entry.
		return uuid.NewString(), cluster
	}
	return auth.DeriveAgentID(id.TenantID, cluster, nodeName, role), cluster
}

// agentRoleFromHello extracts the role tag used as the 4th
// discriminator in DeriveAgentID, so two pods sharing
// (tenant, cluster, node) get distinct agent_ids and the registry
// doesn't evict them in a loop. See project_agent_eviction_loop for
// the full diagnosis.
//
// Resolution order:
//
//  1. Hello.Labels["kubebolt.io/agent-mode"] — agents from chart
//     1.13.0+ stamp this directly from the KUBEBOLT_AGENT_MODE env
//     ("daemonset" / "promread"). Authoritative, orthogonal to
//     capabilities / RBAC mode. Future agent topologies declare
//     their own mode here and get distinct roles automatically.
//
//  2. Fallback for pre-1.13 agents (legacy compat) — classify by
//     presence of the "kube-proxy" capability: proxy vs metrics.
//     Less robust than (1) because rbac.mode=metrics on a DS pod
//     also produces capabilities=[metrics] and would collide with a
//     promread pod, but that's the best we can do without the label
//     and pre-1.13 didn't have the promread Deployment topology
//     anyway.
func agentRoleFromHello(hello *agentv2.Hello) string {
	if hello == nil {
		return "metrics"
	}
	if labels := hello.GetLabels(); labels != nil {
		if mode := labels["kubebolt.io/agent-mode"]; mode != "" {
			return mode
		}
	}
	for _, c := range hello.GetCapabilities() {
		if c == "kube-proxy" {
			return "proxy"
		}
	}
	return "metrics"
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
