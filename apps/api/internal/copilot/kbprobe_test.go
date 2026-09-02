package copilot

import "testing"

// TestKubebolDocsGet_RealWorldQueries pins that the queries Kobi actually
// issued in vivo (2026-08-31 screenshots) resolve to the intended refreshed
// entries instead of "Unknown topic" — 'admin-billing' was the miss that made
// Kobi improvise about plans.
func TestKubebolDocsGet_RealWorldQueries(t *testing.T) {
	cases := map[string]string{
		"admin-billing":   "plans-billing", // the in-vivo miss
		"billing":         "plans-billing",
		"plans":           "plans-billing",
		"connect-cluster": "add-cluster",
		"agent":           "agents",
	}
	for query, wantKey := range cases {
		want := kubebolt_docs[wantKey]
		if got := KubebolDocsGet(query); got != want {
			t.Errorf("KubebolDocsGet(%q) resolved to %.60q, want the %s entry", query, got, wantKey)
		}
	}
}
