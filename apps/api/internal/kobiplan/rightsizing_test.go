package kobiplan

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubebolt/kubebolt/apps/api/internal/costmodel"
	"github.com/kubebolt/kubebolt/apps/api/internal/rightsizing"
)

const mi = 1024 * 1024

func specOf(containers ...corev1.Container) corev1.PodSpec {
	return corev1.PodSpec{Containers: containers}
}

func container(name, cpuReq, memReq, cpuLim, memLim string) corev1.Container {
	c := corev1.Container{Name: name, Image: name + ":1"}
	c.Resources.Requests = corev1.ResourceList{}
	c.Resources.Limits = corev1.ResourceList{}
	if cpuReq != "" {
		c.Resources.Requests[corev1.ResourceCPU] = resource.MustParse(cpuReq)
	}
	if memReq != "" {
		c.Resources.Requests[corev1.ResourceMemory] = resource.MustParse(memReq)
	}
	if cpuLim != "" {
		c.Resources.Limits[corev1.ResourceCPU] = resource.MustParse(cpuLim)
	}
	if memLim != "" {
		c.Resources.Limits[corev1.ResourceMemory] = resource.MustParse(memLim)
	}
	return c
}

func over(request, p95, suggest int64) rightsizing.Finding {
	return rightsizing.Finding{Request: request, P95: p95, State: rightsizing.Over, Suggest: suggest}
}

func ok(request, limit int64) rightsizing.Finding {
	return rightsizing.Finding{Request: request, Limit: limit, State: rightsizing.OK}
}

func rec(ns, kind, name string, cpu, mem rightsizing.Finding) rightsizing.Recommendation {
	return rightsizing.Recommendation{Namespace: ns, Kind: kind, Name: name, CPU: cpu, Mem: mem}
}

func facts(typ, ns, name string, pods int, spec corev1.PodSpec) WorkloadFacts {
	return WorkloadFacts{Type: typ, Namespace: ns, Name: name, Generation: 7, Template: spec, Pods: pods, PodsStable: true}
}

