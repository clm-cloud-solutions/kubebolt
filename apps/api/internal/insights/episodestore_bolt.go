package insights

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

// BoltEpisodeStore is the OSS episode store: the EpisodeSink the engines
// feed, the read side the API serves (EpisodeReader), the shift-report
// anchors (PresenceStore, MuteStore, ShiftStatsReader) and the operational
// clusterer's persistence (OperationalReader) — everything the Enterprise
// build keeps in Postgres, on the same BoltDB file that already holds the
// insight heads, findings and the audit trail.
//
// Single-tenant by construction. The org argument every contract method
// carries is accepted for signature parity with the EE store and is not part
// of any key: OSS has exactly one tenant, and the engines stamp records with
// the manager's tenant while the HTTP layer resolves the default tenant name
// — keying on either would make a lookup miss for no reason. Everything is a
// prefix-free scan over small buckets, which is the right shape for the
// scale a single install reaches (thousands of episodes, not millions).
//
// Sink writes run on the engines' evaluation goroutines, so every method is
// one short transaction, errors are logged (detection must never stall on
// history) and touches are throttled per episode (TouchInterval).
//
// Buckets (created on boot by auth.NewStore):
//
//	insight_episodes      id → Episode JSON
//	insight_transitions   id \x00 seq(uint64 BE) → Transition JSON (append-only)
//	insight_mutes         id → Mute JSON
//	insight_mute_keys     cluster \x00 rule \x00 resource → id (the upsert key)
//	user_dashboard_seen   userID → RFC3339 (presence anchor)
//	operational_episodes  id → OperationalEpisode JSON
type BoltEpisodeStore struct {
	db *bolt.DB
	// heads is the insight head store: a watchdog expiry flips the head row
	// too, so the Active list stops showing a condition nobody can verify.
	heads *BoltInsightStore

	episodesBucket    []byte
	transitionsBucket []byte
	mutesBucket       []byte
	muteKeysBucket    []byte
	presenceBucket    []byte
	operationalBucket []byte

	mu        sync.Mutex
	lastTouch map[string]time.Time
	nowFn     func() time.Time
}

// TouchInterval is the write-behind bound for last_seen bumps (§5 of the
// v2.1 doc: at most one persisted touch per minute per episode).
const TouchInterval = time.Minute

// BoltEpisodeBuckets names the six buckets the store lives in.
type BoltEpisodeBuckets struct {
	Episodes, Transitions, Mutes, MuteKeys, Presence, Operational []byte
}

// NewBoltEpisodeStore opens the store over an existing BoltDB handle. The
// buckets must already exist (auth.NewStore creates them). heads may be nil
// in tests; then the watchdog only flips episodes.
func NewBoltEpisodeStore(db *bolt.DB, heads *BoltInsightStore, b BoltEpisodeBuckets) *BoltEpisodeStore {
	return &BoltEpisodeStore{
		db: db, heads: heads,
		episodesBucket: b.Episodes, transitionsBucket: b.Transitions,
		mutesBucket: b.Mutes, muteKeysBucket: b.MuteKeys,
		presenceBucket: b.Presence, operationalBucket: b.Operational,
		lastTouch: map[string]time.Time{},
		nowFn:     time.Now,
	}
}

// maxSeverity keeps the episode's high-water mark. severityRank orders
// critical=0 < warning=1 < info=2 (LOWER = more severe — it's a sort-first
// rank), so the comparison is inverted.
func maxSeverity(a, b string) string {
	rb, okB := severityRank[b]
	ra, okA := severityRank[a]
	if okB && (!okA || rb < ra) {
		return b
	}
	return a
}

func (s *BoltEpisodeStore) warn(op string, err error) {
	if err != nil {
		slog.Warn("insight episodes: write failed", slog.String("op", op), slog.String("error", err.Error()))
	}
}

// ─── low-level helpers (caller holds a tx) ──────────────────────────────────

