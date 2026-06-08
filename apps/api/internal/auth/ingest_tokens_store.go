// Package auth — ingest_tokens_store hosts the dedicated BoltDB store for
// long-lived ingest bearer tokens (the "kb_" tokens the kubebolt-agent gRPC
// channel and the Prom remote_write receiver authenticate with). W1 seam: OSS
// uses BoltIngestTokenStore, EE swaps a Postgres impl.
//
// Previously these tokens were inlined inside each Tenant record. That coupled
// token storage to tenant storage and blocked an independent Postgres backing
// for SaaS/EE. They now live in their own buckets, keyed by tenant + id, with a
// hash index for O(1) lookup. A one-time boot migration (MigrateInlinedTokens)
// moves any legacy inlined tokens over.
//
// Layout (two buckets, sharing kubebolt.db):
//
//	ingest_tokens             key: tenant_id/token_id      value: IngestToken JSON
//	ingest_token_hash_index   key: hex(sha256(plaintext))  value: tenant_id/token_id
package auth

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

var (
	ingestTokensBucket      = []byte("ingest_tokens")
	ingestTokenHashIdxBucket = []byte("ingest_token_hash_index")
)

// IngestToken is a long-lived bearer credential issued for agents that cannot
// use Kubernetes TokenReview (e.g. SaaS / cross-cluster). Stored only as a
// SHA-256 hash; plaintext is returned once at issue/rotate.
type IngestToken struct {
	ID         string     `json:"id"`
	TenantID   string     `json:"tenantId"` // owner; was implicit when inlined in Tenant
	Hash       string     `json:"hash"`
	Prefix     string     `json:"prefix"`
	Label      string     `json:"label"`
	CreatedAt  time.Time  `json:"createdAt"`
	CreatedBy  string     `json:"createdBy"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
	RevokedAt  *time.Time `json:"revokedAt,omitempty"`

	// ClusterID is the kube-system namespace UID of the cluster this token is
	// scoped to. Empty = "any cluster" (legacy tokens + explicitly unscoped).
	// Matches the `cluster_id` metric label the agent stamps on each sample.
	ClusterID string `json:"clusterId,omitempty"`
}

// Active reports whether the token is currently valid: not revoked and not
// past its optional expiration.
func (t *IngestToken) Active(now time.Time) bool {
	if t.RevokedAt != nil {
		return false
	}
	if t.ExpiresAt != nil && now.After(*t.ExpiresAt) {
		return false
	}
	return true
}

// IngestTokenStore is the W1 seam for ingest tokens. Lookup validates the TOKEN
// only (revoked/expired); the caller validates the owning tenant (Disabled)
// since that's the TenantStore's concern.
type IngestTokenStore interface {
	Issue(tenantID, clusterID, label, createdBy string, ttl *time.Duration) (string, *IngestToken, error)
	Revoke(tenantID, tokenID string) error
	Rotate(tenantID, tokenID, createdBy string) (string, *IngestToken, error)
	Lookup(plaintext string) (*IngestToken, error)
	MarkUsed(tenantID, tokenID string, when time.Time) error
	ListByTenant(tenantID string) ([]IngestToken, error)
}

func ingestKey(tenantID, tokenID string) []byte { return []byte(tenantID + "/" + tokenID) }

// BoltIngestTokenStore is the OSS BoltDB impl.
type BoltIngestTokenStore struct {
	db    *bolt.DB
	nowFn func() time.Time

	// markUsedAt debounces LastUsedAt persistence (one write per token/minute).
	markUsedMu sync.Mutex
	markUsedAt map[string]time.Time
}

var _ IngestTokenStore = (*BoltIngestTokenStore)(nil)

func NewIngestTokenStore(db *bolt.DB) (*BoltIngestTokenStore, error) {
	if db == nil {
		return nil, fmt.Errorf("ingest token store: nil db")
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		for _, b := range [][]byte{ingestTokensBucket, ingestTokenHashIdxBucket} {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("creating ingest token buckets: %w", err)
	}
	return &BoltIngestTokenStore{db: db, nowFn: time.Now, markUsedAt: map[string]time.Time{}}, nil
}

func (s *BoltIngestTokenStore) Issue(tenantID, clusterID, label, createdBy string, ttl *time.Duration) (string, *IngestToken, error) {
	if tenantID == "" {
		return "", nil, fmt.Errorf("tenantID is required")
	}
	plaintext, err := generateTokenPlaintext()
	if err != nil {
		return "", nil, err
	}
	hash := hashToken(plaintext)
	now := s.nowFn().UTC()
	tok := &IngestToken{
		ID:        uuid.New().String(),
		TenantID:  tenantID,
		Hash:      hash,
		Prefix:    tokenDisplayPrefix(plaintext),
		Label:     label,
		CreatedAt: now,
		CreatedBy: createdBy,
		ClusterID: clusterID,
	}
	if ttl != nil {
		exp := now.Add(*ttl)
		tok.ExpiresAt = &exp
	}
	if err := s.db.Update(func(tx *bolt.Tx) error {
		enc, err := json.Marshal(tok)
		if err != nil {
			return err
		}
		if err := tx.Bucket(ingestTokensBucket).Put(ingestKey(tenantID, tok.ID), enc); err != nil {
			return err
		}
		return tx.Bucket(ingestTokenHashIdxBucket).Put([]byte(hash), ingestKey(tenantID, tok.ID))
	}); err != nil {
		return "", nil, err
	}
	return plaintext, tok, nil
}

func (s *BoltIngestTokenStore) Revoke(tenantID, tokenID string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(ingestTokensBucket)
		raw := b.Get(ingestKey(tenantID, tokenID))
		if raw == nil {
			return ErrTokenNotFound
		}
		var tok IngestToken
		if err := json.Unmarshal(raw, &tok); err != nil {
			return err
		}
		if tok.RevokedAt == nil {
			rev := s.nowFn().UTC()
			tok.RevokedAt = &rev
		}
		// Drop the hash index so a revoked token can never be looked up again.
		if err := tx.Bucket(ingestTokenHashIdxBucket).Delete([]byte(tok.Hash)); err != nil {
			return err
		}
		enc, err := json.Marshal(&tok)
		if err != nil {
			return err
		}
		return b.Put(ingestKey(tenantID, tokenID), enc)
	})
}

func (s *BoltIngestTokenStore) Rotate(tenantID, tokenID, createdBy string) (string, *IngestToken, error) {
	old, ok, err := s.get(tenantID, tokenID)
	if err != nil {
		return "", nil, err
	}
	if !ok {
		return "", nil, ErrTokenNotFound
	}
	// Preserve the ORIGINAL ttl *window* (not the remaining time): the
	// rotated token gets a fresh full-length expiration measured from the
	// store's clock, mirroring the pre-W1 RotateToken behavior. Derive the
	// window from the old token's own issue→expiry span so it's independent
	// of wall-clock drift (and testable via nowFn).
	var ttl *time.Duration
	if old.ExpiresAt != nil {
		d := old.ExpiresAt.Sub(old.CreatedAt)
		ttl = &d
	}
	plaintext, newTok, err := s.Issue(tenantID, old.ClusterID, old.Label, createdBy, ttl)
	if err != nil {
		return "", nil, err
	}
	if err := s.Revoke(tenantID, tokenID); err != nil {
		return "", nil, err
	}
	return plaintext, newTok, nil
}

func (s *BoltIngestTokenStore) Lookup(plaintext string) (*IngestToken, error) {
	if !strings.HasPrefix(plaintext, TokenPrefix) {
		return nil, ErrTokenMalformed
	}
	hash := hashToken(plaintext)
	var tok *IngestToken
	err := s.db.View(func(tx *bolt.Tx) error {
		key := tx.Bucket(ingestTokenHashIdxBucket).Get([]byte(hash))
		if key == nil {
			return ErrTokenNotFound
		}
		raw := tx.Bucket(ingestTokensBucket).Get(key)
		if raw == nil {
			return ErrTokenNotFound
		}
		var tt IngestToken
		if err := json.Unmarshal(raw, &tt); err != nil {
			return err
		}
		tok = &tt
		return nil
	})
	if err != nil {
		return nil, err
	}
	now := s.nowFn()
	if tok.RevokedAt != nil {
		return nil, ErrTokenRevoked
	}
	if tok.ExpiresAt != nil && now.After(*tok.ExpiresAt) {
		return nil, ErrTokenExpired
	}
	return tok, nil
}

func (s *BoltIngestTokenStore) MarkUsed(tenantID, tokenID string, when time.Time) error {
	s.markUsedMu.Lock()
	last, ok := s.markUsedAt[tokenID]
	if ok && when.Sub(last) < time.Minute {
		s.markUsedMu.Unlock()
		return nil
	}
	s.markUsedAt[tokenID] = when
	s.markUsedMu.Unlock()

	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(ingestTokensBucket)
		raw := b.Get(ingestKey(tenantID, tokenID))
		if raw == nil {
			return ErrTokenNotFound
		}
		var tok IngestToken
		if err := json.Unmarshal(raw, &tok); err != nil {
			return err
		}
		w := when
		tok.LastUsedAt = &w
		enc, err := json.Marshal(&tok)
		if err != nil {
			return err
		}
		return b.Put(ingestKey(tenantID, tokenID), enc)
	})
}

func (s *BoltIngestTokenStore) ListByTenant(tenantID string) ([]IngestToken, error) {
	var out []IngestToken
	err := s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(ingestTokensBucket).Cursor()
		prefix := []byte(tenantID + "/")
		for k, v := c.Seek(prefix); k != nil && strings.HasPrefix(string(k), string(prefix)); k, v = c.Next() {
			var tok IngestToken
			if err := json.Unmarshal(v, &tok); err != nil {
				return err
			}
			out = append(out, tok)
		}
		return nil
	})
	return out, err
}

func (s *BoltIngestTokenStore) get(tenantID, tokenID string) (*IngestToken, bool, error) {
	var tok *IngestToken
	err := s.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(ingestTokensBucket).Get(ingestKey(tenantID, tokenID))
		if raw == nil {
			return nil
		}
		var tt IngestToken
		if err := json.Unmarshal(raw, &tt); err != nil {
			return err
		}
		tok = &tt
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return tok, tok != nil, nil
}

// MigrateInlinedTokens is the one-time boot cutover: it reads any ingest tokens
// still inlined in tenant records (the legacy `ingestTokens` field), writes each
// into the dedicated buckets (stamping TenantID), and rewrites the tenant record
// WITHOUT the inlined tokens. Idempotent — a token already present in the hash
// index is skipped, and a tenant with no inlined tokens is left untouched.
// Returns the number of tokens migrated.
func (s *BoltIngestTokenStore) MigrateInlinedTokens() (int, error) {
	migrated := 0
	err := s.db.Update(func(tx *bolt.Tx) error {
		tb := tx.Bucket(tenantsBucket)
		if tb == nil {
			return nil // no tenants store yet
		}
		hashIdx := tx.Bucket(ingestTokenHashIdxBucket)
		toks := tx.Bucket(ingestTokensBucket)
		type legacyTenant struct {
			IngestTokens []IngestToken `json:"ingestTokens"`
		}
		// Collect first (don't mutate the bucket mid-cursor).
		var ids [][]byte
		_ = tb.ForEach(func(k, _ []byte) error { ids = append(ids, append([]byte(nil), k...)); return nil })
		for _, id := range ids {
			raw := tb.Get(id)
			var legacy legacyTenant
			if err := json.Unmarshal(raw, &legacy); err != nil || len(legacy.IngestTokens) == 0 {
				continue
			}
			tenantID := string(id)
			for _, tok := range legacy.IngestTokens {
				if tok.Hash != "" && hashIdx.Get([]byte(tok.Hash)) != nil {
					continue // already migrated
				}
				tok.TenantID = tenantID
				enc, err := json.Marshal(&tok)
				if err != nil {
					return err
				}
				if err := toks.Put(ingestKey(tenantID, tok.ID), enc); err != nil {
					return err
				}
				// Only index active tokens so revoked ones can't be looked up.
				if tok.RevokedAt == nil && tok.Hash != "" {
					if err := hashIdx.Put([]byte(tok.Hash), ingestKey(tenantID, tok.ID)); err != nil {
						return err
					}
				}
				migrated++
			}
			// Rewrite the tenant clean (the Tenant struct no longer has the
			// field, so marshalling drops it).
			var clean Tenant
			if err := json.Unmarshal(raw, &clean); err != nil {
				return err
			}
			enc, err := json.Marshal(&clean)
			if err != nil {
				return err
			}
			if err := tb.Put(id, enc); err != nil {
				return err
			}
		}
		return nil
	})
	return migrated, err
}
