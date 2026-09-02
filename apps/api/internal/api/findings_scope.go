package api

import "net/http"

// Team scoping for the Security pillar (S-6).
//
// `/findings`, `/findings/workloads` and `/runtime-events` took `?cluster=`
// straight from the query string and, with no parameter, answered for the whole
// ORG. The org boundary held — every one of them scopes on activeTenantID — but
// the TEAM boundary did not exist here at all, while `/clusters`, the fleet
// search and the metrics reads have enforced it since S-2/S-3.
//
// Confirmed in vivo 2026-08-07: a Team B member whose cluster list shows only
// `kind-kubebolt-lab` (no scanners installed, zero findings) could read every
// finding of the other team's cluster.
//
// What leaks is not a list of counts. A finding names the workload, its
// namespace and its image; a runtime event carries the COMMAND LINE that ran,
// the file it touched and the user it ran as. That is more revealing than the
// metric labels whose leak S-1 closed, and it was added the same week.
//
// The narrowing reuses allowedClusterIDs — the same resolver /clusters and the
// metrics path already use — so a cluster is visible here exactly when it is
// visible there. One definition of "may read", not a second opinion.

// findingsClusterFilter conserva su forma para los tres handlers que ya la
// usan, pero DELEGA en el alcance que resuelve el middleware (cluster_scope.go)
// en vez de repetir la resolución.
//
// Se mantiene la función en vez de reescribir los tres call sites: la firma
// —(cluster, predicado)— encaja con cómo esos handlers ya estructuran su
// lectura, y cambiarla no habría hecho ninguno más seguro. Lo que sí cambia es
// que ya no hay DOS definiciones de «qué puedo leer»; hay una, y ésta es una
// vista sobre ella.
func (h *handlers) findingsClusterFilter(r *http.Request, requested string) (string, func(clusterID string) bool) {
	scope := ClusterScopeFrom(r.Context())
	// El middleware toma el cluster del query string; un llamante que pase otro
	// explícitamente (la ruta de workloads usa su propio parámetro) manda.
	if requested != "" && requested != scope.requested {
		scope.requested = requested
	}
	return scope.Requested(), scope.May
}
