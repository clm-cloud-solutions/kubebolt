package copilot

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

// Conversation persistence — the durable transcript of a Kobi chat so the
// operator can refresh the browser, re-login, or come back days later and
// resume where they left off.
//
// This mirrors the InsightStore / AgentRecord pattern exactly: an extracted
// interface (so tests use a memory impl and SaaS v1 can drop in a Postgres
// impl with zero rekey), tenant-prefixed composite keys, JSON-encoded values,
// forward-compat field additions, and bounded pruning. The chat loop itself
// stays stateless — persistence is a write-through on completion, and resume
// just pre-populates the messages array the existing loop already consumes.
//
// Ownership: conversations are PERSONAL. The owning key dimension is the
// user; tenant + cluster are attributes. Every read/write enforces the
// (tenantID, userID) tuple so a user can never reach another user's history.

const (
	// DefaultConversationRetention drops conversations whose UpdatedAt is
	// older than this horizon. Override via
	// KUBEBOLT_COPILOT_CONVERSATION_RETENTION_HORIZON.
	DefaultConversationRetention = 90 * 24 * time.Hour
	// DefaultConversationsPerUser caps how many conversations one user keeps;
	// the oldest (by UpdatedAt) drop out once exceeded. Override via
	// KUBEBOLT_COPILOT_CONVERSATION_MAX_PER_USER.
	DefaultConversationsPerUser = 200

	conversationPreviewMaxLen = 140
	conversationTitleMaxLen   = 80
	// DefaultConversationTenant is the single-tenant OSS tenant id. SaaS
	// swaps this for the resolved tenant without a key migration.
	DefaultConversationTenant = "default"
	// FallbackConversationUser is used when auth is disabled (ContextUserID
	// is empty) so single-user no-auth installs still get persistence.
	FallbackConversationUser = "local"
)

// ConversationRecord is the persistent shape of one Kobi conversation. The
// Messages slice is the canonical LLM transcript (post-compaction — exactly
// what the model last saw), so resuming a long conversation stays within the
// session budget and the existing auto-compact loop continues seamlessly.
type ConversationRecord struct {
	ID        string    `json:"id"`        // uuid, stable for the conversation's life
	TenantID  string    `json:"tenantId"`  // "default" in OSS
	UserID    string    `json:"userId"`    // owner — primary scope
	ClusterID string    `json:"clusterId"` // cluster the chat ran against
	Title     string    `json:"title"`     // auto-generated (heuristic, refined by LLM)
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`

	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`

	// Messages is the full transcript including tool calls + tool results.
	// The rich inline cards (action proposals, metric charts) live inside
	// tool-result content as JSON, including the frontend's in-place
	// execution-outcome mutations — so they rehydrate verbatim on resume.
	Messages []Message `json:"messages"`

	// LastRoundUsage is the provider-reported usage of the final round, so a
	// resumed conversation seeds the auto-compact decision accurately on its
	// first new turn (same hint the frontend normally carries).
	LastRoundUsage *Usage `json:"lastRoundUsage,omitempty"`

	Trigger string `json:"trigger,omitempty"`
	// OriginatingInsightID links a conversation that began from an insight
	// (the stable fingerprint from the insights-persistence work) so the
	// insight detail can deep-link "Kobi analyzed this" back to resume.
	OriginatingInsightID string `json:"originatingInsightId,omitempty"`

	Archived bool `json:"archived,omitempty"`
}

// VisibleMessageCount counts the turns a human sees in the panel: user
// prompts and assistant replies with text. Tool-result-only turns and empty
// assistant tool-call stubs don't count.
func (r *ConversationRecord) VisibleMessageCount() int {
	n := 0
	for _, m := range r.Messages {
		switch m.Role {
		case RoleUser:
			if len(m.ToolResults) == 0 && strings.TrimSpace(m.Content) != "" {
				n++
			}
		case RoleAssistant:
			if strings.TrimSpace(m.Content) != "" {
				n++
			}
		}
	}
	return n
}

