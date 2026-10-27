package cluster

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// How requests from outside the cluster reach a workload. Pods Ready, Services
// with endpoints and 200s to callers inside the cluster say nothing about that
// path, and a model that reads only those concluded «the store works now»
// while every customer request timed out at the load balancer
// (alb-targets-unhealthy, 2026-10-22). Asked the next time, it found the
// target group by deduction, three of its five connector calls, and had none
// left for the network ACL that dropped the traffic. So the entry is read
// here, from what the cluster already holds: the Ingresses and LoadBalancer
// Services in front of the workload, the address their status gives, and on
// EKS the target groups the AWS Load Balancer Controller keeps the pods in.

// EntryPoint is one Ingress or Service of type LoadBalancer in front of a
// workload.
type EntryPoint struct {
	Kind      string // "Ingress" or "Service"
	Namespace string
	Name      string
	// Address is the hostname or IP its status holds; empty while the load
	// balancer has none.
	Address string
	// Services are the Services it sends to: an Ingress's backends, or the
	// LoadBalancer Service itself.
	Services []string
	// TargetGroups are the ARNs of the target groups the AWS Load Balancer
	// Controller registers those Services' pods in (its TargetGroupBindings).
	TargetGroups []string
}

const (
	// maxEntryPoints bounds the answer; the rest are counted.
	maxEntryPoints = 5
	// entryReadTimeout bounds the one live read, the TargetGroupBindings.
	entryReadTimeout = 5 * time.Second
)

var targetGroupBindingGVR = schema.GroupVersionResource{Group: "elbv2.k8s.aws", Version: "v1beta1", Resource: "targetgroupbindings"}

// EntryPoints returns the Ingresses and LoadBalancer Services of namespace
// ("" for every namespace) in front of the workload kind/name, or all of them
// when name is "", Ingresses first, at most maxEntryPoints, and how many it
// left out. problem says what could not be read, "" when everything could.
// Everything comes from the informer caches but the TargetGroupBindings, a
// live read made only with withTargetGroups (an EKS cluster).
func (c *Connector) EntryPoints(ctx context.Context, kind, namespace, name string, withTargetGroups bool) (points []EntryPoint, omitted int, problem string) {
	if c == nil {
		return nil, 0, ""
	}
	var selected map[string]bool // namespace/service in front of the workload
	if name != "" {
		podLabels, err := c.workloadTemplateLabels(kind, namespace, name)
		if err != nil {
			return nil, 0, ""
		}
		if selected, problem = c.servicesSelecting(namespace, podLabels); selected == nil {
			return nil, 0, problem
		}
	}
	admits := func(ns string, services []string) bool {
		if selected == nil {
			return true
		}
		for _, s := range services {
			if selected[ns+"/"+s] {
				return true
			}
		}
		return false
	}

	if c.ingressLister != nil && c.readableIn("ingresses", namespace) {
		for _, ing := range c.entryIngresses(namespace) {
			if p := ingressPoint(ing); admits(p.Namespace, p.Services) {
				points = append(points, p)
			}
		}
	}
	if c.serviceLister != nil && c.readableIn("services", namespace) {
		for _, s := range c.entryServices(namespace) {
			if s.Spec.Type != corev1.ServiceTypeLoadBalancer {
				continue
			}
			if p := servicePoint(s); admits(p.Namespace, p.Services) {
				points = append(points, p)
			}
		}
	}
	if len(points) == 0 {
		return nil, 0, problem
	}
	sort.SliceStable(points, func(i, j int) bool {
		if points[i].Kind != points[j].Kind {
			return points[i].Kind == "Ingress"
		}
		return points[i].Namespace+"/"+points[i].Name < points[j].Namespace+"/"+points[j].Name
	})
	if len(points) > maxEntryPoints {
		omitted = len(points) - maxEntryPoints
		points = points[:maxEntryPoints]
	}
	if !withTargetGroups {
		return points, omitted, problem
	}
	if groups, why := c.targetGroups(ctx, namespace); why != "" {
		problem = strings.TrimPrefix(problem+"; "+why, "; ")
	} else {
		for i := range points {
			for _, s := range points[i].Services {
				points[i].TargetGroups = append(points[i].TargetGroups, groups[points[i].Namespace+"/"+s]...)
			}
		}
	}
	return points, omitted, problem
}

