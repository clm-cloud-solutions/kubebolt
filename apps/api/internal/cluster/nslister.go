package cluster

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/labels"
	appslisters "k8s.io/client-go/listers/apps/v1"
	autoscalinglisters "k8s.io/client-go/listers/autoscaling/v1"
	batchlisters "k8s.io/client-go/listers/batch/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	discoverylisters "k8s.io/client-go/listers/discovery/v1"
	networkinglisters "k8s.io/client-go/listers/networking/v1"
	rbaclisters "k8s.io/client-go/listers/rbac/v1"
)

// ============================================================
// Helper: tryGet attempts Get(name) on multiple namespace listers
// until one succeeds. Used by all multi-lister Xxx(namespace) methods.
// ============================================================

// multiPodNSLister tries Get across multiple namespace listers.
type multiPodNSLister struct {
	nsListers []corelisters.PodNamespaceLister
}

func (m *multiPodNSLister) List(selector labels.Selector) ([]*corev1.Pod, error) {
	var result []*corev1.Pod
	for _, l := range m.nsListers {
		items, _ := l.List(selector)
		result = append(result, items...)
	}
	return result, nil
}

func (m *multiPodNSLister) Get(name string) (*corev1.Pod, error) {
	for _, l := range m.nsListers {
		item, err := l.Get(name)
		if err == nil {
			return item, nil
		}
	}
	return nil, fmt.Errorf("pod %q not found", name)
}

// ============================================================
// multiPodLister
// ============================================================

type multiPodLister struct {
	listers []corelisters.PodLister
}

func (m *multiPodLister) List(selector labels.Selector) ([]*corev1.Pod, error) {
	var result []*corev1.Pod
	for _, l := range m.listers {
		items, _ := l.List(selector)
		result = append(result, items...)
	}
	return result, nil
}

func (m *multiPodLister) Pods(namespace string) corelisters.PodNamespaceLister {
	var nsListers []corelisters.PodNamespaceLister
	for _, l := range m.listers {
		nsListers = append(nsListers, l.Pods(namespace))
	}
	return &multiPodNSLister{nsListers: nsListers}
}

// ============================================================
// multiServiceLister
// ============================================================

type multiServiceNSLister struct {
	nsListers []corelisters.ServiceNamespaceLister
}

func (m *multiServiceNSLister) List(selector labels.Selector) ([]*corev1.Service, error) {
	var result []*corev1.Service
	for _, l := range m.nsListers {
		items, _ := l.List(selector)
		result = append(result, items...)
	}
	return result, nil
}

func (m *multiServiceNSLister) Get(name string) (*corev1.Service, error) {
	for _, l := range m.nsListers {
		item, err := l.Get(name)
		if err == nil {
			return item, nil
		}
	}
	return nil, fmt.Errorf("service %q not found", name)
}

type multiServiceLister struct {
	listers []corelisters.ServiceLister
}

func (m *multiServiceLister) List(selector labels.Selector) ([]*corev1.Service, error) {
	var result []*corev1.Service
	for _, l := range m.listers {
		items, _ := l.List(selector)
		result = append(result, items...)
	}
	return result, nil
}

func (m *multiServiceLister) Services(namespace string) corelisters.ServiceNamespaceLister {
	var nsListers []corelisters.ServiceNamespaceLister
	for _, l := range m.listers {
		nsListers = append(nsListers, l.Services(namespace))
	}
	return &multiServiceNSLister{nsListers: nsListers}
}

// ============================================================
// multiEventLister
// ============================================================

type multiEventNSLister struct {
	nsListers []corelisters.EventNamespaceLister
}

func (m *multiEventNSLister) List(selector labels.Selector) ([]*corev1.Event, error) {
	var result []*corev1.Event
	for _, l := range m.nsListers {
		items, _ := l.List(selector)
		result = append(result, items...)
	}
	return result, nil
}

