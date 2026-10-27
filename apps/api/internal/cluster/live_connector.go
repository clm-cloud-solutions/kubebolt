package cluster

// LiveConnector returns the connector that is already serving (tenant,
// contextName) — the active slot or a runtime parked in the pool — or nil. Unlike Connector(ctx) it never builds one: a fan-out that reads what
// other clusters expose must not cold-connect every cluster it can name (a
// connect costs up to the cache-sync timeout, and over an agent tunnel every
// informer's initial LIST). The rules for who owns the active slot are
// ListClusters' "connected" ones, so a cluster listed as connected is exactly
// one this returns a connector for.
//
// A pooled runtime still being built is not live: its fields are written
// outside the lock and published by closing ready, so it is read only once
// that close is observed, and never waited for.
func (m *Manager) LiveConnector(tenant, contextName string) *Connector {
	if contextName == "" {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if contextName == m.activeContext {
		if m.connector != nil && m.connErr == nil {
			return m.connector
		}
		return nil
	}
	rt, ok := m.runtimes[poolKey{tenant: tenant, cluster: contextName}]
	if !ok {
		return nil
	}
	if rt.ready != nil {
		select {
		case <-rt.ready:
		default:
			return nil
		}
	}
	if rt.connector == nil || rt.connErr != nil {
		return nil
	}
	return rt.connector
}
