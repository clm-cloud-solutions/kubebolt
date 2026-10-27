package insights

import (
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubebolt/kubebolt/apps/api/internal/models"
)

// Each rule names only a cause it read (rules_causes.go). These pin the cases
// where the old text asserted one it had not checked.

func i32(v int32) *int32 { return &v }

func configErrorPod(msg string) *corev1.Pod {
	p := pod("shop", "pricing-0")
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:  "api",
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CreateContainerConfigError", Message: msg}},
	}}
	return p
}

func TestMissingConfigDependency_NamesWhatIsWrong(t *testing.T) {
	cases := []struct {
		msg, title, mustSay, mustNotSay string
	}{
		{`configmap "pricing-settings" not found`, "Pod references a missing ConfigMap", `Create ConfigMap "pricing-settings"`, ""},
		{`secret "db-creds" not found`, "Pod references a missing Secret", `Create Secret "db-creds"`, ""},
		{`couldn't find key DATABASE_URL in ConfigMap shop/app-settings`, "Pod references a missing ConfigMap key",
			`Add key "DATABASE_URL" to ConfigMap shop/app-settings`, "Create "},
		{`failed to sync configmap cache: timed out waiting for the condition`, "Container configuration could not be read in time",
			"Nothing is missing", "Create "},
		{`container has runAsNonRoot and image will run as root (pod: "pricing-0_shop(…)", container: api)`, "Container blocked by runAsNonRoot",
			"runAsUser", "Create "},
		{`something the kubelet invents next year`, "Container cannot be created: configuration error", "Read the message", "Create "},
	}
	for _, c := range cases {
		got := missingConfigDependencyRule().Evaluate(&ClusterState{Pods: []*corev1.Pod{configErrorPod(c.msg)}})
		if len(got) != 1 {
			t.Fatalf("%q: %d insights", c.msg, len(got))
		}
		in := got[0]
		if in.Title != c.title {
			t.Errorf("%q: title %q, want %q", c.msg, in.Title, c.title)
		}
		if !strings.Contains(in.Suggestion, c.mustSay) || (c.mustNotSay != "" && strings.Contains(in.Suggestion, c.mustNotSay)) {
			t.Errorf("%q: suggestion %q", c.msg, in.Suggestion)
		}
		if w := evidenceByLabel(t, in, "Container waiting"); !strings.Contains(w.Detail, c.msg) {
			t.Errorf("%q: evidence %+v", c.msg, in.Evidence)
		}
	}
}

func stalledDeployment(rev string, desired, updated, total int32) *appsv1.Deployment {
	d := deploymentWithProgressing("shop", "api", corev1.ConditionFalse, "ProgressDeadlineExceeded",
		`ReplicaSet "api-7c5" has timed out progressing.`)
	d.Annotations = map[string]string{"deployment.kubernetes.io/revision": rev}
	d.Spec.Replicas = i32(desired)
	d.Status.UpdatedReplicas, d.Status.Replicas = updated, total
	return d
}

func TestProgressDeadline_SaysRolloutScaleUpOrFirstRevision(t *testing.T) {
	cases := []struct {
		name    string
		d       *appsv1.Deployment
		mustSay string
	}{
		{"first revision", stalledDeployment("1", 3, 1, 1), "nothing to roll back to"},
		{"old pods still running", stalledDeployment("4", 3, 1, 4), "rolling back to the previous revision"},
		{"every pod on the current revision", stalledDeployment("4", 5, 3, 3), "a rollback does not help with a quota"},
	}
	for _, c := range cases {
		got := progressDeadlineExceededRule().Evaluate(&ClusterState{Deployments: []*appsv1.Deployment{c.d}})
		if len(got) != 1 || !strings.Contains(got[0].Suggestion, c.mustSay) {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
}

func TestPDBNoMatch_ScaledToZeroWorkloadIsNotATypo(t *testing.T) {
	pdb := &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Namespace: "demo", Name: "guarded-pdb"},
		Spec:       policyv1.PodDisruptionBudgetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "guarded"}}},
	}
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "demo", Name: "guarded"},
		Spec: appsv1.DeploymentSpec{Replicas: i32(0), Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "guarded"}},
		}},
	}
	if got := pdbNoMatchRule().Evaluate(&ClusterState{PDBs: []*policyv1.PodDisruptionBudget{pdb}, Deployments: []*appsv1.Deployment{dep}}); len(got) != 0 {
		t.Errorf("a PDB whose Deployment is scaled to 0 fired: %+v", got)
	}
	got := pdbNoMatchRule().Evaluate(&ClusterState{PDBs: []*policyv1.PodDisruptionBudget{pdb}})
	if len(got) != 1 || !strings.Contains(got[0].Message, "typo") {
		t.Errorf("a PDB that matches no workload should fire as a likely typo: %+v", got)
	}
}

