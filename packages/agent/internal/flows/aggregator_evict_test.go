package flows

import (
	"testing"
	"time"

	flowpb "github.com/cilium/cilium/api/v1/flow"

	"github.com/kubebolt/kubebolt/packages/agent/internal/buffer"
)

// A key that stops moving is forgotten after idleTTL, so it stops being sent:
// before, every key lived as long as the agent pod and the backend read keys idle
// for hours as edges at rate 0 (finding #76). A key that keeps moving stays, and
// one that comes back after being forgotten restarts from 1.
func TestAggregator_ForgetsIdleKeys(t *testing.T) {
	buf := buffer.New(4096)
	agg := NewAggregator(buf, "cid", "cn", "node", "")
	t0 := time.Date(2026, 11, 10, 21, 0, 0, 0, time.UTC)
	now := t0
	agg.now = func() time.Time { return now }
	agg.Flush() // the clock starts at t0

	agg.Record(testExternalFlow("shop", "api", "3.5.129.105")) // S3, one address of many
	agg.Record(testPodFlow("shop", "api", "shop", "db", flowpb.Verdict_FORWARDED))
	now = t0.Add(5 * time.Second)
	agg.Flush()
	if got := samplesByMetricName(drainBuffer(buf), "pod_flow_external_events_total"); len(got) != 1 {
		t.Fatalf("the fresh external key must be sent: %d samples", len(got))
	}

	// The pod pair keeps talking; the S3 address never comes back.
	for i := 1; i <= 4; i++ {
		now = t0.Add(time.Duration(i) * 5 * time.Minute)
		agg.Flush()
		drainBuffer(buf) // only the last flush is under test
		agg.Record(testPodFlow("shop", "api", "shop", "db", flowpb.Verdict_FORWARDED))
	}
	now = now.Add(5 * time.Second)
	agg.Flush()
	samples := drainBuffer(buf)
	if got := samplesByMetricName(samples, "pod_flow_external_events_total"); len(got) != 0 {
		t.Errorf("an address idle for 20 min is still sent: %+v", got[0].Labels)
	}
	if got := samplesByMetricName(samples, "pod_flow_events_total"); len(got) == 0 {
		t.Error("a pair that keeps talking must stay")
	}
	if sizes := agg.Sizes(); sizes["externals"] != 0 || sizes["flows"] != 1 {
		t.Errorf("sizes after eviction: %v", sizes)
	}

	// Back after being forgotten: a new counter, from 1.
	agg.Record(testExternalFlow("shop", "api", "3.5.129.105"))
	now = now.Add(5 * time.Second)
	agg.Flush()
	got := samplesByMetricName(drainBuffer(buf), "pod_flow_external_events_total")
	if len(got) != 1 || got[0].Value != 1 {
		t.Fatalf("a returning key restarts at 1: %+v", got)
	}
}

// idleTTL 0 keeps every key, the behaviour before eviction existed.
func TestAggregator_NoTTLKeepsKeys(t *testing.T) {
	buf := buffer.New(1024)
	agg := NewAggregator(buf, "cid", "cn", "node", "")
	agg.idleTTL = 0
	now := time.Date(2026, 11, 10, 21, 0, 0, 0, time.UTC)
	agg.now = func() time.Time { return now }
	agg.Flush()
	agg.Record(testExternalFlow("shop", "api", "3.5.129.105"))
	now = now.Add(24 * time.Hour)
	agg.Flush()
	if got := samplesByMetricName(drainBuffer(buf), "pod_flow_external_events_total"); len(got) != 1 {
		t.Fatalf("with no TTL the key must survive: %d samples", len(got))
	}
}
