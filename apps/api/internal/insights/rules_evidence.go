package insights

import (
	"fmt"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubebolt/kubebolt/apps/api/internal/helm"
	"github.com/kubebolt/kubebolt/apps/api/internal/models"
)

// Typed evidence per rule. Every fact here is already in scope when the rule fires —
// these builders add no lookup, they only stop discarding. The doc's three
// rules apply: At only for a fact with a moment, taken from the fact; maps
// sorted and bounded; a timestamp's Detail is the age, not the time again.

// nodeNotReadyEvidence: the Ready condition with when it flipped, the other
// conditions that are true, whether the node is cordoned, and its age. A new
// node that never became Ready, an old one that stopped reporting during a
// drain, and one under DiskPressure are three different findings; node age
// and the transition time also feed the node_rotation burst classifier.
func nodeNotReadyEvidence(node *corev1.Node) []models.Evidence {
	var ev []models.Evidence
	var ready *corev1.NodeCondition
	var others []string
	for i := range node.Status.Conditions {
		c := &node.Status.Conditions[i]
		switch {
		case c.Type == corev1.NodeReady:
			ready = c
		case c.Status == corev1.ConditionTrue:
			others = append(others, string(c.Type))
		}
	}
	if ready == nil {
		ev = append(ev, models.Evidence{
			Kind: models.EvidenceConfig, Label: "Ready",
			Detail: "no Ready condition reported — the kubelet never posted status",
			Source: "node.status.conditions",
		})
	} else {
		d := fmt.Sprintf("%s (%s)", ready.Status, ready.Reason)
		if ready.Message != "" {
			d += ": " + ready.Message
		}
		e := models.Evidence{Kind: models.EvidenceEvent, Label: "Ready", Detail: d, Source: "node.status.conditions[Ready]"}
		if at := ready.LastTransitionTime.Time; !at.IsZero() {
			e.Detail += " — " + humanAge(time.Since(at)) + " before this was flagged"
			e.At = &at
		}
		ev = append(ev, e)
	}
	sort.Strings(others)
	ev = append(ev, models.Evidence{
		Kind: models.EvidenceConfig, Label: "Other conditions true",
		Detail: orNone(strings.Join(others, ", ")), Source: "node.status.conditions",
	})
	cordoned := "no"
	if node.Spec.Unschedulable {
		cordoned = "yes — cordoned, as in a drain or upgrade"
	}
	ev = append(ev, models.Evidence{Kind: models.EvidenceConfig, Label: "Cordoned", Detail: cordoned, Source: "node.spec.unschedulable"})
	if created := node.CreationTimestamp.Time; !created.IsZero() {
		ev = append(ev, models.Evidence{
			Kind: models.EvidenceEvent, Label: "Node created",
			Detail: humanAge(time.Since(created)) + " before this was flagged",
			Source: "metadata.creationTimestamp", At: &created,
		})
	}
	return ev
}

// restartEvidence: what crash-loop and frequent-restarts both need — how the
// last run ended and when, how many restarts against the bar, and the image
// that is running (a new tag that crashes points at the rollout; an old one
// at what it depends on).
func restartEvidence(pod *corev1.Pod, cs *corev1.ContainerStatus, threshold float64) []models.Evidence {
	var ev []models.Evidence
	if t := cs.LastTerminationState.Terminated; t != nil {
		e := models.Evidence{
			Kind: models.EvidenceEvent, Label: "Last termination", Detail: terminationDetail(t),
			Source: "pod.status.containerStatuses[].lastState.terminated",
		}
		if at := t.FinishedAt.Time; !at.IsZero() {
			e.At = &at
		}
		ev = append(ev, e)
	}
	ev = append(ev, models.Evidence{
		Kind: models.EvidenceMetric, Label: "Restarts",
		Detail: fmt.Sprintf("%d (fires above %g)", cs.RestartCount, threshold),
		Source: "pod.status.containerStatuses[].restartCount",
	})
	if c := containerSpec(pod, cs.Name); c != nil {
		ev = append(ev, models.Evidence{Kind: models.EvidenceConfig, Label: "Image", Detail: c.Image, Source: "pod.spec.containers[].image"})
	}
	return ev
}

