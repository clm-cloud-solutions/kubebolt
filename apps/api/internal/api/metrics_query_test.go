package api

import (
	"strings"
	"testing"
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
