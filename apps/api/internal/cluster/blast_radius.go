package cluster

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// BlastRadius describes the cluster-state consequences of deleting a resource.
// It is computed from the local informer cache (no API server round trips)
// and attached to a delete proposal so the user sees concrete numbers in the
// confirmation card before clicking Execute.
//
// Fields are all-optional / additive: each per-type computation populates
// only what's relevant. The frontend renders any non-empty field as a
// bullet in the "What will happen" section.
type BlastRadius struct {
	// Pods that will be terminated (owned by the target).
	OwnedPods int `json:"ownedPods,omitempty"`

	// Names of pods that will be terminated; capped at ~10 to keep the
	// proposal payload bounded.
	OwnedPodNames []string `json:"ownedPodNames,omitempty"`

	// Services whose selector matches pods owned by this resource. They
	// won't be deleted, but will be left without endpoints.
	AffectedServices []string `json:"affectedServices,omitempty"`

	// HPAs whose scaleTargetRef points at this resource. They become
	// orphaned (target gone).
	AffectedHPAs []string `json:"affectedHPAs,omitempty"`

	// PVCs created by a StatefulSet's volumeClaimTemplates. Default
	// behavior retains them on STS deletion.
	OrphanedPVCs []string `json:"orphanedPVCs,omitempty"`

	// Pods that mount or read this ConfigMap or Secret (volumes, env,
	// envFrom, imagePullSecrets).
	UsingPods []string `json:"usingPods,omitempty"`

	// Ingresses that reference this Service (backend) or Secret (TLS).
	AffectedIngresses []string `json:"affectedIngresses,omitempty"`

	// Free-form notes worth surfacing to the user and the LLM.
	Notes []string `json:"notes,omitempty"`
}

// ComputeDeleteBlastRadius returns the consequences of deleting the given
// resource. Read-only against the informer cache; safe to call from a
// proposal tool (no mutation, no round trip).
func (c *Connector) ComputeDeleteBlastRadius(resourceType, namespace, name string) BlastRadius {
	switch resourceType {
	case "deployments":
		return c.deploymentBlastRadius(namespace, name)
	case "statefulsets":
		return c.statefulSetBlastRadius(namespace, name)
	case "daemonsets":
		return c.daemonSetBlastRadius(namespace, name)
	case "services":
		return c.serviceBlastRadius(namespace, name)
	case "configmaps":
		return c.configMapBlastRadius(namespace, name)
	case "secrets":
		return c.secretBlastRadius(namespace, name)
	case "jobs":
		return c.jobBlastRadius(namespace, name)
	case "cronjobs":
		return c.cronJobBlastRadius(namespace, name)
	case "pods":
		return c.podBlastRadius(namespace, name)
	case "ingresses":
		return c.ingressBlastRadius(namespace, name)
	}
	return BlastRadius{Notes: []string{fmt.Sprintf("blast radius computation not implemented for %s", resourceType)}}
}

// ─── Workloads ──────────────────────────────────────────────────────

func (c *Connector) deploymentBlastRadius(ns, name string) BlastRadius {
	br := BlastRadius{}
	if c.deploymentLister == nil {
		return br
	}
	dep, err := c.deploymentLister.Deployments(ns).Get(name)
	if err != nil {
		return BlastRadius{Notes: []string{"target deployment not found in informer cache"}}
	}

	pods := c.GetDeploymentPods(ns, name)
	br.OwnedPods = len(pods)
	br.OwnedPodNames = capPodNames(pods)

	templateLabels := labels.Set(dep.Spec.Template.Labels)
	br.AffectedServices = c.servicesMatchingLabels(ns, templateLabels)
	br.AffectedHPAs = c.hpasTargeting(ns, "Deployment", name)
	return br
}

