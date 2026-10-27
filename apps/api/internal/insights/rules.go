package insights

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/google/uuid"
	"github.com/kubebolt/kubebolt/apps/api/internal/models"
)

// AllRules returns all Phase 1 insight rules.
func AllRules() []Rule {
	return []Rule{
		crashLoopRule(),
		oomKilledRule(),
		cpuThrottleRiskRule(),
		memoryPressureRule(),
		resourceUnderrequestRule(),
		zeroReplicasRule(),
		progressDeadlineExceededRule(),
		pvcPendingRule(),
		nodeNotReadyRule(),
		hpaMaxedOutRule(),
		frequentRestartsRule(),
		imagePullBackoffRule(),
		missingConfigDependencyRule(),
		readinessProbeFailingRule(),
		livenessProbeFailingRule(),
		evictedPodsRule(),
		serviceNoEndpointsRule(),
		networkPolicyNoMatchRule(),
		namespaceWithoutNetworkPolicyRule(),
		pdbNoMatchRule(),
		helmReleaseFailedRule(),
		helmReleaseHookPendingRule(),
		certExpiringRule(),
		argoOutOfSyncRule(),
	}
}

// certExpiringRule flags cert-manager Certificates that have expired or are
// within 14 days of expiry. A healthy auto-renewal clears the warning on its
// own; a persistent one means renewal is failing. (Sprint 3 insight.)
func certExpiringRule() Rule {
	return Rule{
		ID:       "cert-expiring",
		Name:     "Certificate expiring soon",
		Severity: "warning",
		Evaluate: func(state *ClusterState) []models.Insight {
			var insights []models.Insight
			for _, c := range state.Certificates {
				days, ok := c["expiresInDays"].(int)
				if !ok {
					continue
				}
				name, _ := c["name"].(string)
				ns, _ := c["namespace"].(string)
				switch {
				case days < 0:
					insights = append(insights, newInsight(
						"critical",
						fmt.Sprintf("Certificate/%s/%s", ns, name),
						"Certificate expired",
						fmt.Sprintf("cert-manager Certificate %s/%s expired %d day(s) ago — TLS against it now fails.", ns, name, -days),
						"Renewal likely failed; check the issuer and cert-manager logs. kubectl describe certificate "+name+" -n "+ns,
						certEvidence(c, days)...,
					))
				case float64(days) < stateThreshold(state, "cert-expiring", 14):
					insights = append(insights, newInsight(
						"warning",
						fmt.Sprintf("Certificate/%s/%s", ns, name),
						"Certificate expiring soon",
						fmt.Sprintf("cert-manager Certificate %s/%s expires in %d day(s). If auto-renewal is healthy this clears itself; if it persists, renewal is stuck.", ns, name, days),
						"Verify cert-manager is renewing it: kubectl describe certificate "+name+" -n "+ns,
						certEvidence(c, days)...,
					))
				}
			}
			return insights
		},
	}
}

// argoOutOfSyncRule flags ArgoCD Applications that are OutOfSync or Degraded
// — live cluster state has drifted from Git, or the app is unhealthy.
// (Sprint 3 insight.)
func argoOutOfSyncRule() Rule {
	return Rule{
		ID:       "argocd-out-of-sync",
		Name:     "ArgoCD Application not healthy",
		Severity: "warning",
		Evaluate: func(state *ClusterState) []models.Insight {
			var insights []models.Insight
			for _, a := range state.ArgoApps {
				name, _ := a["name"].(string)
				ns, _ := a["namespace"].(string)
				sync, _ := a["syncStatus"].(string)
				health, _ := a["healthStatus"].(string)
				var reasons []string
				if sync == "OutOfSync" {
					reasons = append(reasons, "OutOfSync")
				}
				if health == "Degraded" {
					reasons = append(reasons, "Degraded")
				}
				if len(reasons) == 0 {
					continue
				}
				insights = append(insights, newInsight(
					"warning",
					fmt.Sprintf("Application/%s/%s", ns, name),
					"ArgoCD Application not healthy",
					fmt.Sprintf("ArgoCD Application %s/%s is %s — live state has drifted from Git or the app is unhealthy.",
						ns, name, strings.Join(reasons, " + ")),
					"Inspect in ArgoCD or `kubectl describe application "+name+" -n "+ns+"`; sync or roll back as appropriate.",
					argoEvidence(a)...,
				))
			}
			return insights
		},
	}
}

// helmReleaseFailedRule flags Helm releases whose last action ended in a
// failed state — an install/upgrade that errored or rolled back, leaving the
// release unusable until an operator addresses it. (Sprint 4 insight.)
func helmReleaseFailedRule() Rule {
	return Rule{
		ID:       "helm-release-failed",
		Name:     "Helm release failed",
		Severity: "critical",
		Evaluate: func(state *ClusterState) []models.Insight {
			var insights []models.Insight
			for _, r := range state.HelmReleases {
				if !strings.EqualFold(r.Status, "failed") {
					continue
				}
				desc := r.Description
				if desc == "" {
					desc = "no description recorded"
				}
				insights = append(insights, newInsight(
					"critical",
					fmt.Sprintf("HelmRelease/%s/%s", r.Namespace, r.Name),
					"Helm release failed",
					fmt.Sprintf("Helm release %s/%s (chart %s %s) is in a failed state: %s",
						r.Namespace, r.Name, r.Chart, r.ChartVersion, desc),
					"Inspect with `helm status "+r.Name+" -n "+r.Namespace+"` and `helm history`. "+
						"Roll back to the last good revision or fix the values and upgrade.",
					helmEvidence(r, "Last change")...,
				))
			}
			return insights
		},
	}
}

// helmReleaseHookPendingRule flags releases stuck in a pending-* state for
// more than 5 minutes — typically a pre/post lifecycle hook that never
// completed, leaving the release lock held. (Sprint 4 insight.)
func helmReleaseHookPendingRule() Rule {
	return Rule{
		ID:       "helm-release-hook-pending",
		Name:     "Helm release stuck pending",
		Severity: "warning",
		Evaluate: func(state *ClusterState) []models.Insight {
			var insights []models.Insight
			for _, r := range state.HelmReleases {
				if !strings.HasPrefix(strings.ToLower(r.Status), "pending-") {
					continue
				}
				if r.Updated.IsZero() || time.Since(r.Updated) < 5*time.Minute {
					continue
				}
				insights = append(insights, newInsight(
					"warning",
					fmt.Sprintf("HelmRelease/%s/%s", r.Namespace, r.Name),
					"Helm release stuck pending",
					fmt.Sprintf("Helm release %s/%s has been in %q for over 5 minutes — a lifecycle hook likely never completed, holding the release lock.",
						r.Namespace, r.Name, r.Status),
					"Check hook pods (`kubectl get pods -n "+r.Namespace+"`). If wedged, `helm rollback` or delete the stuck hook job.",
					helmEvidence(r, "Pending since")...,
				))
			}
			return insights
		},
	}
}

// pdbNoMatchRule flags PodDisruptionBudgets whose selector matches zero pods
// in their namespace — the budget protects nothing, so a voluntary
// disruption (drain / Evict) won't be gated as the operator intends.
// Parallel to networkPolicyNoMatchRule. A nil/empty selector is skipped: an
// empty selector matches every pod (intentional), and a nil selector is a
// no-op PDB the apiserver tolerates.
func pdbNoMatchRule() Rule {
	return Rule{
		ID:       "pdb-no-match",
		Name:     "PodDisruptionBudget selector matches no pods",
		Severity: "warning",
		Evaluate: func(state *ClusterState) []models.Insight {
			var insights []models.Insight
			podsByNS := map[string][]*corev1.Pod{}
			for _, p := range state.Pods {
				podsByNS[p.Namespace] = append(podsByNS[p.Namespace], p)
			}
			for _, pdb := range state.PDBs {
				if pdb.Spec.Selector == nil {
					continue
				}
				if len(pdb.Spec.Selector.MatchLabels) == 0 && len(pdb.Spec.Selector.MatchExpressions) == 0 {
					continue // empty selector matches all — intentional
				}
				sel, err := metav1.LabelSelectorAsSelector(pdb.Spec.Selector)
				if err != nil {
					continue
				}
				matched := 0
				for _, p := range podsByNS[pdb.Namespace] {
					if sel.Matches(labels.Set(p.Labels)) {
						matched++
					}
				}
				if matched > 0 {
					continue
				}
				// A selector that matches a workload's pod template is right:
				// the workload is scaled to 0, between CronJob runs, or failing
				// to create pods (zero-replicas reports that). Only a selector
				// that matches no workload at all points at a typo.
				if len(workloadsMatching(state, pdb.Namespace, sel)) > 0 {
					continue
				}
				insights = append(insights, newInsight(
					"warning",
					fmt.Sprintf("PodDisruptionBudget/%s/%s", pdb.Namespace, pdb.Name),
					"PodDisruptionBudget selector matches no pods",
					fmt.Sprintf(
						"PodDisruptionBudget %s/%s matches no pod in its namespace, and no Deployment, StatefulSet, "+
							"DaemonSet, Job or CronJob there has pods with these labels. The budget protects nothing: "+
							"the selector has a typo, or the workload it protected was renamed or deleted "+
							"(or is run by a controller KubeBolt does not read).",
						pdb.Namespace, pdb.Name,
					),
					"Verify the PDB's spec.selector.matchLabels against the pods you expect it to protect. "+
						"kubectl get pods -n "+pdb.Namespace+" --show-labels",
					pdbEvidence(pdb)...,
				))
			}
			return insights
		},
	}
}

