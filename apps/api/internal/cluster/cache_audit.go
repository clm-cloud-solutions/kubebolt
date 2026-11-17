package cluster

import (
	"context"
	"errors"
	"log/slog"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Informer cache audit — does the cache hold everything the apiserver has?
//
// WHY THIS EXISTS. On 2026-08-05 the ReplicaSet informer of an agent-proxy
// cluster served 106 objects while the cluster had 107, the gap survived two
// sweeps ten minutes apart, and `WaitForCacheSync` had reported success. It was
// found only because a finding kept churning; nothing in the system claimed to
// be degraded. A component that declares itself healthy while serving a short
// list is the multiplier on every bug downstream of it.
//
// The open question that this answers: is the gap specific to ReplicaSets — in
// which case it costs some churn in Security — or does it hit Pods and
// Deployments too, in which case every count, every list and every topology edge
// on an agent-proxy cluster is quietly under-reporting. That decides whether
// this is a nuisance or a data-integrity incident, and it cannot be reasoned
// about, only measured.
//
// COST. Deliberately near-zero, because the alternative would be the
// cluster-wide LIST anti-pattern this codebase is careful about: the audit asks
// for ONE item and reads `remainingItemCount` from the list metadata, so the
// total costs one small round trip per type — not a full list. It must NOT pin
// `resourceVersion=0` (see countOnly): that makes the apiserver serve from its
// watch cache, ignore the limit and ship every object — tens of MB per type
// through the agent every 5 minutes.
//
// It logs only on DIVERGENCE. A clean audit says nothing, so the line appearing
// at all is the signal.

// cacheAuditInterval is slow on purpose: the fault is persistent when it
// happens (it survived ten minutes), so polling fast buys nothing and every
// tick crosses the agent tunnel.
const cacheAuditInterval = 5 * time.Minute

// StartCacheAudit runs the audit until the connector stops. Safe to call on any
// connector; types whose informer was never started (permission-gated SA) are
// skipped rather than reported as a gap.
func (c *Connector) StartCacheAudit() {
	go func() {
		tick := time.NewTicker(cacheAuditInterval)
		defer tick.Stop()
		// First pass shortly after sync, not on the tick: the interesting moment
		// is right after the caches claim to be ready.
		select {
		case <-time.After(30 * time.Second):
		case <-c.stopCh:
			return
		}
		c.AuditInformerCaches()
		for {
			select {
			case <-c.stopCh:
				return
			case <-tick.C:
				c.AuditInformerCaches()
			}
		}
	}()
}

// AuditInformerCaches compares each watched type's cache against the apiserver
// and logs every mismatch. Exported so a test or a future admin endpoint can
// ask for one pass on demand.
func (c *Connector) AuditInformerCaches() {
	if c == nil || c.clientset == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	c.auditType(ctx, "pods", c.cachedPodCount, func() (int, error) {
		l, err := c.clientset.CoreV1().Pods(metav1.NamespaceAll).List(ctx, countOnly())
		if err != nil || l == nil {
			return 0, err
		}
		return listCount(len(l.Items), l.ListMeta)
	})
	c.auditType(ctx, "deployments", c.cachedDeploymentCount, func() (int, error) {
		l, err := c.clientset.AppsV1().Deployments(metav1.NamespaceAll).List(ctx, countOnly())
		if err != nil || l == nil {
			return 0, err
		}
		return listCount(len(l.Items), l.ListMeta)
	})
	c.auditType(ctx, "replicasets", c.cachedReplicaSetCount, func() (int, error) {
		l, err := c.clientset.AppsV1().ReplicaSets(metav1.NamespaceAll).List(ctx, countOnly())
		if err != nil || l == nil {
			return 0, err
		}
		return listCount(len(l.Items), l.ListMeta)
	})
}

// countOnly asks for one item and reads the rest from the list metadata's
// RemainingItemCount, so the audit never downloads a full list. It deliberately
// does NOT set resourceVersion=0: served from the watch cache the apiserver
// ignores Limit and returns every object, with no RemainingItemCount — on a
// large cluster that is tens of MB per type every 5 minutes, through the agent,
// enough to OOMKill it. Without it the apiserver honours the limit and
// paginates — one item plus RemainingItemCount, a few KB; a consistent limit-1
// read is served from the watch cache where ConsistentListFromCache is on,
// otherwise one bounded etcd range read.
func countOnly() metav1.ListOptions {
	return metav1.ListOptions{Limit: 1}
}

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// errCountUnavailable means the apiserver paginated the limit-1 list (it sent a
// continue token) but reported no RemainingItemCount, so the collection's size
// is unknown. auditType skips the pass instead of comparing the cache with 1.
var errCountUnavailable = errors.New("apiserver paginated the list without remainingItemCount")

// listCount turns a limit-1 list into the collection's size: the items returned
// plus RemainingItemCount. A whole list never needed that field; with limit=1
// the size depends on it, and the apiserver omits it when a label or field
// selector is set, and a storage backend may not compute it. With a continue
// token and no count the size is unknown, and len(Items)=1 would be logged as a
// divergence of thousands every 5 minutes, so it reports errCountUnavailable.
func listCount(items int, meta metav1.ListMeta) (int, error) {
	if meta.Continue != "" && meta.RemainingItemCount == nil {
		return 0, errCountUnavailable
	}
	return items + int(deref(meta.RemainingItemCount)), nil
}

// auditType runs one comparison. A cached count of -1 means "informer not
// running" — skipped, because a permission-gated SA legitimately has no cache
// and reporting that as a gap would cry wolf on every namespace-scoped install.
func (c *Connector) auditType(ctx context.Context, kind string, cached func() int, live func() (int, error)) {
	have := cached()
	if have < 0 {
		return
	}
	want, err := live()
	if err != nil {
		slog.Debug("cache audit: could not count from apiserver",
			slog.String("kind", kind), slog.String("error", err.Error()))
		return
	}
	if have == want {
		return
	}
	// WARN, not DEBUG: unlike "CRD absent", there is no healthy reading of this.
	// Either the cache is short (silent under-reporting) or it is long (stale
	// objects the apiserver has dropped) — both are wrong answers served as if
	// they were right.
	slog.Warn("cache audit: informer disagrees with the apiserver",
		slog.String("kind", kind),
		slog.Int("cached", have),
		slog.Int("apiserver", want),
		slog.Int("delta", have-want))
}

func (c *Connector) cachedPodCount() int {
	if c.podLister == nil {
		return -1
	}
	l, err := c.podLister.List(everythingSelector())
	if err != nil {
		return -1
	}
	return len(l)
}

func (c *Connector) cachedDeploymentCount() int {
	if c.deploymentLister == nil {
		return -1
	}
	l, err := c.deploymentLister.List(everythingSelector())
	if err != nil {
		return -1
	}
	return len(l)
}

func (c *Connector) cachedReplicaSetCount() int {
	if c.replicaSetLister == nil {
		return -1
	}
	l, err := c.replicaSetLister.List(everythingSelector())
	if err != nil {
		return -1
	}
	return len(l)
}