// terminationDetail: "Error (exit 1)", "Completed (exit 0)", "OOMKilled
// (exit 137)". Exit 0 under CrashLoopBackOff is not one finding: a process
// that finishes on its own, or one the kubelet stopped because its liveness
// probe failed (a graceful server exits 0 on SIGTERM) — livenessKill tells
// them apart.
func terminationDetail(t *corev1.ContainerStateTerminated) string {
	reason := t.Reason
	if reason == "" {
		reason = "Terminated"
	}
	s := fmt.Sprintf("%s (exit %d)", reason, t.ExitCode)
	if t.Signal != 0 {
		s += fmt.Sprintf(", signal %d", t.Signal)
	}
	return s
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// usageEvidence: a measurement against the bar it crossed. With thresholds
// tunable per org (docs/44), an insight that does not record which bar it
// crossed cannot be audited.
func usageEvidence(label, used, limit string, pct, threshold float64) models.Evidence {
	return models.Evidence{
		Kind: models.EvidenceMetric, Label: label,
		Detail: fmt.Sprintf("%s of a %s limit, %.0f%% (fires above %.0f%%%s)", used, limit, pct, threshold*100, sustainedText()),
		Source: "metrics.k8s.io pod usage; pod.spec.containers[].resources.limits",
	}
}

// requestEvidence: resource-underrequest's measurement — the request as a
// share of what the pod uses, against the bar.
func requestEvidence(label, req, used string, share, threshold float64) models.Evidence {
	return models.Evidence{
		Kind: models.EvidenceMetric, Label: label,
		Detail: fmt.Sprintf("request %s, usage %s: the request is %.0f%% of usage (fires under %.0f%%%s)", req, used, share*100, threshold*100, sustainedText()),
		Source: "metrics.k8s.io pod usage; pod.spec.containers[].resources.requests",
	}
}

func sustainedText() string {
	if sustainedMetricWindow <= 0 {
		return ""
	}
	return ", sustained " + humanAge(sustainedMetricWindow)
}

// probeDetail renders a probe with its timings, defaults filled in as the
// kubelet applies them: the timings are what an aggressive probe gets wrong
// and are recorded nowhere else.
func probeDetail(p *corev1.Probe) string {
	if p == nil {
		return "none"
	}
	h := "unknown handler"
	switch {
	case p.HTTPGet != nil:
		h = fmt.Sprintf("httpGet %s on port %s", p.HTTPGet.Path, p.HTTPGet.Port.String())
	case p.TCPSocket != nil:
		h = "tcpSocket on port " + p.TCPSocket.Port.String()
	case p.GRPC != nil:
		h = fmt.Sprintf("grpc on port %d", p.GRPC.Port)
	case p.Exec != nil:
		cmd := strings.Join(p.Exec.Command, " ")
		if len(cmd) > 80 {
			cmd = cmd[:77] + "..."
		}
		h = "exec " + cmd
	}
	return fmt.Sprintf("%s; initialDelay %ds, period %ds, timeout %ds, failureThreshold %d",
		h, p.InitialDelaySeconds, int32Or(p.PeriodSeconds, 10), int32Or(p.TimeoutSeconds, 1), int32Or(p.FailureThreshold, 3))
}

func int32Or(v, def int32) int32 {
	if v == 0 {
		return def
	}
	return v
}

// readinessEvidence: the Ready condition with when it went False, and the
// readiness probe of the container that is not ready — or the readiness
// gates, which keep a pod out with no probe failing at all.
func readinessEvidence(pod *corev1.Pod, cond *corev1.PodCondition) []models.Evidence {
	d := cond.Reason
	if cond.Message != "" {
		d += ": " + cond.Message
	}
	at := cond.LastTransitionTime.Time
	ev := []models.Evidence{{
		Kind: models.EvidenceEvent, Label: "Ready",
		Detail: fmt.Sprintf("False (%s) — %s before this was flagged", d, humanAge(time.Since(at))),
		Source: "pod.status.conditions[Ready]", At: &at,
	}}
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Ready {
			continue
		}
		var probe *corev1.Probe
		if c := containerSpec(pod, cs.Name); c != nil {
			probe = c.ReadinessProbe
		}
		ev = append(ev, models.Evidence{
			Kind: models.EvidenceConfig, Label: "Readiness probe",
			Detail: cs.Name + ": " + probeDetail(probe), Source: "pod.spec.containers[].readinessProbe",
		})
		break
	}
	if len(pod.Spec.ReadinessGates) > 0 {
		gates := make([]string, 0, len(pod.Spec.ReadinessGates))
		for _, g := range pod.Spec.ReadinessGates {
			gates = append(gates, string(g.ConditionType))
		}
		sort.Strings(gates)
		ev = append(ev, models.Evidence{Kind: models.EvidenceConfig, Label: "Readiness gates", Detail: strings.Join(gates, ", "), Source: "pod.spec.readinessGates"})
	}
	return ev
}

