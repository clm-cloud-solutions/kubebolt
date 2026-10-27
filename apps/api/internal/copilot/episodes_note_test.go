package copilot

import (
	"strings"
	"testing"
)

// auto_recovered reads as "it recovered by itself"; the episode results say
// what it does and does not mean (history-oom-earlier: an OOM fixed by hand
// was reported as nobody having intervened).
func TestAnnotateEpisodes_ExplainsAutoRecovered(t *testing.T) {
	list := `{"episodes":[{"id":"a","resolutionKind":"auto_recovered","rule":"oom-killed"}],"total":1}`
	got := AnnotateEpisodes(list)
	if !strings.Contains(got, `"resolutionNote":"resolutionKind cleared_no_action_recorded (the UI shows it as auto_recovered)`) {
		t.Fatalf("no note: %s", got)
	}
	// The note must not license the claim it exists to stop, and a fix
	// outside the cluster never shows in the workload's history.
	for _, want := range []string{"outside the cluster", "Never say it recovered by itself"} {
		if !strings.Contains(got, want) {
			t.Errorf("note lacks %q: %s", want, got)
		}
	}
	if strings.Contains(got, "Before saying it recovered by itself") {
		t.Error("the note still reads as permission to say it recovered by itself")
	}
	if AnnotateEpisodes(got) != got {
		t.Error("annotated twice")
	}
	old := `{"episodes":[{"id":"a","resolutionKind":"auto_recovered"}],"resolutionNote":"an older wording","total":1}`
	if got := AnnotateEpisodes(old); !strings.Contains(got, autoRecoveredNote) {
		t.Errorf("an older note was kept: %s", got)
	}
	firing := `{"episodes":[{"id":"b","status":"firing"}],"total":1}`
	if AnnotateEpisodes(firing) != firing {
		t.Error("a result with no auto_recovered episode was annotated")
	}
}

// The label itself read as "it recovered by itself": the model sees what the
// store knows, on the episode, its recurrences and the timeline.
func TestAnnotateEpisodes_RelabelsWhereTheEpisodeSaysHowItEnded(t *testing.T) {
	detail := `{"id":"a","resolutionKind":"auto_recovered","rule":"oom-killed",` +
		`"recurrence":[{"id":"p","resolutionKind":"auto_recovered"}],` +
		`"timeline":[{"to":"firing"},{"from":"firing","reason":"auto_recovered","to":"resolved"}]}`
	got := AnnotateEpisodes(detail)
	if strings.Contains(got, `"auto_recovered"`) {
		t.Errorf("auto_recovered left as a value: %s", got)
	}
	if n := strings.Count(got, `"`+clearedNoActionRecorded+`"`); n != 3 {
		t.Errorf("relabelled %d places, want 3 (episode, recurrence, transition): %s", n, got)
	}
	rule := `{"episodes":[{"id":"r","resolutionKind":"rule_changed"},{"id":"a","resolutionKind":"auto_recovered"}]}`
	if got := AnnotateEpisodes(rule); !strings.Contains(got, `"resolutionKind":"rule_changed"`) {
		t.Errorf("rule_changed was rewritten: %s", got)
	}
}
