package insights

import (
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubebolt/kubebolt/apps/api/internal/helm"
	"github.com/kubebolt/kubebolt/apps/api/internal/models"
)

// THE INVARIANT
//
//	For every rule there must exist a reachable HEALTHY state — with the
//	involved object STILL PRESENT — in which the rule emits nothing.
//
// A rule that can only stop emitting once its subject is deleted or garbage-
// collected is latched to an edge, not reading a level, and its insight will
// outlive the problem. That is precisely how liveness-probe-failing shipped: its
// only exit was the apiserver GC'ing the Event at --event-ttl, so the insight's
// lifetime tracked Kubernetes' retention policy instead of the workload's
// health, and Autopilot re-opened an incident every cooldown for an hour.
//
// The 2026-07 audit checked this in PROSE and got it wrong — it filed
// livenessProbeFailingRule under "Likely OK — current-state based" when the rule
// reads Events. Prose is why the bug survived; this table is the replacement.
// Every rule in AllRules() must appear below, and each pair asserts both
// directions: sick fires, healthy is silent.
//
// Adding a rule without adding it here fails TestInvariant_EveryRuleIsCovered.

type rulePair struct {
	sick    *ClusterState
	healthy *ClusterState
	// why documents what specifically makes the healthy variant healthy — the
	// clearing evidence a reader should be able to point at.
	why string
}

func TestInvariant_HealthyStateAlwaysClears(t *testing.T) {
	// Metric rules dampen with sustainedMetricWindow before firing; collapse it
	// so a single evaluation is enough to observe both directions.
	prev := sustainedMetricWindow
	sustainedMetricWindow = 0
	t.Cleanup(func() { sustainedMetricWindow = prev })

	for _, r := range AllRules() {
		pair, ok := invariantFixtures()[r.ID]
		if !ok {
			continue // reported by TestInvariant_EveryRuleIsCovered
		}
		t.Run(r.ID, func(t *testing.T) {
			// Rebuild the rule per direction: rules with per-instance trackers
			// (sustainedOver) must not carry state between the two states.
			if got := ruleByID(t, r.ID).Evaluate(pair.sick); len(got) == 0 {
				t.Fatalf("sick fixture produced no insight — the fixture is wrong, not the rule")
			}
			if got := ruleByID(t, r.ID).Evaluate(pair.healthy); len(got) != 0 {
				t.Errorf("healthy fixture still fires %d insight(s); the object is present and %s\n  first message: %s",
					len(got), pair.why, got[0].Message)
			}
		})
	}
}

func TestInvariant_EveryRuleIsCovered(t *testing.T) {
	fixtures := invariantFixtures()
	for _, r := range AllRules() {
		if _, ok := fixtures[r.ID]; !ok {
			t.Errorf("rule %q has no invariant fixture — add a sick/healthy pair so a "+
				"rule that can never clear cannot ship again", r.ID)
		}
	}
	for id := range fixtures {
		if ruleExists(id) {
			continue
		}
		t.Errorf("fixture %q matches no rule in AllRules() — stale after a rename?", id)
	}
}

