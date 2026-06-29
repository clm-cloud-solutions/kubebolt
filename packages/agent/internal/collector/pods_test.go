package collector

import (
	"testing"
	"unicode/utf8"

	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"
)

// TestPodsCache_Enrich_SanitizesInvalidUTF8 guards the enrich path: a pod label
// value with invalid UTF-8 (e.g. metadata injected by a webhook that slipped
// past k8s validation) must not reach Sample.labels unsanitized, or
// proto.Marshal of the Sample fails and tears down the AgentChannel session.
func TestPodsCache_Enrich_SanitizesInvalidUTF8(t *testing.T) {
	c := &PodsCache{
		entries: map[string]podMeta{
			"uid-1": {
				Namespace: "ns",
				Name:      "pod",
				Labels: map[string]string{
					// 0xff/0xfe are never valid UTF-8 start bytes.
					"app": "web-" + string([]byte{0xff, 0xfe}),
				},
			},
		},
	}
	samples := []*agentv2.Sample{
		{Labels: map[string]string{"pod_uid": "uid-1"}},
	}

	c.Enrich(samples)

	for _, s := range samples {
		for k, v := range s.Labels {
			if !utf8.ValidString(k) {
				t.Errorf("label key %q is not valid UTF-8", k)
			}
			if !utf8.ValidString(v) {
				t.Errorf("label value for %q (%q) is not valid UTF-8", k, v)
			}
		}
	}
	// The propagated label must survive (sanitized), not be dropped.
	if _, ok := samples[0].Labels["label_app"]; !ok {
		t.Error("label_app missing after enrich (should be present, sanitized)")
	}
}
