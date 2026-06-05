// Package auth — api_tokens_store hosts the BoltDB-backed registry of
// long-lived REST API tokens (distinct from the ingest tokens in
// tenants_store.go). These authenticate non-interactive callers against
// the REST API (/api/v1/*) without the short-lived user-session JWT.
//
// Two kinds, by prefix:
//
//	kbs_   service token  — machine-to-machine (e.g. Autopilot, EE/Cloud).
//	                        Network-bound: rejected if the request arrived
//	                        via the public edge (see api_token_auth.go).
//	kbk_   API key        — customer self-service key (future); read-only,
//	                        tenant-scoped, works over the public API.
//
// SEPARATE buckets from the ingest tokens by design: the REST validator
// looks up ONLY here, so an ingest/gRPC token (kb_) can never authenticate
// a REST call and vice-versa. That isolation is itself a security property.
//
// Layout (two buckets on the shared kubebolt.db):
//
//	api_tokens         key: token_id (uuid)        value: APIToken JSON
//	api_token_index    key: hex(sha256(plaintext)) value: token_id
//
// Tokens are stored only as SHA-256 hashes (reusing hashToken); plaintext
// is returned exactly once at issue time.
package auth

import (
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"crypto/rand"

	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

var (
	apiTokensBucket     = []byte("api_tokens")
	apiTokenIndexBucket = []byte("api_token_index")
)

// APITokenType discriminates the two token kinds.
type APITokenType string

const (
	// TokenTypeService is a machine-to-machine token (kbs_). Network-bound.
	TokenTypeService APITokenType = "service"
	// TokenTypeAPIKey is a customer self-service key (kbk_). Future use.
	TokenTypeAPIKey APITokenType = "apikey"

	// ServiceTokenPrefix marks a service token.
	ServiceTokenPrefix = "kbs_"
	// APIKeyTokenPrefix marks a customer API key.
	APIKeyTokenPrefix = "kbk_"
)

// ScopeAll grants access to every authenticated route (subject to Role).
const ScopeAll = "*"

// APIToken is a long-lived REST API credential.
type APIToken struct {
	ID     string       `json:"id"`
	Hash   string       `json:"hash"`
	Prefix string       `json:"prefix"` // display, e.g. "kbs_abcd1234"
	Label  string       `json:"label"`
	Type   APITokenType `json:"type"`
	Role   Role         `json:"role"`
	// Scopes are URL-path prefixes the token may call (e.g.
	// "/api/v1/resources"). ScopeAll ("*") means any authenticated route.
	// Enforced by EnforceAPITokenScope on top of Role.
	Scopes []string `json:"scopes,omitempty"`
	// TenantID/ClusterID scope the token to an org/cluster. Empty in OSS
	// (single-tenant default). Carried into the request so multi-tenant
	// EE code can read it via the tenant-context seam.
	TenantID  string `json:"tenantId,omitempty"`
	ClusterID string `json:"clusterId,omitempty"`

	CreatedAt  time.Time  `json:"createdAt"`
	CreatedBy  string     `json:"createdBy"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
	RevokedAt  *time.Time `json:"revokedAt,omitempty"`
}

// Active reports whether the token is currently valid (not revoked, not expired).
func (t *APIToken) Active(now time.Time) bool {
	if t.RevokedAt != nil {
		return false
	}
	if t.ExpiresAt != nil && now.After(*t.ExpiresAt) {
		return false
	}
	return true
}

// APITokenStore owns the api_tokens buckets. Safe for concurrent use.
type APITokenStore struct {
	db    *bolt.DB
	nowFn func() time.Time

	markUsedMu sync.Mutex
	markUsedAt map[string]time.Time
}

// NewAPITokenStore opens the api_tokens buckets on the supplied BoltDB.
func NewAPITokenStore(db *bolt.DB) (*APITokenStore, error) {
	if db == nil {
		return nil, errors.New("api token store: nil db")
	}
	err := db.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{apiTokensBucket, apiTokenIndexBucket} {
			if _, err := tx.CreateBucketIfNotExists(name); err != nil {
				return fmt.Errorf("create bucket %s: %w", name, err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &APITokenStore{
		db:         db,
		nowFn:      func() time.Time { return time.Now().UTC() },
		markUsedAt: map[string]time.Time{},
	}, nil
}

// prefixForType returns the plaintext prefix for a token type.
func prefixForType(t APITokenType) (string, error) {
	switch t {
	case TokenTypeService:
		return ServiceTokenPrefix, nil
	case TokenTypeAPIKey:
		return APIKeyTokenPrefix, nil
	default:
		return "", fmt.Errorf("unknown api token type %q", t)
	}
}

// Issue generates a fresh token, persists its hash, and returns the plaintext
// once (unrecoverable afterwards). ttl=nil means no expiration.
func (s *APITokenStore) Issue(typ APITokenType, role Role, scopes []string, label, createdBy string, ttl *time.Duration) (string, *APIToken, error) {
	prefix, err := prefixForType(typ)
	if err != nil {
		return "", nil, err
	}
	plaintext, err := generateAPITokenPlaintext(prefix)
	if err != nil {
		return "", nil, err
	}
	now := s.nowFn()
	tok := APIToken{
		ID:        uuid.New().String(),
		Hash:      hashToken(plaintext),
		Prefix:    apiTokenDisplayPrefix(plaintext, prefix),
		Label:     label,
		Type:      typ,
		Role:      role,
		Scopes:    scopes,
		CreatedAt: now,
		CreatedBy: createdBy,
	}
	if ttl != nil {
		exp := now.Add(*ttl)
		tok.ExpiresAt = &exp
	}
	err = s.db.Update(func(tx *bolt.Tx) error {
		data, err := json.Marshal(&tok)
		if err != nil {
			return err
		}
		if err := tx.Bucket(apiTokensBucket).Put([]byte(tok.ID), data); err != nil {
			return err
		}
		return tx.Bucket(apiTokenIndexBucket).Put([]byte(tok.Hash), []byte(tok.ID))
	})
	if err != nil {
		return "", nil, err
	}
	return plaintext, &tok, nil
}

// Lookup validates a plaintext token: malformed prefix, unknown hash,
// revoked and expired all return distinct sentinel errors (shared with
// tenants_store.go).
func (s *APITokenStore) Lookup(plaintext string) (*APIToken, error) {
	if !IsAPIToken(plaintext) {
		return nil, ErrTokenMalformed
	}
	hash := hashToken(plaintext)
	var tok APIToken
	err := s.db.View(func(tx *bolt.Tx) error {
		id := tx.Bucket(apiTokenIndexBucket).Get([]byte(hash))
		if id == nil {
			return ErrTokenNotFound
		}
		data := tx.Bucket(apiTokensBucket).Get(id)
		if data == nil {
			return ErrTokenNotFound
		}
		return json.Unmarshal(data, &tok)
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
	return &tok, nil
}

// List returns every token (metadata only — the Hash is internal). Order is
// BoltDB key order (uuid).
func (s *APITokenStore) List() ([]APIToken, error) {
	var out []APIToken
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(apiTokensBucket).ForEach(func(_, v []byte) error {
			var t APIToken
			if err := json.Unmarshal(v, &t); err != nil {
				return nil // skip corrupt entries
			}
			out = append(out, t)
			return nil
		})
	})
	return out, err
}

// Revoke marks a token revoked and removes it from the lookup index so future
// Lookup calls fail fast. The record (RevokedAt) stays for audit.
func (s *APITokenStore) Revoke(id string) error {
	now := s.nowFn()
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(apiTokensBucket)
		data := bucket.Get([]byte(id))
		if data == nil {
			return ErrTokenNotFound
		}
		var t APIToken
		if err := json.Unmarshal(data, &t); err != nil {
			return err
		}
		if t.RevokedAt == nil {
			rev := now
			t.RevokedAt = &rev
		}
		// Keep the hash→id index entry so Lookup resolves the record and
		// returns the precise ErrTokenRevoked (Lookup enforces RevokedAt).
		newData, err := json.Marshal(&t)
		if err != nil {
			return err
		}
		return bucket.Put([]byte(id), newData)
	})
}

// MarkUsed updates LastUsedAt, debounced to one persistence per (token, minute).
func (s *APITokenStore) MarkUsed(id string, when time.Time) error {
	s.markUsedMu.Lock()
	last, ok := s.markUsedAt[id]
	if ok && when.Sub(last) < time.Minute {
		s.markUsedMu.Unlock()
		return nil
	}
	s.markUsedAt[id] = when
	s.markUsedMu.Unlock()

	return s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(apiTokensBucket)
		data := bucket.Get([]byte(id))
		if data == nil {
			return ErrTokenNotFound
		}
		var t APIToken
		if err := json.Unmarshal(data, &t); err != nil {
			return err
		}
		w := when
		t.LastUsedAt = &w
		newData, err := json.Marshal(&t)
		if err != nil {
			return err
		}
		return bucket.Put([]byte(id), newData)
	})
}

// IsAPIToken reports whether a bearer string is a REST API token (by prefix).
// Used to route validation: API token vs user-session JWT.
func IsAPIToken(plaintext string) bool {
	return strings.HasPrefix(plaintext, ServiceTokenPrefix) ||
		strings.HasPrefix(plaintext, APIKeyTokenPrefix)
}

func generateAPITokenPlaintext(prefix string) (string, error) {
	buf := make([]byte, tokenRandomBytes) // 20 bytes → 160 bits (shared const)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("token entropy: %w", err)
	}
	enc := strings.ToLower(strings.TrimRight(base32.StdEncoding.EncodeToString(buf), "="))
	return prefix + enc, nil
}

// apiTokenDisplayPrefix returns prefix + first 8 chars of the random part,
// e.g. "kbs_abcd1234", for UI display without retaining the secret.
func apiTokenDisplayPrefix(plaintext, prefix string) string {
	if len(plaintext) <= len(prefix)+8 {
		return plaintext
	}
	return plaintext[:len(prefix)+8]
}
