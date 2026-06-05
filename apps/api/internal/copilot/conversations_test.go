package copilot

import (
	"fmt"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

// newConvStores returns one of each ConversationStore impl with the given
// caps, so every behavioural test runs against both Bolt and Memory.
func newConvStores(t *testing.T, retention time.Duration, maxPerUser int) map[string]ConversationStore {
	t.Helper()
	dir := t.TempDir()
	db, err := bolt.Open(dir+"/conv.db", 0600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		t.Fatalf("bolt open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	bucket := []byte("copilot_conversations")
	if err := db.Update(func(tx *bolt.Tx) error {
		_, e := tx.CreateBucketIfNotExists(bucket)
		return e
	}); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	mem := NewMemoryConversationStore()
	mem.retention = retention
	mem.maxPerUser = maxPerUser
	return map[string]ConversationStore{
		"bolt":   NewBoltConversationStore(db, bucket, retention, maxPerUser),
		"memory": mem,
	}
}

func userMsg(s string) Message      { return Message{Role: RoleUser, Content: s} }
func assistantMsg(s string) Message { return Message{Role: RoleAssistant, Content: s} }
func toolResultMsg(s string) Message {
	return Message{Role: RoleUser, ToolResults: []ToolResult{{ToolCallID: "t1", Content: s}}}
}

func newConv(id, user, cluster, title string, msgs ...Message) *ConversationRecord {
	return &ConversationRecord{
		ID:        id,
		TenantID:  DefaultConversationTenant,
		UserID:    user,
		ClusterID: cluster,
		Title:     title,
		Messages:  msgs,
	}
}

func TestConversationStore_UpsertGetOwnerScoping(t *testing.T) {
	for name, store := range newConvStores(t, DefaultConversationRetention, DefaultConversationsPerUser) {
		t.Run(name, func(t *testing.T) {
			rec := newConv("c1", "alice", "prod", "OOM in payments", userMsg("why is payments OOMing?"), assistantMsg("checking..."))
			if err := store.Upsert(rec); err != nil {
				t.Fatalf("upsert: %v", err)
			}

			got, ok, err := store.Get(DefaultConversationTenant, "alice", "c1")
			if err != nil || !ok {
				t.Fatalf("get alice: ok=%v err=%v", ok, err)
			}
			if got.Title != "OOM in payments" || got.ClusterID != "prod" {
				t.Fatalf("unexpected record: %+v", got)
			}
			if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
				t.Fatalf("timestamps not stamped: %+v", got)
			}

			// A different user must NOT be able to read alice's conversation.
			if _, ok, _ := store.Get(DefaultConversationTenant, "bob", "c1"); ok {
				t.Fatalf("owner scoping breached: bob read alice's conversation")
			}

			// Mutating the returned copy must not affect the store.
			got.Messages = append(got.Messages, userMsg("mutation"))
			reread, _, _ := store.Get(DefaultConversationTenant, "alice", "c1")
			if len(reread.Messages) != 2 {
				t.Fatalf("store returned a live reference, not a copy: %d msgs", len(reread.Messages))
			}
		})
	}
}

func TestConversationStore_ListFilters(t *testing.T) {
	for name, store := range newConvStores(t, DefaultConversationRetention, DefaultConversationsPerUser) {
		t.Run(name, func(t *testing.T) {
			base := time.Now()
			seed := []*ConversationRecord{
				{ID: "a", TenantID: DefaultConversationTenant, UserID: "alice", ClusterID: "prod", Title: "payments OOM", Messages: []Message{userMsg("payments pod keeps OOMing")}, UpdatedAt: base.Add(-3 * time.Hour)},
				{ID: "b", TenantID: DefaultConversationTenant, UserID: "alice", ClusterID: "stage", Title: "ingress 503", Messages: []Message{userMsg("ingress returns 503")}, UpdatedAt: base.Add(-1 * time.Hour)},
				{ID: "c", TenantID: DefaultConversationTenant, UserID: "alice", ClusterID: "prod", Title: "archived one", Messages: []Message{userMsg("old thing")}, UpdatedAt: base.Add(-2 * time.Hour), Archived: true},
				{ID: "d", TenantID: DefaultConversationTenant, UserID: "bob", ClusterID: "prod", Title: "bob's chat", Messages: []Message{userMsg("bob asks about nodes")}, UpdatedAt: base},
			}
			for _, r := range seed {
				if err := store.Upsert(r); err != nil {
					t.Fatalf("seed upsert: %v", err)
				}
			}

			// Alice sees only her non-archived, newest-first.
			got, err := store.List(ConversationQuery{TenantID: DefaultConversationTenant, UserID: "alice"})
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if len(got) != 2 {
				t.Fatalf("expected 2 active for alice, got %d (%v)", len(got), titles(got))
			}
			if got[0].ID != "b" || got[1].ID != "a" {
				t.Fatalf("not newest-first: %v", titles(got))
			}

			// Cluster filter.
			prod, _ := store.List(ConversationQuery{TenantID: DefaultConversationTenant, UserID: "alice", ClusterID: "prod"})
			if len(prod) != 1 || prod[0].ID != "a" {
				t.Fatalf("cluster filter wrong: %v", titles(prod))
			}

			// IncludeArchived.
			all, _ := store.List(ConversationQuery{TenantID: DefaultConversationTenant, UserID: "alice", IncludeArchived: true})
			if len(all) != 3 {
				t.Fatalf("expected 3 incl archived, got %d", len(all))
			}

			// Search over content (not just title).
			s1, _ := store.List(ConversationQuery{TenantID: DefaultConversationTenant, UserID: "alice", Search: "503"})
			if len(s1) != 1 || s1[0].ID != "b" {
				t.Fatalf("search by title failed: %v", titles(s1))
			}
			s2, _ := store.List(ConversationQuery{TenantID: DefaultConversationTenant, UserID: "alice", Search: "keeps oomING"})
			if len(s2) != 1 || s2[0].ID != "a" {
				t.Fatalf("search by content (case-insensitive) failed: %v", titles(s2))
			}

			// Limit.
			lim, _ := store.List(ConversationQuery{TenantID: DefaultConversationTenant, UserID: "alice", Limit: 1})
			if len(lim) != 1 || lim[0].ID != "b" {
				t.Fatalf("limit wrong: %v", titles(lim))
			}

			// Bob never sees alice's.
			bob, _ := store.List(ConversationQuery{TenantID: DefaultConversationTenant, UserID: "bob"})
			if len(bob) != 1 || bob[0].ID != "d" {
				t.Fatalf("owner scoping in List breached: %v", titles(bob))
			}
		})
	}
}

func TestConversationStore_SetTitleArchiveDelete(t *testing.T) {
	for name, store := range newConvStores(t, DefaultConversationRetention, DefaultConversationsPerUser) {
		t.Run(name, func(t *testing.T) {
			_ = store.Upsert(newConv("c1", "alice", "prod", "heuristic title", userMsg("hi")))

			if err := store.SetTitle(DefaultConversationTenant, "alice", "c1", "Refined Title"); err != nil {
				t.Fatalf("set title: %v", err)
			}
			got, _, _ := store.Get(DefaultConversationTenant, "alice", "c1")
			if got.Title != "Refined Title" {
				t.Fatalf("title not updated: %q", got.Title)
			}
			// SetTitle must not clobber the transcript.
			if len(got.Messages) != 1 {
				t.Fatalf("SetTitle clobbered messages: %d", len(got.Messages))
			}

			// SetTitle for a different user is a no-op (owner scoping).
			_ = store.SetTitle(DefaultConversationTenant, "bob", "c1", "hacked")
			got, _, _ = store.Get(DefaultConversationTenant, "alice", "c1")
			if got.Title != "Refined Title" {
				t.Fatalf("cross-user SetTitle mutated record: %q", got.Title)
			}

			if err := store.SetArchived(DefaultConversationTenant, "alice", "c1", true); err != nil {
				t.Fatalf("archive: %v", err)
			}
			active, _ := store.List(ConversationQuery{TenantID: DefaultConversationTenant, UserID: "alice"})
			if len(active) != 0 {
				t.Fatalf("archived record still in active list")
			}

			if err := store.Delete(DefaultConversationTenant, "alice", "c1"); err != nil {
				t.Fatalf("delete: %v", err)
			}
			if _, ok, _ := store.Get(DefaultConversationTenant, "alice", "c1"); ok {
				t.Fatalf("record not deleted")
			}
		})
	}
}

func TestConversationStore_SetMessages(t *testing.T) {
	for name, store := range newConvStores(t, DefaultConversationRetention, DefaultConversationsPerUser) {
		t.Run(name, func(t *testing.T) {
			_ = store.Upsert(newConv("c1", "alice", "prod", "t", userMsg("hi"), assistantMsg("hello")))

			updated := []Message{userMsg("hi"), assistantMsg("hello"), userMsg("follow-up"), assistantMsg("answer")}
			if err := store.SetMessages(DefaultConversationTenant, "alice", "c1", updated); err != nil {
				t.Fatalf("set messages: %v", err)
			}
			got, _, _ := store.Get(DefaultConversationTenant, "alice", "c1")
			if len(got.Messages) != 4 {
				t.Fatalf("messages not replaced: %d", len(got.Messages))
			}
			// Title is preserved when only messages change.
			if got.Title != "t" {
				t.Fatalf("SetMessages clobbered title: %q", got.Title)
			}

			// Owner scoping: another user can't overwrite alice's transcript.
			_ = store.SetMessages(DefaultConversationTenant, "bob", "c1", []Message{userMsg("hacked")})
			got, _, _ = store.Get(DefaultConversationTenant, "alice", "c1")
			if len(got.Messages) != 4 {
				t.Fatalf("cross-user SetMessages mutated record: %d", len(got.Messages))
			}
		})
	}
}

func TestConversationStore_PruneByAgeAndCap(t *testing.T) {
	// Retention 1h, cap 3.
	for name, store := range newConvStores(t, time.Hour, 3) {
		t.Run(name, func(t *testing.T) {
			old := newConv("old", "alice", "prod", "ancient", userMsg("old"))
			old.UpdatedAt = time.Now().Add(-2 * time.Hour) // beyond retention
			old.CreatedAt = old.UpdatedAt
			if err := store.Upsert(old); err != nil {
				t.Fatalf("upsert old: %v", err)
			}

			// Five fresh ones for alice → cap 3 keeps the newest 3.
			for i := 0; i < 5; i++ {
				r := newConv(fmt.Sprintf("f%d", i), "alice", "prod", fmt.Sprintf("fresh %d", i), userMsg("hi"))
				r.UpdatedAt = time.Now().Add(time.Duration(i) * time.Minute)
				r.CreatedAt = r.UpdatedAt
				if err := store.Upsert(r); err != nil {
					t.Fatalf("upsert fresh: %v", err)
				}
			}

			got, _ := store.List(ConversationQuery{TenantID: DefaultConversationTenant, UserID: "alice", IncludeArchived: true})
			if len(got) != 3 {
				t.Fatalf("expected cap=3, got %d (%v)", len(got), titles(got))
			}
			// The age-expired record must be gone.
			if _, ok, _ := store.Get(DefaultConversationTenant, "alice", "old"); ok {
				t.Fatalf("age-expired record survived pruning")
			}
			// Newest 3 are f4,f3,f2.
			if got[0].ID != "f4" || got[2].ID != "f2" {
				t.Fatalf("cap kept the wrong records: %v", titles(got))
			}
		})
	}
}

func TestConversationStore_PerUserCapIsolated(t *testing.T) {
	// cap 2 per user — alice hitting her cap must not evict bob's records.
	for name, store := range newConvStores(t, DefaultConversationRetention, 2) {
		t.Run(name, func(t *testing.T) {
			_ = store.Upsert(newConv("b1", "bob", "prod", "bob one", userMsg("hi")))
			for i := 0; i < 4; i++ {
				_ = store.Upsert(newConv(fmt.Sprintf("a%d", i), "alice", "prod", "alice", userMsg("hi")))
			}
			bob, _ := store.List(ConversationQuery{TenantID: DefaultConversationTenant, UserID: "bob"})
			if len(bob) != 1 {
				t.Fatalf("bob's record evicted by alice's cap: %d", len(bob))
			}
			alice, _ := store.List(ConversationQuery{TenantID: DefaultConversationTenant, UserID: "alice"})
			if len(alice) != 2 {
				t.Fatalf("alice cap not enforced: %d", len(alice))
			}
		})
	}
}

func TestConversationRecordHelpers(t *testing.T) {
	rec := &ConversationRecord{
		Messages: []Message{
			userMsg("  Why is the   payments pod crashing?  "),
			{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "1", Name: "get_pod"}}}, // no text → not visible
			toolResultMsg("huge log blob"),
			assistantMsg("It's OOMKilled."),
		},
	}
	if got := rec.VisibleMessageCount(); got != 2 {
		t.Fatalf("VisibleMessageCount = %d, want 2", got)
	}
	if got := rec.FirstUserMessage(); got != "  Why is the   payments pod crashing?  " {
		t.Fatalf("FirstUserMessage = %q", got)
	}
	if got := rec.Preview(); got != "Why is the payments pod crashing?" {
		t.Fatalf("Preview = %q", got)
	}
}

