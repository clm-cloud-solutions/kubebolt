package cluster

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	appslisters "k8s.io/client-go/listers/apps/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	networkinglisters "k8s.io/client-go/listers/networking/v1"
	k8stesting "k8s.io/client-go/testing"
)

const testTGARN = "arn:aws:elasticloadbalancing:us-east-2:111122223333:targetgroup/k8s-shopfron-shopweb-6b8a/2084"

// The lab's front: shop-web behind an ALB Ingress, an internal API with a
// LoadBalancer Service of its own, and a workload no load balancer reaches.
func entryFixture(t *testing.T, withTGB bool) *Connector {
	t.Helper()
	deploy := func(name string) *appsv1.Deployment {
		return &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop-frontend"},
			Spec:       appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}}}},
		}
	}
	svc := func(name string, typ corev1.ServiceType, host string) *corev1.Service {
		s := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop-frontend"},
			Spec:       corev1.ServiceSpec{Type: typ, Selector: map[string]string{"app": name}},
		}
		if host != "" {
			s.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{Hostname: host}}
		}
		return s
	}
	ing := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "shop-web", Namespace: "shop-frontend"},
		Spec: networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{
			Paths: []networkingv1.HTTPIngressPath{{Path: "/", Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "shop-web"}}}},
		}}}}},
		Status: networkingv1.IngressStatus{LoadBalancer: networkingv1.IngressLoadBalancerStatus{Ingress: []networkingv1.IngressLoadBalancerIngress{
			{Hostname: "shop-alb-1234567890.us-east-2.elb.amazonaws.com"},
		}}},
	}
	c := &Connector{
		deploymentLister: appslisters.NewDeploymentLister(indexer(t, deploy("shop-web"), deploy("admin-api"), deploy("loadgen"))),
		serviceLister: corelisters.NewServiceLister(indexer(t,
			svc("shop-web", corev1.ServiceTypeClusterIP, ""),
			svc("admin-api", corev1.ServiceTypeLoadBalancer, "admin-1.us-east-2.elb.amazonaws.com"))),
		ingressLister: networkinglisters.NewIngressLister(indexer(t, ing)),
	}
	if withTGB {
		gvr := schema.GroupVersionResource{Group: "elbv2.k8s.aws", Version: "v1beta1", Resource: "targetgroupbindings"}
		dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "TargetGroupBindingList"})
		tgb := &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "elbv2.k8s.aws/v1beta1", "kind": "TargetGroupBinding",
			"metadata": map[string]interface{}{"name": "k8s-shopfron-shopweb-6b8a", "namespace": "shop-frontend"},
			"spec":     map[string]interface{}{"targetGroupARN": testTGARN, "serviceRef": map[string]interface{}{"name": "shop-web", "port": int64(80)}},
		}}
		if _, err := dyn.Resource(gvr).Namespace("shop-frontend").Create(context.Background(), tgb, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		c.dynamicClient = dyn
	}
	return c
}

func TestEntryPoints_TheIngressInFrontOfAWorkload(t *testing.T) {
	c := entryFixture(t, true)
	points, omitted, problem := c.EntryPoints(context.Background(), "Deployment", "shop-frontend", "shop-web", true)
	if omitted != 0 || problem != "" || len(points) != 1 {
		t.Fatalf("points=%+v omitted=%d problem=%q", points, omitted, problem)
	}
	p := points[0]
	if p.Kind != "Ingress" || p.Name != "shop-web" || p.Address != "shop-alb-1234567890.us-east-2.elb.amazonaws.com" ||
		len(p.TargetGroups) != 1 || p.TargetGroups[0] != testTGARN {
		t.Fatalf("entry = %+v", p)
	}

	// A workload behind no load balancer has no entry.
	if points, _, _ := c.EntryPoints(context.Background(), "Deployment", "shop-frontend", "loadgen", true); len(points) != 0 {
		t.Fatalf("loadgen: %+v", points)
	}
	// The namespace: the Ingress first, then the LoadBalancer Service.
	points, _, _ = c.EntryPoints(context.Background(), "", "shop-frontend", "", true)
	if len(points) != 2 || points[0].Kind != "Ingress" || points[1].Kind != "Service" || points[1].Name != "admin-api" || len(points[1].TargetGroups) != 0 {
		t.Fatalf("namespace: %+v", points)
	}
}

// Off EKS nothing is read live; on a cluster without the AWS Load Balancer
// Controller there is no such resource, which is no problem.
func TestEntryPoints_TargetGroupsOnlyWhenAsked(t *testing.T) {
	c := entryFixture(t, true)
	points, _, _ := c.EntryPoints(context.Background(), "Deployment", "shop-frontend", "shop-web", false)
	if len(points) != 1 || len(points[0].TargetGroups) != 0 {
		t.Fatalf("points = %+v", points)
	}
	c = entryFixture(t, false)
	points, _, problem := c.EntryPoints(context.Background(), "Deployment", "shop-frontend", "shop-web", true)
	if len(points) != 1 || problem != "" {
		t.Fatalf("no dynamic client: %+v %q", points, problem)
	}

	// Without the right to read them, the answer says so.
	c = entryFixture(t, true)
	c.dynamicClient.(*dynamicfake.FakeDynamicClient).PrependReactor("list", "targetgroupbindings", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "elbv2.k8s.aws", Resource: "targetgroupbindings"}, "", nil)
	})
	points, _, problem = c.EntryPoints(context.Background(), "Deployment", "shop-frontend", "shop-web", true)
	if len(points) != 1 || len(points[0].TargetGroups) != 0 || problem != "targetgroupbindings (elbv2.k8s.aws) not readable" {
		t.Fatalf("forbidden: %+v %q", points, problem)
	}
}

func TestGetResources_AnAbsentCRDIsNotAPermission(t *testing.T) {
	c := &Connector{permissions: ResourcePermissions{
		"ciliumnetworkpolicies": {Resource: "ciliumnetworkpolicies", Absent: true},
		"networkpolicies":       {Resource: "networkpolicies"},
	}}
	if l := c.GetResources("ciliumnetworkpolicies", "", "", "", "", "", "", 1, 50); !l.Forbidden || !l.NotInstalled {
		t.Fatalf("absent CRD: %+v", l)
	}
	if l := c.GetResources("networkpolicies", "", "", "", "", "", "", 1, 50); !l.Forbidden || l.NotInstalled {
		t.Fatalf("denied: %+v", l)
	}
}