// newInsight builds one insight. `ev` is variadic so the 24 rules migrate to
// typed evidence ONE AT A TIME: every existing call compiles untouched, and a
// rule that has not been instrumented yet simply reports none. A big-bang
// rewrite of 27 call sites is how this work turns into a week instead of an
// afternoon.
func newInsight(severity, resource, title, message, suggestion string, ev ...models.Evidence) models.Insight {
	now := time.Now()
	// Extract namespace from resource string like "Pod/namespace/name"
	namespace := ""
	parts := strings.SplitN(resource, "/", 3)
	if len(parts) >= 3 {
		namespace = parts[1]
	}
	// Derive category from the resource kind
	category := "workload"
	if len(parts) >= 1 {
		switch parts[0] {
		case "Node":
			category = "node"
		case "PVC":
			category = "storage"
		case "HPA":
			category = "autoscaling"
		case "Service":
			category = "networking"
		case "NetworkPolicy":
			category = "networking"
		case "Namespace":
			category = "networking" // covers the "namespace without NetworkPolicy" rule
		}
	}
	return models.Insight{
		ID:         uuid.New().String(),
		Severity:   severity,
		Category:   category,
		Resource:   resource,
		Namespace:  namespace,
		Title:      title,
		Message:    message,
		Suggestion: suggestion,
		// Evidence rides along; EvidenceCount is what survives to the list.
		Evidence:      ev,
		EvidenceCount: len(ev),
		FirstSeen:     now,
		LastSeen:      now,
	}
}

// 1. CrashLoopBackOff with restarts > 3
func crashLoopRule() Rule {
	return Rule{
		ID:       "crash-loop",
		Name:     "CrashLoopBackOff Detection",
		Severity: "critical",
		Evaluate: func(state *ClusterState) []models.Insight {
			var insights []models.Insight
			for _, pod := range state.Pods {
				for _, cs := range pod.Status.ContainerStatuses {
					threshold := stateThreshold(state, "crash-loop", 3)
					if cs.State.Waiting != nil && cs.State.Waiting.Reason == "CrashLoopBackOff" && float64(cs.RestartCount) > threshold {
						message := fmt.Sprintf("Container %s in pod %s/%s is crash-looping with %d restarts", cs.Name, pod.Namespace, pod.Name, cs.RestartCount)
						suggestion := "Check container logs with 'kubectl logs' and review the container's command/args and liveness probes."
						ev := restartEvidence(pod, &cs, threshold)
						if kill := livenessKill(state.Events, pod, cs.Name); kill != nil {
							message += ": the kubelet restarts it because its liveness probe fails"
							suggestion = "The process is not crashing on its own — the kubelet kills it when the liveness probe fails. Check the probe (path, port, timings) against what the app does at startup and under load; if the app is genuinely hung, its logs before each kill say why."
							ev = append(ev, livenessEvidence(pod, cs.Name, kill)...)
						} else if t := cs.LastTerminationState.Terminated; t != nil && t.Reason == "OOMKilled" {
							// Killed for memory, not crashing in its code: the
							// command/args advice pointed the wrong way.
							message += ": each run is OOMKilled"
							suggestion = "It is killed for memory, not failing in its code: the oom-killed insight on this pod says whether it hit its own limit or the node ran out. Fix the memory, not the command."
						}
						insights = append(insights, newInsight(
							"critical",
							fmt.Sprintf("Pod/%s/%s", pod.Namespace, pod.Name),
							"Pod in CrashLoopBackOff",
							message, suggestion, ev...,
						))
					}
				}
			}
			return insights
		},
	}
}

// ─── Recovery evidence ───────────────────────────────────────────────────────
//
// An insight must clear because the system was OBSERVED to recover, never
// because a clock expired. A clock is dishonest at both ends: while it runs the
// insight asserts something false, and when it fires it asserts a recovery
// nobody observed. Operators see the first (a healthy workload flagged as
// broken) and Autopilot is driven by it — a stale insight keeps its
// reconcileClearedIncidents safety valve shut, so the incident cannot auto-close
// and re-triggers every cooldown. See docs/06-insights-rule-lifecycle-audit.md.
//
// Timers are permitted ONLY as a lost-signal fallback, and only when derived
// from the signal's own cadence — see probeSilenceWindow.

// readyGrace is how long a container's CURRENT run must have been healthy before
// we accept that it superseded an earlier failure.
//
// It has to sit inside a band: longer than zero (one lucky probe must not clear
// a real incident) and shorter than the anomaly's natural recurrence period (or
// a live loop would clear itself between kills). A kubelet liveness kill recurs
// every failureThreshold × periodSeconds — 30s at the Kubernetes defaults — and
// every kill RESTARTS the container, resetting the run clock, so a looping
// container can never reach this grace.
//
// A probe slow enough to recur beyond this grace (e.g. periodSeconds=60,
// failureThreshold=3 → 3 min) will clear and re-fire. That is correct, not a
// flap: two minutes of continuous health followed by another kill IS two
// distinct episodes, the engine models them as occurrences on one stable
// identity, and Autopilot's 10-min cooldown absorbs the re-trigger.
const readyGrace = 2 * time.Minute

// containerRecovered reports whether cs's current run supersedes a failure whose
// last evidence is at `since`: the run began at/after that moment, the container
// is Ready now, and the run has lasted at least readyGrace.
//
// Anchoring on State.Running.StartedAt is what makes this safe against a live
// crash loop — each kill resets StartedAt, so the run never accumulates the
// grace while the loop is running.
func containerRecovered(cs *corev1.ContainerStatus, since time.Time) bool {
	if cs == nil || cs.State.Running == nil || !cs.Ready {
		return false
	}
	started := cs.State.Running.StartedAt.Time
	if started.IsZero() || started.Before(since) {
		return false
	}
	return time.Since(started) >= readyGrace
}

// eventLastOccurrence returns the most recent moment an Event was observed.
// kubelet reports either the legacy aggregated shape (Count + LastTimestamp) or
// a Series (EventTime + Series.LastObservedTime), and managed distributions
// differ on which, so probe all four in decreasing order of precision. A zero
// return means "unknown" and callers must not treat it as old.
func eventLastOccurrence(ev *corev1.Event) time.Time {
	if ev == nil {
		return time.Time{}
	}
	if ev.Series != nil && !ev.Series.LastObservedTime.IsZero() {
		return ev.Series.LastObservedTime.Time
	}
	if !ev.LastTimestamp.IsZero() {
		return ev.LastTimestamp.Time
	}
	if !ev.EventTime.IsZero() {
		return ev.EventTime.Time
	}
	return ev.FirstTimestamp.Time
}

// probeSilenceFactor multiplies a probe's own failure cadence to get how long we
// keep believing a failure the container never restarted for. Three cycles of
// silence is well past any single missed emission.
const probeSilenceFactor = 3

// probeSilenceWindow derives the lost-signal fallback from the probe's OWN
// cadence (periodSeconds × failureThreshold × factor) rather than picking a
// constant, so a deliberately slow probe gets a proportionally longer window.
// A nil probe falls back to the Kubernetes defaults (10s × 3 → 90s).
//
// This is NOT the primary clear condition. When the container restarted,
// containerRecovered supersedes on evidence and this never applies; it covers
// only the case where the probe recovered in place (failures never reached
// failureThreshold, so no kill), where there genuinely is no live level to read
// and the absence of new events is the only evidence available.
func probeSilenceWindow(p *corev1.Probe) time.Duration {
	period := 10 * time.Second
	threshold := int32(3)
	if p != nil {
		if p.PeriodSeconds > 0 {
			period = time.Duration(p.PeriodSeconds) * time.Second
		}
		if p.FailureThreshold > 0 {
			threshold = p.FailureThreshold
		}
	}
	if w := period * time.Duration(threshold) * probeSilenceFactor; w > time.Minute {
		return w
	}
	return time.Minute
}

// containerFromFieldPath extracts the container name kubelet stamps on probe
// events as `spec.containers{name}`. Returns "" when absent — callers then fall
// back to the pod's sole container, or give up rather than guess.
func containerFromFieldPath(fp string) string {
	i := strings.Index(fp, "{")
	j := strings.LastIndex(fp, "}")
	if i < 0 || j <= i {
		return ""
	}
	return fp[i+1 : j]
}

// containerStatusFor resolves a container status by name, or the sole status
// when the name is unknown and there is exactly one container. Returns nil when
// it cannot be pinned down — we must not attribute one container's health to
// another.
func containerStatusFor(pod *corev1.Pod, name string) *corev1.ContainerStatus {
	if pod == nil {
		return nil
	}
	if name == "" {
		if len(pod.Status.ContainerStatuses) == 1 {
			return &pod.Status.ContainerStatuses[0]
		}
		return nil
	}
	for i := range pod.Status.ContainerStatuses {
		if pod.Status.ContainerStatuses[i].Name == name {
			return &pod.Status.ContainerStatuses[i]
		}
	}
	return nil
}

// livenessProbeFor resolves a container's liveness probe spec, same name-
// matching rules as containerStatusFor. nil is fine — probeSilenceWindow then
// uses the Kubernetes defaults.
func livenessProbeFor(pod *corev1.Pod, name string) *corev1.Probe {
	if pod == nil {
		return nil
	}
	if name == "" {
		if len(pod.Spec.Containers) == 1 {
			return pod.Spec.Containers[0].LivenessProbe
		}
		return nil
	}
	for i := range pod.Spec.Containers {
		if pod.Spec.Containers[i].Name == name {
			return pod.Spec.Containers[i].LivenessProbe
		}
	}
	return nil
}

// recentEvictionWindow bounds the evicted-pods rule. Unlike the OOM / restart /
// probe rules — which now clear on observed recovery — an evicted pod IS current
// state: the Failed/Evicted object exists until GC, so the condition stays
// legitimately true. The window here is a deliberate PRODUCT choice (a feed of
// recent evictions is actionable; a wall of week-old carcasses is not), which is
// why the rule's title says "recently" rather than hiding the clock.
const recentEvictionWindow = 30 * time.Minute

// podFailedRecently reports whether a Failed/Evicted pod entered that state
// within recentEvictionWindow.
func podFailedRecently(pod *corev1.Pod) bool {
	at := podFailedAt(pod)
	return !at.IsZero() && time.Since(at) < recentEvictionWindow
}

// podFailedAt approximates when a Failed/Evicted pod entered that state: its
// most recent status-condition transition, else its creation time.
func podFailedAt(pod *corev1.Pod) time.Time {
	latest := pod.CreationTimestamp.Time
	for _, c := range pod.Status.Conditions {
		if c.LastTransitionTime.Time.After(latest) {
			latest = c.LastTransitionTime.Time
		}
	}
	return latest
}

// Titles of the evicted-pods insight. The burst clusterer reads them
// (opcluster.go): only an eviction the node forced seeds node_pressure.
const (
	evictedTitle         = "Pod recently evicted"
	evictedOwnLimitTitle = "Pod recently evicted: over its own storage limit"
)

