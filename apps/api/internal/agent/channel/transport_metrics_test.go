package channel

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// Unary requests are timed by result; a tunnel with no agent is a failure of
// the tunnel, recorded as not_connected. The gauge follows the registry.
func TestTransportMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	r := NewAgentRegistry()
	RegisterTransportMetrics(reg, r)
	t.Cleanup(func() { kubeRequestSeconds.Store(nil) })

	observeKubeRequest(tunnelOK, time.Now().Add(-200*time.Millisecond))
	observeKubeRequest(tunnelTimeout, time.Now().Add(-time.Second))

	if n := testutil.CollectAndCount(kubeRequestSeconds.Load()); n != 2 {
		t.Errorf("histogram series = %d, want 2 (ok, timeout)", n)
	}
	if v, err := testutil.GatherAndCount(reg, "kubebolt_agent_channels"); err != nil || v != 1 {
		t.Errorf("kubebolt_agent_channels series = %d (%v), want 1", v, err)
	}
}
