package insights

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// What a rule says about the cause is read from the cluster, not assumed.
// An insight's title, message and suggestion reach the operator in every
// notification and reach Kobi as context, so a cause the rule did not check
// is repeated as fact in both places. Audited 2026-10-11 after the
// evicted-pods insight blamed the node for a pod that went over its own
// storage limit: these helpers hold the facts the
// rules need to tell one cause from another.

// configError is a CreateContainerConfigError message, read. That waiting
// reason covers more than a missing ConfigMap or Secret: an existing one
// without the key the pod asks for, a kubelet that could not read it in time,
// or a setting that is no reference at all (runAsNonRoot with an image that
// runs as root). Each needs a different fix.
type configError struct {
	kind string // "ConfigMap", "Secret", or "" when the message names neither
	name string // the object, as the message names it ("ns/name" for a key)
	key  string // the missing key, when it is a key
	// what: "object" (it does not exist), "key" (it exists, the key does
	// not), "sync" (the kubelet could not fetch it), "nonroot" (runAsNonRoot
	// conflict) or "" (anything else).
	what string
}

var (
	configObjectMissingRE = regexp.MustCompile(`(?i)\b(configmaps?|secrets?) "([^"]+)" not found`)
	configKeyMissingRE    = regexp.MustCompile(`(?i)couldn't find key (\S+) in (ConfigMap|Secret) (\S+)`)
)

func readConfigError(msg string) configError {
	if m := configKeyMissingRE.FindStringSubmatch(msg); m != nil {
		return configError{kind: canonicalConfigKind(m[2]), key: m[1], name: m[3], what: "key"}
	}
	if m := configObjectMissingRE.FindStringSubmatch(msg); m != nil {
		return configError{kind: canonicalConfigKind(m[1]), name: m[2], what: "object"}
	}
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "failed to sync") && strings.Contains(lower, "cache"):
		ce := configError{what: "sync"}
		if strings.Contains(lower, "configmap") {
			ce.kind = "ConfigMap"
		} else if strings.Contains(lower, "secret") {
			ce.kind = "Secret"
		}
		return ce
	case strings.Contains(lower, "runasnonroot"):
		return configError{what: "nonroot"}
	}
	return configError{}
}

func canonicalConfigKind(s string) string {
	if strings.HasPrefix(strings.ToLower(s), "secret") {
		return "Secret"
	}
	return "ConfigMap"
}

// templateMatch is a workload whose pod template carries labels a selector
// matches, and how many pods it asks for right now.
type templateMatch struct {
	kind, name string
	// pods it asks for now: spec.replicas, a DaemonSet's desired count, 0
	// for a CronJob between runs and for a finished Job.
	wants int32
}

// workloadsMatching finds the workloads in ns whose pod template a selector
// matches. A selector that matches no pod but matches a template is not a
// typo: the workload is scaled to 0, between CronJob runs, or failing to
// create its pods (which another rule reports).
func workloadsMatching(state *ClusterState, ns string, sel labels.Selector) []templateMatch {
	var out []templateMatch
	add := func(kind, name string, tmpl map[string]string, wants int32) {
		if sel.Matches(labels.Set(tmpl)) {
			out = append(out, templateMatch{kind: kind, name: name, wants: wants})
		}
	}
	for _, d := range state.Deployments {
		if d.Namespace == ns {
			add("Deployment", d.Name, d.Spec.Template.Labels, replicasOrOne(d.Spec.Replicas))
		}
	}
	for _, s := range state.StatefulSets {
		if s.Namespace == ns {
			add("StatefulSet", s.Name, s.Spec.Template.Labels, replicasOrOne(s.Spec.Replicas))
		}
	}
	for _, ds := range state.DaemonSets {
		if ds.Namespace == ns {
			add("DaemonSet", ds.Name, ds.Spec.Template.Labels, ds.Status.DesiredNumberScheduled)
		}
	}
	for _, cj := range state.CronJobs {
		if cj.Namespace == ns {
			add("CronJob", cj.Name, cj.Spec.JobTemplate.Spec.Template.Labels, 0)
		}
	}
	for _, j := range state.Jobs {
		if j.Namespace == ns && len(j.OwnerReferences) == 0 { // a CronJob's Jobs are counted through it
			add("Job", j.Name, j.Spec.Template.Labels, j.Status.Active)
		}
	}
	return out
}

