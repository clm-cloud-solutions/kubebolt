package auth

import (
	"context"
	"net/http"
)

// tenantKey is the request-context key under which the resolved tenant
// (organization) ID is stored by the ResolveTenant middleware.
const tenantKey contextKey = "auth-tenant"

// TenantResolver decides which tenant (organization) a request belongs to.
//
// This is an OSS↔EE SEAM: the interface and the OSS default impl live here,
// in the public core, and the OSS behavior never changes (every request
// resolves to DefaultTenantName — single-tenant). The EE/SaaS edition swaps
// the active resolver with one that derives the real org from the
// multi-tenant session (JWT tenant claim, subdomain, etc.) by overriding
// DefaultTenantResolver from a build-tagged file that ships only in the
// private repo:
//
//	//go:build ee
//	func init() { DefaultTenantResolver = eeTenantResolver{} }
//
// Nothing here imports a database or knows about multi-tenancy — it only
// exposes the seam. See internal/kubebolt-oss-ee-split-runbook.md §2.1.
type TenantResolver interface {
	// ResolveTenant returns the tenant ID for the request. It must never
	// return "" without a deliberate fallback — callers treat "" as
	// DefaultTenantName.
	ResolveTenant(r *http.Request) string
}

// defaultTenantResolver is the OSS implementation. It honors a tenant claim
// if a future/EE-issued token carries one, otherwise falls back to the
// single auto-seeded tenant. In a stock OSS install this always returns
// DefaultTenantName, so behavior is identical to pre-seam code.
type defaultTenantResolver struct{}

func (defaultTenantResolver) ResolveTenant(r *http.Request) string {
	if c := ContextClaims(r); c != nil && c.TenantID != "" {
		return c.TenantID
	}
	return DefaultTenantName
}

// DefaultTenantResolver is the process-wide active resolver. OSS uses
// defaultTenantResolver; EE overrides this var via an init() in a
// build-tagged (`ee`) file that exists only in the private repo.
var DefaultTenantResolver TenantResolver = defaultTenantResolver{}

// ResolveTenant is middleware that resolves the request's tenant once and
// stashes it in the request context for downstream handlers/stores to read
// via ContextTenantID. Mount it AFTER RequireAuth so the resolver can see
// the JWT claims. Harmless in OSS — it always stamps DefaultTenantName.
func (h *Handlers) ResolveTenant(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tid := DefaultTenantResolver.ResolveTenant(r)
		if tid == "" {
			tid = DefaultTenantName
		}
		next.ServeHTTP(w, r.WithContext(WithTenantID(r.Context(), tid)))
	})
}

// WithTenantID returns ctx carrying tid as the resolved tenant (org), readable
// by TenantIDFromContext / ContextTenantID. ResolveTenant uses it; EE Postgres
// stores and tests can stamp a tenant directly onto a context.
func WithTenantID(ctx context.Context, tid string) context.Context {
	return context.WithValue(ctx, tenantKey, tid)
}

// TenantIDFromContext is the context-based counterpart of ContextTenantID: it
// returns the tenant (org) stamped by ResolveTenant/WithTenantID, or "" if
// absent. EE Postgres stores read this to scope each query's RLS org via
// eedb.WithOrg — "" means "no request org" (system/ingest paths), where the
// caller falls back to the connection's default.
func TenantIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(tenantKey).(string); ok {
		return v
	}
	return ""
}

// ContextTenantID returns the tenant ID resolved for the request. It reads
// the value stamped by ResolveTenant; if that middleware did not run (e.g.
// an unauthenticated/public route), it derives the tenant from the JWT
// claims, and finally falls back to DefaultTenantName. It therefore never
// returns "" — single-tenant OSS code can call it freely and always get
// "default".
func ContextTenantID(r *http.Request) string {
	if v, ok := r.Context().Value(tenantKey).(string); ok && v != "" {
		return v
	}
	if c := ContextClaims(r); c != nil && c.TenantID != "" {
		return c.TenantID
	}
	return DefaultTenantName
}
