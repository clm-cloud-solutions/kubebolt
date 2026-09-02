package integrations

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// OpenCost integration — detects an OpenCost install in the active
// cluster. OpenCost is the pricing oracle for KubeBolt's cost
// features: its exporter emits the node/container cost families
// (node_total_hourly_cost, container_cpu_allocation, …) that the
// Cost dashboard and Lifecycle Management consume.
//
// This provider is detection-only (read-only Detect). The ingestion
// path — the agent's exporter collector scraping OpenCost's
// /metrics — ships separately (E1 WS-B); until it lands, a detected
// install will show "no cost samples reaching KubeBolt yet".
const (
	OpenCostID   = "opencost"
	OpenCostName = "OpenCost"

	// openCostLabelSelector matches the official Helm chart and the
	// upstream manifests, both of which stamp
	// app.kubernetes.io/name=opencost on the Deployment's pods.
	openCostLabelSelector = "app.kubernetes.io/name=opencost"

	// openCostVersionLabel is the standard chart version label; the
	// image-tag parse below is the fallback for manifest installs
	// that don't carry it.
	openCostVersionLabel = "app.kubernetes.io/version"
)

// openCostFallbackNamespaces are probed by pod-name prefix when the
// label selector finds nothing — covers hand-rolled manifests that
// drop the standard labels. kubecost is included because OpenCost
// frequently runs embedded in / migrated from a Kubecost install.
var openCostFallbackNamespaces = []string{"opencost", "kubecost", "monitoring"}

// openCostSamplesProbeFn checks whether VictoriaMetrics holds cost
// samples for the given cluster — i.e. whether the detected OpenCost
// install is actually feeding KubeBolt (via the agent's exporter
// collector) rather than just running. Symmetric to
// agentSamplesProbeFn / promSamplesProbeFn.
type openCostSamplesProbeFn func(ctx context.Context, clusterID string) (bool, error)

type openCostProvider struct {
	currentCluster currentClusterIDFn
	sampleProbe    openCostSamplesProbeFn
}

// openCostProvider is the first SignalProvider implementation — the
// compile-time assertion keeps the framework contract honest as it
// evolves (E2 providers implement the same interface).
var _ SignalProvider = (*openCostProvider)(nil)

// IngestMode: OpenCost is a Prometheus exporter; the agent's
// leader-elected exporter collector scrapes it (KUBEBOLT_AGENT_EXPORTERS)
// and ships samples through the normal agent path.
func (p *openCostProvider) IngestMode() IngestPattern { return IngestScrape }

// Normalize is a passthrough for scrape-mode providers: samples are
// parsed, stamped, and shipped agent-side (see
// packages/agent/internal/collector/exporter.go), so there's no raw
// payload for the backend to normalize. Exists to satisfy the
// SignalProvider contract that CRD/httpSink/cronParse providers (E2)
// implement with real logic.
func (p *openCostProvider) Normalize(ctx context.Context, raw any) (Signals, error) {
	return Signals{}, nil
}

// NewOpenCost constructs the OpenCost integration provider.
//
// currentCluster resolves the active cluster's UID for the sample
// probe's scoped query. sampleProbe is the optional VM probe that
// confirms cost samples are reaching this backend; without it the
// card reports workload state only. Pass nils to disable each check
// independently — tests use nils to exercise the workload branches
// in isolation.
func NewOpenCost(currentCluster currentClusterIDFn, sampleProbe openCostSamplesProbeFn) Provider {
	if currentCluster == nil {
		currentCluster = func() string { return "" }
	}
	return &openCostProvider{
		currentCluster: currentCluster,
		sampleProbe:    sampleProbe,
	}
}

func (p *openCostProvider) Meta() Integration {
	return Integration{
		ID:          OpenCostID,
		Name:        OpenCostName,
		Description: "Cost allocation oracle. OpenCost's exporter provides the per-node and per-container cost metrics behind KubeBolt's Cost dashboard and Lifecycle Management savings accounting.",
		DocsURL:     "https://github.com/clm-cloud-solutions/kubebolt/blob/main/docs/integrations/opencost.md",
		Capabilities: []string{
			"cost.allocation",
			"cost.pricing-oracle",
		},
	}
}

