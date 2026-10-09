package copilot

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func TestFeedbackValidate(t *testing.T) {
	ok := func(rating, reason, comment string) Feedback {
		return Feedback{ConversationID: "c1", TurnID: "t1", Rating: rating, Reason: reason, Comment: comment}
	}
	cases := []struct {
		name    string
		f       Feedback
		wantErr bool
	}{
		{"up", ok(FeedbackUp, "", ""), false},
		{"down without reason", ok(FeedbackDown, "", ""), false},
		{"down with reason and comment", ok(FeedbackDown, "incorrect", "the pod was not OOMKilled"), false},
		{"up with a reason", ok(FeedbackUp, "slow", ""), true},
		{"unknown reason", ok(FeedbackDown, "rude", ""), true},
		{"unknown rating", ok("meh", "", ""), true},
		{"comment too long", ok(FeedbackDown, "", strings.Repeat("x", FeedbackCommentMax+1)), true},
		{"no turn", Feedback{ConversationID: "c1", Rating: FeedbackUp}, true},
		{"no conversation", Feedback{TurnID: "t1", Rating: FeedbackUp}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.f.Validate(); (err != nil) != c.wantErr {
				t.Fatalf("Validate() err = %v, wantErr %v", err, c.wantErr)
			}
		})
	}
	f := ok(FeedbackDown, "", "  padded  ")
	if err := f.Validate(); err != nil || f.Comment != "padded" {
		t.Fatalf("comment not trimmed: %q (%v)", f.Comment, err)
	}
}

func TestBoltFeedbackStore_Contract(t *testing.T) {
	db, err := bolt.Open(filepath.Join(t.TempDir(), "fb.db"), 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s, err := NewBoltFeedbackStore(db, []byte("copilot_feedback"))
	if err != nil {
		t.Fatal(err)
	}
	feedbackStoreContract(t, s)
}

// feedbackStoreContract is what every FeedbackStore engine must do; the
// Postgres store runs it too (feedback_postgres_ee_test.go).
func feedbackStoreContract(t *testing.T, s FeedbackStore) {
	t.Helper()
	rate := func(tenant, user, conv, turn, rating, reason string) *Feedback {
		t.Helper()
		prev, err := s.Set(Feedback{TenantID: tenant, UserID: user, ConversationID: conv, TurnID: turn,
			Rating: rating, Reason: reason, Model: "claude-sonnet-5-5"})
		if err != nil {
			t.Fatalf("Set: %v", err)
		}
		return prev
	}

	if prev := rate("org-a", "alice", "c1", "t1", FeedbackUp, ""); prev != nil {
		t.Fatalf("first rating returned a previous one: %+v", prev)
	}
	first, _ := s.ForConversation("org-a", "alice", "c1")
	if len(first) != 1 || first[0].CreatedAt.IsZero() {
		t.Fatalf("ForConversation after first rating = %+v", first)
	}
	time.Sleep(5 * time.Millisecond)

	// The last rating wins and the previous one comes back.
	prev := rate("org-a", "alice", "c1", "t1", FeedbackDown, "incomplete")
	if prev == nil || prev.Rating != FeedbackUp {
		t.Fatalf("change returned prev = %+v, want the 👍", prev)
	}
	got, _ := s.ForConversation("org-a", "alice", "c1")
	if len(got) != 1 || got[0].Rating != FeedbackDown || got[0].Reason != "incomplete" {
		t.Fatalf("after change = %+v", got)
	}
	if !got[0].CreatedAt.Equal(first[0].CreatedAt) || !got[0].UpdatedAt.After(first[0].UpdatedAt) {
		t.Fatalf("change must keep createdAt and move updatedAt: before %+v after %+v", first[0], got[0])
	}

	// Scoping: another turn, another conversation, another user, another org.
	rate("org-a", "alice", "c1", "t2", FeedbackUp, "")
	rate("org-a", "alice", "c2", "t3", FeedbackUp, "")
	rate("org-a", "bob", "c1", "t1", FeedbackUp, "")
	rate("org-b", "alice", "c1", "t1", FeedbackUp, "")
	if got, _ := s.ForConversation("org-a", "alice", "c1"); len(got) != 2 {
		t.Fatalf("alice c1 = %d ratings, want 2", len(got))
	}
	if got, _ := s.ForConversation("org-a", "bob", "c1"); len(got) != 1 {
		t.Fatalf("bob c1 = %d ratings, want 1", len(got))
	}

	// Withdraw.
	if prev, err := s.Delete("org-a", "alice", "t2"); err != nil || prev == nil || prev.TurnID != "t2" {
		t.Fatalf("Delete = %+v, %v", prev, err)
	}
	if prev, err := s.Delete("org-a", "alice", "t2"); err != nil || prev != nil {
		t.Fatalf("Delete of a missing rating = %+v, %v; want nil, nil", prev, err)
	}

	// The conversation goes, its ratings go — only that user's, only that one.
	if n, err := s.DeleteConversation("org-a", "alice", "c1"); err != nil || n != 1 {
		t.Fatalf("DeleteConversation = %d, %v; want 1", n, err)
	}
	if got, _ := s.ForConversation("org-a", "alice", "c2"); len(got) != 1 {
		t.Fatal("DeleteConversation removed another conversation's rating")
	}
	if got, _ := s.ForConversation("org-a", "bob", "c1"); len(got) != 1 {
		t.Fatal("DeleteConversation removed another user's rating")
	}

	// Retention: nothing older than the past; everything of the org before the future.
	if n, _ := s.PruneOrg("org-a", time.Now().Add(-time.Hour)); n != 0 {
		t.Fatalf("PruneOrg(past) = %d, want 0", n)
	}
	if n, _ := s.PruneOrg("org-a", time.Now().Add(time.Hour)); n != 2 {
		t.Fatalf("PruneOrg(future) = %d, want 2 (alice c2 + bob c1)", n)
	}
	if got, _ := s.ForConversation("org-b", "alice", "c1"); len(got) != 1 {
		t.Fatal("PruneOrg reached another org")
	}
}
