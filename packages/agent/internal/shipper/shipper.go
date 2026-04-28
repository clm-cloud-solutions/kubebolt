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
		Capabilities:     s.capabilities,
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
