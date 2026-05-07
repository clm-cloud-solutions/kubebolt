package flows

import (
	"context"
	"sync"
	"testing"
	"time"
)

// runElectionLoop tests cover the contract the Hubble flow collector
// relies on:
//
//   1. RE-ATTEMPT on return — the load-bearing guarantee. Without
//      this, client-go's leaderelection.RunOrDie returning (after
//      a transient apiserver timeout drops the lease) would
//      permanently kill flow collection. We saw this in production:
//      the lease was free for 2 days because nobody re-attempted.
//   2. CLEAN EXIT on ctx cancel — the agent shutdown path relies
//      on this. A loop that doesn't honour cancellation would
//      delay SIGTERM unbounded.
//   3. BACKOFF GROWS when attempts return immediately — protects
//      the apiserver from a runaway re-election storm if the
//      lease object can't be acquired (RBAC, network).
//   4. BACKOFF RESETS after a real term — distinguishes "blip,
//      held lease for minutes, lost it for a moment" from "spinning
//      because we can't even acquire". Reset means transient blips
//      don't escalate to maxbackoff.

func TestRunElectionLoopReAttemptsAfterReturn(t *testing.T) {
	// Attempt returns immediately every time. Loop should iterate
	// multiple times before the test deadline kills the context.
	var attempts int
	var mu sync.Mutex
	attempt := func(_ context.Context) {
		mu.Lock()
		attempts++
		mu.Unlock()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	// Initial 5ms backoff, max 20ms. With ~80ms window the loop
	// should fit at least 3 attempts (5ms + 10ms + 20ms = 35ms of
	// waits, plus negligible work in attempt itself).
	runElectionLoop(ctx, attempt, time.Hour /* never reset */, 5*time.Millisecond, 20*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if attempts < 3 {
		t.Errorf("expected >=3 attempts (loop must re-attempt after return), got %d", attempts)
	}
}

func TestRunElectionLoopExitsOnCanceledCtx(t *testing.T) {
	// Pre-canceled ctx: loop must exit without ever calling attempt.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var called bool
	attempt := func(_ context.Context) {
		called = true
	}

	done := make(chan struct{})
	go func() {
		runElectionLoop(ctx, attempt, time.Hour, time.Second, 10*time.Second)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("loop did not exit on pre-canceled ctx")
	}
	if called {
		t.Error("attempt called even though ctx was already canceled")
	}
}

func TestRunElectionLoopExitsMidSleepOnCancel(t *testing.T) {
	// The sleep between attempts must honour ctx.Done() so the agent
	// shutdown path is responsive. Without this, a SIGTERM during the
	// backoff sleep would block for the full backoff duration.
	attempt := func(_ context.Context) {
		// Returns immediately so the loop enters the sleep phase.
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	start := time.Now()
	go func() {
		// Long backoff so we'd notice if the cancel didn't break the sleep.
		runElectionLoop(ctx, attempt, time.Hour, 5*time.Second, 30*time.Second)
		close(done)
	}()

	// Let the loop run one attempt, then enter the 5s sleep.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("loop did not exit promptly on ctx cancel during backoff sleep")
	}
	elapsed := time.Since(start)
	if elapsed > 500*time.Millisecond {
		t.Errorf("loop took %v to exit after cancel; expected <500ms (sleep should respect ctx)", elapsed)
	}
}

func TestRunElectionLoopBackoffResetsAfterLongHold(t *testing.T) {
	// First attempt holds for ~50ms (longer than minHeldToReset=20ms),
	// simulating a real lease term followed by a transient loss.
	// Subsequent attempts return immediately. The loop should treat
	// the long first hold as a real term and reset backoff to
	// initial — so attempt 3 fires close to (initial) ms after
	// attempt 2, NOT (2 * initial) or larger.
	var attemptTimes []time.Time
	var mu sync.Mutex
	attempt := func(_ context.Context) {
		mu.Lock()
		attemptTimes = append(attemptTimes, time.Now())
		n := len(attemptTimes)
		mu.Unlock()
		if n == 1 {
			time.Sleep(50 * time.Millisecond)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	runElectionLoop(ctx, attempt, 20*time.Millisecond, 10*time.Millisecond, 100*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(attemptTimes) < 3 {
		t.Fatalf("expected >=3 attempts, got %d", len(attemptTimes))
	}
	// Gap between attempt 2 and attempt 3 reflects the backoff
	// AFTER attempt 2. Since attempt 2 returned immediately (held=0),
	// it's NOT eligible to reset — but the backoff was already
	// reset to initial AFTER attempt 1's long hold, then doubled
	// once after attempt 2's quick return. So gap[2-3] should be
	// around 2*initial = 20ms, comfortably less than 100ms.
	gap23 := attemptTimes[2].Sub(attemptTimes[1])
	if gap23 > 60*time.Millisecond {
		t.Errorf("gap between attempt 2 and 3 = %v; expected <=60ms (long hold should have reset backoff to initial=10ms, then doubled to 20ms)", gap23)
	}
}

func TestRunElectionLoopBackoffGrowsOnImmediateReturns(t *testing.T) {
	// If every attempt returns immediately (e.g., RBAC denial,
	// apiserver permanently unreachable), backoff must grow so we
	// don't hammer the apiserver. Verify by counting attempts in a
	// fixed window with no reset trigger — should be FEWER than
	// you'd get with a flat initial backoff.
	var attempts int
	var mu sync.Mutex
	attempt := func(_ context.Context) {
		mu.Lock()
		attempts++
		mu.Unlock()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	// Initial 5ms, max 80ms, never reset (minHeldToReset > window).
	// Sleeps between attempts: 5, 10, 20, 40, 80, 80, 80, ...
	// In 200ms window we fit ~5 attempts (cumulative waits hit ~155ms by attempt 5).
	// With a flat 5ms backoff (no growth) we'd fit ~30+ attempts.
	runElectionLoop(ctx, attempt, time.Hour, 5*time.Millisecond, 80*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if attempts >= 15 {
		t.Errorf("expected <15 attempts under exponential backoff, got %d (backoff isn't growing)", attempts)
	}
	if attempts < 3 {
		t.Errorf("expected >=3 attempts in 200ms even with backoff, got %d", attempts)
	}
}
