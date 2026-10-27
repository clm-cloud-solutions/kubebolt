package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
	"github.com/kubebolt/kubebolt/apps/api/internal/config"
	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
	"github.com/kubebolt/kubebolt/apps/api/internal/mcpcatalog"
	"github.com/kubebolt/kubebolt/apps/api/internal/mcpgateway"
	"github.com/kubebolt/kubebolt/apps/api/internal/settings"
)

// fakeFlowVM answers the flow queries by metric name and records every query,
// so a test can check the scoping the dashboards rely on.
type fakeFlowVM struct {
	mu        sync.Mutex
	queries   []string
	external  []map[string]string // labels + "value"
	dnsWindow []map[string]string
	dnsHist   []map[string]string
	presence  string // value of the 15m presence probe; "" = no series
	failAll   bool
}

func (f *fakeFlowVM) serve(t *testing.T) {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		f.mu.Lock()
		f.queries = append(f.queries, q)
		fail := f.failAll
		f.mu.Unlock()
		if fail {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		var rows []map[string]string
		switch {
		case strings.Contains(q, "pod_flow_external_events_total"):
			rows = f.external
		case strings.Contains(q, "last_over_time(pod_dns_resolutions_total"):
			rows = f.dnsHist
		case strings.Contains(q, "pod_dns_resolutions_total"):
			rows = f.dnsWindow
		case strings.Contains(q, "count(count_over_time(pod_flow_events_total"):
			if f.presence != "" {
				rows = []map[string]string{{"value": f.presence}}
			}
		}
		type result struct {
			Metric map[string]string `json:"metric"`
			Value  [2]interface{}    `json:"value"`
		}
		res := []result{}
		for _, row := range rows {
			m := map[string]string{}
			for k, v := range row {
				if k != "value" {
					m[k] = v
				}
			}
			res = append(res, result{Metric: m, Value: [2]interface{}{1700000000, row["value"]}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": res}})
	}))
	t.Cleanup(ts.Close)
	t.Setenv("KUBEBOLT_METRICS_STORAGE_URL", ts.URL)
}

func (f *fakeFlowVM) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.queries...)
}

func extRow(ns, pod, ip, value string) map[string]string {
	return map[string]string{"source_namespace": ns, "source_pod": pod, "destination_ip": ip, "verdict": "forwarded", "value": value}
}

const rdsHost = "orders-db.c9x2k1q.us-east-1.rds.amazonaws.com"

func shopFlows() *fakeFlowVM {
	return &fakeFlowVM{
		external: []map[string]string{
			extRow("shop", "orders-7d9f8c-abcde", "10.0.3.17", "2"),
			extRow("shop", "orders-7d9f8c-fghij", "10.0.3.18", "1"),
			extRow("shop", "orders-7d9f8c-abcde", "169.254.169.254", "0.5"),
			extRow("shop", "orders-7d9f8c-abcde", "169.254.169.253", "0.1"),
			extRow("shop", "web-5c6d8f-xyz12", "203.0.113.10", "0.2"),
			extRow("other", "batch-6f7g8h-qwert", "10.0.9.9", "3"),
		},
		dnsWindow: []map[string]string{
			{"source_namespace": "shop", "source_pod": "orders-7d9f8c-abcde", "resolved_ip": "10.0.3.17", "fqdn": rdsHost + ".", "value": "0.01"},
		},
		dnsHist: []map[string]string{
			{"source_namespace": "shop", "resolved_ip": "10.0.3.18", "fqdn": rdsHost, "value": "3"},
		},
	}
}

// The whole path over a fake metrics store, without a connector (a
// metrics-only cluster): scoping, the external selection, the merge of a
// managed endpoint's two addresses under its hostname (one from the window's
// DNS, one from the history), the metadata service dropped, plumbing last,
// and every note that says what could not be seen.
func TestExternalConnectionsFromFlows(t *testing.T) {
	vm := shopFlows()
	vm.serve(t)
	h := &handlers{}
	ctx := auth.WithTenantID(context.Background(), "org-1")

	out := h.externalConnections(ctx, flowScope{uid: "uid-1", tenant: "org-1"}, copilot.ExternalConnectionsQuery{Namespace: "shop", WindowMinutes: 15})

	for _, q := range vm.seen() {
		if !strings.Contains(q, `cluster_id="uid-1"`) || !strings.Contains(q, `tenant_id="org-1"`) {
			t.Errorf("query not scoped to the cluster and the org: %s", q)
		}
	}
	if !out.SourcePresent || !out.ClusterUIDKnown || out.WindowMinutes != 15 {
		t.Fatalf("header wrong: %+v", out)
	}
	if len(out.Destinations) != 3 {
		t.Fatalf("want 3 destinations (rds, public address, aws dns), got %d: %+v", len(out.Destinations), out.Destinations)
	}

	db := out.Destinations[0]
	if db.FQDN != rdsHost || db.IP != "10.0.3.17" || db.IPCount != 2 || db.RatePerSec != 3 {
		t.Errorf("the two addresses of the RDS endpoint were not merged under its name: %+v", db)
	}
	if db.Provider != "aws" || db.Service != "rds" || db.ResourceHint != "orders-db" || db.Region != "us-east-1" || db.Confidence != "fqdn" {
		t.Errorf("RDS endpoint misclassified: %+v", db.ExternalClassification)
	}
	if db.ConnectorEnabled || len(db.FirstTools) != 0 {
		t.Errorf("no gateway: the connector cannot be enabled nor tools suggested: %+v", db.ExternalClassification)
	}
	if len(db.Callers) != 2 || db.Callers[0].Pod != "orders-7d9f8c-abcde" || db.Callers[0].RatePerSec != 2 {
		t.Errorf("without a connector callers are pods, busiest first: %+v", db.Callers)
	}

	if pub := out.Destinations[1]; pub.IP != "203.0.113.10" || pub.Provider != "" || pub.Infrastructure {
		t.Errorf("public address: %+v", pub)
	}
	if dns := out.Destinations[2]; dns.IP != "169.254.169.253" || !dns.Infrastructure {
		t.Errorf("the cloud resolver must be plumbing, sorted last: %+v", dns)
	}
	for _, d := range out.Destinations {
		if d.IP == "169.254.169.254" || d.IP == "10.0.9.9" {
			t.Errorf("destination %s must not appear (metadata service / other namespace)", d.IP)
		}
	}

	notes := strings.Join(out.Notes, "\n")
	for _, want := range []string{noteNoConnector, noteNoHostnames, "Cloud connectors are not available on this installation, so these AWS resources"} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes lack %q:\n%s", want, notes)
		}
	}
	// The history lookup asks only for the addresses the window left
	// unnamed, and stays scoped.
	var hist string
	for _, q := range vm.seen() {
		if strings.Contains(q, "last_over_time") {
			hist = q
		}
	}
	if !strings.Contains(hist, `10\\.0\\.3\\.18`) || strings.Contains(hist, `10\\.0\\.3\\.17`) {
		t.Errorf("DNS history query should filter on the unnamed addresses only: %s", hist)
	}
}

