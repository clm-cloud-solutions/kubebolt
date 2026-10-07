package cluster

import (
	"testing"
	"time"
)

// The agent-proxy connect deadline (25s by default) used to govern every
// connect, so an in-cluster API on a cluster with a large Events collection
// was cut off before its own 45s cache-sync budget and reported as "agent may
// be stuck" — with no agent anywhere. Direct connections are bounded by the
// cache-sync deadline alone.
func TestConnectDeadline_OnlyAgentProxyContextsRaceTheOuterDeadline(t *testing.T) {
	if got := connectDeadline("", 25*time.Second); got != 0 {
		t.Errorf("direct (kubeconfig / in-cluster) connect raced against %s; want no outer deadline", got)
	}
	if got := connectDeadline("cluster-uid-1", 25*time.Second); got != 25*time.Second {
		t.Errorf("agent-proxy connect deadline = %s, want 25s", got)
	}
	if got := connectDeadline("cluster-uid-1", 0); got != 0 {
		t.Errorf("agent-proxy with the deadline disabled = %s, want 0", got)
	}
}