// evictionOverOwnLimit reads the kubelet's eviction message. Its eviction
// manager evicts for one of two reasons: the node ran low on a resource ("The
// node was low on resource: memory. …"), or the pod went over a limit of its
// own — "Pod ephemeral local storage usage exceeds the total limit of
// containers 32Mi.", "Container app exceeded its local ephemeral storage limit
// \"32Mi\".", "Usage of EmptyDir volume \"cache\" exceeds the limit \"1Gi\".".
// The second says nothing about the node: in the 2026-10-11 lab case the
// insight's "the node ran out of resources" sent Kobi to look for pressure on
// a node that had none.
func evictionOverOwnLimit(message string) bool {
	m := strings.ToLower(message)
	return strings.Contains(m, "exceeds the total limit of containers") ||
		strings.Contains(m, "exceeded its local ephemeral storage limit") ||
		(strings.Contains(m, "usage of emptydir volume") && strings.Contains(m, "exceeds the limit"))
}

// evictionByNodePressure: the kubelet evicted the pod to reclaim a resource
// the node was short of.
func evictionByNodePressure(message string) bool {
	m := strings.ToLower(message)
	return strings.Contains(m, "the node was low on resource") || strings.Contains(m, "the node had condition")
}

// 2. OOMKilled
func oomKilledRule() Rule {
	return Rule{
		ID:       "oom-killed",
		Name:     "OOMKilled Detection",
		Severity: "critical",
		Evaluate: func(state *ClusterState) []models.Insight {
			var insights []models.Insight
			for _, pod := range state.Pods {
				for i := range pod.Status.ContainerStatuses {
					cs := &pod.Status.ContainerStatuses[i]
					// LastTerminationState is sticky — it survives a healthy restart —
					// so it can only ARM this rule, never keep it alive. What clears it
					// is evidence: the replacement run holding Ready for readyGrace.
					// (Previously a 30-min clock, which asserted a broken workload for
					// half an hour after it recovered.)
					term := cs.LastTerminationState.Terminated
					if term != nil && term.Reason == "OOMKilled" &&
						!containerRecovered(cs, term.FinishedAt.Time) {
						message, suggestion, ev := oomText(pod, cs, term)
						insights = append(insights, newInsight(
							"critical",
							fmt.Sprintf("Pod/%s/%s", pod.Namespace, pod.Name),
							"Container OOMKilled",
							message, suggestion, ev...,
						))
					}
				}
			}
			return insights
		},
	}
}

// oomText reads the container's memory limit before naming a cause. The
// kernel reports OOMKilled both when a container reaches its own limit and
// when the node runs out of memory and picks it: without a limit only the
// second is possible, and raising "the limit" is no answer.
func oomText(pod *corev1.Pod, cs *corev1.ContainerStatus, term *corev1.ContainerStateTerminated) (message, suggestion string, ev []models.Evidence) {
	at := term.FinishedAt.Time
	ev = append(ev, models.Evidence{
		Kind: models.EvidenceEvent, Label: "Last termination", Detail: fmt.Sprintf("OOMKilled (exit %d)", term.ExitCode),
		Source: "pod.status.containerStatuses[].lastState.terminated", At: &at,
	})
	message = fmt.Sprintf("Container %s in pod %s/%s was OOMKilled (exit code %d)", cs.Name, pod.Namespace, pod.Name, term.ExitCode)
	c := containerSpec(pod, cs.Name)
	limit, ok := resourceLimit(c, corev1.ResourceMemory)
	if !ok {
		ev = append(ev, models.Evidence{Kind: models.EvidenceConfig, Label: "Memory limit", Detail: "none", Source: "pod.spec.containers[].resources.limits.memory"})
		return message + " with no memory limit of its own: the node ran out of memory and the kernel killed it.",
			"Check the node's memory: what else runs there, and pods whose requests are below what they use. Give this container a memory request and limit that match its usage, so the scheduler places it where it fits.",
			ev
	}
	ev = append(ev, models.Evidence{Kind: models.EvidenceConfig, Label: "Memory limit", Detail: limit, Source: "pod.spec.containers[].resources.limits.memory"})
	return message + fmt.Sprintf(" with a memory limit of %s.", limit),
		"Most often it reached that limit: raise it to what the process needs, or find what keeps growing (a leak, a cache, a burst). If its usage was well below the limit, the node itself ran out of memory.",
		ev
}

// sustainedMetricWindow is how long a pod's metric must stay over a rule's
// threshold before the insight fires — dampens brief spikes so a metric rule
// doesn't flap new→resolved on a borderline value. Package var so tests can set
// it. See docs/insights-rule-lifecycle-audit.md (flap-dampening).
var sustainedMetricWindow = 2 * time.Minute

// sustainedOver tracks, per resource key, since when it has been continuously
// over threshold. One instance per rule closure — rules are per-engine and
// evaluated serially; the mutex just guards against an overlapping on-demand eval.
type sustainedOver struct {
	mu    sync.Mutex
	since map[string]time.Time
}

func newSustainedOver() *sustainedOver { return &sustainedOver{since: map[string]time.Time{}} }

// fired takes the keys currently over threshold this eval and returns the subset
// that has been over for at least sustainedMetricWindow. Keys no longer over are
// forgotten, so a recovered-then-relapsed pod restarts its clock.
func (s *sustainedOver) fired(over map[string]bool) map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	out := map[string]bool{}
	for k := range over {
		if s.since[k].IsZero() {
			s.since[k] = now
		}
		if now.Sub(s.since[k]) >= sustainedMetricWindow {
			out[k] = true
		}
	}
	for k := range s.since {
		if !over[k] {
			delete(s.since, k)
		}
	}
	return out
}

// 3. CPU throttle risk (usage > 80% of limit, sustained)
func cpuThrottleRiskRule() Rule {
	tracker := newSustainedOver()
	return Rule{
		ID:       "cpu-throttle-risk",
		Name:     "CPU Throttle Risk",
		Severity: "warning",
		Evaluate: func(state *ClusterState) []models.Insight {
			over := map[string]bool{}
			pending := map[string]models.Insight{}
			for _, pod := range state.Pods {
				key := fmt.Sprintf("%s/%s", pod.Namespace, pod.Name)
				metrics := state.PodMetrics[key]
				if metrics == nil {
					continue
				}
				cpuLimit, limited := podLimitSum(pod, corev1.ResourceCPU)
				cpuBar := stateThreshold(state, "cpu-throttle-risk", 0.8)
				if limited && cpuLimit > 0 && float64(metrics.CPUUsage)/float64(cpuLimit) > cpuBar {
					over[key] = true
					pending[key] = newInsight(
						"warning",
						fmt.Sprintf("Pod/%s/%s", pod.Namespace, pod.Name),
						"CPU Throttle Risk",
						fmt.Sprintf("Pod %s/%s is using %.0f%% of CPU limit (%dm/%dm)", pod.Namespace, pod.Name, float64(metrics.CPUUsage)/float64(cpuLimit)*100, metrics.CPUUsage, cpuLimit),
						"Consider increasing CPU limits or optimizing CPU-intensive operations.",
						usageEvidence("CPU", fmt.Sprintf("%dm", metrics.CPUUsage), fmt.Sprintf("%dm", cpuLimit),
							float64(metrics.CPUUsage)/float64(cpuLimit)*100, cpuBar),
					)
				}
			}
			// Emit only pods sustained over the window; re-walk state.Pods so the
			// output order stays deterministic.
			fired := tracker.fired(over)
			var insights []models.Insight
			for _, pod := range state.Pods {
				if key := fmt.Sprintf("%s/%s", pod.Namespace, pod.Name); fired[key] {
					insights = append(insights, pending[key])
				}
			}
			return insights
		},
	}
}

// 4. Memory near the pod's own limit (usage > 85% of limit, sustained). The
// id keeps its old name; the title does not say "Memory Pressure", which is
// the name of a NODE condition this rule never reads.
func memoryPressureRule() Rule {
	tracker := newSustainedOver()
	return Rule{
		ID:       "memory-pressure",
		Name:     "Memory Near Limit",
		Severity: "warning",
		Evaluate: func(state *ClusterState) []models.Insight {
			over := map[string]bool{}
			pending := map[string]models.Insight{}
			for _, pod := range state.Pods {
				key := fmt.Sprintf("%s/%s", pod.Namespace, pod.Name)
				metrics := state.PodMetrics[key]
				if metrics == nil {
					continue
				}
				// The usage is the pod's, every container included: against
				// the sum of limits it only means something when every
				// container has one.
				memLimit, limited := podLimitSum(pod, corev1.ResourceMemory)
				memBar := stateThreshold(state, "memory-pressure", 0.85)
				if limited && memLimit > 0 && float64(metrics.MemUsage)/float64(memLimit) > memBar {
					pct := float64(metrics.MemUsage) / float64(memLimit) * 100
					over[key] = true
					pending[key] = newInsight(
						"warning",
						fmt.Sprintf("Pod/%s/%s", pod.Namespace, pod.Name),
						"Memory Near Limit",
						fmt.Sprintf("Pod %s/%s is using %.0f%% of its own memory limit (%s of %s)", pod.Namespace, pod.Name, pct,
							formatBytes(metrics.MemUsage), formatBytes(memLimit)),
						"This is the pod's own limit, not memory pressure on the node. Raise the limit to what the process needs, or find what keeps growing, before the container is OOMKilled at 100%.",
						usageEvidence("Memory", formatBytes(metrics.MemUsage), formatBytes(memLimit), pct, memBar),
					)
				}
			}
			fired := tracker.fired(over)
			var insights []models.Insight
			for _, pod := range state.Pods {
				if key := fmt.Sprintf("%s/%s", pod.Namespace, pod.Name); fired[key] {
					insights = append(insights, pending[key])
				}
			}
			return insights
		},
	}
}