func TestExternalConnectionsWorkloadFilter(t *testing.T) {
	vm := shopFlows()
	vm.serve(t)
	out := (&handlers{}).externalConnections(context.Background(), flowScope{uid: "uid-1"},
		copilot.ExternalConnectionsQuery{Namespace: "shop", WorkloadKind: "Deployment", Workload: "orders", WindowMinutes: 15})
	if out.Workload != "Deployment/orders" {
		t.Errorf("workload echo = %q", out.Workload)
	}
	for _, d := range out.Destinations {
		for _, c := range d.Callers {
			if !strings.HasPrefix(c.Pod, "orders-") {
				t.Errorf("caller %s is not a pod of Deployment/orders", c.Pod)
			}
		}
		if d.IP == "203.0.113.10" {
			t.Error("web's destination leaked through the workload filter")
		}
	}
	if len(out.Destinations) != 2 || out.DependencyHosts != nil {
		t.Errorf("flows answered: want the RDS endpoint and the resolver, no fallback; got %+v", out)
	}
}

// No flow data at all: the answer must say it cannot see, and for a workload
// it falls back to the configuration — which, without a connector, it says it
// cannot read either.
func TestExternalConnectionsWithoutFlowData(t *testing.T) {
	vm := &fakeFlowVM{}
	vm.serve(t)
	out := (&handlers{}).externalConnections(context.Background(), flowScope{uid: "uid-1"},
		copilot.ExternalConnectionsQuery{Namespace: "shop", Workload: "orders", WindowMinutes: 15})
	if out.SourcePresent || len(out.Destinations) != 0 {
		t.Fatalf("no flows: %+v", out)
	}
	notes := strings.Join(out.Notes, "\n")
	for _, want := range []string{noteNoFlowSource, "cannot be read without a live connection"} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes lack %q:\n%s", want, notes)
		}
	}
	b, _ := json.Marshal(out)
	if !strings.Contains(string(b), `"destinations":[]`) {
		t.Errorf("destinations must render as []: %s", b)
	}

	// Flows present but quiet in the window.
	vm.presence = "4"
	out = (&handlers{}).externalConnections(context.Background(), flowScope{uid: "uid-1"}, copilot.ExternalConnectionsQuery{WindowMinutes: 5})
	if !out.SourcePresent || !strings.Contains(strings.Join(out.Notes, " "), "in the last 5 minutes") {
		t.Errorf("a quiet window must be told apart from no source: %+v", out)
	}
}

func TestExternalConnectionsBlindSpots(t *testing.T) {
	vm := &fakeFlowVM{}
	vm.serve(t)

	// No cluster at all: nothing is queried.
	out := (&handlers{}).externalConnections(context.Background(), flowScope{}, copilot.ExternalConnectionsQuery{})
	if out.ClusterUIDKnown || !strings.Contains(strings.Join(out.Notes, " "), noteNoCluster) || len(vm.seen()) != 0 {
		t.Errorf("no cluster: %+v, %d queries", out, len(vm.seen()))
	}

	// The metrics store is down: said, and not read as "no Hubble".
	vm.failAll = true
	out = (&handlers{}).externalConnections(context.Background(), flowScope{uid: "uid-1"}, copilot.ExternalConnectionsQuery{WindowMinutes: 15})
	notes := strings.Join(out.Notes, " ")
	if !strings.Contains(notes, "Flow data could not be read") || strings.Contains(notes, noteNoFlowSource) {
		t.Errorf("a failed read must not claim there is no flow source: %s", notes)
	}

	// Window clamping.
	out = (&handlers{}).externalConnections(context.Background(), flowScope{}, copilot.ExternalConnectionsQuery{WindowMinutes: 999})
	if out.WindowMinutes != 60 {
		t.Errorf("window = %d, want 60", out.WindowMinutes)
	}
}

func testView() *externalView {
	return &externalView{
		provider:  "AWS",
		region:    "us-east-1",
		internal:  map[netip.Addr]bool{netip.MustParseAddr("10.0.1.5"): true, netip.MustParseAddr("169.254.169.254"): true},
		apiServer: map[netip.Addr]bool{netip.MustParseAddr("10.0.0.10"): true},
		owner: func(_, pod string) (string, string) {
			if strings.HasPrefix(pod, "orders-") {
				return "Deployment", "orders"
			}
			return "Pod", pod
		},
		gateway: true,
		enabled: map[string]bool{"aws": true},
		// Autopilot: its connector note names only "not enabled" (Kobi's
		// also names the role floor), and its AWS allowlist admits every
		// call the fixtures suggest.
		consumer: mcpcatalog.ConsumerAutopilot,
		toolsOf: func(id string) map[string]bool {
			if id == "aws" {
				return map[string]bool{"call": true}
			}
			return nil
		},
	}
}

func edge(ns, pod, ip, fqdn string, rate float64) FlowEdge {
	return FlowEdge{SrcNamespace: ns, SrcPod: pod, DstIP: ip, DstFqdn: fqdn, Verdict: "forwarded", RatePerSec: rate}
}

