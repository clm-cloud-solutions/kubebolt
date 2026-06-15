package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

// StoredKubeconfig represents a user-uploaded kubeconfig context.
// It stores the raw kubeconfig bytes so the original source is preserved
// exactly as uploaded — useful for diagnostics and re-parsing.
type StoredKubeconfig struct {
	Context    string    `json:"context"`    // context name inside the kubeconfig
	Kubeconfig []byte    `json:"kubeconfig"` // raw YAML bytes
	UploadedAt time.Time `json:"uploadedAt"`
	UploadedBy string    `json:"uploadedBy"` // username of the admin who added it
}

// ClusterStore is the W1 seam for cluster persistence
// (internal/saas/kubebolt-e1-multitenant-scoping.md §8). OSS uses the BoltDB
// *Storage below (the default org's clusters, all owned by the default team);
// EE swaps a Postgres impl that scopes clusters by org and adds owner_team_id +
// cross-team access grants. Pure-seam for now: the interface mirrors the
// current surface; team ownership lands with the team-wiring step (W1 #7).
//
// Every method takes a ctx as its first param (A.2): the EE Postgres impl reads
// the request/boot org off it via auth.TenantIDFromContext and runs each query
// inside eedb.WithOrg so RLS scopes the row to that org. The Bolt impl ignores
// the ctx (single-org OSS). Manager-internal/boot calls pass the manager's
// default-org context so single-org cluster loading keeps working under RLS.
type ClusterStore interface {
	SaveKubeconfig(ctx context.Context, cfg *StoredKubeconfig) error
	GetKubeconfig(ctx context.Context, contextName string) (*StoredKubeconfig, error)
	ListKubeconfigs(ctx context.Context) ([]*StoredKubeconfig, error)
	DeleteKubeconfig(ctx context.Context, contextName string) error
	SetDisplayName(ctx context.Context, contextName, displayName string) error
	GetDisplayName(ctx context.Context, contextName string) string
	DeleteDisplayName(ctx context.Context, contextName string) error
	AllDisplayNames(ctx context.Context) (map[string]string, error)
	SetClusterUID(ctx context.Context, contextName, uid string) error
	GetClusterUID(ctx context.Context, contextName string) string
	AllClusterUIDs(ctx context.Context) (map[string]string, error)
}

// Compile-time guarantee the Bolt impl satisfies the seam.
var _ ClusterStore = (*Storage)(nil)

// Storage is a thin wrapper around BoltDB for cluster-related persistence.
// It shares the underlying DB with the auth Store (bucket separation).
type Storage struct {
	db            *bolt.DB
	configsBucket []byte
	displayBucket []byte
	uidBucket     []byte
}

// NewStorage creates a new cluster storage using an existing BoltDB instance.
// Call ClusterBuckets() on the auth package to get the bucket names.
func NewStorage(db *bolt.DB, configsBucket, displayBucket, uidBucket []byte) *Storage {
	return &Storage{
		db:            db,
		configsBucket: configsBucket,
		displayBucket: displayBucket,
		uidBucket:     uidBucket,
	}
}

// --- Uploaded kubeconfigs ---

// SaveKubeconfig stores a user-uploaded kubeconfig under its context name.
// If a kubeconfig with the same context already exists, it is overwritten.
func (s *Storage) SaveKubeconfig(_ context.Context, cfg *StoredKubeconfig) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		data, err := json.Marshal(cfg)
		if err != nil {
			return err
		}
		return tx.Bucket(s.configsBucket).Put([]byte(cfg.Context), data)
	})
}

// GetKubeconfig retrieves a stored kubeconfig by context name.
// Returns nil, nil if not found (not an error — used to check origin).
func (s *Storage) GetKubeconfig(_ context.Context, contextName string) (*StoredKubeconfig, error) {
	var cfg *StoredKubeconfig
	err := s.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket(s.configsBucket).Get([]byte(contextName))
		if data == nil {
			return nil
		}
		cfg = &StoredKubeconfig{}
		return json.Unmarshal(data, cfg)
	})
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

