package insights

import (
	"sync"
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/models"
	"github.com/kubebolt/kubebolt/apps/api/internal/websocket"
)

// Finding #108. The manager rebinds every engine's hooks while it holds its
// own lock (a cluster switch, an agent reconnect), and a hook may read the
// manager (one that routes a notification by the cluster's owner does). With
// the hook fired under the engine's lock, the two waited on each other forever
// and the whole API stopped. Here mgr stands for the manager's lock: the hook
// reads it, and a "switch" takes it and rebinds the hooks while the hook runs.
func TestEngineHooks_RebindWhileAHookReadsTheManager(t *testing.T) {
	for _, c := range []struct {
		name   string
		bind   func(e *Engine, fn func(models.Insight))
		passes []*ClusterState
	}{
		{"new insight", func(e *Engine, fn func(models.Insight)) { e.SetOnNewInsight(fn) },
			[]*ClusterState{crashLoopState()}},
		{"resolved insight", func(e *Engine, fn func(models.Insight)) { e.SetOnResolvedInsight(fn) },
			[]*ClusterState{crashLoopState(), {}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var mgr sync.RWMutex
			e := NewEngine(websocket.NewHub(), nil, "c", "t")
			inHook, mgrHeld, rebound := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var once sync.Once
			hook := func(models.Insight) {
				once.Do(func() {
					close(inHook)
					<-mgrHeld
					mgr.RLock() // resolveTeam → OwnershipStore
					mgr.RUnlock()
				})
			}
			c.bind(e, hook)

			// The switch: takes the manager's lock, then rebinds the hooks.
			go func() {
				<-inHook
				mgr.Lock()
				close(mgrHeld)
				c.bind(e, hook)
				mgr.Unlock()
				close(rebound)
			}()

			done := make(chan struct{})
			go func() {
				for _, s := range c.passes {
					e.Evaluate(s)
				}
				close(done)
			}()
			for _, ch := range []chan struct{}{done, rebound} {
				select {
				case <-ch:
				case <-time.After(5 * time.Second):
					t.Fatal("deadlock: the hook waits for the manager, the manager waits for the engine")
				}
			}
		})
	}
}

// A slow hook (a notification channel that takes its time) does not hold the
// engine: its insights are readable while the hook runs.
func TestEngineHooks_ASlowHookDoesNotHoldTheEngine(t *testing.T) {
	e := NewEngine(websocket.NewHub(), nil, "c", "t")
	release := make(chan struct{})
	inHook := make(chan struct{})
	e.SetOnNewInsight(func(models.Insight) {
		close(inHook)
		<-release
	})
	go e.Evaluate(crashLoopState())
	<-inHook
	read := make(chan int)
	go func() { read <- len(e.GetAllInsights()) }()
	select {
	case n := <-read:
		if n != 1 {
			t.Fatalf("insights while the hook runs = %d, want 1", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reading the engine waited for a slow hook")
	}
	close(release)
}
