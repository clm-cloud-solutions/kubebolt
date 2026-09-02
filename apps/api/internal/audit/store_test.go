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

	// List newest-first. The records carry no tenant, as in a single-tenant
	// install, and "" is the org such a install asks with.
	all, err := s.ListOrg("", 0)
	if err != nil || len(all) != 3 {
		t.Fatalf("list: n=%d err=%v", len(all), err)
	}
	if !all[0].Timestamp.Equal(base) {
		t.Fatalf("not sorted newest-first: %v", all[0].Timestamp)
	}

	// Limit.
	one, _ := s.ListOrg("", 1)
	if len(one) != 1 || !one[0].Timestamp.Equal(base) {
		t.Fatalf("limit not applied: %+v", one)
	}

	// Prune everything older than 90 min → removes the -2h record only.
	removed, err := s.PruneOrg("", base.Add(-90*time.Minute))
	if err != nil || removed != 1 {
		t.Fatalf("prune: removed=%d err=%v", removed, err)
	}
	left, _ := s.ListOrg("", 0)
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
	got, _ := s.ListOrg("", 1)
	if got[0].OriginatingInsightID != "occ-123" {
		t.Fatalf("provenance not stored: %+v", got[0])
	}
}

// TestMemoryStore_OrgIsolation covers the in-Go filter the Bolt and Memory
// stores share. The Postgres equivalent lives in store_postgres_ee_test.go;
// this one needs no database and so runs on every CI leg.
func TestMemoryStore_OrgIsolation(t *testing.T) {
	s := NewMemoryStore()
	now := time.Now().UTC()
	for _, r := range []Record{
		{ID: "a1", TenantID: "org-a", Timestamp: now},
		{ID: "a2", TenantID: "org-a", Timestamp: now.Add(-48 * time.Hour)},
		{ID: "b1", TenantID: "org-b", Timestamp: now},
		{ID: "legacy", Timestamp: now}, // no tenant: pre-upgrade / single-tenant
	} {
		rec := r
		if err := s.Append(&rec); err != nil {
			t.Fatalf("append %s: %v", r.ID, err)
		}
	}

	// org-a sees its own two plus the untenanted one — never org-b's.
	got, err := s.ListOrg("org-a", 0)
	if err != nil {
		t.Fatalf("ListOrg: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("org-a: got %d records, want 3 — %+v", len(got), got)
	}
	for _, r := range got {
		if r.TenantID == "org-b" {
			t.Fatalf("org-a leaked org-b's record %q", r.ID)
		}
	}

	// The limit is applied after the filter, so a foreign record cannot eat a
	// slot and silently shorten this org's page.
	if one, _ := s.ListOrg("org-b", 1); len(one) != 1 || one[0].ID != "b1" {
		t.Fatalf("org-b limit: %+v", one)
	}

	// Pruning one org leaves the other's history intact.
	n, err := s.PruneOrg("org-a", now.Add(-24*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("PruneOrg(org-a): err=%v n=%d, want 1", err, n)
	}
	if got, _ = s.ListOrg("org-b", 0); len(got) != 2 { // b1 + legacy
		t.Fatalf("org-b after org-a's prune: got %d, want 2 — %+v", len(got), got)
	}
}
