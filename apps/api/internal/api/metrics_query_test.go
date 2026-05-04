package api

import "testing"

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
			in:   `node_cpu_usage_cores{node="n1"}`,
			want: `node_cpu_usage_cores{` + inj + `,node="n1"}`,
		},
		{
			name: "metric with empty selector",
			in:   `node_cpu_usage_cores{}`,
			want: `node_cpu_usage_cores{` + inj + `}`,
		},
		{
			name: "selector already has cluster_id",
			in:   `node_cpu_usage_cores{cluster_id="other-uid"}`,
			want: `node_cpu_usage_cores{cluster_id="other-uid"}`,
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
			in:   `sum(node_cpu_usage_cores)`,
			want: `sum(node_cpu_usage_cores{` + inj + `})`,
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
			in:   `sum(node_cpu_usage_cores) + sum(container_cpu_usage_cores)`,
			want: `sum(node_cpu_usage_cores{` + inj + `}) + sum(container_cpu_usage_cores{` + inj + `})`,
		},

		// --- pass 1 + pass 2 mixed ------------------------------------
		{
			name: "mixed bare and selector forms in one query",
			in:   `sum(node_cpu_usage_cores) / sum(container_memory_working_set_bytes{pod_name="x"})`,
			want: `sum(node_cpu_usage_cores{` + inj + `}) / sum(container_memory_working_set_bytes{` + inj + `,pod_name="x"})`,
		},

		// --- safety: identifiers inside `{...}` aren't rewritten ------
		// `pod_name`, `pod_namespace`, `pod_uid` are LABELS (not metrics)
		// and start with the `pod_` prefix; pass 2 must skip them when
		// they appear inside a selector.
		{
			name: "label inside selector with pod_ prefix is not rewritten",
			in:   `container_memory_working_set_bytes{pod_namespace="ns",pod_name="p"}`,
			want: `container_memory_working_set_bytes{` + inj + `,pod_namespace="ns",pod_name="p"}`,
		},

		// --- safety: short identifiers in `by(...)` not matched -------
		{
			name: "by clause with short label names is untouched",
			in:   `sum by (node, container, interface) (pod_network_receive_bytes_total)`,
			want: `sum by (node, container, interface) (pod_network_receive_bytes_total{` + inj + `})`,
		},

		// --- safety: pod_/container_/etc identifiers in `by(...)` ------
		// Regression: TopWorkloadsCpu uses `by(workload_kind, workload_name,
		// pod_namespace)`. Before this fix, pass 2 mistakenly injected a
		// selector onto `pod_namespace` inside the by-clause, producing
		// `by (workload_kind, workload_name, pod_namespace{cluster_id="…"})`,
		// which VictoriaMetrics rejects as a parse error. The grouping-clause
		// detector now skips identifiers inside by(...) regardless of prefix.
		{
			name: "by clause with pod_ label is untouched",
			in:   `topk(6, sum by (workload_kind, workload_name, pod_namespace) (container_cpu_usage_cores{workload_name!=""}))`,
			want: `topk(6, sum by (workload_kind, workload_name, pod_namespace) (container_cpu_usage_cores{` + inj + `,workload_name!=""}))`,
		},
		{
			name: "without clause with pod_ label is untouched",
			in:   `sum without (pod_name, pod_uid) (container_memory_working_set_bytes)`,
			want: `sum without (pod_name, pod_uid) (container_memory_working_set_bytes{` + inj + `})`,
		},
		{
			name: "by clause with extra whitespace before paren",
			in:   `sum by  (pod_namespace) (container_cpu_usage_cores)`,
			want: `sum by  (pod_namespace) (container_cpu_usage_cores{` + inj + `})`,
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
			in:   `label_replace(container_cpu_usage_cores{workload_kind="ReplicaSet"}, "workload_name", "$1", "workload_name", "^(.+)-[a-z0-9]{6,12}$")`,
			want: `label_replace(container_cpu_usage_cores{` + inj + `,workload_kind="ReplicaSet"}, "workload_name", "$1", "workload_name", "^(.+)-[a-z0-9]{6,12}$")`,
		},
		{
			name: "escaped quote inside string is honored",
			in:   `label_replace(container_cpu_usage_cores, "label", "with \" quote", "src", "{ignored}")`,
			want: `label_replace(container_cpu_usage_cores{` + inj + `}, "label", "with \" quote", "src", "{ignored}")`,
		},

		// --- empty uid fails closed ----------------------------------
		// When the backend can't discover the kube-system UID (e.g. EKS
		// auth was slow at startup), unscoped queries used to leak data
		// across clusters sharing the same VM. Now we inject a sentinel
		// that never matches a real series, so the chart shows zero
		// instead of bleeding another cluster's numbers.
		{
			name: "empty uid injects sentinel into bare metric",
			in:   `sum(node_cpu_usage_cores)`,
			want: `sum(node_cpu_usage_cores{cluster_id="__kubebolt_no_uid__"})`,
		},
		{
			name: "empty uid injects sentinel into selector",
			in:   `node_cpu_usage_cores{node="n1"}`,
			want: `node_cpu_usage_cores{cluster_id="__kubebolt_no_uid__",node="n1"}`,
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

// Idempotency: running the function twice should be a no-op after the
// first pass (cluster_id is already present everywhere it should be).
func TestScopeQueryByClusterIdempotent(t *testing.T) {
	const uid = "uid-x"
	queries := []string{
		`sum(node_cpu_usage_cores)`,
		`rate(node_network_receive_bytes_total{node="n1"}[1m])`,
		`sum by (node) (rate(node_network_transmit_bytes_total[1m]))`,
		`container_memory_working_set_bytes{pod_namespace="ns",pod_name="p"}`,
	}
	for _, q := range queries {
		once := scopeQueryByCluster(q, uid)
		twice := scopeQueryByCluster(once, uid)
		if once != twice {
			t.Errorf("not idempotent\n once: %s\n twice: %s", once, twice)
		}
	}
}
