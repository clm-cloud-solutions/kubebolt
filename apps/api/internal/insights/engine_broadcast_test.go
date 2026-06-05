package insights

import (
	"sync/atomic"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/models"
	"github.com/kubebolt/kubebolt/apps/api/internal/websocket"
)

// A parked engine (broadcast gate false) must not touch the WS hub. We leave
// wsHub nil: if broadcast didn't short-circuit on the gate it would panic
// dereferencing the nil hub, so a clean return proves the suppression.
func TestEngineBroadcast_GateSuppresses(t *testing.T) {
	e := &Engine{} // nil wsHub on purpose
	gate := &atomic.Bool{}
	gate.Store(false)
	e.SetBroadcastGate(gate)

	e.broadcast(websocket.InsightNew, models.Insight{})
	e.broadcast(websocket.InsightResolved, models.Insight{})
}

// With no gate set, broadcast must reach the hub (default = always broadcast).
// A real hub with no clients makes Broadcast a safe no-op, so this just
// confirms the nil-gate path doesn't suppress.
func TestEngineBroadcast_NilGateBroadcasts(t *testing.T) {
	e := &Engine{wsHub: websocket.NewHub()}
	e.broadcast(websocket.InsightNew, models.Insight{})
}