// fixture is a cluster with one of everything the builder decides on.
func fixture() RightsizingInput {
	window := 6.94
	yes := true
	recs := []rightsizing.Recommendation{
		// The spec's example: 3 pods, 500m/1Gi each, the engine says 360m and
		// 900Mi for the whole workload → 120m and 300Mi per pod.
		rec("payments", "Deployment", "api", over(1500, 300, 360), over(3*1024*mi, 750*mi, 900*mi)),
		rec("payments", "Deployment", "worker",
			rightsizing.Finding{Limit: 800, P95: 780, State: rightsizing.NearLimit, Suggest: 1170}, ok(0, 0)),
		rec("kube-system", "DaemonSet", "azure-cns", over(400, 20, 30), ok(0, 0)),
		rec("shop", "Deployment", "web", over(1000, 100, 120), ok(0, 0)),
		rec("shop", "Deployment", "sidecar", over(1000, 100, 120), ok(0, 0)),
		rec("shop", "StatefulSet", "db", over(2000, 200, 240), ok(0, 0)),
		rec("other", "Deployment", "nospec", rightsizing.Finding{P95: 300, State: rightsizing.NoSpecs}, ok(0, 0)),
		rec("ops", "Deployment", "varied", over(1000, 100, 120), ok(0, 0)),
		rec("ops", "Deployment", "managed", over(1000, 100, 120), ok(0, 0)),
		rec("ops", "Deployment", "limitrange", over(1000, 100, 120), ok(0, 0)),
	}
	ws := map[string]WorkloadFacts{}
	add := func(kind string, f WorkloadFacts) { ws[rightsizing.Key(f.Namespace, kind, f.Name)] = f }
	api := facts("deployments", "payments", "api", 3, specOf(container("api", "500m", "1Gi", "1", "2Gi")))
	api.Object.Labels = map[string]string{"app.kubernetes.io/managed-by": "Helm"}
	api.Object.Annotations = map[string]string{"meta.helm.sh/release-name": "payments"}
	add("Deployment", api)
	add("Deployment", facts("deployments", "payments", "worker", 2, specOf(container("worker", "200m", "", "400m", ""))))
	cns := facts("daemonsets", "kube-system", "azure-cns", 4, specOf(container("cns", "100m", "", "", "")))
	cns.Object.Labels = map[string]string{"kubernetes.azure.com/managedby": "aks"}
	add("DaemonSet", cns)
	web := facts("deployments", "shop", "web", 2, specOf(container("web", "500m", "", "", "")))
	web.HPA = "web"
	add("Deployment", web)
	add("Deployment", facts("deployments", "shop", "sidecar", 2,
		specOf(container("app", "250m", "", "", ""), container("proxy", "250m", "", "", ""))))
	add("StatefulSet", facts("statefulsets", "shop", "db", 1, specOf(container("db", "2", "", "", ""))))
	add("Deployment", facts("deployments", "other", "nospec", 1, specOf(container("x", "", "", "", ""))))
	varied := facts("deployments", "ops", "varied", 2, specOf(container("v", "500m", "", "", "")))
	varied.PodsStable = false
	add("Deployment", varied)
	managed := facts("deployments", "ops", "managed", 2, specOf(container("m", "500m", "", "", "")))
	managed.Object.OwnerRefs = []metav1.OwnerReference{{Kind: "Grafana", Name: "main", Controller: &yes}}
	add("Deployment", managed)
	// The pods got their request from a LimitRange: the spec has none.
	add("Deployment", facts("deployments", "ops", "limitrange", 2, specOf(container("l", "", "", "", ""))))

	return RightsizingInput{
		ID: "pln_test", OrgID: "org-1", ClusterID: "c-uid", Title: "Right-sizing",
		Source:     Source{Initiator: InitiatorChat, UserID: "u-1", ConversationID: "conv-1"},
		Result:     rightsizing.Result{Recs: recs, WindowDays: &window, MinHistoryHours: 48},
		ComputedAt: t0.Add(-time.Minute),
		Workloads:  ws,
		Pricing: Pricing{Rates: costmodel.NodeRates{CPUCoreHourly: 0.03, RAMGiBHourly: 0.004},
			Factor: 1, Currency: "usd", Source: "opencost"},
		Now: t0,
	}
}

func stepByName(p Plan, name string) *Step {
	for i := range p.Steps {
		if p.Steps[i].Target.Name == name {
			return &p.Steps[i]
		}
	}
	return nil
}

func TestBuildRightsizing_StepsExclusionsAndOrder(t *testing.T) {
	p, err := BuildRightsizing(fixture())
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, s := range p.Steps {
		order = append(order, s.ID+":"+s.Target.Name)
	}
	// Near-limit first, then over-provisioned by risk: api is medium (lowers a
	// memory request), db high (a StatefulSet with one pod).
	want := []string{"s1:worker", "s2:api", "s3:db"}
	if len(order) != len(want) {
		t.Fatalf("steps = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("steps = %v, want %v", order, want)
		}
	}

	excluded := map[string]ExclusionReason{}
	for _, e := range p.Excluded {
		excluded[e.Target.Name] = e.Reason
	}
	wantExcl := map[string]ExclusionReason{
		"azure-cns":  ReasonProviderManaged,
		"web":        ReasonHPATarget,
		"sidecar":    ReasonMultiContainer,
		"nospec":     ReasonNoEngineValue,
		"varied":     ReasonPodsVaried,
		"managed":    ReasonOperatorManaged,
		"limitrange": ReasonNoEngineValue,
	}
	if len(excluded) != len(wantExcl) {
		t.Fatalf("excluded = %v, want %v", excluded, wantExcl)
	}
	for name, reason := range wantExcl {
		if excluded[name] != reason {
			t.Errorf("%s excluded as %q, want %q", name, excluded[name], reason)
		}
	}
	for _, e := range p.Excluded {
		if e.Target.Name == "azure-cns" && (e.Target.Owner == nil || e.Target.Owner.Ref != "aks" || e.Detail != "kubernetes.azure.com/managedby") {
			t.Errorf("azure-cns exclusion = %+v, want the aks owner and its marker", e)
		}
	}
	if p.Status != StatusDraft || p.Version != 1 || !p.ExpiresAt.Equal(t0.Add(7*24*time.Hour)) {
		t.Errorf("status %s version %d expires %v", p.Status, p.Version, p.ExpiresAt)
	}
	if p.Engine.WindowDays != 6.9 || p.Engine.Percentile != 0.95 || p.Engine.MinHistoryHours != 48 {
		t.Errorf("engine = %+v", p.Engine)
	}
}

