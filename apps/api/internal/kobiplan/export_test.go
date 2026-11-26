package kobiplan

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// kyvernoValues mimics the Kyverno chart: two components with the same
// default resources, each under a key that names it.
func kyvernoValues() map[string]any {
	res := func() map[string]any {
		return map[string]any{
			"requests": map[string]any{"cpu": "100m", "memory": "128Mi"},
			"limits":   map[string]any{"memory": "384Mi"},
		}
	}
	return map[string]any{
		"admissionController": map[string]any{
			"replicas":  nil,
			"container": map[string]any{"image": map[string]any{"tag": "v1"}, "resources": res()},
		},
		"cleanupController": map[string]any{"resources": res()},
		"reportsController": map[string]any{"resources": map[string]any{
			"requests": map[string]any{"cpu": "100m", "memory": "64Mi"},
			"limits":   map[string]any{"memory": "128Mi"},
		}},
		"webhooksCleanup": map[string]any{"resources": map[string]any{}},
	}
}

func live(reqCPU, reqMem, limMem string) ContainerResources {
	c := ContainerResources{Container: "c", Requests: &Resources{}, Limits: &Resources{}}
	if reqCPU != "" {
		c.Requests.CPU = ptr(reqCPU)
	}
	if reqMem != "" {
		c.Requests.Memory = ptr(reqMem)
	}
	if limMem != "" {
		c.Limits.Memory = ptr(limMem)
	}
	return c
}

func TestFindResourcesPath(t *testing.T) {
	v := kyvernoValues()
	cases := []struct {
		name  string
		live  ContainerResources
		names []string
		want  string
	}{
		// Two blocks match; the one the workload's name names wins.
		{"admission", live("100m", "128Mi", "384Mi"), []string{"kyverno-admission-controller", "kyverno"}, "admissionController.container.resources"},
		{"cleanup", live("100m", "128Mi", "384Mi"), []string{"kyverno-cleanup-controller", "controller"}, "cleanupController.resources"},
		// One block matches: no name needed. Quantities compare by value.
		{"reports", live("0.1", "64Mi", "128Mi"), []string{"x"}, "reportsController.resources"},
		// Two match and the name names neither: no answer.
		{"tie", live("100m", "128Mi", "384Mi"), []string{"something-else"}, ""},
		// Nothing matches.
		{"none", live("250m", "128Mi", "384Mi"), []string{"kyverno-admission-controller"}, ""},
		// No resources set: an empty block would match anything.
		{"empty", ContainerResources{Container: "c"}, []string{"webhooks-cleanup"}, ""},
	}
	for _, c := range cases {
		path, ok := FindResourcesPath(v, c.live, c.names...)
		got := strings.Join(path, ".")
		if (c.want != "") != ok || got != c.want {
			t.Errorf("%s: path = %q (%v), want %q", c.name, got, ok, c.want)
		}
	}
}

func TestValuesPatch_OnlyTheChangedValues(t *testing.T) {
	patch := ValuesPatch([]string{"admissionController", "container", "resources"},
		ContainerResources{Container: "kyverno", Requests: &Resources{CPU: ptr("20m")}})
	mergePatch(patch, ValuesPatch([]string{"cleanupController", "resources"},
		ContainerResources{Container: "controller", Requests: &Resources{Memory: ptr("64Mi")}}))
	want := map[string]any{
		"admissionController": map[string]any{"container": map[string]any{"resources": map[string]any{"requests": map[string]any{"cpu": "20m"}}}},
		"cleanupController":   map[string]any{"resources": map[string]any{"requests": map[string]any{"memory": "64Mi"}}},
	}
	if !reflect.DeepEqual(patch, want) {
		t.Fatalf("patch = %v", patch)
	}
}

