// Package buffer implements a bounded FIFO buffer of samples between
// collectors and the shipper. When full, the oldest samples are dropped so
// that a slow or disconnected backend never blocks a collector.
package buffer

import (
	"sync"

	agentv1 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v1"
)

type Ring struct {
	mu       sync.Mutex
	samples  []*agentv1.Sample
	capacity int

	collectedTotal uint64
	droppedTotal   uint64
}

func New(capacity int) *Ring {
	if capacity <= 0 {
		capacity = 10_000
	}
	return &Ring{
		samples:  make([]*agentv1.Sample, 0, capacity),
		capacity: capacity,
	}
}

// Push appends samples, dropping the oldest ones if the ring would overflow.
func (r *Ring) Push(samples []*agentv1.Sample) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.collectedTotal += uint64(len(samples))

	overflow := len(r.samples) + len(samples) - r.capacity
	if overflow > 0 {
		if overflow >= len(r.samples) {
			// Incoming batch alone exceeds capacity; drop everything and keep
			// only the newest tail that fits.
			r.droppedTotal += uint64(len(r.samples) + overflow - r.capacity)
			r.samples = r.samples[:0]
			if overflow < len(samples) {
				r.droppedTotal += uint64(overflow)
				samples = samples[overflow:]
			} else {
				r.droppedTotal += uint64(len(samples) - r.capacity)
				samples = samples[len(samples)-r.capacity:]
			}
		} else {
			r.droppedTotal += uint64(overflow)
			r.samples = r.samples[overflow:]
		}
	}
	r.samples = append(r.samples, samples...)
}

// PopBatch removes and returns up to n samples. Returns nil when empty.
func (r *Ring) PopBatch(n int) []*agentv1.Sample {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.samples) == 0 {
		return nil
	}
	if n <= 0 || n > len(r.samples) {
		n = len(r.samples)
	}
	out := make([]*agentv1.Sample, n)
	copy(out, r.samples[:n])
	r.samples = r.samples[n:]
	return out
}

// Len returns the current number of buffered samples.
func (r *Ring) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.samples)
}

// Stats returns collected/dropped counters plus current occupancy for
// exporting in the Heartbeat RPC.
func (r *Ring) Stats() (collected, dropped uint64, current, capacity int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.collectedTotal, r.droppedTotal, len(r.samples), r.capacity
}
