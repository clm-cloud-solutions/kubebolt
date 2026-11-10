package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
	// uids maps namespace/name to the pod's UID, for samples that know the
	// pod only by name (cAdvisor's).
	uids map[podKey]string
}

type podKey struct{ namespace, name string }

type podMeta struct {
	Namespace    string
	Name         string
	Labels       map[string]string
	WorkloadKind string
	WorkloadName string
	// HostNetwork is spec.hostNetwork: the pod shares the node's network
	// namespace, so its network counters are the node's.
	HostNetwork bool
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
	if c.client == nil {
		return errors.New("pods cache has no kubelet client")
	}
	body, err := c.client.Get(ctx, "/pods")
	if err != nil {
		return err
	}
	var list podList
	if err := json.Unmarshal(body, &list); err != nil {
		return fmt.Errorf("decode /pods: %w", err)
	}

	next := make(map[string]podMeta, len(list.Items))
	uids := make(map[podKey]string, len(list.Items))
	for _, p := range list.Items {
		uid := p.Metadata.UID
		if uid == "" {
			continue
		}
		meta := podMeta{
			Namespace:   p.Metadata.Namespace,
			Name:        p.Metadata.Name,
			Labels:      filterLabels(p.Metadata.Labels),
			HostNetwork: p.Spec.HostNetwork,
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
		uids[podKey{meta.Namespace, meta.Name}] = uid
	}

	c.mu.Lock()
	c.entries = next
	c.uids = uids
	c.mu.Unlock()
	return nil
}

// UIDFor returns the UID of the pod with this namespace and name, or "" when
// the cache has not seen it. Safe on a nil cache.
func (c *PodsCache) UIDFor(namespace, name string) string {
	if c == nil {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.uids[podKey{namespace, name}]
}

// IsHostNetwork reports whether the pod with this namespace and name runs
// with spec.hostNetwork. A pod the cache has not seen reports false, so its
// network is kept. Safe on a nil cache.
func (c *PodsCache) IsHostNetwork(namespace, name string) bool {
	if c == nil {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	uid, ok := c.uids[podKey{namespace, name}]
	return ok && c.entries[uid].HostNetwork
}

// Knows reports whether the cache holds the pod with this UID. Safe on a nil
// cache.
func (c *PodsCache) Knows(uid string) bool {
	if c == nil {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.entries[uid]
	return ok
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
			s.Labels["label_"+sanitizeLabel(k)] = safeUTF8(v)
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

// safeUTF8 replaces invalid UTF-8 byte runs with the replacement char so a pod
// label value is safe in the Sample.labels protobuf map<string,string> (proto3
// rejects invalid UTF-8). k8s validates label values today, so this is
// belt-and-suspenders against anything that slips past validation.
func safeUTF8(s string) string {
	return strings.ToValidUTF8(s, "�")
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
	Spec     podSpec     `json:"spec"`
}

type podSpec struct {
	HostNetwork bool `json:"hostNetwork"`
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
