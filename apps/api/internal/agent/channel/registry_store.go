package channel

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

// AgentRecord is the persistent shape of one connected (or
// previously-connected) agent. The in-memory Agent struct holds the
// gRPC stream + multiplexor and dies on disconnect; this record
// survives restarts so the cluster.Manager can restore its
// agentProxyContexts on boot before any agent reconnects.
//
// Forensics: LastSeen + DisconnectedAt let operators understand
// agent lifecycle even after the live registry forgets. The store
// keeps records with no time-based pruning of "currently disconnected"
// agents up to a configurable horizon (default 24h) — long enough
// to bridge a deploy / node-replacement, short enough that abandoned
// installs don't leak forever.
type AgentRecord struct {
	ClusterID string `json:"clusterId"`
	AgentID   string `json:"agentId"`

	// Identity (auth) bits captured at first welcome — useful for the
	// admin UI and for forensics when an agent goes offline.
	TenantID string `json:"tenantId,omitempty"`
	AuthMode string `json:"authMode,omitempty"`

	// Pod + node identity from the agent's environment. NodeName is
	// the canonical way to map a record back to a Pod.
	NodeName     string `json:"nodeName,omitempty"`
	AgentVersion string `json:"agentVersion,omitempty"`

	// Capabilities advertised in the Hello — `["metrics","kube-proxy"]`
	// for v0.2.0+ agents in reader/operator mode. Persisted so the
	// manager can decide on boot whether a cluster_id should be
	// restored as agent-proxy (kube-proxy in caps) or just observed
	// as a metrics-only agent.
	Capabilities []string `json:"capabilities,omitempty"`

	// DisplayName comes from Hello.Labels["kubebolt.io/cluster-name"]
	// (which the agent sources from KUBEBOLT_AGENT_CLUSTER_NAME env).
	// Empty when the operator didn't set a friendly name; the manager
	// then falls back to the cluster_id.
	DisplayName string `json:"displayName,omitempty"`

	// First/Last seen timestamps. FirstSeen is fixed at the moment
	// the record is first persisted; LastSeen bumps on every
	// re-Register and on disconnect (the disconnect time is what
	// drives prune decisions).
	FirstSeen time.Time `json:"firstSeen"`
	LastSeen  time.Time `json:"lastSeen"`

	// DisconnectedAt is set when the agent unregisters cleanly. Stays
	// zero while the agent is connected. The cleanup goroutine prunes
	// records with non-zero DisconnectedAt older than the horizon.
	DisconnectedAt time.Time `json:"disconnectedAt,omitempty"`
}

// Connected reports whether the live agent is currently connected
// (i.e. Unregister hasn't fired since the last Register).
func (r *AgentRecord) Connected() bool {
	return r.DisconnectedAt.IsZero()
}

// HasKubeProxy reports whether this agent's capabilities advertised
// the kube-proxy role — i.e. whether the cluster.Manager should
// restore an agent-proxy context for it on boot.
func (r *AgentRecord) HasKubeProxy() bool {
	for _, c := range r.Capabilities {
		if c == "kube-proxy" {
			return true
		}
	}
	return false
}

// AgentStore persists the registry across restarts. The interface is
// extracted so tests can swap a memory impl for the BoltDB one.
//
// Concurrency: implementations must be safe for concurrent use — the
// registry's Register/Unregister paths call into the store from
// arbitrary goroutines.
type AgentStore interface {
	// Upsert writes the full record, replacing any prior copy with
	// the same (cluster_id, agent_id). Caller is responsible for
	// stamping FirstSeen + LastSeen + DisconnectedAt on the value.
	Upsert(rec *AgentRecord) error
	// MarkDisconnected sets DisconnectedAt=now and bumps LastSeen.
	// No-op when the record doesn't exist.
	MarkDisconnected(clusterID, agentID string, at time.Time) error
	// List returns every record currently in the store, sorted by
	// (cluster_id, agent_id) for deterministic order.
	List() ([]AgentRecord, error)
	// Prune deletes records whose DisconnectedAt is older than
	// `before`. Returns the count of removed records.
	Prune(before time.Time) (int, error)
}

// recordKey is the BoltDB key for a record. Composite so the store
// can List/Prune efficiently without unmarshaling values; values
// stay JSON-encoded for forward-compat (adding fields is a rolling
// upgrade rather than a schema migration).
func recordKey(clusterID, agentID string) []byte {
	return []byte(clusterID + "/" + agentID)
}

// ─── BoltDB implementation ────────────────────────────────────────

// BoltAgentStore is the production AgentStore — backed by the same
// BoltDB file that already holds users + tenants + cluster configs.
// One bucket (`agents`) holds JSON-encoded AgentRecord values keyed
// by `<clusterID>/<agentID>`.
type BoltAgentStore struct {
	db     *bolt.DB
	bucket []byte
}

// NewBoltAgentStore wires the store to a BoltDB handle + bucket name.
// The bucket must already exist — it's created at boot in
// auth.NewStore so the schema lives in one place.
func NewBoltAgentStore(db *bolt.DB, bucket []byte) *BoltAgentStore {
	return &BoltAgentStore{db: db, bucket: bucket}
}

