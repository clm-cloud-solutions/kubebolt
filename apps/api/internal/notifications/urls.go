package notifications

import (
	"strings"

	"github.com/kubebolt/kubebolt/apps/api/internal/models"
)

// resourceKindToRoute maps Kubernetes resource kinds (as written in Insight.Resource)
// to the plural URL segment used by the KubeBolt frontend.
//
// Insight.Resource is formatted as "Kind/namespace/name" (e.g. "Pod/prod/api-abc").
// The frontend routes live at /:type/:namespace/:name (plural form).
var resourceKindToRoute = map[string]string{
	"Pod":         "pods",
	"Deployment":  "deployments",
	"StatefulSet": "statefulsets",
	"DaemonSet":   "daemonsets",
	"Job":         "jobs",
	"CronJob":     "cronjobs",
	"ReplicaSet":  "replicasets",
	"Service":     "services",
	"Ingress":     "ingresses",
	"Node":        "nodes",
	"Namespace":   "namespaces",
	"PVC":         "pvcs",
	"PV":          "pvs",
	"HPA":         "hpas",
	"ConfigMap":   "configmaps",
	"Secret":      "secrets",
}

// resourceURL returns a deep-link URL to the resource detail page in KubeBolt,
// or an empty string if no baseURL is configured or the resource can't be parsed.
//
// Example:
//
//	baseURL  = "https://kubebolt.example.com"
//	resource = "Pod/default/api-abc123"
//	→        = "https://kubebolt.example.com/pods/default/api-abc123"
//
// For cluster-scoped resources (Node, PV, Namespace) we still produce a link
// using the special "_" namespace that the frontend accepts.
func resourceURL(baseURL string, ins models.Insight) string {
	if baseURL == "" {
		return ""
	}
	parts := strings.SplitN(ins.Resource, "/", 3)
	if len(parts) < 2 {
		return ""
	}
	kind := parts[0]
	route, ok := resourceKindToRoute[kind]
	if !ok {
		return ""
	}

	var namespace, name string
	if len(parts) == 3 {
		namespace = parts[1]
		name = parts[2]
	} else {
		// Cluster-scoped resources (Node/PV/Namespace): Resource = "Kind/name"
		namespace = "_"
		name = parts[1]
	}
	if name == "" {
		return ""
	}

	// Trim trailing slash from baseURL to avoid double slashes
	base := strings.TrimRight(baseURL, "/")
	return base + "/" + route + "/" + namespace + "/" + name
}