func (p *openCostProvider) Detect(ctx context.Context, cs kubernetes.Interface) (Integration, error) {
	meta := p.Meta()
	if cs == nil {
		meta.Status = StatusUnknown
		meta.Health = &Health{Message: "no cluster connection"}
		return meta, nil
	}

	workload, err := detectOpenCostWorkload(ctx, cs)
	if err != nil {
		// Couldn't LIST pods (typically RBAC) — distinct from "looked
		// and found nothing". Unknown lets the UI point the operator
		// at the cause instead of claiming OpenCost is absent.
		meta.Status = StatusUnknown
		meta.Health = &Health{Message: fmt.Sprintf("could not list pods: %v", err)}
		return meta, nil
	}
	if workload.Namespace == "" {
		meta.Status = StatusNotInstalled
		return meta, nil
	}

	meta.Namespace = workload.Namespace
	meta.Version = workload.Version
	meta.Managed = workload.Managed
	meta.Health = &Health{
		PodsReady:   workload.PodsReady,
		PodsDesired: workload.PodsDesired,
	}
	if workload.PodsReady == 0 || workload.PodsReady < workload.PodsDesired {
		meta.Status = StatusDegraded
		meta.Health.Message = "OpenCost pods not fully ready"
		return meta, nil
	}
	meta.Status = StatusInstalled

	// Advisory data-flow check: a running OpenCost that nothing
	// scrapes into KubeBolt is only half the story. Probe errors are
	// swallowed on purpose — a slow/unreachable VM must not degrade
	// the workload verdict the operator can see with their own eyes.
	if p.sampleProbe != nil {
		if flowing, probeErr := p.sampleProbe(ctx, p.currentCluster()); probeErr == nil && !flowing {
			meta.Health.Message = "running, but no cost samples reaching KubeBolt yet — enable the agent's OpenCost exporter"
		}
	}
	return meta, nil
}

// openCostWorkload is the slim slice of in-cluster state the card
// renders. Empty Namespace signals "not found" — same convention as
// promWorkload.
type openCostWorkload struct {
	Namespace   string
	PodsReady   int
	PodsDesired int
	Version     string
	Managed     bool
}

// detectOpenCostWorkload finds the OpenCost pods: first by the
// standard label selector cluster-wide, then by pod-name prefix in
// the conventional namespaces. Returns an error only when the API
// refused the primary LIST (RBAC etc.) — "nothing matched" is a
// zero-value workload, not an error.
func detectOpenCostWorkload(ctx context.Context, cs kubernetes.Interface) (openCostWorkload, error) {
	pods, err := cs.CoreV1().Pods("").List(ctx, metav1.ListOptions{
		LabelSelector: openCostLabelSelector,
		Limit:         32, // presence + first pod's labels is all we need
	})
	if err != nil {
		return openCostWorkload{}, err
	}
	items := pods.Items

	if len(items) == 0 {
		// Fallback: unlabeled manifest installs. Best-effort — a
		// namespace we can't list is skipped, not fatal, because the
		// primary cluster-wide LIST already succeeded.
		for _, ns := range openCostFallbackNamespaces {
			nsPods, nsErr := cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{Limit: 32})
			if nsErr != nil {
				continue
			}
			for i := range nsPods.Items {
				if strings.HasPrefix(nsPods.Items[i].Name, "opencost") {
					items = append(items, nsPods.Items[i])
				}
			}
			if len(items) > 0 {
				break
			}
		}
	}
	if len(items) == 0 {
		return openCostWorkload{}, nil
	}

	// Multiple releases across namespaces: take the first — same
	// convention as the agent / prometheus providers.
	first := items[0]
	result := openCostWorkload{
		Namespace: first.Namespace,
		Version:   openCostVersion(&first),
		Managed:   first.Labels["app.kubernetes.io/managed-by"] == "kubebolt",
	}
	for i := range items {
		pod := &items[i]
		if pod.Namespace != result.Namespace {
			continue
		}
		result.PodsDesired++
		if isPodReady(pod) {
			result.PodsReady++
		}
	}
	return result, nil
}

// openCostVersion resolves a display version: standard version label
// first, image tag as fallback (skipping digests — "…@sha256:…" tags
// aren't operator-meaningful).
func openCostVersion(pod *corev1.Pod) string {
	if v := pod.Labels[openCostVersionLabel]; v != "" {
		return v
	}
	for _, c := range pod.Spec.Containers {
		image := c.Image
		if at := strings.Index(image, "@"); at >= 0 {
			image = image[:at]
		}
		if colon := strings.LastIndex(image, ":"); colon >= 0 && !strings.Contains(image[colon+1:], "/") {
			if tag := image[colon+1:]; tag != "" && tag != "latest" {
				return tag
			}
		}
	}
	return ""
}