func ruleByID(t *testing.T, id string) Rule {
	t.Helper()
	for _, r := range AllRules() {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("no rule %q", id)
	return Rule{}
}

func ruleExists(id string) bool {
	for _, r := range AllRules() {
		if r.ID == id {
			return true
		}
	}
	return false
}

// --- fixtures ---------------------------------------------------------------

func invariantFixtures() map[string]rulePair {
	now := time.Now()
	ago := func(d time.Duration) metav1.Time { return metav1.NewTime(now.Add(-d)) }

	// A container that has demonstrably recovered: replaced after the failure and
	// Ready well past readyGrace. This is the clearing EVIDENCE the supersession
	// rules read — note it is a state of the live object, not a passage of time.
	recovered := func(lastTerm *corev1.ContainerStateTerminated) corev1.ContainerStatus {
		return corev1.ContainerStatus{
			Name:                 "app",
			Ready:                true,
			RestartCount:         12,
			State:                corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: ago(30 * time.Minute)}},
			LastTerminationState: corev1.ContainerState{Terminated: lastTerm},
		}
	}
	oomTerm := &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137, FinishedAt: ago(time.Hour)}
	errTerm := &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1, FinishedAt: ago(time.Hour)}

	podWith := func(cs corev1.ContainerStatus) *corev1.Pod {
		p := pod("prod", "api")
		p.Status.Phase = corev1.PodRunning
		p.Status.ContainerStatuses = []corev1.ContainerStatus{cs}
		return p
	}
	waiting := func(reason, msg string) corev1.ContainerStatus {
		return corev1.ContainerStatus{Name: "app", State: corev1.ContainerState{
			Waiting: &corev1.ContainerStateWaiting{Reason: reason, Message: msg},
		}}
	}
	running := corev1.ContainerStatus{
		Name: "app", Ready: true,
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: ago(time.Hour)}},
	}

	// Metric fixtures: 900m used against a 1000m limit is over both thresholds;
	// the healthy variant keeps the pod and only moves the usage.
	metricPod := func() *corev1.Pod {
		p := pod("prod", "api")
		p.Spec.Containers = []corev1.Container{{
			Name: "app",
			Resources: corev1.ResourceRequirements{
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1000m"),
					corev1.ResourceMemory: resource.MustParse("1000Mi"),
				},
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("900m"),
					corev1.ResourceMemory: resource.MustParse("900Mi"),
				},
			},
		}}
		return p
	}
	metrics := func(cpuMilli, memMi int64) map[string]*models.MetricPoint {
		return map[string]*models.MetricPoint{
			"prod/api": {CPUUsage: cpuMilli, MemUsage: memMi * 1024 * 1024},
		}
	}

	labeled := func(l map[string]string) *corev1.Pod {
		p := pod("prod", "api")
		p.Labels = l
		p.Status.Phase = corev1.PodRunning
		return p
	}
	selector := &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}}

	deploy := func(mutate func(*appsv1.Deployment)) *appsv1.Deployment {
		one := int32(1)
		d := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "api"},
			Spec:       appsv1.DeploymentSpec{Replicas: &one},
			Status:     appsv1.DeploymentStatus{AvailableReplicas: 1},
		}
		mutate(d)
		return d
	}

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "api"},
		Spec:       corev1.ServiceSpec{ClusterIP: "10.0.0.1", Selector: map[string]string{"app": "api"}},
	}
	slice := func(ready bool) *discoveryv1.EndpointSlice {
		return &discoveryv1.EndpointSlice{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "prod", Name: "api-abc",
				Labels: map[string]string{"kubernetes.io/service-name": "api"},
			},
			Endpoints: []discoveryv1.Endpoint{{Conditions: discoveryv1.EndpointConditions{Ready: &ready}}},
		}
	}

	node := func(ready corev1.ConditionStatus) *corev1.Node {
		return &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
			Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: ready},
			}},
		}
	}

	hpa := func(current int32) *autoscalingv1.HorizontalPodAutoscaler {
		return &autoscalingv1.HorizontalPodAutoscaler{
			ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "api"},
			Spec:       autoscalingv1.HorizontalPodAutoscalerSpec{MaxReplicas: 10},
			Status:     autoscalingv1.HorizontalPodAutoscalerStatus{CurrentReplicas: current},
		}
	}

	pvc := func(phase corev1.PersistentVolumeClaimPhase) *corev1.PersistentVolumeClaim {
		return &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "data"},
			Status:     corev1.PersistentVolumeClaimStatus{Phase: phase},
		}
	}

	evicted := func(when metav1.Time) *corev1.Pod {
		p := pod("prod", "evicted")
		p.Status.Phase = corev1.PodFailed
		p.Status.Reason = "Evicted"
		p.Status.Conditions = []corev1.PodCondition{{LastTransitionTime: when}}
		return p
	}

	netpol := func(sel metav1.LabelSelector) *networkingv1.NetworkPolicy {
		return &networkingv1.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "allow-api"},
			Spec:       networkingv1.NetworkPolicySpec{PodSelector: sel},
		}
	}

	pdb := func(sel *metav1.LabelSelector) *policyv1.PodDisruptionBudget {
		return &policyv1.PodDisruptionBudget{
			ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "api"},
			Spec:       policyv1.PodDisruptionBudgetSpec{Selector: sel},
		}
	}

	// Liveness: the failing container is REPLACED and the replacement is healthy.
	livenessSick := probedPod("prod", "api", "app", nil, corev1.ContainerStatus{
		Ready: true,
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: ago(15 * time.Second)}},
	})
	livenessHealthy := probedPod("prod", "api", "app", nil, corev1.ContainerStatus{
		Ready: true,
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: ago(30 * time.Minute)}},
	})
	livenessEv := []*corev1.Event{livenessEvent("prod", "api", "app", 5, now.Add(-31*time.Minute))}

	notReady := func(transition metav1.Time) *corev1.Pod {
		p := podWith(running)
		p.Status.Conditions = []corev1.PodCondition{{
			Type: corev1.PodReady, Status: corev1.ConditionFalse,
			Reason: "ContainersNotReady", LastTransitionTime: transition,
		}}
		return p
	}
	ready := func() *corev1.Pod {
		p := podWith(running)
		p.Status.Conditions = []corev1.PodCondition{{
			Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: ago(time.Hour),
		}}
		return p
	}

	return map[string]rulePair{
		"crash-loop": {
			why:     "the container left CrashLoopBackOff and is Running",
			sick:    &ClusterState{Pods: []*corev1.Pod{podWith(corev1.ContainerStatus{Name: "app", RestartCount: 9, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}})}},
			healthy: &ClusterState{Pods: []*corev1.Pod{podWith(running)}},
		},
		"oom-killed": {
			why:     "the replacement run has been Ready past readyGrace (the OOM is still on record)",
			sick:    &ClusterState{Pods: []*corev1.Pod{podWith(corev1.ContainerStatus{Name: "app", LastTerminationState: corev1.ContainerState{Terminated: oomTerm}})}},
			healthy: &ClusterState{Pods: []*corev1.Pod{podWith(recovered(oomTerm))}},
		},
		"frequent-restarts": {
			why:     "RestartCount is still 12 but the current run is Ready past readyGrace",
			sick:    &ClusterState{Pods: []*corev1.Pod{podWith(corev1.ContainerStatus{Name: "app", RestartCount: 12, LastTerminationState: corev1.ContainerState{Terminated: errTerm}})}},
			healthy: &ClusterState{Pods: []*corev1.Pod{podWith(recovered(errTerm))}},
		},
		"liveness-probe-failing": {
			why:     "the killed container was replaced and the replacement held Ready past readyGrace",
			sick:    &ClusterState{Pods: []*corev1.Pod{livenessSick}, Events: livenessEv},
			healthy: &ClusterState{Pods: []*corev1.Pod{livenessHealthy}, Events: livenessEv},
		},
		"readiness-probe-failing": {
			why:     "the Ready condition flipped True",
			sick:    &ClusterState{Pods: []*corev1.Pod{notReady(ago(10 * time.Minute))}},
			healthy: &ClusterState{Pods: []*corev1.Pod{ready()}},
		},
		"image-pull-backoff": {
			why:     "the image resolved and the container is Running",
			sick:    &ClusterState{Pods: []*corev1.Pod{podWith(waiting("ImagePullBackOff", ""))}},
			healthy: &ClusterState{Pods: []*corev1.Pod{podWith(running)}},
		},
		"missing-config-dependency": {
			why:     "the ConfigMap exists now and the container started",
			sick:    &ClusterState{Pods: []*corev1.Pod{podWith(waiting("CreateContainerConfigError", `configmap "app-config" not found`))}},
			healthy: &ClusterState{Pods: []*corev1.Pod{podWith(running)}},
		},
		"evicted-pods": {
			why:     "the eviction fell outside the declared recent-evictions window (title says so)",
			sick:    &ClusterState{Pods: []*corev1.Pod{evicted(ago(time.Minute))}},
			healthy: &ClusterState{Pods: []*corev1.Pod{evicted(ago(3 * time.Hour))}},
		},
		"cpu-throttle-risk": {
			why:     "usage fell back under 80% of the limit",
			sick:    &ClusterState{Pods: []*corev1.Pod{metricPod()}, PodMetrics: metrics(900, 100)},
			healthy: &ClusterState{Pods: []*corev1.Pod{metricPod()}, PodMetrics: metrics(100, 100)},
		},
		"memory-pressure": {
			why:     "usage fell back under 85% of the limit",
			sick:    &ClusterState{Pods: []*corev1.Pod{metricPod()}, PodMetrics: metrics(10, 950)},
			healthy: &ClusterState{Pods: []*corev1.Pod{metricPod()}, PodMetrics: metrics(10, 100)},
		},
		"resource-underrequest": {
			why:     "usage came back in line with the requests",
			sick:    &ClusterState{Pods: []*corev1.Pod{metricPod()}, PodMetrics: metrics(9000, 9000)},
			healthy: &ClusterState{Pods: []*corev1.Pod{metricPod()}, PodMetrics: metrics(900, 900)},
		},
		"zero-replicas": {
			why:     "a replica became available",
			sick:    &ClusterState{Deployments: []*appsv1.Deployment{deploy(func(d *appsv1.Deployment) { d.Status.AvailableReplicas = 0 })}},
			healthy: &ClusterState{Deployments: []*appsv1.Deployment{deploy(func(d *appsv1.Deployment) {})}},
		},
		"progress-deadline-exceeded": {
			why: "the Progressing condition flipped back to True",
			sick: &ClusterState{Deployments: []*appsv1.Deployment{deploy(func(d *appsv1.Deployment) {
				d.Status.Conditions = []appsv1.DeploymentCondition{{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse, Reason: "ProgressDeadlineExceeded"}}
			})}},
			healthy: &ClusterState{Deployments: []*appsv1.Deployment{deploy(func(d *appsv1.Deployment) {
				d.Status.Conditions = []appsv1.DeploymentCondition{{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionTrue, Reason: "NewReplicaSetAvailable"}}
			})}},
		},
		"pvc-pending": {
			why:     "the claim bound",
			sick:    &ClusterState{PVCs: []*corev1.PersistentVolumeClaim{pvc(corev1.ClaimPending)}},
			healthy: &ClusterState{PVCs: []*corev1.PersistentVolumeClaim{pvc(corev1.ClaimBound)}},
		},
		"node-not-ready": {
			why:     "the node's Ready condition went True",
			sick:    &ClusterState{Nodes: []*corev1.Node{node(corev1.ConditionFalse)}},
			healthy: &ClusterState{Nodes: []*corev1.Node{node(corev1.ConditionTrue)}},
		},
		"hpa-maxed-out": {
			why:     "the HPA scaled back below its ceiling",
			sick:    &ClusterState{HPAs: []*autoscalingv1.HorizontalPodAutoscaler{hpa(10)}},
			healthy: &ClusterState{HPAs: []*autoscalingv1.HorizontalPodAutoscaler{hpa(3)}},
		},
		"service-no-endpoints": {
			why:     "an endpoint became ready",
			sick:    &ClusterState{Services: []*corev1.Service{svc}, EndpointSlices: []*discoveryv1.EndpointSlice{slice(false)}},
			healthy: &ClusterState{Services: []*corev1.Service{svc}, EndpointSlices: []*discoveryv1.EndpointSlice{slice(true)}},
		},
		"policy-no-match": {
			why:     "a pod now carries the labels the policy selects",
			sick:    &ClusterState{NetworkPolicies: []*networkingv1.NetworkPolicy{netpol(*selector)}, Pods: []*corev1.Pod{labeled(map[string]string{"app": "other"})}},
			healthy: &ClusterState{NetworkPolicies: []*networkingv1.NetworkPolicy{netpol(*selector)}, Pods: []*corev1.Pod{labeled(map[string]string{"app": "api"})}},
		},
		"policy-orphan": {
			why:     "the namespace got a NetworkPolicy",
			sick:    &ClusterState{Pods: []*corev1.Pod{labeled(map[string]string{"app": "api"})}},
			healthy: &ClusterState{Pods: []*corev1.Pod{labeled(map[string]string{"app": "api"})}, NetworkPolicies: []*networkingv1.NetworkPolicy{netpol(*selector)}},
		},
		"pdb-no-match": {
			why:     "a pod now carries the labels the budget selects",
			sick:    &ClusterState{PDBs: []*policyv1.PodDisruptionBudget{pdb(selector)}, Pods: []*corev1.Pod{labeled(map[string]string{"app": "other"})}},
			healthy: &ClusterState{PDBs: []*policyv1.PodDisruptionBudget{pdb(selector)}, Pods: []*corev1.Pod{labeled(map[string]string{"app": "api"})}},
		},
		"helm-release-failed": {
			why:     "the release status went back to deployed",
			sick:    &ClusterState{HelmReleases: []helm.Release{{Namespace: "prod", Name: "api", Status: "failed"}}},
			healthy: &ClusterState{HelmReleases: []helm.Release{{Namespace: "prod", Name: "api", Status: "deployed"}}},
		},
		"helm-release-hook-pending": {
			why:     "the hook completed and the release left pending-*",
			sick:    &ClusterState{HelmReleases: []helm.Release{{Namespace: "prod", Name: "api", Status: "pending-upgrade", Updated: now.Add(-time.Hour)}}},
			healthy: &ClusterState{HelmReleases: []helm.Release{{Namespace: "prod", Name: "api", Status: "deployed", Updated: now.Add(-time.Hour)}}},
		},
		"cert-expiring": {
			why:     "cert-manager renewed it",
			sick:    &ClusterState{Certificates: []map[string]interface{}{{"name": "tls", "namespace": "prod", "expiresInDays": 2}}},
			healthy: &ClusterState{Certificates: []map[string]interface{}{{"name": "tls", "namespace": "prod", "expiresInDays": 89}}},
		},
		"argocd-out-of-sync": {
			why:     "the app synced and reports Healthy",
			sick:    &ClusterState{ArgoApps: []map[string]interface{}{{"name": "api", "namespace": "prod", "syncStatus": "OutOfSync", "healthStatus": "Healthy"}}},
			healthy: &ClusterState{ArgoApps: []map[string]interface{}{{"name": "api", "namespace": "prod", "syncStatus": "Synced", "healthStatus": "Healthy"}}},
		},
	}
}
