package copilot

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
)

func TestSearchTargets(t *testing.T) {
	clusters := []cluster.ClusterInfo{
		{Context: "kubeshop-back", Status: "connected"},                           // the selected one
		{Context: "kubeshop-front", Status: "disconnected", AgentConnected: true}, // agent-live
		{Context: "direct", Status: "connected"},
		{Context: "metrics", Status: "connected", Mode: "metrics-only"}, // no resource connector
		{Context: "gone", Status: "disconnected"},                       // would need a cold connect
	}
	var got []string
	for _, ci := range searchTargets(clusters, "kubeshop-back") {
		got = append(got, ci.Context)
	}
	if strings.Join(got, ",") != "kubeshop-front,direct" {
		t.Errorf("targets = %v", got)
	}
}

func hit(kind, ns, name string) map[string]interface{} {
	return map[string]interface{}{"kind": kind, "namespace": ns, "name": name}
}

func TestSearchOtherClusters(t *testing.T) {
	targets := []cluster.ClusterInfo{
		{Context: "agent:front", DisplayName: "kubeshop-front"},
		{Context: "empty"},
		{Context: "unreadable"},
		{Context: "slow"},
	}
	hits, answered, late := searchOtherClusters(targets, 200*time.Millisecond, func(ci cluster.ClusterInfo) ([]map[string]interface{}, bool) {
		switch ci.Context {
		case "agent:front":
			return []map[string]interface{}{hit("Service", "shop-frontend", "shop-web"), hit("Deployment", "shop-frontend", "shop-web")}, true
		case "empty":
			return nil, true
		case "unreadable":
			return nil, false
		default:
			time.Sleep(2 * time.Second) // a cold connect
			return []map[string]interface{}{hit("Deployment", "x", "shop-web")}, true
		}
	})
	if answered != 2 {
		t.Errorf("answered = %d, want 2 (front and empty)", answered)
	}
	if strings.Join(late, ",") != "slow,unreadable" {
		t.Errorf("not answered = %v", late)
	}
	if len(hits) != 2 || hits[0]["cluster"] != "kubeshop-front" || hits[0]["kind"] != "Deployment" {
		t.Errorf("hits must carry the cluster's display name, sorted: %v", hits)
	}
}

func TestElsewherePayload(t *testing.T) {
	found := elsewherePayload([]map[string]interface{}{{"cluster": "kubeshop-front", "kind": "Deployment", "namespace": "shop-frontend", "name": "shop-web"}}, 1, nil)
	if found["elsewhere"] == nil || !strings.Contains(found["note"].(string), "offer_cluster_switch") {
		t.Errorf("a hit elsewhere must point at the switch: %v", found)
	}
	if r, _ := found["results"].([]map[string]interface{}); r == nil || len(r) != 0 {
		t.Errorf("results stays the selected cluster's, empty: %v", found["results"])
	}

	none := elsewherePayload(nil, 2, nil)
	if !strings.Contains(none["note"].(string), "nor in the 2 other clusters") || none["elsewhere"] != nil {
		t.Errorf("searched and absent: %v", none)
	}

	partial := elsewherePayload(nil, 1, []string{"kubeshop-front"})
	note := partial["note"].(string)
	if !strings.Contains(note, "kubeshop-front did not answer") || !strings.Contains(note, "search again") {
		t.Errorf("a cluster that did not answer is not a cluster without it: %v", partial)
	}

	single := elsewherePayload(nil, 0, nil)
	if len(single) != 1 {
		t.Errorf("a single-cluster install answers as before: %v", single)
	}
}

// shop-web, found in the frontend cluster from the backend one, calls
// orders-api and catalog-api there: what it calls HERE is the lead that keeps
// the investigation here (three-tier-shop lab, 2026-10-10).
func TestCallsInto(t *testing.T) {
	back := &FleetMatch{Cluster: "kubeshop-back", ClusterContext: "agent:back", Kind: "Service", Namespace: "shop-backend", Name: "orders-api"}
	res := ExternalConnectionsResult{
		Destinations: []ExternalDestination{{IP: "203.0.113.10", InFleet: back}},
		DependencyHosts: []ExternalDependencyHost{
			{Source: "env ORDERS_URL", Host: "203.0.113.10", InFleet: back}, // the same Service, also in traffic
			{Source: "env CATALOG_URL", Host: "203.0.113.20", InFleet: &FleetMatch{ClusterContext: "agent:back", Kind: "Service", Namespace: "shop-backend", Name: "catalog-api"}},
			{Source: "env AUTH_URL", Host: "10.1.2.3", InFleet: &FleetMatch{ClusterContext: "agent:other", Kind: "Service", Namespace: "auth", Name: "auth"}},
			{Source: "env PAYMENTS_URL", Host: "api.stripe.com"}, // not in the fleet
		},
	}
	got := callsInto(res, "agent:back")
	want := []string{"Service shop-backend/orders-api (seen in its traffic)", "Service shop-backend/catalog-api (env CATALOG_URL)"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("callsInto = %q, want %q", got, want)
	}
	if got := callsInto(res, "agent:front"); len(got) != 0 {
		t.Errorf("nothing of it runs in the frontend cluster: %q", got)
	}
}

func TestAnnotateCallsHere(t *testing.T) {
	hits := []map[string]interface{}{
		hit("Service", "shop-frontend", "shop-web"), // no configuration to read
		hit("Deployment", "shop-frontend", "shop-web"),
		hit("Deployment", "a", "slow"),
		hit("Deployment", "b", "x"),
		hit("Deployment", "c", "y"), // over maxDependencyLookups
	}
	var mu sync.Mutex
	looked := map[string]bool{}
	annotateCallsHere(hits, 200*time.Millisecond, func(h map[string]interface{}) []string {
		mu.Lock()
		looked[h["name"].(string)] = true
		mu.Unlock()
		switch h["name"] {
		case "shop-web":
			return []string{"Service shop-backend/orders-api (env ORDERS_URL)"}
		case "slow":
			time.Sleep(2 * time.Second)
			return []string{"late"}
		}
		return nil
	})
	if hits[0]["callsHere"] != nil || hits[1]["callsHere"] == nil {
		t.Errorf("only the Deployment is looked up: %v / %v", hits[0], hits[1])
	}
	if hits[2]["callsHere"] != nil {
		t.Error("a lookup past the deadline must be left out")
	}
	mu.Lock()
	defer mu.Unlock()
	if looked["y"] {
		t.Errorf("more than %d lookups ran", maxDependencyLookups)
	}

	p := elsewherePayload(hits, 1, nil)
	if note := p["note"].(string); !strings.Contains(note, "callsHere") || !strings.Contains(note, "Check those here first") {
		t.Errorf("a hit that calls this cluster must keep the investigation here first: %q", note)
	}
}