// storedEpisode is the on-disk shape: Episode hides TenantID from its JSON
// (the API never returns it) but the head store keys on it, so the row
// carries the tenant alongside.
type storedEpisode struct {
	Tenant  string  `json:"tenant,omitempty"`
	Episode Episode `json:"episode"`
}

func (s *BoltEpisodeStore) getEpisode(tx *bolt.Tx, id string) (Episode, bool) {
	var row storedEpisode
	v := tx.Bucket(s.episodesBucket).Get([]byte(id))
	if v == nil {
		return Episode{}, false
	}
	if err := json.Unmarshal(v, &row); err != nil {
		return Episode{}, false
	}
	row.Episode.TenantID = row.Tenant
	return row.Episode, true
}

func (s *BoltEpisodeStore) putEpisode(tx *bolt.Tx, ep Episode) error {
	data, err := json.Marshal(storedEpisode{Tenant: ep.TenantID, Episode: ep})
	if err != nil {
		return err
	}
	return tx.Bucket(s.episodesBucket).Put([]byte(ep.ID), data)
}

func (s *BoltEpisodeStore) addTransition(tx *bolt.Tx, t Transition) error {
	b := tx.Bucket(s.transitionsBucket)
	seq, err := b.NextSequence()
	if err != nil {
		return err
	}
	key := make([]byte, 0, len(t.EpisodeID)+9)
	key = append(key, t.EpisodeID...)
	key = append(key, 0)
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], seq)
	key = append(key, n[:]...)
	data, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return b.Put(key, data)
}

func (s *BoltEpisodeStore) transitionsOf(tx *bolt.Tx, id string) []Transition {
	var out []Transition
	prefix := append([]byte(id), 0)
	c := tx.Bucket(s.transitionsBucket).Cursor()
	for k, v := c.Seek(prefix); k != nil && len(k) > len(prefix) && string(k[:len(prefix)]) == string(prefix); k, v = c.Next() {
		var t Transition
		if json.Unmarshal(v, &t) == nil {
			out = append(out, t)
		}
	}
	return out
}

func (s *BoltEpisodeStore) forEachEpisode(tx *bolt.Tx, fn func(ep Episode)) {
	_ = tx.Bucket(s.episodesBucket).ForEach(func(k, v []byte) error {
		var row storedEpisode
		if json.Unmarshal(v, &row) == nil {
			row.Episode.TenantID = row.Tenant
			fn(row.Episode)
		}
		return nil
	})
}

func (s *BoltEpisodeStore) forEachMute(tx *bolt.Tx, fn func(m Mute)) {
	_ = tx.Bucket(s.mutesBucket).ForEach(func(k, v []byte) error {
		var m Mute
		if json.Unmarshal(v, &m) == nil {
			fn(m)
		}
		return nil
	})
}

func muteKey(clusterID, ruleID, resource string) []byte {
	return []byte(clusterID + "\x00" + ruleID + "\x00" + resource)
}

// ─── EpisodeSink ────────────────────────────────────────────────────────────

func (s *BoltEpisodeStore) EpisodeOpened(rec *InsightRecord, episodeID string, at time.Time, prevEpisodeID string) {
	s.warn("open", s.db.Update(func(tx *bolt.Tx) error {
		ep := Episode{
			ID: episodeID, TenantID: rec.TenantID, ClusterID: rec.ClusterID,
			Fingerprint: rec.Fingerprint, RuleID: rec.RuleID, Resource: rec.Resource,
			Namespace: rec.Namespace, Title: rec.Title,
			Status: EpisodeFiring, Severity: rec.Severity, MaxSeverity: rec.Severity,
			FirstSeen: at, LastSeen: at, PrevEpisodeID: prevEpisodeID,
		}
		if err := s.putEpisode(tx, ep); err != nil {
			return err
		}
		reason := ""
		if prevEpisodeID != "" {
			reason = "reopened after expired — there was an observation gap"
		}
		return s.addTransition(tx, Transition{
			EpisodeID: episodeID, FromState: "", ToState: EpisodeFiring, At: at,
			Actor: "rule:" + rec.RuleID, Reason: reason,
		})
	}))
}

