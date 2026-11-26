package kobiplan

import (
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ObjectFacts is what DetectOwner reads from an object's metadata.
type ObjectFacts struct {
	Labels      map[string]string
	Annotations map[string]string
	OwnerRefs   []metav1.OwnerReference
	// Managers are the object's field managers with their operation, as
	// "manager/operation" (e.g. "eks/Apply"). Empty when they were not read:
	// the informer caches drop managedFields, so the caller reads them apart.
	Managers []string
}

// Provider markers (Kobi Plans spec, «Cómo se reconoce al proveedor»).
const (
	markerAKS        = "kubernetes.azure.com/managedby"
	markerEKS        = "eks.amazonaws.com/component"
	markerGKEPrefix  = "components.gke.io/"
	markerAddonMode  = "addonmanager.kubernetes.io/mode"
	markerEKSManager = "eks/Apply"

	labelManagedBy      = "app.kubernetes.io/managed-by"
	annotationHelmName  = "meta.helm.sh/release-name"
	annotationHelmNS    = "meta.helm.sh/release-namespace"
	annotationArgoCD    = "argocd.argoproj.io/tracking-id"
	labelArgoCDInstance = "argocd.argoproj.io/instance"
	labelFluxKustomize  = "kustomize.toolkit.fluxcd.io/name"
	labelFluxHelm       = "helm.toolkit.fluxcd.io/name"
)

// DetectOwner says who reconciles an object, and the marker that told. The
// order is the spec's: the provider first (its reconcile wins over anything
// else on the object), then an operator's controller reference, GitOps, Helm.
// With no marker the owner is unknown, never «none»: nothing here can prove
// that nobody reconciles it (P16).
func DetectOwner(f ObjectFacts) (Owner, string) {
	if f.Labels[markerAKS] == "aks" {
		return Owner{Kind: OwnerProvider, Ref: "aks"}, markerAKS
	}
	if _, ok := f.Labels[markerEKS]; ok {
		return Owner{Kind: OwnerProvider, Ref: "eks"}, markerEKS
	}
	for _, m := range f.Managers {
		if m == markerEKSManager {
			return Owner{Kind: OwnerProvider, Ref: "eks"}, "field manager eks (Apply)"
		}
	}
	for k := range f.Annotations {
		if strings.HasPrefix(k, markerGKEPrefix) {
			return Owner{Kind: OwnerProvider, Ref: "gke"}, k
		}
	}
	for k := range f.Labels {
		if strings.HasPrefix(k, markerGKEPrefix) {
			return Owner{Kind: OwnerProvider, Ref: "gke"}, k
		}
	}
	// EnsureExists means the addon manager creates the object and then leaves
	// it alone, so it stays editable.
	if f.Labels[markerAddonMode] == "Reconcile" {
		return Owner{Kind: OwnerProvider, Ref: "addon-manager"}, markerAddonMode + "=Reconcile"
	}

	for _, r := range f.OwnerRefs {
		if r.Controller != nil && *r.Controller {
			return Owner{Kind: OwnerOperator, Ref: r.Kind + "/" + r.Name}, "controller ownerReference"
		}
	}

	if v := f.Annotations[annotationArgoCD]; v != "" {
		return Owner{Kind: OwnerGitOps, Ref: "argocd:" + argoApp(v)}, annotationArgoCD
	}
	if v := f.Labels[labelArgoCDInstance]; v != "" {
		return Owner{Kind: OwnerGitOps, Ref: "argocd:" + v}, labelArgoCDInstance
	}
	if v := f.Labels[labelFluxKustomize]; v != "" {
		return Owner{Kind: OwnerGitOps, Ref: "flux:" + v}, labelFluxKustomize
	}
	if v := f.Labels[labelFluxHelm]; v != "" {
		return Owner{Kind: OwnerGitOps, Ref: "flux:" + v}, labelFluxHelm
	}

	if f.Labels[labelManagedBy] == "Helm" || f.Annotations[annotationHelmName] != "" {
		return Owner{Kind: OwnerHelm, Ref: f.Annotations[annotationHelmName], Namespace: f.Annotations[annotationHelmNS]}, "helm release"
	}
	return Owner{Kind: OwnerUnknown}, ""
}

// argoApp is the application in an Argo CD tracking id
// ("app:group/Kind:namespace/name").
func argoApp(trackingID string) string {
	if i := strings.IndexByte(trackingID, ':'); i > 0 {
		return trackingID[:i]
	}
	return trackingID
}
