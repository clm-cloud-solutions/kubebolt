package api

import (
	"net/http"
	"strings"
)

type searchResult struct {
	Name         string `json:"name"`
	Namespace    string `json:"namespace"`
	Kind         string `json:"kind"`
	ResourceType string `json:"resourceType"`
	Status       string `json:"status,omitempty"`
}

func (h *handlers) handleSearch(w http.ResponseWriter, r *http.Request) {
	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	if query == "" {
		respondJSON(w, http.StatusOK, []searchResult{})
		return
	}

	conn := h.manager.Connector()
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}

	// Search across all resource types using existing listers
	types := []string{
		"pods", "deployments", "statefulsets", "daemonsets", "jobs", "cronjobs",
		"services", "ingresses", "configmaps", "secrets", "nodes", "namespaces",
		"pvcs", "pvs", "hpas", "storageclasses",
	}

	var results []searchResult
	limit := 50

	for _, rt := range types {
		if len(results) >= limit {
			break
		}
		list := conn.GetResources(rt, "", query, "", "", "", "", 1, limit)
		for _, item := range list.Items {
			if len(results) >= limit {
				break
			}
			name, _ := item["name"].(string)
			ns, _ := item["namespace"].(string)
			status, _ := item["status"].(string)
			results = append(results, searchResult{
				Name:         name,
				Namespace:    ns,
				Kind:         resourceTypeToKind(rt),
				ResourceType: rt,
				Status:       status,
			})
		}
	}

	respondJSON(w, http.StatusOK, results)
}

func resourceTypeToKind(rt string) string {
	kinds := map[string]string{
		"pods": "Pod", "deployments": "Deployment", "statefulsets": "StatefulSet",
		"daemonsets": "DaemonSet", "jobs": "Job", "cronjobs": "CronJob",
		"services": "Service", "ingresses": "Ingress", "configmaps": "ConfigMap",
		"secrets": "Secret", "nodes": "Node", "namespaces": "Namespace",
		"pvcs": "PVC", "pvs": "PV", "hpas": "HPA", "storageclasses": "StorageClass",
	}
	if k, ok := kinds[rt]; ok {
		return k
	}
	return rt
}