func TestPolicyNoMatch_CronJobBetweenRunsIsNotATypo(t *testing.T) {
	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: "batch", Name: "report-egress"},
		Spec:       networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "report"}}},
	}
	cj := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "batch", Name: "report"},
		Spec: batchv1.CronJobSpec{JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "report"}},
		}}}},
	}
	if got := networkPolicyNoMatchRule().Evaluate(&ClusterState{NetworkPolicies: []*networkingv1.NetworkPolicy{np}, CronJobs: []*batchv1.CronJob{cj}}); len(got) != 0 {
		t.Errorf("a policy for a CronJob between runs fired: %+v", got)
	}
}

func limitedPod(ns, name string, mem ...string) *corev1.Pod {
	p := pod(ns, name)
	for i, m := range mem {
		c := corev1.Container{Name: "c" + string(rune('0'+i))}
		if m != "" {
			c.Resources.Limits = corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(m)}
		}
		p.Spec.Containers = append(p.Spec.Containers, c)
	}
	return p
}

func TestMemoryNearLimit_IsThePodsOwnLimit(t *testing.T) {
	old := sustainedMetricWindow
	sustainedMetricWindow = 0
	defer func() { sustainedMetricWindow = old }()

	full := limitedPod("shop", "api", "100Mi", "100Mi")
	sidecar := limitedPod("shop", "web", "100Mi", "")
	state := &ClusterState{
		Pods: []*corev1.Pod{full, sidecar},
		PodMetrics: map[string]*models.MetricPoint{
			"shop/api": {MemUsage: 190 << 20},
			"shop/web": {MemUsage: 190 << 20},
		},
	}
	got := memoryPressureRule().Evaluate(state)
	if len(got) != 1 || got[0].Resource != "Pod/shop/api" {
		t.Fatalf("want only the pod whose every container has a limit, got %+v", got)
	}
	if got[0].Title != "Memory Near Limit" || !strings.Contains(got[0].Suggestion, "not memory pressure on the node") {
		t.Errorf("insight %+v", got[0])
	}
}

func TestPolicyOrphan_CiliumPoliciesCount(t *testing.T) {
	running := func(ns string) *corev1.Pod {
		p := pod(ns, "p")
		p.Status.Phase = corev1.PodRunning
		return p
	}
	state := &ClusterState{
		Pods:                []*corev1.Pod{running("demo"), running("shop")},
		CiliumPolicies:      map[string]int{"demo": 2},
		CiliumPoliciesKnown: true,
	}
	got := namespaceWithoutNetworkPolicyRule().Evaluate(state)
	if len(got) != 1 || got[0].Resource != "Namespace/shop/shop" {
		t.Fatalf("a namespace with CiliumNetworkPolicies fired, or the other did not: %+v", got)
	}
	if !strings.Contains(got[0].Message, "no NetworkPolicy or CiliumNetworkPolicy") || !strings.Contains(got[0].Message, "Unless the CNI") {
		t.Errorf("message %q", got[0].Message)
	}

	state.CiliumClusterwidePolicies = 1
	if got := namespaceWithoutNetworkPolicyRule().Evaluate(state); !strings.Contains(got[0].Message, "CiliumClusterwideNetworkPolicy") {
		t.Errorf("cluster-wide policies not mentioned: %q", got[0].Message)
	}

	unknown := &ClusterState{Pods: []*corev1.Pod{running("shop")}}
	if got := namespaceWithoutNetworkPolicyRule().Evaluate(unknown); strings.Contains(got[0].Message, "CiliumNetworkPolicy") {
		t.Errorf("claimed no CiliumNetworkPolicy without having read them: %q", got[0].Message)
	}
}

