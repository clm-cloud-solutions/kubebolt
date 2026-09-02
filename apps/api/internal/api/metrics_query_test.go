package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
)

func TestScopeQueryByCluster(t *testing.T) {
	const uid = "cluster-uid-1"
	const inj = `cluster_id="cluster-uid-1"`

	tests := []struct {
		name string
		in   string
		want string
	}{
		// --- pass 1: existing `{...}` selectors -----------------------
		{
			name: "metric with non-empty selector",
			in:   `node_cpu_usage_seconds_total{node="n1"}`,
			want: `node_cpu_usage_seconds_total{` + inj + `,node="n1"}`,
		},
		{
			name: "metric with empty selector",
			in:   `node_cpu_usage_seconds_total{}`,
			want: `node_cpu_usage_seconds_total{` + inj + `}`,
		},
		{
			name: "selector already has cluster_id",
			in:   `node_cpu_usage_seconds_total{cluster_id="other-uid"}`,
			want: `node_cpu_usage_seconds_total{cluster_id="other-uid"}`,
		},
		{
			name: "rate over range vector with selector",
			in:   `rate(node_network_receive_bytes_total{node="n1"}[1m])`,
			want: `rate(node_network_receive_bytes_total{` + inj + `,node="n1"}[1m])`,
		},

		// --- pass 2: bare metric references ---------------------------
		// These are the OverviewPage / NodesPage shapes that previously
		// slipped through scoping and caused cross-cluster bleed.
		{
			name: "bare metric in sum",
			in:   `sum(rate(node_cpu_usage_seconds_total[1m]))`,
			want: `sum(rate(node_cpu_usage_seconds_total{` + inj + `}[1m]))`,
		},
		{
			name: "bare metric in sum-rate over range",
			in:   `sum(rate(node_network_receive_bytes_total[1m]))`,
			want: `sum(rate(node_network_receive_bytes_total{` + inj + `}[1m]))`,
		},
		{
			name: "bare metric standalone",
			in:   `node_fs_used_bytes`,
			want: `node_fs_used_bytes{` + inj + `}`,
		},
		{
			name: "sum by clause around bare metric",
			in:   `sum by (node) (rate(node_network_transmit_bytes_total[1m]))`,
			want: `sum by (node) (rate(node_network_transmit_bytes_total{` + inj + `}[1m]))`,
		},
		{
			name: "two bare metrics in arithmetic",
			in:   `sum(rate(node_cpu_usage_seconds_total[1m])) + sum(rate(container_cpu_usage_seconds_total[1m]))`,
			want: `sum(rate(node_cpu_usage_seconds_total{` + inj + `}[1m])) + sum(rate(container_cpu_usage_seconds_total{` + inj + `}[1m]))`,
		},

		// --- pass 1 + pass 2 mixed ------------------------------------
		{
			name: "mixed bare and selector forms in one query",
			in:   `sum(rate(node_cpu_usage_seconds_total[1m])) / sum(container_memory_working_set_bytes{pod="x"})`,
			want: `sum(rate(node_cpu_usage_seconds_total{` + inj + `}[1m])) / sum(container_memory_working_set_bytes{` + inj + `,pod="x"})`,
		},

		// --- safety: identifiers inside `{...}` aren't rewritten ------
		// `pod_uid` is a v1.0 label (not a metric) but starts with the
		// `pod_` prefix that bareMetricRE matches; pass 2 must skip it
		// when it appears inside a selector. Plain `pod` and `namespace`
		// don't match the regex prefix list and never collide.
		{
			name: "label inside selector with pod_ prefix is not rewritten",
			in:   `container_memory_working_set_bytes{namespace="ns",pod="p",pod_uid="abc"}`,
			want: `container_memory_working_set_bytes{` + inj + `,namespace="ns",pod="p",pod_uid="abc"}`,
		},

		// --- safety: short identifiers in `by(...)` not matched -------
		{
			name: "by clause with short label names is untouched",
			in:   `sum by (node, container, interface) (container_network_receive_bytes_total)`,
			want: `sum by (node, container, interface) (container_network_receive_bytes_total{` + inj + `})`,
		},

		// --- safety: pod_/container_/etc identifiers in `by(...)` ------
		// Regression: queries that group by a `pod_*`-prefixed label
		// (e.g. `pod_uid`) used to have the by-clause's identifier
		// rewritten as if it were a metric ref, producing
		// `by (workload_kind, workload_name, pod_uid{cluster_id="…"})`,
		// which VictoriaMetrics rejects as a parse error. The
		// grouping-clause detector now skips identifiers inside by(...)
		// regardless of prefix.
		{
			name: "by clause with pod_ label is untouched",
			in:   `topk(6, sum by (workload_kind, workload_name, pod_uid) (rate(container_cpu_usage_seconds_total{workload_name!=""}[5m])))`,
			want: `topk(6, sum by (workload_kind, workload_name, pod_uid) (rate(container_cpu_usage_seconds_total{` + inj + `,workload_name!=""}[5m])))`,
		},
		{
			name: "without clause with pod_ label is untouched",
			in:   `sum without (pod_uid) (container_memory_working_set_bytes)`,
			want: `sum without (pod_uid) (container_memory_working_set_bytes{` + inj + `})`,
		},
		{
			name: "by clause with extra whitespace before paren",
			in:   `sum by  (pod_uid) (rate(container_cpu_usage_seconds_total[1m]))`,
			want: `sum by  (pod_uid) (rate(container_cpu_usage_seconds_total{` + inj + `}[1m]))`,
		},

		// --- safety: regex literals with `{N,M}` quantifiers ---------
		// Regression: TopWorkloadsCpu uses label_replace with a regex
		// like "^(.+)-[a-z0-9]{6,12}$". The {6,12} quantifier inside
		// the quoted string was being treated as a label selector by
		// the brace-aware walker, producing nonsense like
		// `cluster_id="…",6,12` and breaking the query at VM. Pass 0
		// now masks quoted strings before the walker sees them.
		{
			name: "regex with brace quantifier inside string literal is preserved",
			in:   `label_replace(rate(container_cpu_usage_seconds_total{workload_kind="ReplicaSet"}[5m]), "workload_name", "$1", "workload_name", "^(.+)-[a-z0-9]{6,12}$")`,
			want: `label_replace(rate(container_cpu_usage_seconds_total{` + inj + `,workload_kind="ReplicaSet"}[5m]), "workload_name", "$1", "workload_name", "^(.+)-[a-z0-9]{6,12}$")`,
		},
		{
			name: "escaped quote inside string is honored",
			in:   `label_replace(container_memory_working_set_bytes, "label", "with \" quote", "src", "{ignored}")`,
			want: `label_replace(container_memory_working_set_bytes{` + inj + `}, "label", "with \" quote", "src", "{ignored}")`,
		},

		// --- Phase 2 prefixes: kube_* and kubelet_* ------------------
		// Surfaced during Phase 2 in-vivo testing — the coverage banner
		// reported kube-state-metrics as ACTIVE because the bare-metric
		// regex didn't recognize `kube_*` and the query went to VM
		// unscoped, matching every cluster's KSM samples. Same gap
		// existed for `kubelet_volume_stats_*` since Day 1 of Phase 1.
		{
			name: "kube_* metric scoped (kube-state-metrics)",
			in:   `count(kube_pod_info)`,
			want: `count(kube_pod_info{` + inj + `})`,
		},
		{
			name: "kubelet_* metric scoped (kubelet_volume_stats)",
			in:   `kubelet_volume_stats_used_bytes`,
			want: `kubelet_volume_stats_used_bytes{` + inj + `}`,
		},
		{
			name: "kube_* with existing selector preserved",
			in:   `kube_deployment_status_replicas{namespace="demo"}`,
			want: `kube_deployment_status_replicas{` + inj + `,namespace="demo"}`,
		},

		// --- empty uid fails closed ----------------------------------
		// When the backend can't discover the kube-system UID (e.g. EKS
		// auth was slow at startup), unscoped queries used to leak data
		// across clusters sharing the same VM. Now we inject a sentinel
		// that never matches a real series, so the chart shows zero
		// instead of bleeding another cluster's numbers.
		{
			name: "empty uid injects sentinel into bare metric",
			in:   `sum(rate(node_cpu_usage_seconds_total[1m]))`,
			want: `sum(rate(node_cpu_usage_seconds_total{cluster_id="__kubebolt_no_uid__"}[1m]))`,
		},
		{
			name: "empty uid injects sentinel into selector",
			in:   `node_cpu_usage_seconds_total{node="n1"}`,
			want: `node_cpu_usage_seconds_total{cluster_id="__kubebolt_no_uid__",node="n1"}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			useUID := uid
			if tc.name == "empty uid injects sentinel into bare metric" ||
				tc.name == "empty uid injects sentinel into selector" {
				useUID = ""
			}
			got := scopeQueryByCluster(tc.in, useUID)
			if got != tc.want {
				t.Errorf("\n in:   %s\n want: %s\n got:  %s", tc.in, tc.want, got)
			}
		})
	}
}

func TestScopeQueryByTenant(t *testing.T) {
	const org = "e84d526e-org"
	tests := []struct {
		name, in, want string
		tenant         string
	}{
		{
			name: "empty tenant is a no-op (OSS / single-tenant)",
			in:   `sum(rate(node_cpu_usage_seconds_total[1m]))`,
			want: `sum(rate(node_cpu_usage_seconds_total[1m]))`,
			// tenant left "" — series carry no stamped tenant_id to filter on.
		},
		{
			name:   "bare metric gets tenant_id",
			in:     `sum(rate(node_cpu_usage_seconds_total[1m]))`,
			want:   `sum(rate(node_cpu_usage_seconds_total{tenant_id="e84d526e-org"}[1m]))`,
			tenant: org,
		},
		{
			name:   "selector gets tenant_id prepended",
			in:     `container_memory_working_set_bytes{pod="p"}`,
			want:   `container_memory_working_set_bytes{tenant_id="e84d526e-org",pod="p"}`,
			tenant: org,
		},
		{
			name:   "existing tenant_id left alone (idempotent)",
			in:     `node_load1{tenant_id="e84d526e-org"}`,
			want:   `node_load1{tenant_id="e84d526e-org"}`,
			tenant: org,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := scopeQueryByTenant(tc.in, tc.tenant); got != tc.want {
				t.Errorf("\n in:   %s\n want: %s\n got:  %s", tc.in, tc.want, got)
			}
		})
	}
}

// org is the hard boundary, cluster the selector within it — the two scopes
// must compose without clobbering each other, and re-running either is a no-op.
func TestScopeQueryComposesClusterAndTenant(t *testing.T) {
	q := scopeQueryByCluster(`sum(rate(node_cpu_usage_seconds_total[1m]))`, "uid-1")
	q = scopeQueryByTenant(q, "org-9")
	const want = `sum(rate(node_cpu_usage_seconds_total{tenant_id="org-9",cluster_id="uid-1"}[1m]))`
	if q != want {
		t.Fatalf("compose\n want: %s\n got:  %s", want, q)
	}
	if again := scopeQueryByTenant(q, "org-9"); again != q {
		t.Errorf("tenant scoping not idempotent over a cluster-scoped query\n once:  %s\n twice: %s", q, again)
	}
}

// Idempotency: running the function twice should be a no-op after the
// first pass (cluster_id is already present everywhere it should be).
func TestScopeQueryByClusterIdempotent(t *testing.T) {
	const uid = "uid-x"
	queries := []string{
		`sum(rate(node_cpu_usage_seconds_total[1m]))`,
		`rate(node_network_receive_bytes_total{node="n1"}[1m])`,
		`sum by (node) (rate(node_network_transmit_bytes_total[1m]))`,
		`container_memory_working_set_bytes{namespace="ns",pod="p"}`,
	}
	for _, q := range queries {
		once := scopeQueryByCluster(q, uid)
		twice := scopeQueryByCluster(once, uid)
		if once != twice {
			t.Errorf("not idempotent\n once: %s\n twice: %s", once, twice)
		}
	}
}

// TestScopeQueryCostFamilies pins the scoping of the OpenCost metric
// families (E1 WS-C). Most sit under prefixes bareMetricRE already
// covered — those cases are regression pins — but pv_hourly_cost
// needed the new `pv` prefix: unscoped it would sum every tenant's
// volume costs on a shared VM.
func TestScopeQueryCostFamilies(t *testing.T) {
	const uid = "cluster-uid-1"
	const inj = `cluster_id="cluster-uid-1"`

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "pv_hourly_cost bare (the family the pv prefix fixes)",
			in:   `sum(pv_hourly_cost)`,
			want: `sum(pv_hourly_cost{` + inj + `})`,
		},
		{
			name: "pv_hourly_cost with selector",
			in:   `pv_hourly_cost{persistentvolume="pvc-123"}`,
			want: `pv_hourly_cost{` + inj + `,persistentvolume="pvc-123"}`,
		},
		{
			name: "node_total_hourly_cost bare (regression: node_ prefix)",
			in:   `sum(node_total_hourly_cost)`,
			want: `sum(node_total_hourly_cost{` + inj + `})`,
		},
		{
			name: "container allocation × node rate join (regression: container_/node_)",
			in:   `sum by (namespace) (container_cpu_allocation * on(node) group_left node_cpu_hourly_cost)`,
			want: `sum by (namespace) (container_cpu_allocation{` + inj + `} * on(node) group_left node_cpu_hourly_cost{` + inj + `})`,
		},
		{
			name: "pod_pvc_allocation bare (regression: pod_ prefix)",
			in:   `sum(pod_pvc_allocation)`,
			want: `sum(pod_pvc_allocation{` + inj + `})`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := scopeQueryByCluster(tt.in, uid); got != tt.want {
				t.Errorf("scopeQueryByCluster(%q)\n got:  %s\n want: %s", tt.in, got, tt.want)
			}
		})
	}

	// Tenant composition: the org boundary must land on cost families
	// too (same both-scopes contract the coverage probes use).
	in := `sum(pv_hourly_cost)`
	got := scopeQueryByTenant(scopeQueryByCluster(in, uid), "org-1")
	want := `sum(pv_hourly_cost{tenant_id="org-1",` + inj + `})`
	if got != want {
		t.Errorf("tenant+cluster compose:\n got:  %s\n want: %s", got, want)
	}
}

// TestScopeQueryForRequestFleet pins the read-side isolation contract of the
// `?scope=fleet` opt-in (E2 A1 · Fleet roll-up):
//
//   - default scope keeps BOTH scopes — and fails closed on an unknown cluster
//     UID by injecting the sentinel, so a broken connector returns 0 series
//     rather than every cluster sharing the VM;
//   - fleet scope drops ONLY the cluster selector, so `by (cluster_id)`
//     aggregations can span the org's clusters;
//   - fleet scope STILL pins tenant_id. This is the security-critical row: the
//     org boundary must survive the widening, otherwise a fleet roll-up would
//     read another org's series off a shared VM.
func TestScopeQueryForRequestFleet(t *testing.T) {
	const org = "org-42"
	// h.manager is nil → activeClusterUID returns "" → sentinel path.
	h := &handlers{}

	newReq := func(scope, tenant string) *http.Request {
		url := "/api/v1/metrics/query?query=x"
		if scope != "" {
			url += "&scope=" + scope
		}
		r := httptest.NewRequest(http.MethodGet, url, nil)
		if tenant != "" {
			r = r.WithContext(auth.WithTenantID(r.Context(), tenant))
		}
		return r
	}

	tests := []struct {
		name, scope, tenant, in, want string
		// singleTenant runs the row as an OSS build. It is not decoration: this
		// package is compiled with the `saas` tag in CI, where an init() sets
		// auth.MultiTenantEnabled = true, so an "OSS" row that does not flip it is
		// silently asserting SaaS behaviour under an OSS name.
		singleTenant bool
	}{
		{
			name: "default scope pins cluster (fail-closed sentinel) and tenant",
			in:   `sum(node_total_hourly_cost)`,
			want: `sum(node_total_hourly_cost{tenant_id="org-42",cluster_id="` + noClusterUIDSentinel + `"})`,
			// scope left "" → per-cluster behaviour, unchanged by this feature.
			tenant: org,
		},
		{
			name:   "fleet scope drops the cluster selector but keeps the org",
			scope:  "fleet",
			tenant: org,
			in:     `sum by (cluster_id) (node_total_hourly_cost)`,
			want:   `sum by (cluster_id) (node_total_hourly_cost{tenant_id="org-42"})`,
		},
		{
			name:  "fleet scope in OSS (no tenant) leaves the query unscoped",
			scope: "fleet",
			in:    `sum by (cluster_id) (node_total_hourly_cost)`,
			want:  `sum by (cluster_id) (node_total_hourly_cost)`,
			// Single-tenant: every series in the VM belongs to this install, so a
			// sentinel would hide the user's own data rather than protect anyone.
			singleTenant: true,
		},
		{
			name:   "an unknown scope value is NOT treated as fleet",
			scope:  "everything",
			tenant: org,
			in:     `sum(node_total_hourly_cost)`,
			want:   `sum(node_total_hourly_cost{tenant_id="org-42",cluster_id="` + noClusterUIDSentinel + `"})`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.singleTenant {
				prev := auth.MultiTenantEnabled
				auth.MultiTenantEnabled = false
				defer func() { auth.MultiTenantEnabled = prev }()
			}
			got := h.scopeQueryForRequest(newReq(tt.scope, tt.tenant), tt.in)
			if got != tt.want {
				t.Errorf("scopeQueryForRequest(scope=%q, tenant=%q)\n got:  %s\n want: %s",
					tt.scope, tt.tenant, got, tt.want)
			}
		})
	}
}

// TestScopeQueryFleetFailsClosedWithoutOrg covers the one shape that could cross
// an org boundary: fleet scope drops the cluster selector, so if the org ALSO
// failed to resolve the query would run completely unscoped against a shared VM.
// In multi-tenant that must degrade to zero series, never to everyone's series.
func TestScopeQueryFleetFailsClosedWithoutOrg(t *testing.T) {
	prev := auth.MultiTenantEnabled
	auth.MultiTenantEnabled = true
	defer func() { auth.MultiTenantEnabled = prev }()

	h := &handlers{}
	// No tenant in context — the pathological case.
	r := httptest.NewRequest(http.MethodGet, "/api/v1/metrics/query?query=x&scope=fleet", nil)

	got := h.scopeQueryForRequest(r, `sum by (cluster_id) (node_total_hourly_cost)`)
	want := `sum by (cluster_id) (node_total_hourly_cost{tenant_id="` + noTenantSentinel + `"})`
	if got != want {
		t.Errorf("fleet scope without a resolved org must fail closed\n got:  %s\n want: %s", got, want)
	}

	// Single-tenant (OSS) keeps the empty tenant: there, no series carries a
	// tenant_id at all, so a sentinel would hide the user's own data.
	auth.MultiTenantEnabled = false
	got = h.scopeQueryForRequest(r, `sum by (cluster_id) (node_total_hourly_cost)`)
	if want := `sum by (cluster_id) (node_total_hourly_cost)`; got != want {
		t.Errorf("OSS fleet scope must stay unscoped\n got:  %s\n want: %s", got, want)
	}
}

// TestScopeQueryByAllowedClusters pins the fleet path's replacement for the
// single-cluster confinement it drops.
//
// GET /clusters already hides agent-proxy clusters owned by teams the caller
// isn't in (scopeClustersByTeamPure), and switching to one is guarded — so
// before `scope=fleet` existed, a metrics read could only ever describe a
// cluster the caller was entitled to. Widening removed that guarantee, and
// without this narrowing a team member reads the cost, node and pod counts of
// clusters their own cluster list omits.
func TestScopeQueryByAllowedClusters(t *testing.T) {
	const in = `sum by (cluster_id) (node_total_hourly_cost)`

	t.Run("empty set fails closed", func(t *testing.T) {
		// A caller entitled to nothing must read zero series, never everything.
		got := scopeQueryByAllowedClusters(in, nil)
		want := `sum by (cluster_id) (node_total_hourly_cost{cluster_id="` + noClusterUIDSentinel + `"})`
		if got != want {
			t.Errorf("\n got:  %s\n want: %s", got, want)
		}
	})

	t.Run("single id", func(t *testing.T) {
		got := scopeQueryByAllowedClusters(in, []string{"uid-a"})
		want := `sum by (cluster_id) (node_total_hourly_cost{cluster_id=~"^(uid-a)$"})`
		if got != want {
			t.Errorf("\n got:  %s\n want: %s", got, want)
		}
	})

	t.Run("several ids are anchored and alternated", func(t *testing.T) {
		got := scopeQueryByAllowedClusters(in, []string{"uid-a", "uid-b"})
		want := `sum by (cluster_id) (node_total_hourly_cost{cluster_id=~"^(uid-a|uid-b)$"})`
		if got != want {
			t.Errorf("\n got:  %s\n want: %s", got, want)
		}
	})

	t.Run("metacharacters are quoted, not honoured", func(t *testing.T) {
		// UIDs are hex-and-dashes today, but nothing enforces that forever. One
		// unescaped metacharacter would silently widen the matcher — `.*` here
		// would match every cluster in the org.
		got := scopeQueryByAllowedClusters(in, []string{".*"})
		if strings.Contains(got, `~"^(.*)$"`) {
			t.Fatalf("metacharacter reached the matcher unescaped: %s", got)
		}
		want := `sum by (cluster_id) (node_total_hourly_cost{cluster_id=~"^(\\.\\*)$"})`
		if got != want {
			t.Errorf("\n got:  %s\n want: %s", got, want)
		}
	})

	t.Run("the by(cluster_id) clause is not mistaken for a metric", func(t *testing.T) {
		// The grouping label must survive untouched — otherwise the roll-up
		// loses the very dimension it aggregates on.
		got := scopeQueryByAllowedClusters(in, []string{"uid-a"})
		if !strings.HasPrefix(got, `sum by (cluster_id) (`) {
			t.Errorf("grouping clause was rewritten: %s", got)
		}
	})
}

// TestStripReservedScopeLabels is the regression lock for a cross-ORG read, and
// for the outage that the first attempt at fixing it caused.
//
// The hole: injectLabelMatcher leaves a `{...}` selector alone when it already
// mentions the label being injected — correct idempotency for internal
// composition, but the selector comes from the CALLER. So
// `node_load1{tenant_id=~".+"}` made org scoping a no-op and any authenticated
// user, viewer included, could read every org's series. Proven end-to-end
// against the running stack: querying as a different org returned 0 series when
// correctly scoped, 4 through the bypass.
//
// The first fix REJECTED such queries with a 400 — and broke the
// /admin/ingest-activity page, which legitimately sends `tenant_id="<org>"` to
// render its per-tenant cards. Stripping instead of rejecting serves both: the
// page's own org is re-injected identically, an attacker's value is replaced by
// the caller's. The server always wins, so nothing is left to validate.
func TestStripReservedScopeLabels(t *testing.T) {
	tests := []struct{ name, in, want string }{
		// --- the bypass shapes, all neutralised -----------------------
		{"tenant_id regex", `node_load1{tenant_id=~".+"}`, `node_load1{}`},
		{"tenant_id impersonating another org", `node_load1{tenant_id="other-org"}`, `node_load1{}`},
		{"cluster_id regex", `node_load1{cluster_id=~".+"}`, `node_load1{}`},
		{"both labels — the real cross-org primitive", `node_load1{cluster_id=~".+",tenant_id=~".+"}`, `node_load1{}`},
		{"negative matchers", `node_load1{cluster_id!="x",tenant_id!="x"}`, `node_load1{}`},
		{"hidden among legitimate matchers", `container_cpu_usage_seconds_total{namespace="ns",tenant_id=~".+",pod="p"}`, `container_cpu_usage_seconds_total{namespace="ns",pod="p"}`},
		{"nested in a subquery", `sum(rate(node_load1{cluster_id=~".+"}[5m]))`, `sum(rate(node_load1{}[5m]))`},
		{"bare selector keeps its braces", `{tenant_id=~".+"}`, `{}`},
		{"__name__ selector keeps the name", `{__name__="node_load1",tenant_id=~".+"}`, `{__name__="node_load1"}`},

		// --- the legitimate caller that the reject-based fix broke ----
		// /admin/ingest-activity builds `tenant_id="<org>"` and interpolates it.
		// Stripped here, re-injected by the server with the same value.
		{"ingest-activity headline", `sum(rate(kubebolt_agent_grpc_samples_received_total{tenant_id="e84d526e"}[5m]))`, `sum(rate(kubebolt_agent_grpc_samples_received_total{}[5m]))`},
		{"ingest-activity by status", `sum by (status) (increase(kubebolt_prom_write_samples_accepted_total{tenant_id="e84d526e"}[1h]))`, `sum by (status) (increase(kubebolt_prom_write_samples_accepted_total{}[1h]))`},

		// --- everything else must survive byte for byte --------------
		{"fleet roll-up groups by cluster_id", `sum by (cluster_id) (node_total_hourly_cost)`, `sum by (cluster_id) (node_total_hourly_cost)`},
		{"nested grouping", `count by (cluster_id) (count by (cluster_id, namespace, pod) (container_cpu_usage_seconds_total))`, `count by (cluster_id) (count by (cluster_id, namespace, pod) (container_cpu_usage_seconds_total))`},
		{"without clause", `sum without (cluster_id) (node_load1)`, `sum without (cluster_id) (node_load1)`},
		{"no selector at all", `sum(rate(node_cpu_usage_seconds_total[1m]))`, `sum(rate(node_cpu_usage_seconds_total[1m]))`},
		{"unrelated labels untouched", `container_memory_working_set_bytes{namespace="ns",pod="p"}`, `container_memory_working_set_bytes{namespace="ns",pod="p"}`},
		// A label whose NAME merely contains a reserved one is a different label.
		{"my_tenant_id is not tenant_id", `node_load1{my_tenant_id="x"}`, `node_load1{my_tenant_id="x"}`},
		// A reserved name inside a quoted VALUE is data, not a matcher — masking
		// keeps label_replace arguments intact.
		{"reserved name as a string argument", `label_replace(node_load1, "dst", "$1", "instance", "(.*)")`, `label_replace(node_load1, "dst", "$1", "instance", "(.*)")`},
		{"reserved name inside a value", `node_load1{job=~"tenant_id stuff"}`, `node_load1{job=~"tenant_id stuff"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripReservedScopeLabels(tt.in); got != tt.want {
				t.Errorf("\n in:   %s\n got:  %s\n want: %s", tt.in, got, tt.want)
			}
		})
	}
}

// TestStripThenInjectIsAuthoritative proves the end-to-end property that matters:
// whatever the client sent, the value that reaches VictoriaMetrics is the
// server's. This is the actual security guarantee — the strip alone is only half.
func TestStripThenInjectIsAuthoritative(t *testing.T) {
	const org = "org-A"
	for _, attack := range []string{
		`node_load1`,
		`node_load1{tenant_id=~".+"}`,
		`node_load1{tenant_id="org-VICTIM"}`,
		`node_load1{tenant_id!="org-A"}`,
	} {
		got := scopeQueryByTenant(stripReservedScopeLabels(attack), org)
		if !strings.Contains(got, `tenant_id="org-A"`) {
			t.Errorf("caller's org did not win for %q → %s", attack, got)
		}
		if strings.Contains(got, "org-VICTIM") || strings.Contains(got, `tenant_id=~`) || strings.Contains(got, `tenant_id!=`) {
			t.Errorf("client-supplied matcher survived for %q → %s", attack, got)
		}
	}
}
