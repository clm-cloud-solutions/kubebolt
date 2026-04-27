package agent

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
)

// AuthEnforcement controls how strictly the interceptor reacts when
// authentication fails. The Sprint A migration window defaults to
// "disabled"; production flips to "enforced" before the SaaS release.
type AuthEnforcement string

const (
	// EnforcementDisabled — accept every call, inject a synthetic
	// identity (Mode=ModeDisabled). Logs a startup warning so operators
	// notice they are running an unauthenticated channel.
	EnforcementDisabled AuthEnforcement = "disabled"
	// EnforcementPermissive — try to authenticate; on failure, log at
	// WARN level and let the call through with the synthetic identity.
	// Useful while migrating existing fleets onto the new credentials.
	EnforcementPermissive AuthEnforcement = "permissive"
	// EnforcementEnforced — reject any call that fails authentication.
	EnforcementEnforced AuthEnforcement = "enforced"
)

// ParseEnforcement maps a config string to AuthEnforcement, falling
// back to EnforcementDisabled on empty or unrecognized values. The
// fallback is loud at startup (callers should log it) — silent default
// to "disabled" would mask misconfigured production deployments.
func ParseEnforcement(s string) (AuthEnforcement, bool) {
	switch AuthEnforcement(s) {
	case EnforcementDisabled, EnforcementPermissive, EnforcementEnforced:
		return AuthEnforcement(s), true
	default:
		return EnforcementDisabled, false
	}
}

// AuthConfig is the runtime parameters of the auth interceptor.
//
// Authenticator is required for permissive/enforced modes; disabled
// mode tolerates a nil authenticator (the interceptor short-circuits).
//
// RequireMTLS layers on top of the chosen mode: even if authentication
// succeeds, missing a verified client cert rejects the call. Has no
// effect when EnforcementDisabled.
type AuthConfig struct {
	Enforcement   AuthEnforcement
	Authenticator auth.AgentAuthenticator
	RequireMTLS   bool
}

// dummyIdentity is the placeholder the interceptor stamps when
// enforcement is disabled or permissive-fallback. It deliberately leaves
// TenantID/ClusterID empty so handlers that branch on those (commit 6+)
// see the absence and treat the call as untrusted.
func dummyIdentity(now time.Time) *auth.AgentIdentity {
	return &auth.AgentIdentity{Mode: auth.ModeDisabled, AuthedAt: now}
}

// authenticateMD runs the authenticator against the call metadata,
// honoring enforcement and mTLS rules. Returns the identity to stamp on
// the context, or a gRPC status error.
func authenticateMD(ctx context.Context, cfg AuthConfig) (*auth.AgentIdentity, error) {
	now := time.Now().UTC()
	if cfg.Enforcement == EnforcementDisabled {
		return dummyIdentity(now), nil
	}
	if cfg.Authenticator == nil {
		// Misconfigured: enforcement requested but no authenticator
		// wired. Fail loud rather than silently accept.
		return nil, status.Error(codes.Internal, "agent auth: authenticator not configured")
	}

	md, _ := metadata.FromIncomingContext(ctx)
	p, _ := peer.FromContext(ctx)

	id, err := cfg.Authenticator.Authenticate(ctx, md, p)
	if err != nil {
		if cfg.Enforcement == EnforcementPermissive {
			slog.Warn("agent auth permissive-fallback",
				slog.String("error", err.Error()),
			)
			return dummyIdentity(now), nil
		}
		return nil, mapAuthError(err)
	}
	if cfg.RequireMTLS && !id.TLSVerified {
		return nil, mapAuthError(auth.ErrTLSRequired)
	}
	return id, nil
}

// mapAuthError converts the auth package's sentinel errors into gRPC
// status codes. Unmapped errors collapse to Unauthenticated — never
// Internal — to avoid leaking implementation details to the agent.
func mapAuthError(err error) error {
	switch {
	case errors.Is(err, auth.ErrUnknownMode):
		return status.Error(codes.InvalidArgument, "agent auth: unknown auth mode")
	case errors.Is(err, auth.ErrTokenRevoked),
		errors.Is(err, auth.ErrTenantDisabled):
		return status.Error(codes.PermissionDenied, "agent auth: credential rejected")
	case errors.Is(err, auth.ErrMissingToken),
		errors.Is(err, auth.ErrTokenInvalid),
		errors.Is(err, auth.ErrTokenMalformed),
		errors.Is(err, auth.ErrTokenExpired),
		errors.Is(err, auth.ErrTokenNotFound),
		errors.Is(err, auth.ErrTLSRequired):
		return status.Error(codes.Unauthenticated, "agent auth: invalid credentials")
	default:
		return status.Error(codes.Unauthenticated, "agent auth: invalid credentials")
	}
}

// UnaryAuthInterceptor authenticates and stamps the identity on the
// context for unary RPCs (Register, Heartbeat).
func UnaryAuthInterceptor(cfg AuthConfig) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		id, err := authenticateMD(ctx, cfg)
		if err != nil {
			return nil, err
		}
		return handler(auth.WithAgentIdentity(ctx, id), req)
	}
}

// StreamAuthInterceptor authenticates and wraps the ServerStream so
// handlers see the enriched context (StreamMetrics).
func StreamAuthInterceptor(cfg AuthConfig) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		id, err := authenticateMD(ss.Context(), cfg)
		if err != nil {
			return err
		}
		wrapped := &authedServerStream{
			ServerStream: ss,
			ctx:          auth.WithAgentIdentity(ss.Context(), id),
		}
		return handler(srv, wrapped)
	}
}

// authedServerStream overrides ServerStream.Context() to expose the
// auth-enriched ctx to the handler. Everything else delegates.
type authedServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *authedServerStream) Context() context.Context { return s.ctx }

// LogStartupMode emits a single line at startup describing the auth
// posture so operators can confirm it from logs without reading config.
func LogStartupMode(cfg AuthConfig) {
	attrs := []any{
		slog.String("enforcement", string(cfg.Enforcement)),
		slog.Bool("require_mtls", cfg.RequireMTLS),
		slog.Bool("authenticator_configured", cfg.Authenticator != nil),
	}
	switch cfg.Enforcement {
	case EnforcementDisabled:
		slog.Warn("agent ingest auth DISABLED — every call accepted (Sprint A migration window)", attrs...)
	case EnforcementPermissive:
		slog.Warn("agent ingest auth PERMISSIVE — failures logged, calls allowed", attrs...)
	case EnforcementEnforced:
		slog.Info("agent ingest auth ENFORCED", attrs...)
	default:
		slog.Warn("agent ingest auth: unknown enforcement, defaulting to disabled",
			slog.String("requested", string(cfg.Enforcement)),
		)
	}
}

