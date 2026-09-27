package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/insights"
	"github.com/kubebolt/kubebolt/apps/api/internal/models"
)

// The two history tools. get_insights answers "what is wrong now" from the
// live engine; these answer "what happened" from the stored episodes — which
// is why they must work when the cluster does not.

type stubEpisodes struct {
	gotQuery insights.EpisodeQuery
	window   []insights.Episode
	episode  insights.Episode
	trans    []insights.Transition
	recur    []insights.Episode
	err      error
}

func (s *stubEpisodes) Window(_ context.Context, q insights.EpisodeQuery) ([]insights.Episode, error) {
	s.gotQuery = q
	return s.window, s.err
}
func (s *stubEpisodes) Episode(context.Context, string) (insights.Episode, []insights.Transition, error) {
	return s.episode, s.trans, s.err
}
func (s *stubEpisodes) Recurrence(context.Context, string, int32) ([]insights.Episode, error) {
	return s.recur, nil
}

func sampleEpisode(id string) insights.Episode {
	t0 := time.Date(2026, 9, 16, 3, 10, 0, 0, time.UTC)
	resolved := t0.Add(40 * time.Minute)
	return insights.Episode{
		ID: id, TenantID: "org-1", ClusterID: "8bc12070-uid", ClusterName: "cluster-processing",
		Fingerprint: strings.Repeat("a", 64), RuleID: "crash-loop",
		Resource: "Pod/airflow/airflow-scheduler-84fd", Namespace: "airflow",
		Title: "CrashLoopBackOff", Status: insights.EpisodeResolved,
		Severity: "critical", MaxSeverity: "critical",
		FirstSeen: t0, LastSeen: resolved, ResolvedAt: &resolved,
		ResolutionKind: insights.ResolutionAutoRecovered, FlapCount: 3,
	}
}

func callEpisodeTool(t *testing.T, e *Executor, name, input string) (ToolResult, map[string]any) {
	t.Helper()
	if input == "" {
		input = "{}"
	}
	res := e.ExecuteCtx(context.Background(), ToolCall{ID: "c1", Name: name, Input: json.RawMessage(input)})
	var payload map[string]any
	if err := json.Unmarshal([]byte(res.Content), &payload); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, res.Content)
	}
	return res, payload
}

// The regression that matters most, and the reason these tools exist at all:
// "what happened on the cluster that died last night" is asked ABOUT a dead
// cluster. Every other tool is gated on a live connector.
func TestEpisodeTools_AnswerWithoutAConnector(t *testing.T) {
	src := &stubEpisodes{window: []insights.Episode{sampleEpisode("ep-1")}, episode: sampleEpisode("ep-1")}
	e := NewExecutor(nil).WithEpisodes(src)

	for _, tc := range []struct{ name, input string }{
		{"get_insight_episodes", "{}"},
		{"get_insight_episode", `{"id":"ep-1"}`},
	} {
		res, _ := callEpisodeTool(t, e, tc.name, tc.input)
		if res.IsError {
			t.Errorf("%s failed with no connector, which is exactly when it is needed: %s", tc.name, res.Content)
		}
		if strings.Contains(res.Content, "needsProxy") {
			t.Errorf("%s hit the connector gate; stored history does not need one", tc.name)
		}
	}
}

func TestEpisodeTools_UnavailableIsNotAnEmptyHistory(t *testing.T) {
	e := NewExecutor(nil)
	for _, tc := range []struct{ name, input string }{
		{"get_insight_episodes", "{}"},
		{"get_insight_episode", `{"id":"ep-1"}`},
	} {
		res := e.ExecuteCtx(context.Background(), ToolCall{ID: "c1", Name: tc.name, Input: json.RawMessage(tc.input)})
		if !res.IsError {
			t.Errorf("%s reported success with no store", tc.name)
		}
		if strings.Contains(res.Content, `"episodes"`) {
			t.Errorf("%s returned an empty-history shape; the model would read it as 'nothing happened'", tc.name)
		}
		if !strings.Contains(strings.ToLower(res.Content), "not") {
			t.Errorf("%s does not say the absence is a capability gap: %s", tc.name, res.Content)
		}
	}
}

