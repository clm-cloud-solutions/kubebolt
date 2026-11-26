package kobiplan

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

// examplePlan is the spec's example (Kobi Plans v0, «Esquema JSON»): one
// right-sizing step on payments/api and the plan around it, as a draft.
func examplePlan() Plan {
	params := json.RawMessage(`{"containers":[{"container":"api","requests":{"cpu":"120m","memory":"300Mi"}}]}`)
	p := Plan{
		ID: "pln_01JDQ7Z3K8M2V5", OrgID: "org-1", ClusterID: "c-uid", Title: "Right-sizing de payments",
		Kind:      KindRightsizing,
		Scope:     Scope{Namespaces: []string{"payments"}, Workloads: []string{}},
		Version:   1,
		Status:    StatusDraft,
		CreatedAt: t0,
		Source:    Source{Initiator: InitiatorChat, UserID: "u-1", ConversationID: "conv-1"},
		ExpiresAt: t0.Add(7 * 24 * time.Hour),
		Exports:   []Export{},
		Engine: Engine{Name: "rightsizing", Percentile: 0.95, WindowDays: 6.9, MinHistoryHours: 48,
			ComputedAt: t0.Add(-2 * time.Minute)},
		Impact: Impact{Estimated: Savings{CPUMilli: 1140, MemBytes: 2277507072,
			Money: &Money{Monthly: 41.2, Currency: "eur", Source: "opencost"}}},
		Steps: []Step{{
			ID: "s1", Order: 1, Action: ActionSetResources,
			Target: Target{Type: "deployments", Namespace: "payments", Name: "api",
				Owner: &Owner{Kind: OwnerUnknown}},
			Params:       params,
			Before:       json.RawMessage(`{"containers":[{"container":"api","requests":{"cpu":"500m","memory":"1Gi"},"limits":{"cpu":"1","memory":"2Gi"}}]}`),
			Evidence:     json.RawMessage(`{"cpu":{"state":"over","request":1500,"p95":300,"suggest":360},"pods":3,"podsStableInWindow":true}`),
			Precondition: Precondition{SpecHash: "sha256:abc", Generation: 42},
			Verification: Verification{Window: "24h", Checks: []string{"rollout_complete", "no_oom_killed"}},
			Rollback:     &Rollback{Action: ActionSetResources, Params: json.RawMessage(`{"containers":[{"container":"api","requests":{"cpu":"500m","memory":"1Gi"}}]}`)},
			Risk:         RiskLow, RiskReasons: []string{},
			Impact:     Impact{Estimated: Savings{CPUMilli: 1140, MemBytes: 2277507072}},
			Provenance: "rightsizing · P95 7 d · ×1,2 · 3 pods",
			Status:     StepPending,
		}},
		Excluded: []Exclusion{{
			Target: Target{Type: "deployments", Namespace: "kube-system", Name: "coredns",
				Owner: &Owner{Kind: OwnerProvider, Ref: "eks"}},
			Reason: ReasonProviderManaged, Detail: "eks.amazonaws.com/component",
		}},
		Notes: []Note{},
	}
	p.ContentHash = ContentHash(p)
	return p
}

func TestValidate_TheSpecExampleIsValid(t *testing.T) {
	p := examplePlan()
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
}

// The doors of the v0 schema are in the JSON from the first plan, even with a
// single value: kind, source, programId, exports, impact.{estimated,observed},
// baseline, and each step's owner, exportOnly and impact.
func TestJSON_TheDoorsAreInTheShape(t *testing.T) {
	b, err := json.Marshal(examplePlan())
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"kind", "source", "programId", "exports", "impact", "baseline", "steps", "excluded", "contentHash"} {
		if _, ok := m[key]; !ok {
			t.Errorf("plan JSON has no %q", key)
		}
	}
	for _, gone := range []string{"domains", "createdBy", "savings"} {
		if _, ok := m[gone]; ok {
			t.Errorf("plan JSON still has %q, replaced by the 25-nov schema", gone)
		}
	}
	if string(m["programId"]) != "null" || string(m["baseline"]) != "null" || string(m["exports"]) != "[]" {
		t.Errorf("programId=%s baseline=%s exports=%s, want null/null/[]", m["programId"], m["baseline"], m["exports"])
	}
	var impact map[string]json.RawMessage
	_ = json.Unmarshal(m["impact"], &impact)
	if _, ok := impact["estimated"]; !ok || string(impact["observed"]) != "null" {
		t.Errorf("impact = %s, want estimated and observed null", m["impact"])
	}
	var steps []map[string]json.RawMessage
	_ = json.Unmarshal(m["steps"], &steps)
	for _, key := range []string{"action", "target", "exportOnly", "params", "precondition", "impact", "baseline", "status"} {
		if _, ok := steps[0][key]; !ok {
			t.Errorf("step JSON has no %q", key)
		}
	}
	if !strings.Contains(string(steps[0]["target"]), `"owner":{"kind":"unknown"}`) {
		t.Errorf("step target = %s, want the owner", steps[0]["target"])
	}
}

func TestJSON_RoundTrip(t *testing.T) {
	p := examplePlan()
	b, _ := json.Marshal(p)
	var back Plan
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if err := back.Validate(); err != nil {
		t.Fatalf("a plan read back from JSON is invalid: %v", err)
	}
	if ContentHash(back) != p.ContentHash {
		t.Fatal("the content hash moved through JSON")
	}
}

