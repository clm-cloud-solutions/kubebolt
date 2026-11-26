package kobiplan

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestDetectOwner(t *testing.T) {
	yes := true
	no := false
	cases := []struct {
		name   string
		facts  ObjectFacts
		want   Owner
		marker string
	}{
		{"aks", ObjectFacts{Labels: map[string]string{"kubernetes.azure.com/managedby": "aks"}},
			Owner{Kind: OwnerProvider, Ref: "aks"}, "kubernetes.azure.com/managedby"},
		{"eks label", ObjectFacts{Labels: map[string]string{"eks.amazonaws.com/component": "coredns"}},
			Owner{Kind: OwnerProvider, Ref: "eks"}, "eks.amazonaws.com/component"},
		{"eks field manager", ObjectFacts{Managers: []string{"kubectl/Update", "eks/Apply"}},
			Owner{Kind: OwnerProvider, Ref: "eks"}, "field manager eks (Apply)"},
		{"an eks update is not the add-on", ObjectFacts{Managers: []string{"eks/Update"}},
			Owner{Kind: OwnerUnknown}, ""},
		{"gke annotation", ObjectFacts{Annotations: map[string]string{"components.gke.io/component-name": "kubedns"}},
			Owner{Kind: OwnerProvider, Ref: "gke"}, "components.gke.io/component-name"},
		{"addon manager reconciles", ObjectFacts{Labels: map[string]string{"addonmanager.kubernetes.io/mode": "Reconcile"}},
			Owner{Kind: OwnerProvider, Ref: "addon-manager"}, "addonmanager.kubernetes.io/mode=Reconcile"},
		{"addon manager only creates", ObjectFacts{Labels: map[string]string{"addonmanager.kubernetes.io/mode": "EnsureExists"}},
			Owner{Kind: OwnerUnknown}, ""},
		{"the provider wins over helm", ObjectFacts{
			Labels:      map[string]string{"kubernetes.azure.com/managedby": "aks", "app.kubernetes.io/managed-by": "Helm"},
			Annotations: map[string]string{"meta.helm.sh/release-name": "x"}},
			Owner{Kind: OwnerProvider, Ref: "aks"}, "kubernetes.azure.com/managedby"},
		{"operator", ObjectFacts{OwnerRefs: []metav1.OwnerReference{{Kind: "Prometheus", Name: "k8s", Controller: &yes}}},
			Owner{Kind: OwnerOperator, Ref: "Prometheus/k8s"}, "controller ownerReference"},
		{"an owner that is not the controller", ObjectFacts{OwnerRefs: []metav1.OwnerReference{{Kind: "Thing", Name: "t", Controller: &no}}},
			Owner{Kind: OwnerUnknown}, ""},
		{"argo cd tracking id", ObjectFacts{Annotations: map[string]string{"argocd.argoproj.io/tracking-id": "shop:apps/Deployment:shop/web"}},
			Owner{Kind: OwnerGitOps, Ref: "argocd:shop"}, "argocd.argoproj.io/tracking-id"},
		{"argo cd instance label", ObjectFacts{Labels: map[string]string{"argocd.argoproj.io/instance": "shop"}},
			Owner{Kind: OwnerGitOps, Ref: "argocd:shop"}, "argocd.argoproj.io/instance"},
		{"flux kustomization", ObjectFacts{Labels: map[string]string{"kustomize.toolkit.fluxcd.io/name": "apps"}},
			Owner{Kind: OwnerGitOps, Ref: "flux:apps"}, "kustomize.toolkit.fluxcd.io/name"},
		{"gitops wins over helm", ObjectFacts{
			Labels: map[string]string{"helm.toolkit.fluxcd.io/name": "redis", "app.kubernetes.io/managed-by": "Helm"}},
			Owner{Kind: OwnerGitOps, Ref: "flux:redis"}, "helm.toolkit.fluxcd.io/name"},
		{"helm", ObjectFacts{
			Labels:      map[string]string{"app.kubernetes.io/managed-by": "Helm"},
			Annotations: map[string]string{"meta.helm.sh/release-name": "payments", "meta.helm.sh/release-namespace": "apps"}},
			Owner{Kind: OwnerHelm, Ref: "payments", Namespace: "apps"}, "helm release"},
		{"nothing", ObjectFacts{Labels: map[string]string{"app": "api"}}, Owner{Kind: OwnerUnknown}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, marker := DetectOwner(c.facts)
			if got != c.want || marker != c.marker {
				t.Fatalf("got %+v %q, want %+v %q", got, marker, c.want, c.marker)
			}
		})
	}
}
