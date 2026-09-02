package insights

import (
	"fmt"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
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
					))
				case float64(days) < stateThreshold(state, "cert-expiring", 14):
					insights = append(insights, newInsight(
						"warning",
						fmt.Sprintf("Certificate/%s/%s", ns, name),
						"Certificate expiring soon",
						fmt.Sprintf("cert-manager Certificate %s/%s expires in %d day(s). If auto-renewal is healthy this clears itself; if it persists, renewal is stuck.", ns, name, days),
						"Verify cert-manager is renewing it: kubectl describe certificate "+name+" -n "+ns,
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
				insights = append(insights, newInsight(
					"warning",
					fmt.Sprintf("PodDisruptionBudget/%s/%s", pdb.Namespace, pdb.Name),
					"PodDisruptionBudget selector matches no pods",
					fmt.Sprintf(
						"PodDisruptionBudget %s/%s declares a selector but no pod in its namespace matches. "+
							"The budget protects nothing — a drain or Evict won't be gated as intended, "+
							"either because the selector has a typo or the target workload was renamed / deleted.",
						pdb.Namespace, pdb.Name,
					),
					"Verify the PDB's spec.selector.matchLabels against the pods you expect it to protect. "+
						"kubectl get pods -n "+pdb.Namespace+" --show-labels",
				))
			}
			return insights
		},
	}
}