func (m *multiEventNSLister) Get(name string) (*corev1.Event, error) {
	for _, l := range m.nsListers {
		item, err := l.Get(name)
		if err == nil {
			return item, nil
		}
	}
	return nil, fmt.Errorf("event %q not found", name)
}

type multiEventLister struct {
	listers []corelisters.EventLister
}

func (m *multiEventLister) List(selector labels.Selector) ([]*corev1.Event, error) {
	var result []*corev1.Event
	for _, l := range m.listers {
		items, _ := l.List(selector)
		result = append(result, items...)
	}
	return result, nil
}

func (m *multiEventLister) Events(namespace string) corelisters.EventNamespaceLister {
	var nsListers []corelisters.EventNamespaceLister
	for _, l := range m.listers {
		nsListers = append(nsListers, l.Events(namespace))
	}
	return &multiEventNSLister{nsListers: nsListers}
}

// ============================================================
// multiConfigMapLister
// ============================================================

type multiConfigMapNSLister struct {
	nsListers []corelisters.ConfigMapNamespaceLister
}

func (m *multiConfigMapNSLister) List(selector labels.Selector) ([]*corev1.ConfigMap, error) {
	var result []*corev1.ConfigMap
	for _, l := range m.nsListers {
		items, _ := l.List(selector)
		result = append(result, items...)
	}
	return result, nil
}

func (m *multiConfigMapNSLister) Get(name string) (*corev1.ConfigMap, error) {
	for _, l := range m.nsListers {
		item, err := l.Get(name)
		if err == nil {
			return item, nil
		}
	}
	return nil, fmt.Errorf("configmap %q not found", name)
}

type multiConfigMapLister struct {
	listers []corelisters.ConfigMapLister
}

func (m *multiConfigMapLister) List(selector labels.Selector) ([]*corev1.ConfigMap, error) {
	var result []*corev1.ConfigMap
	for _, l := range m.listers {
		items, _ := l.List(selector)
		result = append(result, items...)
	}
	return result, nil
}

func (m *multiConfigMapLister) ConfigMaps(namespace string) corelisters.ConfigMapNamespaceLister {
	var nsListers []corelisters.ConfigMapNamespaceLister
	for _, l := range m.listers {
		nsListers = append(nsListers, l.ConfigMaps(namespace))
	}
	return &multiConfigMapNSLister{nsListers: nsListers}
}

// ============================================================
// multiSecretLister
// ============================================================

type multiSecretNSLister struct {
	nsListers []corelisters.SecretNamespaceLister
}

func (m *multiSecretNSLister) List(selector labels.Selector) ([]*corev1.Secret, error) {
	var result []*corev1.Secret
	for _, l := range m.nsListers {
		items, _ := l.List(selector)
		result = append(result, items...)
	}
	return result, nil
}

func (m *multiSecretNSLister) Get(name string) (*corev1.Secret, error) {
	for _, l := range m.nsListers {
		item, err := l.Get(name)
		if err == nil {
			return item, nil
		}
	}
	return nil, fmt.Errorf("secret %q not found", name)
}

type multiSecretLister struct {
	listers []corelisters.SecretLister
}

func (m *multiSecretLister) List(selector labels.Selector) ([]*corev1.Secret, error) {
	var result []*corev1.Secret
	for _, l := range m.listers {
		items, _ := l.List(selector)
		result = append(result, items...)
	}
	return result, nil
}

func (m *multiSecretLister) Secrets(namespace string) corelisters.SecretNamespaceLister {
	var nsListers []corelisters.SecretNamespaceLister
	for _, l := range m.listers {
		nsListers = append(nsListers, l.Secrets(namespace))
	}
	return &multiSecretNSLister{nsListers: nsListers}
}

// ============================================================
// multiPVCLister
// ============================================================

type multiPVCNSLister struct {
	nsListers []corelisters.PersistentVolumeClaimNamespaceLister
}

