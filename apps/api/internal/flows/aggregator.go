package flows

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	flowpb "github.com/cilium/cilium/api/v1/flow"
)

// flowKey is the aggregation key for pod-to-pod flow counts. Source label
// records the provenance (hubble / mesh / conntrack / ebpf) so dashboards
// can filter or blend sources later without losing attribution.
type flowKey struct {
	srcNs   string
	srcPod  string
	dstNs   string
	dstPod  string
	verdict string
}

// Aggregator receives raw Hubble flows on Record and periodically writes
// cumulative counters to VictoriaMetrics via its Prometheus text-import
// endpoint. Counters grow for the lifetime of the process so that VM's
// rate() works naturally; cardinality is bounded by the count of active
// pod pairs.
type Aggregator struct {
	mu     sync.Mutex
	totals map[flowKey]uint64

	vmURL  string
	client *http.Client
}

func NewAggregator(vmURL string) *Aggregator {
	return &Aggregator{
		totals: make(map[flowKey]uint64),
		vmURL:  strings.TrimRight(vmURL, "/"),
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// Record increments the running counter for the pod pair this flow
// represents. Flows without pod identity on both ends are dropped — they
// describe host-to-host or world-to-host traffic that isn't useful for the
// pod-level cluster map.
func (a *Aggregator) Record(f *flowpb.Flow) {
	src, dst := f.GetSource(), f.GetDestination()
	if src == nil || dst == nil {
		return
	}
	if src.GetPodName() == "" || dst.GetPodName() == "" {
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

// Flush writes the current cumulative totals to VictoriaMetrics. Safe to
// call concurrently with Record.
func (a *Aggregator) Flush(ctx context.Context) error {
	a.mu.Lock()
	snapshot := make(map[flowKey]uint64, len(a.totals))
	for k, v := range a.totals {
		snapshot[k] = v
	}
	a.mu.Unlock()

	if len(snapshot) == 0 {
		return nil
	}

	var buf bytes.Buffer
	ts := time.Now().UnixMilli()
	for k, count := range snapshot {
		fmt.Fprintf(&buf,
			`pod_flow_events_total{src_namespace=%q,src_pod=%q,dst_namespace=%q,dst_pod=%q,verdict=%q,source="hubble"} %d %d`+"\n",
			k.srcNs, k.srcPod, k.dstNs, k.dstPod, k.verdict, count, ts,
		)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.vmURL+"/api/v1/import/prometheus", &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/plain")

	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("vm write: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("vm write status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// Size returns the number of distinct pod pairs currently tracked. Useful
// for health logging.
func (a *Aggregator) Size() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.totals)
}
