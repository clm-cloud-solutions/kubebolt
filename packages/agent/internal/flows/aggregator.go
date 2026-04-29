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
	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"
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

// externalFlowKey tracks flows where the source is a pod and the
// destination is outside the cluster (empty pod name, non-empty IP).
// Emitted as a separate metric so pod_flow_events_total's label set
// stays exactly "pod pair + verdict" — the backend crosses the IP
// with DNS resolutions to turn these into "pod → fqdn" edges.
type externalFlowKey struct {
	srcNs   string
	srcPod  string
	dstIP   string
	verdict string
}

// dnsKey tracks observed DNS answers: which pod queried which FQDN
// and what IP(s) came back. Used downstream to label external flow
// edges with a human-readable hostname. Cardinality is bounded per
// pod by its actual DNS query pattern — workloads that talk to a
// handful of upstreams stay cheap.
type dnsKey struct {
	srcNs       string
	srcPod      string
	fqdn        string
	resolvedIP  string
}

// Aggregator turns a stream of Hubble flow events into pod_flow_events_total
// samples pushed to the agent's ring buffer. The shipper already owns the
// delivery path to the backend, so the aggregator's only job is dedup,
// count, and periodic flush into the same buffer the kubelet collectors
// use.
type Aggregator struct {
	mu        sync.Mutex
	totals    map[flowKey]uint64
	httpReqs  map[httpKey]uint64
	httpLat   map[httpLatKey]*latSummary
	externals map[externalFlowKey]uint64
	dns       map[dnsKey]uint64

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
		externals:   make(map[externalFlowKey]uint64),
		dns:         make(map[dnsKey]uint64),
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
	// Source needs to be a pod — flows originating from host / world
	// aren't useful for KubeBolt's pod-level map regardless of what
	// they're going to. Destination may be empty (external call) and
	// still be interesting; we branch on that below.
	if src.GetPodName() == "" {
		return
	}

	// L7 DNS: extract the FQDN a pod just resolved so the backend can
	// label external edges with a hostname. Only look at successful
	// RESPONSE events with answers — REQUEST events have no Ips, and
	// NXDOMAIN/SERVFAIL (Rcode != 0) means no resolution happened.
	// DNS is an L7 event so it bypasses the pod-to-pod filter below.
	//
	// Same flip as HTTP responses: Cilium's proxy emits DNS RESPONSE
	// events with src=DNS server and dst=querying pod (because the
	// response is flowing FROM the server TO the caller). We want
	// src_pod to be the pod that made the query, so use the dst side
	// of the event. src=coredns as the "asker" would be misleading —
	// it didn't ask, it answered.
	if l7 := f.GetL7(); l7 != nil && l7.GetDns() != nil {
		if dnsRec := l7.GetDns(); dnsRec != nil && l7.GetType() == flowpb.L7FlowType_RESPONSE && dnsRec.GetRcode() == 0 {
			fqdn := strings.TrimSuffix(dnsRec.GetQuery(), ".")
			askerPod := dst.GetPodName()
			askerNs := dst.GetNamespace()
			if fqdn != "" && askerPod != "" {
				a.mu.Lock()
				for _, ip := range dnsRec.GetIps() {
					if ip == "" {
						continue
					}
					a.dns[dnsKey{
						srcNs:      askerNs,
						srcPod:     askerPod,
						fqdn:       fqdn,
						resolvedIP: ip,
					}]++
				}
				a.mu.Unlock()
			}
		}
		// Fall through to HTTP handling (for HTTP-flavored L7 events
		// this is a no-op because we checked GetDns).
	}

	// Pod-to-external L4 flow: destination isn't a pod but has an IP.
	// Same IsReply / EGRESS filter as pod-to-pod so we count initiator
	// side only. Emitted as its own metric so existing pod_flow_events
	// consumers don't see a schema change.
	if dst.GetPodName() == "" {
		if f.GetL7() != nil {
			// L7 event already handled above; don't double count the
			// L4 counterpart.
			return
		}
		if f.GetIsReply() != nil && f.GetIsReply().GetValue() {
			return
		}
		if f.GetTrafficDirection() != flowpb.TrafficDirection_EGRESS {
			return
		}
		dstIP := f.GetIP().GetDestination()
		if dstIP == "" {
			return
		}
		a.mu.Lock()
		a.externals[externalFlowKey{
			srcNs:   src.GetNamespace(),
			srcPod:  src.GetPodName(),
			dstIP:   dstIP,
			verdict: strings.ToLower(f.GetVerdict().String()),
		}]++
		a.mu.Unlock()
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

// Flush pushes the current cumulative totals as agentv2.Sample into the
// ring buffer. Safe to call concurrently with Record.
func (a *Aggregator) Flush() {
	a.mu.Lock()
	if len(a.totals) == 0 && len(a.httpReqs) == 0 && len(a.httpLat) == 0 &&
		len(a.externals) == 0 && len(a.dns) == 0 {
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
	externalSnap := make(map[externalFlowKey]uint64, len(a.externals))
	for k, v := range a.externals {
		externalSnap[k] = v
	}
	dnsSnap := make(map[dnsKey]uint64, len(a.dns))
	for k, v := range a.dns {
		dnsSnap[k] = v
	}
	a.mu.Unlock()

	ts := timestamppb.Now()
	samples := make([]*agentv2.Sample, 0, len(flowSnap)+len(httpReqSnap)+2*len(httpLatSnap))

	// Inlined tag helper — adds cluster_name when the aggregator has
	// one configured. Keeps the sample-emission sites below compact.
	tag := func(m map[string]string) map[string]string {
		if a.clusterName != "" {
			m["cluster_name"] = a.clusterName
		}
		return m
	}

	for k, count := range flowSnap {
		samples = append(samples, &agentv2.Sample{
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
		samples = append(samples, &agentv2.Sample{
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
			&agentv2.Sample{
				Timestamp:  ts,
				MetricName: "pod_flow_http_latency_seconds_sum",
				Value:      float64(s.sumNs) / 1e9,
				Labels:     base,
			},
			&agentv2.Sample{
				Timestamp:  ts,
				MetricName: "pod_flow_http_latency_seconds_count",
				Value:      float64(s.count),
				Labels:     base,
			},
		)
	}

	// External flow events: pod → non-pod IP (outside-the-cluster
	// calls). Same shape as pod_flow_events_total but with dst_ip in
	// place of (dst_namespace, dst_pod). The backend crosses this with
	// DNS resolutions to turn dst_ip into a hostname where possible.
	for k, count := range externalSnap {
		samples = append(samples, &agentv2.Sample{
			Timestamp:  ts,
			MetricName: "pod_flow_external_events_total",
			Value:      float64(count),
			Labels: tag(map[string]string{
				"cluster_id":    a.clusterID,
				"node":          a.node,
				"src_namespace": k.srcNs,
				"src_pod":       k.srcPod,
				"dst_ip":        k.dstIP,
				"verdict":       k.verdict,
				"source":        "hubble",
			}),
		})
	}

	// DNS resolutions: (pod, fqdn) → ip pairs. Feeds the FQDN-labeling
	// of external edges. Only successful responses with answers land
	// here (Record filters NXDOMAIN / REQUEST events upstream).
	for k, count := range dnsSnap {
		samples = append(samples, &agentv2.Sample{
			Timestamp:  ts,
			MetricName: "pod_dns_resolutions_total",
			Value:      float64(count),
			Labels: tag(map[string]string{
				"cluster_id":    a.clusterID,
				"node":          a.node,
				"src_namespace": k.srcNs,
				"src_pod":       k.srcPod,
				"fqdn":          k.fqdn,
				"resolved_ip":   k.resolvedIP,
				"source":        "hubble",
			}),
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
