package copilot

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/kubectl/pkg/describe"

	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
)

// resourceTypeToGroupKind maps copilot resource type strings to K8s GroupKind.
// MUST stay in sync with apps/api/internal/api/describe.go (the REST endpoint
// uses the API package's map; the Copilot tool executor uses this one). A
// drift means the operator's UI Describe button works for a type but Kobi
// claims the type is unsupported, or vice versa. The drift test in
// describe_sync_test.go fails the build when these maps diverge — DO NOT
// remove the mirror; refactor both call sites to share if you must.
// SPEC §3.2.1 has the full coverage roadmap.
var ResourceTypeToGroupKind = map[string]schema.GroupKind{
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
	// Describe-only support (added 2026-05-15). See SPEC §3.2.1 for the
	// full UI-coverage roadmap of these types.
	"resourcequotas":       {Group: "", Kind: "ResourceQuota"},
	"limitranges":          {Group: "", Kind: "LimitRange"},
	"serviceaccounts":      {Group: "", Kind: "ServiceAccount"},
	"networkpolicies":      {Group: "networking.k8s.io", Kind: "NetworkPolicy"},
	"poddisruptionbudgets": {Group: "policy", Kind: "PodDisruptionBudget"},
	"priorityclasses":      {Group: "scheduling.k8s.io", Kind: "PriorityClass"},
	"ingressclasses":       {Group: "networking.k8s.io", Kind: "IngressClass"},
	// 1.14 first-class types — MUST match api/describe.go (sync test). The 3
	// CRD types have no built-in describer and use the generic fallback below.
	"pdbs":         {Group: "policy", Kind: "PodDisruptionBudget"},
	"certificates": {Group: "cert-manager.io", Kind: "Certificate"},
	"argocdapps":   {Group: "argoproj.io", Kind: "Application"},
	"vpas":         {Group: "autoscaling.k8s.io", Kind: "VerticalPodAutoscaler"},
}

// describeResource runs `kubectl describe` for the given resource and returns
// the formatted output as a string.
func describeResource(conn *cluster.Connector, resourceType, namespace, name string) (string, error) {
	gk, ok := ResourceTypeToGroupKind[resourceType]
	if !ok {
		return "", fmt.Errorf("unsupported resource type for describe: %s", resourceType)
	}
	describer, found := describe.DescriberFor(gk, conn.RestConfig())
	if !found {
		// Generic describer fallback for dynamic CRDs (no built-in describer).
		if gvr, ok := cluster.ResourceTypeGVR(resourceType); ok {
			mapping := &meta.RESTMapping{
				Resource:         gvr,
				GroupVersionKind: gvr.GroupVersion().WithKind(gk.Kind),
				Scope:            meta.RESTScopeNamespace,
			}
			describer, found = describe.GenericDescriberFor(mapping, conn.RestConfig())
		}
	}
	if !found {
		return "", fmt.Errorf("no describer available for: %s", resourceType)
	}
	return describer.Describe(namespace, name, describe.DescriberSettings{ShowEvents: true})
}
