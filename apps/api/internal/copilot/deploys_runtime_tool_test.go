package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/findings"
	"github.com/kubebolt/kubebolt/apps/api/internal/integrations"
	"github.com/kubebolt/kubebolt/apps/api/internal/models"
)

// ─── get_recent_deploys ──────────────────────────────────────────────────

func TestRecentDeploys_NewestFirstFilteredAndCapped(t *testing.T) {
	now := time.Now()
	var ds []models.DeployEvent
	for i := 0; i < 30; i++ {
		ds = append(ds, models.DeployEvent{Namespace: "shop", Kind: "Deployment", Name: fmt.Sprintf("svc-%02d", i), DeployedAt: now.Add(-time.Duration(i) * time.Minute), Image: "app:v1"})
	}
	ds = append(ds, models.DeployEvent{Namespace: "other", Kind: "Deployment", Name: "x", DeployedAt: now})

	out := summarizeDeploys(ds, 24, "shop", "", 25)
	if out["total"] != 30 || out["truncated"] != true {
		t.Fatalf("total=%v truncated=%v", out["total"], out["truncated"])
	}
	rows := out["deploys"].([]map[string]interface{})
	if len(rows) != 25 || rows[0]["resource"] != "shop/svc-00" {
		t.Errorf("first row %v of %d — want newest first", rows[0], len(rows))
	}
	// The note is what stops "no rollouts" being read as "nothing changed".
	if !strings.Contains(out["note"].(string), "not proof that nothing changed") {
		t.Error("the coverage note is missing")
	}
	if one := summarizeDeploys(ds, 24, "shop", "svc-03", 25); one["total"] != 1 {
		t.Errorf("name filter: total=%v", one["total"])
	}
}

// ─── get_runtime_events ──────────────────────────────────────────────────

type stubRuntimeEvents struct {
	gotQuery findings.EventQuery
	gotAll   bool
	evs      []findings.EventRecord
	err      error
}

func (s *stubRuntimeEvents) List(_ context.Context, q findings.EventQuery, all bool) ([]findings.EventRecord, error) {
	s.gotQuery, s.gotAll = q, all
	return s.evs, s.err
}

func runtimeEvent(rule, prio, pod, behavior string, fields map[string]string) findings.EventRecord {
	return findings.EventRecord{ClusterID: "c1", RuntimeEvent: integrations.RuntimeEvent{
		At: time.Now(), Priority: prio, RuleName: rule, Namespace: "shop", PodName: pod,
		DetectedBehavior: behavior, Source: "falco", Fields: fields,
	}}
}

func callRuntime(t *testing.T, e *Executor, input string) (ToolResult, map[string]any) {
	t.Helper()
	res := e.ExecuteCtx(context.Background(), ToolCall{ID: "c1", Name: "get_runtime_events", Input: json.RawMessage(input)})
	var payload map[string]any
	if err := json.Unmarshal([]byte(res.Content), &payload); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, res.Content)
	}
	return res, payload
}