// 5. Resource underrequest (requests < 40% of actual usage, sustained)
func resourceUnderrequestRule() Rule {
	tracker := newSustainedOver()
	return Rule{
		ID:       "resource-underrequest",
		Name:     "Resource Under-Request",
		Severity: "info",
		Evaluate: func(state *ClusterState) []models.Insight {
			// Two independent metric conditions per pod (cpu / mem), so track
			// them under distinct keys — a pod can be CPU-underrequested but not
			// memory-underrequested, and each dampens on its own clock.
			over := map[string]bool{}
			pending := map[string]models.Insight{}
			for _, pod := range state.Pods {
				key := fmt.Sprintf("%s/%s", pod.Namespace, pod.Name)
				metrics := state.PodMetrics[key]
				if metrics == nil {
					continue
				}
				var cpuReq, memReq int64
				for _, c := range pod.Spec.Containers {
					cpuReq += c.Resources.Requests.Cpu().MilliValue()
					memReq += c.Resources.Requests.Memory().Value()
				}
				// The bar is tunable per org: the message says the one it used
				// (it said "40%" whatever the policy was).
				bar := stateThreshold(state, "resource-underrequest", 0.4)
				if cpuReq > 0 && metrics.CPUUsage > 0 && float64(cpuReq)/float64(metrics.CPUUsage) < bar {
					share := float64(cpuReq) / float64(metrics.CPUUsage)
					over[key+"/cpu"] = true
					pending[key+"/cpu"] = newInsight(
						"info",
						fmt.Sprintf("Pod/%s/%s", pod.Namespace, pod.Name),
						"CPU Request Too Low",
						fmt.Sprintf("Pod %s/%s CPU request (%dm) is less than %.0f%% of actual usage (%dm)", pod.Namespace, pod.Name, cpuReq, bar*100, metrics.CPUUsage),
						"Increase CPU requests to better reflect actual usage for improved scheduling.",
						requestEvidence("CPU", fmt.Sprintf("%dm", cpuReq), fmt.Sprintf("%dm", metrics.CPUUsage), share, bar),
					)
				}
				if memReq > 0 && metrics.MemUsage > 0 && float64(memReq)/float64(metrics.MemUsage) < bar {
					share := float64(memReq) / float64(metrics.MemUsage)
					over[key+"/mem"] = true
					pending[key+"/mem"] = newInsight(
						"info",
						fmt.Sprintf("Pod/%s/%s", pod.Namespace, pod.Name),
						"Memory Request Too Low",
						fmt.Sprintf("Pod %s/%s memory request (%s) is less than %.0f%% of actual usage (%s)", pod.Namespace, pod.Name,
							formatBytes(memReq), bar*100, formatBytes(metrics.MemUsage)),
						"Increase memory requests to better reflect actual usage for improved scheduling.",
						requestEvidence("Memory", formatBytes(memReq), formatBytes(metrics.MemUsage), share, bar),
					)
				}
			}
			// Emit only the dimensions sustained over the window; re-walk pods so
			// output order stays deterministic (cpu before mem, per pod).
			fired := tracker.fired(over)
			var insights []models.Insight
			for _, pod := range state.Pods {
				key := fmt.Sprintf("%s/%s", pod.Namespace, pod.Name)
				if fired[key+"/cpu"] {
					insights = append(insights, pending[key+"/cpu"])
				}
				if fired[key+"/mem"] {
					insights = append(insights, pending[key+"/mem"])
				}
			}
			return insights
		},
	}
}

// 6. Deployment with 0 available replicas
func zeroReplicasRule() Rule {
	return Rule{
		ID:       "zero-replicas",
		Name:     "Zero Available Replicas",
		Severity: "critical",
		Evaluate: func(state *ClusterState) []models.Insight {
			var insights []models.Insight
			for _, d := range state.Deployments {
				if d.Spec.Replicas != nil && *d.Spec.Replicas > 0 && d.Status.AvailableReplicas == 0 {
					insights = append(insights, newInsight(
						"critical",
						fmt.Sprintf("Deployment/%s/%s", d.Namespace, d.Name),
						"Zero Available Replicas",
						fmt.Sprintf("Deployment %s/%s has 0 available replicas (desired: %d)", d.Namespace, d.Name, *d.Spec.Replicas),
						"Check pod events and logs. The deployment may have failing containers or scheduling issues.",
						zeroReplicasEvidence(d)...,
					))
				}
			}
			return insights
		},
	}
}

// progressDeadlineExceededRule — a Deployment stopped making progress. The
// Deployment controller sets the Progressing condition to False with
// reason=ProgressDeadlineExceeded when pods don't become available within
// spec.progressDeadlineSeconds (default 600s). Most often a rollout whose new
// pods fail, where a rollback is the remedy (Autopilot auto-triggers on it,
// registry: progress-deadline-exceeded). But the same condition stalls a
// scale-up that cannot create or place its pods (a ResourceQuota, no
// capacity), with no new revision, and a first revision has nothing to roll
// back to — so the suggestion says which of these the Deployment shows.
func progressDeadlineExceededRule() Rule {
	return Rule{
		ID:       "progress-deadline-exceeded",
		Name:     "Deployment Progress Deadline Exceeded",
		Severity: "critical",
		Evaluate: func(state *ClusterState) []models.Insight {
			var insights []models.Insight
			for _, d := range state.Deployments {
				for _, cond := range d.Status.Conditions {
					if cond.Type == appsv1.DeploymentProgressing &&
						cond.Status == corev1.ConditionFalse &&
						cond.Reason == "ProgressDeadlineExceeded" {
						rev := d.Annotations["deployment.kubernetes.io/revision"]
						at := cond.LastUpdateTime.Time
						insights = append(insights, newInsight(
							"critical",
							fmt.Sprintf("Deployment/%s/%s", d.Namespace, d.Name),
							"Deployment Progress Deadline Exceeded",
							fmt.Sprintf("Deployment %s/%s stopped making progress: %s", d.Namespace, d.Name, cond.Message),
							progressDeadlineSuggestion(d, rev),
							models.Evidence{
								Kind: models.EvidenceEvent, Label: "Progressing", Detail: "False, ProgressDeadlineExceeded: " + cond.Message,
								Source: "deployment.status.conditions[Progressing]", At: &at,
							},
							progressDeadlineEvidence(d, rev),
							models.Evidence{
								Kind: models.EvidenceConfig, Label: "Replicas",
								Detail: fmt.Sprintf("%d desired, %d on the current revision %s, %d in total, %d available",
									replicasOrOne(d.Spec.Replicas), d.Status.UpdatedReplicas, rev, d.Status.Replicas, d.Status.AvailableReplicas),
								Source: "deployment.status",
							},
						))
						break // one insight per Deployment, not per condition
					}
				}
			}
			return insights
		},
	}
}

// progressDeadlineSuggestion says what the Deployment shows: pods of an
// older revision still running mean a rollout stalled (roll back); a first
// revision has nothing to roll back to; every pod on the current revision may
// be a scale-up that cannot create or place its pods, which a rollback does
// not fix.
func progressDeadlineSuggestion(d *appsv1.Deployment, rev string) string {
	const read = "Read the events of its newest ReplicaSet and pods: FailedCreate (a ResourceQuota or LimitRange), FailedScheduling (capacity, affinity, taints), container config errors (a missing ConfigMap, runAsNonRoot), image pulls, crashes, readiness."
	switch {
	case rev == "" || rev == "1":
		return "This is the Deployment's first revision, so there is nothing to roll back to. " + read
	case d.Status.UpdatedReplicas < d.Status.Replicas:
		return fmt.Sprintf("Pods of an older revision are still running, so the rollout of revision %s is what stalled: its new pods are not becoming available. Read their events and logs; rolling back to the previous revision restores the old pods.", rev)
	}
	return fmt.Sprintf("Every pod is on the current revision (%s), so this may be a scale-up that cannot finish rather than a bad rollout — and a rollback does not help with a quota or missing capacity. %s If the revision changed recently and its pods fail, roll back.", rev, read)
}

// 7. PVC in Pending state
func pvcPendingRule() Rule {
	return Rule{
		ID:       "pvc-pending",
		Name:     "PVC Pending",
		Severity: "warning",
		Evaluate: func(state *ClusterState) []models.Insight {
			var insights []models.Insight
			for _, pvc := range state.PVCs {
				if pvc.Status.Phase != corev1.ClaimPending {
					continue
				}
				message, suggestion, ev, fire := pendingClaimText(state, pvc)
				if !fire {
					continue
				}
				insights = append(insights, newInsight(
					"warning",
					fmt.Sprintf("PVC/%s/%s", pvc.Namespace, pvc.Name),
					"PVC Pending",
					message, suggestion, ev...,
				))
			}
			return insights
		},
	}
}

// pendingClaimText says why a claim is Pending, from its StorageClass and the
// pods that mount it. A claim whose class binds on first consumer
// (WaitForFirstConsumer) and that no pod uses yet is Pending by design and
// does not fire: kind's local-path and most cloud classes work that way. One
// whose pod is not scheduled is waiting for the pod, not for storage.
func pendingClaimText(state *ClusterState, pvc *corev1.PersistentVolumeClaim) (message, suggestion string, ev []models.Evidence, fire bool) {
	id := fmt.Sprintf("PersistentVolumeClaim %s/%s", pvc.Namespace, pvc.Name)
	sc, className, noClass := storageClassFor(pvc, state.StorageClasses)
	users := podsUsingClaim(state.Pods, pvc)
	var scheduled bool
	for _, p := range users {
		if p.Spec.NodeName != "" {
			scheduled = true
		}
	}
	ev = claimEvidence(pvc, sc, className, noClass, users)
	if sc != nil && sc.VolumeBindingMode != nil && *sc.VolumeBindingMode == storagev1.VolumeBindingWaitForFirstConsumer && !scheduled {
		if len(users) == 0 {
			return "", "", nil, false
		}
		return fmt.Sprintf("%s waits for its pod to be scheduled: StorageClass %s binds on first consumer, and pod %s is %s, not on a node yet.",
				id, sc.Name, users[0].Name, users[0].Status.Phase),
			fmt.Sprintf("The claim binds once its pod lands on a node. Read why pod %s is not scheduled (FailedScheduling: capacity, affinity, taints, or the class's allowed topologies).", users[0].Name),
			ev, true
	}
	switch {
	case noClass:
		return fmt.Sprintf("%s has no StorageClass (storageClassName \"\"), so it binds only to a pre-created PersistentVolume, and none matches it.", id),
			"Create a PersistentVolume that matches its size, access mode and selector, or give the claim a StorageClass that provisions one.", ev, true
	case sc == nil && className != "":
		return fmt.Sprintf("%s asks for StorageClass %q, which does not exist.", id, className),
			"Create that StorageClass, or point the claim at one that exists (kubectl get storageclass).", ev, true
	case sc == nil:
		return fmt.Sprintf("%s names no StorageClass and the cluster has no default class, so it binds only to a pre-created PersistentVolume, and none matches it.", id),
			"Create a matching PersistentVolume, or set a StorageClass on the claim (or mark one as the cluster default).", ev, true
	}
	message = fmt.Sprintf("%s is Pending: StorageClass %s (provisioner %s) has not provisioned a volume for it.", id, sc.Name, sc.Provisioner)
	suggestion = "Read the claim's events (ProvisioningFailed names the reason) and the provisioner's logs."
	if e := latestWarningFor(state.Events, "PersistentVolumeClaim", pvc.Namespace, pvc.Name); e != nil {
		at := eventLastOccurrence(e)
		message += fmt.Sprintf(" Latest event: %s: %s", e.Reason, e.Message)
		ev = append(ev, models.Evidence{Kind: models.EvidenceEvent, Label: e.Reason, Detail: e.Message, Source: "events", At: &at})
	}
	return message, suggestion, ev, true
}

