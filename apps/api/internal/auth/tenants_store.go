// Package auth — tenants_store hosts the BoltDB-backed registry of tenants
// and their long-lived ingest bearer tokens used by the kubebolt-agent
// gRPC channel (Sprint A).
//
// Layout (three buckets, sharing the same kubebolt.db file as the user store):
//
//   tenants               key: tenant_id (uuid)        value: Tenant JSON
//   tenant_token_index    key: hex(sha256(plaintext))  value: tenant_id
//   tenant_name_index     key: lower(tenant.name)      value: tenant_id
//
// The token index makes LookupByToken O(1) without scanning every tenant.
// The name index enforces uniqueness on Tenant.Name.
//
// Tokens are stored only as SHA-256 hashes; plaintext is returned exactly
// once at issue/rotate time. The first 8 chars after the "kb_" prefix are
// kept verbatim so the UI can show "kb_abcd1234…" without retaining the
// secret material.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

var (
	tenantsBucket          = []byte("tenants")
	tenantTokenIndexBucket = []byte("tenant_token_index")
	tenantNameIndexBucket  = []byte("tenant_name_index")
)

const (
	// DefaultTenantName is the auto-seeded tenant for self-hosted
	// single-cluster deployments where the operator never explicitly
	// creates a tenant.
	DefaultTenantName = "default"
	// TokenPrefix marks every issued ingest token. Used both as a
	// fast-path malformed check and to identify the secret kind in logs.
	TokenPrefix = "kb_"
	// tokenRandomBytes — 20 bytes → 32 base32 chars → 160 bits of entropy.
	// Plenty for a server-issued bearer token; SHA-256 hash is safe at
	// this entropy level, no key-stretching needed.
	tokenRandomBytes = 20
)

var (
	ErrTenantNotFound = errors.New("tenant not found")
	ErrTenantExists   = errors.New("tenant name already exists")
	ErrTokenNotFound  = errors.New("token not found")
	ErrTenantDisabled = errors.New("tenant disabled")
	ErrTokenRevoked   = errors.New("token revoked")
	ErrTokenExpired   = errors.New("token expired")
	ErrTokenMalformed = errors.New("token malformed")
)

// Tenant is the canonical owner of ingest tokens and (later) of cluster
// agents and quota. Tokens are inlined because the cap per tenant is small
// (~20) and reads always need them together with tenant metadata.
//
// Limits is the per-tenant override of system-default Prom remote_write
// limits (rate, burst, cardinality). nil means "inherit from the fleet-
// wide defaults set via KUBEBOLT_PROM_WRITE_DEFAULT_* env vars". See
// tenant_limits.go for the resolution model. The pointer + omitempty
// preserves wire compatibility with pre-Phase-3 tenant records (they
// unmarshal cleanly with Limits == nil).
type Tenant struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	Plan         string        `json:"plan"`
	CreatedAt    time.Time     `json:"createdAt"`
	UpdatedAt    time.Time     `json:"updatedAt"`
	Disabled     bool          `json:"disabled"`
	IngestTokens []IngestToken `json:"ingestTokens"`
	Limits       *TenantLimits `json:"limits,omitempty"`
}