func TestBuildRightsizing_TheSpecExampleStep(t *testing.T) {
	p, err := BuildRightsizing(fixture())
	if err != nil {
		t.Fatal(err)
	}
	s := stepByName(p, "api")
	if got := string(s.Params); got != `{"containers":[{"container":"api","requests":{"cpu":"120m","memory":"300Mi"}}]}` {
		t.Errorf("params = %s", got)
	}
	if got := string(s.Rollback.Params); got != `{"containers":[{"container":"api","requests":{"cpu":"500m","memory":"1Gi"}}]}` {
		t.Errorf("rollback = %s", got)
	}
	if got := string(s.Before); got != `{"containers":[{"container":"api","requests":{"cpu":"500m","memory":"1Gi"},"limits":{"cpu":"1","memory":"2Gi"}}]}` {
		t.Errorf("before = %s", got)
	}
	if s.Impact.Estimated.CPUMilli != 1140 || s.Impact.Estimated.MemBytes != 2277507072 {
		t.Errorf("savings = %+v, want 1140m and 2172Mi", s.Impact.Estimated)
	}
	if s.Target.Owner == nil || s.Target.Owner.Kind != OwnerHelm || s.Target.Owner.Ref != "payments" {
		t.Errorf("owner = %+v, want helm payments", s.Target.Owner)
	}
	if s.Risk != RiskMedium || len(s.RiskReasons) != 1 || s.RiskReasons[0] != RiskReasonLowersMemory {
		t.Errorf("risk = %s %v", s.Risk, s.RiskReasons)
	}
	if s.Precondition.SpecHash != SpecHash(fixture().Workloads["payments/Deployment/api"].Template) || s.Precondition.Generation != 7 {
		t.Errorf("precondition = %+v", s.Precondition)
	}
	if s.Provenance != "rightsizing · P95 7 d · request ×1.2 · 3 pods" {
		t.Errorf("provenance = %q", s.Provenance)
	}
	var ev map[string]any
	if err := json.Unmarshal(s.Evidence, &ev); err != nil || ev["pods"] != float64(3) || ev["podsStableInWindow"] != true {
		t.Errorf("evidence = %s", s.Evidence)
	}
}

func TestBuildRightsizing_ALimitRaisedSavesNothing(t *testing.T) {
	p, _ := BuildRightsizing(fixture())
	s := stepByName(p, "worker")
	// 1170m over 2 pods = 585m, rounded to 590m; today 400m.
	if got := string(s.Params); got != `{"containers":[{"container":"worker","limits":{"cpu":"590m"}}]}` {
		t.Errorf("params = %s", got)
	}
	if s.Impact.Estimated.CPUMilli != 0 || s.Impact.Estimated.MemBytes != 0 {
		t.Errorf("a raised limit saves %+v", s.Impact.Estimated)
	}
	if s.Impact.Estimated.Money == nil || s.Impact.Estimated.Money.Monthly != 0 {
		t.Errorf("money = %+v, want 0", s.Impact.Estimated.Money)
	}
}

