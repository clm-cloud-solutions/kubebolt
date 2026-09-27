package insights

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/models"
)

// The seam for typed evidence. No rule emits any yet — these pin the plumbing
// so instrumenting the 24 rules one at a time stays a mechanical change.

// The whole point of the variadic parameter: 27 existing call sites compile
// and behave exactly as before. If this ever needs updating, the migration
// stopped being incremental.
func TestNewInsight_WithoutEvidenceIsUnchanged(t *testing.T) {
	ins := newInsight("warning", "Pod/ns/p1", "Title", "Message", "Suggestion")
	if ins.EvidenceCount != 0 {
		t.Errorf("EvidenceCount = %d, want 0", ins.EvidenceCount)
	}
	if ins.Evidence != nil {
		t.Errorf("Evidence = %v, want nil", ins.Evidence)
	}
	if ins.Severity != "warning" || ins.Title != "Title" || ins.Suggestion != "Suggestion" {
		t.Error("the prose fields moved")
	}
}

func TestNewInsight_CarriesEvidenceAndCountsIt(t *testing.T) {
	at := time.Date(2026, 9, 16, 3, 10, 0, 0, time.UTC)
	ins := newInsight("critical", "Pod/ns/p1", "OOM", "msg", "sug",
		models.Evidence{Kind: models.EvidenceConfig, Label: "Memory limit", Detail: "64Mi"},
		models.Evidence{Kind: models.EvidenceEvent, Label: "Last termination", Detail: "OOMKilled (137)", At: &at},
	)
	if ins.EvidenceCount != 2 {
		t.Fatalf("EvidenceCount = %d, want 2", ins.EvidenceCount)
	}
	if len(ins.Evidence) != 2 {
		t.Fatalf("len(Evidence) = %d, want 2", len(ins.Evidence))
	}
	if ins.Evidence[1].At == nil || !ins.Evidence[1].At.Equal(at) {
		t.Error("the event's own clock did not survive — that is the field the insight timestamps cannot give")
	}
}

// The guard that matters most. /insights returns the whole list unpaginated
// (383 rows, ~230 KB on the busiest cluster) and Kobi's get_insights is capped
// at 32KB and ALREADY truncating. Evidence on the list would make a live
// problem worse, so it must not be serialisable from an Insight at all — a
// count is what the list gets.
func TestInsight_EvidenceNeverReachesTheWire(t *testing.T) {
	at := time.Now()
	ins := newInsight("critical", "Pod/ns/p1", "OOM", "msg", "sug",
		models.Evidence{Kind: models.EvidenceEvent, Label: "Last termination",
			Detail: "OOMKilled (exit 137)", Source: "pod.status.containerStatuses[0]", At: &at},
	)
	raw, err := json.Marshal(ins)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, leaked := range []string{"OOMKilled (exit 137)", "Last termination", "containerStatuses", `"evidence"`} {
		if strings.Contains(body, leaked) {
			t.Errorf("the insight serialised %q; evidence belongs to the episode, not the list", leaked)
		}
	}
	if !strings.Contains(body, `"evidenceCount":1`) {
		t.Errorf("the list lost its count, which is the only affordance it has: %s", body)
	}
}

// A count of zero must not add a key to 383 rows for nothing.
func TestInsight_ZeroCountIsOmitted(t *testing.T) {
	raw, _ := json.Marshal(newInsight("info", "Pod/ns/p1", "t", "m", "s"))
	if strings.Contains(string(raw), "evidenceCount") {
		t.Errorf("an insight with no evidence still pays for the key: %s", raw)
	}
}

// Both persistence paths run through stampRecordContent now — the open path
// before the sink, the touch path after it. A field that reaches only one of
// them is a field that is present or absent depending on which tick you look.
func TestStampRecordContent_CarriesEvidenceToTheRecord(t *testing.T) {
	ins := newInsight("critical", "Pod/ns/p1", "OOM", "msg", "sug",
		models.Evidence{Kind: models.EvidenceConfig, Label: "Memory limit", Detail: "64Mi"})
	rec := &InsightRecord{}
	stampRecordContent(rec, ins)
	if len(rec.Evidence) != 1 || rec.Evidence[0].Detail != "64Mi" {
		t.Fatalf("Evidence = %+v, want the rule's one fact", rec.Evidence)
	}
	if rec.Title != "OOM" || rec.Suggestion != "sug" {
		t.Error("the prose stopped being stamped")
	}
}

// The record is ONE identity; evidence belongs to ONE occurrence. Persisting
// it on the record would keep only the last episode's proof, and the Bolt
// store (a JSON blob) would do it silently.
func TestInsightRecord_EvidenceIsNotPersisted(t *testing.T) {
	rec := &InsightRecord{
		Fingerprint: "fp", Title: "OOM",
		Evidence: []models.Evidence{{Kind: models.EvidenceConfig, Label: "Memory limit", Detail: "64Mi"}},
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "64Mi") || strings.Contains(string(raw), "evidence") {
		t.Errorf("the record persisted its evidence: %s", raw)
	}
}

// At is a pointer because "this is state" and "I don't know when" are
// different answers, and a zero Time cannot tell them apart.
func TestEvidence_NilClockIsOmittedNotZero(t *testing.T) {
	raw, _ := json.Marshal(models.Evidence{
		Kind: models.EvidenceConfig, Label: "Memory limit", Detail: "64Mi",
	})
	if strings.Contains(string(raw), `"at"`) {
		t.Errorf("a config fact reported a moment it does not have: %s", raw)
	}
	if strings.Contains(string(raw), "0001-01-01") {
		t.Errorf("a zero time leaked to the wire: %s", raw)
	}
}
