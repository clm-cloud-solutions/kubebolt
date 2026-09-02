package cluster

import (
	"log/slog"

	"k8s.io/client-go/dynamic"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
)

// Dynamic exposes the connector's dynamic client for cross-package
// consumers that list optional CRDs (the findings ingest sweep,
// E2 SEC-C). nil when the connector was built without one — callers
// must tolerate it, same contract as listOptionalCRD.
func (c *Connector) Dynamic() dynamic.Interface { return c.dynamicClient }

// ForEachLiveConnector visits every runtime that currently holds a
// healthy connector — the active slot plus the parked pool — with its
// owning tenant and context name. The snapshot is taken under the
// read lock and the callbacks run OUTSIDE it, so a slow visitor
// (listing CRDs over the network) never stalls cluster switching.
//
// Multi-tenant note: pool keys carry the owning org, and the active
// slot carries activeTenant — the visitor receives the right tenant
// for EVERY runtime, which is what lets a background sweep stamp
// findings without a request context.
//
// That last sentence used to be a promise this function did not keep.
// Pool entries are safe by construction (the org is part of the key),
// but the ACTIVE slot carries activeTenant, which is only set when the
// cluster was connected through an org-aware path — and nothing stopped
// an empty one from reaching the visitor. Measured 2026-08-07 on dev: a
// single pass wrote 1013 findings with an empty tenant_id, a complete
// copy of the org's own set. Under RLS they are unreachable forever, and
// PruneOrg cannot collect them either — it is scoped to an org, and no
// caller passes the empty one. Invisible, immortal, and counted by
// nothing.
//
// So a runtime with no org is now SKIPPED and logged at ERROR while
// multi-tenant is on. Fail closed: a sweep that misses one pass is
// recoverable, garbage in an RLS table is not. Single-tenant editions
// legitimately have no org, so the guard does not apply there.
func (m *Manager) ForEachLiveConnector(fn func(tenant, contextName string, conn *Connector)) {
	type entry struct {
		tenant, ctxName string
		conn            *Connector
	}
	m.mu.RLock()
	entries := make([]entry, 0, 1+len(m.runtimes))
	if m.connector != nil && m.connErr == nil {
		// OSS: the active slot belongs to the single tenant (m.tenantID); EE carries
		// the owning org in activeTenant.
		entries = append(entries, entry{m.tenantID, m.activeContext, m.connector})
	}
	for pk, rt := range m.runtimes {
		if rt.connector != nil && rt.connErr == nil {
			entries = append(entries, entry{pk.tenant, pk.cluster, rt.connector})
		}
	}
	m.mu.RUnlock()
	for _, e := range entries {
		if auth.MultiTenantEnabled && e.tenant == "" {
			slog.Error("sweep iterate: live runtime has no owning org — skipped",
				slog.String("context", e.ctxName))
			continue
		}
		fn(e.tenant, e.ctxName, e.conn)
	}
}