func (c *Connector) statefulSetBlastRadius(ns, name string) BlastRadius {
	br := BlastRadius{}
	if c.statefulSetLister == nil {
		return br
	}
	sts, err := c.statefulSetLister.StatefulSets(ns).Get(name)
	if err != nil {
		return BlastRadius{Notes: []string{"target statefulset not found in informer cache"}}
	}

	pods := c.GetStatefulSetPods(ns, name)
	br.OwnedPods = len(pods)
	br.OwnedPodNames = capPodNames(pods)

	templateLabels := labels.Set(sts.Spec.Template.Labels)
	br.AffectedServices = c.servicesMatchingLabels(ns, templateLabels)
	br.AffectedHPAs = c.hpasTargeting(ns, "StatefulSet", name)

	// PVCs from volumeClaimTemplates, named "{vct.name}-{sts.name}-{ordinal}".
	if c.pvcLister != nil {
		pvcs, _ := c.pvcLister.PersistentVolumeClaims(ns).List(labels.Everything())
		for _, vct := range sts.Spec.VolumeClaimTemplates {
			prefix := vct.Name + "-" + name + "-"
			for _, pvc := range pvcs {
				if strings.HasPrefix(pvc.Name, prefix) {
					br.OrphanedPVCs = append(br.OrphanedPVCs, pvc.Name)
				}
			}
		}
		if len(br.OrphanedPVCs) > 0 {
			br.Notes = append(br.Notes,
				"PVCs from volumeClaimTemplates are retained by default. Delete them manually if you want the storage gone.")
		}
	}
	return br
}

func (c *Connector) daemonSetBlastRadius(ns, name string) BlastRadius {
	br := BlastRadius{}
	if c.daemonSetLister == nil {
		return br
	}
	ds, err := c.daemonSetLister.DaemonSets(ns).Get(name)
	if err != nil {
		return BlastRadius{Notes: []string{"target daemonset not found in informer cache"}}
	}

	pods := c.GetDaemonSetPods(ns, name)
	br.OwnedPods = len(pods)
	br.OwnedPodNames = capPodNames(pods)

	templateLabels := labels.Set(ds.Spec.Template.Labels)
	br.AffectedServices = c.servicesMatchingLabels(ns, templateLabels)
	return br
}

// ─── Services ───────────────────────────────────────────────────────

func (c *Connector) serviceBlastRadius(ns, name string) BlastRadius {
	br := BlastRadius{}
	if c.serviceLister == nil {
		return br
	}
	svc, err := c.serviceLister.Services(ns).Get(name)
	if err != nil {
		return BlastRadius{Notes: []string{"target service not found in informer cache"}}
	}

	if len(svc.Spec.Selector) > 0 && c.podLister != nil {
		sel := labels.SelectorFromSet(svc.Spec.Selector)
		pods, _ := c.podLister.Pods(ns).List(sel)
		br.OwnedPods = len(pods)
		br.OwnedPodNames = capPodNamesFromCorev1(pods)
		if br.OwnedPods > 0 {
			br.Notes = append(br.Notes,
				fmt.Sprintf("%d pod(s) currently selected by this service will lose this exposure (the pods are not deleted, but traffic via this Service stops).", br.OwnedPods))
		}
	}

	br.AffectedIngresses = c.ingressesReferencingService(ns, name)
	if len(br.AffectedIngresses) > 0 {
		br.Notes = append(br.Notes,
			"Ingresses still reference this Service; their routes will return errors after deletion.")
	}
	return br
}

// ─── Pods ───────────────────────────────────────────────────────────

func (c *Connector) podBlastRadius(ns, name string) BlastRadius {
	br := BlastRadius{}
	if c.podLister == nil {
		return br
	}
	pod, err := c.podLister.Pods(ns).Get(name)
	if err != nil {
		return BlastRadius{Notes: []string{"target pod not found in informer cache"}}
	}

	if len(pod.OwnerReferences) > 0 {
		owner := pod.OwnerReferences[0]
		br.Notes = append(br.Notes,
			fmt.Sprintf("Pod is owned by %s/%s — it will be recreated automatically by its controller. Net effect is similar to a restart of this single pod.", owner.Kind, owner.Name))
	} else {
		br.Notes = append(br.Notes,
			"Pod is NOT owned by a controller (standalone). Deletion is permanent — no automatic recreation.")
	}
	return br
}