// The fingerprint is 64 hex characters no tool takes (the detail returns
// recurrence itself), and the tenant is the same value on every row.
func TestEpisodeTools_RowIsShapedForAModelNotForTheDatabase(t *testing.T) {
	src := &stubEpisodes{window: []insights.Episode{sampleEpisode("ep-1")}}
	res, payload := callEpisodeTool(t, NewExecutor(nil).WithEpisodes(src), "get_insight_episodes", "")

	if strings.Contains(res.Content, strings.Repeat("a", 64)) {
		t.Error("shipped the 64-char fingerprint on a list row")
	}
	if strings.Contains(res.Content, "org-1") {
		t.Error("shipped the tenant id, which is identical on every row")
	}
	eps, _ := payload["episodes"].([]any)
	if len(eps) != 1 {
		t.Fatalf("episodes = %d", len(eps))
	}
	row, _ := eps[0].(map[string]any)
	// The cluster's NAME: an episode outlives its cluster, and a uid means
	// nothing to whoever reads the answer.
	if row["cluster"] != "cluster-processing" {
		t.Errorf("cluster = %v, want the display name", row["cluster"])
	}
	for field, want := range map[string]any{
		"rule": "crash-loop", "status": insights.EpisodeResolved,
		"severity": "critical", "resolutionKind": insights.ResolutionAutoRecovered,
		"flapCount": float64(3),
	} {
		if row[field] != want {
			t.Errorf("%s = %v, want %v", field, row[field], want)
		}
	}
}

func TestEpisodeTools_WindowAndLimitAreClamped(t *testing.T) {
	cases := []struct {
		input     string
		wantHours float64
		wantLimit int32
	}{
		{`{}`, defaultEpisodeWindowHours, defaultEpisodesReturned},
		{`{"sinceHours":6,"limit":5}`, 6, 5},
		{`{"sinceHours":0}`, 1, defaultEpisodesReturned},
		{`{"sinceHours":100000}`, maxEpisodeWindowHours, defaultEpisodesReturned},
		{`{"limit":5000}`, defaultEpisodeWindowHours, maxEpisodesReturned},
		{`{"limit":-3}`, defaultEpisodeWindowHours, maxEpisodesReturned},
	}
	for _, tc := range cases {
		src := &stubEpisodes{}
		if res, _ := callEpisodeTool(t, NewExecutor(nil).WithEpisodes(src), "get_insight_episodes", tc.input); res.IsError {
			t.Fatalf("%s: %s", tc.input, res.Content)
		}
		if got := src.gotQuery.Until.Sub(src.gotQuery.Since).Hours(); got != tc.wantHours {
			t.Errorf("%s → %.0fh window, want %.0f", tc.input, got, tc.wantHours)
		}
		if src.gotQuery.Limit != tc.wantLimit {
			t.Errorf("%s → limit %d, want %d", tc.input, src.gotQuery.Limit, tc.wantLimit)
		}
	}
}

// "all" is the only way to reach episodes of a cluster that is no longer in
// the selector — which is most of what history is for.
func TestEpisodeTools_ClusterAllWidensToTheOrg(t *testing.T) {
	src := &stubEpisodes{}
	if res, _ := callEpisodeTool(t, NewExecutor(nil).WithEpisodes(src), "get_insight_episodes", `{"cluster":"all"}`); res.IsError {
		t.Fatal(res.Content)
	}
	if src.gotQuery.ClusterID != "" {
		t.Errorf("ClusterID = %q, want empty so the query spans the org", src.gotQuery.ClusterID)
	}
}

func TestEpisodeTools_FiltersReachTheQuery(t *testing.T) {
	src := &stubEpisodes{}
	callEpisodeTool(t, NewExecutor(nil).WithEpisodes(src), "get_insight_episodes",
		`{"status":"expired","severity":"critical","rule":"oom-killed"}`)
	if src.gotQuery.Status != "expired" || src.gotQuery.Severity != "critical" || src.gotQuery.RuleID != "oom-killed" {
		t.Errorf("filters dropped: %+v", src.gotQuery)
	}
}

