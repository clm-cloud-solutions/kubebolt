package api

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
	"github.com/kubebolt/kubebolt/apps/api/internal/rightsizing"
)

// GET /right-sizing — the recommendations the Capacity and Cost screens show,
// and the same function behind Kobi's get_right_sizing. See the rightsizing
// package for the rules.

// P95 over 7d per workload (Deployment / StatefulSet / DaemonSet). The
// label_replace pair collapses ReplicaSet → Deployment the way TopWorkloadsCpu
// does. Copied verbatim from the web hook this replaced.
func rightSizingP95Query(expr func(matcher string) string) string {
	return `quantile_over_time(0.95, ` +
		`sum by (workload_kind, workload_name, namespace) (` +
		`label_replace(label_replace(` + expr(`workload_kind="ReplicaSet",workload_name!=""`) + `, ` +
		`"workload_name", "$1", "workload_name", "^(.+)-[a-z0-9]{6,12}$"), ` +
		`"workload_kind", "Deployment", "workload_kind", "ReplicaSet") ` +
		`or ` + expr(`workload_kind=~"StatefulSet|DaemonSet",workload_name!=""`) +
		`)[7d:5m])`
}

var (
	rightSizingCPUQuery = rightSizingP95Query(func(m string) string {
		return `rate(container_cpu_usage_seconds_total{` + m + `}[5m])`
	})
	rightSizingMemQuery = rightSizingP95Query(func(m string) string {
		return `container_memory_working_set_bytes{` + m + `}`
	})
	// How many hours of usage history the P95 really spans (1h buckets over 7d).
	rightSizingWindowQuery = `count_over_time((count(container_cpu_usage_seconds_total))[7d:1h])`
)

// The P95 queries are 7-day subqueries — heavy. The answer does not move
// minute to minute, so one computation per (org, cluster) serves every reader
// for rightSizingTTL: the screens poll and the tool may be called repeatedly.
const rightSizingTTL = 5 * time.Minute

type rightSizingEntry struct {
	at  time.Time
	res rightsizing.Result
}

var rightSizingCache sync.Map // "tenant|clusterUID" → rightSizingEntry

// computeRightSizing runs the three queries confined to the caller's org and
// the request's cluster and applies the rules to the connector's workloads.
func (h *handlers) computeRightSizing(ctx context.Context, conn *cluster.Connector) (rightsizing.Result, error) {
	uid := h.activeClusterUID(ctx)
	tenant := metricsTenantPinCtx(ctx)
	key := tenant + "|" + uid
	if v, ok := rightSizingCache.Load(key); ok {
		if e := v.(rightSizingEntry); time.Since(e.at) < rightSizingTTL {
			return e.res, nil
		}
	}
	scope := func(q string) string { return scopeQueryByTenant(scopeQueryByCluster(q, uid), tenant) }

	cpuRows, err := runInstantQuery(ctx, scope(rightSizingCPUQuery))
	if err != nil {
		return rightsizing.Result{}, err
	}
	memRows, err := runInstantQuery(ctx, scope(rightSizingMemQuery))
	if err != nil {
		return rightsizing.Result{}, err
	}
	var windowDays *float64
	if rows, err := runInstantQuery(ctx, scope(rightSizingWindowQuery)); err == nil && len(rows) > 0 {
		d := rows[0].Value / 24
		windowDays = &d
	}
	res := rightsizing.Evaluate(conn.NamespaceWorkloads(),
		p95Index(cpuRows, 1000), // cores → millicores
		p95Index(memRows, 1), windowDays)
	rightSizingCache.Store(key, rightSizingEntry{at: time.Now(), res: res})
	return res, nil
}

func p95Index(rows []vmRow, scale float64) map[string]int64 {
	out := make(map[string]int64, len(rows))
	for _, r := range rows {
		ns, kind, name := r.Labels["namespace"], r.Labels["workload_kind"], r.Labels["workload_name"]
		if ns == "" || kind == "" || name == "" {
			continue
		}
		out[rightsizing.Key(ns, kind, name)] = int64(r.Value*scale + 0.5)
	}
	return out
}

func (h *handlers) handleRightSizing(w http.ResponseWriter, r *http.Request) {
	conn := h.manager.Connector(r.Context())
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}
	res, err := h.computeRightSizing(r.Context(), conn)
	if err != nil {
		respondError(w, http.StatusBadGateway, "metrics storage unreachable")
		return
	}
	respondJSON(w, http.StatusOK, res)
}

var errClusterNotConnected = errors.New("the cluster is not connected, so its workloads cannot be read")

func (h *handlers) rightSizingSourceFor() copilot.RightSizingSource {
	if h.manager == nil {
		return nil
	}
	return rightSizingSource{h: h}
}

// rightSizingSource is get_right_sizing's source: the same computation as
// the endpoint, on the tool call's context.
type rightSizingSource struct{ h *handlers }

func (s rightSizingSource) RightSizing(ctx context.Context) (rightsizing.Result, error) {
	conn := s.h.manager.Connector(ctx)
	if conn == nil {
		return rightsizing.Result{}, errClusterNotConnected
	}
	return s.h.computeRightSizing(ctx, conn)
}