func newInsight(severity, resource, title, message, suggestion string) models.Insight {
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
		FirstSeen:  now,
		LastSeen:   now,
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
					if cs.State.Waiting != nil && cs.State.Waiting.Reason == "CrashLoopBackOff" && float64(cs.RestartCount) > stateThreshold(state, "crash-loop", 3) {
						insights = append(insights, newInsight(
							"critical",
							fmt.Sprintf("Pod/%s/%s", pod.Namespace, pod.Name),
							"Pod in CrashLoopBackOff",
							fmt.Sprintf("Container %s in pod %s/%s is crash-looping with %d restarts", cs.Name, pod.Namespace, pod.Name, cs.RestartCount),
							"Check container logs with 'kubectl logs' and review the container's command/args and liveness probes.",
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

// podFailedRecently approximates when a Failed/Evicted pod entered that state
// (most recent status-condition transition, else creation time) and reports
// whether that was within recentEvictionWindow.
func podFailedRecently(pod *corev1.Pod) bool {
	latest := pod.CreationTimestamp.Time
	for _, c := range pod.Status.Conditions {
		if c.LastTransitionTime.Time.After(latest) {
			latest = c.LastTransitionTime.Time
		}
	}
	return !latest.IsZero() && time.Since(latest) < recentEvictionWindow
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
						insights = append(insights, newInsight(
							"critical",
							fmt.Sprintf("Pod/%s/%s", pod.Namespace, pod.Name),
							"Container OOMKilled",
							fmt.Sprintf("Container %s in pod %s/%s was terminated due to OOM (exit code 137)", cs.Name, pod.Namespace, pod.Name),
							"Increase memory limits for this container or investigate memory leaks in the application.",
						))
					}
				}
			}
			return insights
		},
	}
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
				var cpuLimit int64
				for _, c := range pod.Spec.Containers {
					cpuLimit += c.Resources.Limits.Cpu().MilliValue()
				}
				if cpuLimit > 0 && float64(metrics.CPUUsage)/float64(cpuLimit) > stateThreshold(state, "cpu-throttle-risk", 0.8) {
					over[key] = true
					pending[key] = newInsight(
						"warning",
						fmt.Sprintf("Pod/%s/%s", pod.Namespace, pod.Name),
						"CPU Throttle Risk",
						fmt.Sprintf("Pod %s/%s is using %.0f%% of CPU limit (%dm/%dm)", pod.Namespace, pod.Name, float64(metrics.CPUUsage)/float64(cpuLimit)*100, metrics.CPUUsage, cpuLimit),
						"Consider increasing CPU limits or optimizing CPU-intensive operations.",
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

// 4. Memory pressure (usage > 85% of limit, sustained)
func memoryPressureRule() Rule {
	tracker := newSustainedOver()
	return Rule{
		ID:       "memory-pressure",
		Name:     "Memory Pressure",
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
				var memLimit int64
				for _, c := range pod.Spec.Containers {
					memLimit += c.Resources.Limits.Memory().Value()
				}
				if memLimit > 0 && float64(metrics.MemUsage)/float64(memLimit) > stateThreshold(state, "memory-pressure", 0.85) {
					pct := float64(metrics.MemUsage) / float64(memLimit) * 100
					over[key] = true
					pending[key] = newInsight(
						"warning",
						fmt.Sprintf("Pod/%s/%s", pod.Namespace, pod.Name),
						"Memory Pressure",
						fmt.Sprintf("Pod %s/%s is using %.0f%% of memory limit", pod.Namespace, pod.Name, pct),
						"Increase memory limits or investigate memory usage patterns to avoid OOMKill.",
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
				if cpuReq > 0 && metrics.CPUUsage > 0 && float64(cpuReq)/float64(metrics.CPUUsage) < stateThreshold(state, "resource-underrequest", 0.4) {
					over[key+"/cpu"] = true
					pending[key+"/cpu"] = newInsight(
						"info",
						fmt.Sprintf("Pod/%s/%s", pod.Namespace, pod.Name),
						"CPU Request Too Low",
						fmt.Sprintf("Pod %s/%s CPU request (%dm) is less than 40%% of actual usage (%dm)", pod.Namespace, pod.Name, cpuReq, metrics.CPUUsage),
						"Increase CPU requests to better reflect actual usage for improved scheduling.",
					)
				}
				if memReq > 0 && metrics.MemUsage > 0 && float64(memReq)/float64(metrics.MemUsage) < stateThreshold(state, "resource-underrequest", 0.4) {
					over[key+"/mem"] = true
					pending[key+"/mem"] = newInsight(
						"info",
						fmt.Sprintf("Pod/%s/%s", pod.Namespace, pod.Name),
						"Memory Request Too Low",
						fmt.Sprintf("Pod %s/%s memory request is less than 40%% of actual usage", pod.Namespace, pod.Name),
						"Increase memory requests to better reflect actual usage for improved scheduling.",
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
					))
				}
			}
			return insights
		},
	}
}

// progressDeadlineExceededRule — a Deployment's rollout stalled. The
// Deployment controller sets the Progressing condition to False with
// reason=ProgressDeadlineExceeded when new pods don't become available
// within spec.progressDeadlineSeconds (default 600s). This is one of the
// most common "my deploy is stuck" incidents, and the default remedy is
// almost always a rollback to the last working revision — which is why
// Autopilot auto-triggers on it (registry: progress-deadline-exceeded).
func progressDeadlineExceededRule() Rule {
	return Rule{
		ID:       "progress-deadline-exceeded",
		Name:     "Rollout Progress Deadline Exceeded",
		Severity: "critical",
		Evaluate: func(state *ClusterState) []models.Insight {
			var insights []models.Insight
			for _, d := range state.Deployments {
				for _, cond := range d.Status.Conditions {
					if cond.Type == appsv1.DeploymentProgressing &&
						cond.Status == corev1.ConditionFalse &&
						cond.Reason == "ProgressDeadlineExceeded" {
						insights = append(insights, newInsight(
							"critical",
							fmt.Sprintf("Deployment/%s/%s", d.Namespace, d.Name),
							"Rollout Progress Deadline Exceeded",
							fmt.Sprintf("Deployment %s/%s rollout has not progressed: %s", d.Namespace, d.Name, cond.Message),
							"The new ReplicaSet failed to become available in time. Roll back to the last working revision, or check the new pods' events and logs for the failure cause.",
						))
						break // one insight per Deployment, not per condition
					}
				}
			}
			return insights
		},
	}
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
				if pvc.Status.Phase == corev1.ClaimPending {
					insights = append(insights, newInsight(
						"warning",
						fmt.Sprintf("PVC/%s/%s", pvc.Namespace, pvc.Name),
						"PVC Pending",
						fmt.Sprintf("PersistentVolumeClaim %s/%s is in Pending state", pvc.Namespace, pvc.Name),
						"Check if a suitable PersistentVolume exists or if the StorageClass can dynamically provision one.",
					))
				}
			}
			return insights
		},
	}
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
					))
				}
			}
			return insights
		},
	}
}

