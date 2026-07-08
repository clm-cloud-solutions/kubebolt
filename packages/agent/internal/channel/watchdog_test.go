package channel

import (
	"context"
	"testing"
	"time"
)

// wdStatser is a bufferStatser stub for the stall-watchdog tests.
type wdStatser struct {
	current, capacity int
	incDropped        bool
	dropped           uint64
}

func (s *wdStatser) Stats() (collected, dropped uint64, current, capacity int) {
	if s.incDropped {
		s.dropped++
	}
	return 0, s.dropped, s.current, s.capacity
}

// Saturated (current==capacity) AND dropping past the timeout → the watchdog
// must force a reconnect (call cancel).
func TestStallWatchdog_ForcesReconnectOnSustainedSaturation(t *testing.T) {
	oi, ot := stallWatchdogInterval, stallWatchdogTimeout
	stallWatchdogInterval, stallWatchdogTimeout = 5*time.Millisecond, 20*time.Millisecond
	defer func() { stallWatchdogInterval, stallWatchdogTimeout = oi, ot }()

	c := &Client{}
	st := &wdStatser{current: 100, capacity: 100, incDropped: true}
	fired := make(chan struct{})
	go c.stallWatchdog(context.Background(), func() { close(fired) }, st)

	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("watchdog did not force reconnect on sustained saturation + dropping")
	}
}

// A buffer that never saturates (shipper healthy) must NOT trigger a reconnect,
// even if the odd sample is dropped.
func TestStallWatchdog_QuietOnHealthyBuffer(t *testing.T) {
	oi, ot := stallWatchdogInterval, stallWatchdogTimeout
	stallWatchdogInterval, stallWatchdogTimeout = 5*time.Millisecond, 20*time.Millisecond
	defer func() { stallWatchdogInterval, stallWatchdogTimeout = oi, ot }()

	c := &Client{}
	st := &wdStatser{current: 10, capacity: 100, incDropped: true} // not saturated
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fired := make(chan struct{})
	go c.stallWatchdog(ctx, func() { close(fired) }, st)

	select {
	case <-fired:
		t.Fatal("watchdog forced a reconnect on a healthy (non-saturated) buffer")
	case <-time.After(150 * time.Millisecond):
		// good — stayed quiet
	}
}
