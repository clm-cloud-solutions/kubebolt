package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/kobiplan"
)

// create_plan is the chat's: it stays when actions are off (it changes
// nothing, P12) and never reaches the catalogue the MCP doors serve.
func TestCreatePlan_IsAChatOnlyTool(t *testing.T) {
	for _, name := range PlanToolNames() {
		has := func(defs []ToolDefinition) bool {
			for _, d := range defs {
				if d.Name == name {
					return true
				}
			}
			return false
		}
		if !has(ChatToolDefinitions(false, false)) {
			t.Errorf("%s is gone from the chat with actions off", name)
		}
		if has(GovernedToolDefinitions(true, true)) || has(ToolDefinitions()) {
			t.Errorf("%s is in the catalogue the MCP server and Autopilot draw from", name)
		}
	}
}

// revise_plan passes on the person's values and says what the plan did with
// them; the card gets the plan, the model never its id.
func TestRevisePlan(t *testing.T) {
	p := createdPlan()
	src := &fakePlanSource{revised: ReviseStepOutcome{Plan: &p, Order: 1, Changed: true}}
	m, isErr := runPlanToolNamed(t, src, "revise_plan", `{"workload":"kyverno/deployment/cleanup","requests":{"cpu":"30m"}}`)
	if isErr || m["outcome"] != "revised" || m["note"] != planNoteRevised || m["step"] != float64(1) {
		t.Fatalf("summary = %v", m)
	}
	if src.gotRevise.Workload != "kyverno/deployment/cleanup" || src.gotRevise.Requests["cpu"] != "30m" || len(src.gotRevise.Limits) != 0 {
		t.Fatalf("request = %+v", src.gotRevise)
	}
	if strings.Contains(fmt.Sprint(m), p.ID) {
		t.Fatal("the plan's id reached the model")
	}

	src.revised = ReviseStepOutcome{Plan: &p, Order: 1, Refused: "cpu request 5m is under the 7m per pod the engine measured as the P95; the step keeps 10m"}
	m, _ = runPlanToolNamed(t, src, "revise_plan", `{"workload":"kyverno/cleanup","requests":{"cpu":"5m"}}`)
	if m["outcome"] != "refused" || !strings.Contains(m["reason"].(string), "under the 7m per pod") {
		t.Fatalf("refused = %v", m)
	}

	// No value the person gave: nothing to send.
	if _, isErr := runPlanToolNamed(t, src, "revise_plan", `{"workload":"kyverno/cleanup"}`); !isErr {
		t.Fatal("a revision without values was sent")
	}
}

type fakePlanSource struct {
	out       CreatePlanOutcome
	err       error
	got       CreatePlanRequest
	revised   ReviseStepOutcome
	gotRevise ReviseStepRequest
	read      *kobiplan.Plan
	readErr   error
}

func (f *fakePlanSource) ReadPlan(_ context.Context, id string) (*kobiplan.Plan, error) {
	if f.read == nil || f.read.ID != id {
		return nil, ErrPlanNotFound
	}
	return f.read, f.readErr
}

func (f *fakePlanSource) ReviseStep(_ context.Context, req ReviseStepRequest) (ReviseStepOutcome, error) {
	f.gotRevise = req
	return f.revised, f.err
}

func (f *fakePlanSource) MatchStep(context.Context, string, string, string, []map[string]interface{}) *PlanStepMatch {
	return nil
}

func (f *fakePlanSource) CreatePlan(_ context.Context, req CreatePlanRequest) (CreatePlanOutcome, error) {
	f.got = req
	return f.out, f.err
}

