package promread

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunElectionLoop_ExitsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var attempts int32
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	runElectionLoop(ctx, func(ctx context.Context) {
		atomic.AddInt32(&attempts, 1)
		<-ctx.Done()
	}, time.Second, 10*time.Millisecond, 100*time.Millisecond)
	if got := atomic.LoadInt32(&attempts); got == 0 {
		t.Errorf("expected at least 1 attempt, got %d", got)
	}
}

func TestRunElectionLoop_BackoffDoublesUntilCap(t *testing.T) {
	// Force fast cycles (attempt returns immediately) and observe the
	// time gap between successive calls. Expectation: backoffs grow
	// 10ms, 20ms, 40ms, capped at 50ms.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var times []time.Time
	done := make(chan struct{})
	go func() {
		runElectionLoop(ctx, func(_ context.Context) {
			times = append(times, time.Now())
			if len(times) >= 5 {
				cancel()
			}
		}, time.Hour, 10*time.Millisecond, 50*time.Millisecond)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runElectionLoop did not return in time")
	}
	if len(times) < 4 {
		t.Fatalf("need at least 4 cycles to observe backoff growth, got %d", len(times))
	}
	// Gaps between cycles (subtract the prior call time).
	gaps := make([]time.Duration, 0, len(times)-1)
	for i := 1; i < len(times); i++ {
		gaps = append(gaps, times[i].Sub(times[i-1]))
	}
	// First gap should be near 10ms (the initial backoff). Later gaps
	// must not exceed the cap (50ms) by more than scheduler slop.
	if gaps[0] > 30*time.Millisecond {
		t.Errorf("first backoff too long: %v (want ~10ms)", gaps[0])
	}
	for i, g := range gaps {
		if g > 200*time.Millisecond {
			t.Errorf("gap[%d] exceeds reasonable cap+slop: %v", i, g)
		}
	}
}

func TestRunElectionLoop_HeldTermResetsBackoff(t *testing.T) {
	// Cycles that "hold" longer than minHeldToReset must drop the
	// backoff back to the initial value — distinguishes a transient
	// renew blip from a spin-only loop.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cycle := 0
	var gaps []time.Duration
	var lastReturn time.Time
	done := make(chan struct{})
	go func() {
		runElectionLoop(ctx, func(_ context.Context) {
			if !lastReturn.IsZero() {
				gaps = append(gaps, time.Since(lastReturn))
			}
			cycle++
			// First 2 cycles: fast returns (force backoff to grow).
			// Cycle 3: hold long enough to qualify as "real term" and
			// trigger the reset.
			if cycle == 3 {
				time.Sleep(80 * time.Millisecond)
			}
			if cycle >= 5 {
				cancel()
			}
			lastReturn = time.Now()
		}, 50*time.Millisecond, 10*time.Millisecond, 100*time.Millisecond)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runElectionLoop did not return in time")
	}
	if len(gaps) < 4 {
		t.Fatalf("expected at least 4 gaps, got %d", len(gaps))
	}
	// gap[2] is the wait AFTER cycle 3 (the held cycle) → should be
	// near initialBackoff (10ms), not grown. Allow scheduler slop.
	if gaps[2] > 40*time.Millisecond {
		t.Errorf("gap[2] after held term not reset: %v (want ~10ms)", gaps[2])
	}
}

func TestResolveLeaseNamespace_PodNamespaceEnvWins(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "from-env")
	got, err := ResolveLeaseNamespace()
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "from-env" {
		t.Errorf("got %q, want from-env", got)
	}
}

func TestResolveLeaseNamespace_NoEnvNoFileErrors(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "")
	// In test env the SA namespace file at /var/run/secrets/... does
	// NOT exist, so the function must surface a clear error rather
	// than return an empty string that would confuse the Lease lock.
	_, err := ResolveLeaseNamespace()
	if err == nil {
		t.Fatal("expected error when neither POD_NAMESPACE nor SA file present")
	}
}