// FirstUserMessage returns the operator's first literal prompt (no session
// context prefix — the stored transcript is the canonical, un-prefixed one).
func (r *ConversationRecord) FirstUserMessage() string {
	for _, m := range r.Messages {
		if m.Role == RoleUser && len(m.ToolResults) == 0 && strings.TrimSpace(m.Content) != "" {
			return m.Content
		}
	}
	return ""
}

// LastAssistantMessage returns the most recent assistant turn that carried
// text — used as context for title generation.
func (r *ConversationRecord) LastAssistantMessage() string {
	for i := len(r.Messages) - 1; i >= 0; i-- {
		if r.Messages[i].Role == RoleAssistant && strings.TrimSpace(r.Messages[i].Content) != "" {
			return r.Messages[i].Content
		}
	}
	return ""
}

// Preview is the first user prompt, single-lined and truncated, for the
// history list.
func (r *ConversationRecord) Preview() string {
	return truncateRunes(collapseWhitespace(r.FirstUserMessage()), conversationPreviewMaxLen)
}

// ConversationStoreConfigFromEnv reads the retention horizon + per-user cap
// from the environment, falling back to the package defaults on an unset or
// invalid value (with a WARN, mirroring config/ingest_channel.go).
func ConversationStoreConfigFromEnv() (time.Duration, int) {
	retention := DefaultConversationRetention
	if v := os.Getenv("KUBEBOLT_COPILOT_CONVERSATION_RETENTION_HORIZON"); v != "" {
		switch d, err := time.ParseDuration(v); {
		case err != nil:
			log.Printf("WARN copilot: KUBEBOLT_COPILOT_CONVERSATION_RETENTION_HORIZON=%q is not a valid Go duration — using default %s", v, retention)
		case d <= 0:
			log.Printf("WARN copilot: KUBEBOLT_COPILOT_CONVERSATION_RETENTION_HORIZON=%s must be > 0 — using default %s", d, retention)
		default:
			retention = d
		}
	}
	maxPerUser := DefaultConversationsPerUser
	if v := os.Getenv("KUBEBOLT_COPILOT_CONVERSATION_MAX_PER_USER"); v != "" {
		if n, err := strconv.Atoi(v); err != nil || n <= 0 {
			log.Printf("WARN copilot: KUBEBOLT_COPILOT_CONVERSATION_MAX_PER_USER=%q must be a positive integer — using default %d", v, maxPerUser)
		} else {
			maxPerUser = n
		}
	}
	return retention, maxPerUser
}

// ConversationQuery filters a List call. Zero-valued fields are unbounded.
type ConversationQuery struct {
	TenantID        string
	UserID          string
	ClusterID       string // "" = any cluster
	Search          string // "" = any; case-insensitive over title + message content
	IncludeArchived bool
	Limit           int // 0 = no cap (applied after newest-first sort)
}

// ConversationStore persists Kobi conversations per user. Extracted as an
// interface (like InsightStore / AgentStore) so tests use the memory impl and
// SaaS v1 can drop in a Postgres impl with zero rekey — keys are already
// tenant/user-prefixed.
//
// Implementations must be safe for concurrent use: the chat handler upserts
// from the request goroutine while a background title goroutine calls
// SetTitle and the API reads via List/Get.
type ConversationStore interface {
	// Upsert writes the full record, replacing any prior copy with the same
	// (tenantID, userID, id). Implementations prune on write.
	Upsert(rec *ConversationRecord) error
	// Get returns one record by identity, scoped to the owner.
	Get(tenantID, userID, id string) (*ConversationRecord, bool, error)
	// List returns records matching the query, newest UpdatedAt first.
	List(q ConversationQuery) ([]ConversationRecord, error)
	// Delete removes one record, scoped to the owner. No-op if absent.
	Delete(tenantID, userID, id string) error
	// SetTitle updates only the Title field via load-modify-save, so a
	// background title refinement never clobbers concurrent message appends.
	// Also clears Archived when renaming? No — title only.
	SetTitle(tenantID, userID, id, title string) error
	// SetArchived flips the Archived flag, scoped to the owner.
	SetArchived(tenantID, userID, id string, archived bool) error
	// SetMessages replaces the transcript via load-modify-save. Used to
	// persist between-turn client state (e.g. an action-proposal's executed
	// outcome) so a refresh before the next chat turn doesn't resurrect an
	// already-run action's Execute button.
	SetMessages(tenantID, userID, id string, msgs []Message) error
}

