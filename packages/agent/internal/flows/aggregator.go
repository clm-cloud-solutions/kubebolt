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

// httpKey extends flowKey with HTTP method + status class so the cluster
// map can color edges by response health without driving cardinality
// through the roof. Exact status codes get folded into 5 buckets
// (info / ok / redir / client_err / server_err).
type httpKey struct {
	srcNs       string
	srcPod      string
	dstNs       string
	dstPod      string
	method      string
	statusClass string
	verdict     string
}

// httpLatKey is httpKey minus status_class — latency is meaningful per
// call shape (method + pair) regardless of outcome.
type httpLatKey struct {
	srcNs  string
	srcPod string
	dstNs  string
	dstPod string
	method string
}

type latSummary struct {
	sumNs uint64
	count uint64
}

// Aggregator turns a stream of Hubble flow events into pod_flow_events_total
// samples pushed to the agent's ring buffer. The shipper already owns the
// delivery path to the backend, so the aggregator's only job is dedup,
// count, and periodic flush into the same buffer the kubelet collectors
// use.
type Aggregator struct {
	mu       sync.Mutex
	totals   map[flowKey]uint64
	httpReqs map[httpKey]uint64
	httpLat  map[httpLatKey]*latSummary

	buffer      *buffer.Ring
	clusterID   string
	clusterName string
	node        string
}

