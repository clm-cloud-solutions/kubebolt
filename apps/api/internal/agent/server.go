// Package agent hosts the backend-side of the kubebolt-agent wire contract:
// the gRPC AgentIngest service and its metrics storage writer.
//
// This is the walking-skeleton scope (Sprint 0): no TokenReview auth, no
// agent registry persistence, no rate limiting. Every call is accepted and
// samples are forwarded to the MetricsWriter. Production concerns are added
// in Sprint 2.
package agent

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

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

func (s *Server) Register(ctx context.Context, req *agentv1.RegisterRequest) (*agentv1.RegisterResponse, error) {
	agentID := uuid.NewString()
	slog.Info("agent registered",
		slog.String("agent_id", agentID),
		slog.String("node_name", req.GetNodeName()),
		slog.String("agent_version", req.GetAgentVersion()),
		slog.String("kernel", req.GetKernelVersion()),
		slog.String("runtime", req.GetContainerRuntime()),
	)
	return &agentv1.RegisterResponse{
		AgentId:   agentID,
		ClusterId: "local",
		Config: &agentv1.AgentConfig{
			SampleIntervalSeconds: 15,
			BatchSize:             500,
			BatchFlushSeconds:     5,
		},
	}, nil
}

func (s *Server) StreamMetrics(stream agentv1.AgentIngest_StreamMetricsServer) error {
	ctx := stream.Context()
	for {
		batch, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		slog.Info("received metric batch",
			slog.String("agent_id", batch.GetAgentId()),
			slog.Int("samples", len(batch.GetSamples())),
		)
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
		slog.Info("agent heartbeat",
			slog.String("agent_id", req.GetAgentId()),
			slog.Uint64("samples_sent", stats.GetSamplesSentTotal()),
			slog.Uint64("samples_dropped", stats.GetSamplesDroppedTotal()),
			slog.Uint64("buffer_size", stats.GetBufferSizeCurrent()),
		)
	}
	return &agentv1.HeartbeatResponse{
		ReceivedAt:           timestamppb.Now(),
		AgentShouldReconnect: false,
	}, nil
}

// Listen binds a gRPC listener at addr and serves AgentIngest on it until
// ctx is cancelled. Blocks until the server exits.
func Listen(ctx context.Context, addr string, srv *Server) error {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("agent gRPC listen %s: %w", addr, err)
	}
	grpcSrv := grpc.NewServer()
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