func createdPlan() kobiplan.Plan {
	p := testPlan("org-1", "pln_1", "c-uid")
	p.Steps[0].Params = json.RawMessage(`{"containers":[{"container":"api","requests":{"cpu":"120m","memory":"300Mi"}}]}`)
	p.Steps[0].Before = json.RawMessage(`{"containers":[{"container":"api","requests":{"cpu":"500m","memory":"1Gi"},"limits":{"cpu":"1"}}]}`)
	p.Steps[0].Target.Owner = &kobiplan.Owner{Kind: kobiplan.OwnerHelm, Ref: "payments"}
	p.Steps[0].Evidence = json.RawMessage(`{"cpu":{"state":"over","request":1500,"p95":300,"suggest":360},"mem":{"state":"over","p95":786432000},"pods":3}`)
	p.Steps[0].Risk = kobiplan.RiskMedium
	p.Steps[0].RiskReasons = []string{"lowers_memory_request"}
	p.Steps[0].Impact.Estimated = kobiplan.Savings{CPUMilli: 1140, MemBytes: 2172 * 1024 * 1024,
		Money: &kobiplan.Money{Monthly: 41.2, Currency: "eur", Source: "opencost"}}
	p.Impact.Estimated = p.Steps[0].Impact.Estimated
	p.Excluded = []kobiplan.Exclusion{{
		Target: kobiplan.Target{Type: "daemonsets", Namespace: "kube-system", Name: "azure-cns"},
		Reason: kobiplan.ReasonProviderManaged, Detail: "kubernetes.azure.com/managedby",
	}}
	return p
}

func runPlanTool(t *testing.T, src PlanSource, input string) (map[string]interface{}, bool) {
	t.Helper()
	e := NewExecutor(nil).WithPlans(src)
	content, isErr, _ := e.runCreatePlan(context.Background(), parseArgs(json.RawMessage(input)))
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(content), &m); err != nil {
		t.Fatalf("not JSON: %s", content)
	}
	return m, isErr
}

func runPlanToolNamed(t *testing.T, src PlanSource, name, input string) (map[string]interface{}, bool) {
	t.Helper()
	e := NewExecutor(nil).WithPlans(src)
	var content string
	var isErr bool
	if name == "revise_plan" {
		content, isErr, _ = e.runRevisePlan(context.Background(), parseArgs(json.RawMessage(input)))
	} else {
		content, isErr, _ = e.runCreatePlan(context.Background(), parseArgs(json.RawMessage(input)))
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(content), &m); err != nil {
		t.Fatalf("not JSON: %s", content)
	}
	return m, isErr
}

func TestCreatePlan_Created(t *testing.T) {
	p := createdPlan()
	src := &fakePlanSource{out: CreatePlanOutcome{Created: &p}}
	m, isErr := runPlanTool(t, src, `{"kind":"rightsizing","namespaces":["payments"," "],"workloads":["shop/api"],"title":" Ahorro "}`)
	if isErr {
		t.Fatalf("error: %v", m)
	}
	if src.got.Kind != kobiplan.KindRightsizing || len(src.got.Namespaces) != 1 || src.got.Namespaces[0] != "payments" ||
		len(src.got.Workloads) != 1 || src.got.Title != "Ahorro" {
		t.Errorf("request = %+v", src.got)
	}
	if m["outcome"] != "created" || m["title"] == nil || m["note"] != planNoteCreated {
		t.Errorf("summary = %v", m)
	}
	// Kobi put a plan id and a step id in an action card: neither reaches it.
	if _, ok := m["planId"]; ok {
		t.Error("the plan's id reached Kobi")
	}
	step := m["steps"].([]interface{})[0].(map[string]interface{})
	if step["change"] != "cpu request 500m → 120m; memory request 1Gi → 300Mi" {
		t.Errorf("change = %v", step["change"])
	}
	if _, ok := step["id"]; ok {
		t.Error("a step's id reached Kobi")
	}
	if step["usage"] != "P95 over the plan's history, whole workload (3 pods): cpu 300m, memory 750Mi" {
		t.Errorf("usage = %v", step["usage"])
	}
	if step["current"] != "cpu request 500m, cpu limit 1; memory request 1Gi, no memory limit" {
		t.Errorf("current = %v", step["current"])
	}
	if step["owner"] != "helm:payments" || step["monthly"] != "41.20 EUR/month" || step["frees"] != "1140m CPU, 2172Mi memory" {
		t.Errorf("step = %v", step)
	}
	excluded := m["excluded"].(map[string]interface{})
	if excluded["byReason"].(map[string]interface{})["provider_managed"] != float64(1) {
		t.Errorf("excluded = %v", excluded)
	}
	if ex := excluded["examples"].([]interface{}); !strings.Contains(ex[0].(string), "kube-system/azure-cns: provider_managed (kubernetes.azure.com/managedby)") {
		t.Errorf("examples = %v", ex)
	}
}

