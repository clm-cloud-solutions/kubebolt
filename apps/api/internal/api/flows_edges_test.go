package api

import "testing"

// mkFlowRow builds a pod-to-pod L4 event row for the given pair + verdict.
func mkFlowRow(src, dst, verdict string, val float64) vmRow {
	return vmRow{
		Labels: map[string]string{
			"source_namespace":      "kubebolt-system",
			"source_pod":            src,
			"destination_namespace": "kubebolt-system",
			"destination_pod":       dst,
			"verdict":               verdict,
		},
		Value: val,
	}
}

func pk(src, dst string) pairKey {
	return pairKey{srcNs: "kubebolt-system", srcPod: src, dstNs: "kubebolt-system", dstPod: dst}
}

// When an L7 CNP redirects web→api, Hubble emits BOTH a forwarded and a
// redirected L4 flow for the pair. The redirect is the ghost that carries no L7
// and must be dropped so the surviving forwarded edge (with L7) is the only one.
func TestBuildPodFlowEdges_DropsRedundantRedirect(t *testing.T) {
	rows := []vmRow{
		mkFlowRow("web", "api", "forwarded", 3.9),
		mkFlowRow("web", "api", "redirected", 4.0),
	}
	httpByPair := map[pairKey]*L7Summary{
		pk("web", "api"): {RequestsPerSec: 3.9},
	}
	edges := buildPodFlowEdges(rows, httpByPair)
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge (redirect ghost dropped), got %d: %+v", len(edges), edges)
	}
	if edges[0].Verdict != "forwarded" {
		t.Fatalf("expected forwarded, got %q", edges[0].Verdict)
	}
	if edges[0].L7 == nil || edges[0].L7.RequestsPerSec != 3.9 {
		t.Fatalf("L7 must stay attached to the surviving edge, got %+v", edges[0].L7)
	}
}

// A redirect with NO forwarded counterpart in the window is still a real,
// allowed (L7-proxied) flow — surface it, aliased to forwarded, and carry L7.
func TestBuildPodFlowEdges_LoneRedirectAliasedToForwarded(t *testing.T) {
	rows := []vmRow{mkFlowRow("web", "api", "redirected", 4.0)}
	httpByPair := map[pairKey]*L7Summary{pk("web", "api"): {RequestsPerSec: 2.0}}
	edges := buildPodFlowEdges(rows, httpByPair)
	if len(edges) != 1 || edges[0].Verdict != "forwarded" {
		t.Fatalf("lone redirect should alias to forwarded: %+v", edges)
	}
	if edges[0].L7 == nil {
		t.Fatal("aliased redirect should carry its L7")
	}
}

// Dropped flows never reached the proxy — they keep their verdict and get no L7.
func TestBuildPodFlowEdges_DroppedKeepsNoL7(t *testing.T) {
	rows := []vmRow{mkFlowRow("web", "api", "dropped", 1.0)}
	httpByPair := map[pairKey]*L7Summary{pk("web", "api"): {RequestsPerSec: 5}}
	edges := buildPodFlowEdges(rows, httpByPair)
	if len(edges) != 1 || edges[0].Verdict != "dropped" {
		t.Fatalf("dropped edge should survive unchanged: %+v", edges)
	}
	if edges[0].L7 != nil {
		t.Fatal("dropped edge must not carry L7")
	}
}

// A forwarded pair with no matching L7 series (no CNP / no HTTP) → nil L7.
func TestBuildPodFlowEdges_ForwardedWithoutL7(t *testing.T) {
	rows := []vmRow{mkFlowRow("a", "b", "forwarded", 1.0)}
	edges := buildPodFlowEdges(rows, map[pairKey]*L7Summary{})
	if len(edges) != 1 || edges[0].L7 != nil {
		t.Fatalf("forwarded edge without an L7 series should have nil L7: %+v", edges)
	}
}

// Distinct pairs are independent: a redirect on one pair must not be dropped
// because a DIFFERENT pair has a forwarded edge.
func TestBuildPodFlowEdges_RedirectDropIsPerPair(t *testing.T) {
	rows := []vmRow{
		mkFlowRow("web", "api", "forwarded", 3.0),
		mkFlowRow("cli", "db", "redirected", 1.0), // different pair, lone redirect
	}
	edges := buildPodFlowEdges(rows, map[pairKey]*L7Summary{})
	if len(edges) != 2 {
		t.Fatalf("expected both pairs' edges, got %d: %+v", len(edges), edges)
	}
}