// latestWarningFor returns the most recent Warning event about one object.
func latestWarningFor(events []*corev1.Event, kind, ns, name string) *corev1.Event {
	var latest *corev1.Event
	for _, e := range events {
		if e == nil || e.Type != corev1.EventTypeWarning || e.InvolvedObject.Kind != kind ||
			e.InvolvedObject.Namespace != ns || e.InvolvedObject.Name != name {
			continue
		}
		if latest == nil || eventLastOccurrence(e).After(eventLastOccurrence(latest)) {
			latest = e
		}
	}
	return latest
}

// 8. Node not ready
func nodeNotReadyRule() Rule {
	return Rule{
		ID:       "node-not-ready",
		Name:     "Node Not Ready",
		Severity: "critical",
		Evaluate: func(state *ClusterState) []models.Insight {
			var insights []models.Insight
			for _, node := range state.Nodes {
				ready := false
				for _, cond := range node.Status.Conditions {
					if cond.Type == corev1.NodeReady {
						if cond.Status == corev1.ConditionTrue {
							ready = true
						}
						break
					}
				}
				if !ready {
					insights = append(insights, newInsight(
						"critical",
						fmt.Sprintf("Node/%s", node.Name),
						"Node Not Ready",
						fmt.Sprintf("Node %s is not in Ready state", node.Name),
						"Check node conditions, kubelet logs, and ensure the node has sufficient resources.",
						nodeNotReadyEvidence(node)...,
					))
				}
			}
			return insights
		},
	}
}

// hpaConditionsAnnotation is where the autoscaling/v1 representation carries
// the v2 status conditions (ScalingLimited, ScalingActive, AbleToScale).
const hpaConditionsAnnotation = "autoscaling.alpha.kubernetes.io/conditions"

// hpaWantsMoreThanMax reports whether the HPA computed more replicas than its
// maximum allows — the condition worth reporting — rather than merely sitting
// at its maximum. A pinned HPA (min == max) or one that can't read its metrics
// sits at max with nothing wrong.
//
// The source is the controller's own verdict: ScalingLimited=True with reason
// TooManyReplicas. When the conditions annotation is absent (an older API
// server), CPU utilization above its target stands in for it.
func hpaWantsMoreThanMax(hpa *autoscalingv1.HorizontalPodAutoscaler) bool {
	if raw := hpa.Annotations[hpaConditionsAnnotation]; raw != "" {
		var conds []struct{ Type, Status, Reason string }
		if err := json.Unmarshal([]byte(raw), &conds); err == nil {
			for _, c := range conds {
				if c.Type == "ScalingLimited" && c.Status == "True" && c.Reason == "TooManyReplicas" {
					return true
				}
			}
			return false
		}
	}
	target, current := hpa.Spec.TargetCPUUtilizationPercentage, hpa.Status.CurrentCPUUtilizationPercentage
	return target != nil && current != nil && *current > *target
}

// 9. HPA maxed out: at its maximum replicas AND wanting more. Being at the
// maximum alone fired for every HPA pinned with min == max, and for one that
// couldn't read its metrics at all — and Autopilot auto-triggers on this rule
// (#64 pattern tests, 2026-10-06: an HPA showing cpu <unknown> fired in 49 s).
func hpaMaxedOutRule() Rule {
	return Rule{
		ID:       "hpa-maxed-out",
		Name:     "HPA Maxed Out",
		Severity: "warning",
		Evaluate: func(state *ClusterState) []models.Insight {
			var insights []models.Insight
			for _, hpa := range state.HPAs {
				// At the maximum is not enough: a fixed size (minReplicas ==
				// maxReplicas) is always there by design, and so is an HPA that
				// cannot read its metrics. hpaWantsMoreThanMax lets through only
				// one whose controller asks for more than it may have — pinned
				// or not, that is worth a reader's attention.
				if hpa.Status.CurrentReplicas < hpa.Spec.MaxReplicas || hpa.Status.CurrentReplicas == 0 || !hpaWantsMoreThanMax(hpa) {
					continue
				}
				message, suggestion := hpaAtMaxText(hpa)
				insights = append(insights, newInsight(
					"warning",
					fmt.Sprintf("HPA/%s/%s", hpa.Namespace, hpa.Name),
					"HPA at Maximum Replicas",
					message, suggestion, hpaEvidence(hpa)...,
				))
			}
			return insights
		},
	}
}

// hpaAtMaxText says what the HPA shows. The autoscaling/v1 view carries only
// the CPU metric: above its target, demand exceeds what maxReplicas allows;
// at or below it, another metric (memory, custom) holds the HPA at the
// maximum or it is cooling down, and raising maxReplicas is not the answer
// it looks like.
func hpaAtMaxText(hpa *autoscalingv1.HorizontalPodAutoscaler) (message, suggestion string) {
	id := fmt.Sprintf("HPA %s/%s is at its maximum (%d/%d replicas)", hpa.Namespace, hpa.Name, hpa.Status.CurrentReplicas, hpa.Spec.MaxReplicas)
	cur, tgt := hpa.Status.CurrentCPUUtilizationPercentage, hpa.Spec.TargetCPUUtilizationPercentage
	switch {
	case cur != nil && tgt != nil && *cur > *tgt:
		return fmt.Sprintf("%s with CPU at %d%% of requests against a %d%% target: demand is above what maxReplicas allows.", id, *cur, *tgt),
			"Raise maxReplicas if the cluster has room for more pods, or reduce the load per pod."
	case cur != nil && tgt != nil:
		return fmt.Sprintf("%s while CPU is at %d%% against a %d%% target: another metric (memory, custom) is holding it at the maximum, or it is waiting out its scale-down window.", id, *cur, *tgt),
			"Read all of its metrics against their targets (kubectl describe hpa) before raising maxReplicas: CPU is not the one asking for more."
	}
	return id + ".",
		"Read its metrics against their targets (kubectl describe hpa): if they are above target, raise maxReplicas or reduce the load per pod; if not, it is cooling down."
}

// 10. Frequent restarts (> 5)
func frequentRestartsRule() Rule {
	return Rule{
		ID:       "frequent-restarts",
		Name:     "Frequent Restarts",
		Severity: "warning",
		Evaluate: func(state *ClusterState) []models.Insight {
			var insights []models.Insight
			for _, pod := range state.Pods {
				for i := range pod.Status.ContainerStatuses {
					cs := &pod.Status.ContainerStatuses[i]
					// RestartCount is monotonic — it never falls, so on its own it can
					// never clear this insight. Contrast crashLoopRule, which pairs the
					// same counter with a LEVEL (Waiting.Reason) and is safe for it.
					// Here the clearing evidence is the current run holding Ready for
					// readyGrace. A container with no recorded termination cannot be
					// churning right now, so it does not fire.
					term := cs.LastTerminationState.Terminated
					threshold := stateThreshold(state, "frequent-restarts", 5)
					if float64(cs.RestartCount) > threshold && term != nil && !term.FinishedAt.IsZero() &&
						!containerRecovered(cs, term.FinishedAt.Time) {
						insights = append(insights, newInsight(
							"warning",
							fmt.Sprintf("Pod/%s/%s", pod.Namespace, pod.Name),
							"Frequent Container Restarts",
							fmt.Sprintf("Container %s in pod %s/%s has restarted %d times", cs.Name, pod.Namespace, pod.Name, cs.RestartCount),
							"Check container logs and health probes. Frequent restarts indicate instability.",
							restartsWithCause(state, pod, cs, threshold)...,
						))
					}
				}
			}
			return insights
		},
	}
}

// 11. ImagePullBackOff
func imagePullBackoffRule() Rule {
	return Rule{
		ID:       "image-pull-backoff",
		Name:     "Image Pull BackOff",
		Severity: "critical",
		Evaluate: func(state *ClusterState) []models.Insight {
			var insights []models.Insight
			for _, pod := range state.Pods {
				for _, cs := range pod.Status.ContainerStatuses {
					if cs.State.Waiting != nil && (cs.State.Waiting.Reason == "ImagePullBackOff" || cs.State.Waiting.Reason == "ErrImagePull") {
						insights = append(insights, newInsight(
							"critical",
							fmt.Sprintf("Pod/%s/%s", pod.Namespace, pod.Name),
							"Image Pull Failed",
							fmt.Sprintf("Container %s in pod %s/%s cannot pull image: %s", cs.Name, pod.Namespace, pod.Name, cs.State.Waiting.Reason),
							"Verify the image name/tag exists, check registry credentials, and ensure network access to the registry.",
							imagePullEvidence(pod, &cs)...,
						))
					}
				}
			}
			return insights
		},
	}
}