func TestEpisodeDetail_CarriesTimelineEvidenceAndRecurrence(t *testing.T) {
	ep := sampleEpisode("ep-1")
	at := ep.FirstSeen
	ep.Evidence = []models.Evidence{
		{Kind: models.EvidenceEvent, Label: "Last termination", Detail: "OOMKilled (137)", At: &at},
	}
	src := &stubEpisodes{
		episode: ep,
		trans: []insights.Transition{
			{EpisodeID: "ep-1", FromState: "", ToState: insights.EpisodeFiring, At: at, Actor: "rule:crash-loop"},
			{EpisodeID: "ep-1", FromState: insights.EpisodeFiring, ToState: insights.EpisodeResolved,
				At: at.Add(40 * time.Minute), Actor: "system", Reason: "auto_recovered"},
		},
		// Includes the episode itself, which must not be reported as a prior
		// occurrence of itself.
		recur: []insights.Episode{sampleEpisode("ep-1"), sampleEpisode("ep-0"), sampleEpisode("ep-old")},
	}
	_, payload := callEpisodeTool(t, NewExecutor(nil).WithEpisodes(src), "get_insight_episode", `{"id":"ep-1"}`)

	tl, _ := payload["timeline"].([]any)
	if len(tl) != 2 {
		t.Fatalf("timeline = %d entries, want 2", len(tl))
	}
	first, _ := tl[0].(map[string]any)
	if first["actor"] != "rule:crash-loop" {
		t.Errorf("the timeline lost WHO: %v", first)
	}
	if _, ok := first["from"]; ok {
		t.Error("an opening transition reported a from-state it does not have")
	}

	ev, _ := payload["evidence"].([]any)
	if len(ev) != 1 {
		t.Fatalf("evidence = %+v, want the one fact", payload["evidence"])
	}

	if payload["recurrenceCount"] != float64(2) {
		t.Errorf("recurrenceCount = %v, want 2 — the episode is not a recurrence of itself", payload["recurrenceCount"])
	}
}

func TestEpisodeDetail_RequiresAnID(t *testing.T) {
	src := &stubEpisodes{}
	res, _ := callEpisodeTool(t, NewExecutor(nil).WithEpisodes(src), "get_insight_episode", "{}")
	if !res.IsError || !strings.Contains(res.Content, "id is required") {
		t.Errorf("a missing id was not refused: %s", res.Content)
	}
}

func TestEpisodeTools_ReaderErrorSurfaces(t *testing.T) {
	src := &stubEpisodes{err: fmt.Errorf("postgres went away")}
	for _, tc := range []struct{ name, input string }{
		{"get_insight_episodes", "{}"},
		{"get_insight_episode", `{"id":"ep-1"}`},
	} {
		res, _ := callEpisodeTool(t, NewExecutor(nil).WithEpisodes(src), tc.name, tc.input)
		if !res.IsError || !strings.Contains(res.Content, "postgres went away") {
			t.Errorf("%s swallowed the cause: %s", tc.name, res.Content)
		}
	}
}

// MCP publishes GovernedToolDefinitions(false, false) as its read-only
// catalogue; a read tool missing from it is invisible to every MCP host.
func TestEpisodeTools_ArePublishedAndReadOnly(t *testing.T) {
	for _, name := range []string{"get_insight_episodes", "get_insight_episode"} {
		var def *ToolDefinition
		for _, d := range GovernedToolDefinitions(false, false) {
			if d.Name == name {
				dd := d
				def = &dd
			}
		}
		if def == nil {
			t.Fatalf("%s is missing from the read-only catalogue", name)
		}
		if !strings.Contains(strings.ToLower(def.Description), "history") &&
			!strings.Contains(strings.ToLower(def.Description), "timeline") {
			t.Errorf("%s does not say it reads history", name)
		}
	}
}