func TestCreatePlan_ExistingLivePlan(t *testing.T) {
	p := createdPlan()
	m, isErr := runPlanTool(t, &fakePlanSource{out: CreatePlanOutcome{Existing: &p, ExistingReason: ExistingOnePerCluster}}, `{"kind":"rightsizing"}`)
	if isErr || m["outcome"] != "existing_live_plan" || m["title"] == nil || m["note"] != planNoteExisting {
		t.Fatalf("summary = %v", m)
	}
	// The same request again says so, not Free's limit.
	m, _ = runPlanTool(t, &fakePlanSource{out: CreatePlanOutcome{Existing: &p, ExistingReason: ExistingSameScope}}, `{"kind":"rightsizing"}`)
	if m["outcome"] != "existing_live_plan" || m["note"] != planNoteSameScope {
		t.Fatalf("same scope = %v", m)
	}
	same := NewExecutor(nil).WithPlans(&fakePlanSource{out: CreatePlanOutcome{Existing: &p, ExistingReason: ExistingSameScope}})
	if _, _, ref := same.runCreatePlan(context.Background(), parseArgs(json.RawMessage(`{"kind":"rightsizing"}`))); ref == nil || ref.Outcome != "existing_same_scope" {
		t.Fatalf("ref = %+v", ref)
	}
}

func TestCreatePlan_NothingToPlan(t *testing.T) {
	p := createdPlan()
	p.Steps = []kobiplan.Step{}
	p.Impact.Estimated = kobiplan.Savings{MoneyUnavailable: kobiplan.MoneyNoCostData}
	m, isErr := runPlanTool(t, &fakePlanSource{out: CreatePlanOutcome{Empty: &p}}, `{"kind":"rightsizing"}`)
	if isErr || m["outcome"] != "nothing_to_plan" {
		t.Fatalf("summary = %v", m)
	}
	if _, ok := m["planId"]; ok {
		t.Error("a plan that was not stored has an id in the answer")
	}
	if m["estimated"].(map[string]interface{})["moneyUnavailable"] == nil {
		t.Error("no word on the missing money")
	}
}

func TestCreatePlan_Refusals(t *testing.T) {
	if m, isErr := runPlanTool(t, nil, `{"kind":"rightsizing"}`); !isErr || m["error"] == nil {
		t.Errorf("without a source: %v", m)
	}
	src := &fakePlanSource{}
	if m, isErr := runPlanTool(t, src, `{"kind":"cleanup"}`); !isErr || !strings.Contains(m["error"].(string), "the kinds are rightsizing and consolidation") {
		t.Errorf("unknown kind: %v", m)
	}
	src.err = errors.New("metrics storage unreachable")
	if m, isErr := runPlanTool(t, src, `{"kind":"rightsizing"}`); !isErr || !strings.Contains(m["error"].(string), "metrics storage unreachable") {
		t.Errorf("source error: %v", m)
	}
}

func TestUnitsLabel_NamesOnlyWhatIsFreed(t *testing.T) {
	cases := map[string][2]int64{
		"330m CPU":              {330, 0},
		"120Mi memory":          {0, 120 * 1024 * 1024},
		"90m CPU, 120Mi memory": {90, 120 * 1024 * 1024},
		"nothing (a raised limit frees no capacity)": {0, 0},
	}
	for want, in := range cases {
		if got := unitsLabel(in[0], in[1]); got != want {
			t.Errorf("unitsLabel(%d, %d) = %q, want %q", in[0], in[1], got, want)
		}
	}
}