func NewAggregator(buf *buffer.Ring, clusterID, clusterName, node string) *Aggregator {
	return &Aggregator{
		totals:      make(map[flowKey]uint64),
		httpReqs:    make(map[httpKey]uint64),
		httpLat:     make(map[httpLatKey]*latSummary),
		buffer:      buf,
		clusterID:   clusterID,
		clusterName: clusterName,
		node:        node,
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
//
// L7 events (HTTP responses surfaced by Cilium's Envoy proxy when
// visibility is enabled) are a separate path: they skip the IsReply /
// direction filter because the proxy emits exactly one event per
// request/response pair, and we want the RESPONSE side where the
// status code + latency live.
func (a *Aggregator) Record(f *flowpb.Flow) {
	src, dst := f.GetSource(), f.GetDestination()
	if src == nil || dst == nil {
		return
	}
	if src.GetPodName() == "" || dst.GetPodName() == "" {
		return
	}

	// L7 HTTP: route responses into the http metric maps. L7 flows are
	// emitted by the proxy alongside the L4 kernel event — the L4 one
	// still feeds pod_flow_events_total below, the L7 one enriches.
	//
	// Cilium emits RESPONSE events with source/destination from the
	// proxy's view (server → client). Flip them so the HTTP metric
	// lines up with pod_flow_events_total's caller → callee convention
	// and the cluster map can correlate the two.
	if l7 := f.GetL7(); l7 != nil {
		if http := l7.GetHttp(); http != nil && l7.GetType() == flowpb.L7FlowType_RESPONSE {
			callerPod, callerNs := dst.GetPodName(), dst.GetNamespace()
			calleePod, calleeNs := src.GetPodName(), src.GetNamespace()
			method := strings.ToUpper(http.GetMethod())
			if method == "" {
				method = "UNKNOWN"
			}
			hk := httpKey{
				srcNs:       callerNs,
				srcPod:      callerPod,
				dstNs:       calleeNs,
				dstPod:      calleePod,
				method:      method,
				statusClass: statusClassFor(http.GetCode()),
				verdict:     strings.ToLower(f.GetVerdict().String()),
			}
			lk := httpLatKey{
				srcNs: hk.srcNs, srcPod: hk.srcPod,
				dstNs: hk.dstNs, dstPod: hk.dstPod,
				method: method,
			}
			a.mu.Lock()
			a.httpReqs[hk]++
			s := a.httpLat[lk]
			if s == nil {
				s = &latSummary{}
				a.httpLat[lk] = s
			}
			s.sumNs += l7.GetLatencyNs()
			s.count++
			a.mu.Unlock()
		}
		// L7 events are not L4 — don't double-count into pod_flow_events_total.
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

// statusClassFor folds an HTTP status code into one of five buckets so
// the cluster map can color edges without exploding label cardinality
// with every possible (src, dst, status_code) tuple. "unknown" catches
// code==0 which Cilium emits when the proxy couldn't parse a response.
func statusClassFor(code uint32) string {
	switch {
	case code == 0:
		return "unknown"
	case code >= 500:
		return "server_err"
	case code >= 400:
		return "client_err"
	case code >= 300:
		return "redir"
	case code >= 200:
		return "ok"
	case code >= 100:
		return "info"
	}
	return "unknown"
}

// Flush pushes the current cumulative totals as agentv1.Sample into the
// ring buffer. Safe to call concurrently with Record.
func (a *Aggregator) Flush() {
	a.mu.Lock()
	if len(a.totals) == 0 && len(a.httpReqs) == 0 && len(a.httpLat) == 0 {
		a.mu.Unlock()
		return
	}
	flowSnap := make(map[flowKey]uint64, len(a.totals))
	for k, v := range a.totals {
		flowSnap[k] = v
	}
	httpReqSnap := make(map[httpKey]uint64, len(a.httpReqs))
	for k, v := range a.httpReqs {
		httpReqSnap[k] = v
	}
	httpLatSnap := make(map[httpLatKey]latSummary, len(a.httpLat))
	for k, v := range a.httpLat {
		httpLatSnap[k] = *v
	}
	a.mu.Unlock()

	ts := timestamppb.Now()
	samples := make([]*agentv1.Sample, 0, len(flowSnap)+len(httpReqSnap)+2*len(httpLatSnap))

	// Inlined tag helper — adds cluster_name when the aggregator has
	// one configured. Keeps the sample-emission sites below compact.
	tag := func(m map[string]string) map[string]string {
		if a.clusterName != "" {
			m["cluster_name"] = a.clusterName
		}
		return m
	}

	for k, count := range flowSnap {
		samples = append(samples, &agentv1.Sample{
			Timestamp:  ts,
			MetricName: "pod_flow_events_total",
			Value:      float64(count),
			Labels: tag(map[string]string{
				"cluster_id":    a.clusterID,
				"node":          a.node,
				"src_namespace": k.srcNs,
				"src_pod":       k.srcPod,
				"dst_namespace": k.dstNs,
				"dst_pod":       k.dstPod,
				"verdict":       k.verdict,
				"source":        "hubble",
			}),
		})
	}

	for k, count := range httpReqSnap {
		samples = append(samples, &agentv1.Sample{
			Timestamp:  ts,
			MetricName: "pod_flow_http_requests_total",
			Value:      float64(count),
			Labels: tag(map[string]string{
				"cluster_id":    a.clusterID,
				"node":          a.node,
				"src_namespace": k.srcNs,
				"src_pod":       k.srcPod,
				"dst_namespace": k.dstNs,
				"dst_pod":       k.dstPod,
				"method":        k.method,
				"status_class":  k.statusClass,
				"verdict":       k.verdict,
				"source":        "hubble",
			}),
		})
	}

	// Latency is reported as cumulative sum_seconds + count so VM can
	// derive avg via `sum/count` without needing histogram buckets.
	// P95 / P99 would require histogram shape, which is Phase 2.
	for k, s := range httpLatSnap {
		base := tag(map[string]string{
			"cluster_id":    a.clusterID,
			"node":          a.node,
			"src_namespace": k.srcNs,
			"src_pod":       k.srcPod,
			"dst_namespace": k.dstNs,
			"dst_pod":       k.dstPod,
			"method":        k.method,
			"source":        "hubble",
		})
		samples = append(samples,
			&agentv1.Sample{
				Timestamp:  ts,
				MetricName: "pod_flow_http_latency_seconds_sum",
				Value:      float64(s.sumNs) / 1e9,
				Labels:     base,
			},
			&agentv1.Sample{
				Timestamp:  ts,
				MetricName: "pod_flow_http_latency_seconds_count",
				Value:      float64(s.count),
				Labels:     base,
			},
		)
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
