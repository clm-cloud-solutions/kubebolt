package api

import (
	"net/http"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
)

// ClusterHeader carries the cluster the frontend selected for this request.
// OSS may omit it (→ the active context); the EE/SaaS UI sends it per
// request so one backend can serve many (tenant, cluster) pairs
// concurrently (W2).
const ClusterHeader = "X-KubeBolt-Cluster"

// resolveCluster stashes the request's (tenant, cluster) RuntimeKey in
// context, mounted after RequireAuth + ResolveTenant (W0). It is
// behavior-neutral today: the Manager still serves the single active
// cluster until the connector pool (W2 Fase A.3) reads the key. Threading
// the key now lets that pool land WITHOUT touching the 56 handler call
// sites again. See internal/kubebolt-w2-connector-pool-design.md.
func (h *handlers) resolveCluster(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := cluster.RuntimeKey{
			Tenant:  auth.ContextTenantID(r), // W0 — "default" in OSS
			Cluster: r.Header.Get(ClusterHeader),
		}
		next.ServeHTTP(w, r.WithContext(cluster.WithRuntimeKey(r.Context(), key)))
	})
}