// livenessEvidence: the probe's configuration and the failures with their
// last occurrence.
func livenessEvidence(pod *corev1.Pod, container string, ev *corev1.Event) []models.Evidence {
	last := eventLastOccurrence(ev)
	out := []models.Evidence{{
		Kind: models.EvidenceConfig, Label: "Liveness probe",
		Detail: orNone(container) + ": " + probeDetail(livenessProbeFor(pod, container)), Source: "pod.spec.containers[].livenessProbe",
	}}
	e := models.Evidence{
		Kind: models.EvidenceEvent, Label: "Failures",
		Detail: fmt.Sprintf("%d: %s", ev.Count, ev.Message), Source: "events (Unhealthy)",
	}
	if !last.IsZero() {
		e.At = &last
	}
	return append(out, e)
}

// zeroReplicasEvidence: desired against observed, and the conditions the
// Deployment controller wrote. ReplicaFailure (a ResourceQuota refusing the
// pods) and a deliberate scale are told apart only here.
func zeroReplicasEvidence(d *appsv1.Deployment) []models.Evidence {
	ev := []models.Evidence{{
		Kind: models.EvidenceConfig, Label: "Replicas",
		Detail: fmt.Sprintf("desired %d, %d created, %d ready, %d available, %d on the current revision",
			replicasOrOne(d.Spec.Replicas), d.Status.Replicas, d.Status.ReadyReplicas, d.Status.AvailableReplicas, d.Status.UpdatedReplicas),
		Source: "deployment.spec.replicas; deployment.status",
	}}
	for _, t := range []appsv1.DeploymentConditionType{appsv1.DeploymentReplicaFailure, appsv1.DeploymentAvailable, appsv1.DeploymentProgressing} {
		for _, c := range d.Status.Conditions {
			if c.Type != t {
				continue
			}
			e := models.Evidence{
				Kind: models.EvidenceEvent, Label: string(c.Type),
				Detail: fmt.Sprintf("%s (%s): %s", c.Status, c.Reason, c.Message), Source: "deployment.status.conditions[" + string(c.Type) + "]",
			}
			if at := c.LastUpdateTime.Time; !at.IsZero() {
				e.At = &at
			}
			ev = append(ev, e)
		}
	}
	return ev
}

// imagePullEvidence: the image, what the kubelet reported (the message names
// "not found", "unauthorized" or a network error), and the pull secrets.
func imagePullEvidence(pod *corev1.Pod, cs *corev1.ContainerStatus) []models.Evidence {
	var ev []models.Evidence
	if c := containerSpec(pod, cs.Name); c != nil {
		ev = append(ev, models.Evidence{Kind: models.EvidenceConfig, Label: "Image", Detail: c.Image, Source: "pod.spec.containers[].image"})
	}
	if w := cs.State.Waiting; w != nil {
		d := w.Reason
		if w.Message != "" {
			d += ": " + w.Message
		}
		ev = append(ev, models.Evidence{Kind: models.EvidenceEvent, Label: "Waiting", Detail: d, Source: "pod.status.containerStatuses[].state.waiting"})
	}
	secrets := make([]string, 0, len(pod.Spec.ImagePullSecrets))
	for _, s := range pod.Spec.ImagePullSecrets {
		secrets = append(secrets, s.Name)
	}
	sort.Strings(secrets)
	ev = append(ev, models.Evidence{Kind: models.EvidenceConfig, Label: "Pull secrets", Detail: orNone(strings.Join(secrets, ", ")), Source: "pod.spec.imagePullSecrets"})
	return ev
}