// NewConversationID returns a fresh conversation id.
func NewConversationID() string { return uuid.NewString() }

// ConversationUpsertInput carries one turn's data for building the record to
// persist. Extracted so the merge/preservation logic is unit-testable apart
// from the SSE chat handler.
type ConversationUpsertInput struct {
	ID                   string
	TenantID             string
	UserID               string
	ClusterID            string
	Provider             string
	Model                string
	Messages             []Message
	Trigger              string
	OriginatingInsightID string
	LastRoundUsage       *Usage
	Now                  time.Time
}

// MergeConversationRecord builds the ConversationRecord to persist for a turn,
// preserving identity-stable fields from `existing` (nil on the first turn).
// Preserved across resumes: CreatedAt, the (possibly LLM-refined) Title, the
// Archived flag, the ORIGIN Trigger, the OriginatingInsightID, and — crucially
// — the prior round's LastRoundUsage when THIS turn produced none (e.g. it
// errored before any provider response). Without the last two, resuming a
// conversation and erroring on the first turn would silently drop its insight
// provenance / origin / usage seed.
func MergeConversationRecord(in ConversationUpsertInput, existing *ConversationRecord) *ConversationRecord {
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	tenant := in.TenantID
	if tenant == "" {
		tenant = DefaultConversationTenant
	}
	rec := &ConversationRecord{
		ID:                   in.ID,
		TenantID:             tenant,
		UserID:               in.UserID,
		ClusterID:            in.ClusterID,
		CreatedAt:            now,
		UpdatedAt:            now,
		Provider:             in.Provider,
		Model:                in.Model,
		Messages:             in.Messages,
		Trigger:              in.Trigger,
		OriginatingInsightID: in.OriginatingInsightID,
		LastRoundUsage:       in.LastRoundUsage,
	}
	if existing != nil {
		rec.CreatedAt = existing.CreatedAt
		if existing.Title != "" {
			rec.Title = existing.Title
		}
		rec.Archived = existing.Archived
		// The conversation's origin trigger is a session-level attribute; later
		// turns (which arrive as "manual") must not overwrite it.
		if existing.Trigger != "" {
			rec.Trigger = existing.Trigger
		}
		if rec.OriginatingInsightID == "" {
			rec.OriginatingInsightID = existing.OriginatingInsightID
		}
		if rec.LastRoundUsage == nil {
			rec.LastRoundUsage = existing.LastRoundUsage
		}
	}
	if rec.Title == "" {
		rec.Title = HeuristicTitle(rec.FirstUserMessage())
	}
	return rec
}

// conversationKey is the BoltDB key: tenant/user/id. Tenant+user-prefixed so
// SaaS multi-tenant needs no rekey and a user's history range-scans cleanly.
func conversationKey(tenantID, userID, id string) []byte {
	return []byte(tenantID + "/" + userID + "/" + id)
}

func matchesConversationQuery(rec *ConversationRecord, q ConversationQuery) bool {
	if q.TenantID != "" && rec.TenantID != q.TenantID {
		return false
	}
	if q.UserID != "" && rec.UserID != q.UserID {
		return false
	}
	if q.ClusterID != "" && rec.ClusterID != q.ClusterID {
		return false
	}
	if !q.IncludeArchived && rec.Archived {
		return false
	}
	if q.Search != "" && !conversationMatchesSearch(rec, q.Search) {
		return false
	}
	return true
}