func replicasOrOne(r *int32) int32 {
	if r == nil {
		return 1
	}
	return *r
}

// storageClassFor resolves the class a PVC uses: its storageClassName, or the
// cluster's default class when the field is unset. explicitNone is true for
// storageClassName: "" — static binding to a pre-created PV, no class.
func storageClassFor(pvc *corev1.PersistentVolumeClaim, classes []*storagev1.StorageClass) (sc *storagev1.StorageClass, name string, explicitNone bool) {
	if pvc.Spec.StorageClassName != nil {
		name = *pvc.Spec.StorageClassName
		if name == "" {
			return nil, "", true
		}
		for _, c := range classes {
			if c.Name == name {
				return c, name, false
			}
		}
		return nil, name, false
	}
	for _, c := range classes {
		if c.Annotations["storageclass.kubernetes.io/is-default-class"] == "true" ||
			c.Annotations["storageclass.beta.kubernetes.io/is-default-class"] == "true" {
			return c, c.Name, false
		}
	}
	return nil, "", false
}

// podsUsingClaim returns the pods in the claim's namespace that mount it and
// have not finished.
func podsUsingClaim(pods []*corev1.Pod, pvc *corev1.PersistentVolumeClaim) []*corev1.Pod {
	var out []*corev1.Pod
	for _, p := range pods {
		if p.Namespace != pvc.Namespace || p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		for _, v := range p.Spec.Volumes {
			if v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName == pvc.Name {
				out = append(out, p)
				break
			}
		}
	}
	return out
}

// containerSpec returns the pod's container with this name.
func containerSpec(pod *corev1.Pod, name string) *corev1.Container {
	for i := range pod.Spec.Containers {
		if pod.Spec.Containers[i].Name == name {
			return &pod.Spec.Containers[i]
		}
	}
	return nil
}

// podLimitSum adds up the pod's container limits for a resource, in the unit
// the metrics use (millicores for CPU, bytes for memory). all is false when a
// container has none: the pod's usage, which counts every container, cannot
// be held against a sum that leaves one out.
func podLimitSum(pod *corev1.Pod, res corev1.ResourceName) (sum int64, all bool) {
	all = len(pod.Spec.Containers) > 0
	for _, c := range pod.Spec.Containers {
		q, ok := c.Resources.Limits[res]
		if !ok || q.IsZero() {
			all = false
			continue
		}
		if res == corev1.ResourceCPU {
			sum += q.MilliValue()
		} else {
			sum += q.Value()
		}
	}
	return sum, all
}

// formatBytes renders a byte count the way limits are written: 205Mi.
func formatBytes(b int64) string {
	const mi = 1 << 20
	if b >= 1<<30 && b%(1<<30) == 0 {
		return fmt.Sprintf("%dGi", b>>30)
	}
	return fmt.Sprintf("%dMi", (b+mi/2)/mi)
}

// resourceLimit returns a container's limit for a resource, as written.
func resourceLimit(c *corev1.Container, res corev1.ResourceName) (string, bool) {
	if c == nil {
		return "", false
	}
	q, ok := c.Resources.Limits[res]
	if !ok || q.IsZero() {
		return "", false
	}
	return q.String(), true
}

// notReadyFromProbe: a not-ready container that has a readiness probe and
// has been running past the grace (a run that just started has not had its
// probe's chance yet), or readiness gates on the pod — the cases where "not
// Ready" means a readiness check is failing. A container with neither is
// starting, which the restart rules own.
func notReadyFromProbe(pod *corev1.Pod, grace time.Duration) bool {
	if len(pod.Spec.ReadinessGates) > 0 {
		return true
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Ready {
			continue
		}
		c := containerSpec(pod, cs.Name)
		if c == nil || c.ReadinessProbe == nil {
			continue
		}
		if r := cs.State.Running; r != nil && !r.StartedAt.IsZero() && time.Since(r.StartedAt.Time) < grace {
			continue
		}
		return true
	}
	return false
}