// hpaEvidence: replicas against both bounds, the target, and the CPU against
// its target when the autoscaling/v1 view carries it.
func hpaEvidence(hpa *autoscalingv1.HorizontalPodAutoscaler) []models.Evidence {
	ev := []models.Evidence{
		{Kind: models.EvidenceConfig, Label: "Replicas",
			Detail: fmt.Sprintf("%d current, min %d, max %d", hpa.Status.CurrentReplicas, replicasOrOne(hpa.Spec.MinReplicas), hpa.Spec.MaxReplicas),
			Source: "hpa.status.currentReplicas; hpa.spec"},
		{Kind: models.EvidenceConfig, Label: "Scale target",
			Detail: hpa.Spec.ScaleTargetRef.Kind + "/" + hpa.Spec.ScaleTargetRef.Name, Source: "hpa.spec.scaleTargetRef"},
	}
	if cur, tgt := hpa.Status.CurrentCPUUtilizationPercentage, hpa.Spec.TargetCPUUtilizationPercentage; cur != nil && tgt != nil {
		ev = append(ev, models.Evidence{Kind: models.EvidenceMetric, Label: "CPU",
			Detail: fmt.Sprintf("%d%% of requests, target %d%%", *cur, *tgt), Source: "hpa.status.currentCPUUtilizationPercentage"})
	}
	return ev
}

// progressDeadlineEvidence: the revision and the deadline the controller
// measured against.
func progressDeadlineEvidence(d *appsv1.Deployment, rev string) models.Evidence {
	deadline := "600s (default)"
	if d.Spec.ProgressDeadlineSeconds != nil {
		deadline = fmt.Sprintf("%ds", *d.Spec.ProgressDeadlineSeconds)
	}
	return models.Evidence{Kind: models.EvidenceConfig, Label: "Revision and deadline",
		Detail: fmt.Sprintf("revision %s, progress deadline %s", orNone(rev), deadline), Source: "metadata.annotations; spec.progressDeadlineSeconds"}
}

// configReferenceEvidence: what the pod references, as the kubelet named it.
func configReferenceEvidence(ce configError) (models.Evidence, bool) {
	switch ce.what {
	case "object":
		return models.Evidence{Kind: models.EvidenceConfig, Label: "Reference",
			Detail: fmt.Sprintf("%s %s — does not exist", ce.kind, ce.name), Source: "pod.spec (envFrom, env, volumes)"}, true
	case "key":
		return models.Evidence{Kind: models.EvidenceConfig, Label: "Reference",
			Detail: fmt.Sprintf("key %s in %s %s — the object exists, the key does not", ce.key, ce.kind, ce.name), Source: "pod.spec (env valueFrom)"}, true
	}
	return models.Evidence{}, false
}

// selectorDetail renders a label selector deterministically and bounded.
func selectorDetail(sel *metav1.LabelSelector) string {
	if sel == nil {
		return "none"
	}
	if len(sel.MatchExpressions) == 0 {
		return orNone(joinSortedPairs(sel.MatchLabels, evidenceMaxPairs))
	}
	return metav1.FormatLabelSelector(sel)
}

// noWorkloadEvidence records the absence that makes a selector a likely typo:
// no workload's pod template in the namespace matches it (one that matches
// keeps the rule from firing).
func noWorkloadEvidence() models.Evidence {
	return models.Evidence{Kind: models.EvidenceConfig, Label: "Matching workloads",
		Detail: "none — no Deployment, StatefulSet, DaemonSet, Job or CronJob pod template matches", Source: "pod templates in the namespace"}
}