func TestRuntimeEvents_CountsFirstAndHidesCredentialsInCommandLines(t *testing.T) {
	// The store returns newest first; the credential-carrying event is the
	// newest, so it is one of the rows shown — the assertion below would be
	// vacuous if the cap had cut it.
	evs := []findings.EventRecord{runtimeEvent("Read sensitive file", "Critical", "api-7f9",
		"mysql -u root --password=hunter2 read /etc/shadow",
		map[string]string{"proc.cmdline": "mysql -u root --password=hunter2", "user.name": "root"})}
	for i := 0; i < 40; i++ {
		evs = append(evs, runtimeEvent("Terminal shell in container", "Notice", "api-7f9", "shell spawned in api", nil))
	}

	src := &stubRuntimeEvents{evs: evs}
	res, payload := callRuntime(t, NewExecutor(nil).WithRuntimeEvents(src), `{}`)
	if res.IsError {
		t.Fatalf("error: %s", res.Content)
	}
	if !strings.Contains(res.Content, "Read sensitive file") {
		t.Fatal("the credential-carrying event is not among the rows — the test would prove nothing")
	}
	if strings.Contains(res.Content, "hunter2") {
		t.Errorf("a command line leaked its password: %s", res.Content)
	}
	if !strings.Contains(res.Content, "root") {
		t.Error("the non-secret fields were lost")
	}
	if payload["total"].(float64) != 41 || payload["truncated"] != true {
		t.Errorf("total=%v truncated=%v", payload["total"], payload["truncated"])
	}
	top := payload["topRules"].([]any)[0].(map[string]any)
	if top["rule"] != "Terminal shell in container" || top["events"].(float64) != 40 {
		t.Errorf("topRules[0] = %v — the noisy rule is one fact with its count", top)
	}
	if src.gotQuery.Since.IsZero() || src.gotAll {
		t.Errorf("query = %+v all=%v — want a window on the current cluster", src.gotQuery, src.gotAll)
	}
}

func TestRuntimeEvents_UnavailableIsNotAQuietCluster(t *testing.T) {
	res, payload := callRuntime(t, NewExecutor(nil), `{}`)
	if !res.IsError || !strings.Contains(fmt.Sprint(payload["error"]), "NOT evidence") {
		t.Errorf("err=%v payload=%v", res.IsError, payload)
	}
}

func TestDeploysAndRuntimeEvents_ArePublishedReadOnly(t *testing.T) {
	want := map[string][]string{
		"get_recent_deploys": {"sinceHours", "namespace", "name", "limit"},
		"get_runtime_events": {"priority", "source", "namespace", "pod", "sinceHours", "cluster", "limit"},
	}
	for _, d := range GovernedToolDefinitions(false, false) {
		props, ok := want[d.Name]
		if !ok {
			continue
		}
		delete(want, d.Name)
		schema, _ := d.InputSchema["properties"].(map[string]interface{})
		for _, p := range props {
			if _, ok := schema[p]; !ok {
				t.Errorf("%s: %q is read by the executor but not declared", d.Name, p)
			}
		}
	}
	for name := range want {
		t.Errorf("%s is missing from the read-only catalogue", name)
	}
}

// A real Falco event is ~1.7 KB (≈40 fields plus an output line repeating
// them): 22 of them passed the 32 KB tool cap and the answer said it had been
// truncated. At the maximum page, the result must still fit.
func TestRuntimeEvents_MaxPageFitsTheToolCap(t *testing.T) {
	fields := map[string]string{}
	for i := 0; i < 40; i++ {
		fields[fmt.Sprintf("falco.field.%02d", i)] = strings.Repeat("x", 30)
	}
	for _, k := range runtimeEventFieldKeys {
		fields[k] = strings.Repeat("v", 150)
	}
	var evs []findings.EventRecord
	for i := 0; i < 200; i++ {
		evs = append(evs, runtimeEvent(fmt.Sprintf("rule-%d", i%7), "Notice", "kindnetd-x", strings.Repeat("output ", 250), fields))
	}
	res, payload := callRuntime(t, NewExecutor(nil).WithRuntimeEvents(&stubRuntimeEvents{evs: evs}), `{"limit":1000}`)
	if n := len(res.Content); n > 32*1024 {
		t.Errorf("max page is %d bytes, over the 32 KB tool cap", n)
	}
	// Heavy rows stop at the byte budget before the count cap — and say so.
	if got := len(payload["events"].([]any)); got == 0 || got > maxRuntimeEventsReturned || payload["truncated"] != true {
		t.Errorf("rows = %d truncated = %v — want a non-empty page, at most %d, marked truncated", got, payload["truncated"], maxRuntimeEventsReturned)
	}
	row := payload["events"].([]any)[0].(map[string]any)
	if _, ok := row["fields"].(map[string]any)["falco.field.00"]; ok {
		t.Error("a field outside the kept set reached the row")
	}
}
