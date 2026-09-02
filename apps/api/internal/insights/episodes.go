package insights

import (
	"context"
	"sync/atomic"
	"time"
)

// Fase 2 of the lifecycle plan (D-2): the episode becomes a first-class,
// SQL-queryable row instead of an entry in the head record's bounded jsonb
// ring. The head `insights` row stays the identity ("cabeza de serie": one
// per fingerprint, current status, latest content); `insight_episodes` holds
// one row per episode and `insight_transitions` its append-only history.
//
// The episode id IS the Occurrence id the ring already carries — the value
// Kobi triggers, ActionProposals and Autopilot's trigger_source_ref have
// referenced all along, so no consumer re-keys.
//
// The engine emits SEMANTIC events at its existing decision points
// (admitNew / persistActive / the resolve loop) through the EpisodeSink
// seam. OSS (no sink) keeps the ring-only behavior; the EE sink writes the
// rows. Same wiring pattern as the policy source: engines are born inside
// the cluster manager, so the seam is package-level.

// Episode states. The head row's status mirrors the CURRENT episode's state
// ("active" maps to firing; "resolved"/"expired" carry over verbatim).
const (
	EpisodeFiring   = "firing"
	EpisodeResolved = "resolved"
	EpisodeExpired  = "expired"
	// EpisodeSuperseded is reserved for the dedup/merge engine (later fase).
	EpisodeSuperseded = "superseded"
)

// Resolution kinds (§3.1 of the v2.1 doc): what turned the light off. They
// convert history into narrative ("169 auto-resolved") and feed «Ignorados
// 30d» (#44).
const (
	ResolutionAutoRecovered = "auto_recovered"
	ResolutionRemediated    = "remediated" // via an executed action (action_id set)
	ResolutionManual        = "manual"
	ResolutionRuleChanged   = "rule_changed" // a policy change (e.g. severity off) cleared it
)

// ReopenCooldown is A1 (decided 2026-08-31): a fingerprint that re-fires
// within this window of resolving is the SAME episode flapping (flap_count++,
// no new notification), not a new finding. Outside the window it is a new
// episode. Var, not const, so tests can shrink it.
var ReopenCooldown = 10 * time.Minute

// ExpireTTL is the staleness bound for the watchdog: a firing episode whose
// last_seen is older than this is no longer VERIFIABLE — the cluster stopped
// evaluating (agent gone, cluster deleted). expired ≠ resolved: nobody
// observed recovery. Overridable via KUBEBOLT_INSIGHT_EXPIRE_TTL (wired in
// cmd/server).
var ExpireTTL = 15 * time.Minute

// EpisodeSink receives the engine's lifecycle events. Implementations must
// be cheap and non-blocking on the evaluation path (the EE sink batches
// touches with a per-episode 1/min throttle). All identity comes from rec
// (tenant/cluster/fingerprint/rule/resource) plus the episode id.
type EpisodeSink interface {
	// EpisodeOpened: a genuinely new episode. prevEpisodeID links a reopen
	// after `expired` (A2 — there was an observation gap) and is ""
	// otherwise.
	EpisodeOpened(rec *InsightRecord, episodeID string, at time.Time, prevEpisodeID string)
	// EpisodeFlapped: re-fire within ReopenCooldown — same episode, one more
	// flap. The episode returns to firing.
	EpisodeFlapped(rec *InsightRecord, episodeID string, at time.Time, flapCount int)
	// EpisodeTouched: still firing; refresh last_seen (impl may throttle).
	EpisodeTouched(rec *InsightRecord, episodeID string, at time.Time)
	// EpisodeSeverityChanged: the produced severity moved while firing
	// (escalation or de-escalation). Impls keep max_severity and record a
	// transition — escalation is what later pierces mutes (#54).
	EpisodeSeverityChanged(rec *InsightRecord, episodeID string, from, to string, at time.Time)
	// EpisodeResolved: the condition cleared. kind is a Resolution* value.
	EpisodeResolved(rec *InsightRecord, episodeID string, at time.Time, kind string)
}

// sinkBox wraps the sink so atomic.Value always stores ONE concrete type —
// storing different implementations directly panics ("inconsistent type").
type sinkBox struct{ s EpisodeSink }

var episodeSink atomic.Value // sinkBox

// SetEpisodeSink installs the episode sink (EE wiring). nil resets (tests/OSS).
func SetEpisodeSink(s EpisodeSink) {
	episodeSink.Store(sinkBox{s: s})
}

func sink() EpisodeSink {
	if b, ok := episodeSink.Load().(sinkBox); ok {
		return b.s
	}
	return nil
}

// EpisodeQuery filters a Window read. Zero values are unbounded (Until
// zero = now). Limit is clamped by the store.
type EpisodeQuery struct {
	ClusterID string
	Status    string
	Severity  string // matches the episode's MAX severity
	RuleID    string
	Since     time.Time
	Until     time.Time
	Limit     int32
	Offset    int32
}

// EpisodeReader is the read side the API serves (PR 2.2). Implemented by the
// EE Postgres store; nil on installs without it (OSS) — endpoints 503.
type EpisodeReader interface {
	Window(ctx context.Context, org string, q EpisodeQuery) ([]Episode, error)
	Episode(ctx context.Context, org, id string) (Episode, []Transition, error)
	ByFingerprint(ctx context.Context, org, fingerprint string, limit int32) ([]Episode, error)
	IgnoredRate(ctx context.Context, org string, window time.Duration) (map[string][2]int64, error)
}

// Episode is the read-side shape of one insight_episodes row.
type Episode struct {
	ID        string `json:"id"`
	TenantID  string `json:"-"`
	ClusterID string `json:"clusterId"`
	// ClusterName is enriched by the API layer from the org's persisted
	// display names — it survives the cluster's death, unlike the fleet list.
	ClusterName    string     `json:"clusterName,omitempty"`
	Fingerprint    string     `json:"fingerprint"`
	RuleID         string     `json:"ruleId"`
	Resource       string     `json:"resource"`
	Namespace      string     `json:"namespace,omitempty"`
	Title          string     `json:"title,omitempty"`
	Status         string     `json:"status"`
	Severity       string     `json:"severity"`
	MaxSeverity    string     `json:"maxSeverity"`
	FirstSeen      time.Time  `json:"firstSeen"`
	LastSeen       time.Time  `json:"lastSeen"`
	ResolvedAt     *time.Time `json:"resolvedAt,omitempty"`
	ResolutionKind string     `json:"resolutionKind,omitempty"`
	FlapCount      int        `json:"flapCount"`
	AckedBy        string     `json:"ackedBy,omitempty"`
	AckedAt        *time.Time `json:"ackedAt,omitempty"`
	PrevEpisodeID  string     `json:"prevEpisodeId,omitempty"`
}

// Transition is one append-only history entry of an episode.
type Transition struct {
	EpisodeID string    `json:"episodeId"`
	FromState string    `json:"from"`
	ToState   string    `json:"to"`
	At        time.Time `json:"at"`
	Actor     string    `json:"actor"` // system | rule:<id> | watchdog | user:<id>
	Reason    string    `json:"reason,omitempty"`
}