// 9. HPA maxed out (current == max replicas)
func hpaMaxedOutRule() Rule {
	return Rule{
		ID:       "hpa-maxed-out",
		Name:     "HPA Maxed Out",
		Severity: "warning",
		Evaluate: func(state *ClusterState) []models.Insight {
			var insights []models.Insight
			for _, hpa := range state.HPAs {
				if hpa.Status.CurrentReplicas >= hpa.Spec.MaxReplicas && hpa.Status.CurrentReplicas > 0 {
					insights = append(insights, newInsight(
						"warning",
						fmt.Sprintf("HPA/%s/%s", hpa.Namespace, hpa.Name),
						"HPA at Maximum Replicas",
						fmt.Sprintf("HPA %s/%s is at maximum replicas (%d/%d)", hpa.Namespace, hpa.Name, hpa.Status.CurrentReplicas, hpa.Spec.MaxReplicas),
						"Consider increasing maxReplicas or optimizing the target workload to reduce resource demand.",
					))
				}
			}
			return insights
		},
	}
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
					if float64(cs.RestartCount) > stateThreshold(state, "frequent-restarts", 5) && term != nil && !term.FinishedAt.IsZero() &&
						!containerRecovered(cs, term.FinishedAt.Time) {
						insights = append(insights, newInsight(
							"warning",
							fmt.Sprintf("Pod/%s/%s", pod.Namespace, pod.Name),
							"Frequent Container Restarts",
							fmt.Sprintf("Container %s in pod %s/%s has restarted %d times", cs.Name, pod.Namespace, pod.Name, cs.RestartCount),
							"Check container logs and health probes. Frequent restarts indicate instability.",
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
					// Identify which kind of dependency is missing, for a
					// sharper title + suggestion. kubelet phrases it as
					// `configmap "X" not found` / `secret "X" not found`.
					kind := "ConfigMap or Secret"
					if strings.Contains(msg, "configmap") {
						kind = "ConfigMap"
					} else if strings.Contains(msg, "secret") {
						kind = "Secret"
					}
					insights = append(insights, newInsight(
						"critical",
						fmt.Sprintf("Pod/%s/%s", pod.Namespace, pod.Name),
						"Pod missing ConfigMap or Secret",
						fmt.Sprintf("Container %s in pod %s/%s cannot start — a referenced %s is missing: %s",
							cs.Name, pod.Namespace, pod.Name, kind, msg),
						fmt.Sprintf("Create the missing %s in namespace %s (the exact name is in the message above), or remove the reference from the pod spec. kubectl describe pod %s -n %s shows the full error.",
							kind, pod.Namespace, pod.Name, pod.Namespace),
					))
					break // one insight per pod, not per container
				}
			}
			return insights
		},
	}
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
				// Skip pods with any container still Waiting — that's a
				// startup/crash problem owned by another rule, not a probe
				// failing on a running container.
				stillWaiting := false
				for _, cs := range pod.Status.ContainerStatuses {
					if cs.State.Waiting != nil {
						stillWaiting = true
						break
					}
				}
				if stillWaiting {
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
						fmt.Sprintf("Pod %s/%s is Running but has not been Ready for over 2 minutes (%s) — it's up but the Service is keeping it out of rotation.",
							pod.Namespace, pod.Name, detail),
						"Check the container's readiness probe (path/port/timeout) and its logs. If the probe regressed in a recent deploy, roll back; if the app genuinely isn't ready, fix the dependency it's waiting on.",
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
				))
			}
			return insights
		},
	}
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
					insights = append(insights, newInsight(
						"warning",
						fmt.Sprintf("Pod/%s/%s", pod.Namespace, pod.Name),
						"Pod recently evicted",
						fmt.Sprintf("Pod %s/%s was evicted in the last %s: %s",
							pod.Namespace, pod.Name, recentEvictionWindow, pod.Status.Message),
						"Check node resource pressure. Evicted pods indicate the node ran out of resources.",
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
				insights = append(insights, newInsight(
					"warning",
					fmt.Sprintf("NetworkPolicy/%s/%s", np.Namespace, np.Name),
					"NetworkPolicy podSelector matches no pods",
					fmt.Sprintf(
						"NetworkPolicy %s/%s declares a podSelector but no pod in its namespace matches. "+
							"Traffic policy intent is unverifiable — either the selector has a typo or "+
							"the target workload was renamed / deleted.",
						np.Namespace, np.Name,
					),
					"Verify the podSelector matchLabels against the pods you expect this policy to gate. "+
						"kubectl get pods -n "+np.Namespace+" --show-labels",
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
				if policiesByNS[ns] > 0 {
					continue
				}
				insights = append(insights, newInsight(
					"info",
					fmt.Sprintf("Namespace/%s/%s", ns, ns),
					"Namespace has running pods but no NetworkPolicy",
					fmt.Sprintf(
						"Namespace %q has %d running pod(s) and zero NetworkPolicies. "+
							"All pod-to-pod and pod-to-external traffic in this namespace "+
							"is allowed by default — review whether that matches your "+
							"intended security posture.",
						ns, podCount,
					),
					"If this namespace should be isolated, start with a default-deny ingress "+
						"policy: https://kubernetes.io/docs/concepts/services-networking/network-policies/#default-deny-all-ingress-traffic",
				))
			}
			return insights
		},
	}
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
