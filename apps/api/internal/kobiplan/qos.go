package kobiplan

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// QoS classes (plans-14). A step that changes a request or a limit can move
// the pod to another class: a Guaranteed pod becomes Burstable when a limit
// rises above its request or a request falls below its limit, and the kernel
// then kills it much sooner under memory pressure (oom_score_adj −997 → up
// to 999). The engine says so instead of leaving it to the person to notice.
const (
	QoSGuaranteed = "Guaranteed"
	QoSBurstable  = "Burstable"
	QoSBestEffort = "BestEffort"
)

// QoSClass is the class Kubernetes gives a pod with this spec: Guaranteed when
// every container (init containers included) has CPU and memory limits and
// requests equal to them (a request left out takes the limit), BestEffort
// when none has any request or limit, Burstable otherwise.
func QoSClass(spec corev1.PodSpec) string {
	all := append(append([]corev1.Container{}, spec.InitContainers...), spec.Containers...)
	guaranteed, any := true, false
	for _, c := range all {
		for _, name := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
			lim, hasLim := c.Resources.Limits[name]
			req, hasReq := c.Resources.Requests[name]
			if hasLim || hasReq {
				any = true
			}
			if !hasLim {
				guaranteed = false
				continue
			}
			if hasReq && req.Cmp(lim) != 0 {
				guaranteed = false
			}
		}
	}
	switch {
	case !any:
		return QoSBestEffort
	case guaranteed:
		return QoSGuaranteed
	}
	return QoSBurstable
}

// withChange is the spec with a step's values on the container they change.
func withChange(spec corev1.PodSpec, change ContainerResources) corev1.PodSpec {
	out := *spec.DeepCopy()
	list := out.Containers
	if change.InitContainer {
		list = out.InitContainers
	}
	for i := range list {
		if list[i].Name != change.Container {
			continue
		}
		set := func(rl *corev1.ResourceList, r *Resources) {
			if r == nil {
				return
			}
			if *rl == nil {
				*rl = corev1.ResourceList{}
			}
			if r.CPU != nil {
				(*rl)[corev1.ResourceCPU] = resource.MustParse(*r.CPU)
			}
			if r.Memory != nil {
				(*rl)[corev1.ResourceMemory] = resource.MustParse(*r.Memory)
			}
		}
		set(&list[i].Resources.Requests, change.Requests)
		set(&list[i].Resources.Limits, change.Limits)
	}
	return out
}

// QoSChange is what a step does to the pod's class: before and after, and,
// when the step takes a Guaranteed pod out of its class by raising a limit,
// the request that keeps it there (KeepWith: the limit's new value).
type QoSChange struct {
	Before   string     `json:"before"`
	After    string     `json:"after"`
	KeepWith *Resources `json:"keepWith,omitempty"`
}

// qosChange is the step's effect on the class; nil when it keeps it.
func qosChange(spec corev1.PodSpec, change ContainerResources) *QoSChange {
	before := QoSClass(spec)
	after := QoSClass(withChange(spec, change))
	if before == after {
		return nil
	}
	out := &QoSChange{Before: before, After: after}
	// A raised limit keeps the class when the request rises with it.
	if before == QoSGuaranteed && change.Limits != nil {
		keep := &Resources{}
		if change.Limits.CPU != nil {
			v := *change.Limits.CPU
			keep.CPU = &v
		}
		if change.Limits.Memory != nil {
			v := *change.Limits.Memory
			keep.Memory = &v
		}
		if keep.CPU != nil || keep.Memory != nil {
			out.KeepWith = keep
		}
	}
	return out
}
