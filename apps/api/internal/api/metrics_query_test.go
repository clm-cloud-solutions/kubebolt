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

		// --- empty uid disables scoping ------------------------------
		{
			name: "empty uid leaves query unchanged",
			in:   `sum(node_cpu_usage_cores)`,
			want: `sum(node_cpu_usage_cores)`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			useUID := uid
			if tc.name == "empty uid leaves query unchanged" {
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
