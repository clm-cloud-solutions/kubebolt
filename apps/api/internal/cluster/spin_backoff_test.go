package cluster

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/agent/channel"
)

func TestSpinBackoff_DoublesUpToTheCap(t *testing.T) {
	want := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 5 * time.Minute, 5 * time.Minute}
	for i, w := range want {
		if got := spinBackoff(i + 1); got != w {
			t.Errorf("attempt %d: backoff %s, want %s", i+1, got, w)
		}
	}
}

// backoffManager: an agent-proxy cluster with a live agent, so getOrSpinPooled
// gets past its "no agent" fast-fail and only the backoff can stop it.
func backoffManager(t *testing.T) (*Manager, poolKey) {
	t.Helper()
	m := newBareManager()
	reg := channel.NewAgentRegistry()
	m.SetAgentRegistry(reg)
	if _, err := m.AddAgentProxyCluster("c-lab", "Lab"); err != nil {
		t.Fatal(err)
	}
	reg.Register(channel.NewAgent("c-lab", "a1", "node-0", nil, []string{"kube-proxy"}, nil))
	return m, poolKey{tenant: m.tenantID, cluster: "agent:c-lab"}
}

// A connect that just failed is not restarted by the requests that keep
// arriving for the cluster (the UI polls every few seconds): each attempt
// restarts the whole initial LIST through the agent. In the lab that loop sent
// hundreds of MB in minutes and never connected.
func TestGetOrSpinPooled_AFailedConnectIsNotRestartedByEveryRequest(t *testing.T) {
	m, pk := backoffManager(t)
	cause := errors.New("connect to context agent:c-lab stopped: no response from the cluster for 25s")
	m.mu.Lock()
	m.recordSpinFailureLocked(pk, cause, time.Now())
	m.mu.Unlock()

	for i := 0; i < 3; i++ {
		if rt := m.getOrSpinPooled(pk.tenant, pk.cluster); rt != nil {
			t.Fatalf("request %d got a runtime while the backoff holds", i+1)
		}
	}
	m.mu.RLock()
	_, reserved := m.runtimes[pk]
	attempts := m.spinFailures[pk].attempts
	m.mu.RUnlock()
	if reserved || attempts != 1 {
		t.Fatalf("a connect was started during the backoff (placeholder %v, attempts %d)", reserved, attempts)
	}

	// The request is told why, and when the next attempt may start.
	err := m.ConnErrorFor(WithRuntimeKey(context.Background(), RuntimeKey{Tenant: pk.tenant, Cluster: pk.cluster}))
	if err == nil || !errors.Is(err, cause) || !strings.Contains(err.Error(), "next attempt in") {
		t.Fatalf("ConnErrorFor = %v, want the failure and the time to the next attempt", err)
	}
}

// After the backoff, the next request may try again, and that failure counts
// as one more attempt (a longer wait next time).
func TestSpinBackoff_ExpiresAndCountsAttempts(t *testing.T) {
	m, pk := backoffManager(t)
	now := time.Now()
	m.mu.Lock()
	m.recordSpinFailureLocked(pk, errors.New("first"), now.Add(-31*time.Second))
	backing := m.spinBackingOffLocked(pk, now)
	m.recordSpinFailureLocked(pk, errors.New("second"), now)
	attempts := m.spinFailures[pk].attempts
	again := m.spinBackingOffLocked(pk, now.Add(59*time.Second))
	m.mu.Unlock()
	if backing {
		t.Fatal("still backing off 31s after a first failure (30s)")
	}
	if attempts != 2 || !again {
		t.Fatalf("after a second failure: attempts %d, backing off at +59s %v; want 2 and true (1 min)", attempts, again)
	}
}

// An agent's Hello means the channel is up again: it clears the backoff, so
// the always-on spin (or the active cluster's retry) may connect right away.
func TestAddAgentProxyCluster_HelloClearsTheBackoff(t *testing.T) {
	m := newBareManager()
	m.SetAgentRegistry(channel.NewAgentRegistry()) // no live agent: the eager spin gives up quietly
	if _, err := m.AddAgentProxyCluster("c-lab", "Lab"); err != nil {
		t.Fatal(err)
	}
	pk := poolKey{tenant: m.tenantID, cluster: "agent:c-lab"}
	m.mu.Lock()
	m.recordSpinFailureLocked(pk, errors.New("stalled"), time.Now())
	m.mu.Unlock()

	if _, err := m.AddAgentProxyCluster("c-lab", "Lab"); err != nil { // the Hello
		t.Fatal(err)
	}
	m.mu.RLock()
	_, still := m.spinFailures[pk]
	m.mu.RUnlock()
	if still {
		t.Fatal("the backoff survived the agent's Hello")
	}
}
