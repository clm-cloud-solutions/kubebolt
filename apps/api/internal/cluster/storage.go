package cluster

import (
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

// Storage is a thin wrapper around BoltDB for cluster-related persistence.
// It shares the underlying DB with the auth Store (bucket separation).
type Storage struct {
	db            *bolt.DB
	configsBucket []byte
	displayBucket []byte
}

// NewStorage creates a new cluster storage using an existing BoltDB instance.
// Call ClusterBuckets() on the auth package to get the bucket names.
func NewStorage(db *bolt.DB, configsBucket, displayBucket []byte) *Storage {
	return &Storage{
		db:            db,
		configsBucket: configsBucket,
		displayBucket: displayBucket,
	}
}

// --- Uploaded kubeconfigs ---

// SaveKubeconfig stores a user-uploaded kubeconfig under its context name.
// If a kubeconfig with the same context already exists, it is overwritten.
func (s *Storage) SaveKubeconfig(cfg *StoredKubeconfig) error {
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
func (s *Storage) GetKubeconfig(contextName string) (*StoredKubeconfig, error) {
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
func (s *Storage) ListKubeconfigs() ([]*StoredKubeconfig, error) {
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
func (s *Storage) DeleteKubeconfig(contextName string) error {
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
func (s *Storage) SetDisplayName(contextName, displayName string) error {
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
func (s *Storage) GetDisplayName(contextName string) string {
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
func (s *Storage) DeleteDisplayName(contextName string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(s.displayBucket).Delete([]byte(contextName))
	})
}

// AllDisplayNames returns all display name overrides as a map.
func (s *Storage) AllDisplayNames() (map[string]string, error) {
	result := make(map[string]string)
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(s.displayBucket).ForEach(func(k, v []byte) error {
			result[string(k)] = string(v)
			return nil
		})
	})
	return result, err
}
