package audit

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

// classFixture appends two mutations (one written before records carried a
// class) and, after them, five access events: the busy minutes of cloud calls
// that used to take the whole page.
func classFixture(t *testing.T, s Store) {
	t.Helper()
	now := time.Now().UTC()
	recs := []*Record{
		{ID: "m-legacy", Timestamp: now.Add(-10 * time.Minute), Action: "scale_workload", Result: "success"},
		{ID: "m-new", Timestamp: now.Add(-9 * time.Minute), Class: ClassMutation, Action: "restart_workload", Result: "success"},
	}
	for i := 0; i < 5; i++ {
		recs = append(recs, &Record{ID: fmt.Sprintf("a%d", i), Timestamp: now.Add(time.Duration(i-5) * time.Minute), Class: ClassAccess, Action: "mcp_tool_call", Result: "success"})
	}
	for _, r := range recs {
		if err := s.Append(r); err != nil {
			t.Fatalf("Append %s: %v", r.ID, err)
		}
	}
}

func ids(recs []Record) []string {
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = r.ID
	}
	return out
}

func checkClassPages(t *testing.T, s Store, org string) {
	t.Helper()
	for _, tc := range []struct {
		class string
		limit int
		want  []string
	}{
		// The mutations sit behind five access events: a limit of two still
		// reaches them, the legacy one included (no class = mutation).
		{ClassMutation, 2, []string{"m-new", "m-legacy"}},
		{ClassAccess, 3, []string{"a4", "a3", "a2"}},
		{"", 3, []string{"a4", "a3", "a2"}},
		{ClassMutation, 0, []string{"m-new", "m-legacy"}},
	} {
		got, err := s.ListOrgClass(org, tc.class, tc.limit)
		if err != nil {
			t.Fatalf("ListOrgClass(%q, %d): %v", tc.class, tc.limit, err)
		}
		if fmt.Sprint(ids(got)) != fmt.Sprint(tc.want) {
			t.Errorf("ListOrgClass(%q, %d) = %v, want %v", tc.class, tc.limit, ids(got), tc.want)
		}
	}
	// ListOrg is every class.
	if got, _ := s.ListOrg(org, 3); fmt.Sprint(ids(got)) != "[a4 a3 a2]" {
		t.Errorf("ListOrg(3) = %v", ids(got))
	}
}

func TestListOrgClass_AppliesTheClassBeforeTheLimit(t *testing.T) {
	t.Run("memory", func(t *testing.T) {
		s := NewMemoryStore()
		classFixture(t, s)
		checkClassPages(t, s, "")
	})
	t.Run("bolt", func(t *testing.T) {
		db, err := bolt.Open(filepath.Join(t.TempDir(), "audit.db"), 0o600, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		bucket := []byte("kobi_actions")
		if err := db.Update(func(tx *bolt.Tx) error { _, err := tx.CreateBucket(bucket); return err }); err != nil {
			t.Fatal(err)
		}
		s := NewBoltStore(db, bucket)
		classFixture(t, s)
		checkClassPages(t, s, "")
	})
}