func TestBuildRightsizing_Money(t *testing.T) {
	p, _ := BuildRightsizing(fixture())
	rates := costmodel.NodeRates{CPUCoreHourly: 0.03, RAMGiBHourly: 0.004}
	var sum float64
	for _, s := range p.Steps {
		want := costmodel.Round2(costmodel.MonthlySavings(s.Impact.Estimated.CPUMilli, s.Impact.Estimated.MemBytes, rates))
		if s.Impact.Estimated.Money.Monthly != want || s.Impact.Estimated.Money.Currency != "usd" {
			t.Errorf("%s money = %+v, want %v usd", s.ID, s.Impact.Estimated.Money, want)
		}
		sum += want
	}
	// The total is the sum of what the steps show, to the cent.
	if p.Impact.Estimated.Money == nil || p.Impact.Estimated.Money.Monthly != costmodel.Round2(sum) {
		t.Errorf("plan money = %+v, want the steps' sum %v", p.Impact.Estimated.Money, costmodel.Round2(sum))
	}
	if p.Impact.Estimated.CPUMilli != 1140+1760 || p.Impact.Estimated.MemBytes != 2277507072 {
		t.Errorf("plan units = %+v", p.Impact.Estimated)
	}

	in := fixture()
	in.Pricing.Factor = 0.9
	in.Pricing.Currency = "eur"
	converted, _ := BuildRightsizing(in)
	var convertedSum float64
	for _, s := range converted.Steps {
		convertedSum += s.Impact.Estimated.Money.Monthly
	}
	if got, want := converted.Impact.Estimated.Money.Monthly, costmodel.Round2(convertedSum); math.Abs(got-want) > 0.001 || converted.Impact.Estimated.Money.Currency != "eur" {
		t.Errorf("converted = %+v, want %v eur", converted.Impact.Estimated.Money, want)
	}
}

func TestBuildRightsizing_NoPricesNoMoney(t *testing.T) {
	in := fixture()
	in.Pricing = Pricing{}
	p, err := BuildRightsizing(in)
	if err != nil {
		t.Fatal(err)
	}
	if p.Impact.Estimated.Money != nil || p.Impact.Estimated.MoneyUnavailable != MoneyNoCostData {
		t.Errorf("impact = %+v, want no money and no_cost_data", p.Impact.Estimated)
	}
	for _, s := range p.Steps {
		if s.Impact.Estimated.Money != nil {
			t.Errorf("%s has money without prices", s.ID)
		}
	}
}

func TestBuildRightsizing_Scope(t *testing.T) {
	in := fixture()
	in.Scope = Scope{Namespaces: []string{"payments"}}
	p, _ := BuildRightsizing(in)
	if len(p.Steps) != 2 || len(p.Excluded) != 0 {
		t.Fatalf("payments only: %d steps, %d excluded", len(p.Steps), len(p.Excluded))
	}

	in.Scope = Scope{Workloads: []string{"shop/statefulsets/db", "ops/varied"}}
	p, _ = BuildRightsizing(in)
	if len(p.Steps) != 1 || p.Steps[0].Target.Name != "db" || len(p.Excluded) != 1 || p.Excluded[0].Target.Name != "varied" {
		t.Fatalf("two workloads: steps %+v excluded %+v", p.Steps, p.Excluded)
	}
}