func pdbEvidence(pdb *policyv1.PodDisruptionBudget) []models.Evidence {
	budget := "unset"
	switch {
	case pdb.Spec.MinAvailable != nil:
		budget = "minAvailable " + pdb.Spec.MinAvailable.String()
	case pdb.Spec.MaxUnavailable != nil:
		budget = "maxUnavailable " + pdb.Spec.MaxUnavailable.String()
	}
	return []models.Evidence{
		{Kind: models.EvidenceConfig, Label: "Selector", Detail: selectorDetail(pdb.Spec.Selector), Source: "spec.selector"},
		{Kind: models.EvidenceConfig, Label: "Budget", Detail: budget, Source: "spec.minAvailable / spec.maxUnavailable"},
		noWorkloadEvidence(),
	}
}

func networkPolicyEvidence(np *networkingv1.NetworkPolicy) []models.Evidence {
	types := make([]string, 0, len(np.Spec.PolicyTypes))
	for _, t := range np.Spec.PolicyTypes {
		types = append(types, string(t))
	}
	sort.Strings(types)
	return []models.Evidence{
		{Kind: models.EvidenceConfig, Label: "Pod selector", Detail: selectorDetail(&np.Spec.PodSelector), Source: "spec.podSelector"},
		{Kind: models.EvidenceConfig, Label: "Policy types", Detail: orNone(strings.Join(types, ", ")), Source: "spec.policyTypes"},
		noWorkloadEvidence(),
	}
}

// orphanNamespaceEvidence: what was counted, and what could not be read —
// "not read" is recorded as such, never as zero.
func orphanNamespaceEvidence(state *ClusterState, ns string, pods int) []models.Evidence {
	cilium, clusterwide := "not read", "not read"
	if state.CiliumPoliciesKnown {
		cilium = fmt.Sprintf("%d", state.CiliumPolicies[ns])
		clusterwide = fmt.Sprintf("%d", state.CiliumClusterwidePolicies)
	}
	return []models.Evidence{
		{Kind: models.EvidenceConfig, Label: "NetworkPolicies", Detail: "0", Source: "networking.k8s.io/v1 NetworkPolicy"},
		{Kind: models.EvidenceConfig, Label: "CiliumNetworkPolicies", Detail: cilium, Source: "cilium.io/v2 CiliumNetworkPolicy"},
		{Kind: models.EvidenceConfig, Label: "Cluster-wide Cilium policies", Detail: clusterwide, Source: "cilium.io/v2 CiliumClusterwideNetworkPolicy"},
		{Kind: models.EvidenceConfig, Label: "Running pods", Detail: fmt.Sprintf("%d", pods), Source: "pods in the namespace"},
	}
}

// claimEvidence: the class the claim resolves to (with its binding mode and
// provisioner, or why there is none) and the pods that mount it.
func claimEvidence(pvc *corev1.PersistentVolumeClaim, sc *storagev1.StorageClass, className string, noClass bool, users []*corev1.Pod) []models.Evidence {
	class := "none — no class named and no default class"
	switch {
	case noClass:
		class = `none — storageClassName "" (binds only to a pre-created PersistentVolume)`
	case sc != nil:
		mode := "Immediate"
		if sc.VolumeBindingMode != nil {
			mode = string(*sc.VolumeBindingMode)
		}
		class = fmt.Sprintf("%s (%s, provisioner %s)", sc.Name, mode, sc.Provisioner)
	case className != "":
		class = className + " — does not exist"
	}
	names := make([]string, 0, len(users))
	for _, p := range users {
		state := string(p.Status.Phase)
		if p.Spec.NodeName == "" {
			state += ", not scheduled"
		}
		names = append(names, fmt.Sprintf("%s (%s)", p.Name, state))
	}
	sort.Strings(names)
	return []models.Evidence{
		{Kind: models.EvidenceConfig, Label: "StorageClass", Detail: class, Source: "pvc.spec.storageClassName; storage.k8s.io StorageClass"},
		{Kind: models.EvidenceConfig, Label: "Mounted by", Detail: orNone(strings.Join(names, ", ")), Source: "pod.spec.volumes[].persistentVolumeClaim"},
	}
}