func (m *multiPVCNSLister) List(selector labels.Selector) ([]*corev1.PersistentVolumeClaim, error) {
	var result []*corev1.PersistentVolumeClaim
	for _, l := range m.nsListers {
		items, _ := l.List(selector)
		result = append(result, items...)
	}
	return result, nil
}

func (m *multiPVCNSLister) Get(name string) (*corev1.PersistentVolumeClaim, error) {
	for _, l := range m.nsListers {
		item, err := l.Get(name)
		if err == nil {
			return item, nil
		}
	}
	return nil, fmt.Errorf("pvc %q not found", name)
}

type multiPVCLister struct {
	listers []corelisters.PersistentVolumeClaimLister
}

func (m *multiPVCLister) List(selector labels.Selector) ([]*corev1.PersistentVolumeClaim, error) {
	var result []*corev1.PersistentVolumeClaim
	for _, l := range m.listers {
		items, _ := l.List(selector)
		result = append(result, items...)
	}
	return result, nil
}

func (m *multiPVCLister) PersistentVolumeClaims(namespace string) corelisters.PersistentVolumeClaimNamespaceLister {
	var nsListers []corelisters.PersistentVolumeClaimNamespaceLister
	for _, l := range m.listers {
		nsListers = append(nsListers, l.PersistentVolumeClaims(namespace))
	}
	return &multiPVCNSLister{nsListers: nsListers}
}

// ============================================================
// multiEndpointSliceLister
// ============================================================

type multiEndpointSliceNSLister struct {
	nsListers []discoverylisters.EndpointSliceNamespaceLister
}

func (m *multiEndpointSliceNSLister) List(selector labels.Selector) ([]*discoveryv1.EndpointSlice, error) {
	var result []*discoveryv1.EndpointSlice
	for _, l := range m.nsListers {
		items, _ := l.List(selector)
		result = append(result, items...)
	}
	return result, nil
}

func (m *multiEndpointSliceNSLister) Get(name string) (*discoveryv1.EndpointSlice, error) {
	for _, l := range m.nsListers {
		item, err := l.Get(name)
		if err == nil {
			return item, nil
		}
	}
	return nil, fmt.Errorf("endpointslice %q not found", name)
}

type multiEndpointSliceLister struct {
	listers []discoverylisters.EndpointSliceLister
}

func (m *multiEndpointSliceLister) List(selector labels.Selector) ([]*discoveryv1.EndpointSlice, error) {
	var result []*discoveryv1.EndpointSlice
	for _, l := range m.listers {
		items, _ := l.List(selector)
		result = append(result, items...)
	}
	return result, nil
}

func (m *multiEndpointSliceLister) EndpointSlices(namespace string) discoverylisters.EndpointSliceNamespaceLister {
	var nsListers []discoverylisters.EndpointSliceNamespaceLister
	for _, l := range m.listers {
		nsListers = append(nsListers, l.EndpointSlices(namespace))
	}
	return &multiEndpointSliceNSLister{nsListers: nsListers}
}

// ============================================================
// multiDeploymentLister
// ============================================================

type multiDeploymentNSLister struct {
	nsListers []appslisters.DeploymentNamespaceLister
}

func (m *multiDeploymentNSLister) List(selector labels.Selector) ([]*appsv1.Deployment, error) {
	var result []*appsv1.Deployment
	for _, l := range m.nsListers {
		items, _ := l.List(selector)
		result = append(result, items...)
	}
	return result, nil
}

func (m *multiDeploymentNSLister) Get(name string) (*appsv1.Deployment, error) {
	for _, l := range m.nsListers {
		item, err := l.Get(name)
		if err == nil {
			return item, nil
		}
	}
	return nil, fmt.Errorf("deployment %q not found", name)
}

type multiDeploymentLister struct {
	listers []appslisters.DeploymentLister
}

func (m *multiDeploymentLister) List(selector labels.Selector) ([]*appsv1.Deployment, error) {
	var result []*appsv1.Deployment
	for _, l := range m.listers {
		items, _ := l.List(selector)
		result = append(result, items...)
	}
	return result, nil
}

