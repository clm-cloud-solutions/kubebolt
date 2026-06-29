package channel

import (
	"testing"
	"unicode/utf8"

	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"
)

// A single invalid-UTF-8 byte in a label fails the gRPC marshal of the whole
// MetricBatch and tears down the AgentChannel session. sanitizeSampleLabels is
// the last line of defense before the wire — it must scrub every key/value.
func TestSanitizeSampleLabels_ReplacesInvalidUTF8(t *testing.T) {
	bad := "tag-\xff\xfe-val"
	samples := []*agentv2.Sample{
		{Labels: map[string]string{"__name__": "kube_pod_info", "annotation": bad, "ok": "fine"}},
		{Labels: map[string]string{"__name__": "node_load1", "ok": "fine"}}, // clean — must be untouched
	}

	sanitizeSamples(samples)

	for _, s := range samples {
		for k, v := range s.Labels {
			if !utf8.ValidString(k) || !utf8.ValidString(v) {
				t.Fatalf("label %q=%q still invalid UTF-8 after sanitize", k, v)
			}
		}
	}
	if got := samples[0].Labels["ok"]; got != "fine" {
		t.Fatalf("good label on the bad sample was mutated: ok=%q", got)
	}
	if got := samples[1].Labels["ok"]; got != "fine" {
		t.Fatalf("clean sample was mutated: ok=%q", got)
	}
}

// metric_name is a dedicated proto field (#2), separate from the labels map (#4).
// It was the real sucal trigger — labels were clean, metric_name was not.
func TestSanitizeSamples_InvalidMetricName(t *testing.T) {
	samples := []*agentv2.Sample{
		{MetricName: "node_load\xff1", Labels: map[string]string{"node": "ok"}},
	}

	sanitizeSamples(samples)

	if !utf8.ValidString(samples[0].MetricName) {
		t.Fatalf("metric_name %q still invalid UTF-8 after sanitize", samples[0].MetricName)
	}
	if samples[0].Labels["node"] != "ok" {
		t.Fatalf("clean label mutated: node=%q", samples[0].Labels["node"])
	}
}

// Bad bytes in the KEY are also fatal to the marshal — scrub keys too.
func TestSanitizeSampleLabels_InvalidKey(t *testing.T) {
	samples := []*agentv2.Sample{
		{Labels: map[string]string{"bad\xffkey": "v", "__name__": "m"}},
	}

	sanitizeSamples(samples)

	for k := range samples[0].Labels {
		if !utf8.ValidString(k) {
			t.Fatalf("key %q still invalid UTF-8 after sanitize", k)
		}
	}
}

// nil sample / nil-or-empty labels must not panic.
func TestSanitizeSampleLabels_NilSafe(t *testing.T) {
	sanitizeSamples([]*agentv2.Sample{nil, {Labels: nil}, {}})
}