// exportPlan has one step per owner kind: two on a Helm release, one nobody
// was identified as owning, one Argo CD owns.
func exportPlan() Plan {
	p := examplePlan()
	helmStep := func(id string, order int, name, container string) Step {
		return Step{
			ID: id, Order: order, Action: ActionSetResources,
			Target:       Target{Type: "deployments", Namespace: "kyverno", Name: name, Owner: &Owner{Kind: OwnerHelm, Ref: "kyverno"}},
			Params:       json.RawMessage(`{"containers":[{"container":"` + container + `","requests":{"cpu":"20m"}}]}`),
			Before:       json.RawMessage(`{"containers":[{"container":"` + container + `","requests":{"cpu":"100m","memory":"128Mi"},"limits":{"memory":"384Mi"}}]}`),
			Precondition: Precondition{SpecHash: "sha256:h" + id},
			Verification: Verification{Window: "24h", Checks: []string{"rollout_complete"}},
			Rollback:     &Rollback{Action: ActionSetResources, Params: json.RawMessage(`{"containers":[{"container":"` + container + `","requests":{"cpu":"100m"}}]}`)},
			Risk:         RiskMedium, RiskReasons: []string{"single_pod"},
			Impact: Impact{Estimated: Savings{CPUMilli: 80}},
			Status: StepPending,
		}
	}
	gitops := p.Steps[0]
	gitops.ID, gitops.Order = "s4", 4
	gitops.Target = Target{Type: "deployments", Namespace: "shop", Name: "web", Owner: &Owner{Kind: OwnerGitOps, Ref: "argocd:shop"}}
	p.Steps = append(p.Steps,
		helmStep("s2", 2, "kyverno-admission-controller", "kyverno"),
		helmStep("s3", 3, "kyverno-cleanup-controller", "controller"),
		gitops,
	)
	p.ContentHash = ContentHash(p)
	return p
}