func (m *multiDeploymentLister) Deployments(namespace string) appslisters.DeploymentNamespaceLister {
	var nsListers []appslisters.DeploymentNamespaceLister
	for _, l := range m.listers {
		nsListers = append(nsListers, l.Deployments(namespace))
	}
	return &multiDeploymentNSLister{nsListers: nsListers}
}

// ============================================================
// multiStatefulSetLister
// ============================================================

type multiStatefulSetNSLister struct {
	nsListers []appslisters.StatefulSetNamespaceLister
}

func (m *multiStatefulSetNSLister) List(selector labels.Selector) ([]*appsv1.StatefulSet, error) {
	var result []*appsv1.StatefulSet
	for _, l := range m.nsListers {
		items, _ := l.List(selector)
		result = append(result, items...)
	}
	return result, nil
}

func (m *multiStatefulSetNSLister) Get(name string) (*appsv1.StatefulSet, error) {
	for _, l := range m.nsListers {
		item, err := l.Get(name)
		if err == nil {
			return item, nil
		}
	}
	return nil, fmt.Errorf("statefulset %q not found", name)
}

type multiStatefulSetLister struct {
	listers []appslisters.StatefulSetLister
}

func (m *multiStatefulSetLister) List(selector labels.Selector) ([]*appsv1.StatefulSet, error) {
	var result []*appsv1.StatefulSet
	for _, l := range m.listers {
		items, _ := l.List(selector)
		result = append(result, items...)
	}
	return result, nil
}

func (m *multiStatefulSetLister) StatefulSets(namespace string) appslisters.StatefulSetNamespaceLister {
	var nsListers []appslisters.StatefulSetNamespaceLister
	for _, l := range m.listers {
		nsListers = append(nsListers, l.StatefulSets(namespace))
	}
	return &multiStatefulSetNSLister{nsListers: nsListers}
}

func (m *multiStatefulSetLister) GetPodStatefulSets(pod *corev1.Pod) ([]*appsv1.StatefulSet, error) {
	for _, l := range m.listers {
		result, err := l.GetPodStatefulSets(pod)
		if err == nil && len(result) > 0 {
			return result, nil
		}
	}
	return nil, fmt.Errorf("no matching statefulsets found")
}

// ============================================================
// multiDaemonSetLister
// ============================================================

type multiDaemonSetNSLister struct {
	nsListers []appslisters.DaemonSetNamespaceLister
}

func (m *multiDaemonSetNSLister) List(selector labels.Selector) ([]*appsv1.DaemonSet, error) {
	var result []*appsv1.DaemonSet
	for _, l := range m.nsListers {
		items, _ := l.List(selector)
		result = append(result, items...)
	}
	return result, nil
}

func (m *multiDaemonSetNSLister) Get(name string) (*appsv1.DaemonSet, error) {
	for _, l := range m.nsListers {
		item, err := l.Get(name)
		if err == nil {
			return item, nil
		}
	}
	return nil, fmt.Errorf("daemonset %q not found", name)
}

type multiDaemonSetLister struct {
	listers []appslisters.DaemonSetLister
}

func (m *multiDaemonSetLister) List(selector labels.Selector) ([]*appsv1.DaemonSet, error) {
	var result []*appsv1.DaemonSet
	for _, l := range m.listers {
		items, _ := l.List(selector)
		result = append(result, items...)
	}
	return result, nil
}

func (m *multiDaemonSetLister) DaemonSets(namespace string) appslisters.DaemonSetNamespaceLister {
	var nsListers []appslisters.DaemonSetNamespaceLister
	for _, l := range m.listers {
		nsListers = append(nsListers, l.DaemonSets(namespace))
	}
	return &multiDaemonSetNSLister{nsListers: nsListers}
}