// ─── Config & Secrets ──────────────────────────────────────────────

func (c *Connector) configMapBlastRadius(ns, name string) BlastRadius {
	br := BlastRadius{}
	br.UsingPods = c.podsUsingConfigMap(ns, name)
	if len(br.UsingPods) > 0 {
		br.Notes = append(br.Notes,
			"Pods that mount or read this ConfigMap will continue running with their current view, but a future restart will fail to mount it.")
	}
	return br
}

func (c *Connector) secretBlastRadius(ns, name string) BlastRadius {
	br := BlastRadius{}
	br.UsingPods = c.podsUsingSecret(ns, name)
	br.AffectedIngresses = c.ingressesUsingTLSSecret(ns, name)
	if len(br.UsingPods) > 0 {
		br.Notes = append(br.Notes,
			"Pods that mount or read this Secret will continue running with their current view, but a future restart will fail to mount it.")
	}
	if len(br.AffectedIngresses) > 0 {
		br.Notes = append(br.Notes,
			"Ingresses referencing this Secret for TLS will lose their certificate; HTTPS endpoints will break.")
	}
	return br
}

// ─── Jobs / CronJobs ───────────────────────────────────────────────

func (c *Connector) jobBlastRadius(ns, name string) BlastRadius {
	br := BlastRadius{}
	pods := c.GetJobPods(ns, name)
	br.OwnedPods = len(pods)
	br.OwnedPodNames = capPodNames(pods)
	if br.OwnedPods == 0 {
		br.Notes = append(br.Notes, "Job has no running pods (already completed or failed).")
	} else {
		br.Notes = append(br.Notes,
			"In-flight pods will be terminated. Job results (logs, exit codes) are lost unless captured elsewhere.")
	}
	return br
}

func (c *Connector) cronJobBlastRadius(ns, name string) BlastRadius {
	br := BlastRadius{}
	jobs := c.GetCronJobJobs(ns, name)
	if len(jobs) > 0 {
		jobNames := make([]string, 0, len(jobs))
		for _, j := range jobs {
			if jn, ok := j["name"].(string); ok {
				jobNames = append(jobNames, jn)
			}
		}
		if len(jobNames) > 5 {
			jobNames = append(jobNames[:5], fmt.Sprintf("... +%d more", len(jobs)-5))
		}
		// Reuse UsingPods slot for visual rendering of child jobs.
		br.UsingPods = jobNames
		br.Notes = append(br.Notes,
			fmt.Sprintf("CronJob has %d child Job(s). With default cascade=Background they are deleted with the CronJob; with cascade=Orphan they remain (and so do their pods).", len(jobs)))
	} else {
		br.Notes = append(br.Notes, "CronJob has no child Jobs to clean up.")
	}
	return br
}

// ─── Ingresses ─────────────────────────────────────────────────────

func (c *Connector) ingressBlastRadius(ns, name string) BlastRadius {
	br := BlastRadius{}
	if c.ingressLister == nil {
		return br
	}
	ing, err := c.ingressLister.Ingresses(ns).Get(name)
	if err != nil {
		return BlastRadius{Notes: []string{"target ingress not found in informer cache"}}
	}

	hosts := []string{}
	for _, rule := range ing.Spec.Rules {
		if rule.Host != "" {
			hosts = append(hosts, rule.Host)
		}
	}
	if len(hosts) > 0 {
		br.Notes = append(br.Notes,
			fmt.Sprintf("Routes for these hostnames will become unreachable: %s", strings.Join(hosts, ", ")))
	}

	backendSvcs := map[string]bool{}
	for _, rule := range ing.Spec.Rules {
		if rule.HTTP == nil {
			continue
		}
		for _, p := range rule.HTTP.Paths {
			if p.Backend.Service != nil {
				backendSvcs[p.Backend.Service.Name] = true
			}
		}
	}
	if ing.Spec.DefaultBackend != nil && ing.Spec.DefaultBackend.Service != nil {
		backendSvcs[ing.Spec.DefaultBackend.Service.Name] = true
	}
	if len(backendSvcs) > 0 {
		svcs := make([]string, 0, len(backendSvcs))
		for s := range backendSvcs {
			svcs = append(svcs, s)
		}
		br.AffectedServices = svcs
		br.Notes = append(br.Notes,
			"Backend services remain but lose this ingress route (other ingresses or in-cluster traffic still work).")
	}
	return br
}

