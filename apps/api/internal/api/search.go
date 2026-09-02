package api

import (
	"net/http"
	"strings"

	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
)

type searchResult struct {
	Name         string `json:"name"`
	Namespace    string `json:"namespace"`
	Kind         string `json:"kind"`
	ResourceType string `json:"resourceType"`
	Status       string `json:"status,omitempty"`
	// Cluster labels fleet-scope hits with the cluster they live in
	// (display name when set, context name otherwise). Empty on
	// single-cluster searches — the shape is backward-compatible.
	Cluster string `json:"cluster,omitempty"`
}

func (h *handlers) handleSearch(w http.ResponseWriter, r *http.Request) {
	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	if query == "" {
		respondJSON(w, http.StatusOK, []searchResult{})
		return
	}

	// Fleet scope (E2 Fleet C1): fan out across every searchable cluster
	// in the caller's org instead of just the request's cluster.
	if r.URL.Query().Get("scope") == "fleet" {
		h.handleFleetSearch(w, r, query)
		return
	}

	conn := h.manager.Connector(r.Context())
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}
	respondJSON(w, http.StatusOK, searchConnector(conn, query, 50))
}

// searchTypes is the resource-type sweep a search covers — shared by
// the single-cluster path and the fleet fan-out.
var searchTypes = []string{
	"pods", "deployments", "statefulsets", "daemonsets", "jobs", "cronjobs",
	"services", "ingresses", "networkpolicies", "configmaps", "secrets", "nodes", "namespaces",
	"pvcs", "pvs", "hpas", "storageclasses", "pdbs",
	"certificates", "argocdapps", "vpas", "serviceaccounts",
	"ciliumnetworkpolicies", "ciliumclusterwidenetworkpolicies",
}

// searchConnector runs the name-substring sweep against one cluster's
// connector, capped at limit results.
func searchConnector(conn *cluster.Connector, query string, limit int) []searchResult {
	results := []searchResult{}
	for _, rt := range searchTypes {
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
	return results
}

func resourceTypeToKind(rt string) string {
	kinds := map[string]string{
		"pods": "Pod", "deployments": "Deployment", "statefulsets": "StatefulSet",
		"daemonsets": "DaemonSet", "jobs": "Job", "cronjobs": "CronJob",
		"services": "Service", "ingresses": "Ingress", "networkpolicies": "NetworkPolicy",
		"configmaps": "ConfigMap", "secrets": "Secret", "nodes": "Node", "namespaces": "Namespace",
		"pvcs": "PVC", "pvs": "PV", "hpas": "HPA", "storageclasses": "StorageClass",
		"pdbs":         "PodDisruptionBudget",
		"certificates": "Certificate", "argocdapps": "Application", "vpas": "VerticalPodAutoscaler",
		"serviceaccounts":       "ServiceAccount",
		"ciliumnetworkpolicies": "CiliumNetworkPolicy", "ciliumclusterwidenetworkpolicies": "CiliumClusterwideNetworkPolicy",
	}
	if k, ok := kinds[rt]; ok {
		return k
	}
	return rt
}
