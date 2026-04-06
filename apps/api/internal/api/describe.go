package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/kubectl/pkg/describe"
)

// resourceTypeToGroupKind maps KubeBolt resource type strings to K8s GroupKind
// for use with kubectl's DescriberFor.
var resourceTypeToGroupKind = map[string]schema.GroupKind{
	"pods":                {Group: "", Kind: "Pod"},
	"nodes":              {Group: "", Kind: "Node"},
	"namespaces":         {Group: "", Kind: "Namespace"},
	"services":           {Group: "", Kind: "Service"},
	"configmaps":         {Group: "", Kind: "ConfigMap"},
	"secrets":            {Group: "", Kind: "Secret"},
	"pvcs":               {Group: "", Kind: "PersistentVolumeClaim"},
	"persistentvolumeclaims": {Group: "", Kind: "PersistentVolumeClaim"},
	"pvs":                {Group: "", Kind: "PersistentVolume"},
	"persistentvolumes":  {Group: "", Kind: "PersistentVolume"},
	"events":             {Group: "", Kind: "Event"},
	"endpoints":          {Group: "", Kind: "Endpoints"},
	"deployments":        {Group: "apps", Kind: "Deployment"},
	"statefulsets":       {Group: "apps", Kind: "StatefulSet"},
	"daemonsets":         {Group: "apps", Kind: "DaemonSet"},
	"replicasets":        {Group: "apps", Kind: "ReplicaSet"},
	"jobs":               {Group: "batch", Kind: "Job"},
	"cronjobs":           {Group: "batch", Kind: "CronJob"},
	"ingresses":          {Group: "networking.k8s.io", Kind: "Ingress"},
	"hpas":               {Group: "autoscaling", Kind: "HorizontalPodAutoscaler"},
	"horizontalpodautoscalers": {Group: "autoscaling", Kind: "HorizontalPodAutoscaler"},
	"storageclasses":     {Group: "storage.k8s.io", Kind: "StorageClass"},
	"roles":              {Group: "rbac.authorization.k8s.io", Kind: "Role"},
	"clusterroles":       {Group: "rbac.authorization.k8s.io", Kind: "ClusterRole"},
	"rolebindings":       {Group: "rbac.authorization.k8s.io", Kind: "RoleBinding"},
	"clusterrolebindings": {Group: "rbac.authorization.k8s.io", Kind: "ClusterRoleBinding"},
	"endpointslices":     {Group: "discovery.k8s.io", Kind: "EndpointSlice"},
}

func (h *handlers) getResourceDescribe(w http.ResponseWriter, r *http.Request) {
	resourceType := chi.URLParam(r, "type")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	if namespace == "_" {
		namespace = ""
	}

	conn := h.manager.Connector()
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}

	gk, ok := resourceTypeToGroupKind[resourceType]
	if !ok {
		respondError(w, http.StatusBadRequest, "unsupported resource type for describe: "+resourceType)
		return
	}

	restConfig := conn.RestConfig()
	describer, found := describe.DescriberFor(gk, restConfig)
	if !found {
		respondError(w, http.StatusBadRequest, "no describer available for: "+resourceType)
		return
	}

	output, err := describer.Describe(namespace, name, describe.DescriberSettings{ShowEvents: true})
	if err != nil {
		respondError(w, http.StatusNotFound, err.Error())
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(output))
}