// conversationMatchesSearch does a case-insensitive substring match over the
// title plus every user/assistant message's text content. Tool-result blobs
// (logs / YAML) are skipped so a search for "payments" matches what the
// operator actually discussed, not incidental tool output.
func conversationMatchesSearch(rec *ConversationRecord, search string) bool {
	needle := strings.ToLower(strings.TrimSpace(search))
	if needle == "" {
		return true
	}
	if strings.Contains(strings.ToLower(rec.Title), needle) {
		return true
	}
	for _, m := range rec.Messages {
		if m.Role != RoleUser && m.Role != RoleAssistant {
			continue
		}
		if len(m.ToolResults) > 0 {
			continue
		}
		if m.Content != "" && strings.Contains(strings.ToLower(m.Content), needle) {
			return true
		}
	}
	return false
}

func sortConversationsNewestFirst(out []ConversationRecord) {
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
}

func cloneConversation(rec *ConversationRecord) ConversationRecord {
	cp := *rec
	cp.Messages = append([]Message(nil), rec.Messages...)
	if rec.LastRoundUsage != nil {
		u := *rec.LastRoundUsage
		cp.LastRoundUsage = &u
	}
	return cp
}

func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return strings.TrimSpace(string(r[:max])) + "…"
}

// ─── BoltDB implementation ────────────────────────────────────────

// BoltConversationStore is the production ConversationStore — backed by the
// same BoltDB file as users/agents/insights. One bucket
// (`copilot_conversations`) holds JSON ConversationRecord values keyed by
// `<tenantID>/<userID>/<id>`.
type BoltConversationStore struct {
	db         *bolt.DB
	bucket     []byte
	retention  time.Duration
	maxPerUser int
	mu         sync.Mutex
}

// NewBoltConversationStore wires the store to a BoltDB handle + bucket. A
// non-positive retention/maxPerUser falls back to the package defaults. The
// bucket must already exist (created at boot in auth.NewStore).
func NewBoltConversationStore(db *bolt.DB, bucket []byte, retention time.Duration, maxPerUser int) *BoltConversationStore {
	if retention <= 0 {
		retention = DefaultConversationRetention
	}
	if maxPerUser <= 0 {
		maxPerUser = DefaultConversationsPerUser
	}
	return &BoltConversationStore{db: db, bucket: bucket, retention: retention, maxPerUser: maxPerUser}
}

func (s *BoltConversationStore) Upsert(rec *ConversationRecord) error {
	if rec == nil {
		return fmt.Errorf("nil ConversationRecord")
	}
	if rec.ID == "" {
		return fmt.Errorf("ConversationRecord missing id")
	}
	if rec.UserID == "" {
		return fmt.Errorf("ConversationRecord missing userId")
	}
	if rec.TenantID == "" {
		rec.TenantID = DefaultConversationTenant
	}
	now := time.Now()
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = now
	}
	if rec.UpdatedAt.IsZero() {
		rec.UpdatedAt = now
	}
	payload, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshal ConversationRecord: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(s.bucket)
		if b == nil {
			return fmt.Errorf("bucket %s not found", s.bucket)
		}
		if err := b.Put(conversationKey(rec.TenantID, rec.UserID, rec.ID), payload); err != nil {
			return err
		}
		return s.pruneLocked(b, rec.UserID)
	})
}

