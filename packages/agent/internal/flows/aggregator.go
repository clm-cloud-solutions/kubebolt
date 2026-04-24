package flows

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	flowpb "github.com/cilium/cilium/api/v1/flow"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/kubebolt/kubebolt/packages/agent/internal/buffer"
	agentv1 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v1"
)

// flowKey dedupes per-conversation samples. The aggregator groups raw
// Hubble events by (src_pod, dst_pod, verdict) and reports cumulative
// counts each tick so VM's rate() works without further massaging.
type flowKey struct {
	srcNs   string
	srcPod  string
	dstNs   string
	dstPod  string
	verdict string
}

// Aggregator turns a stream of Hubble flow events into pod_flow_events_total
// samples pushed to the agent's ring buffer. The shipper already owns the
// delivery path to the backend, so the aggregator's only job is dedup,
// count, and periodic flush into the same buffer the kubelet collectors
// use.
type Aggregator struct {
	mu     sync.Mutex
	totals map[flowKey]uint64

	buffer    *buffer.Ring
	clusterID string
	node      string
}

func NewAggregator(buf *buffer.Ring, clusterID, node string) *Aggregator {
	return &Aggregator{
		totals:    make(map[flowKey]uint64),
		buffer:    buf,
		clusterID: clusterID,
		node:      node,
	}
}

// Record increments the running counter for the pod pair this flow
// represents. Hubble emits up to four events per TCP conversation
// (request + reply × egress + ingress perspectives). Filter to
// initiator-egress only:
//
//   - IsReply=true           → same conversation in reverse; counting it
//                               would draw edges in both directions for
//                               every call.
//   - TrafficDirection≠EGRESS → ingress perspective of the same flow
//                                already captured on the sender's node.
//
// Dropped flows have no reply so is_reply filter is a no-op for them.
// Flows without pod identity on either side describe host / world
// traffic that isn't useful for the pod-level cluster map — dropped.
func (a *Aggregator) Record(f *flowpb.Flow) {
	src, dst := f.GetSource(), f.GetDestination()
	if src == nil || dst == nil {
		return
	}
	if src.GetPodName() == "" || dst.GetPodName() == "" {
		return
	}
	if f.GetIsReply() != nil && f.GetIsReply().GetValue() {
		return
	}
	if f.GetTrafficDirection() != flowpb.TrafficDirection_EGRESS {
		return
	}
	key := flowKey{
		srcNs:   src.GetNamespace(),
		srcPod:  src.GetPodName(),
		dstNs:   dst.GetNamespace(),
		dstPod:  dst.GetPodName(),
		verdict: strings.ToLower(f.GetVerdict().String()),
	}
	a.mu.Lock()
	a.totals[key]++
	a.mu.Unlock()
}

// Flush pushes the current cumulative totals as agentv1.Sample into the
// ring buffer. Safe to call concurrently with Record.
func (a *Aggregator) Flush() {
	a.mu.Lock()
	if len(a.totals) == 0 {
		a.mu.Unlock()
		return
	}
	snapshot := make(map[flowKey]uint64, len(a.totals))
	for k, v := range a.totals {
		snapshot[k] = v
	}
	a.mu.Unlock()

	ts := timestamppb.Now()
	samples := make([]*agentv1.Sample, 0, len(snapshot))
	for k, count := range snapshot {
		samples = append(samples, &agentv1.Sample{
			Timestamp:  ts,
			MetricName: "pod_flow_events_total",
			Value:      float64(count),
			Labels: map[string]string{
				"cluster_id":    a.clusterID,
				"node":          a.node,
				"src_namespace": k.srcNs,
				"src_pod":       k.srcPod,
				"dst_namespace": k.dstNs,
				"dst_pod":       k.dstPod,
				"verdict":       k.verdict,
				"source":        "hubble",
			},
		})
	}
	a.buffer.Push(samples)
}

// Size returns the number of distinct pod pairs tracked. Used for health
// logging and test inspection.
func (a *Aggregator) Size() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.totals)
}

// RunFlushLoop calls Flush every interval until ctx is cancelled.
func (a *Aggregator) RunFlushLoop(ctx context.Context, interval time.Duration) {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			a.Flush()
			slog.Debug("hubble aggregator flushed", slog.Int("pairs", a.Size()))
		}
	}
}