func (s *BoltAgentStore) Upsert(rec *AgentRecord) error {
	if rec == nil {
		return fmt.Errorf("nil AgentRecord")
	}
	if rec.ClusterID == "" || rec.AgentID == "" {
		return fmt.Errorf("AgentRecord missing clusterID or agentID")
	}
	payload, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshal AgentRecord: %w", err)
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(s.bucket)
		if b == nil {
			return fmt.Errorf("bucket %s not found", s.bucket)
		}
		return b.Put(recordKey(rec.ClusterID, rec.AgentID), payload)
	})
}

func (s *BoltAgentStore) MarkDisconnected(clusterID, agentID string, at time.Time) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(s.bucket)
		if b == nil {
			return fmt.Errorf("bucket %s not found", s.bucket)
		}
		key := recordKey(clusterID, agentID)
		raw := b.Get(key)
		if raw == nil {
			return nil // no-op when record doesn't exist
		}
		var rec AgentRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			return fmt.Errorf("unmarshal AgentRecord: %w", err)
		}
		rec.LastSeen = at
		rec.DisconnectedAt = at
		payload, err := json.Marshal(&rec)
		if err != nil {
			return fmt.Errorf("marshal AgentRecord: %w", err)
		}
		return b.Put(key, payload)
	})
}

func (s *BoltAgentStore) List() ([]AgentRecord, error) {
	var out []AgentRecord
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(s.bucket)
		if b == nil {
			return fmt.Errorf("bucket %s not found", s.bucket)
		}
		return b.ForEach(func(_, v []byte) error {
			var rec AgentRecord
			if err := json.Unmarshal(v, &rec); err != nil {
				// Skip corrupt records but keep iterating — a single
				// bad value shouldn't blank the whole registry on
				// boot. Future: log via slog so operators see drift.
				return nil
			}
			out = append(out, rec)
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ClusterID != out[j].ClusterID {
			return out[i].ClusterID < out[j].ClusterID
		}
		return out[i].AgentID < out[j].AgentID
	})
	return out, nil
}

func (s *BoltAgentStore) Prune(before time.Time) (int, error) {
	var removed int
	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(s.bucket)
		if b == nil {
			return fmt.Errorf("bucket %s not found", s.bucket)
		}
		var toDelete [][]byte
		err := b.ForEach(func(k, v []byte) error {
			var rec AgentRecord
			if err := json.Unmarshal(v, &rec); err != nil {
				return nil
			}
			// Connected records (DisconnectedAt zero) never get
			// pruned. Disconnected records older than the horizon
			// drop out.
			if !rec.DisconnectedAt.IsZero() && rec.DisconnectedAt.Before(before) {
				// Copy because k is only valid for the duration of
				// the iteration.
				keyCopy := make([]byte, len(k))
				copy(keyCopy, k)
				toDelete = append(toDelete, keyCopy)
			}
			return nil
		})
		if err != nil {
			return err
		}
		for _, k := range toDelete {
			if err := b.Delete(k); err != nil {
				return err
			}
		}
		removed = len(toDelete)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return removed, nil
}

// ─── Memory implementation (tests) ────────────────────────────────

// MemoryAgentStore is the in-memory AgentStore for tests that don't
// want to spin up a temp BoltDB. Same semantics as BoltAgentStore;
// thread-safe.
type MemoryAgentStore struct {
	mu      sync.RWMutex
	records map[string]*AgentRecord // key = recordKey(clusterID, agentID) as string
}

func NewMemoryAgentStore() *MemoryAgentStore {
	return &MemoryAgentStore{records: make(map[string]*AgentRecord)}
}

func (s *MemoryAgentStore) Upsert(rec *AgentRecord) error {
	if rec == nil {
		return fmt.Errorf("nil AgentRecord")
	}
	if rec.ClusterID == "" || rec.AgentID == "" {
		return fmt.Errorf("AgentRecord missing clusterID or agentID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *rec
	s.records[string(recordKey(rec.ClusterID, rec.AgentID))] = &cp
	return nil
}

func (s *MemoryAgentStore) MarkDisconnected(clusterID, agentID string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[string(recordKey(clusterID, agentID))]
	if !ok {
		return nil
	}
	rec.LastSeen = at
	rec.DisconnectedAt = at
	return nil
}

func (s *MemoryAgentStore) List() ([]AgentRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]AgentRecord, 0, len(s.records))
	for _, rec := range s.records {
		out = append(out, *rec)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ClusterID != out[j].ClusterID {
			return out[i].ClusterID < out[j].ClusterID
		}
		return out[i].AgentID < out[j].AgentID
	})
	return out, nil
}

func (s *MemoryAgentStore) Prune(before time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var removed int
	for k, rec := range s.records {
		if !rec.DisconnectedAt.IsZero() && rec.DisconnectedAt.Before(before) {
			delete(s.records, k)
			removed++
		}
	}
	return removed, nil
}