// ─── Helpers ───────────────────────────────────────────────────────

// servicesMatchingLabels returns names of services whose selector matches
// the given pod template labels. Empty selectors are skipped (a service
// without selector — like ExternalName — has no endpoints to lose).
func (c *Connector) servicesMatchingLabels(ns string, podLabels labels.Set) []string {
	if c.serviceLister == nil {
		return nil
	}
	services, _ := c.serviceLister.Services(ns).List(labels.Everything())
	var out []string
	for _, svc := range services {
		if len(svc.Spec.Selector) == 0 {
			continue
		}
		sel := labels.SelectorFromSet(svc.Spec.Selector)
		if sel.Matches(podLabels) {
			out = append(out, svc.Name)
		}
	}
	return out
}

// hpasTargeting returns names of HPAs whose scaleTargetRef matches.
func (c *Connector) hpasTargeting(ns, kind, name string) []string {
	if c.hpaLister == nil {
		return nil
	}
	hpas, _ := c.hpaLister.HorizontalPodAutoscalers(ns).List(labels.Everything())
	var out []string
	for _, hpa := range hpas {
		ref := hpa.Spec.ScaleTargetRef
		if ref.Kind == kind && ref.Name == name {
			out = append(out, hpa.Name)
		}
	}
	return out
}

// ingressesReferencingService returns ingress names that route to the given service.
func (c *Connector) ingressesReferencingService(ns, svcName string) []string {
	if c.ingressLister == nil {
		return nil
	}
	ings, _ := c.ingressLister.Ingresses(ns).List(labels.Everything())
	var out []string
	for _, ing := range ings {
		matched := false
		for _, rule := range ing.Spec.Rules {
			if rule.HTTP == nil {
				continue
			}
			for _, p := range rule.HTTP.Paths {
				if p.Backend.Service != nil && p.Backend.Service.Name == svcName {
					matched = true
					break
				}
			}
			if matched {
				break
			}
		}
		if !matched && ing.Spec.DefaultBackend != nil && ing.Spec.DefaultBackend.Service != nil {
			if ing.Spec.DefaultBackend.Service.Name == svcName {
				matched = true
			}
		}
		if matched {
			out = append(out, ing.Name)
		}
	}
	return out
}

// ingressesUsingTLSSecret returns ingresses that reference the given secret in their tls config.
func (c *Connector) ingressesUsingTLSSecret(ns, secretName string) []string {
	if c.ingressLister == nil {
		return nil
	}
	ings, _ := c.ingressLister.Ingresses(ns).List(labels.Everything())
	var out []string
	for _, ing := range ings {
		for _, tls := range ing.Spec.TLS {
			if tls.SecretName == secretName {
				out = append(out, ing.Name)
				break
			}
		}
	}
	return out
}

// podsUsingConfigMap finds pods that reference the ConfigMap via volumes,
// projected sources, env.valueFrom.configMapKeyRef, or envFrom.configMapRef.
func (c *Connector) podsUsingConfigMap(ns, name string) []string {
	if c.podLister == nil {
		return nil
	}
	pods, _ := c.podLister.Pods(ns).List(labels.Everything())
	var out []string
	for _, pod := range pods {
		if podReferencesConfigMap(pod, name) {
			out = append(out, pod.Name)
		}
	}
	return out
}

