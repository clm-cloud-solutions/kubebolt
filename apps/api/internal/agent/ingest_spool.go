package agent

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"
)

// DefaultIngestSpoolBytes bounds what the API holds for VictoriaMetrics while
// it does not answer: 64 MiB of encoded samples, about 400k samples — four
// minutes of one production install's ingest, which covers a restart.
const DefaultIngestSpoolBytes = 64 << 20

// The drain retries the oldest held batch on this backoff, doubled on each
// failure and reset on a success.
const (
	spoolRetryMin = time.Second
	spoolRetryMax = 30 * time.Second
)

// SpoolingWriter writes agent samples to VictoriaMetrics and, when a write
// fails for a reason that passes (VictoriaMetrics restarting, unreachable, a
// 5xx or a 429), holds the encoded batch and writes it again once
// VictoriaMetrics answers. The agent's v2 channel has no acknowledgement: a
// batch the API could not write used to be gone, and every restart of
// VictoriaMetrics left a gap in every cluster. Samples carry their own
// timestamps, so a late write lands where it belongs.
//
// Bounded by bytes: over the bound the OLDEST held batches go first, so a long
// outage keeps its most recent minutes. A refusal that will not pass (a 4xx:
// the data itself was refused) is not held. The hold lives in this process:
// an API restarted together with VictoriaMetrics loses it.
type SpoolingWriter struct {
	w        *VMWriter
	maxBytes int

	mu     sync.Mutex
	queue  []spooled
	bytes  int
	wake   chan struct{}
	failed func(error)

	held    prometheus.Gauge
	samples *prometheus.CounterVec
}

type spooled struct {
	body    []byte
	samples int
}

// NewSpoolingWriter wraps w. maxBytes <= 0 holds nothing (plain writes). reg
// may be nil in tests that do not look at the series.
func NewSpoolingWriter(w *VMWriter, maxBytes int, reg prometheus.Registerer) *SpoolingWriter {
	s := &SpoolingWriter{
		w:        w,
		maxBytes: maxBytes,
		wake:     make(chan struct{}, 1),
		failed:   logSpoolFailures(),
		held: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "kubebolt_agent_grpc_ingest_spool_bytes",
			Help: "Encoded agent samples held for VictoriaMetrics while it does not answer.",
		}),
		samples: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kubebolt_agent_grpc_ingest_spool_samples_total",
			Help: "Agent samples that went through the ingest hold, by outcome: held (VictoriaMetrics did not take them), written (written later), dropped (over the bound, oldest first).",
		}, []string{"outcome"}),
	}
	if reg != nil {
		reg.MustRegister(s.held, s.samples)
	}
	return s
}

// Write writes samples now and holds them when the failure passes. The error
// is still returned, so the caller keeps logging the outage.
func (s *SpoolingWriter) Write(ctx context.Context, samples []*agentv2.Sample) error {
	body := encodeSamples(samples)
	if len(body) == 0 {
		return nil
	}
	err := s.w.post(ctx, body)
	if err == nil || s.maxBytes <= 0 || !transientWriteError(err) {
		return err
	}
	s.hold(spooled{body: body, samples: len(samples)})
	return err
}

func (s *SpoolingWriter) hold(b spooled) {
	if len(b.body) > s.maxBytes {
		s.samples.WithLabelValues("dropped").Add(float64(b.samples))
		return
	}
	s.mu.Lock()
	first := len(s.queue) == 0
	s.queue = append(s.queue, b)
	s.bytes += len(b.body)
	dropped := 0
	for s.bytes > s.maxBytes && len(s.queue) > 0 {
		s.bytes -= len(s.queue[0].body)
		dropped += s.queue[0].samples
		s.queue[0] = spooled{}
		s.queue = s.queue[1:]
	}
	s.held.Set(float64(s.bytes))
	s.mu.Unlock()
	s.samples.WithLabelValues("held").Add(float64(b.samples))
	if dropped > 0 {
		s.samples.WithLabelValues("dropped").Add(float64(dropped))
	}
	// Only the first held batch wakes the drain: while VictoriaMetrics is
	// down, its backoff paces the retries, not the rate batches arrive at.
	if first {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
}

// Held reports the batches and bytes waiting.
func (s *SpoolingWriter) Held() (batches, bytes int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.queue), s.bytes
}

// Run writes the held batches, oldest first, until ctx ends.
func (s *SpoolingWriter) Run(ctx context.Context) {
	delay := spoolRetryMin
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-time.After(delay):
		}
		for {
			b, ok := s.peek()
			if !ok {
				delay = spoolRetryMin
				break
			}
			if err := s.w.post(ctx, b.body); err != nil {
				if ctx.Err() != nil {
					return
				}
				if !transientWriteError(err) {
					// Refused for what it is: holding it would retry it forever.
					s.pop(b)
					s.samples.WithLabelValues("dropped").Add(float64(b.samples))
					continue
				}
				if s.failed != nil {
					s.failed(err)
				}
				delay = min(delay*2, spoolRetryMax)
				break
			}
			s.pop(b)
			s.samples.WithLabelValues("written").Add(float64(b.samples))
			delay = spoolRetryMin
		}
	}
}

func (s *SpoolingWriter) peek() (spooled, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queue) == 0 {
		return spooled{}, false
	}
	return s.queue[0], true
}

// pop removes b if it is still the oldest (the bound may have dropped it
// while it was being written).
func (s *SpoolingWriter) pop(b spooled) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queue) == 0 || &s.queue[0].body[0] != &b.body[0] {
		return
	}
	s.bytes -= len(b.body)
	s.queue[0] = spooled{}
	s.queue = s.queue[1:]
	s.held.Set(float64(s.bytes))
}

// transientWriteError: VictoriaMetrics did not take the write for a reason
// that passes — unreachable, timed out, restarting (5xx), or asking to slow
// down (429). A 4xx refused the data itself.
func transientWriteError(err error) bool {
	var st *vmStatusError
	if errors.As(err, &st) {
		return st.code >= 500 || st.code == 429
	}
	return true
}

// logSpoolFailures logs a failed retry at most once a minute.
func logSpoolFailures() func(error) {
	var mu sync.Mutex
	var last time.Time
	return func(err error) {
		mu.Lock()
		defer mu.Unlock()
		if time.Since(last) < time.Minute {
			return
		}
		last = time.Now()
		slog.Warn("agent ingest: VictoriaMetrics still not taking the held samples", slog.String("error", err.Error()))
	}
}