// IngestToken is a long-lived bearer credential issued by the backend for
// agents that cannot use Kubernetes TokenReview (e.g. SaaS / cross-cluster).
type IngestToken struct {
	ID         string     `json:"id"`
	Hash       string     `json:"hash"`
	Prefix     string     `json:"prefix"`
	Label      string     `json:"label"`
	CreatedAt  time.Time  `json:"createdAt"`
	CreatedBy  string     `json:"createdBy"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
	RevokedAt  *time.Time `json:"revokedAt,omitempty"`

	// ClusterID is the kube-system namespace UID of the cluster this
	// token is scoped to. Empty value means "any cluster" — applies
	// to legacy tokens issued before this field existed AND to
	// tokens explicitly issued without a cluster scope. Used by the
	// Prometheus integration card to discriminate which token's
	// activity belongs to the current cluster's view; without it,
	// multi-cluster self-hosted installs collapse all activity under
	// the single tenant `default` and the card can't tell sources
	// apart.
	//
	// Convention matches the `cluster_id` metric label the agent
	// stamps on every sample, so future per-cluster receiver
	// counters can join cleanly with this field.
	ClusterID string `json:"clusterId,omitempty"`
}

// Active reports whether the token is currently valid: not revoked and
// not past its optional expiration.
func (t *IngestToken) Active(now time.Time) bool {
	if t.RevokedAt != nil {
		return false
	}
	if t.ExpiresAt != nil && now.After(*t.ExpiresAt) {
		return false
	}
	return true
}

// TenantStore is the W1 seam for the Organization domain — a Tenant IS the
// Organization (internal/saas/kubebolt-e1-multitenant-scoping.md §8). OSS uses
// the BoltDB *TenantsStore below (degenerate: a single auto-seeded "default"
// org); EE swaps a Postgres impl that activates multi-org. The interface covers
// org management only; the inlined ingest-token methods (IssueToken, …) are the
// TokenStore concern (W1 #5).
type TenantStore interface {
	GetDefaultTenant() (*Tenant, error)
	CreateTenant(name, plan string) (*Tenant, error)
	GetTenant(id string) (*Tenant, error)
	ListTenants() ([]Tenant, error)
	UpdateTenant(id string, mut func(*Tenant) error) (*Tenant, error)
	SetLimits(id string, patch *TenantLimits) (*Tenant, LimitsValidation, error)
	ClearLimits(id string) (*Tenant, error)
	DeleteTenant(id string) error
}

// Compile-time guarantee the Bolt impl satisfies the seam.
var _ TenantStore = (*TenantsStore)(nil)

// TenantsStore wraps the BoltDB handle from auth.Store and owns the
// tenant + ingest-token lifecycle. Safe for concurrent use.
type TenantsStore struct {
	db *bolt.DB

	// nowFn is overridable in tests to drive expiration / debounce logic
	// without sleeping.
	nowFn func() time.Time

	// markUsedAt debounces LastUsedAt persistence so we do not write to
	// BoltDB on every single agent RPC. Process-local and intentionally
	// non-persistent: a server restart simply resumes the dance.
	markUsedMu sync.Mutex
	markUsedAt map[string]time.Time
}

// NewTenantsStore opens the tenant buckets on the supplied BoltDB and
// auto-seeds the "default" tenant if no tenants exist.
func NewTenantsStore(db *bolt.DB) (*TenantsStore, error) {
	if db == nil {
		return nil, errors.New("tenants store: nil db")
	}
	err := db.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{tenantsBucket, tenantTokenIndexBucket, tenantNameIndexBucket} {
			if _, err := tx.CreateBucketIfNotExists(name); err != nil {
				return fmt.Errorf("create bucket %s: %w", name, err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s := &TenantsStore{
		db:         db,
		nowFn:      func() time.Time { return time.Now().UTC() },
		markUsedAt: map[string]time.Time{},
	}
	if _, err := s.ensureDefaultTenant(); err != nil {
		return nil, fmt.Errorf("seed default tenant: %w", err)
	}
	return s, nil
}

func (s *TenantsStore) ensureDefaultTenant() (*Tenant, error) {
	if t, err := s.getTenantByName(DefaultTenantName); err == nil {
		return t, nil
	}
	return s.CreateTenant(DefaultTenantName, "self-hosted")
}

// GetDefaultTenant returns the auto-seeded "default" tenant. Callers
// in self-hosted single-cluster paths use this as the canonical tenant
// for TokenReview-authenticated agents (where the credential identifies
// the cluster, not a tenant).
func (s *TenantsStore) GetDefaultTenant() (*Tenant, error) {
	return s.getTenantByName(DefaultTenantName)
}

func (s *TenantsStore) getTenantByName(name string) (*Tenant, error) {
	var t Tenant
	err := s.db.View(func(tx *bolt.Tx) error {
		id := tx.Bucket(tenantNameIndexBucket).Get([]byte(strings.ToLower(name)))
		if id == nil {
			return ErrTenantNotFound
		}
		data := tx.Bucket(tenantsBucket).Get(id)
		if data == nil {
			return ErrTenantNotFound
		}
		return json.Unmarshal(data, &t)
	})
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// CreateTenant inserts a new tenant. Returns ErrTenantExists if the name
// (case-insensitive) collides with an existing tenant.
func (s *TenantsStore) CreateTenant(name, plan string) (*Tenant, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("tenant name required")
	}
	now := s.nowFn()
	t := &Tenant{
		ID:        uuid.New().String(),
		Name:      name,
		Plan:      plan,
		CreatedAt: now,
		UpdatedAt: now,
	}
	err := s.db.Update(func(tx *bolt.Tx) error {
		idx := tx.Bucket(tenantNameIndexBucket)
		nameKey := []byte(strings.ToLower(name))
		if idx.Get(nameKey) != nil {
			return ErrTenantExists
		}
		data, err := json.Marshal(t)
		if err != nil {
			return err
		}
		if err := tx.Bucket(tenantsBucket).Put([]byte(t.ID), data); err != nil {
			return err
		}
		return idx.Put(nameKey, []byte(t.ID))
	})
	if err != nil {
		return nil, err
	}
	return t, nil
}

// GetTenant returns the tenant by ID.
func (s *TenantsStore) GetTenant(id string) (*Tenant, error) {
	var t Tenant
	err := s.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket(tenantsBucket).Get([]byte(id))
		if data == nil {
			return ErrTenantNotFound
		}
		return json.Unmarshal(data, &t)
	})
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// ListTenants returns every tenant. Order is BoltDB key order (uuid sort,
// effectively insertion-time random).
func (s *TenantsStore) ListTenants() ([]Tenant, error) {
	var out []Tenant
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(tenantsBucket).ForEach(func(_, v []byte) error {
			var t Tenant
			if err := json.Unmarshal(v, &t); err != nil {
				return nil // skip corrupt entries — never block list
			}
			out = append(out, t)
			return nil
		})
	})
	return out, err
}

// UpdateTenant applies mut inside a single transaction. mut may freely
// modify the tenant; UpdatedAt is bumped automatically. Name changes
// rewrite the name index atomically.
func (s *TenantsStore) UpdateTenant(id string, mut func(*Tenant) error) (*Tenant, error) {
	var updated Tenant
	err := s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(tenantsBucket)
		data := bucket.Get([]byte(id))
		if data == nil {
			return ErrTenantNotFound
		}
		var t Tenant
		if err := json.Unmarshal(data, &t); err != nil {
			return err
		}
		oldName := t.Name
		if err := mut(&t); err != nil {
			return err
		}
		if !strings.EqualFold(oldName, t.Name) {
			idx := tx.Bucket(tenantNameIndexBucket)
			newKey := []byte(strings.ToLower(t.Name))
			if existing := idx.Get(newKey); existing != nil && string(existing) != id {
				return ErrTenantExists
			}
			_ = idx.Delete([]byte(strings.ToLower(oldName)))
			if err := idx.Put(newKey, []byte(id)); err != nil {
				return err
			}
		}
		t.UpdatedAt = s.nowFn()
		newData, err := json.Marshal(&t)
		if err != nil {
			return err
		}
		updated = t
		return bucket.Put([]byte(id), newData)
	})
	if err != nil {
		return nil, err
	}
	return &updated, nil
}

// SetLimits applies a partial update of the tenant's per-tenant limits
// overrides. Fields set on the patch overwrite the existing values; nil
// fields preserve whatever was there (or fall back to system defaults
// at enforcement time). To clear ALL overrides and revert the tenant
// to system defaults, use ClearLimits.
//
// The patch is validated via ValidateLimits before persistence; hard-
// reject errors propagate (caller maps to HTTP 400). Warnings are
// returned so the admin handler can surface them in the response.
func (s *TenantsStore) SetLimits(id string, patch *TenantLimits) (*Tenant, LimitsValidation, error) {
	v, err := ValidateLimits(patch)
	if err != nil {
		return nil, v, fmt.Errorf("%w: %v", ErrLimitsValidation, err)
	}
	updated, err := s.UpdateTenant(id, func(t *Tenant) error {
		t.Limits = MergeLimits(t.Limits, patch)
		return nil
	})
	if err != nil {
		return nil, v, err
	}
	return updated, v, nil
}

// ClearLimits removes ALL per-tenant overrides so the tenant inherits
// the fleet-wide system defaults again. Used by the admin "Reset to
// default" affordance. Idempotent: clearing on a tenant with no
// overrides is a no-op.
func (s *TenantsStore) ClearLimits(id string) (*Tenant, error) {
	return s.UpdateTenant(id, func(t *Tenant) error {
		t.Limits = nil
		return nil
	})
}

// DeleteTenant removes the tenant and clears every index entry it owned.
func (s *TenantsStore) DeleteTenant(id string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(tenantsBucket)
		data := bucket.Get([]byte(id))
		if data == nil {
			return ErrTenantNotFound
		}
		var t Tenant
		if err := json.Unmarshal(data, &t); err != nil {
			return err
		}
		tokIdx := tx.Bucket(tenantTokenIndexBucket)
		for _, tok := range t.IngestTokens {
			_ = tokIdx.Delete([]byte(tok.Hash))
		}
		_ = tx.Bucket(tenantNameIndexBucket).Delete([]byte(strings.ToLower(t.Name)))
		return bucket.Delete([]byte(id))
	})
}

// IssueToken generates a fresh ingest token, persists its hash, and returns
// the plaintext to the caller. The plaintext is unrecoverable afterwards.
// ttl=nil means the token never expires.
//
// clusterID is the kube-system namespace UID of the cluster this token
// is scoped to. Pass "" to issue an unscoped token (matches any cluster
// — used by integrations that aren't cluster-attributable, and by the
// admin UI's legacy issue flow that doesn't yet collect a cluster).
func (s *TenantsStore) IssueToken(tenantID, clusterID, label, createdBy string, ttl *time.Duration) (string, *IngestToken, error) {
	plaintext, err := generateTokenPlaintext()
	if err != nil {
		return "", nil, err
	}
	hash := hashToken(plaintext)
	now := s.nowFn()
	tok := IngestToken{
		ID:        uuid.New().String(),
		Hash:      hash,
		Prefix:    tokenDisplayPrefix(plaintext),
		Label:     label,
		ClusterID: clusterID,
		CreatedAt: now,
		CreatedBy: createdBy,
	}
	if ttl != nil {
		exp := now.Add(*ttl)
		tok.ExpiresAt = &exp
	}
	err = s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(tenantsBucket)
		data := bucket.Get([]byte(tenantID))
		if data == nil {
			return ErrTenantNotFound
		}
		var t Tenant
		if err := json.Unmarshal(data, &t); err != nil {
			return err
		}
		t.IngestTokens = append(t.IngestTokens, tok)
		t.UpdatedAt = now
		newData, err := json.Marshal(&t)
		if err != nil {
			return err
		}
		if err := bucket.Put([]byte(tenantID), newData); err != nil {
			return err
		}
		return tx.Bucket(tenantTokenIndexBucket).Put([]byte(hash), []byte(tenantID))
	})
	if err != nil {
		return "", nil, err
	}
	return plaintext, &tok, nil
}

// RevokeToken marks a token revoked and removes it from the lookup index
// so future LookupByToken calls fail fast. The audit record (RevokedAt)
// stays on the tenant for traceability.
func (s *TenantsStore) RevokeToken(tenantID, tokenID string) error {
	now := s.nowFn()
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(tenantsBucket)
		data := bucket.Get([]byte(tenantID))
		if data == nil {
			return ErrTenantNotFound
		}
		var t Tenant
		if err := json.Unmarshal(data, &t); err != nil {
			return err
		}
		found := false
		for i := range t.IngestTokens {
			if t.IngestTokens[i].ID != tokenID {
				continue
			}
			if t.IngestTokens[i].RevokedAt == nil {
				rev := now
				t.IngestTokens[i].RevokedAt = &rev
			}
			_ = tx.Bucket(tenantTokenIndexBucket).Delete([]byte(t.IngestTokens[i].Hash))
			found = true
			break
		}
		if !found {
			return ErrTokenNotFound
		}
		t.UpdatedAt = now
		newData, err := json.Marshal(&t)
		if err != nil {
			return err
		}
		return bucket.Put([]byte(tenantID), newData)
	})
}

// RotateToken issues a replacement token preserving the old token's label
// and TTL window, then revokes the old one. The new plaintext is returned
// once. Caller hands the new value to the operator before the rotation
// grace period begins.
func (s *TenantsStore) RotateToken(tenantID, tokenID, createdBy string) (string, *IngestToken, error) {
	t, err := s.GetTenant(tenantID)
	if err != nil {
		return "", nil, err
	}
	var old *IngestToken
	for i := range t.IngestTokens {
		if t.IngestTokens[i].ID == tokenID {
			old = &t.IngestTokens[i]
			break
		}
	}
	if old == nil {
		return "", nil, ErrTokenNotFound
	}
	var ttl *time.Duration
	if old.ExpiresAt != nil {
		d := old.ExpiresAt.Sub(old.CreatedAt)
		ttl = &d
	}
	// Preserve the cluster scope of the rotated token — a rotation
	// is the same logical credential with a fresh secret, so the
	// cluster binding must carry over.
	plaintext, newTok, err := s.IssueToken(tenantID, old.ClusterID, old.Label, createdBy, ttl)
	if err != nil {
		return "", nil, err
	}
	if err := s.RevokeToken(tenantID, tokenID); err != nil {
		return "", nil, err
	}
	return plaintext, newTok, nil
}

// LookupByToken validates a plaintext bearer token: malformed prefix,
// unknown hash, revoked, expired, and disabled-tenant cases all return
// distinct sentinel errors so the interceptor can map them to gRPC codes.
func (s *TenantsStore) LookupByToken(plaintext string) (*Tenant, *IngestToken, error) {
	if !strings.HasPrefix(plaintext, TokenPrefix) {
		return nil, nil, ErrTokenMalformed
	}
	hash := hashToken(plaintext)
	var tenantID string
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(tenantTokenIndexBucket).Get([]byte(hash))
		if v == nil {
			return ErrTokenNotFound
		}
		tenantID = string(v)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	t, err := s.GetTenant(tenantID)
	if err != nil {
		return nil, nil, err
	}
	if t.Disabled {
		return nil, nil, ErrTenantDisabled
	}
	var tok *IngestToken
	for i := range t.IngestTokens {
		if t.IngestTokens[i].Hash == hash {
			tok = &t.IngestTokens[i]
			break
		}
	}
	if tok == nil {
		return nil, nil, ErrTokenNotFound
	}
	now := s.nowFn()
	if tok.RevokedAt != nil {
		return nil, nil, ErrTokenRevoked
	}
	if tok.ExpiresAt != nil && now.After(*tok.ExpiresAt) {
		return nil, nil, ErrTokenExpired
	}
	return t, tok, nil
}

// MarkUsed updates LastUsedAt on the token, debounced to one persistence
// per (token, minute). The first call after a server restart always
// persists; subsequent calls within the window are coalesced.
func (s *TenantsStore) MarkUsed(tenantID, tokenID string, when time.Time) error {
	s.markUsedMu.Lock()
	last, ok := s.markUsedAt[tokenID]
	if ok && when.Sub(last) < time.Minute {
		s.markUsedMu.Unlock()
		return nil
	}
	s.markUsedAt[tokenID] = when
	s.markUsedMu.Unlock()

	return s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(tenantsBucket)
		data := bucket.Get([]byte(tenantID))
		if data == nil {
			return ErrTenantNotFound
		}
		var t Tenant
		if err := json.Unmarshal(data, &t); err != nil {
			return err
		}
		for i := range t.IngestTokens {
			if t.IngestTokens[i].ID == tokenID {
				w := when
				t.IngestTokens[i].LastUsedAt = &w
				break
			}
		}
		newData, err := json.Marshal(&t)
		if err != nil {
			return err
		}
		return bucket.Put([]byte(tenantID), newData)
	})
}

func generateTokenPlaintext() (string, error) {
	buf := make([]byte, tokenRandomBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("token entropy: %w", err)
	}
	enc := strings.ToLower(strings.TrimRight(base32.StdEncoding.EncodeToString(buf), "="))
	return TokenPrefix + enc, nil
}

func hashToken(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// tokenDisplayPrefix returns the first 8 chars after the "kb_" marker,
// for UI display (e.g. "kb_abcd1234…"). Falls back to the whole string
// if it is implausibly short.
func tokenDisplayPrefix(plaintext string) string {
	if len(plaintext) <= len(TokenPrefix)+8 {
		return plaintext
	}
	return plaintext[:len(TokenPrefix)+8]
}