// podsUsingSecret finds pods that reference the Secret via volumes,
// projected sources, env.valueFrom.secretKeyRef, envFrom.secretRef, or
// imagePullSecrets.
func (c *Connector) podsUsingSecret(ns, name string) []string {
	if c.podLister == nil {
		return nil
	}
	pods, _ := c.podLister.Pods(ns).List(labels.Everything())
	var out []string
	for _, pod := range pods {
		if podReferencesSecret(pod, name) {
			out = append(out, pod.Name)
		}
	}
	return out
}

func podReferencesConfigMap(pod *corev1.Pod, name string) bool {
	for _, v := range pod.Spec.Volumes {
		if v.ConfigMap != nil && v.ConfigMap.Name == name {
			return true
		}
		if v.Projected != nil {
			for _, src := range v.Projected.Sources {
				if src.ConfigMap != nil && src.ConfigMap.Name == name {
					return true
				}
			}
		}
	}
	if containerEnvUsesConfigMap(pod.Spec.Containers, name) {
		return true
	}
	if containerEnvUsesConfigMap(pod.Spec.InitContainers, name) {
		return true
	}
	return false
}

func podReferencesSecret(pod *corev1.Pod, name string) bool {
	for _, ips := range pod.Spec.ImagePullSecrets {
		if ips.Name == name {
			return true
		}
	}
	for _, v := range pod.Spec.Volumes {
		if v.Secret != nil && v.Secret.SecretName == name {
			return true
		}
		if v.Projected != nil {
			for _, src := range v.Projected.Sources {
				if src.Secret != nil && src.Secret.Name == name {
					return true
				}
			}
		}
	}
	if containerEnvUsesSecret(pod.Spec.Containers, name) {
		return true
	}
	if containerEnvUsesSecret(pod.Spec.InitContainers, name) {
		return true
	}
	return false
}

func containerEnvUsesConfigMap(containers []corev1.Container, name string) bool {
	for _, c := range containers {
		for _, e := range c.Env {
			if e.ValueFrom != nil && e.ValueFrom.ConfigMapKeyRef != nil && e.ValueFrom.ConfigMapKeyRef.Name == name {
				return true
			}
		}
		for _, ef := range c.EnvFrom {
			if ef.ConfigMapRef != nil && ef.ConfigMapRef.Name == name {
				return true
			}
		}
	}
	return false
}

func containerEnvUsesSecret(containers []corev1.Container, name string) bool {
	for _, c := range containers {
		for _, e := range c.Env {
			if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil && e.ValueFrom.SecretKeyRef.Name == name {
				return true
			}
		}
		for _, ef := range c.EnvFrom {
			if ef.SecretRef != nil && ef.SecretRef.Name == name {
				return true
			}
		}
	}
	return false
}

// capPodNames extracts up to N names from the slice returned by the existing
// GetXPods helpers (which return []map[string]interface{}). Cap keeps the
// proposal payload bounded and the card readable.
func capPodNames(pods []map[string]interface{}) []string {
	const max = 10
	if len(pods) == 0 {
		return nil
	}
	out := make([]string, 0, max)
	for i, p := range pods {
		if i >= max {
			out = append(out, fmt.Sprintf("... +%d more", len(pods)-max))
			break
		}
		if n, ok := p["name"].(string); ok {
			out = append(out, n)
		}
	}
	return out
}

// capPodNamesFromCorev1 is the same as capPodNames but for the slice
// returned by podLister.List(...) directly.
func capPodNamesFromCorev1(pods []*corev1.Pod) []string {
	const max = 10
	if len(pods) == 0 {
		return nil
	}
	out := make([]string, 0, max)
	for i, p := range pods {
		if i >= max {
			out = append(out, fmt.Sprintf("... +%d more", len(pods)-max))
			break
		}
		out = append(out, p.Name)
	}
	return out
}
