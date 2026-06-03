package audit

import (
	"testing"
	"time"
)

func TestMemoryStore_AppendListPrune(t *testing.T) {
	s := NewMemoryStore()
	base := time.Now().UTC()

	// Append three records out of chronological order.
	for i, ts := range []time.Time{base.Add(-2 * time.Hour), base, base.Add(-1 * time.Hour)} {
		if err := s.Append(&Record{
			ID:        string(rune('a' + i)),
			Timestamp: ts,
			Source:    "copilot_proposal",
			Action:    "scale_workload",
			Result:    "success",
		}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	// List newest-first.
	all, err := s.List(0)
	if err != nil || len(all) != 3 {
		t.Fatalf("list: n=%d err=%v", len(all), err)
	}
	if !all[0].Timestamp.Equal(base) {
		t.Fatalf("not sorted newest-first: %v", all[0].Timestamp)
	}

	// Limit.
	one, _ := s.List(1)
	if len(one) != 1 || !one[0].Timestamp.Equal(base) {
		t.Fatalf("limit not applied: %+v", one)
	}

	// Prune everything older than 90 min → removes the -2h record only.
	removed, err := s.Prune(base.Add(-90 * time.Minute))
	if err != nil || removed != 1 {
		t.Fatalf("prune: removed=%d err=%v", removed, err)
	}
	left, _ := s.List(0)
	if len(left) != 2 {
		t.Fatalf("expected 2 records after prune, got %d", len(left))
	}
}

func TestRecord_OriginatingInsightProvenance(t *testing.T) {
	s := NewMemoryStore()
	if err := s.Append(&Record{
		ID: "x", Timestamp: time.Now(), Source: "copilot_proposal",
		Action: "restart_workload", Result: "success",
		OriginatingInsightID: "occ-123",
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	got, _ := s.List(1)
	if got[0].OriginatingInsightID != "occ-123" {
		t.Fatalf("provenance not stored: %+v", got[0])
	}
}
