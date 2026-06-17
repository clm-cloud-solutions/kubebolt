// Package auth — tenants_store hosts the BoltDB-backed registry of tenants
// and their long-lived ingest bearer tokens used by the kubebolt-agent
// gRPC channel (Sprint A).
//
// Layout (three buckets, sharing the same kubebolt.db file as the user store):
//
//	tenants               key: tenant_id (uuid)        value: Tenant JSON
//	tenant_token_index    key: hex(sha256(plaintext))  value: tenant_id
//	tenant_name_index     key: lower(tenant.name)      value: tenant_id
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
	"time"

	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

var (
	tenantsBucket         = []byte("tenants")
	tenantNameIndexBucket = []byte("tenant_name_index")
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
	ID        string        `json:"id"`
	Name      string        `json:"name"`
	Plan      string        `json:"plan"`
	CreatedAt time.Time     `json:"createdAt"`
	UpdatedAt time.Time     `json:"updatedAt"`
	Disabled  bool          `json:"disabled"`
	Limits    *TenantLimits `json:"limits,omitempty"`
}

// (IngestToken + its store moved to ingest_tokens_store.go — tokens are no
// longer inlined in the Tenant record. A one-time boot migration
// (BoltIngestTokenStore.MigrateInlinedTokens) moves legacy inlined tokens out.)

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

	// nowFn is overridable in tests to drive expiration logic without sleeping.
	nowFn func() time.Time
}

// NewTenantsStore opens the tenant buckets on the supplied BoltDB and
// auto-seeds the "default" tenant if no tenants exist.
func NewTenantsStore(db *bolt.DB) (*TenantsStore, error) {
	if db == nil {
		return nil, errors.New("tenants store: nil db")
	}
	err := db.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{tenantsBucket, tenantNameIndexBucket} {
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
		db:    db,
		nowFn: func() time.Time { return time.Now().UTC() },
	}
	// Single-tenant (OSS / self-hosted) auto-seeds the canonical "default"
	// tenant. The multi-tenant edition does NOT: there is no magic default —
	// the operator's org is a normal org created at first boot via BootstrapOrg
	// (cmd/server operator bootstrap). See Track D §2.6.
	if !MultiTenantEnabled {
		if _, err := s.ensureDefaultTenant(); err != nil {
			return nil, fmt.Errorf("seed default tenant: %w", err)
		}
	}
	return s, nil
}

func (s *TenantsStore) ensureDefaultTenant() (*Tenant, error) {
	if t, err := s.getTenantByName(DefaultTenantName); err == nil {
		return t, nil
	}
	return s.CreateTenant(DefaultTenantName, "self-hosted")
}

// GetDefaultTenant returns the canonical/primary org — the home for data that
// isn't explicitly tenant-stamped (the directly-connected kubeconfig cluster,
// TokenReview agents, the unauthenticated-ingest fallback). In single-tenant
// (OSS) that's the auto-seeded "default" tenant. In multi-tenant (cloud) there
// is no "default" tenant (Track D §2.6), so it resolves to the OPERATOR org —
// the earliest-created tenant (created first at first-boot bootstrap). This
// keeps every legacy "default tenant" caller working uniformly across editions.
func (s *TenantsStore) GetDefaultTenant() (*Tenant, error) {
	if t, err := s.getTenantByName(DefaultTenantName); err == nil {
		return t, nil
	}
	all, err := s.ListTenants()
	if err != nil {
		return nil, err
	}
	return earliestTenant(all)
}

// earliestTenant returns the oldest tenant — the operator/primary org in the
// multi-tenant edition (created first at bootstrap). ErrTenantNotFound if none
// exist yet (a brand-new DB before the operator org is bootstrapped).
func earliestTenant(ts []Tenant) (*Tenant, error) {
	if len(ts) == 0 {
		return nil, ErrTenantNotFound
	}
	idx := 0
	for i := range ts {
		if ts[i].CreatedAt.Before(ts[idx].CreatedAt) {
			idx = i
		}
	}
	t := ts[idx]
	return &t, nil
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
		// NB: the tenant's ingest tokens now live in IngestTokenStore — the
		// caller is responsible for cleaning them up (cascade) when removing a
		// tenant. OSS never deletes the default tenant, so this is moot there.
		_ = tx.Bucket(tenantNameIndexBucket).Delete([]byte(strings.ToLower(t.Name)))
		return bucket.Delete([]byte(id))
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

// hashToken returns the SHA-256 of a token's plaintext, hex-encoded, for
// storage + constant-time lookup. SHA-256 (not bcrypt/argon2) is deliberate
// and correct here: the inputs are HIGH-ENTROPY random tokens minted by
// generateTokenPlaintext (160 bits of crypto/rand), never human-chosen
// passwords. A slow password hash buys nothing against a 160-bit random
// secret and would only add latency to every token validation. (CodeQL's
// go/weak-sensitive-data-hashing flags this as a password hash — it is not;
// alert dismissed as a false positive.)
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