// EntryPointFor returns the entry point one Ingress, or one Service of type
// LoadBalancer, is: what Kobi reads when it looks at the way in itself. ok is
// false for anything else, or an object the caches do not hold.
func (c *Connector) EntryPointFor(ctx context.Context, kind, namespace, name string, withTargetGroups bool) (p EntryPoint, problem string, ok bool) {
	if c == nil {
		return EntryPoint{}, "", false
	}
	switch kind {
	case "Ingress":
		if c.ingressLister == nil || !c.readableIn("ingresses", namespace) {
			return EntryPoint{}, "", false
		}
		ing, err := c.ingressLister.Ingresses(namespace).Get(name)
		if err != nil {
			return EntryPoint{}, "", false
		}
		p = ingressPoint(ing)
	case "Service":
		if c.serviceLister == nil || !c.readableIn("services", namespace) {
			return EntryPoint{}, "", false
		}
		s, err := c.serviceLister.Services(namespace).Get(name)
		if err != nil || s.Spec.Type != corev1.ServiceTypeLoadBalancer {
			return EntryPoint{}, "", false
		}
		p = servicePoint(s)
	default:
		return EntryPoint{}, "", false
	}
	if withTargetGroups {
		groups, why := c.targetGroups(ctx, namespace)
		if why != "" {
			return p, why, true
		}
		for _, s := range p.Services {
			p.TargetGroups = append(p.TargetGroups, groups[p.Namespace+"/"+s]...)
		}
	}
	return p, "", true
}

func ingressPoint(ing *networkingv1.Ingress) EntryPoint {
	return EntryPoint{Kind: "Ingress", Namespace: ing.Namespace, Name: ing.Name, Address: ingressAddress(ing), Services: ingressBackends(ing)}
}

func servicePoint(s *corev1.Service) EntryPoint {
	return EntryPoint{Kind: "Service", Namespace: s.Namespace, Name: s.Name, Address: serviceAddress(s), Services: []string{s.Name}}
}

// workloadTemplateLabels returns the labels of the workload's pods.
func (c *Connector) workloadTemplateLabels(kind, namespace, name string) (map[string]string, error) {
	switch normalizeWorkloadKind(kind) {
	case "Deployment":
		if c.deploymentLister != nil {
			o, err := c.deploymentLister.Deployments(namespace).Get(name)
			if err != nil {
				return nil, err
			}
			return o.Spec.Template.Labels, nil
		}
	case "StatefulSet":
		if c.statefulSetLister != nil {
			o, err := c.statefulSetLister.StatefulSets(namespace).Get(name)
			if err != nil {
				return nil, err
			}
			return o.Spec.Template.Labels, nil
		}
	case "DaemonSet":
		if c.daemonSetLister != nil {
			o, err := c.daemonSetLister.DaemonSets(namespace).Get(name)
			if err != nil {
				return nil, err
			}
			return o.Spec.Template.Labels, nil
		}
	case "ReplicaSet":
		if c.replicaSetLister != nil {
			o, err := c.replicaSetLister.ReplicaSets(namespace).Get(name)
			if err != nil {
				return nil, err
			}
			return o.Spec.Template.Labels, nil
		}
	case "Pod":
		if c.podLister != nil {
			o, err := c.podLister.Pods(namespace).Get(name)
			if err != nil {
				return nil, err
			}
			return o.Labels, nil
		}
	default:
		return nil, fmt.Errorf("no entry for workload kind %q", kind)
	}
	return nil, &PermissionDeniedError{Resource: strings.ToLower(normalizeWorkloadKind(kind)) + "s"}
}