// ListKubeconfigs returns all user-uploaded kubeconfigs.
func (s *Storage) ListKubeconfigs(_ context.Context) ([]*StoredKubeconfig, error) {
	var configs []*StoredKubeconfig
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(s.configsBucket).ForEach(func(k, v []byte) error {
			var cfg StoredKubeconfig
			if err := json.Unmarshal(v, &cfg); err != nil {
				return err
			}
			configs = append(configs, &cfg)
			return nil
		})
	})
	return configs, err
}

// DeleteKubeconfig removes a stored kubeconfig. Returns an error if not found.
func (s *Storage) DeleteKubeconfig(_ context.Context, contextName string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(s.configsBucket)
		if bucket.Get([]byte(contextName)) == nil {
			return fmt.Errorf("kubeconfig for context %q not found", contextName)
		}
		return bucket.Delete([]byte(contextName))
	})
}

// --- Display name overrides ---

// SetDisplayName stores a human-friendly name for a context. The override
// applies to any context (from the kubeconfig file or user-uploaded).
func (s *Storage) SetDisplayName(_ context.Context, contextName, displayName string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		if displayName == "" {
			// Empty display name = remove the override
			return tx.Bucket(s.displayBucket).Delete([]byte(contextName))
		}
		return tx.Bucket(s.displayBucket).Put([]byte(contextName), []byte(displayName))
	})
}

// GetDisplayName returns the display name override for a context,
// or empty string if none is set.
func (s *Storage) GetDisplayName(_ context.Context, contextName string) string {
	var name string
	s.db.View(func(tx *bolt.Tx) error {
		if v := tx.Bucket(s.displayBucket).Get([]byte(contextName)); v != nil {
			name = string(v)
		}
		return nil
	})
	return name
}

// DeleteDisplayName removes the display name override for a context.
func (s *Storage) DeleteDisplayName(_ context.Context, contextName string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(s.displayBucket).Delete([]byte(contextName))
	})
}

// AllDisplayNames returns all display name overrides as a map.
func (s *Storage) AllDisplayNames(_ context.Context) (map[string]string, error) {
	result := make(map[string]string)
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(s.displayBucket).ForEach(func(k, v []byte) error {
			result[string(k)] = string(v)
			return nil
		})
	})
	return result, err
}

// --- Cluster UID cache ---
//
// Per-context kube-system namespace UID, persisted after the
// Connector resolves it. Without this cache the UID is only
// known for the *currently-connected* cluster (transient on
// the Connector); previously-visited clusters silently drop
// their UID until the operator switches back.
//
// Storing it means ListClusters() can populate ClusterID for
// every context the operator has touched in any past session,
// which is what the IssueToken admin UI needs to offer a
// complete cluster picker.

// SetClusterUID persists (contextName → kube-system UID). Idempotent:
// re-writing the same UID is a no-op. Empty uid clears the entry.
func (s *Storage) SetClusterUID(_ context.Context, contextName, uid string) error {
	if contextName == "" {
		return nil
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(s.uidBucket)
		if uid == "" {
			return bucket.Delete([]byte(contextName))
		}
		return bucket.Put([]byte(contextName), []byte(uid))
	})
}

// GetClusterUID returns the cached UID for a context, or "" when
// the context has never been visited (and thus has no UID).
func (s *Storage) GetClusterUID(_ context.Context, contextName string) string {
	var uid string
	_ = s.db.View(func(tx *bolt.Tx) error {
		if v := tx.Bucket(s.uidBucket).Get([]byte(contextName)); v != nil {
			uid = string(v)
		}
		return nil
	})
	return uid
}

// AllClusterUIDs returns all cached (contextName → uid) pairs.
// Used by ListClusters() to bulk-populate ClusterID without
// per-context lookups.
func (s *Storage) AllClusterUIDs(_ context.Context) (map[string]string, error) {
	result := make(map[string]string)
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(s.uidBucket).ForEach(func(k, v []byte) error {
			result[string(k)] = string(v)
			return nil
		})
	})
	return result, err
}