// missingConfigDependencyRule flags pods that can't start because a ConfigMap
// or Secret they reference doesn't exist. kubelet reports this on the container
// as `Waiting.Reason == "CreateContainerConfigError"` with a message like
// `configmap "app-config" not found` or `secret "db-creds" not found` — the pod
// is admitted but the container never starts. Unlike a crash-loop (the process
// runs and exits) the container here never executes, so the remedy is always to
// (re)create the missing dependency, not to touch the workload. We read the
// container waiting state (current truth) rather than Events (historical) so the
// insight clears as soon as the dependency exists. Tier-1 (2026-06).
func missingConfigDependencyRule() Rule {
	return Rule{
		ID:       "missing-config-dependency",
		Name:     "Pod missing ConfigMap or Secret",
		Severity: "critical",
		Evaluate: func(state *ClusterState) []models.Insight {
			var insights []models.Insight
			for _, pod := range state.Pods {
				for _, cs := range pod.Status.ContainerStatuses {
					if cs.State.Waiting == nil || cs.State.Waiting.Reason != "CreateContainerConfigError" {
						continue
					}
					msg := cs.State.Waiting.Message
					ce := readConfigError(msg)
					title, message, suggestion := configErrorText(ce, cs.Name, pod, msg)
					ev := []models.Evidence{{
						Kind: models.EvidenceEvent, Label: "Container waiting",
						Detail: "CreateContainerConfigError: " + msg,
						Source: "pod.status.containerStatuses[].state.waiting",
					}}
					if ref, ok := configReferenceEvidence(ce); ok {
						ev = append(ev, ref)
					}
					insights = append(insights, newInsight(
						"critical",
						fmt.Sprintf("Pod/%s/%s", pod.Namespace, pod.Name),
						title, message, suggestion, ev...,
					))
					break // one insight per pod, not per container
				}
			}
			return insights
		},
	}
}

// configErrorText words a CreateContainerConfigError by what it is. Only a
// missing object is fixed by creating one: a missing key is fixed in the
// object that exists, a sync timeout clears on its own, and a runAsNonRoot
// conflict involves no ConfigMap or Secret at all.
func configErrorText(ce configError, container string, pod *corev1.Pod, msg string) (title, message, suggestion string) {
	where := fmt.Sprintf("Container %s in pod %s/%s", container, pod.Namespace, pod.Name)
	switch ce.what {
	case "object":
		return "Pod references a missing " + ce.kind,
			fmt.Sprintf("%s cannot start: the %s %q it references does not exist in namespace %s.", where, ce.kind, ce.name, pod.Namespace),
			fmt.Sprintf("Create %s %q in namespace %s with the keys the application expects, or correct the name in the pod spec. Its values belong to the application: nothing in the cluster records them.", ce.kind, ce.name, pod.Namespace)
	case "key":
		return "Pod references a missing " + ce.kind + " key",
			fmt.Sprintf("%s cannot start: %s %s exists but has no key %q.", where, ce.kind, ce.name, ce.key),
			fmt.Sprintf("Add key %q to %s %s, or correct the key name in the pod spec (or mark the reference optional if the application runs without it).", ce.key, ce.kind, ce.name)
	case "sync":
		what := "configuration"
		if ce.kind != "" {
			what = ce.kind
		}
		return "Container configuration could not be read in time",
			fmt.Sprintf("%s cannot start yet: the kubelet could not read its %s from the API server in time (%s).", where, what, msg),
			"Nothing is missing: the kubelet on this node timed out fetching it. It usually clears on its own; if it persists, check the node's connection to the API server."
	case "nonroot":
		return "Container blocked by runAsNonRoot",
			fmt.Sprintf("%s cannot start: %s", where, msg),
			"Nothing is missing from the cluster: the pod requires a non-root user (runAsNonRoot) and the image runs as root, or names its user by name. Set runAsUser to a numeric non-root UID, or use an image that runs as one."
	}
	return "Container cannot be created: configuration error",
		fmt.Sprintf("%s cannot be created: %s", where, msg),
		"Read the message: it names what the kubelet could not resolve in the pod spec."
}

// runsSettled reports whether every container is running and its current run
// has been up longer than grace — i.e. not starting, crashing or restarting.
// A run with no recorded start time counts as settled (nothing says otherwise).
func runsSettled(pod *corev1.Pod, grace time.Duration) bool {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Running == nil {
			return false // Waiting (crash / pull / config) or Terminated (between restarts)
		}
		if started := cs.State.Running.StartedAt.Time; !started.IsZero() && time.Since(started) < grace {
			return false
		}
	}
	return true
}

// readinessProbeFailingRule flags pods whose container is Running but whose
// Ready condition has been False (reason=ContainersNotReady) for more than the
// startup grace window. This is "running but not serving traffic" — the pod
// process is up, but its readiness probe keeps failing, so the Service keeps it
// out of rotation. The time threshold is what separates a real failure from a
// slow start: a pod that just launched is legitimately not-Ready for a while,
// so we only fire once it's been not-Ready for >2 minutes. Pods with a Waiting
// container are skipped — those are crash-loop / image-pull / config-error,
// other rules' concern. Tier-1 (2026-06).
//
// Two more conditions keep it about the PROBE (#64 pattern tests, 2026-10-06):
//   - a not-ready container must declare a readiness probe, or the pod carry
//     readiness gates (notReadyFromProbe, below). A pod without either is
//     not-Ready only because a container isn't running — there is no probe to
//     check, and the old text told the reader to check it anyway;
//   - every container's current run must have been up past the grace too. A
//     crash-looping container passes through Running and Terminated between
//     restarts, so the Waiting check alone let it through while the pod had been
//     not-Ready for minutes: the rule fired on pods with no readiness probe at
//     all, beside the crash-loop and OOM insights for the same container, and
//     cost Autopilot a second investigation of the same crash.
func readinessProbeFailingRule() Rule {
	const grace = 2 * time.Minute
	return Rule{
		ID:       "readiness-probe-failing",
		Name:     "Pod not passing readiness",
		Severity: "warning",
		Evaluate: func(state *ClusterState) []models.Insight {
			var insights []models.Insight
			for _, pod := range state.Pods {
				if pod.Status.Phase != corev1.PodRunning {
					continue
				}
				// Skip pods with a container that is not running, or whose
				// current run is younger than the grace — a start or a crash
				// owned by another rule, not a probe failing on a running
				// container.
				if !runsSettled(pod, grace) {
					continue
				}
				// Nor a pod whose not-Ready is a start, not a probe: between
				// the deaths of a crash-looping container (OOMKilled, exit 1)
				// it is briefly Running and not Ready, with a Ready condition
				// that has been False for minutes — and this rule said "check
				// the readiness probe" of a container that had none (lab,
				// 2026-10-11). Fire only when a not-ready container has a
				// readiness probe (or the pod has readiness gates) and its
				// current run is older than the grace.
				if !notReadyFromProbe(pod, grace) {
					continue
				}
				for _, cond := range pod.Status.Conditions {
					if cond.Type != corev1.PodReady || cond.Status != corev1.ConditionFalse {
						continue
					}
					// Slow-start guard: only fire once it's been not-Ready
					// past the grace window.
					if cond.LastTransitionTime.IsZero() || time.Since(cond.LastTransitionTime.Time) < grace {
						break
					}
					detail := cond.Reason
					if cond.Message != "" {
						detail = cond.Reason + " — " + cond.Message
					}
					insights = append(insights, newInsight(
						"warning",
						fmt.Sprintf("Pod/%s/%s", pod.Namespace, pod.Name),
						"Pod not passing readiness",
						fmt.Sprintf("Pod %s/%s is Running but has not been Ready for over 2 minutes (%s) — it's up, but any Service in front of it keeps it out of rotation.",
							pod.Namespace, pod.Name, detail),
						"Check the container's readiness probe (path/port/timeout) and its logs. If the probe regressed in a recent deploy, roll back; if the app genuinely isn't ready, fix the dependency it's waiting on.",
						readinessEvidence(pod, &cond)...,
					))
					break // one insight per pod
				}
			}
			return insights
		},
	}
}

// livenessProbeFailingRule flags pods whose liveness probe is actively failing,
// read from kubelet's `Unhealthy` events ("Liveness probe failed: …"). Unlike
// crash-loop (which keys on restart count, the downstream symptom) this keys on
// the probe failure itself — the direct cause — and fires before the restarts
// pile up. We require the event to have recurred (Count > 1) so a single
// transient blip doesn't trip it. Tier-1 (2026-06).
//
// An Event is an EDGE (something that happened), and an insight is a LEVEL
// (something that is true). Converting one into the other is this rule's whole
// difficulty, and getting it wrong is what made the insight outlive its cause by
// up to an hour: it used to stop firing only when the apiserver garbage-
// collected the Event at --event-ttl, so the insight's lifetime was governed by
// Kubernetes' retention policy rather than by the workload's health. Proven in
// vivo 2026-08-02 — the pod was Ready for 31 minutes while the insight stayed
// active, then the insight vanished at the TTL boundary with the pod unchanged.
//
// livenessFailureCleared converts the edge to a level: primarily by evidence
// (the container that was being killed has been replaced and the replacement is
// healthy), and only where no evidence can exist by probe-cadence silence.
func livenessProbeFailingRule() Rule {
	return Rule{
		ID:       "liveness-probe-failing",
		Name:     "Liveness probe failing",
		Severity: "warning",
		Evaluate: func(state *ClusterState) []models.Insight {
			var insights []models.Insight
			// Kubernetes retains Events for ~1h after the object is gone
			// (apiserver --event-ttl). This rule keys on events, not live
			// pod state, so without this guard it keeps firing for a pod
			// that's already been deleted — a phantom insight that the UI
			// shows for up to an hour and that Autopilot re-opens as a new
			// incident every poll tick. Only emit for pods that still exist.
			podByKey := make(map[string]*corev1.Pod, len(state.Pods))
			for _, p := range state.Pods {
				podByKey[p.Namespace+"/"+p.Name] = p
			}
			seen := map[string]bool{} // dedup per pod
			for _, ev := range state.Events {
				if ev == nil || ev.Reason != "Unhealthy" {
					continue
				}
				if !strings.Contains(ev.Message, "Liveness probe failed") {
					continue
				}
				if ev.Count <= 1 {
					continue // a single blip isn't an incident
				}
				io := ev.InvolvedObject
				if io.Kind != "Pod" {
					continue
				}
				key := io.Namespace + "/" + io.Name
				pod := podByKey[key]
				if pod == nil {
					continue // pod is gone; the event is just stale history
				}
				if seen[key] {
					continue
				}
				container := containerFromFieldPath(io.FieldPath)
				if livenessFailureCleared(pod, container, eventLastOccurrence(ev)) {
					continue
				}
				seen[key] = true
				insights = append(insights, newInsight(
					"warning",
					fmt.Sprintf("Pod/%s/%s", io.Namespace, io.Name),
					"Liveness probe failing",
					fmt.Sprintf("Pod %s/%s liveness probe has failed %d times: %s — kubelet will restart the container, risking a restart loop.",
						io.Namespace, io.Name, ev.Count, ev.Message),
					"Check the liveness probe config (path/port/initialDelaySeconds/timeout) against what the app actually does at startup. If the probe regressed in a recent deploy, roll back; if it's too aggressive, relax initialDelaySeconds/failureThreshold.",
					livenessEvidence(pod, container, ev)...,
				))
			}
			return insights
		},
	}
}