// servicesSelecting returns the Services of namespace whose selector matches
// podLabels, as namespace/name, or nil with why when the Services cannot be
// read.
func (c *Connector) servicesSelecting(namespace string, podLabels map[string]string) (map[string]bool, string) {
	if c.serviceLister == nil || !c.readableIn("services", namespace) {
		return nil, "services not readable in " + namespace
	}
	out := map[string]bool{}
	for _, s := range c.entryServices(namespace) {
		if len(s.Spec.Selector) > 0 && labels.SelectorFromSet(s.Spec.Selector).Matches(labels.Set(podLabels)) {
			out[s.Namespace+"/"+s.Name] = true
		}
	}
	return out, ""
}

func (c *Connector) entryIngresses(namespace string) []*networkingv1.Ingress {
	var list []*networkingv1.Ingress
	var err error
	if namespace == "" {
		list, err = c.ingressLister.List(labels.Everything())
	} else {
		list, err = c.ingressLister.Ingresses(namespace).List(labels.Everything())
	}
	if err != nil {
		return nil
	}
	return list
}

func (c *Connector) entryServices(namespace string) []*corev1.Service {
	var list []*corev1.Service
	var err error
	if namespace == "" {
		list, err = c.serviceLister.List(labels.Everything())
	} else {
		list, err = c.serviceLister.Services(namespace).List(labels.Everything())
	}
	if err != nil {
		return nil
	}
	return list
}

// ingressBackends are the Services an Ingress sends to, default backend
// included, each once.
func ingressBackends(ing *networkingv1.Ingress) []string {
	seen := map[string]bool{}
	var out []string
	add := func(b *networkingv1.IngressBackend) {
		if b != nil && b.Service != nil && b.Service.Name != "" && !seen[b.Service.Name] {
			seen[b.Service.Name] = true
			out = append(out, b.Service.Name)
		}
	}
	add(ing.Spec.DefaultBackend)
	for _, r := range ing.Spec.Rules {
		if r.HTTP == nil {
			continue
		}
		for i := range r.HTTP.Paths {
			add(&r.HTTP.Paths[i].Backend)
		}
	}
	return out
}

func ingressAddress(ing *networkingv1.Ingress) string {
	for _, a := range ing.Status.LoadBalancer.Ingress {
		if a.Hostname != "" {
			return a.Hostname
		}
		if a.IP != "" {
			return a.IP
		}
	}
	return ""
}

func serviceAddress(s *corev1.Service) string {
	for _, a := range s.Status.LoadBalancer.Ingress {
		if a.Hostname != "" {
			return a.Hostname
		}
		if a.IP != "" {
			return a.IP
		}
	}
	return ""
}

// targetGroups reads the AWS Load Balancer Controller's TargetGroupBindings
// of namespace ("" for every one) as namespace/service → target group ARNs.
// A cluster without the controller has no such resource: that is no target
// groups, not a problem. why says what kept them from being read.
func (c *Connector) targetGroups(ctx context.Context, namespace string) (map[string][]string, string) {
	if c.dynamicClient == nil {
		return nil, ""
	}
	ctx, cancel := context.WithTimeout(ctx, entryReadTimeout)
	defer cancel()
	list, err := c.dynamicClient.Resource(targetGroupBindingGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return nil, ""
	case apierrors.IsForbidden(err):
		return nil, "targetgroupbindings (elbv2.k8s.aws) not readable"
	case err != nil:
		return nil, "targetgroupbindings (elbv2.k8s.aws) could not be read"
	}
	return targetGroupsByService(list.Items), ""
}

func targetGroupsByService(items []unstructured.Unstructured) map[string][]string {
	out := map[string][]string{}
	for _, it := range items {
		arn, _, _ := unstructured.NestedString(it.Object, "spec", "targetGroupARN")
		svc, _, _ := unstructured.NestedString(it.Object, "spec", "serviceRef", "name")
		if arn == "" || svc == "" {
			continue
		}
		key := it.GetNamespace() + "/" + svc
		out[key] = append(out[key], arn)
	}
	for k := range out {
		sort.Strings(out[k])
	}
	return out
}
