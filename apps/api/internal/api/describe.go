package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/kubectl/pkg/describe"

	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
)

// resourceTypeToGroupKind maps KubeBolt resource type strings to K8s GroupKind
// for use with kubectl's DescriberFor. When adding new entries here, see
// docs/SPEC.md §3.2.1 (Resource Type Coverage) — the UI side (list page,
// detail page, sidebar nav, route) needs to be expanded in lockstep before
// these types can be navigated to from the operator's perspective.
var resourceTypeToGroupKind = map[string]schema.GroupKind{
	"pods":                     {Group: "", Kind: "Pod"},
	"nodes":                    {Group: "", Kind: "Node"},
	"namespaces":               {Group: "", Kind: "Namespace"},
	"services":                 {Group: "", Kind: "Service"},
	"configmaps":               {Group: "", Kind: "ConfigMap"},
	"secrets":                  {Group: "", Kind: "Secret"},
	"pvcs":                     {Group: "", Kind: "PersistentVolumeClaim"},
	"persistentvolumeclaims":   {Group: "", Kind: "PersistentVolumeClaim"},
	"pvs":                      {Group: "", Kind: "PersistentVolume"},
	"persistentvolumes":        {Group: "", Kind: "PersistentVolume"},
	"events":                   {Group: "", Kind: "Event"},
	"endpoints":                {Group: "", Kind: "Endpoints"},
	"deployments":              {Group: "apps", Kind: "Deployment"},
	"statefulsets":             {Group: "apps", Kind: "StatefulSet"},
	"daemonsets":               {Group: "apps", Kind: "DaemonSet"},
	"replicasets":              {Group: "apps", Kind: "ReplicaSet"},
	"jobs":                     {Group: "batch", Kind: "Job"},
	"cronjobs":                 {Group: "batch", Kind: "CronJob"},
	"ingresses":                {Group: "networking.k8s.io", Kind: "Ingress"},
	"hpas":                     {Group: "autoscaling", Kind: "HorizontalPodAutoscaler"},
	"horizontalpodautoscalers": {Group: "autoscaling", Kind: "HorizontalPodAutoscaler"},
	"storageclasses":           {Group: "storage.k8s.io", Kind: "StorageClass"},
	"roles":                    {Group: "rbac.authorization.k8s.io", Kind: "Role"},
	"clusterroles":             {Group: "rbac.authorization.k8s.io", Kind: "ClusterRole"},
	"rolebindings":             {Group: "rbac.authorization.k8s.io", Kind: "RoleBinding"},
	"clusterrolebindings":      {Group: "rbac.authorization.k8s.io", Kind: "ClusterRoleBinding"},
	"endpointslices":           {Group: "discovery.k8s.io", Kind: "EndpointSlice"},
	// Describe-only support (added 2026-05-15): kubectl can describe these
	// natively but the rest of the platform — informers, list pages,
	// sidebar nav, detail tabs — still needs to catch up. See SPEC §3.2.1.
	// Removing any of these without the SPEC update silently regresses
	// Kobi's ability to investigate them (get_resource_describe tool).
	"resourcequotas":       {Group: "", Kind: "ResourceQuota"},
	"limitranges":          {Group: "", Kind: "LimitRange"},
	"serviceaccounts":      {Group: "", Kind: "ServiceAccount"},
	"networkpolicies":      {Group: "networking.k8s.io", Kind: "NetworkPolicy"},
	"poddisruptionbudgets": {Group: "policy", Kind: "PodDisruptionBudget"},
	"priorityclasses":      {Group: "scheduling.k8s.io", Kind: "PriorityClass"},
	"ingressclasses":       {Group: "networking.k8s.io", Kind: "IngressClass"},
	// 1.14 first-class types. "pdbs" is the short alias the routes use (the
	// built-in describer resolves via the policy/PodDisruptionBudget GK). The
	// 3 CRD types have NO built-in describer — they fall through to the
	// generic describer below (GroupKind here just makes them "recognized"
	// and supplies the Kind for the RESTMapping).
	"pdbs":         {Group: "policy", Kind: "PodDisruptionBudget"},
	"certificates": {Group: "cert-manager.io", Kind: "Certificate"},
	"argocdapps":   {Group: "argoproj.io", Kind: "Application"},
	"vpas":         {Group: "autoscaling.k8s.io", Kind: "VerticalPodAutoscaler"},
	// Cilium policy CRDs — no built-in describer (generic fallback below).
	// CiliumClusterwideNetworkPolicy is cluster-scoped; the fallback picks the
	// RESTScope from cluster.IsClusterScoped, so it must not be hardcoded.
	"ciliumnetworkpolicies":            {Group: "cilium.io", Kind: "CiliumNetworkPolicy"},
	"ciliumclusterwidenetworkpolicies": {Group: "cilium.io", Kind: "CiliumClusterwideNetworkPolicy"},
}

func (h *handlers) getResourceDescribe(w http.ResponseWriter, r *http.Request) {
	resourceType := chi.URLParam(r, "type")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	if namespace == "_" {
		namespace = ""
	}

	conn := h.manager.Connector(r.Context())
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
		// No built-in describer (dynamic CRDs like cert-manager / ArgoCD /
		// VPA / Cilium policies). Fall back to kubectl's generic describer via
		// a RESTMapping built from the type's GVR + Kind. Scope comes from
		// cluster.IsClusterScoped (CCNP is cluster-scoped; the rest namespaced).
		if gvr, ok := cluster.ResourceTypeGVR(resourceType); ok {
			scope := meta.RESTScope(meta.RESTScopeNamespace)
			if cluster.IsClusterScoped(resourceType) {
				scope = meta.RESTScopeRoot
			}
			mapping := &meta.RESTMapping{
				Resource:         gvr,
				GroupVersionKind: gvr.GroupVersion().WithKind(gk.Kind),
				Scope:            scope,
			}
			describer, found = describe.GenericDescriberFor(mapping, restConfig)
		}
	}
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
