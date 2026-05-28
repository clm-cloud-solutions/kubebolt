package integrations

import (
	"context"
	"fmt"

	"k8s.io/client-go/kubernetes"
)

// Prometheus (read) identity. This integration is the inverse of the
// Prometheus card: instead of the customer's Prom pushing samples to
// KubeBolt via remote_write, the kubebolt-agent READS samples FROM a
// customer-managed Prom over /api/v1/query_range and ships them via
// the gRPC AgentChannel.
//
// Why both exist:
//   - "Prometheus" (write path) is the default for self-managed Prom
//     where the operator can edit remote_write — kube-prometheus-stack,
//     Prometheus Operator, plain Helm chart.
//   - "Prometheus (read)" is the only path that works for AMP / Azure
//     Managed Prometheus / GMP, where remote_write outbound either
//     isn't supported or requires SigV4/OIDC the operator can't bolt
//     onto a managed control plane. Also fits the change-management
//     edge case ("I cannot edit the customer's Prom config").
//
// Detection is signal-driven, not config-driven: the agent's promread
// pod emits a `kubebolt_promread_leader == 1` gauge as long as it
// holds the kubebolt-promread Lease. Presence of that signal in VM,
// scoped to the active cluster_id, is the truth that Mode C is wired
// and shipping. No leader → either the chart wasn't installed with
// `agent.promRead.enabled=true`, the deployment crashed, or its auth
// is failing (leader election succeeds but the poll loop errors —
// followed up in a future revision by also looking at
// kubebolt_agent_promread_poll_errors_total).
const (
	PrometheusReadID   = "prometheus-read"
	PrometheusReadName = "Prometheus (read)"
)

// promreadActiveProbeFn confirms whether any promread pod is currently
// the elected leader for the given cluster_id. Returns (true, nil)
// when at least one `kubebolt_promread_leader{cluster_id=X} == 1`
// series exists in VM; (false, nil) when no such series; (false, err)
// on transport failure — the provider treats err as "couldn't tell"
// and surfaces StatusUnknown rather than misclassifying as
// NotInstalled.
//
// Pass nil to disable the check (the card reports StatusUnknown with
// the reason "vm probe not configured"). Tests use nil to exercise
// the unknown branch without standing up a VM.
type promreadActiveProbeFn func(ctx context.Context, clusterID string) (bool, error)

// prometheusReadProvider implements Provider for Mode C — the agent
// reading from an existing customer Prom. Unlike the Agent provider
// it does not own any in-cluster workload (the same agent DaemonSet
// runs Mode C in a separate Deployment), so Detect only has a
// data-plane truth-signal to read: presence of the promread leader
// gauge in VM. Stateless; safe to register once at startup.
type prometheusReadProvider struct {
	currentCluster currentClusterIDFn
	activeProbe    promreadActiveProbeFn
}

// NewPrometheusRead constructs the Prometheus (read) integration
// provider. currentCluster resolves the active cluster's kube-system
// UID on every Detect — the same UID the agent stamps onto the
// promread leader gauge, so VM-side scoping joins on this single
// value. activeProbe is the VM client method that asks "is anything
// holding the lease?".
//
// Pass nil currentCluster or nil activeProbe to disable the detection
// path independently — the card then reports StatusUnknown with a
// reason. Tests use nils.
func NewPrometheusRead(
	currentCluster currentClusterIDFn,
	activeProbe promreadActiveProbeFn,
) Provider {
	if currentCluster == nil {
		currentCluster = func() string { return "" }
	}
	return &prometheusReadProvider{
		currentCluster: currentCluster,
		activeProbe:    activeProbe,
	}
}

func (p *prometheusReadProvider) Meta() Integration {
	return Integration{
		ID:          PrometheusReadID,
		Name:        PrometheusReadName,
		Description: "Agent reads samples FROM a customer-managed Prometheus via /api/v1/query_range (Mode C). The only path for AMP / Azure Managed Prometheus / GMP where remote_write outbound isn't an option. Enabled via the kubebolt-agent helm chart (agent.promRead.enabled=true) with one of 6 auth modes: none / basicAuth / bearer / awsSigV4 / gcpIam / azureWorkloadIdentity.",
		DocsURL:     "https://github.com/clm-cloud-solutions/kubebolt/blob/main/docs/integrations/prometheus.md",
		Capabilities: []string{
			"metrics.scraped",
			"metrics.historical",
		},
	}
}

func (p *prometheusReadProvider) Detect(ctx context.Context, _ kubernetes.Interface) (Integration, error) {
	meta := p.Meta()

	// No probe wired (auth disabled / VM not configured) — we cannot
	// produce a truth-signal. Distinct from NotInstalled so the UI
	// renders an actionable "wire VM" hint instead of a misleading
	// "install the chart" prompt.
	if p.activeProbe == nil {
		meta.Status = StatusUnknown
		meta.Health = &Health{Message: "vm probe not configured — cannot detect promread leader presence"}
		return meta, nil
	}

	clusterID := p.currentCluster()
	if clusterID == "" {
		// Without a cluster UID the scoped query would either match
		// every cluster (label_replace inflation) or nothing at all.
		// Surface Unknown so the operator knows to wait for cluster
		// resolution before trusting the card.
		meta.Status = StatusUnknown
		meta.Health = &Health{Message: "active cluster UID not yet resolved"}
		return meta, nil
	}

	active, err := p.activeProbe(ctx, clusterID)
	if err != nil {
		// Transport error — VM unreachable, query parse error, etc.
		// Same rationale as the Prometheus card: don't flip a working
		// integration to NotInstalled because of a transient probe
		// blip. StatusUnknown invites the operator to retry rather
		// than reinstall.
		meta.Status = StatusUnknown
		meta.Health = &Health{Message: fmt.Sprintf("vm probe failed: %v", err)}
		return meta, nil
	}

	if !active {
		meta.Status = StatusNotInstalled
		meta.Health = &Health{
			Message: "No promread leader detected for this cluster. Install the kubebolt-agent chart with agent.promRead.enabled=true and agent.promRead.url pointing at the customer's Prometheus.",
		}
		return meta, nil
	}

	meta.Status = StatusInstalled
	meta.Health = &Health{
		Message: "Promread leader is active — agent is polling the customer's Prometheus and shipping samples.",
	}
	return meta, nil
}
