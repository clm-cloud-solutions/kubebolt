package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/kubebolt/kubebolt/packages/agent/internal/kubelet"
	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"
)

// PodsCache holds a recent snapshot of the pods on this node, keyed by pod
// UID, for enriching stats samples with workload and app metadata.
//
// Keep the label surface narrow by design: pods in the wild carry dozens of
// labels, many of which would explode TSDB cardinality if we propagated
// them verbatim. Phase B forwards only a curated set.
type PodsCache struct {
	client *kubelet.Client

	mu      sync.RWMutex
	entries map[string]podMeta
}

type podMeta struct {
	Namespace    string
	Name         string
	Labels       map[string]string
	WorkloadKind string
	WorkloadName string
}

var propagatedLabels = []string{
	"app",
	"app.kubernetes.io/name",
	"app.kubernetes.io/instance",
	"app.kubernetes.io/version",
	"app.kubernetes.io/component",
	"k8s-app",
	"component",
	"tier",
	"release",
}

func NewPods(client *kubelet.Client) *PodsCache {
	return &PodsCache{
		client:  client,
		entries: make(map[string]podMeta),
	}
}

// Refresh pulls /pods from kubelet and replaces the cache.
func (c *PodsCache) Refresh(ctx context.Context) error {
	body, err := c.client.Get(ctx, "/pods")
	if err != nil {
		return err
	}
	var list podList
	if err := json.Unmarshal(body, &list); err != nil {
		return fmt.Errorf("decode /pods: %w", err)
	}

	next := make(map[string]podMeta, len(list.Items))
	for _, p := range list.Items {
		uid := p.Metadata.UID
		if uid == "" {
			continue
		}
		meta := podMeta{
			Namespace: p.Metadata.Namespace,
			Name:      p.Metadata.Name,
			Labels:    filterLabels(p.Metadata.Labels),
		}
		// Take the first controller ownerRef if present.
		for _, ref := range p.Metadata.OwnerReferences {
			if ref.Controller != nil && *ref.Controller {
				meta.WorkloadKind = ref.Kind
				meta.WorkloadName = ref.Name
				break
			}
		}
		next[uid] = meta
	}

	c.mu.Lock()
	c.entries = next
	c.mu.Unlock()
	return nil
}

// Size returns the number of cached pods. Useful for health logging.
func (c *PodsCache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}

// Enrich attaches pod labels and workload identity to each sample that
// carries a pod_uid label. Samples without a pod_uid pass through unchanged.
// Enrichment is additive — existing sample labels are preserved.
func (c *PodsCache) Enrich(samples []*agentv2.Sample) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, s := range samples {
		uid := s.Labels["pod_uid"]
		if uid == "" {
			continue
		}
		meta, ok := c.entries[uid]
		if !ok {
			continue
		}
		if meta.WorkloadKind != "" {
			s.Labels["workload_kind"] = meta.WorkloadKind
		}
		if meta.WorkloadName != "" {
			s.Labels["workload_name"] = meta.WorkloadName
		}
		for k, v := range meta.Labels {
			s.Labels["label_"+sanitizeLabel(k)] = v
		}
	}
}

// filterLabels keeps only the propagated label set defined above.
func filterLabels(in map[string]string) map[string]string {
	out := make(map[string]string, len(propagatedLabels))
	for _, k := range propagatedLabels {
		if v, ok := in[k]; ok {
			out[k] = v
		}
	}
	return out
}

// sanitizeLabel replaces characters that would be invalid in Prometheus label
// names (dots, slashes, dashes) with underscores so label_app.kubernetes.io/name
// becomes label_app_kubernetes_io_name on the wire.
func sanitizeLabel(s string) string {
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9', ch == '_':
			b = append(b, ch)
		default:
			b = append(b, '_')
		}
	}
	return string(b)
}

// --- wire types for the kubelet /pods response (corev1.PodList subset) ------

type podList struct {
	Items []pod `json:"items"`
}

type pod struct {
	Metadata podMetadata `json:"metadata"`
}

type podMetadata struct {
	Name            string            `json:"name"`
	Namespace       string            `json:"namespace"`
	UID             string            `json:"uid"`
	Labels          map[string]string `json:"labels"`
	OwnerReferences []ownerReference  `json:"ownerReferences"`
}

type ownerReference struct {
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Controller *bool  `json:"controller"`
}