// With a connector: node addresses are dropped, the API server is plumbing,
// a bare private address is attributed to the cluster's cloud with the
// lookup to run, pods collapse into their workload, and the org's AWS
// connector is marked enabled with only the tools it serves.
func TestAggregateExternalWithClusterState(t *testing.T) {
	v := testView()
	edges := []FlowEdge{
		edge("shop", "orders-7d9f8c-abcde", "10.0.1.5", "", 9),
		edge("shop", "orders-7d9f8c-abcde", "10.0.0.10", "", 0.3),
		edge("shop", "orders-7d9f8c-abcde", "10.0.3.50", "", 1),
		edge("shop", "orders-7d9f8c-fghij", "10.0.3.50", "", 1.5),
		edge("shop", "orders-7d9f8c-abcde", "10.0.3.17", rdsHost, 0.25),
	}
	dests := aggregateExternal(edges, v)
	if len(dests) != 3 {
		t.Fatalf("want private address, RDS, API server; got %+v", dests)
	}

	priv := dests[0]
	// No region for an address alone (the cluster's would read as the
	// resource's); the note says to look in the cluster's region instead.
	if priv.IP != "10.0.3.50" || priv.RatePerSec != 2.5 || priv.Provider != "aws" || priv.Confidence != "ip-in-provider" || priv.Region != "" ||
		!strings.Contains(priv.Note, "in us-east-1 (the cluster's region, where its network is)") {
		t.Errorf("private address: %+v", priv)
	}
	if !priv.ConnectorEnabled || len(priv.FirstTools) != 1 || !strings.HasPrefix(priv.FirstTools[0], "call ec2 DescribeNetworkInterfaces") {
		t.Errorf("private address must point at the enabled connector's lookup: %+v", priv.ExternalClassification)
	}
	if len(priv.Callers) != 1 || priv.Callers[0].Workload != "Deployment/orders" || priv.Callers[0].RatePerSec != 2.5 || priv.Callers[0].Pod != "" {
		t.Errorf("two pods of one Deployment are one caller: %+v", priv.Callers)
	}

	if rds := dests[1]; rds.FQDN != rdsHost || !rds.ConnectorEnabled || len(rds.FirstTools) == 0 {
		t.Errorf("RDS: %+v", rds)
	} else {
		for _, ft := range rds.FirstTools {
			if !strings.HasPrefix(ft, "call ") {
				t.Errorf("suggested tool %q is not one the connector serves", ft)
			}
		}
	}

	api := dests[2]
	if api.Service != "kube-apiserver" || !api.Infrastructure || api.Provider != "aws" || len(api.FirstTools) != 0 {
		t.Errorf("the API server must be plumbing with no tools: %+v", api)
	}

	// A connector that serves none of the suggested tools gets none.
	v.toolsOf = func(string) map[string]bool { return map[string]bool{"search_documentation": true} }
	for _, d := range aggregateExternal(edges[2:3], v) {
		if len(d.FirstTools) != 0 {
			t.Errorf("tools the connector does not serve were suggested: %v", d.FirstTools)
		}
	}
	// And an org without the connector: named, never tooled.
	v.enabled = map[string]bool{}
	d := aggregateExternal(edges[4:5], v)[0]
	if d.ConnectorEnabled || len(d.FirstTools) != 0 {
		t.Errorf("disabled connector: %+v", d.ExternalClassification)
	}
	notes := connectorNotes(copilot.ExternalConnectionsResult{Destinations: []copilot.ExternalDestination{d}}, v)
	if len(notes) != 1 || !strings.Contains(notes[0], "No AWS connector is enabled") || !strings.Contains(notes[0], "Administration → AI → Connectors") {
		t.Errorf("connector note: %v", notes)
	}
	// Kobi's connectors also follow a minimum role, and the gateway leaves
	// out one the user is below: the note must not tell a viewer that
	// nothing is set up.
	v.consumer = mcpcatalog.ConsumerKobi
	notes = connectorNotes(copilot.ExternalConnectionsResult{Destinations: []copilot.ExternalDestination{d}}, v)
	if len(notes) != 1 || !strings.Contains(notes[0], "No AWS connector is available here") || !strings.Contains(notes[0], "minimum role for Kobi is above this user's") {
		t.Errorf("Kobi connector note: %v", notes)
	}
}

func TestAggregateExternalCapsCallers(t *testing.T) {
	v := testView()
	var edges []FlowEdge
	for i := 0; i < 14; i++ {
		edges = append(edges, edge("jobs", fmt.Sprintf("worker-%02d", i), "10.0.3.60", "", float64(i+1)))
	}
	d := aggregateExternal(edges, v)[0]
	if len(d.Callers) != maxExternalCallers || d.MoreCallers != 4 || d.Callers[0].Pod != "worker-13" {
		t.Errorf("callers: %d kept, %d more, first %+v", len(d.Callers), d.MoreCallers, d.Callers[0])
	}
}

// Configured hosts are classified; a reference is passed through with its
// source only — its value was never read, so nothing is invented for it.
func TestDependencyRows(t *testing.T) {
	rows := dependencyRows([]cluster.DependencyHost{
		{Source: "env DATABASE_URL (container app)", Scheme: "postgres", Host: rdsHost, Port: "5432"},
		{Source: "secret db/url via env DATABASE_URL (container app): not read"},
	}, testView())
	if c := rows[0].ExternalClassification; c == nil || c.Service != "rds" || !c.ConnectorEnabled || c.ResourceHint != "orders-db" {
		t.Errorf("configured host not classified: %+v", rows[0])
	}
	if rows[1].ExternalClassification != nil {
		t.Errorf("a reference must not be classified: %+v", rows[1])
	}
	b, _ := json.Marshal(rows[1])
	if string(b) != `{"source":"secret db/url via env DATABASE_URL (container app): not read"}` {
		t.Errorf("reference rendered as %s", b)
	}
}

// A host reached through a Service of another namespace (the 2026-09-24
// miss: DB_HOST named shop-data/shop-db, an ExternalName to Azure) is a row
// like any other: classified from its host, with the chain as its source. The
// note that introduces the rows must say such rows exist, and every kind of
// host-less row: a Secret, a ConfigMap, unreadable Services, a chain that
// ends without an address.
func TestDependencyRowsThroughAnotherNamespace(t *testing.T) {
	const src = "env DB_HOST (container api) → service shop-data/shop-db (ExternalName)"
	rows := dependencyRows([]cluster.DependencyHost{
		{Source: src, Host: "pg-orders.postgres.database.azure.com", Port: "5432"},
		{Source: "services not readable in shop-data"},
	}, testView())
	if c := rows[0].ExternalClassification; rows[0].Source != src || c == nil || c.Provider != "azure" || c.Service != "azure-postgres" || c.ResourceHint != "pg-orders" {
		t.Errorf("chained host not classified: %+v", rows[0])
	}
	if rows[1].ExternalClassification != nil {
		t.Errorf("an unreadable Service must not be classified: %+v", rows[1])
	}
	for _, want := range []string{
		"in any namespace, followed to their targets",
		"a ConfigMap that is missing or unreadable",
		"a namespace whose Services KubeBolt cannot read",
		"a Service chain that ends without an address (no such Service, no endpoints, a loop, too many hops, back inside the cluster)",
	} {
		if !strings.Contains(noteFallback, want) {
			t.Errorf("noteFallback no longer says %q:\n%s", want, noteFallback)
		}
	}
}