func (m *multiDaemonSetLister) GetPodDaemonSets(pod *corev1.Pod) ([]*appsv1.DaemonSet, error) {
	for _, l := range m.listers {
		result, err := l.GetPodDaemonSets(pod)
		if err == nil && len(result) > 0 {
			return result, nil
		}
	}
	return nil, fmt.Errorf("no matching daemonsets found")
}

func (m *multiDaemonSetLister) GetHistoryDaemonSets(history *appsv1.ControllerRevision) ([]*appsv1.DaemonSet, error) {
	for _, l := range m.listers {
		result, err := l.GetHistoryDaemonSets(history)
		if err == nil && len(result) > 0 {
			return result, nil
		}
	}
	return nil, fmt.Errorf("no matching daemonsets found")
}

// ============================================================
// multiReplicaSetLister
// ============================================================

type multiReplicaSetNSLister struct {
	nsListers []appslisters.ReplicaSetNamespaceLister
}

func (m *multiReplicaSetNSLister) List(selector labels.Selector) ([]*appsv1.ReplicaSet, error) {
	var result []*appsv1.ReplicaSet
	for _, l := range m.nsListers {
		items, _ := l.List(selector)
		result = append(result, items...)
	}
	return result, nil
}

func (m *multiReplicaSetNSLister) Get(name string) (*appsv1.ReplicaSet, error) {
	for _, l := range m.nsListers {
		item, err := l.Get(name)
		if err == nil {
			return item, nil
		}
	}
	return nil, fmt.Errorf("replicaset %q not found", name)
}

type multiReplicaSetLister struct {
	listers []appslisters.ReplicaSetLister
}

func (m *multiReplicaSetLister) List(selector labels.Selector) ([]*appsv1.ReplicaSet, error) {
	var result []*appsv1.ReplicaSet
	for _, l := range m.listers {
		items, _ := l.List(selector)
		result = append(result, items...)
	}
	return result, nil
}

func (m *multiReplicaSetLister) ReplicaSets(namespace string) appslisters.ReplicaSetNamespaceLister {
	var nsListers []appslisters.ReplicaSetNamespaceLister
	for _, l := range m.listers {
		nsListers = append(nsListers, l.ReplicaSets(namespace))
	}
	return &multiReplicaSetNSLister{nsListers: nsListers}
}

func (m *multiReplicaSetLister) GetPodReplicaSets(pod *corev1.Pod) ([]*appsv1.ReplicaSet, error) {
	for _, l := range m.listers {
		result, err := l.GetPodReplicaSets(pod)
		if err == nil && len(result) > 0 {
			return result, nil
		}
	}
	return nil, fmt.Errorf("no matching replicasets found")
}

// ============================================================
// multiJobLister
// ============================================================

type multiJobNSLister struct {
	nsListers []batchlisters.JobNamespaceLister
}

func (m *multiJobNSLister) List(selector labels.Selector) ([]*batchv1.Job, error) {
	var result []*batchv1.Job
	for _, l := range m.nsListers {
		items, _ := l.List(selector)
		result = append(result, items...)
	}
	return result, nil
}

func (m *multiJobNSLister) Get(name string) (*batchv1.Job, error) {
	for _, l := range m.nsListers {
		item, err := l.Get(name)
		if err == nil {
			return item, nil
		}
	}
	return nil, fmt.Errorf("job %q not found", name)
}

type multiJobLister struct {
	listers []batchlisters.JobLister
}

func (m *multiJobLister) List(selector labels.Selector) ([]*batchv1.Job, error) {
	var result []*batchv1.Job
	for _, l := range m.listers {
		items, _ := l.List(selector)
		result = append(result, items...)
	}
	return result, nil
}

func (m *multiJobLister) Jobs(namespace string) batchlisters.JobNamespaceLister {
	var nsListers []batchlisters.JobNamespaceLister
	for _, l := range m.listers {
		nsListers = append(nsListers, l.Jobs(namespace))
	}
	return &multiJobNSLister{nsListers: nsListers}
}

