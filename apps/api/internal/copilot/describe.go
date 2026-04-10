package copilot

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/kubectl/pkg/describe"

	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
)

// resourceTypeToGroupKind maps copilot resource type strings to K8s GroupKind.
// Mirrors the same map in apps/api/internal/api/describe.go.
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
}

// describeResource runs `kubectl describe` for the given resource and returns
// the formatted output as a string.
func describeResource(conn *cluster.Connector, resourceType, namespace, name string) (string, error) {
	gk, ok := resourceTypeToGroupKind[resourceType]
	if !ok {
		return "", fmt.Errorf("unsupported resource type for describe: %s", resourceType)
	}
	describer, found := describe.DescriberFor(gk, conn.RestConfig())
	if !found {
		return "", fmt.Errorf("no describer available for: %s", resourceType)
	}
	return describer.Describe(namespace, name, describe.DescriberSettings{ShowEvents: true})
}
