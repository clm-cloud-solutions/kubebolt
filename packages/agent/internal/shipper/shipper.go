// Package shipper owns the long-lived gRPC connection to the backend
// and the reconnect loop. The actual session state machine — Hello,
// Welcome, heartbeat, metrics flush, BackendMessage dispatch — lives
// in packages/agent/internal/channel.
//
// shipper composes:
//
//   AuthOptions / NewTokenCreds / BuildTransportCredentials
//     → builds the *grpc.ClientConn with auth + TLS as decided in
//       Sprint A.
//
//   channel.NewClient(conn, ring buffer, hello info, handler)
//     → owns one session. Returns when the session ends.
//
// Reconnect: exponential 1s → 60s on session end. Each retry builds
// a fresh Client (the underlying conn is reused — gRPC handles its
// own connection-level recovery, the session state is the bit we
// rebuild).
package shipper

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"time"

	"google.golang.org/grpc"

	"github.com/kubebolt/kubebolt/packages/agent/internal/buffer"
	"github.com/kubebolt/kubebolt/packages/agent/internal/channel"
)

type Shipper struct {
	backendURL   string
	buf          *buffer.Ring
	nodeName     string
	agentVersion string
	auth         AuthOptions
	handler      channel.Handler
	capabilities []string

	// Cluster identity that goes into Hello. clusterHint is the
	// agent's best-effort cluster_id (auto-derived from kube-system
	// UID); clusterName is a human label sourced from
	// KUBEBOLT_AGENT_CLUSTER_NAME. Both are forwarded so the backend
	// can pick a friendlier display when auto-registering.
	clusterHint string
	clusterName string

	// Populated each time a session reaches Welcome.
	agentID string
}

// Option mutates a Shipper at construction time. Functional-options
// pattern keeps New backward-compatible.
type Option func(*Shipper)

// WithAuth attaches credentials to the shipper. The zero AuthOptions
// keeps the legacy plaintext-no-token behavior.
func WithAuth(opts AuthOptions) Option {
	return func(s *Shipper) { s.auth = opts }
}

// WithHandler attaches a channel.Handler to the shipper. Used by the
// kube-proxy wiring (Sprint A.5 commit 4) to plug in a KubeAPIProxy
// dispatcher. nil falls back to channel.NoopHandler.
func WithHandler(h channel.Handler) Option {
	return func(s *Shipper) { s.handler = h }
}

// WithCapabilities advertises agent capabilities in the Hello message.
// The kube-proxy wiring sets ["metrics", "kube-proxy"] when the proxy
// is built. Default is ["metrics"].
func WithCapabilities(caps ...string) Option {
	return func(s *Shipper) { s.capabilities = caps }
}

// WithClusterIdent sets the cluster identity carried in Hello. The
// agent resolves these from kube-system UID + KUBEBOLT_AGENT_CLUSTER_NAME
// at startup; passing them here lets the backend's auto-register
// pick a human-friendly display name for the cluster entry. Empty
// strings are forwarded unchanged — the backend falls back to the
// cluster_id it derives from auth.
func WithClusterIdent(clusterID, clusterName string) Option {
	return func(s *Shipper) {
		s.clusterHint = clusterID
		s.clusterName = clusterName
	}
}

func New(backendURL, nodeName, agentVersion string, buf *buffer.Ring, opts ...Option) *Shipper {
	s := &Shipper{
		backendURL:   backendURL,
		buf:          buf,
		nodeName:     nodeName,
		agentVersion: agentVersion,
		capabilities: []string{"metrics"},
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// AgentID is the id assigned by the backend on the latest session.
// Empty until the first session reaches Welcome.
func (s *Shipper) AgentID() string { return s.agentID }

// Run owns the reconnect loop and returns only when ctx is cancelled.
//
// Backoff strategy: exponential growth (1s → 2s → 4s ... up to 60s)
// while the dial keeps failing — appropriate for a backend that's
// genuinely down or unreachable. But sessions that ran cleanly for a
// while and THEN dropped (typical of a graceful backend restart) reset
// the backoff to 1s, so a deploy-induced reconnect doesn't make the
// agent sit out the full 60s cap. Without this reset, repeated dev
// restarts of the backend pin the backoff at 60s and the cluster stays
// blank in the UI for a full minute every iteration.
func (s *Shipper) Run(ctx context.Context) {
	backoff := time.Second
	const (
		backoffMax           = 60 * time.Second
		healthySessionMinAge = 10 * time.Second
	)

	for {
		if ctx.Err() != nil {
			return
		}
		sessionStart := time.Now()
		err := s.runSession(ctx)
		if err == nil || ctx.Err() != nil {
			return
		}
		// A session that survived >= healthySessionMinAge means we did
		// reach the backend, completed handshake, and shipped at least
		// some traffic. Treat the next failure as fresh — don't carry
		// any prior backoff state into the reconnect.
		if time.Since(sessionStart) >= healthySessionMinAge {
			backoff = time.Second
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

// runSession dials the backend, runs one channel.Client session, and
// returns whatever Client.Run returns. The reconnect loop above turns
// non-nil errors into another attempt.
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

	hello := channel.HelloInfo{
		NodeName:         s.nodeName,
		AgentVersion:     s.agentVersion,
		KernelVersion:    runtime.GOOS + "/" + runtime.GOARCH,
		ContainerRuntime: "phaseB",
		CgroupVersion:    "n/a",
		KubeletVersion:   "n/a",
		ClusterHint:      s.clusterHint,
		Capabilities:     s.capabilities,
	}
	if s.clusterName != "" {
		hello.Labels = map[string]string{"kubebolt.io/cluster-name": s.clusterName}
	}

	handler := s.handler
	if handler == nil {
		handler = channel.NoopHandler{}
	}
	client := channel.NewClient(conn, s.buf, hello, handler)
	if err := client.Run(ctx); err != nil {
		return err
	}
	s.agentID = client.AgentID()
	return nil
}