func (s *BoltEpisodeStore) EpisodeFlapped(rec *InsightRecord, episodeID string, at time.Time, flapCount int) {
	s.warn("flap", s.db.Update(func(tx *bolt.Tx) error {
		ep, ok := s.getEpisode(tx, episodeID)
		if !ok {
			// first_seen is only known from the row; an unknown id opens fresh.
			ep = Episode{ID: episodeID, FirstSeen: at, PrevEpisodeID: ""}
		}
		ep.TenantID, ep.ClusterID = rec.TenantID, rec.ClusterID
		ep.Fingerprint, ep.RuleID, ep.Resource = rec.Fingerprint, rec.RuleID, rec.Resource
		ep.Namespace, ep.Title = rec.Namespace, rec.Title
		ep.Status = EpisodeFiring
		ep.Severity = rec.Severity
		ep.MaxSeverity = maxSeverity(ep.MaxSeverity, rec.Severity)
		ep.LastSeen = at
		ep.ResolvedAt, ep.ResolutionKind = nil, ""
		ep.FlapCount = flapCount
		if err := s.putEpisode(tx, ep); err != nil {
			return err
		}
		return s.addTransition(tx, Transition{
			EpisodeID: episodeID, FromState: EpisodeResolved, ToState: EpisodeFiring, At: at,
			Actor:  "rule:" + rec.RuleID,
			Reason: fmt.Sprintf("re-fired within the reopen cooldown (flap %d)", flapCount),
		})
	}))
}

func (s *BoltEpisodeStore) EpisodeTouched(rec *InsightRecord, episodeID string, at time.Time) {
	s.mu.Lock()
	if last, ok := s.lastTouch[episodeID]; ok && at.Sub(last) < TouchInterval {
		s.mu.Unlock()
		return
	}
	s.lastTouch[episodeID] = at
	if len(s.lastTouch) > 20000 {
		s.lastTouch = map[string]time.Time{episodeID: at}
	}
	s.mu.Unlock()
	s.warn("touch", s.db.Update(func(tx *bolt.Tx) error {
		ep, ok := s.getEpisode(tx, episodeID)
		if !ok {
			return nil
		}
		ep.LastSeen = at
		return s.putEpisode(tx, ep)
	}))
}

func (s *BoltEpisodeStore) EpisodeSeverityChanged(rec *InsightRecord, episodeID string, from, to string, at time.Time) {
	s.warn("severity", s.db.Update(func(tx *bolt.Tx) error {
		ep, ok := s.getEpisode(tx, episodeID)
		if !ok {
			return nil
		}
		ep.Severity = to
		ep.MaxSeverity = maxSeverity(ep.MaxSeverity, to)
		if err := s.putEpisode(tx, ep); err != nil {
			return err
		}
		return s.addTransition(tx, Transition{
			EpisodeID: episodeID, FromState: EpisodeFiring, ToState: EpisodeFiring, At: at,
			Actor: "system", Reason: "severity " + from + " → " + to,
		})
	}))
}

func (s *BoltEpisodeStore) EpisodeResolved(rec *InsightRecord, episodeID string, at time.Time, kind string) {
	s.warn("resolve", s.db.Update(func(tx *bolt.Tx) error {
		ep, ok := s.getEpisode(tx, episodeID)
		if !ok {
			return nil
		}
		t := at
		ep.Status = EpisodeResolved
		ep.ResolvedAt = &t
		ep.ResolutionKind = kind
		ep.LastSeen = at
		if err := s.putEpisode(tx, ep); err != nil {
			return err
		}
		if err := s.addTransition(tx, Transition{
			EpisodeID: episodeID, FromState: EpisodeFiring, ToState: EpisodeResolved, At: at,
			Actor: "system", Reason: kind,
		}); err != nil {
			return err
		}
		// «Until resolved» mutes on this key are consumed by the resolution —
		// same transaction, so the silence and its trigger can't diverge.
		return s.consumeResolvedMutes(tx, rec.ClusterID, rec.RuleID, rec.Resource)
	}))
}

