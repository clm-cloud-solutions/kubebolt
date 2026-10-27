package cluster

import (
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
)

// Single-tenant: the active slot has no org to match.
func TestLiveConnectorSingleTenant(t *testing.T) {
	prev := auth.MultiTenantEnabled
	auth.MultiTenantEnabled = false
	t.Cleanup(func() { auth.MultiTenantEnabled = prev })

	m := newBareManager()
	m.tenantID = "default"
	active := &Connector{}
	m.activeContext, m.connector = "kind-dev", active
	if got := m.LiveConnector("", "kind-dev"); got != active {
		t.Errorf("OSS active slot: %p", got)
	}
	if got := m.LiveConnector(auth.DefaultTenantName, "kind-dev"); got != active {
		t.Errorf("OSS active slot for the default tenant name: %p", got)
	}
	if got := m.LiveConnector("", "kind-other"); got != nil {
		t.Errorf("an unconnected context: %p", got)
	}
}