func TestNotFoundAndShortError(t *testing.T) {
	if !isNotFound(errors.New(`deployment "orders" not found`)) {
		t.Error("the multi-namespace listers' miss must count as not found")
	}
	if isNotFound(&cluster.PermissionDeniedError{Resource: "deployments"}) {
		t.Error("a permission error is not a miss")
	}
	long := errors.New(strings.Repeat("q", 500))
	if got := shortError(long); len(got) > 204 || !strings.HasSuffix(got, "…") {
		t.Errorf("shortError kept %d bytes", len(got))
	}
}

func TestLookupDependencyHostsWithoutConnection(t *testing.T) {
	if got := lookupDependencyHosts(nil, "Deployment", "shop", "orders"); got.problem == "" || len(got.hosts) != 0 {
		t.Errorf("no connector: %+v", got)
	}
}

func TestFilterFirstTools(t *testing.T) {
	in := []string{"call rds DescribeDBInstances", "sqladmin_get_instance", "call cloudwatch GetMetricData"}
	if got := filterFirstTools(in, nil); len(got) != 3 {
		t.Errorf("an unknown tool set filters nothing: %v", got)
	}
	if got := filterFirstTools(in, map[string]bool{"call": true}); len(got) != 2 || got[0] != in[0] || got[1] != in[2] {
		t.Errorf("filtered = %v", got)
	}
	if got := filterFirstTools(in, map[string]bool{}); len(got) != 0 {
		t.Errorf("a known empty tool set filters everything: %v", got)
	}
}

// One executor answers Kobi and the Autopilot profiles. The first tools must
// be the caller's: Autopilot used to be told to start with Kobi's.
func TestExternalFirstToolsFollowTheConsumer(t *testing.T) {
	m := newMCPAPIHarness(t, nil)
	m.fb.mu.Lock()
	m.fb.autopilot = []string{"describe"}
	m.fb.mu.Unlock()
	if rec := m.do(http.MethodPut, "/admin/mcp-connectors/aws", enableBody); rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	kobi := auth.WithTenantID(context.Background(), auth.DefaultTenantName)
	autopilot := mcpcatalog.WithConsumer(kobi, mcpcatalog.ConsumerAutopilot)
	rds := func(ctx context.Context) copilot.ExternalDestination {
		v := m.h.externalViewFor(ctx, flowScope{})
		return aggregateExternal([]FlowEdge{edge("shop", "orders-1", "10.0.3.17", rdsHost, 1)}, v)[0]
	}

	if d := rds(kobi); !d.ConnectorEnabled || len(d.FirstTools) == 0 || !strings.HasPrefix(d.FirstTools[0], "call ") {
		t.Errorf("Kobi: %+v", d.ExternalClassification)
	}
	// Autopilot is served "describe", not "call": the connector is still
	// there, but nothing Kobi would start with is suggested.
	if d := rds(autopilot); !d.ConnectorEnabled || len(d.FirstTools) != 0 {
		t.Errorf("Autopilot: connectorEnabled=%v firstTools=%v, want enabled and no suggestions", d.ConnectorEnabled, d.FirstTools)
	}
	// Served nothing at all, next to Kobi's list: known, not unknown, so it
	// filters everything rather than nothing.
	m.fb.mu.Lock()
	m.fb.autopilot = []string{}
	m.fb.mu.Unlock()
	if d := rds(autopilot); len(d.FirstTools) != 0 {
		t.Errorf("Autopilot served no tools was suggested %v", d.FirstTools)
	}
	// A door with no connector tools at all (the public /mcp endpoint, an
	// Autopilot profile without cloud access) still learns the connector is
	// on, and is offered nothing to call.
	if d := rds(copilot.WithoutConnectorTools(kobi)); !d.ConnectorEnabled || len(d.FirstTools) != 0 {
		t.Errorf("no connector tools: connectorEnabled=%v firstTools=%v, want enabled and no suggestions", d.ConnectorEnabled, d.FirstTools)
	}
}