func TestConversationMatchesSearch_SkipsToolBlobs(t *testing.T) {
	rec := &ConversationRecord{
		Title:    "Node pressure",
		Messages: []Message{userMsg("disk filling up"), toolResultMsg("secret-token-xyz in logs")},
	}
	if conversationMatchesSearch(rec, "secret-token") {
		t.Fatalf("search matched tool-result blob — should only match title + chat text")
	}
	if !conversationMatchesSearch(rec, "disk") {
		t.Fatalf("search missed chat content")
	}
	if !conversationMatchesSearch(rec, "PRESSURE") {
		t.Fatalf("search missed title (case-insensitive)")
	}
}

func TestMergeConversationRecord_FirstTurn(t *testing.T) {
	rec := MergeConversationRecord(ConversationUpsertInput{
		ID: "c1", UserID: "alice", ClusterID: "prod",
		Provider: "anthropic", Model: "claude-sonnet-4-6",
		Messages: []Message{userMsg("why is payments OOMing?")},
		Trigger:  "insight", OriginatingInsightID: "fp-123",
	}, nil)
	if rec.TenantID != DefaultConversationTenant {
		t.Fatalf("tenant default not applied: %q", rec.TenantID)
	}
	if rec.CreatedAt.IsZero() || rec.UpdatedAt.IsZero() {
		t.Fatalf("timestamps not stamped")
	}
	if rec.Title != "why is payments OOMing" {
		t.Fatalf("heuristic title wrong: %q", rec.Title)
	}
	if rec.Trigger != "insight" || rec.OriginatingInsightID != "fp-123" {
		t.Fatalf("origin fields wrong: %+v", rec)
	}
}