// livenessFailureCleared reports whether a liveness failure whose last evidence
// is at `last` has been superseded — i.e. whether we may stop asserting that the
// probe is failing.
//
// Two paths, in strict priority:
//
//  1. EVIDENCE (primary). The container's current run started at/after the last
//     failure, so kubelet already killed and replaced the failing one. We clear
//     once that replacement has held Ready for readyGrace. A live loop kills
//     every failureThreshold × periodSeconds and each kill resets the run clock,
//     so it can never reach the grace — no false negative.
//
//  2. SILENCE (fallback). No restart happened since the failure, which means the
//     probe recovered in place (failures never reached failureThreshold). There
//     is no live level to read here — the absence of new events is the only
//     evidence that exists — so we fall back to a window derived from the
//     probe's own cadence. This is the one legitimate use of a clock in this
//     file, and it is a lost-signal fallback, not a staleness bound.
//
// A container that is Waiting (crash-looping) has no current run, so it takes
// path 2 and clears after a short silence; that is deliberate — crashLoopRule
// owns that state, exactly as readinessProbeFailingRule skips Waiting pods.
//
// An unknown `last` (no usable timestamp on the event at all) does NOT clear:
// Count > 1 already established the failure recurred, and we refuse to invent a
// recovery we cannot date.
func livenessFailureCleared(pod *corev1.Pod, container string, last time.Time) bool {
	if last.IsZero() {
		return false
	}
	if cs := containerStatusFor(pod, container); cs != nil && cs.State.Running != nil {
		if started := cs.State.Running.StartedAt.Time; !started.IsZero() && !started.Before(last) {
			return containerRecovered(cs, last)
		}
	}
	return time.Since(last) > probeSilenceWindow(livenessProbeFor(pod, container))
}

// 13. Service has zero ready endpoints (P25-05).
//
// Fires when a Service that's expected to back a workload (has a
// selector, is not Headless, is not ExternalName) has zero
// EndpointSlice addresses with `ready=true`. The signal answers
// "is this Service actually serving?" — a common failure mode is
// a Service whose selector drifted away from its pods (mismatched
// labels after a rename, deleted Deployment leaving the Service
// behind), and traffic to ClusterIP black-holes silently. This
// rule surfaces it before users hit a 503.
//
// Skip rules:
//   - ExternalName services have no endpoints by design.
//   - Headless services (clusterIP == "None") are DNS-only and
//     legitimately can have zero ready endpoints during scale-to-
//     zero of operator workloads (think StatefulSet with replicas=0).
//   - Services without a selector are manually managed — the operator
//     wires Endpoints by hand, and that's intentional.
//
// EndpointSlice <-> Service binding uses the canonical
// `kubernetes.io/service-name` label, which the EndpointSlice
// controller writes for every slice it produces.
func serviceNoEndpointsRule() Rule {
	return Rule{
		ID:       "service-no-endpoints",
		Name:     "Service Has Zero Ready Endpoints",
		Severity: "warning",
		Evaluate: func(state *ClusterState) []models.Insight {
			var insights []models.Insight

			// Index slices by (namespace, service-name) for O(1) lookup
			// per service. A single Service can have multiple slices
			// (sharded by IP family or address count), so the value is
			// a list.
			slicesByService := map[string][]int{}
			for i, slice := range state.EndpointSlices {
				name := slice.Labels["kubernetes.io/service-name"]
				if name == "" {
					continue
				}
				key := slice.Namespace + "/" + name
				slicesByService[key] = append(slicesByService[key], i)
			}

			for _, svc := range state.Services {
				if svc.Spec.Type == corev1.ServiceTypeExternalName {
					continue
				}
				if svc.Spec.ClusterIP == corev1.ClusterIPNone {
					continue
				}
				if len(svc.Spec.Selector) == 0 {
					continue
				}

				readyCount := 0
				totalCount := 0
				key := svc.Namespace + "/" + svc.Name
				for _, idx := range slicesByService[key] {
					slice := state.EndpointSlices[idx]
					for _, ep := range slice.Endpoints {
						totalCount++
						if ep.Conditions.Ready != nil && *ep.Conditions.Ready {
							readyCount++
						}
					}
				}

				if readyCount > 0 {
					continue
				}

				suggestion := "Verify the Service selector matches pod labels and that pods are running and ready. " +
					"Try: kubectl get endpoints " + svc.Name + " -n " + svc.Namespace
				message := fmt.Sprintf("Service %s/%s has no ready endpoints", svc.Namespace, svc.Name)
				if totalCount > 0 {
					// Distinguishes "selector matches pods, but they're not ready"
					// (workload is unhealthy) from "selector matches nothing"
					// (configuration error). Different operator response.
					message = fmt.Sprintf(
						"Service %s/%s has %d endpoint(s) but none are ready",
						svc.Namespace, svc.Name, totalCount,
					)
					suggestion = "Pods backing this Service exist but aren't passing readiness. " +
						"Check pod readiness probes and container logs."
				}

				insights = append(insights, newInsight(
					"warning",
					fmt.Sprintf("Service/%s/%s", svc.Namespace, svc.Name),
					"Service Has Zero Ready Endpoints",
					message,
					suggestion,
					serviceEndpointEvidence(svc, totalCount)...,
				))
			}
			return insights
		},
	}
}

// serviceEndpointEvidence types what the rule saw. Every fact is already in
// scope when the rule fires — this adds no lookup, only stops discarding.
//
// It answers a question the prose cannot. On one production cluster 362 of 383
// active insights are this rule, 352 of them on the "selector matches nothing"
// branch, all named <something>-job-<hash>-ui-svc: batch-driver UI Services
// whose driver finished and whose Service nobody collected. They are not 352
// broken Services, they are one uncollected leak — and the only way to tell
// the two apart is the owner, the selector and the age, none of which survive
// fmt.Sprintf.
//
// The owner is the decisive one and its ABSENCE is the finding: Kubernetes
// garbage-collects a dependent whose owner is gone, so a Service that outlives
// its workload by a month almost certainly never had an ownerReference at all.
// "none" is therefore recorded explicitly, not omitted.
func serviceEndpointEvidence(svc *corev1.Service, readyTotal int) []models.Evidence {
	created := svc.CreationTimestamp.Time
	ev := []models.Evidence{
		{
			Kind: models.EvidenceConfig, Label: "Owner",
			Detail: ownerRefSummary(svc.OwnerReferences),
			Source: "metadata.ownerReferences",
		},
		{
			Kind: models.EvidenceConfig, Label: "Selector",
			Detail: labelSelectorSummary(svc.Spec.Selector),
			Source: "spec.selector",
		},
		{
			Kind: models.EvidenceConfig, Label: "Endpoints",
			Detail: endpointSummary(readyTotal),
			Source: "discovery.k8s.io/v1 EndpointSlice",
		},
	}
	// The Service's birth is a real moment, so it carries a clock — and it is
	// the age dimension the rule otherwise lacks: no endpoints for five
	// minutes during a rollout and no endpoints for a month are the same
	// finding today.
	if !created.IsZero() {
		// Detail is the AGE, not the timestamp — At already carries the exact
		// moment, and repeating it twice says nothing twice. The age is the
		// fact that decides: no endpoints for five minutes during a rollout
		// and no endpoints for a month are the same finding today. It is the
		// age AT THE MOMENT THE EPISODE OPENED, since that is when evidence
		// is written, which is more useful than a number that moves.
		ev = append(ev, models.Evidence{
			Kind: models.EvidenceEvent, Label: "Service created",
			Detail: humanAge(time.Since(created)) + " before this was flagged",
			Source: "metadata.creationTimestamp", At: &created,
		})
	}
	if s := workloadLabelSummary(svc.Labels); s != "" {
		// Operator-set labels outlive the Job and the driver pod, so they are
		// the one durable clue to what created this Service.
		ev = append(ev, models.Evidence{
			Kind: models.EvidenceConfig, Label: "Service labels",
			Detail: s, Source: "metadata.labels",
		})
	}
	return ev
}

// humanAge renders a duration the way an operator says it. Coarse on purpose:
// the difference that matters is minutes vs days, not seconds.
func humanAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "moments"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// ownerRefSummary — "none" when empty, because an absent owner is the signal.
func ownerRefSummary(refs []metav1.OwnerReference) string {
	if len(refs) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(refs))
	for _, r := range refs {
		parts = append(parts, r.Kind+"/"+r.Name)
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

// labelSelectorSummary renders a selector deterministically. Map iteration
// order is random in Go, and evidence that changes text between evaluations of
// an unchanged Service is noise that looks like a change.
func labelSelectorSummary(sel map[string]string) string {
	return joinSortedPairs(sel, evidenceMaxPairs)
}

// workloadLabelSummary keeps only the labels that identify what made the
// Service, and drops Kubernetes' own bookkeeping — which is on everything and
// therefore distinguishes nothing.
func workloadLabelSummary(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keep := make(map[string]string, len(labels))
	for k, v := range labels {
		if strings.HasPrefix(k, "app.kubernetes.io/managed-by") ||
			strings.HasPrefix(k, "kubernetes.io/") ||
			strings.HasPrefix(k, "helm.sh/") {
			continue
		}
		keep[k] = v
	}
	return joinSortedPairs(keep, evidenceMaxPairs)
}

// evidenceMaxPairs bounds a rendered map. A Service with forty labels would
// otherwise put a paragraph in a field meant to be read at a glance, and the
// episode row carries this for every occurrence.
const evidenceMaxPairs = 6

func joinSortedPairs(m map[string]string, max int) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	extra := 0
	if len(keys) > max {
		extra = len(keys) - max
		keys = keys[:max]
	}
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	out := strings.Join(parts, ", ")
	if extra > 0 {
		out += fmt.Sprintf(" (+%d more)", extra)
	}
	return out
}