func TestValidate_Refuses(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Plan)
		want   string
	}{
		{"unknown kind", func(p *Plan) { p.Kind = "cleanup" }, `unknown kind "cleanup"`},
		{"version zero", func(p *Plan) { p.Version = 0 }, "versions start at 1"},
		{"chat without conversation", func(p *Plan) { p.Source.ConversationID = "" }, "needs its user and conversation"},
		{"mandate without mandate", func(p *Plan) { p.Source = Source{Initiator: InitiatorMandate} }, "needs the mandate"},
		{"unknown initiator", func(p *Plan) { p.Source.Initiator = "cron" }, `unknown initiator "cron"`},
		{"expired without a reason", func(p *Plan) { p.Status = StatusExpired }, "expired with reason"},
		{"a reason on a live plan", func(p *Plan) { p.ExpiredReason = ExpiredDiscarded }, "an expired reason on a draft plan"},
		{"exported without an export", func(p *Plan) { p.Status = StatusExported }, "exported without an export"},
		{"a draft that was exported", func(p *Plan) {
			p.Exports = []Export{{Format: ExportCommands, At: t0}}
			p.ExportedAt = ptr(t0)
		}, "a draft that was exported"},
		{"unknown export format", func(p *Plan) {
			p.Status = StatusExported
			p.Exports = []Export{{Format: "zip", At: t0}}
			p.ExportedAt = ptr(t0)
		}, `unknown format "zip"`},
		{"baseline before export", func(p *Plan) { p.Baseline = &Baseline{CapturedAt: t0, WindowDays: 7} }, "a baseline before any export"},
		{"no money and no reason", func(p *Plan) { p.Impact.Estimated.Money = nil }, "plan: no money and no reason"},
		{"cluster cost attributed", func(p *Plan) {
			p.Impact.Observed = &Observed{ClusterCost: &ClusterCostDelta{Attributed: true}}
		}, "never attributed to the plan"},
		{"no engine", func(p *Plan) { p.Engine.Name = "" }, "no engine"},
		{"steps out of order", func(p *Plan) { p.Steps[0].Order = 2 }, "order 2, want 1"},
		{"step without owner", func(p *Plan) { p.Steps[0].Target.Owner = nil }, "no owner"},
		{"provider-managed step", func(p *Plan) { p.Steps[0].Target.Owner = &Owner{Kind: OwnerProvider, Ref: "aks"} }, "excluded (provider_managed), not a step"},
		{"not a Kobi verb", func(p *Plan) { p.Steps[0].Action = "create_pdb" }, "not one of Kobi's verbs; make it export-only"},
		{"a verb the kind does not use", func(p *Plan) { p.Steps[0].Action = ActionDeleteResource }, "a rightsizing plan does not delete_resource"},
		{"export-only without reason", func(p *Plan) { p.Steps[0].ExportOnly = true }, "export-only without a reason"},
		{"no spec hash", func(p *Plan) { p.Steps[0].Precondition.SpecHash = "" }, "no spec hash"},
		{"set_resources on a pod", func(p *Plan) { p.Steps[0].Target.Type = "pods" }, "set_resources does not apply to pods"},
		{"unknown params field", func(p *Plan) {
			p.Steps[0].Params = json.RawMessage(`{"containers":[{"container":"api","requests":{"cpu":"120m"}}],"dryRun":true}`)
		}, "unknown field"},
		{"bad quantity", func(p *Plan) {
			p.Steps[0].Params = json.RawMessage(`{"containers":[{"container":"api","requests":{"cpu":"lots"}}]}`)
		}, `"lots" is not a quantity`},
		{"limit under request", func(p *Plan) {
			p.Steps[0].Params = json.RawMessage(`{"containers":[{"container":"api","requests":{"memory":"512Mi"},"limits":{"memory":"256Mi"}}]}`)
		}, "limit 256Mi under request 512Mi"},
		{"a container that changes nothing", func(p *Plan) {
			p.Steps[0].Params = json.RawMessage(`{"containers":[{"container":"api"}]}`)
		}, "changes nothing"},
		{"cluster cost on a step", func(p *Plan) {
			p.Steps[0].Impact.Observed = &Observed{ClusterCost: &ClusterCostDelta{}}
		}, "belongs to the plan, not a step"},
		{"unknown exclusion reason", func(p *Plan) { p.Excluded[0].Reason = "too_big" }, `unknown reason "too_big"`},
		{"stale content hash", func(p *Plan) { p.Steps[0].Risk = RiskHigh }, "contentHash does not match"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := examplePlan()
			c.mutate(&p)
			err := p.Validate()
			if err == nil {
				t.Fatalf("valid, want a problem containing %q", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v\nwant a problem containing %q", err, c.want)
			}
		})
	}
}

// An export-only step is how a plan carries a change with no Kobi verb yet:
// it needs a reason and no params, spec hash or allowed action.
func TestValidate_ExportOnlyStep(t *testing.T) {
	p := examplePlan()
	p.Steps[0].ExportOnly = true
	p.Steps[0].ExportOnlyReason = "no verb creates a PodDisruptionBudget"
	p.Steps[0].Action = "create_pdb"
	p.Steps[0].Params = nil
	p.Steps[0].Precondition = Precondition{}
	p.ContentHash = ContentHash(p)
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidate_ReportsEveryProblemAtOnce(t *testing.T) {
	p := examplePlan()
	p.Title = ""
	p.Version = 0
	p.Steps[0].Risk = "extreme"
	p.ContentHash = ""
	err := p.Validate()
	ve, ok := err.(*ValidationError)
	if !ok || len(ve.Problems) != 3 {
		t.Fatalf("got %v, want three problems", err)
	}
}

func TestStatus_Live(t *testing.T) {
	for s, want := range map[Status]bool{
		StatusDraft: true, StatusExported: true,
		StatusVerified: false, StatusExpired: false, StatusApproved: false, StatusExecuting: false, StatusFailed: false,
	} {
		if s.Live() != want {
			t.Errorf("%s.Live() = %v, want %v", s, s.Live(), want)
		}
	}
}
