package copilot

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Feedback is a user's 👍/👎 on one Kobi answer (doc #67 §4.4). It names the
// turn the answer closed (Message.TurnID, the SessionRecord's ID) inside the
// user's own conversation. The comment is the user's words: it lives here, with
// the conversation, inside the org — never in a metric or the operator's view.
type Feedback struct {
	TenantID       string    `json:"tenantId"`
	UserID         string    `json:"userId"`
	ConversationID string    `json:"conversationId"`
	TurnID         string    `json:"turnId"`
	Rating         string    `json:"rating"`           // FeedbackUp | FeedbackDown
	Reason         string    `json:"reason,omitempty"` // a 👎's reason, from FeedbackReasons
	Comment        string    `json:"comment,omitempty"`
	ClusterID      string    `json:"clusterId,omitempty"`
	TeamID         string    `json:"teamId,omitempty"`
	Provider       string    `json:"provider,omitempty"`
	Model          string    `json:"model,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

const (
	FeedbackUp   = "up"
	FeedbackDown = "down"
	// FeedbackCommentMax bounds the comment: a note, not a document.
	FeedbackCommentMax = 2000
)

// FeedbackReasons are the reasons a 👎 can carry: the answer was wrong,
// incomplete, did not answer what was asked, or came too slowly.
var FeedbackReasons = map[string]bool{"incorrect": true, "incomplete": true, "off_topic": true, "slow": true}

// Validate checks a rating as the user sent it: up or down, a known reason
// only on a 👎, a comment of bounded size.
func (f *Feedback) Validate() error {
	switch f.Rating {
	case FeedbackUp:
		if f.Reason != "" {
			return fmt.Errorf("a 👍 takes no reason")
		}
	case FeedbackDown:
		if f.Reason != "" && !FeedbackReasons[f.Reason] {
			return fmt.Errorf("unknown reason %q", f.Reason)
		}
	default:
		return fmt.Errorf("rating must be %q or %q", FeedbackUp, FeedbackDown)
	}
	f.Comment = strings.TrimSpace(f.Comment)
	if len(f.Comment) > FeedbackCommentMax {
		return fmt.Errorf("comment is longer than %d characters", FeedbackCommentMax)
	}
	if f.TurnID == "" || f.ConversationID == "" {
		return fmt.Errorf("turnId and conversationId are required")
	}
	return nil
}

// FeedbackStore keeps the ratings: one per (org, user, turn), the last one given.
type FeedbackStore interface {
	// Set stores the user's rating of a turn, replacing an earlier one, and
	// returns the one it replaced (nil when there was none).
	Set(f Feedback) (*Feedback, error)
	// Delete withdraws the user's rating of a turn and returns it (nil when
	// there was none).
	Delete(tenantID, userID, turnID string) (*Feedback, error)
	// ForConversation lists the user's ratings in one conversation.
	ForConversation(tenantID, userID, conversationID string) ([]Feedback, error)
	// DeleteConversation drops the user's ratings in one conversation: the
	// user deleted it, and a 👎's comment goes with the transcript it rated.
	DeleteConversation(tenantID, userID, conversationID string) (int, error)
	// PruneOrg drops an org's ratings last touched before `before` — the
	// retention pass, with the conversations' horizon.
	PruneOrg(tenantID string, before time.Time) (int, error)
}

// BoltFeedbackStore is the FeedbackStore of the single-tenant installs (OSS,
// and EE without Postgres): one JSON value per tenant\0user\0turn key.
type BoltFeedbackStore struct {
	db     *bolt.DB
	bucket []byte
}

var _ FeedbackStore = (*BoltFeedbackStore)(nil)

// NewBoltFeedbackStore creates the bucket when missing.
func NewBoltFeedbackStore(db *bolt.DB, bucket []byte) (*BoltFeedbackStore, error) {
	if err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(bucket)
		return err
	}); err != nil {
		return nil, fmt.Errorf("create %s bucket: %w", bucket, err)
	}
	return &BoltFeedbackStore{db: db, bucket: bucket}, nil
}

func feedbackKey(tenantID, userID, turnID string) []byte {
	return []byte(tenantID + "\x00" + userID + "\x00" + turnID)
}

func (s *BoltFeedbackStore) Set(f Feedback) (*Feedback, error) {
	var prev *Feedback
	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(s.bucket)
		key := feedbackKey(f.TenantID, f.UserID, f.TurnID)
		if raw := b.Get(key); raw != nil {
			var old Feedback
			if err := json.Unmarshal(raw, &old); err == nil {
				prev = &old
				f.CreatedAt = old.CreatedAt
			}
		}
		now := time.Now().UTC()
		if f.CreatedAt.IsZero() {
			f.CreatedAt = now
		}
		f.UpdatedAt = now
		raw, err := json.Marshal(f)
		if err != nil {
			return err
		}
		return b.Put(key, raw)
	})
	return prev, err
}

func (s *BoltFeedbackStore) Delete(tenantID, userID, turnID string) (*Feedback, error) {
	var prev *Feedback
	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(s.bucket)
		key := feedbackKey(tenantID, userID, turnID)
		raw := b.Get(key)
		if raw == nil {
			return nil
		}
		var old Feedback
		if err := json.Unmarshal(raw, &old); err == nil {
			prev = &old
		}
		return b.Delete(key)
	})
	return prev, err
}

func (s *BoltFeedbackStore) ForConversation(tenantID, userID, conversationID string) ([]Feedback, error) {
	var out []Feedback
	prefix := []byte(tenantID + "\x00" + userID + "\x00")
	err := s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(s.bucket).Cursor()
		for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
			var f Feedback
			if err := json.Unmarshal(v, &f); err != nil {
				continue
			}
			if f.ConversationID == conversationID {
				out = append(out, f)
			}
		}
		return nil
	})
	return out, err
}

func (s *BoltFeedbackStore) DeleteConversation(tenantID, userID, conversationID string) (int, error) {
	n := 0
	prefix := []byte(tenantID + "\x00" + userID + "\x00")
	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(s.bucket)
		var gone [][]byte
		c := b.Cursor()
		for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
			var f Feedback
			if err := json.Unmarshal(v, &f); err == nil && f.ConversationID == conversationID {
				gone = append(gone, append([]byte(nil), k...))
			}
		}
		for _, k := range gone {
			if err := b.Delete(k); err != nil {
				return err
			}
		}
		n = len(gone)
		return nil
	})
	return n, err
}

func (s *BoltFeedbackStore) PruneOrg(tenantID string, before time.Time) (int, error) {
	n := 0
	prefix := []byte(tenantID + "\x00")
	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(s.bucket)
		var stale [][]byte
		c := b.Cursor()
		for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
			var f Feedback
			if err := json.Unmarshal(v, &f); err == nil && f.UpdatedAt.Before(before) {
				stale = append(stale, append([]byte(nil), k...))
			}
		}
		for _, k := range stale {
			if err := b.Delete(k); err != nil {
				return err
			}
		}
		n = len(stale)
		return nil
	})
	return n, err
}