// The chat's plan card finds its plan through the result's Plan, which the
// model never reads; the content stays without ids.
func TestCreatePlan_TheCardGetsTheIDTheModelDoesNot(t *testing.T) {
	p := createdPlan()
	e := NewExecutor(nil).WithPlans(&fakePlanSource{out: CreatePlanOutcome{Created: &p}})
	content, _, ref := e.runCreatePlan(context.Background(), parseArgs(json.RawMessage(`{"kind":"rightsizing"}`)))
	if ref == nil || ref.ID != "pln_1" || ref.Outcome != "created" {
		t.Fatalf("ref = %+v, want pln_1 created", ref)
	}
	if strings.Contains(content, "pln_1") {
		t.Error("the plan id reached the model's content")
	}

	existing := NewExecutor(nil).WithPlans(&fakePlanSource{out: CreatePlanOutcome{Existing: &p}})
	if _, _, ref := existing.runCreatePlan(context.Background(), parseArgs(json.RawMessage(`{"kind":"rightsizing"}`))); ref == nil || ref.Outcome != "existing_live_plan" {
		t.Errorf("existing ref = %+v", ref)
	}
	empty := NewExecutor(nil).WithPlans(&fakePlanSource{out: CreatePlanOutcome{Empty: &p}})
	if _, _, ref := empty.runCreatePlan(context.Background(), parseArgs(json.RawMessage(`{"kind":"rightsizing"}`))); ref != nil {
		t.Errorf("a plan that was not stored got a card: %+v", ref)
	}
}

// The evidence the webhook and QoS risks rest on reaches Kobi in words.
func TestSummarizePlan_SaysWhatTheRisksRestOn(t *testing.T) {
	p := createdPlan()
	p.Steps[0].Evidence = json.RawMessage(`{"pods":1,"webhooks":[{"name":"validate.x","failurePolicy":"Fail","timeoutSeconds":10},{"name":"mutate.x","failurePolicy":"Fail","timeoutSeconds":5}],` +
		`"qos":{"before":"Guaranteed","after":"Burstable","keepWith":{"cpu":"80m"}}}`)
	row := summarizePlan(p)["steps"].([]map[string]interface{})[0]
	if row["webhooks"] != "serves 2 admission webhooks that fail closed (5 s timeout): validate.x, mutate.x" {
		t.Fatalf("webhooks = %v", row["webhooks"])
	}
	if row["qos"] != "Guaranteed → Burstable; a cpu request of 80m too keeps it Guaranteed" {
		t.Fatalf("qos = %v", row["qos"])
	}
}