func (m *multiJobLister) GetPodJobs(pod *corev1.Pod) ([]batchv1.Job, error) {
	for _, l := range m.listers {
		result, err := l.GetPodJobs(pod)
		if err == nil && len(result) > 0 {
			return result, nil
		}
	}
	return nil, fmt.Errorf("no matching jobs found")
}

// ============================================================
// multiCronJobLister
// ============================================================

type multiCronJobNSLister struct {
	nsListers []batchlisters.CronJobNamespaceLister
}

func (m *multiCronJobNSLister) List(selector labels.Selector) ([]*batchv1.CronJob, error) {
	var result []*batchv1.CronJob
	for _, l := range m.nsListers {
		items, _ := l.List(selector)
		result = append(result, items...)
	}
	return result, nil
}

func (m *multiCronJobNSLister) Get(name string) (*batchv1.CronJob, error) {
	for _, l := range m.nsListers {
		item, err := l.Get(name)
		if err == nil {
			return item, nil
		}
	}
	return nil, fmt.Errorf("cronjob %q not found", name)
}

type multiCronJobLister struct {
	listers []batchlisters.CronJobLister
}

func (m *multiCronJobLister) List(selector labels.Selector) ([]*batchv1.CronJob, error) {
	var result []*batchv1.CronJob
	for _, l := range m.listers {
		items, _ := l.List(selector)
		result = append(result, items...)
	}
	return result, nil
}

func (m *multiCronJobLister) CronJobs(namespace string) batchlisters.CronJobNamespaceLister {
	var nsListers []batchlisters.CronJobNamespaceLister
	for _, l := range m.listers {
		nsListers = append(nsListers, l.CronJobs(namespace))
	}
	return &multiCronJobNSLister{nsListers: nsListers}
}

// ============================================================
// multiIngressLister
// ============================================================

type multiIngressNSLister struct {
	nsListers []networkinglisters.IngressNamespaceLister
}

func (m *multiIngressNSLister) List(selector labels.Selector) ([]*networkingv1.Ingress, error) {
	var result []*networkingv1.Ingress
	for _, l := range m.nsListers {
		items, _ := l.List(selector)
		result = append(result, items...)
	}
	return result, nil
}

func (m *multiIngressNSLister) Get(name string) (*networkingv1.Ingress, error) {
	for _, l := range m.nsListers {
		item, err := l.Get(name)
		if err == nil {
			return item, nil
		}
	}
	return nil, fmt.Errorf("ingress %q not found", name)
}

type multiIngressLister struct {
	listers []networkinglisters.IngressLister
}

func (m *multiIngressLister) List(selector labels.Selector) ([]*networkingv1.Ingress, error) {
	var result []*networkingv1.Ingress
	for _, l := range m.listers {
		items, _ := l.List(selector)
		result = append(result, items...)
	}
	return result, nil
}

func (m *multiIngressLister) Ingresses(namespace string) networkinglisters.IngressNamespaceLister {
	var nsListers []networkinglisters.IngressNamespaceLister
	for _, l := range m.listers {
		nsListers = append(nsListers, l.Ingresses(namespace))
	}
	return &multiIngressNSLister{nsListers: nsListers}
}

// ============================================================
// multiHPALister
// ============================================================

type multiHPANSLister struct {
	nsListers []autoscalinglisters.HorizontalPodAutoscalerNamespaceLister
}

func (m *multiHPANSLister) List(selector labels.Selector) ([]*autoscalingv1.HorizontalPodAutoscaler, error) {
	var result []*autoscalingv1.HorizontalPodAutoscaler
	for _, l := range m.nsListers {
		items, _ := l.List(selector)
		result = append(result, items...)
	}
	return result, nil
}

func (m *multiHPANSLister) Get(name string) (*autoscalingv1.HorizontalPodAutoscaler, error) {
	for _, l := range m.nsListers {
		item, err := l.Get(name)
		if err == nil {
			return item, nil
		}
	}
	return nil, fmt.Errorf("hpa %q not found", name)
}

