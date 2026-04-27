// Package agent hosts the backend-side of the kubebolt-agent wire contract:
// the gRPC AgentIngest service and its metrics storage writer.
//
// Sprint A wired authentication onto this service: a CompositeAuth
// dispatches by metadata header (tokenreview vs ingest-token) and the
// interceptor stamps an *auth.AgentIdentity on the request context.
// Handlers consume that identity via auth.AgentIdentityFromContext to
// derive a stable agent_id and enrich audit logs.
//
// Persistence (agents bucket, ULID identifiers) and rate limiting land
// in Sprints B and A-week3 respectively.
package agent

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	agentv1 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v1"
)

// MetricsWriter is the contract between the ingest server and whatever TSDB
// is underneath. Sprint 0 uses VMWriter; Sprint 2 abstracts further once
// more than one implementation is needed.
type MetricsWriter interface {
	Write(ctx context.Context, samples []*agentv1.Sample) error
}

// Server implements agentv1.AgentIngestServer.
type Server struct {
	agentv1.UnimplementedAgentIngestServer
	writer MetricsWriter
}

func NewServer(writer MetricsWriter) *Server {
	return &Server{writer: writer}
}

// resolveAgentID returns a stable agent identifier. With an authenticated
// identity it derives sha256(tenant|cluster|node)[:16] so reconnects
// land on the same id without persistence (Sprint B replaces with ULID
// stored in the agents bucket). Without an identity (auth disabled) it
// falls back to a fresh UUID — the legacy Sprint 0 behavior.
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

func (s *Server) Register(ctx context.Context, req *agentv1.RegisterRequest) (*agentv1.RegisterResponse, error) {
	id := auth.AgentIdentityFromContext(ctx)
	agentID, clusterID := resolveAgentID(id, req.GetNodeName())

	logAttrs := []any{
		slog.String("agent_id", agentID),
		slog.String("cluster_id", clusterID),
		slog.String("node_name", req.GetNodeName()),
		slog.String("agent_version", req.GetAgentVersion()),
		slog.String("kernel", req.GetKernelVersion()),
		slog.String("runtime", req.GetContainerRuntime()),
	}
	if id != nil {
		logAttrs = append(logAttrs,
			slog.String("auth_mode", string(id.Mode)),
			slog.String("tenant_id", id.TenantID),
			slog.Bool("tls_verified", id.TLSVerified),
		)
	}
	slog.Info("agent registered", logAttrs...)

	return &agentv1.RegisterResponse{
		AgentId:   agentID,
		ClusterId: clusterID,
		Config: &agentv1.AgentConfig{
			SampleIntervalSeconds: 15,
			BatchSize:             500,
			BatchFlushSeconds:     5,
		},
	}, nil
}

func (s *Server) StreamMetrics(stream agentv1.AgentIngest_StreamMetricsServer) error {
	ctx := stream.Context()
	id := auth.AgentIdentityFromContext(ctx)
	for {
		batch, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		logAttrs := []any{
			slog.String("agent_id", batch.GetAgentId()),
			slog.Int("samples", len(batch.GetSamples())),
		}
		if id != nil {
			logAttrs = append(logAttrs, slog.String("tenant_id", id.TenantID))
		}
		slog.Info("received metric batch", logAttrs...)
		if werr := s.writer.Write(ctx, batch.GetSamples()); werr != nil {
			slog.Error("metrics write failed", slog.String("error", werr.Error()))
			if sendErr := stream.Send(&agentv1.IngestAck{
				ReceivedAt:       timestamppb.Now(),
				SamplesAccepted:  0,
				SamplesRejected:  uint32(len(batch.GetSamples())),
				RejectionReasons: []string{"storage_write_failed: " + werr.Error()},
			}); sendErr != nil {
				return sendErr
			}
			continue
		}
		if sendErr := stream.Send(&agentv1.IngestAck{
			ReceivedAt:      timestamppb.Now(),
			SamplesAccepted: uint32(len(batch.GetSamples())),
		}); sendErr != nil {
			return sendErr
		}
	}
}

func (s *Server) Heartbeat(ctx context.Context, req *agentv1.HeartbeatRequest) (*agentv1.HeartbeatResponse, error) {
	stats := req.GetStats()
	if stats != nil {
		logAttrs := []any{
			slog.String("agent_id", req.GetAgentId()),
			slog.Uint64("samples_sent", stats.GetSamplesSentTotal()),
			slog.Uint64("samples_dropped", stats.GetSamplesDroppedTotal()),
			slog.Uint64("buffer_size", stats.GetBufferSizeCurrent()),
		}
		if id := auth.AgentIdentityFromContext(ctx); id != nil {
			logAttrs = append(logAttrs, slog.String("tenant_id", id.TenantID))
		}
		slog.Info("agent heartbeat", logAttrs...)
	}
	return &agentv1.HeartbeatResponse{
		ReceivedAt:           timestamppb.Now(),
		AgentShouldReconnect: false,
	}, nil
}

// ListenOptions configures Listen.
//
//	Auth.Enforcement=""  defaults to EnforcementDisabled (with a startup warning).
//	TLS=nil              runs plaintext (with a startup warning).
type ListenOptions struct {
	Auth AuthConfig
	TLS  *TLSConfig
}

// Listen binds a gRPC listener at addr and serves AgentIngest on it
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
	agentv1.RegisterAgentIngestServer(grpcSrv, srv)
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