func TestMergeConversationRecord_PreservesOnResume(t *testing.T) {
	created := time.Now().Add(-48 * time.Hour)
	prevUsage := &Usage{InputTokens: 4000, OutputTokens: 200}
	existing := &ConversationRecord{
		ID: "c1", TenantID: DefaultConversationTenant, UserID: "alice", ClusterID: "prod",
		Title: "LLM-refined title", CreatedAt: created, UpdatedAt: created,
		Trigger: "insight", OriginatingInsightID: "fp-123", Archived: true,
		LastRoundUsage: prevUsage,
	}

	// A later turn that arrives as a plain "manual" message, produced NO usage
	// (e.g. it errored before the provider replied), and carries no insight id.
	rec := MergeConversationRecord(ConversationUpsertInput{
		ID: "c1", UserID: "alice", ClusterID: "prod",
		Provider: "anthropic", Model: "claude-sonnet-4-6",
		Messages: []Message{userMsg("why is payments OOMing?"), assistantMsg("checking")},
		Trigger:  "manual", OriginatingInsightID: "", LastRoundUsage: nil,
		Now: time.Now(),
	}, existing)

	if !rec.CreatedAt.Equal(created) {
		t.Fatalf("CreatedAt not preserved: %v", rec.CreatedAt)
	}
	if rec.Title != "LLM-refined title" {
		t.Fatalf("refined Title not preserved: %q", rec.Title)
	}
	if !rec.Archived {
		t.Fatalf("Archived flag not preserved")
	}
	if rec.Trigger != "insight" {
		t.Fatalf("origin Trigger overwritten by later 'manual' turn: %q", rec.Trigger)
	}
	if rec.OriginatingInsightID != "fp-123" {
		t.Fatalf("OriginatingInsightID not preserved on resume: %q", rec.OriginatingInsightID)
	}
	if rec.LastRoundUsage == nil || rec.LastRoundUsage.InputTokens != 4000 {
		t.Fatalf("LastRoundUsage seed lost when the resumed turn produced none: %+v", rec.LastRoundUsage)
	}
	// UpdatedAt should still advance.
	if !rec.UpdatedAt.After(created) {
		t.Fatalf("UpdatedAt did not advance")
	}
}

func TestMergeConversationRecord_FreshUsageWins(t *testing.T) {
	existing := &ConversationRecord{
		ID: "c1", UserID: "alice", CreatedAt: time.Now().Add(-time.Hour),
		LastRoundUsage: &Usage{InputTokens: 100},
	}
	rec := MergeConversationRecord(ConversationUpsertInput{
		ID: "c1", UserID: "alice", Messages: []Message{userMsg("hi")},
		LastRoundUsage: &Usage{InputTokens: 9999}, Now: time.Now(),
	}, existing)
	if rec.LastRoundUsage.InputTokens != 9999 {
		t.Fatalf("fresh usage should win over preserved: %+v", rec.LastRoundUsage)
	}
}

func titles(recs []ConversationRecord) []string {
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = r.ID
	}
	return out
}
