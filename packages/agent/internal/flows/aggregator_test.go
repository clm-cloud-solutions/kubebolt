package flows

import (
	"testing"

	flowpb "github.com/cilium/cilium/api/v1/flow"

	"github.com/kubebolt/kubebolt/packages/agent/internal/buffer"
	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"
)

// TestAggregator_PromCanonicalLabels validates the v1.0 label rename:
// src_/dst_ prefixes flip to the source_/destination_ Hubble convention,
// which also aligns with Istio's source_workload / destination_workload
// labels for service-mesh metrics that the vmagent sidecar will scrape in
// Phase 2.
func TestAggregator_PromCanonicalLabels(t *testing.T) {
	buf := buffer.New(1024)
	agg := NewAggregator(buf, "test-cluster-id", "test-cluster", "kind-control-plane")

	// One pod-to-pod forwarded flow + one dropped flow + one external flow.
	agg.Record(testPodFlow("default", "client-1", "default", "server-1", flowpb.Verdict_FORWARDED))
	agg.Record(testPodFlow("default", "client-1", "default", "server-1", flowpb.Verdict_DROPPED))
	agg.Record(testExternalFlow("default", "client-1", "1.2.3.4"))
	agg.Flush()

	samples := drainBuffer(buf)
	if len(samples) == 0 {
		t.Fatal("Flush emitted zero samples")
	}

	t.Run("pod-to-pod flow uses source_*/destination_* labels", func(t *testing.T) {
		s := findSample(t, samples, "pod_flow_events_total")
		assertLabel(t, s, "source_namespace", "default")
		assertLabel(t, s, "source_pod", "client-1")
		assertLabel(t, s, "destination_namespace", "default")
		assertLabel(t, s, "destination_pod", "server-1")
	})

	t.Run("legacy src_/dst_ labels not emitted", func(t *testing.T) {
		for _, s := range samples {
			for _, legacy := range []string{"src_namespace", "src_pod", "dst_namespace", "dst_pod", "dst_ip"} {
				if _, has := s.Labels[legacy]; has {
					t.Errorf("metric %s emits legacy label %q", s.MetricName, legacy)
				}
			}
		}
	})

	t.Run("external flow uses destination_ip", func(t *testing.T) {
		ext := samplesByMetricName(samples, "pod_flow_external_events_total")
		if len(ext) == 0 {
			t.Fatal("expected pod_flow_external_events_total sample")
		}
		assertLabel(t, ext[0], "source_namespace", "default")
		assertLabel(t, ext[0], "source_pod", "client-1")
		assertLabel(t, ext[0], "destination_ip", "1.2.3.4")
	})

	t.Run("verdict label preserved", func(t *testing.T) {
		flowSamples := samplesByMetricName(samples, "pod_flow_events_total")
		var seenForwarded, seenDropped bool
		for _, s := range flowSamples {
			switch s.Labels["verdict"] {
			case "forwarded":
				seenForwarded = true
			case "dropped":
				seenDropped = true
			}
		}
		if !seenForwarded {
			t.Error("expected verdict=forwarded sample")
		}
		if !seenDropped {
			t.Error("expected verdict=dropped sample (must bypass EGRESS filter)")
		}
	})

	t.Run("source label propagated", func(t *testing.T) {
		s := findSample(t, samples, "pod_flow_events_total")
		assertLabel(t, s, "source", "hubble")
	})

	t.Run("cluster + node labels propagated", func(t *testing.T) {
		s := findSample(t, samples, "pod_flow_events_total")
		assertLabel(t, s, "cluster_id", "test-cluster-id")
		assertLabel(t, s, "cluster_name", "test-cluster")
		assertLabel(t, s, "node", "kind-control-plane")
	})
}

// --- helpers ---------------------------------------------------------------

func testPodFlow(srcNs, srcPod, dstNs, dstPod string, verdict flowpb.Verdict) *flowpb.Flow {
	return &flowpb.Flow{
		Verdict:          verdict,
		TrafficDirection: flowpb.TrafficDirection_EGRESS,
		Source:           &flowpb.Endpoint{Namespace: srcNs, PodName: srcPod},
		Destination:      &flowpb.Endpoint{Namespace: dstNs, PodName: dstPod},
	}
}

func testExternalFlow(srcNs, srcPod, dstIP string) *flowpb.Flow {
	return &flowpb.Flow{
		Verdict:          flowpb.Verdict_FORWARDED,
		TrafficDirection: flowpb.TrafficDirection_EGRESS,
		Source:           &flowpb.Endpoint{Namespace: srcNs, PodName: srcPod},
		Destination:      &flowpb.Endpoint{}, // empty pod_name = external
		IP:               &flowpb.IP{Destination: dstIP},
	}
}

func drainBuffer(buf *buffer.Ring) []*agentv2.Sample {
	return buf.PopBatch(10000)
}

func findSample(t *testing.T, samples []*agentv2.Sample, name string) *agentv2.Sample {
	t.Helper()
	for _, s := range samples {
		if s.MetricName == name {
			return s
		}
	}
	t.Fatalf("expected metric %q, got none in %d samples", name, len(samples))
	return nil
}

func samplesByMetricName(samples []*agentv2.Sample, name string) []*agentv2.Sample {
	var out []*agentv2.Sample
	for _, s := range samples {
		if s.MetricName == name {
			out = append(out, s)
		}
	}
	return out
}

func assertLabel(t *testing.T, s *agentv2.Sample, key, want string) {
	t.Helper()
	if got := s.Labels[key]; got != want {
		t.Errorf("metric %s: label %s = %q, want %q", s.MetricName, key, got, want)
	}
}