// A consolidation step reaches Kobi in words: the pool, the nodes that go and
// why, the autoscaler, and the money as the nodes' list price, never a
// saving; the dependency on a right-sizing plan is a sentence, not an id.
func TestSummarizePlan_Consolidation(t *testing.T) {
	ev, _ := json.Marshal(kobiplan.ConsolidationEvidence{Scenario: kobiplan.ScenarioPlanned, RemovableToday: 0, Ceiling: 0.8, MinNodes: 3,
		Blocked: []kobiplan.NodeBlock{{Node: "ip-4", Reason: kobiplan.BlockPDB, Pod: "etl/loader-0", Owner: "etl/loader"}}})
	params, _ := json.Marshal(kobiplan.RemoveNodesParams{Pool: "general", PoolSource: kobiplan.PoolEKS, Autoscaler: kobiplan.AutoscalerNone, NodesBefore: 5, NodesAfter: 4,
		Nodes: []kobiplan.RemovedNode{{Name: "ip-10-0-3-41", Zone: "us-east-1c", InstanceType: "m6i.xlarge"}}})
	p := kobiplan.Plan{
		Kind: kobiplan.KindConsolidation, Title: "Consolidation", DependsOn: []string{"pln_rs"},
		Impact: kobiplan.Impact{Estimated: kobiplan.Savings{CPUMilli: 4000, MemBytes: 16 << 30, Money: &kobiplan.Money{Monthly: 129.4, Currency: "eur"}}},
		Steps: []kobiplan.Step{{
			Action: kobiplan.ActionRemoveNodes, Target: kobiplan.Target{Type: kobiplan.NodePoolType, Name: "general", Owner: &kobiplan.Owner{Kind: kobiplan.OwnerUnknown}},
			Params: params, Evidence: ev, Risk: kobiplan.RiskMedium, RiskReasons: []string{kobiplan.RiskReasonLessHeadroom, kobiplan.RiskReasonDependsOnPlan},
			Status: kobiplan.StepPending, Impact: kobiplan.Impact{Estimated: kobiplan.Savings{CPUMilli: 4000, MemBytes: 16 << 30, Money: &kobiplan.Money{Monthly: 129.4, Currency: "eur"}}},
		}},
		Excluded: []kobiplan.Exclusion{{Target: kobiplan.Target{Type: kobiplan.NodePoolType, Name: "batch"}, Reason: kobiplan.ReasonPoolBlocked, Detail: "the PodDisruptionBudget etl/loader allows no disruption now"}},
	}
	m := summarizePlan(p)
	b, _ := json.Marshal(m)
	text := string(b)
	for _, want := range []string{
		`"change":"5 → 4 nodes: removes ip-10-0-3-41 (us-east-1c, m6i.xlarge)"`,
		`"monthly":"129.40 EUR/month"`,
		`"capacityRemoved":"4000m CPU, 16384Mi memory"`,
		`today's requests free 0`,
		`no autoscaler`,
		`"dependsOn":"Computed on the requests the live right-sizing plan leaves`,
		`"monthlyNote":"The list price of the nodes the plan suggests removing`,
		`"batch: pool_blocked (the PodDisruptionBudget etl/loader allows no disruption now)"`,
		`the PodDisruptionBudget etl/loader allows no disruption now, so etl/loader-0 keeps ip-4`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %s in %s", want, text)
		}
	}
	if strings.Contains(text, "pln_rs") || strings.Contains(text, `"frees"`) {
		t.Errorf("an id or a «frees» reached Kobi: %s", text)
	}
}

// The plan the person is looking at is read through its page's address
// (get_resource_detail with type plans), with the summary create_plan gives
// and no id; another cluster's plan says so; outside the chat, nothing
// changes.
func TestReadPlan_ThroughItsPage(t *testing.T) {
	p := kobiplan.Plan{ID: "pln_x", Kind: kobiplan.KindConsolidation, Title: "Consolidation", Status: kobiplan.StatusExported}
	src := &fakePlanSource{read: &p}
	e := NewExecutor(nil).WithPlans(src)
	call := func(input string) ToolResult {
		return e.executeCtx(context.Background(), ToolCall{ID: "c", Name: "get_resource_detail", Input: json.RawMessage(input)})
	}
	got := call(`{"type":"plans","namespace":"_","name":"pln_x"}`)
	if got.IsError || !strings.Contains(got.Content, `"title":"Consolidation"`) || !strings.Contains(got.Content, "kobi-plans procedure") || strings.Contains(got.Content, "pln_x") {
		t.Fatalf("read = %v %s", got.IsError, got.Content)
	}
	src.readErr = ErrPlanOtherCluster
	if got := call(`{"type":"plans","namespace":"_","name":"pln_x"}`); !got.IsError || !strings.Contains(got.Content, "another cluster") {
		t.Fatalf("other cluster = %s", got.Content)
	}
	if got := call(`{"type":"plans","namespace":"_","name":"pln_nope"}`); !got.IsError || !strings.Contains(got.Content, "no such plan") {
		t.Fatalf("missing = %s", got.Content)
	}
	if got := NewExecutor(nil).executeCtx(context.Background(), ToolCall{ID: "c", Name: "get_resource_detail", Input: json.RawMessage(`{"type":"plans","namespace":"_","name":"pln_x"}`)}); strings.Contains(got.Content, "Consolidation") {
		t.Fatal("an executor without the chat's plans read a plan")
	}
}