func TestPVCPending_ReadsBindingModeClassAndPods(t *testing.T) {
	wffc := storagev1.VolumeBindingWaitForFirstConsumer
	immediate := storagev1.VolumeBindingImmediate
	classes := []*storagev1.StorageClass{
		{ObjectMeta: metav1.ObjectMeta{Name: "standard", Annotations: map[string]string{"storageclass.kubernetes.io/is-default-class": "true"}},
			Provisioner: "rancher.io/local-path", VolumeBindingMode: &wffc},
		{ObjectMeta: metav1.ObjectMeta{Name: "fast"}, Provisioner: "ebs.csi.aws.com", VolumeBindingMode: &immediate},
	}
	claim := func(name string, class *string) *corev1.PersistentVolumeClaim {
		return &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Namespace: "db", Name: name},
			Spec:       corev1.PersistentVolumeClaimSpec{StorageClassName: class},
			Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimPending},
		}
	}
	str := func(s string) *string { return &s }
	user := pod("db", "pg-0")
	user.Status.Phase = corev1.PodPending
	user.Spec.Volumes = []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{
		PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "used"}}}}
	failed := &corev1.Event{
		Type: corev1.EventTypeWarning, Reason: "ProvisioningFailed", Message: "quota exceeded for gp3",
		InvolvedObject: corev1.ObjectReference{Kind: "PersistentVolumeClaim", Namespace: "db", Name: "fast-one"},
		LastTimestamp:  metav1.NewTime(time.Now()),
	}
	state := &ClusterState{
		StorageClasses: classes,
		Pods:           []*corev1.Pod{user},
		Events:         []*corev1.Event{failed},
		PVCs: []*corev1.PersistentVolumeClaim{
			claim("unused", nil),           // default class, WaitForFirstConsumer, no pod: by design
			claim("used", nil),             // its pod is not scheduled
			claim("ghost", str("premium")), // a class that does not exist
			claim("fast-one", str("fast")), // Immediate, provisioning failed
		},
	}
	got := map[string]models.Insight{}
	for _, in := range pvcPendingRule().Evaluate(state) {
		got[in.Resource] = in
	}
	if _, ok := got["PVC/db/unused"]; ok || len(got) != 3 {
		t.Fatalf("a claim waiting for its first pod fired, or another did not: %v", got)
	}
	if !strings.Contains(got["PVC/db/used"].Message, "waits for its pod to be scheduled") {
		t.Errorf("used: %q", got["PVC/db/used"].Message)
	}
	if !strings.Contains(got["PVC/db/ghost"].Message, `"premium", which does not exist`) {
		t.Errorf("ghost: %q", got["PVC/db/ghost"].Message)
	}
	if !strings.Contains(got["PVC/db/fast-one"].Message, "quota exceeded for gp3") {
		t.Errorf("fast-one: %q", got["PVC/db/fast-one"].Message)
	}
}

func TestHPAMaxedOut_FixedSizeAndCPUBelowTarget(t *testing.T) {
	hpa := func(min, max, cur int32, cpu, target *int32) *autoscalingv1.HorizontalPodAutoscaler {
		return &autoscalingv1.HorizontalPodAutoscaler{
			ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api"},
			Spec:       autoscalingv1.HorizontalPodAutoscalerSpec{MinReplicas: &min, MaxReplicas: max, TargetCPUUtilizationPercentage: target},
			Status:     autoscalingv1.HorizontalPodAutoscalerStatus{CurrentReplicas: cur, CurrentCPUUtilizationPercentage: cpu},
		}
	}
	eval := func(h *autoscalingv1.HorizontalPodAutoscaler) []models.Insight {
		return hpaMaxedOutRule().Evaluate(&ClusterState{HPAs: []*autoscalingv1.HorizontalPodAutoscaler{h}})
	}
	if got := eval(hpa(3, 3, 3, i32(10), i32(80))); len(got) != 0 {
		t.Errorf("a fixed-size HPA fired: %+v", got)
	}
	if got := eval(hpa(1, 5, 5, i32(140), i32(80))); len(got) != 1 || !strings.Contains(got[0].Message, "demand is above") {
		t.Errorf("CPU above target: %+v", got)
	}
	// CPU below target: only the controller can say another metric wants
	// more (hpaWantsMoreThanMax); without its verdict nothing fires.
	if got := eval(hpa(1, 5, 5, i32(30), i32(80))); len(got) != 0 {
		t.Errorf("CPU below target, no controller verdict, fired: %+v", got)
	}
	limited := hpa(1, 5, 5, i32(30), i32(80))
	limited.Annotations = map[string]string{hpaConditionsAnnotation: `[{"type":"ScalingLimited","status":"True","reason":"TooManyReplicas"}]`}
	if got := eval(limited); len(got) != 1 || !strings.Contains(got[0].Message, "another metric") ||
		strings.Contains(got[0].Suggestion, "Raise maxReplicas if") {
		t.Errorf("CPU below target, controller wants more: %+v", got)
	}
}

func TestOOMKilled_ReadsTheLimitAndExitCode(t *testing.T) {
	oom := func(limit string) *corev1.Pod {
		p := limitedPod("shop", "api", limit)
		p.Status.ContainerStatuses = []corev1.ContainerStatus{{
			Name: "c0",
			LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				Reason: "OOMKilled", ExitCode: 137, FinishedAt: metav1.NewTime(time.Now()),
			}},
		}}
		return p
	}
	got := oomKilledRule().Evaluate(&ClusterState{Pods: []*corev1.Pod{oom("")}})
	if len(got) != 1 || !strings.Contains(got[0].Message, "no memory limit of its own") || strings.Contains(got[0].Suggestion, "raise it") {
		t.Errorf("no limit: %+v", got)
	}
	got = oomKilledRule().Evaluate(&ClusterState{Pods: []*corev1.Pod{oom("128Mi")}})
	if len(got) != 1 || !strings.Contains(got[0].Message, "limit of 128Mi") || !strings.Contains(got[0].Message, "exit code 137") {
		t.Errorf("with limit: %+v", got)
	}
}