// A workload with a pending step in another live plan is left out, naming
// that plan: changed there, its saving is not counted twice (plans-06).
func TestBuildRightsizing_AWorkloadPlannedElsewhereIsLeftOut(t *testing.T) {
	in := fixture()
	in.Scope = Scope{Namespaces: []string{"payments"}}
	in.Planned = map[string]string{TargetKey("deployments", "payments", "api"): "Plan de ahorro - payments"}
	p, err := BuildRightsizing(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Steps) != 1 || p.Steps[0].Target.Name != "worker" || p.Steps[0].Order != 1 {
		t.Fatalf("steps = %+v", p.Steps)
	}
	if len(p.Excluded) != 1 || p.Excluded[0].Reason != ReasonInLivePlan || p.Excluded[0].Target.Name != "api" ||
		!strings.Contains(p.Excluded[0].Detail, "Plan de ahorro - payments") {
		t.Fatalf("excluded = %+v", p.Excluded)
	}
	// The total is what is left.
	if p.Impact.Estimated.CPUMilli != p.Steps[0].Impact.Estimated.CPUMilli {
		t.Fatalf("total = %+v", p.Impact.Estimated)
	}
	// An exclusion the engine makes anyway keeps its own reason.
	in.Scope = Scope{}
	in.Planned = map[string]string{TargetKey("daemonsets", "kube-system", "azure-cns"): "Otro"}
	p, _ = BuildRightsizing(in)
	for _, x := range p.Excluded {
		if x.Target.Name == "azure-cns" && x.Reason != ReasonProviderManaged {
			t.Fatalf("azure-cns = %+v", x)
		}
	}
}

func TestSameScope(t *testing.T) {
	if !SameScope(Scope{}, Scope{Namespaces: []string{}, Workloads: nil}) {
		t.Fatal("the whole cluster twice")
	}
	if !SameScope(Scope{Namespaces: []string{"b", "a"}}, Scope{Namespaces: []string{"a", "b"}}) {
		t.Fatal("the same namespaces in another order")
	}
	if SameScope(Scope{Namespaces: []string{"a"}}, Scope{}) || SameScope(Scope{Namespaces: []string{"a"}}, Scope{Workloads: []string{"a"}}) {
		t.Fatal("different scopes compared equal")
	}
}

// A workload behind an admission webhook that fails closed is high risk,
// and the step keeps the webhook in its evidence (plans-13); one that fails
// open is not.
func TestBuildRightsizing_AdmissionWebhookIsHighRisk(t *testing.T) {
	in := fixture()
	in.Scope = Scope{Workloads: []string{"payments/worker"}}
	w := in.Workloads[rightsizing.Key("payments", "Deployment", "worker")]
	w.Webhooks = []WebhookFacts{
		{Name: "validate.payments.io", FailurePolicy: "Fail", TimeoutSeconds: 10},
		{Name: "validate.payments.io", FailurePolicy: "Fail", TimeoutSeconds: 10}, // registered twice
		{Name: "audit.payments.io", FailurePolicy: "Ignore", TimeoutSeconds: 5},
	}
	in.Workloads[rightsizing.Key("payments", "Deployment", "worker")] = w
	p, _ := BuildRightsizing(in)
	s := stepByName(p, "worker")
	if s == nil || s.Risk != RiskHigh || !contains(s.RiskReasons, RiskReasonAdmissionWebhook) {
		t.Fatalf("step = %+v", s)
	}
	if !strings.Contains(string(s.Evidence), `"webhooks":[{"name":"validate.payments.io","failurePolicy":"Fail","timeoutSeconds":10}]`) {
		t.Fatalf("evidence = %s", s.Evidence)
	}

	w.Webhooks = []WebhookFacts{{Name: "audit.payments.io", FailurePolicy: "Ignore", TimeoutSeconds: 5}}
	in.Workloads[rightsizing.Key("payments", "Deployment", "worker")] = w
	p, _ = BuildRightsizing(in)
	if s := stepByName(p, "worker"); contains(s.RiskReasons, RiskReasonAdmissionWebhook) {
		t.Fatalf("a webhook that fails open raised the risk: %v", s.RiskReasons)
	}
}

func TestBuildRightsizing_LowConfidenceIsHighRisk(t *testing.T) {
	in := fixture()
	in.Result.LowConfidence = true
	p, _ := BuildRightsizing(in)
	for _, s := range p.Steps {
		if s.Risk != RiskHigh {
			t.Errorf("%s risk %s with low confidence", s.Target.Name, s.Risk)
		}
	}
	if !p.Engine.LowConfidence {
		t.Error("the engine's low confidence is not on the plan")
	}
}

