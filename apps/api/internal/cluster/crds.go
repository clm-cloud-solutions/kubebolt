package cluster

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Optional standard CRDs surfaced as first-class resources (Sprint 3),
// accessed via the dynamic client — the Gateway/HTTPRoute pattern — because
// they may not be installed (no typed listers, list errors → empty, not a
// failure). cert-manager Certificate, ArgoCD Application, and VPA.
var optionalCRDGVRs = map[string]schema.GroupVersionResource{
	"certificates": {Group: "cert-manager.io", Version: "v1", Resource: "certificates"},
	"argocdapps":   {Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"},
	"vpas":         {Group: "autoscaling.k8s.io", Version: "v1", Resource: "verticalpodautoscalers"},
}

// isOptionalCRD reports whether a resource type is one of the dynamic-client
// optional CRDs (used by the GetResources/GetResourceDetail dispatch).
func isOptionalCRD(rtype string) bool {
	_, ok := optionalCRDGVRs[rtype]
	return ok
}

// listOptionalCRD lists one optional CRD type via the dynamic client and maps
// each item to the frontend shape. Returns nil when the dynamic client is
// absent or the CRD isn't installed (list error) — these are optional.
func (c *Connector) listOptionalCRD(rtype, namespace string) []map[string]interface{} {
	gvr, ok := optionalCRDGVRs[rtype]
	if !ok || c.dynamicClient == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var list *unstructured.UnstructuredList
	var err error
	if namespace != "" {
		list, err = c.dynamicClient.Resource(gvr).Namespace(namespace).List(ctx, metav1.ListOptions{})
	} else {
		list, err = c.dynamicClient.Resource(gvr).List(ctx, metav1.ListOptions{})
	}
	if err != nil {
		return nil // CRD not installed in this cluster
	}
	items := make([]map[string]interface{}, 0, len(list.Items))
	for i := range list.Items {
		items = append(items, optionalCRDToMap(rtype, &list.Items[i]))
	}
	return items
}

// getOptionalCRD fetches one optional CRD instance for the detail view.
func (c *Connector) getOptionalCRD(rtype, namespace, name string) (map[string]interface{}, error) {
	gvr, ok := optionalCRDGVRs[rtype]
	if !ok {
		return nil, fmt.Errorf("unknown optional CRD %q", rtype)
	}
	if c.dynamicClient == nil {
		return nil, fmt.Errorf("%s not available (CRD not installed or dynamic client unavailable)", rtype)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	obj, err := c.dynamicClient.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return optionalCRDToMap(rtype, obj), nil
}

// optionalCRDToMap renders one optional CRD's unstructured object into the
// frontend map shape, with per-type status/field extraction.
func optionalCRDToMap(rtype string, item *unstructured.Unstructured) map[string]interface{} {
	m := map[string]interface{}{
		"name":        item.GetName(),
		"namespace":   item.GetNamespace(),
		"labels":      safeLabels(item.GetLabels()),
		"annotations": safeAnnotations(item.GetAnnotations()),
		"createdAt":   item.GetCreationTimestamp().Time.Format(time.RFC3339),
		"age":         formatAge(item.GetCreationTimestamp().Time),
	}
	spec, _ := item.Object["spec"].(map[string]interface{})
	status, _ := item.Object["status"].(map[string]interface{})

	switch rtype {
	case "certificates":
		m["status"] = "Unknown"
		if ready := conditionStatus(status, "Ready"); ready != "" {
			if ready == "True" {
				m["status"] = "Ready"
			} else {
				m["status"] = "NotReady"
			}
		}
		m["issuer"] = nestedString(spec, "issuerRef", "name")
		m["secretName"] = stringField(spec, "secretName")
		m["commonName"] = stringField(spec, "commonName")
		if dns := stringSlice(spec, "dnsNames"); len(dns) > 0 {
			m["dnsNames"] = dns
		}
		if na := stringField(status, "notAfter"); na != "" {
			m["notAfter"] = na
			if t, err := time.Parse(time.RFC3339, na); err == nil {
				m["expiresInDays"] = int(time.Until(t).Hours() / 24)
			}
		}
		m["renewalTime"] = stringField(status, "renewalTime")

	case "argocdapps":
		sync := nestedString(status, "sync", "status")     // Synced | OutOfSync
		health := nestedString(status, "health", "status") // Healthy | Degraded | ...
		m["syncStatus"] = sync
		m["healthStatus"] = health
		m["project"] = nestedString(spec, "project")
		m["revision"] = nestedString(status, "sync", "revision")
		// One-word status for the list: surface health, fall back to sync.
		if health != "" {
			m["status"] = health
		} else if sync != "" {
			m["status"] = sync
		} else {
			m["status"] = "Unknown"
		}

	case "vpas":
		m["status"] = "Active"
		m["targetRef"] = nestedString(spec, "targetRef", "name")
		m["updateMode"] = nestedString(spec, "updatePolicy", "updateMode")
		// Recommendation summary: container → target cpu/memory.
		if rec, ok := status["recommendation"].(map[string]interface{}); ok {
			if crs, ok := rec["containerRecommendations"].([]interface{}); ok {
				var recs []map[string]interface{}
				for _, cr := range crs {
					crm, ok := cr.(map[string]interface{})
					if !ok {
						continue
					}
					target, _ := crm["target"].(map[string]interface{})
					recs = append(recs, map[string]interface{}{
						"container": stringField(crm, "containerName"),
						"targetCPU": stringField(target, "cpu"),
						"targetMem": stringField(target, "memory"),
					})
				}
				m["recommendations"] = recs
			}
		}
	}
	return m
}

// ─── unstructured field helpers ───────────────────────────────────

func stringField(m map[string]interface{}, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

// nestedString walks a chain of map keys and returns the terminal string.
func nestedString(m map[string]interface{}, keys ...string) string {
	cur := m
	for i, k := range keys {
		if cur == nil {
			return ""
		}
		if i == len(keys)-1 {
			return stringField(cur, k)
		}
		cur, _ = cur[k].(map[string]interface{})
	}
	return ""
}

func stringSlice(m map[string]interface{}, key string) []string {
	if m == nil {
		return nil
	}
	raw, ok := m[key].([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// conditionStatus returns the status string ("True"/"False") of the named
// condition in a status.conditions[] array, or "" if absent.
func conditionStatus(status map[string]interface{}, condType string) string {
	if status == nil {
		return ""
	}
	conds, ok := status["conditions"].([]interface{})
	if !ok {
		return ""
	}
	for _, cond := range conds {
		cm, ok := cond.(map[string]interface{})
		if !ok {
			continue
		}
		if stringField(cm, "type") == condType {
			return stringField(cm, "status")
		}
	}
	return ""
}