func render(t *testing.T, format ExportFormat, releases map[string]*ReleaseValues) Rendered {
	t.Helper()
	r, err := RenderExport(ExportInput{
		Plan: exportPlan(), Format: format, ClusterName: "kind-kubebolt-dev", By: "Admin",
		At: t0.Add(time.Hour), Releases: releases,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRenderExport_Commands(t *testing.T) {
	r := render(t, ExportCommands, map[string]*ReleaseValues{
		"kyverno/kyverno": {Chart: "kyverno", ChartVersion: "3.8.2", Revision: 3, Values: kyvernoValues()},
	})
	if r.Filename != "kobi-plan-pln_01JDQ7Z3K8M2V5-commands.sh" {
		t.Errorf("filename = %q", r.Filename)
	}
	for _, want := range []string{
		`# Kobi Plan "Right-sizing de payments" · version 1`,
		"# Cluster kind-kubebolt-dev · exported 2026-11-20 11:00 UTC by Admin",
		// Suggestions, the decision stays with the person.
		"# These are KubeBolt's suggestions, not instructions: whether and how to apply each one is your decision.",
		// The plan has a price: what it is worth, and when it is a saving.
		"# What a step frees is priced at list prices; it lowers the bill only once nodes or disks are removed.",
		// Nobody identified: kubectl, the warning, the rollback under it.
		"kubectl -n payments set resources deployment/api -c api --requests=cpu=120m,memory=300Mi",
		"# Nobody was identified as its owner",
		"# kubectl -n payments set resources deployment/api -c api --requests=cpu=500m,memory=1Gi",
		// Helm: both steps in one values file at the paths the values hold,
		// previewed with helm diff, applied with --reuse-values, rolled back
		// with helm rollback. Never kubectl.
		"# Helm release kyverno (namespace kyverno) · chart kyverno 3.8.2",
		"(values: admissionController.container.resources)",
		"(values: cleanupController.resources)",
		"cat > kyverno-values-patch.yaml <<'EOF'\nadmissionController:\n  container:\n    resources:\n      requests:\n        cpu: 20m\ncleanupController:\n  resources:\n    requests:\n      cpu: 20m\nEOF",
		`: "${CHART_KYVERNO:?set CHART_KYVERNO to the chart of release kyverno}"`,
		`helm diff upgrade kyverno "$CHART_KYVERNO" --version 3.8.2 -n kyverno --reuse-values -f kyverno-values-patch.yaml`,
		`helm upgrade kyverno "$CHART_KYVERNO" --version 3.8.2 -n kyverno --reuse-values -f kyverno-values-patch.yaml`,
		"# Roll back: helm rollback kyverno -n kyverno",
		"# 3 · kyverno / kyverno-cleanup-controller (deployment)",
		// GitOps: the change in words, nothing to run.
		"# 4 · shop / web (deployment) — Argo CD (application shop)",
		"# Make this change in the repository",
	} {
		if !strings.Contains(r.Text, want) {
			t.Errorf("commands miss %q\n%s", want, r.Text)
		}
	}
	if strings.Contains(r.Text, "set resources deployment/kyverno") || strings.Contains(r.Text, "deployment/web") {
		t.Errorf("a Helm or GitOps step got a kubectl command:\n%s", r.Text)
	}
	notes := strings.Join(r.Notes, "\n")
	for _, want := range []string{"1 step has no identified owner", "CHART_", "1 step is managed by Argo CD or Flux"} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes miss %q: %v", want, r.Notes)
		}
	}
}

// Without the release's values, a Helm change is said in words: no values
// file at a guessed path.
func TestRenderExport_HelmWithoutValues(t *testing.T) {
	r := render(t, ExportCommands, nil)
	if strings.Contains(r.Text, "helm upgrade kyverno") || strings.Contains(r.Text, "cat > ") {
		t.Fatalf("rendered a values file without the values:\n%s", r.Text)
	}
	if !strings.Contains(r.Text, "# Not placed (the release's values could not be read)") ||
		!strings.Contains(r.Text, "#   kyverno-admission-controller, container kyverno: cpu request 100m → 20m") {
		t.Fatalf("the unplaced changes are not said:\n%s", r.Text)
	}
	if !strings.Contains(strings.Join(r.Notes, " "), "2 Helm changes could not be placed") {
		t.Fatalf("notes = %v", r.Notes)
	}
}

func TestRenderExport_Diff(t *testing.T) {
	r := render(t, ExportDiff, map[string]*ReleaseValues{
		"kyverno/kyverno": {Chart: "kyverno", ChartVersion: "3.8.2", Values: kyvernoValues()},
	})
	if !strings.HasSuffix(r.Filename, "-diff.yaml") {
		t.Errorf("filename = %q", r.Filename)
	}
	for _, want := range []string{
		"# Strategic merge patch on deployment/api:\n---\nspec:\n  template:\n    spec:\n      containers:\n      - name: api\n        resources:\n          requests:\n            cpu: 120m\n            memory: 300Mi",
		"# Values patch for release kyverno (kyverno-values-patch.yaml):\n---\nadmissionController:",
	} {
		if !strings.Contains(r.Text, want) {
			t.Errorf("diff misses %q\n%s", want, r.Text)
		}
	}
	if strings.Contains(r.Text, "kubectl -n") || strings.Contains(r.Text, "helm upgrade kyverno") {
		t.Errorf("a diff carries commands:\n%s", r.Text)
	}
}

func TestRenderExport_InitContainerIsPatched(t *testing.T) {
	p := examplePlan()
	p.Steps[0].Params = json.RawMessage(`{"containers":[{"container":"migrate","initContainer":true,"requests":{"cpu":"50m"}}]}`)
	r, err := RenderExport(ExportInput{Plan: p, Format: ExportCommands, At: t0})
	if err != nil {
		t.Fatal(err)
	}
	want := `kubectl -n payments patch deployment/api --type=strategic -p '{"spec":{"template":{"spec":{"initContainers":[{"name":"migrate","resources":{"requests":{"cpu":"50m"}}}]}}}}'`
	if !strings.Contains(r.Text, want) {
		t.Fatalf("missing %q\n%s", want, r.Text)
	}
}

// The first export makes the plan exported and keeps its baseline; a later
// one only adds its record (P20).
func TestRecordExport(t *testing.T) {
	p := examplePlan()
	at := t0.Add(time.Hour)
	first := &Baseline{CapturedAt: at, WindowDays: 7, ClusterCost: &Money{Monthly: 900, Currency: "eur", Source: "opencost"}}
	p.RecordExport(ExportCommands, "Admin", at, first, map[string]*Baseline{
		"s1": {CapturedAt: at, WindowDays: 7, AllocatedCost: &Money{Monthly: 30, Currency: "eur", Source: "opencost"}},
	})
	if p.Status != StatusExported || p.ExportedAt == nil || !p.ExportedAt.Equal(at) || len(p.Exports) != 1 {
		t.Fatalf("after the first export: %+v", p)
	}
	if p.Baseline != first || p.Steps[0].Baseline == nil || p.Steps[0].Baseline.AllocatedCost.Monthly != 30 {
		t.Fatalf("baselines not kept: %+v / %+v", p.Baseline, p.Steps[0].Baseline)
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	later := at.Add(time.Hour)
	p.RecordExport(ExportDiff, "Ana", later, &Baseline{CapturedAt: later, WindowDays: 7}, nil)
	if len(p.Exports) != 2 || !p.ExportedAt.Equal(at) || p.Baseline != first {
		t.Fatalf("a second export moved the first's record: %+v", p)
	}
	if p.ContentHash != ContentHash(p) {
		t.Fatal("recording an export changed what the plan proposes")
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestExportable(t *testing.T) {
	p := examplePlan()
	if err := p.Exportable(); err != nil {
		t.Fatal(err)
	}
	p.Status = StatusExpired
	if p.Exportable() == nil {
		t.Fatal("an expired plan is exportable")
	}
}
