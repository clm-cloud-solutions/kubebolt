package copilot

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/skills"
)

func TestLoadSkillServesTheLibrary(t *testing.T) {
	e := NewExecutor(nil) // no cluster: a skill is KubeBolt's own content
	res := e.ExecuteCtx(context.Background(), ToolCall{ID: "c1", Name: "load_skill", Input: json.RawMessage(`{"name":"workload-revisions"}`)})
	if res.IsError {
		t.Fatalf("load_skill failed: %s", res.Content)
	}
	var got struct{ Skill, Content string }
	if err := json.Unmarshal([]byte(res.Content), &got); err != nil {
		t.Fatal(err)
	}
	want, _ := skills.Get("workload-revisions")
	if got.Skill != "workload-revisions" || got.Content != want.Body {
		t.Errorf("load_skill answered %+v", got)
	}

	res = e.ExecuteCtx(context.Background(), ToolCall{ID: "c2", Name: "load_skill", Input: json.RawMessage(`{"name":"nope"}`)})
	if !res.IsError || !strings.Contains(res.Content, "workload-revisions") {
		t.Errorf("an unknown skill must fail and list the ones that exist: %s", res.Content)
	}
}

// The chat's load_skill lists every listed skill, Kobi's own included; the
// shared definition (the MCP profiles, Autopilot) lists the shared ones only.
func TestLoadSkillIndexesTheListedSkills(t *testing.T) {
	desc := func(defs []ToolDefinition) string {
		for _, d := range defs {
			if d.Name == "load_skill" {
				return d.Description
			}
		}
		t.Fatal("load_skill is not a tool")
		return ""
	}
	chat, shared := desc(ChatToolDefinitions(true, true)), desc(ToolDefinitions())
	for _, s := range skills.All() {
		line := s.Name + ": " + s.Description
		if got := strings.Contains(chat, line); got != s.Listed {
			t.Errorf("%s: listed=%v but in the chat's index=%v", s.Name, s.Listed, got)
		}
		if got, want := strings.Contains(shared, line), s.Listed && s.Audience == ""; got != want {
			t.Errorf("%s (audience %q): in the shared index=%v, want %v", s.Name, s.Audience, got, want)
		}
	}
}

type fakeHistory []map[string]interface{}

func (f fakeHistory) GetWorkloadHistoryWithChanges(_, _, _ string) []map[string]interface{} { return f }

func TestWorkloadHistoryPointsAtItsSkill(t *testing.T) {
	out := workloadHistoryContent(fakeHistory{{"revision": "1"}}, "deployments", "lab", "app")
	if !strings.Contains(out, WorkloadRevisionsSkill) {
		t.Errorf("history without the skill pointer: %s", out)
	}
	if strings.Contains(workloadHistoryContent(fakeHistory{}, "deployments", "lab", "app"), "load_skill") {
		t.Error("an empty history points at a skill it has nothing for")
	}
}

func TestAnnotateWorkloadHistory(t *testing.T) {
	recorded := `{"history":[{"revision":"2"},{"revision":"1"}]}`
	if got := AnnotateWorkloadHistory(recorded, "deployments"); !strings.Contains(got, WorkloadRevisionsSkill) || !strings.Contains(got, `"revision":"2"`) || !strings.Contains(got, `"rollout"`) {
		t.Errorf("annotated: %s", got)
	}
	for _, raw := range []string{`{"error":"not found"}`, `{"history":[]}`, `not json`} {
		if got := AnnotateWorkloadHistory(raw, "deployments"); got != raw {
			t.Errorf("%s changed to %s", raw, got)
		}
	}
}
