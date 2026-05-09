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