// endpointSummary distinguishes the two branches the rule already splits on,
// in a form something other than a human can read.
func endpointSummary(total int) string {
	if total == 0 {
		return "0 — the selector matches nothing"
	}
	return fmt.Sprintf("%d, none ready", total)
}

// 12. Evicted pods
func evictedPodsRule() Rule {
	return Rule{
		ID:       "evicted-pods",
		Name:     "Evicted Pods",
		Severity: "warning",
		Evaluate: func(state *ClusterState) []models.Insight {
			var insights []models.Insight
			for _, pod := range state.Pods {
				// Unlike the OOM / restart / probe rules, an evicted pod IS current
				// state — the Failed/Evicted object exists until GC, so the condition
				// stays legitimately true and there is nothing to "supersede". The
				// window is a deliberate product choice, not a staleness bound: a feed
				// of recent evictions is actionable, a wall of week-old carcasses is
				// not. It is named in the title so the operator can see the clock
				// rather than wonder where the insight went.
				if pod.Status.Phase == corev1.PodFailed && pod.Status.Reason == "Evicted" && podFailedRecently(pod) {
					title, suggestion := evictedTitle,
						"Read the eviction message: the kubelet evicts pods when the node runs low on a resource, or when a pod goes over its own local storage limit."
					switch {
					case evictionOverOwnLimit(pod.Status.Message):
						title = evictedOwnLimitTitle
						suggestion = "The pod went over its own local storage limit (ephemeral-storage, or an emptyDir sizeLimit); the node was not short of anything. Raise that limit, or write large files to a volume sized for them. Restarting changes nothing."
					case evictionByNodePressure(pod.Status.Message):
						suggestion = "The node ran low on a resource and the kubelet evicted pods to reclaim it. Check the node's pressure conditions and what is using that resource."
					}
					at := podFailedAt(pod)
					insights = append(insights, newInsight(
						"warning",
						fmt.Sprintf("Pod/%s/%s", pod.Namespace, pod.Name),
						title,
						fmt.Sprintf("Pod %s/%s was evicted in the last %s: %s",
							pod.Namespace, pod.Name, recentEvictionWindow, pod.Status.Message),
						suggestion,
						models.Evidence{
							Kind: models.EvidenceEvent, Label: "Eviction", Detail: pod.Status.Message,
							Source: "pod.status.message", At: &at,
						},
					))
				}
			}
			return insights
		},
	}
}

// networkPolicyNoMatchRule flags NetworkPolicies whose podSelector
// matches zero pods in their namespace. The canonical case for this
// is a typo in matchLabels (the policy was supposed to gate the
// `tier=db` pods but actually says `tier=database`), but it also
// catches policies whose target workload was renamed or deleted
// without cleaning up the matching policy.
//
// Empty selector ({}) is NOT a no-match — per NetworkPolicy v1
// spec it matches EVERY pod in the namespace. We skip those
// explicitly so the rule doesn't false-positive on catch-all
// policies (which are intentional in many security postures).
func networkPolicyNoMatchRule() Rule {
	return Rule{
		ID:       "policy-no-match",
		Name:     "NetworkPolicy podSelector matches no pods",
		Severity: "warning",
		Evaluate: func(state *ClusterState) []models.Insight {
			var insights []models.Insight
			// Index pods by namespace once — N policies × M pods
			// without the index would be O(N×M) per evaluation tick.
			podsByNS := map[string][]*corev1.Pod{}
			for _, p := range state.Pods {
				podsByNS[p.Namespace] = append(podsByNS[p.Namespace], p)
			}
			for _, np := range state.NetworkPolicies {
				// Empty selector = catch-all (matches everything).
				// Intentional in deny-all defaults — don't flag.
				if len(np.Spec.PodSelector.MatchLabels) == 0 && len(np.Spec.PodSelector.MatchExpressions) == 0 {
					continue
				}
				sel, err := metav1.LabelSelectorAsSelector(&np.Spec.PodSelector)
				if err != nil {
					// Invalid selector — different signal than no-match;
					// the policy itself is malformed. Skip here, the
					// apiserver should have rejected it on create anyway.
					continue
				}
				matched := 0
				for _, p := range podsByNS[np.Namespace] {
					if sel.Matches(labels.Set(p.Labels)) {
						matched++
					}
				}
				if matched > 0 {
					continue
				}
				// Same as pdb-no-match: a workload scaled to 0 or a CronJob
				// between runs leaves the selector right and the pods absent.
				if len(workloadsMatching(state, np.Namespace, sel)) > 0 {
					continue
				}
				insights = append(insights, newInsight(
					"warning",
					fmt.Sprintf("NetworkPolicy/%s/%s", np.Namespace, np.Name),
					"NetworkPolicy podSelector matches no pods",
					fmt.Sprintf(
						"NetworkPolicy %s/%s matches no pod in its namespace, and no Deployment, StatefulSet, "+
							"DaemonSet, Job or CronJob there has pods with these labels. The selector has a typo, "+
							"or the workload it governed was renamed or deleted (or is run by a controller "+
							"KubeBolt does not read).",
						np.Namespace, np.Name,
					),
					"Verify the podSelector matchLabels against the pods you expect this policy to gate. "+
						"kubectl get pods -n "+np.Namespace+" --show-labels",
					networkPolicyEvidence(np)...,
				))
			}
			return insights
		},
	}
}

// namespaceWithoutNetworkPolicyRule flags namespaces that have
// running pods but zero NetworkPolicies attached. Informational
// severity (not warning) — many clusters legitimately run without
// NetworkPolicies (single-tenant trust boundary), so this is a
// nudge for operators who expected policy coverage rather than a
// "fix this now" alert.
//
// System namespaces (kube-*, gke-*, etc.) are skipped — they're
// often managed by the platform and the operator has no policy
// authority over them.
func namespaceWithoutNetworkPolicyRule() Rule {
	return Rule{
		ID:       "policy-orphan",
		Name:     "Namespace has running pods but no NetworkPolicy",
		Severity: "info",
		Evaluate: func(state *ClusterState) []models.Insight {
			var insights []models.Insight
			// Index policies by namespace once.
			policiesByNS := map[string]int{}
			for _, np := range state.NetworkPolicies {
				policiesByNS[np.Namespace]++
			}
			// Group pods by namespace to surface coverage gaps
			// where workloads exist. We only flag namespaces that
			// have at least one pod — empty namespaces aren't
			// actionable from a security posture standpoint.
			runningPodsByNS := map[string]int{}
			for _, p := range state.Pods {
				if isSystemNamespace(p.Namespace) {
					continue
				}
				if p.Status.Phase == corev1.PodRunning {
					runningPodsByNS[p.Namespace]++
				}
			}
			for ns, podCount := range runningPodsByNS {
				// A CiliumNetworkPolicy governs the namespace as much as a
				// NetworkPolicy does: on a Cilium cluster with only Cilium
				// policies, "all traffic is allowed" was false.
				if policiesByNS[ns] > 0 || state.CiliumPolicies[ns] > 0 {
					continue
				}
				insights = append(insights, newInsight(
					"info",
					fmt.Sprintf("Namespace/%s/%s", ns, ns),
					"Namespace has running pods but no NetworkPolicy",
					orphanNamespaceMessage(state, ns, podCount),
					"If this namespace should be isolated, start with a default-deny ingress "+
						"policy: https://kubernetes.io/docs/concepts/services-networking/network-policies/#default-deny-all-ingress-traffic",
					orphanNamespaceEvidence(state, ns, podCount)...,
				))
			}
			return insights
		},
	}
}

// orphanNamespaceMessage claims only what was read: no NetworkPolicy, and no
// CiliumNetworkPolicy when Cilium's policies could be listed. Cluster-wide
// Cilium policies may still select these pods, and a CNI's own policies
// that KubeBolt does not read (Calico's, for one) may apply — so "all
// traffic is allowed" is said with its condition.
func orphanNamespaceMessage(state *ClusterState, ns string, pods int) string {
	none := "no NetworkPolicy"
	if state.CiliumPoliciesKnown {
		none = "no NetworkPolicy or CiliumNetworkPolicy"
	}
	msg := fmt.Sprintf("Namespace %q has %d running pod(s) and %s. ", ns, pods, none)
	if state.CiliumPoliciesKnown && state.CiliumClusterwidePolicies > 0 {
		msg += fmt.Sprintf("%d CiliumClusterwideNetworkPolicy(ies) exist and may select these pods; if none does, ",
			state.CiliumClusterwidePolicies)
	} else {
		msg += "Unless the CNI enforces policies of its own here, "
	}
	return msg + "all pod-to-pod and pod-to-external traffic in it is allowed by default — " +
		"review whether that matches your intended security posture."
}

// isSystemNamespace returns true for namespaces operators don't
// typically have policy authority over — built-in Kubernetes
// system namespaces and the common managed-platform prefixes.
// Skipping them prevents the policy-orphan rule from generating
// false-positive nudges the operator can't action.
func isSystemNamespace(ns string) bool {
	switch {
	case ns == "kubebolt", ns == "kubebolt-system", ns == "kubebolt-agent":
		return true
	}
	prefixes := []string{
		"kube-",   // kube-system, kube-public, kube-node-lease, kube-prom-stack, etc.
		"gke-",    // gke-managed-*, gke-system-*
		"gmp-",    // GCP Managed Prometheus
		"aks-",    // AKS-managed
		"eks-",    // EKS-managed
		"cilium-", // Cilium control plane (when split out)
		"istio-",  // istio-system if treated as platform
	}
	for _, p := range prefixes {
		if strings.HasPrefix(ns, p) {
			return true
		}
	}
	return false
}