func (s *BoltEpisodeStore) consumeResolvedMutes(tx *bolt.Tx, clusterID, ruleID, resource string) error {
	key := muteKey(clusterID, ruleID, resource)
	id := tx.Bucket(s.muteKeysBucket).Get(key)
	if id == nil {
		return nil
	}
	v := tx.Bucket(s.mutesBucket).Get(id)
	if v == nil {
		return tx.Bucket(s.muteKeysBucket).Delete(key)
	}
	var m Mute
	if err := json.Unmarshal(v, &m); err != nil || !m.UntilResolved {
		return nil
	}
	if err := tx.Bucket(s.mutesBucket).Delete(id); err != nil {
		return err
	}
	return tx.Bucket(s.muteKeysBucket).Delete(key)
}

var _ EpisodeSink = (*BoltEpisodeStore)(nil)

// ─── watchdog + retention ───────────────────────────────────────────────────

// ExpireStaleForOrg is the watchdog pass: firing episodes with no signal for
// longer than ttl flip to expired, their head rows follow, and each gets its
// transition. Returns how many expired. The org is informational (single
// tenant).
func (s *BoltEpisodeStore) ExpireStaleForOrg(ctx context.Context, org string, ttl time.Duration) (int, error) {
	now := s.nowFn().UTC()
	cutoff := now.Add(-ttl)
	var expired []Episode
	err := s.db.Update(func(tx *bolt.Tx) error {
		var stale []Episode
		s.forEachEpisode(tx, func(ep Episode) {
			if ep.Status == EpisodeFiring && ep.LastSeen.Before(cutoff) {
				stale = append(stale, ep)
			}
		})
		for _, ep := range stale {
			ep.Status = EpisodeExpired
			if err := s.putEpisode(tx, ep); err != nil {
				return err
			}
			if err := s.addTransition(tx, Transition{
				EpisodeID: ep.ID, FromState: EpisodeFiring, ToState: EpisodeExpired, At: now,
				Actor: "watchdog", Reason: "no data from the cluster beyond the TTL — the condition stopped being verifiable",
			}); err != nil {
				return err
			}
			expired = append(expired, ep)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	// Head rows follow OUTSIDE the episode transaction: the head store owns
	// its own bucket and locking, and a head that fails to flip is logged,
	// not fatal — the episode row is already the truth the UI reads.
	if s.heads != nil {
		for _, ep := range expired {
			if err := s.heads.MarkExpired(ep.TenantID, ep.ClusterID, ep.Fingerprint, now); err != nil {
				slog.Warn("insight episodes: head expire failed", slog.String("fingerprint", ep.Fingerprint), slog.String("error", err.Error()))
			}
		}
	}
	return len(expired), nil
}

// PruneOrg deletes non-firing episodes whose last activity is older than
// `before`, transitions included, plus lapsed mutes (24h past their expiry)
// and operational episodes older than the cutoff. Firing episodes are never
// pruned regardless of age: an old active episode is a live problem, and if
// its cluster went silent the watchdog already flipped it to expired, which
// IS prunable. Joins the hourly retention pass in cmd/server/retention.go.
func (s *BoltEpisodeStore) PruneOrg(orgID string, before time.Time) (int, error) {
	var n int
	now := s.nowFn().UTC()
	err := s.db.Update(func(tx *bolt.Tx) error {
		var gone []string
		s.forEachEpisode(tx, func(ep Episode) {
			if ep.Status != EpisodeFiring && ep.LastSeen.Before(before) {
				gone = append(gone, ep.ID)
			}
		})
		eb, tb := tx.Bucket(s.episodesBucket), tx.Bucket(s.transitionsBucket)
		for _, id := range gone {
			if err := eb.Delete([]byte(id)); err != nil {
				return err
			}
			prefix := append([]byte(id), 0)
			c := tb.Cursor()
			var keys [][]byte
			for k, _ := c.Seek(prefix); k != nil && len(k) > len(prefix) && string(k[:len(prefix)]) == string(prefix); k, _ = c.Next() {
				keys = append(keys, append([]byte(nil), k...))
			}
			for _, k := range keys {
				if err := tb.Delete(k); err != nil {
					return err
				}
			}
		}
		n = len(gone)
		// Expired mutes ride the same pass; their horizon is their own expiry
		// (+24h of slack for the shift report to still count them).
		var lapsed []Mute
		s.forEachMute(tx, func(m Mute) {
			if m.ExpiresAt != nil && m.ExpiresAt.Add(24*time.Hour).Before(now) {
				lapsed = append(lapsed, m)
			}
		})
		for _, m := range lapsed {
			if err := tx.Bucket(s.mutesBucket).Delete([]byte(m.ID)); err != nil {
				return err
			}
			key := muteKey(m.ClusterID, m.RuleID, m.Resource)
			if string(tx.Bucket(s.muteKeysBucket).Get(key)) == m.ID {
				if err := tx.Bucket(s.muteKeysBucket).Delete(key); err != nil {
					return err
				}
			}
		}
		// Operational episodes age with the member episodes they summarize.
		ob := tx.Bucket(s.operationalBucket)
		var opGone [][]byte
		_ = ob.ForEach(func(k, v []byte) error {
			var op OperationalEpisode
			if json.Unmarshal(v, &op) == nil && op.WindowTo.Before(before) {
				opGone = append(opGone, append([]byte(nil), k...))
			}
			return nil
		})
		for _, k := range opGone {
			if err := ob.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
	return n, err
}

// ─── EpisodeReader ──────────────────────────────────────────────────────────

func (s *BoltEpisodeStore) Window(ctx context.Context, org string, q EpisodeQuery) ([]Episode, error) {
	if q.Limit <= 0 || q.Limit > 200 {
		q.Limit = 50
	}
	until := q.Until
	if until.IsZero() {
		until = s.nowFn().Add(time.Hour)
	}
	var out []Episode
	err := s.db.View(func(tx *bolt.Tx) error {
		s.forEachEpisode(tx, func(ep Episode) {
			// Overlap semantics: episodes alive at any point of [since, until].
			if !q.Since.IsZero() && ep.LastSeen.Before(q.Since) {
				return
			}
			if ep.FirstSeen.After(until) {
				return
			}
			if q.ClusterID != "" && ep.ClusterID != q.ClusterID {
				return
			}
			if q.Status != "" && ep.Status != q.Status {
				return
			}
			if q.Severity != "" && ep.MaxSeverity != q.Severity {
				return
			}
			if q.RuleID != "" && ep.RuleID != q.RuleID {
				return
			}
			out = append(out, ep)
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FirstSeen.After(out[j].FirstSeen) })
	if int(q.Offset) >= len(out) {
		return []Episode{}, nil
	}
	out = out[q.Offset:]
	if len(out) > int(q.Limit) {
		out = out[:q.Limit]
	}
	return out, nil
}

func (s *BoltEpisodeStore) Episode(ctx context.Context, org, id string) (Episode, []Transition, error) {
	var ep Episode
	var trs []Transition
	err := s.db.View(func(tx *bolt.Tx) error {
		var ok bool
		ep, ok = s.getEpisode(tx, id)
		if !ok {
			return errors.New("episode not found")
		}
		trs = s.transitionsOf(tx, id)
		return nil
	})
	if err != nil {
		return Episode{}, nil, err
	}
	if trs == nil {
		trs = []Transition{}
	}
	return ep, trs, nil
}

func (s *BoltEpisodeStore) ByFingerprint(ctx context.Context, org, fingerprint string, limit int32) ([]Episode, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	var out []Episode
	err := s.db.View(func(tx *bolt.Tx) error {
		s.forEachEpisode(tx, func(ep Episode) {
			if ep.Fingerprint == fingerprint {
				out = append(out, ep)
			}
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FirstSeen.After(out[j].FirstSeen) })
	if len(out) > int(limit) {
		out = out[:limit]
	}
	if out == nil {
		out = []Episode{}
	}
	return out, nil
}

// IgnoredRate: per rule, episodes opened inside the window and closed
// without a human touch (no ack, no executed action) over the total closed
// (#44's honesty column).
func (s *BoltEpisodeStore) IgnoredRate(ctx context.Context, org string, window time.Duration) (map[string][2]int64, error) {
	since := s.nowFn().Add(-window)
	out := map[string][2]int64{}
	err := s.db.View(func(tx *bolt.Tx) error {
		s.forEachEpisode(tx, func(ep Episode) {
			if ep.FirstSeen.Before(since) || ep.Status == EpisodeFiring {
				return
			}
			r := out[ep.RuleID]
			r[1]++
			if ep.AckedBy == "" && ep.ResolutionKind != ResolutionRemediated && ep.ResolutionKind != ResolutionManual {
				r[0]++
			}
			out[ep.RuleID] = r
		})
		return nil
	})
	return out, err
}

var _ EpisodeReader = (*BoltEpisodeStore)(nil)

// ─── PresenceStore ──────────────────────────────────────────────────────────

func (s *BoltEpisodeStore) MarkDashboardRendered(ctx context.Context, org, userID string, at time.Time) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(s.presenceBucket).Put([]byte(userID), []byte(at.UTC().Format(time.RFC3339Nano)))
	})
}

func (s *BoltEpisodeStore) DashboardLastSeen(ctx context.Context, org, userID string) (time.Time, bool, error) {
	var raw []byte
	err := s.db.View(func(tx *bolt.Tx) error {
		if v := tx.Bucket(s.presenceBucket).Get([]byte(userID)); v != nil {
			raw = append([]byte(nil), v...)
		}
		return nil
	})
	if err != nil {
		return time.Time{}, false, err
	}
	if raw == nil {
		return time.Time{}, false, nil // never rendered — a first shift, not an error
	}
	t, err := time.Parse(time.RFC3339Nano, string(raw))
	if err != nil {
		return time.Time{}, false, err
	}
	return t, true, nil
}

var _ PresenceStore = (*BoltEpisodeStore)(nil)

// ─── MuteStore ──────────────────────────────────────────────────────────────

// muteTermsReason renders the silence terms as the timeline's English copy.
func muteTermsReason(m Mute) string {
	var terms string
	switch {
	case m.UntilResolved:
		terms = "silenced until it resolves"
	case m.ExpiresAt != nil:
		terms = "silenced until " + m.ExpiresAt.UTC().Format("Jan 2, 2006 15:04 UTC")
	default:
		terms = "silenced permanently"
	}
	if r := m.Reason; r != "" {
		terms += " — " + r
	}
	return terms
}

// muteTransition appends a muted/unmuted entry to the LATEST episode of the
// mute's key, if one exists. Best-effort by design: a mute on a resource
// with no episode yet still stands — only the narration is skipped.
func (s *BoltEpisodeStore) muteTransition(tx *bolt.Tx, clusterID, ruleID, resource, kind, by, reason string) error {
	var latest *Episode
	s.forEachEpisode(tx, func(ep Episode) {
		if ep.ClusterID != clusterID || ep.RuleID != ruleID || ep.Resource != resource {
			return
		}
		if latest == nil || ep.FirstSeen.After(latest.FirstSeen) {
			e := ep
			latest = &e
		}
	})
	if latest == nil {
		return nil
	}
	actor := "user"
	if by != "" {
		actor = "user:" + by
	}
	return s.addTransition(tx, Transition{
		EpisodeID: latest.ID, FromState: latest.Status, ToState: kind, At: s.nowFn().UTC(),
		Actor: actor, Reason: reason,
	})
}

func (s *BoltEpisodeStore) CreateMute(ctx context.Context, org string, m Mute) (Mute, error) {
	if err := ValidateMute(m); err != nil {
		return Mute{}, err
	}
	m.TenantID = org
	m.CreatedAt = s.nowFn().UTC()
	err := s.db.Update(func(tx *bolt.Tx) error {
		key := muteKey(m.ClusterID, m.RuleID, m.Resource)
		// Upsert on the key: re-muting updates the terms instead of stacking.
		if existing := tx.Bucket(s.muteKeysBucket).Get(key); existing != nil {
			m.ID = string(existing)
		} else if m.ID == "" {
			m.ID = uuid.NewString()
		}
		data, err := json.Marshal(m)
		if err != nil {
			return err
		}
		if err := tx.Bucket(s.mutesBucket).Put([]byte(m.ID), data); err != nil {
			return err
		}
		if err := tx.Bucket(s.muteKeysBucket).Put(key, []byte(m.ID)); err != nil {
			return err
		}
		return s.muteTransition(tx, m.ClusterID, m.RuleID, m.Resource, "muted", m.CreatedBy, muteTermsReason(m))
	})
	if err != nil {
		return Mute{}, err
	}
	return m, nil
}

func (s *BoltEpisodeStore) DeleteMute(ctx context.Context, org, id, by string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		v := tx.Bucket(s.mutesBucket).Get([]byte(id))
		if v == nil {
			return ErrMuteNotFound
		}
		var m Mute
		if err := json.Unmarshal(v, &m); err != nil {
			return err
		}
		if err := tx.Bucket(s.mutesBucket).Delete([]byte(id)); err != nil {
			return err
		}
		key := muteKey(m.ClusterID, m.RuleID, m.Resource)
		if string(tx.Bucket(s.muteKeysBucket).Get(key)) == id {
			if err := tx.Bucket(s.muteKeysBucket).Delete(key); err != nil {
				return err
			}
		}
		return s.muteTransition(tx, m.ClusterID, m.RuleID, m.Resource, "unmuted", by, "silence lifted")
	})
}

// ListMutes returns the ACTIVE mutes (unexpired), newest first; clusterID ""
// widens to every cluster.
func (s *BoltEpisodeStore) ListMutes(ctx context.Context, org, clusterID string) ([]Mute, error) {
	now := s.nowFn()
	out := []Mute{}
	err := s.db.View(func(tx *bolt.Tx) error {
		s.forEachMute(tx, func(m Mute) {
			if clusterID != "" && m.ClusterID != clusterID {
				return
			}
			if m.ExpiresAt != nil && !m.ExpiresAt.After(now) {
				return
			}
			out = append(out, m)
		})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, err
}

var _ MuteStore = (*BoltEpisodeStore)(nil)

// ─── OperationalReader ──────────────────────────────────────────────────────

// ClusterAndStore recomputes the operational episodes over the window with
// the deterministic clusterer and persists them (delete-window + insert;
// deterministic ids make recomputation converge), returning the fresh set.
func (s *BoltEpisodeStore) ClusterAndStore(ctx context.Context, org string, from, to time.Time) ([]OperationalEpisode, error) {
	var members []Episode
	if err := s.db.View(func(tx *bolt.Tx) error {
		s.forEachEpisode(tx, func(ep Episode) {
			if ep.LastSeen.Before(from) || ep.FirstSeen.After(to) {
				return
			}
			members = append(members, ep)
		})
		return nil
	}); err != nil {
		return nil, err
	}
	ops := ClusterEpisodes(org, members)
	if ops == nil {
		ops = []OperationalEpisode{}
	}
	err := s.db.Update(func(tx *bolt.Tx) error {
		ob := tx.Bucket(s.operationalBucket)
		var gone [][]byte
		_ = ob.ForEach(func(k, v []byte) error {
			var op OperationalEpisode
			if json.Unmarshal(v, &op) == nil && !op.WindowFrom.Before(from) && !op.WindowFrom.After(to) {
				gone = append(gone, append([]byte(nil), k...))
			}
			return nil
		})
		for _, k := range gone {
			if err := ob.Delete(k); err != nil {
				return err
			}
		}
		for _, op := range ops {
			data, err := json.Marshal(op)
			if err != nil {
				return err
			}
			if err := ob.Put([]byte(op.ID), data); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(ops, func(i, j int) bool { return ops[i].WindowFrom.Before(ops[j].WindowFrom) })
	return ops, nil
}

var _ OperationalReader = (*BoltEpisodeStore)(nil)

// ─── ShiftStatsReader ───────────────────────────────────────────────────────

// WindowStats counts EVENTS inside the window from the append-only
// transitions — what opened, resolved and expired while the user was away —
// never standing conditions that merely overlap it.
func (s *BoltEpisodeStore) WindowStats(ctx context.Context, org string, from, to time.Time) (ShiftEpisodeStats, error) {
	var st ShiftEpisodeStats
	err := s.db.View(func(tx *bolt.Tx) error {
		opened := map[string]bool{}
		_ = tx.Bucket(s.transitionsBucket).ForEach(func(k, v []byte) error {
			var t Transition
			if json.Unmarshal(v, &t) != nil || t.At.Before(from) || t.At.After(to) {
				return nil
			}
			switch {
			case t.FromState == "" && t.ToState == EpisodeFiring:
				st.Opened++
				opened[t.EpisodeID] = true
			case t.ToState == EpisodeResolved:
				if t.Reason == ResolutionAutoRecovered || t.Reason == "" {
					st.AutoRecovered++
				} else {
					st.Remediated++
				}
			case t.ToState == EpisodeExpired:
				st.Expired++
			}
			return nil
		})
		for id := range opened {
			if ep, ok := s.getEpisode(tx, id); ok {
				if ep.Status == EpisodeFiring {
					st.StillFiring++
				}
				if ep.MaxSeverity == "critical" {
					st.Criticals++
				}
			}
		}
		return nil
	})
	return st, err
}

// WorstEpisode is the longest episode opened inside the window.
func (s *BoltEpisodeStore) WorstEpisode(ctx context.Context, org string, from, to time.Time) (*ShiftWorstEpisode, error) {
	var worst *ShiftWorstEpisode
	err := s.db.View(func(tx *bolt.Tx) error {
		s.forEachEpisode(tx, func(ep Episode) {
			if ep.FirstSeen.Before(from) || ep.FirstSeen.After(to) {
				return
			}
			secs := episodeSeconds(ep)
			if worst == nil || secs > worst.Seconds {
				worst = &ShiftWorstEpisode{ID: ep.ID, Resource: ep.Resource, RuleID: ep.RuleID, Title: ep.Title, Status: ep.Status, Seconds: secs}
			}
		})
		return nil
	})
	return worst, err
}

// MuteStats counts the silence overlay: mutes created since `from`, and how
// many stand right now.
func (s *BoltEpisodeStore) MuteStats(ctx context.Context, org string, from time.Time) (ShiftMuteStats, error) {
	var st ShiftMuteStats
	now := s.nowFn()
	err := s.db.View(func(tx *bolt.Tx) error {
		s.forEachMute(tx, func(m Mute) {
			active := m.ExpiresAt == nil || m.ExpiresAt.After(now)
			if active {
				st.ActiveNow++
			}
			if !m.CreatedAt.Before(from) {
				st.CreatedInWindow++
			}
		})
		return nil
	})
	return st, err
}

var _ ShiftStatsReader = (*BoltEpisodeStore)(nil)
