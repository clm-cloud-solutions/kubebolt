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

// Non-unicast destinations are not endpoints: the IPv6 Router Solicitation every
// pod sends when its interface comes up (ff02::2, dropped on an IPv4-only
// datapath) drew a red edge to "(external)" after each restart. Link-local
// unicast stays: the metadata service and the EKS Pod Identity agent are real
// dependencies.
func TestIsExternalPeer(t *testing.T) {
	cases := map[string]bool{
		"":                false,
		"ff02::2":         false, // all-routers (Router Solicitation)
		"ff02::1:ff00:1":  false, // solicited-node (neighbor discovery)
		"ff01::1":         false, // interface-local multicast
		"224.0.0.251":     false, // mDNS
		"239.255.255.250": false, // SSDP
		"0.0.0.0":         false,
		"::":              false,
		"169.254.169.254": true, // instance metadata
		"169.254.170.23":  true, // EKS Pod Identity agent
		"fe80::1":         true,
		"10.42.116.155":   true,
		"3.5.129.105":     true,
		"2600:1f16::1":    true,
		"not-an-ip":       true,
	}
	for ip, want := range cases {
		if got := isExternalPeer(ip); got != want {
			t.Errorf("isExternalPeer(%q) = %v, want %v", ip, got, want)
		}
	}
}

func externalFlowRow(srcPod, ip, verdict string, val float64) vmRow {
	return vmRow{
		Labels: map[string]string{
			"source_namespace": "shop",
			"source_pod":       srcPod,
			"destination_ip":   ip,
			"verdict":          verdict,
		},
		Value: val,
	}
}

// An external row becomes a pod edge, an external edge or nothing; the API
// server's addresses (EKS control-plane ENIs in the VPC) get its in-cluster
// name instead of showing up as bare IPs, unless the pod's own DNS named them.
func TestExternalPeersEdges(t *testing.T) {
	p := externalPeers{
		podByIP:   map[string]podRef{"10.42.5.7": {namespace: "shop", name: "db-0"}},
		inPodCIDR: func(ip string) bool { return ip == "10.244.1.9" },
		apiServer: map[string]bool{"10.42.89.28": true, "10.42.101.135": true},
		dns: map[dnsKey]dnsValue{
			{srcNs: "shop", srcPod: "api", ip: "3.5.129.105"}:     {fqdn: "s3.us-east-2.amazonaws.com", rate: 1},
			{srcNs: "shop", srcPod: "agent", ip: "10.42.101.135"}: {fqdn: "ABC.gr7.us-east-2.eks.amazonaws.com", rate: 1},
		},
	}
	got := p.edges([]vmRow{
		externalFlowRow("api", "10.42.5.7", "forwarded", 1),       // live pod
		externalFlowRow("api", "10.244.1.9", "forwarded", 1),      // stale pod IP
		externalFlowRow("api", "ff02::2", "dropped", 0.01),        // Router Solicitation
		externalFlowRow("api", "3.5.129.105", "forwarded", 2),     // DNS-named
		externalFlowRow("api", "10.42.89.28", "forwarded", 3),     // API server, no DNS
		externalFlowRow("agent", "10.42.101.135", "forwarded", 4), // API server, own DNS wins
		externalFlowRow("api", "52.15.162.77", "forwarded", 5),    // unnamed
		externalFlowRow("api", "3.5.88.59", "forwarded", 0),       // idle in the window
	})
	want := []FlowEdge{
		{SrcNamespace: "shop", SrcPod: "api", DstNamespace: "shop", DstPod: "db-0", Verdict: "forwarded", RatePerSec: 1},
		{SrcNamespace: "shop", SrcPod: "api", DstIP: "3.5.129.105", DstFqdn: "s3.us-east-2.amazonaws.com", Verdict: "forwarded", RatePerSec: 2},
		{SrcNamespace: "shop", SrcPod: "api", DstIP: "10.42.89.28", DstFqdn: apiServerFQDN, Verdict: "forwarded", RatePerSec: 3},
		{SrcNamespace: "shop", SrcPod: "agent", DstIP: "10.42.101.135", DstFqdn: "ABC.gr7.us-east-2.eks.amazonaws.com", Verdict: "forwarded", RatePerSec: 4},
		{SrcNamespace: "shop", SrcPod: "api", DstIP: "52.15.162.77", Verdict: "forwarded", RatePerSec: 5},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d edges, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("edge %d:\n got %+v\nwant %+v", i, got[i], want[i])
		}
	}
}