// pruneLocked drops age-expired records (any user) and enforces the per-user
// cap for the just-written user. Called inside the Upsert transaction.
func (s *BoltConversationStore) pruneLocked(b *bolt.Bucket, userID string) error {
	cutoff := time.Now().Add(-s.retention)

	type keyed struct {
		key     []byte
		updated time.Time
	}
	var userRecords []keyed
	var expired [][]byte

	err := b.ForEach(func(k, v []byte) error {
		var rec ConversationRecord
		if err := json.Unmarshal(v, &rec); err != nil {
			return nil // skip corrupt
		}
		if !rec.UpdatedAt.IsZero() && rec.UpdatedAt.Before(cutoff) {
			expired = append(expired, append([]byte(nil), k...))
			return nil
		}
		if rec.UserID == userID {
			userRecords = append(userRecords, keyed{key: append([]byte(nil), k...), updated: rec.UpdatedAt})
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, k := range expired {
		if err := b.Delete(k); err != nil {
			return err
		}
	}
	// Enforce per-user cap: keep the newest maxPerUser, drop the rest.
	if len(userRecords) > s.maxPerUser {
		sort.Slice(userRecords, func(i, j int) bool {
			return userRecords[i].updated.After(userRecords[j].updated)
		})
		for _, kr := range userRecords[s.maxPerUser:] {
			if err := b.Delete(kr.key); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *BoltConversationStore) Get(tenantID, userID, id string) (*ConversationRecord, bool, error) {
	if tenantID == "" {
		tenantID = DefaultConversationTenant
	}
	var rec ConversationRecord
	var found bool
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(s.bucket)
		if b == nil {
			return fmt.Errorf("bucket %s not found", s.bucket)
		}
		raw := b.Get(conversationKey(tenantID, userID, id))
		if raw == nil {
			return nil
		}
		if err := json.Unmarshal(raw, &rec); err != nil {
			return fmt.Errorf("unmarshal ConversationRecord: %w", err)
		}
		found = true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if !found {
		return nil, false, nil
	}
	return &rec, true, nil
}

func (s *BoltConversationStore) List(q ConversationQuery) ([]ConversationRecord, error) {
	if q.TenantID == "" {
		q.TenantID = DefaultConversationTenant
	}
	var out []ConversationRecord
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(s.bucket)
		if b == nil {
			return fmt.Errorf("bucket %s not found", s.bucket)
		}
		return b.ForEach(func(_, v []byte) error {
			var rec ConversationRecord
			if err := json.Unmarshal(v, &rec); err != nil {
				return nil // skip corrupt — one bad value shouldn't blank history
			}
			if matchesConversationQuery(&rec, q) {
				out = append(out, rec)
			}
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	sortConversationsNewestFirst(out)
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

func (s *BoltConversationStore) Delete(tenantID, userID, id string) error {
	if tenantID == "" {
		tenantID = DefaultConversationTenant
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(s.bucket)
		if b == nil {
			return fmt.Errorf("bucket %s not found", s.bucket)
		}
		return b.Delete(conversationKey(tenantID, userID, id))
	})
}

func (s *BoltConversationStore) SetTitle(tenantID, userID, id, title string) error {
	return s.mutate(tenantID, userID, id, func(rec *ConversationRecord) {
		rec.Title = title
	})
}

func (s *BoltConversationStore) SetArchived(tenantID, userID, id string, archived bool) error {
	return s.mutate(tenantID, userID, id, func(rec *ConversationRecord) {
		rec.Archived = archived
	})
}

func (s *BoltConversationStore) SetMessages(tenantID, userID, id string, msgs []Message) error {
	return s.mutate(tenantID, userID, id, func(rec *ConversationRecord) {
		rec.Messages = msgs
	})
}

// mutate loads a record, applies fn, and writes it back inside one
// transaction so field-level updates (title/archived) never clobber a
// concurrent full-transcript Upsert.
func (s *BoltConversationStore) mutate(tenantID, userID, id string, fn func(*ConversationRecord)) error {
	if tenantID == "" {
		tenantID = DefaultConversationTenant
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(s.bucket)
		if b == nil {
			return fmt.Errorf("bucket %s not found", s.bucket)
		}
		key := conversationKey(tenantID, userID, id)
		raw := b.Get(key)
		if raw == nil {
			return nil // no-op when absent
		}
		var rec ConversationRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			return fmt.Errorf("unmarshal ConversationRecord: %w", err)
		}
		fn(&rec)
		rec.UpdatedAt = time.Now()
		payload, err := json.Marshal(&rec)
		if err != nil {
			return fmt.Errorf("marshal ConversationRecord: %w", err)
		}
		return b.Put(key, payload)
	})
}

// ─── Memory implementation (tests) ────────────────────────────────

// MemoryConversationStore is the in-memory ConversationStore for tests. Same
// semantics as BoltConversationStore; thread-safe.
type MemoryConversationStore struct {
	mu         sync.RWMutex
	records    map[string]*ConversationRecord // key = conversationKey(...) as string
	retention  time.Duration
	maxPerUser int
}

func NewMemoryConversationStore() *MemoryConversationStore {
	return &MemoryConversationStore{
		records:    make(map[string]*ConversationRecord),
		retention:  DefaultConversationRetention,
		maxPerUser: DefaultConversationsPerUser,
	}
}

func (s *MemoryConversationStore) Upsert(rec *ConversationRecord) error {
	if rec == nil {
		return fmt.Errorf("nil ConversationRecord")
	}
	if rec.ID == "" {
		return fmt.Errorf("ConversationRecord missing id")
	}
	if rec.UserID == "" {
		return fmt.Errorf("ConversationRecord missing userId")
	}
	if rec.TenantID == "" {
		rec.TenantID = DefaultConversationTenant
	}
	now := time.Now()
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = now
	}
	if rec.UpdatedAt.IsZero() {
		rec.UpdatedAt = now
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := cloneConversation(rec)
	s.records[string(conversationKey(rec.TenantID, rec.UserID, rec.ID))] = &cp
	s.pruneLocked(rec.UserID)
	return nil
}

func (s *MemoryConversationStore) pruneLocked(userID string) {
	cutoff := time.Now().Add(-s.retention)
	var userRecords []*ConversationRecord
	for k, rec := range s.records {
		if !rec.UpdatedAt.IsZero() && rec.UpdatedAt.Before(cutoff) {
			delete(s.records, k)
			continue
		}
		if rec.UserID == userID {
			userRecords = append(userRecords, rec)
		}
	}
	if len(userRecords) > s.maxPerUser {
		sort.Slice(userRecords, func(i, j int) bool {
			return userRecords[i].UpdatedAt.After(userRecords[j].UpdatedAt)
		})
		for _, rec := range userRecords[s.maxPerUser:] {
			delete(s.records, string(conversationKey(rec.TenantID, rec.UserID, rec.ID)))
		}
	}
}

func (s *MemoryConversationStore) Get(tenantID, userID, id string) (*ConversationRecord, bool, error) {
	if tenantID == "" {
		tenantID = DefaultConversationTenant
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.records[string(conversationKey(tenantID, userID, id))]
	if !ok {
		return nil, false, nil
	}
	cp := cloneConversation(rec)
	return &cp, true, nil
}

func (s *MemoryConversationStore) List(q ConversationQuery) ([]ConversationRecord, error) {
	if q.TenantID == "" {
		q.TenantID = DefaultConversationTenant
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []ConversationRecord
	for _, rec := range s.records {
		if matchesConversationQuery(rec, q) {
			out = append(out, cloneConversation(rec))
		}
	}
	sortConversationsNewestFirst(out)
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

func (s *MemoryConversationStore) Delete(tenantID, userID, id string) error {
	if tenantID == "" {
		tenantID = DefaultConversationTenant
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.records, string(conversationKey(tenantID, userID, id)))
	return nil
}

func (s *MemoryConversationStore) SetTitle(tenantID, userID, id, title string) error {
	return s.mutate(tenantID, userID, id, func(rec *ConversationRecord) { rec.Title = title })
}

func (s *MemoryConversationStore) SetArchived(tenantID, userID, id string, archived bool) error {
	return s.mutate(tenantID, userID, id, func(rec *ConversationRecord) { rec.Archived = archived })
}

func (s *MemoryConversationStore) SetMessages(tenantID, userID, id string, msgs []Message) error {
	return s.mutate(tenantID, userID, id, func(rec *ConversationRecord) {
		rec.Messages = append([]Message(nil), msgs...)
	})
}

func (s *MemoryConversationStore) mutate(tenantID, userID, id string, fn func(*ConversationRecord)) error {
	if tenantID == "" {
		tenantID = DefaultConversationTenant
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[string(conversationKey(tenantID, userID, id))]
	if !ok {
		return nil
	}
	fn(rec)
	rec.UpdatedAt = time.Now()
	return nil
}