// newRealCatalogGateway is the shipped catalog behind a real gateway, its
// three connectors enabled in their self-hosted modes. Nothing binds: the
// admin view names each consumer's tools from the catalog itself.
func newRealCatalogGateway(t *testing.T) (*handlers, *mcpcatalog.Catalog, context.Context) {
	t.Helper()
	store, err := auth.NewStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("auth store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	rt, err := settings.NewRuntime(store, store, config.CopilotConfig{}, config.NotificationsConfig{}, config.AuthConfig{}, config.GeneralConfig{}, config.IngestChannelConfig{}, []byte(mcpTestJWT))
	if err != nil {
		t.Fatalf("settings runtime: %v", err)
	}
	// With the connectors not offered yet (Google Cloud): every kind of
	// destination extclassify knows keeps a connector to check against.
	cat, err := mcpcatalog.NewWithPreview()
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	gw := mcpgateway.New(cat, rt, mcpgateway.Options{AmbientAllowed: true, AzmcpURL: "http://127.0.0.1:5008"})
	t.Cleanup(gw.Close)
	ctx := auth.WithTenantID(context.Background(), auth.DefaultTenantName)
	on := true
	for _, c := range []struct{ id, mode, field, value string }{
		{"aws", "ambient", "region", "us-east-1"},
		{"azure", "sidecar", "subscriptionId", "00000000-0000-0000-0000-000000000001"},
		{"gcp", "adc", "projectId", "shop-prod"},
	} {
		mode, value := c.mode, c.value
		req := mcpgateway.PutRequest{Enabled: &on, Mode: &mode, Fields: map[string]*string{c.field: &value}}
		if _, err := gw.Put(ctx, c.id, req, "admin"); err != nil {
			t.Fatalf("enable %s: %v", c.id, err)
		}
	}
	return &handlers{mcpGateway: gw}, cat, ctx
}

// writeCountingOrgStore counts writes of the MCP connectors blob.
type writeCountingOrgStore struct {
	auth.OrgSettingStore
	mu     sync.Mutex
	writes int
}

func (s *writeCountingOrgStore) SetOrgSetting(ctx context.Context, key string, value []byte) error {
	if key == "mcp:connectors" {
		s.mu.Lock()
		s.writes++
		s.mu.Unlock()
	}
	return s.OrgSettingStore.SetOrgSetting(ctx, key, value)
}

func (s *writeCountingOrgStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writes
}

// get_external_connections is a read a model makes — a viewer's Kobi turn, an
// Autopilot token — and must not write the org's settings. An AWS connector
// saved before KubeBolt generated external IDs stays exactly as stored: the
// admin view would generate one, and that write would change the connector's
// fingerprint and rebuild its live binding from a read.
func TestExternalConnectionsWritesNoSettings(t *testing.T) {
	store, err := auth.NewStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("auth store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	orgStore := &writeCountingOrgStore{OrgSettingStore: store}
	rt, err := settings.NewRuntime(store, orgStore, config.CopilotConfig{}, config.NotificationsConfig{}, config.AuthConfig{}, config.GeneralConfig{}, config.IngestChannelConfig{}, []byte(mcpTestJWT))
	if err != nil {
		t.Fatalf("settings runtime: %v", err)
	}
	cat, err := mcpcatalog.New()
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	gw := mcpgateway.New(cat, rt, mcpgateway.Options{AmbientAllowed: true})
	t.Cleanup(gw.Close)
	ctx := auth.WithTenantID(context.Background(), auth.DefaultTenantName)
	on, mode, region := true, "ambient", "us-east-1"
	if err := rt.PutMCPConnector(ctx, "aws", settings.MCPConnectorPatch{Enabled: &on, Mode: &mode, Fields: map[string]*string{"region": &region}}); err != nil {
		t.Fatal(err)
	}
	writes := orgStore.count()

	h := &handlers{mcpGateway: gw}
	for _, consumer := range []mcpcatalog.Consumer{mcpcatalog.ConsumerKobi, mcpcatalog.ConsumerAutopilot} {
		v := h.externalViewFor(mcpcatalog.WithConsumer(ctx, consumer), flowScope{})
		for _, host := range []string{rdsHost, "media-assets.s3.us-east-1.amazonaws.com"} {
			d := classifyExternal(host, "", v)
			if !d.ConnectorEnabled {
				t.Errorf("%s %s: the enabled aws connector is not reported", consumer, host)
			}
		}
		if d := classifyExternal(rdsHost, "", v); len(d.FirstTools) == 0 {
			t.Errorf("%s: no first tool for RDS through the read-only door", consumer)
		}
	}
	if got := orgStore.count() - writes; got != 0 {
		t.Fatalf("get_external_connections wrote the org's settings %d time(s)", got)
	}
	all, err := rt.MCP(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := all.Connectors["aws"].Fields["externalId"]; got != "" {
		t.Fatalf("a read generated an external ID (%d characters)", len(got))
	}
}

// externalCorpus is one destination of each kind extclassify suggests first
// tools for, per cloud: hostnames, and a bare private address attributed to
// the cluster's own cloud.
var externalCorpus = []struct{ fqdn, ip, clusterProvider string }{
	{"mydb.c9akciq32.us-east-1.rds.amazonaws.com", "", ""},
	{"orders.cluster-c9akciq32.eu-west-1.rds.amazonaws.com", "", ""},
	{"analytics.cluster-custom-c9akciq32.eu-west-1.rds.amazonaws.com", "", ""},
	{"orders-proxy.proxy-c9akciq32.us-west-2.rds.amazonaws.com", "", ""},
	{"ro-ep.endpoint.proxy-c9akciq32.us-west-2.rds.amazonaws.com", "", ""},
	{"master.sessions.abc123.use1.cache.amazonaws.com", "", ""},
	{"sessions.abc123.clustercfg.euc1.cache.amazonaws.com", "", ""},
	{"carts-x1y2z3.serverless.usw2.cache.amazonaws.com", "", ""},
	{"clustercfg.rank.abc123.memorydb.us-east-1.amazonaws.com", "", ""},
	{"k8s-shop-ingress-1234567890.us-east-1.elb.amazonaws.com", "", ""},
	{"edge-nlb-0123456789abcdef.elb.us-east-2.amazonaws.com", "", ""},
	{"sqs.us-east-1.amazonaws.com", "", ""},
	{"sns.us-east-1.amazonaws.com", "", ""},
	{"media-assets.s3.us-east-1.amazonaws.com", "", ""},
	{"dynamodb.ap-northeast-1.amazonaws.com", "", ""},
	{"b-1.events.a1b2c3.c2.kafka.us-east-1.amazonaws.com", "", ""},
	{"123456789012.dkr.ecr.us-east-1.amazonaws.com", "", ""},
	{"fs-0123abcd.efs.us-east-1.amazonaws.com", "", ""},
	{"warehouse.abc123xyz.us-east-1.redshift.amazonaws.com", "", ""},
	{"ec2-54-1-2-3.compute-1.amazonaws.com", "", ""},
	{"", "10.0.3.17", "AWS"},
	{"orders-sql.database.windows.net", "", ""},
	{"pg-orders.postgres.database.azure.com", "", ""},
	{"my-orders.mysql.database.azure.com", "", ""},
	{"sessions.redis.cache.windows.net", "", ""},
	{"sessions.westeurope.redis.azure.net", "", ""},
	{"orders-bus.servicebus.windows.net", "", ""},
	{"catalog.documents.azure.com", "", ""},
	{"stmedia.blob.core.windows.net", "", ""},
	{"contosoprod.azurecr.io", "", ""},
	{"kv-prod.vault.azure.net", "", ""},
	{"", "172.16.4.5", "Azure"},
	{"sqladmin.googleapis.com", "", ""},
	{"1a2b3c4d5e6f.1a2b3c4d5e6f.us-central1.sql.goog", "", ""},
	{"alloydb.us-central1.rep.googleapis.com", "", ""},
	{"redis.googleapis.com", "", ""},
	{"pubsub.googleapis.com", "", ""},
	{"media-bucket.storage.googleapis.com", "", ""},
	{"db-vm-1.us-central1-a.c.my-project.internal", "", ""},
	{"", "192.168.10.3", "GCP"},
}

// Every first tool get_external_connections offers a consumer names a tool
// the shipped catalog serves that consumer, for every kind of destination
// extclassify knows, through the production view. Kobi and Autopilot are
// served different sets (Azure's keyvault and storage are Kobi's, and so are
// AWS calls to s3), so the answer must differ by who asks.
func TestExternalFirstToolsAreTheConsumersTools(t *testing.T) {
	h, cat, ctx := newRealCatalogGateway(t)
	served := func(c mcpcatalog.Consumer, id string) map[string]bool {
		def, ok := cat.Get(id)
		if !ok {
			t.Fatalf("no %s connector in the catalog", id)
		}
		lister, ok := def.Binder.(mcpgateway.ToolLister)
		if !ok {
			t.Fatalf("%s: the binder cannot name its tools", id)
		}
		out := map[string]bool{}
		for _, n := range lister.ToolNames(c) {
			out[n] = true
		}
		return out
	}

	offered := map[mcpcatalog.Consumer]map[string]bool{}
	for _, consumer := range []mcpcatalog.Consumer{mcpcatalog.ConsumerKobi, mcpcatalog.ConsumerAutopilot} {
		offered[consumer] = map[string]bool{}
		v := h.externalViewFor(mcpcatalog.WithConsumer(ctx, consumer), flowScope{})
		for _, c := range externalCorpus {
			v.provider, v.region = c.clusterProvider, ""
			got := classifyExternal(c.fqdn, c.ip, v)
			if got.Provider == "" || got.Infrastructure {
				t.Errorf("%s/%s: not attributed to a cloud: %+v", c.fqdn, c.ip, got)
				continue
			}
			if !got.ConnectorEnabled {
				t.Errorf("%s/%s: the %s connector is enabled but not reported", c.fqdn, c.ip, got.Provider)
			}
			tools := served(consumer, got.Provider)
			for _, ft := range got.FirstTools {
				f := strings.Fields(ft)
				local := f[0]
				if !tools[local] {
					t.Errorf("%s: %s/%s suggests %q, and %s does not serve %s", consumer, c.fqdn, c.ip, ft, got.Provider, local)
				}
				// AWS serves both consumers `call`; which services a call
				// may reach is the allowlist's decision, so the coverage
				// key is the service.
				key := got.Provider + " " + local
				if got.Provider == "aws" && len(f) == 3 {
					if !mcpcatalog.AWSCallAllowed(consumer, f[1], f[2]) {
						t.Errorf("%s: %s suggests %q, which the AWS allowlist refuses it", consumer, c.fqdn, ft)
					}
					key = got.Provider + " " + f[1]
					offered[consumer][key+" "+f[2]] = true
				}
				offered[consumer][key] = true
			}
		}
	}
	// The corpus reaches the tools the two consumers differ on: the check
	// above is not passing on an empty answer.
	// S3 offers both consumers its bucket reads; GetBucketLocation is Kobi's.
	for _, kobiOnly := range []string{"azure keyvault", "azure storage", "aws s3 GetBucketLocation"} {
		if !offered[mcpcatalog.ConsumerKobi][kobiOnly] {
			t.Errorf("Kobi was never offered %s", kobiOnly)
		}
		if offered[mcpcatalog.ConsumerAutopilot][kobiOnly] {
			t.Errorf("Autopilot was offered %s", kobiOnly)
		}
	}
	if len(offered[mcpcatalog.ConsumerAutopilot]) == 0 {
		t.Error("Autopilot was offered no first tool at all")
	}
}

func TestRoundRate(t *testing.T) {
	cases := map[float64]float64{0: 0, -1: 0, 1.23456: 1.23, 0.0021456: 0.00215, 1234.5: 1230, 0.1 + 0.2: 0.3}
	for in, want := range cases {
		if got := roundRate(in); got != want {
			t.Errorf("roundRate(%v) = %v, want %v", in, got, want)
		}
	}
	// encoding/json refuses these: one of them would turn the whole answer
	// into a marshal error.
	for _, in := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if got := roundRate(in); got != 0 {
			t.Errorf("roundRate(%v) = %v, want 0", in, got)
		}
	}
	d := aggregateExternal([]FlowEdge{edge("shop", "orders-7d9f8c-abcde", "10.0.3.60", "", math.Inf(1))}, testView())
	if _, err := json.Marshal(d); err != nil {
		t.Errorf("a non-finite rate broke the answer: %v", err)
	}
}

// ----- the flows refactor -----

// GET /flows/edges must keep its behaviour, and the ctx-based tenant rule is
// the handler's: an org id scopes, "default" does not.
func TestFlowEdgesHandlerScopesFromContext(t *testing.T) {
	cases := []struct {
		name       string
		tenant     string
		multi      bool
		wantTenant string // "" = no tenant_id matcher
	}{
		{"org", "org-7", false, `tenant_id="org-7"`},
		{"oss default", auth.DefaultTenantName, false, ""},
		{"multi-tenant without org fails closed", "", true, `tenant_id="` + noTenantSentinel + `"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prev := auth.MultiTenantEnabled
			auth.MultiTenantEnabled = tc.multi
			defer func() { auth.MultiTenantEnabled = prev }()

			vm := &fakeFlowVM{}
			vm.serve(t)
			req := httptest.NewRequest(http.MethodGet, "/flows/edges?window=5&namespace=shop", nil)
			ctx := req.Context()
			if tc.tenant != "" {
				ctx = auth.WithTenantID(ctx, tc.tenant)
			}
			rec := httptest.NewRecorder()
			(&handlers{}).handleFlowEdges(rec, req.WithContext(ctx))
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			}
			var resp FlowEdgesResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
			if resp.WindowMinutes != 5 || resp.Source != "hubble" || resp.SourcePresent {
				t.Errorf("response: %+v", resp)
			}
			qs := vm.seen()
			if len(qs) != 6 {
				t.Errorf("want the 5 flow queries plus the presence probe, got %d", len(qs))
			}
			for _, q := range qs {
				if !strings.Contains(q, `cluster_id="`+noClusterUIDSentinel+`"`) {
					t.Errorf("unknown cluster must fail closed: %s", q)
				}
				if tc.wantTenant == "" && strings.Contains(q, "tenant_id") {
					t.Errorf("OSS must not filter on tenant_id: %s", q)
				}
				if tc.wantTenant != "" && !strings.Contains(q, tc.wantTenant) {
					t.Errorf("query lacks %s: %s", tc.wantTenant, q)
				}
			}
		})
	}
}

func TestFlowEdgesHandlerUpstreamError(t *testing.T) {
	vm := &fakeFlowVM{failAll: true}
	vm.serve(t)
	rec := httptest.NewRecorder()
	(&handlers{}).handleFlowEdges(rec, httptest.NewRequest(http.MethodGet, "/flows/edges", nil))
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status %d, want 502", rec.Code)
	}
	rec = httptest.NewRecorder()
	(&handlers{}).handleFlowEdges(rec, httptest.NewRequest(http.MethodGet, "/flows/edges?window=x", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad window: status %d, want 400", rec.Code)
	}
}

// The lab's back cluster (nsg-egress-deny-pg, 2026-10-12): its nodes carry the
// group whose security group held the outbound rule that cut it off its
// database, and the answer must name it whenever a row is an Azure resource.
func TestAKSClusterNetwork(t *testing.T) {
	const group = "MC_rg-kubeshop-back_aks-kubeshop-back_eastus2"
	node := func(name string, l map[string]string) *corev1.Node {
		return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: l}}
	}
	nodes := []*corev1.Node{
		node("aks-nodepool1-0", map[string]string{"kubernetes.azure.com/network-resourcegroup": group, "kubernetes.azure.com/cluster": group}),
		node("aks-nodepool1-1", map[string]string{"kubernetes.azure.com/network-resourcegroup": group}),
	}
	cn := aksClusterNetwork(nodes, true)
	if cn == nil || cn.Provider != "azure" || len(cn.ResourceGroups) != 1 || cn.ResourceGroups[0] != group ||
		cn.From != "node label kubernetes.azure.com/network-resourcegroup" {
		t.Fatalf("cluster network: %+v", cn)
	}
	for _, want := range []string{group, "outbound rules", "group_resource_list", "network security group", "no need to list the account"} {
		if !strings.Contains(cn.Note, want) {
			t.Errorf("note lacks %q: %s", want, cn.Note)
		}
	}
	// A caller without the connector is told what to name, not which tool.
	if cn := aksClusterNetwork(nodes, false); cn == nil || strings.Contains(cn.Note, "group_resource_list") || !strings.Contains(cn.Note, "name this group's network security group") {
		t.Errorf("no connector: %+v", cn)
	}
	// Only the node resource group label: still named, and said where from.
	if cn := aksClusterNetwork([]*corev1.Node{node("n", map[string]string{"kubernetes.azure.com/cluster": group})}, true); cn == nil || cn.From != "node label kubernetes.azure.com/cluster" {
		t.Errorf("cluster label: %+v", cn)
	}
	// Not AKS.
	if cn := aksClusterNetwork([]*corev1.Node{node("kind-control-plane", nil)}, true); cn != nil {
		t.Errorf("a cluster without the labels has no cluster network: %+v", cn)
	}
}

func TestLeadsToProvider(t *testing.T) {
	pg := &copilot.ExternalClassification{Provider: "azure", Service: "postgres", Kind: "azure-postgres"}
	cases := []struct {
		name string
		r    copilot.ExternalConnectionsResult
		want bool
	}{
		{"configured database", copilot.ExternalConnectionsResult{DependencyHosts: []copilot.ExternalDependencyHost{{Host: "pg.postgres.database.azure.com", ExternalClassification: pg}}}, true},
		{"flow to a database", copilot.ExternalConnectionsResult{Destinations: []copilot.ExternalDestination{{FQDN: "pg.postgres.database.azure.com", ExternalClassification: *pg}}}, true},
		{"a Service of another cluster of the org", copilot.ExternalConnectionsResult{DependencyHosts: []copilot.ExternalDependencyHost{{Host: "57.165.100.6", ExternalClassification: &copilot.ExternalClassification{Provider: "azure"}, InFleet: &copilot.FleetMatch{Cluster: "back"}}}}, false},
		{"plumbing", copilot.ExternalConnectionsResult{Destinations: []copilot.ExternalDestination{{IP: "168.63.129.16", ExternalClassification: copilot.ExternalClassification{Provider: "azure", Infrastructure: true}}}}, false},
		{"another cloud", copilot.ExternalConnectionsResult{Destinations: []copilot.ExternalDestination{{FQDN: rdsHost, ExternalClassification: copilot.ExternalClassification{Provider: "aws"}}}}, false},
	}
	for _, c := range cases {
		if got := leadsToProvider(c.r, "azure"); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestEKSClusterNetwork(t *testing.T) {
	node := func(name, providerID string) *corev1.Node {
		return &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{corev1.LabelTopologyRegion: "us-east-1"}},
			Spec:       corev1.NodeSpec{ProviderID: providerID},
		}
	}
	nodes := []*corev1.Node{
		node("ip-10-0-1-10.ec2.internal", "aws:///us-east-1a/i-0bbb"),
		node("ip-10-0-2-20.ec2.internal", "aws:///us-east-1b/i-0aaa"),
		node("fargate-ip-10-0-3-30.ec2.internal", "aws:///us-east-1b/0f1e2d/fargate-ip-10-0-3-30.ec2.internal"),
	}
	cn := eksClusterNetwork(nodes, true)
	if cn == nil || cn.Provider != "aws" || cn.Region != "us-east-1" || cn.From != "node spec.providerID" ||
		len(cn.Instances) != 2 || cn.Instances[0] != "i-0aaa" || cn.Instances[1] != "i-0bbb" || len(cn.ResourceGroups) != 0 {
		t.Fatalf("cluster network: %+v", cn)
	}
	for _, want := range []string{"security groups", "network ACLs", "CloudTrail", "DescribeInstances", "no need to list the account"} {
		if !strings.Contains(cn.Note, want) {
			t.Errorf("note lacks %q: %s", want, cn.Note)
		}
	}
	if cn := eksClusterNetwork(nodes, false); cn == nil || strings.Contains(cn.Note, "DescribeInstances") || !strings.Contains(cn.Note, "name these nodes' security groups") {
		t.Errorf("no connector: %+v", cn)
	}
	// Fargate only, kind, another cloud: no instances, no cluster network.
	for _, id := range []string{"aws:///us-east-1b/0f1e2d/fargate-ip-10-0-3-30.ec2.internal", "kind://docker/kind/kind-control-plane", "azure:///subscriptions/x", ""} {
		if cn := eksClusterNetwork([]*corev1.Node{node("n", id)}, true); cn != nil {
			t.Errorf("%q: %+v", id, cn)
		}
	}
	// A large node group is cut to a few instances: they share their groups.
	var many []*corev1.Node
	for i := 0; i < 25; i++ {
		many = append(many, node(fmt.Sprintf("n%d", i), fmt.Sprintf("aws:///us-east-1a/i-%04d", i)))
	}
	if cn := eksClusterNetwork(many, true); cn == nil || len(cn.Instances) != maxClusterNetworkInstances {
		t.Errorf("many nodes: %+v", cn)
	}
}

// The way in, from the entry points the cluster holds: the load balancer's
// address classified, and with its target groups their health first.
func TestEntryRows(t *testing.T) {
	v := testView()
	v.consumer = mcpcatalog.ConsumerKobi
	tg := "arn:aws:elasticloadbalancing:us-east-2:111122223333:targetgroup/k8s-shopfron-shopweb-6b8a/2084"
	rows := entryRows([]cluster.EntryPoint{
		{Kind: "Ingress", Namespace: "shop-frontend", Name: "shop-web", Address: "shop-alb-1234567890.us-east-2.elb.amazonaws.com", TargetGroups: []string{tg}},
		{Kind: "Service", Namespace: "shop-frontend", Name: "pending", Address: ""},
	}, v)
	if len(rows) != 2 || rows[0].Via != "Ingress shop-frontend/shop-web" || rows[1].ExternalClassification != nil {
		t.Fatalf("rows = %+v", rows)
	}
	c := rows[0].ExternalClassification
	if c == nil || c.Provider != "aws" || c.Service != "elb" || !c.ConnectorEnabled || len(c.FirstTools) == 0 ||
		c.FirstTools[0] != "call elbv2 DescribeTargetHealth" {
		t.Fatalf("classification = %+v", c)
	}
	if !leadsToProvider(copilot.ExternalConnectionsResult{Entry: rows}, "aws") {
		t.Error("an AWS entry does not bring the cluster network")
	}
	// Without target groups the load balancer comes first, as for any address.
	rows = entryRows([]cluster.EntryPoint{{Kind: "Ingress", Namespace: "a", Name: "b", Address: "shop-alb-1234567890.us-east-2.elb.amazonaws.com"}}, v)
	if ft := rows[0].FirstTools; len(ft) == 0 || ft[0] == "call elbv2 DescribeTargetHealth" {
		t.Fatalf("first tools without target groups = %v", ft)
	}
}

// A read of the Ingress carries its entry: Kobi read the way in there in
// every run, never through get_external_connections.
func TestWithEntry(t *testing.T) {
	v := testView()
	v.consumer = mcpcatalog.ConsumerKobi
	tg := "arn:aws:elasticloadbalancing:us-east-2:111122223333:targetgroup/k8s-shopfron-shopweb-6b8a/2084"
	p := cluster.EntryPoint{Kind: "Ingress", Namespace: "shop-frontend", Name: "shop-web",
		Address: "shop-alb-1234567890.us-east-2.elb.amazonaws.com", Services: []string{"shop-web"}, TargetGroups: []string{tg}}
	out, ok := withEntry(`{"describe":"Name: shop-web"}`, p, "", v)
	if !ok {
		t.Fatal("no entry added")
	}
	var got struct {
		Describe  string                `json:"describe"`
		Entry     copilot.ExternalEntry `json:"entry"`
		EntryNote string                `json:"entryNote"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Describe != "Name: shop-web" || got.Entry.Via != "Ingress shop-frontend/shop-web" || len(got.Entry.TargetGroups) != 1 ||
		got.Entry.ExternalClassification == nil || got.Entry.FirstTools[0] != "call elbv2 DescribeTargetHealth" || !strings.Contains(got.EntryNote, "DescribeTargetHealth") {
		t.Fatalf("answer = %s", out)
	}
	if out, ok := withEntry(`{"describe":"x"}`, p, "targetgroupbindings (elbv2.k8s.aws) not readable", v); !ok || !strings.Contains(out, "could not be read: targetgroupbindings") {
		t.Fatalf("problem: %s", out)
	}
	// No address yet, or an answer that is not an object: left as it was.
	if _, ok := withEntry(`{"describe":"x"}`, cluster.EntryPoint{Kind: "Service"}, "", v); ok {
		t.Error("an entry without an address")
	}
	if _, ok := withEntry(`not json`, p, "", v); ok {
		t.Error("a text answer was rewritten")
	}
	for in, want := range map[string]string{"ingresses": "Ingress", "Ingress": "Ingress", "svc": "Service", "services": "Service", "pods": ""} {
		if got := entryKind(in); got != want {
			t.Errorf("entryKind(%q) = %q, want %q", in, got, want)
		}
	}
}

// nat-route-removed (2026-10-22): catalog-api's bucket was in
// FEATURED_BUCKET, and the rows named only its database. A bucket row is
// classified as S3 with its first calls; a bucket from a variable only on an
// AWS cluster, and it says how it was read.
func TestDependencyRowsBuckets(t *testing.T) {
	kobi := testView()
	kobi.consumer = mcpcatalog.ConsumerKobi
	rows := dependencyRows([]cluster.DependencyHost{
		{Source: "env FEATURED_BUCKET (container app)", Scheme: "bucket", Host: "tienda-featured"},
		{Source: "env EXPORT_URL (container app)", Scheme: "s3", Host: "tienda-exports"},
	}, kobi)
	for i, bucket := range []string{"tienda-featured", "tienda-exports"} {
		c := rows[i].ExternalClassification
		if c == nil || c.Provider != "aws" || c.Service != "s3" || c.ResourceHint != bucket || !c.ConnectorEnabled {
			t.Fatalf("%s: %+v", bucket, c)
		}
		if strings.Join(c.FirstTools, ",") != "call s3 GetBucketLocation,call s3 GetBucketEncryption,call s3 GetBucketPolicy" {
			t.Errorf("%s first tools: %v", bucket, c.FirstTools)
		}
		if rows[i].Host != bucket {
			t.Errorf("the row must keep the bucket as its host: %+v", rows[i])
		}
	}
	if !strings.HasPrefix(rows[0].ExternalClassification.Note, bucketVarNote) || strings.Contains(rows[1].ExternalClassification.Note, bucketVarNote) {
		t.Errorf("notes: %q / %q", rows[0].ExternalClassification.Note, rows[1].ExternalClassification.Note)
	}

	// On another cloud a variable's bucket is a lead with no classification.
	gke := testView()
	gke.provider = "GCP"
	other := dependencyRows([]cluster.DependencyHost{{Source: "env FEATURED_BUCKET (container app)", Scheme: "bucket", Host: "tienda-featured"}}, gke)
	if other[0].ExternalClassification != nil || other[0].Host != "tienda-featured" {
		t.Errorf("a bucket variable on GCP: %+v", other[0])
	}
}