type multiHPALister struct {
	listers []autoscalinglisters.HorizontalPodAutoscalerLister
}

func (m *multiHPALister) List(selector labels.Selector) ([]*autoscalingv1.HorizontalPodAutoscaler, error) {
	var result []*autoscalingv1.HorizontalPodAutoscaler
	for _, l := range m.listers {
		items, _ := l.List(selector)
		result = append(result, items...)
	}
	return result, nil
}

func (m *multiHPALister) HorizontalPodAutoscalers(namespace string) autoscalinglisters.HorizontalPodAutoscalerNamespaceLister {
	var nsListers []autoscalinglisters.HorizontalPodAutoscalerNamespaceLister
	for _, l := range m.listers {
		nsListers = append(nsListers, l.HorizontalPodAutoscalers(namespace))
	}
	return &multiHPANSLister{nsListers: nsListers}
}

// ============================================================
// multiRoleLister
// ============================================================

type multiRoleNSLister struct {
	nsListers []rbaclisters.RoleNamespaceLister
}

func (m *multiRoleNSLister) List(selector labels.Selector) ([]*rbacv1.Role, error) {
	var result []*rbacv1.Role
	for _, l := range m.nsListers {
		items, _ := l.List(selector)
		result = append(result, items...)
	}
	return result, nil
}

func (m *multiRoleNSLister) Get(name string) (*rbacv1.Role, error) {
	for _, l := range m.nsListers {
		item, err := l.Get(name)
		if err == nil {
			return item, nil
		}
	}
	return nil, fmt.Errorf("role %q not found", name)
}

type multiRoleLister struct {
	listers []rbaclisters.RoleLister
}

func (m *multiRoleLister) List(selector labels.Selector) ([]*rbacv1.Role, error) {
	var result []*rbacv1.Role
	for _, l := range m.listers {
		items, _ := l.List(selector)
		result = append(result, items...)
	}
	return result, nil
}

func (m *multiRoleLister) Roles(namespace string) rbaclisters.RoleNamespaceLister {
	var nsListers []rbaclisters.RoleNamespaceLister
	for _, l := range m.listers {
		nsListers = append(nsListers, l.Roles(namespace))
	}
	return &multiRoleNSLister{nsListers: nsListers}
}

// ============================================================
// multiRoleBindingLister
// ============================================================

type multiRoleBindingNSLister struct {
	nsListers []rbaclisters.RoleBindingNamespaceLister
}

func (m *multiRoleBindingNSLister) List(selector labels.Selector) ([]*rbacv1.RoleBinding, error) {
	var result []*rbacv1.RoleBinding
	for _, l := range m.nsListers {
		items, _ := l.List(selector)
		result = append(result, items...)
	}
	return result, nil
}

func (m *multiRoleBindingNSLister) Get(name string) (*rbacv1.RoleBinding, error) {
	for _, l := range m.nsListers {
		item, err := l.Get(name)
		if err == nil {
			return item, nil
		}
	}
	return nil, fmt.Errorf("rolebinding %q not found", name)
}

type multiRoleBindingLister struct {
	listers []rbaclisters.RoleBindingLister
}

func (m *multiRoleBindingLister) List(selector labels.Selector) ([]*rbacv1.RoleBinding, error) {
	var result []*rbacv1.RoleBinding
	for _, l := range m.listers {
		items, _ := l.List(selector)
		result = append(result, items...)
	}
	return result, nil
}

func (m *multiRoleBindingLister) RoleBindings(namespace string) rbaclisters.RoleBindingNamespaceLister {
	var nsListers []rbaclisters.RoleBindingNamespaceLister
	for _, l := range m.listers {
		nsListers = append(nsListers, l.RoleBindings(namespace))
	}
	return &multiRoleBindingNSLister{nsListers: nsListers}
}
