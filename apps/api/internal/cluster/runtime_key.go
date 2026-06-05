package cluster

import "context"

// RuntimeKey identifies which (tenant, cluster) a request targets, so the
// Manager can route it to the right per-(tenant,cluster) runtime once the
// connector pool lands (W2 Fase A.3).
//
// In OSS, Tenant is always "default" (single-tenant) and Cluster is usually
// empty — meaning "the active context", preserving today's single-active
// behavior. The EE/SaaS edition fills both so ONE backend serves many
// (tenant, cluster) pairs concurrently. Set by the resolveCluster
// middleware; read by Manager.
//
// Design: internal/kubebolt-w2-connector-pool-design.md.
type RuntimeKey struct {
	Tenant  string
	Cluster string // cluster_id / context name; "" → active context (OSS)
}

type runtimeKeyCtx struct{}

// WithRuntimeKey returns ctx carrying the resolved runtime key.
func WithRuntimeKey(ctx context.Context, key RuntimeKey) context.Context {
	return context.WithValue(ctx, runtimeKeyCtx{}, key)
}

// RuntimeKeyFromContext returns the runtime key stashed by the middleware,
// or the zero value (Tenant "", Cluster "") when absent — which callers
// treat as "default tenant, active cluster".
func RuntimeKeyFromContext(ctx context.Context) RuntimeKey {
	if k, ok := ctx.Value(runtimeKeyCtx{}).(RuntimeKey); ok {
		return k
	}
	return RuntimeKey{}
}