func TestBuildRightsizing_InsufficientHistoryBuildsNothing(t *testing.T) {
	in := fixture()
	in.Result = rightsizing.Result{InsufficientHistory: true, MinHistoryHours: 48}
	if _, err := BuildRightsizing(in); !errors.Is(err, ErrInsufficientHistory) {
		t.Fatalf("err = %v, want ErrInsufficientHistory", err)
	}
}

// A native sidecar runs beside the container and is in the P95.
func TestBuildRightsizing_ANativeSidecarIsAContainer(t *testing.T) {
	in := fixture()
	always := corev1.ContainerRestartPolicyAlways
	w := in.Workloads["payments/Deployment/api"]
	w.Template.InitContainers = []corev1.Container{{Name: "mesh", RestartPolicy: &always}}
	in.Workloads["payments/Deployment/api"] = w
	p, _ := BuildRightsizing(in)
	if stepByName(p, "api") != nil {
		t.Fatal("a pod with a native sidecar became a step")
	}
}

func TestBuildRightsizing_IsStable(t *testing.T) {
	a, _ := BuildRightsizing(fixture())
	b, _ := BuildRightsizing(fixture())
	if a.ContentHash != b.ContentHash {
		t.Fatal("the same input gave two content hashes")
	}
}

// Everything excluded is a plan with no steps, built without an error: the
// caller stores nothing and says why.
func TestBuildRightsizing_AllExcluded(t *testing.T) {
	in := fixture()
	in.Scope = Scope{Namespaces: []string{"kube-system", "shop"}}
	w := in.Workloads["shop/StatefulSet/db"]
	w.PodsStable = false
	in.Workloads["shop/StatefulSet/db"] = w
	p, err := BuildRightsizing(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Steps) != 0 || len(p.Excluded) != 4 {
		t.Fatalf("%d steps, %d excluded; want 0 and 4", len(p.Steps), len(p.Excluded))
	}
	if p.Impact.Estimated.Money == nil || p.Impact.Estimated.Money.Monthly != 0 {
		t.Errorf("money = %+v", p.Impact.Estimated.Money)
	}
}

// kyverno on kind-kubebolt-dev, 25-nov: four single-pod controllers freeing
// 90, 90, 80 and 70m at OpenCost's default rates in euros. Rounded apart, the
// steps showed 1.90 + 1.90 + 1.69 + 1.48 = 6.97 and the plan 6.96.
func TestBuildRightsizing_TheTotalIsTheStepsSum(t *testing.T) {
	in := fixture()
	in.Result.Recs = nil
	in.Workloads = map[string]WorkloadFacts{}
	for i, suggest := range []int64{10, 10, 20, 30} {
		name := []string{"cleanup", "background", "admission", "reports"}[i]
		in.Result.Recs = append(in.Result.Recs, rec("kyverno", "Deployment", name, over(100, suggest-2, suggest), ok(0, 0)))
		in.Workloads[rightsizing.Key("kyverno", "Deployment", name)] = facts("deployments", "kyverno", name, 1,
			specOf(container("c", "100m", "", "", "")))
	}
	in.Pricing = Pricing{Rates: costmodel.NodeRates{CPUCoreHourly: 0.031611, RAMGiBHourly: 0.004237},
		Factor: 1 / 1.0935, Currency: "eur", Source: "opencost"}
	p, err := BuildRightsizing(in)
	if err != nil {
		t.Fatal(err)
	}
	var steps float64
	for _, s := range p.Steps {
		steps += s.Impact.Estimated.Money.Monthly
	}
	if got := p.Impact.Estimated.Money.Monthly; got != costmodel.Round2(steps) || got != 6.97 {
		t.Fatalf("plan %v, steps sum %v; want both 6.97", got, steps)
	}
}