// certEvidence: when it expires (or expired), the issuer, its Ready status
// and when cert-manager means to renew it.
func certEvidence(c map[string]interface{}, days int) []models.Evidence {
	var ev []models.Evidence
	when := fmt.Sprintf("expires in %d day(s)", days)
	if days < 0 {
		when = fmt.Sprintf("expired %d day(s) ago", -days)
	}
	e := models.Evidence{Kind: models.EvidenceEvent, Label: "Not after", Detail: when, Source: "certificate.status.notAfter"}
	if s, _ := c["notAfter"].(string); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			e.At = &t
		}
	}
	ev = append(ev, e)
	for _, f := range []struct{ key, label, source string }{
		{"issuer", "Issuer", "certificate.spec.issuerRef.name"},
		{"status", "Ready", "certificate.status.conditions[Ready]"},
		{"renewalTime", "Renewal time", "certificate.status.renewalTime"},
	} {
		if v, _ := c[f.key].(string); v != "" {
			ev = append(ev, models.Evidence{Kind: models.EvidenceConfig, Label: f.label, Detail: v, Source: f.source})
		}
	}
	return ev
}

// argoEvidence: sync and health as Argo CD reports them, with the revision.
func argoEvidence(a map[string]interface{}) []models.Evidence {
	sync, _ := a["syncStatus"].(string)
	health, _ := a["healthStatus"].(string)
	rev, _ := a["revision"].(string)
	project, _ := a["project"].(string)
	if len(rev) > 12 {
		rev = rev[:12]
	}
	return []models.Evidence{
		{Kind: models.EvidenceConfig, Label: "Sync", Detail: fmt.Sprintf("%s at revision %s", orNone(sync), orNone(rev)), Source: "application.status.sync"},
		{Kind: models.EvidenceConfig, Label: "Health", Detail: orNone(health), Source: "application.status.health"},
		{Kind: models.EvidenceConfig, Label: "Project", Detail: orNone(project), Source: "application.spec.project"},
	}
}

// helmEvidence: the release as Helm recorded it, and when it last changed —
// the age is what tells a release stuck since minutes from one stuck since a
// month.
func helmEvidence(r helm.Release, changedLabel string) []models.Evidence {
	ev := []models.Evidence{{
		Kind: models.EvidenceConfig, Label: "Release",
		Detail: fmt.Sprintf("chart %s %s, revision %d, status %s", r.Chart, r.ChartVersion, r.Revision, r.Status),
		Source: "helm release secret",
	}}
	if !r.Updated.IsZero() {
		at := r.Updated
		d := humanAge(time.Since(at)) + " before this was flagged"
		if r.Description != "" {
			d = r.Description + " — " + d
		}
		ev = append(ev, models.Evidence{Kind: models.EvidenceEvent, Label: changedLabel, Detail: d, Source: "helm release info.last_deployed", At: &at})
	}
	return ev
}

// livenessKill finds the liveness failures behind a container's restarts.
// The liveness rule hands a container in back-off to crash-loop (no probes
// run while it waits), so the restart rules carry the cause themselves: in
// the lab (2026-10-11) a container killed by its probe read "Completed
// (exit 0)", which says the opposite of what happened.
func livenessKill(events []*corev1.Event, pod *corev1.Pod, container string) *corev1.Event {
	var latest *corev1.Event
	for _, e := range events {
		if e == nil || e.Reason != "Unhealthy" || !strings.Contains(e.Message, "Liveness probe failed") {
			continue
		}
		io := e.InvolvedObject
		if io.Kind != "Pod" || io.Namespace != pod.Namespace || io.Name != pod.Name {
			continue
		}
		if c := containerFromFieldPath(io.FieldPath); c != "" && c != container {
			continue
		}
		if latest == nil || eventLastOccurrence(e).After(eventLastOccurrence(latest)) {
			latest = e
		}
	}
	return latest
}

// restartsWithCause: restartEvidence plus the liveness failures when the
// kubelet is the one restarting the container.
func restartsWithCause(state *ClusterState, pod *corev1.Pod, cs *corev1.ContainerStatus, threshold float64) []models.Evidence {
	ev := restartEvidence(pod, cs, threshold)
	if kill := livenessKill(state.Events, pod, cs.Name); kill != nil {
		ev = append(ev, livenessEvidence(pod, cs.Name, kill)...)
	}
	return ev
}
