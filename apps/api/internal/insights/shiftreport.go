package insights

import (
	"context"
	"time"
)

// Fase 3 (PR 3.3): the shift report's data layer — the numbers behind
// «mientras no estabas». The API assembles the report (it also holds the
// capability registry and the policy service); this file only defines the
// read surface the episode store provides for it.

// ShiftEpisodeStats counts EVENTS inside the window — what opened, what
// resolved, what expired while the user was away. Standing conditions that
// merely overlap the window are the Active list's business, not the
// report's (in-vivo 31-ago: one minute away re-showed the full report).
type ShiftEpisodeStats struct {
	Opened        int64 `json:"opened"`
	AutoRecovered int64 `json:"autoRecovered"`
	Remediated    int64 `json:"remediated"`
	Expired       int64 `json:"expired"`
	// StillFiring: of the episodes OPENED in the window, how many are still
	// burning — the new problems that persist.
	StillFiring int64 `json:"stillFiring"`
	Criticals   int64 `json:"criticals"`
}

// ShiftWorstEpisode is the longest episode of the window — the report's
// «el peor» sentence.
type ShiftWorstEpisode struct {
	ID       string `json:"id"` // links the sentence to the episode
	Resource string `json:"resource"`
	RuleID   string `json:"ruleId"`
	Title    string `json:"title,omitempty"`
	Status   string `json:"status"`
	Seconds  int64  `json:"seconds"`
}

// ShiftMuteStats counts the silence overlay: what was muted during the
// window, and how many mutes stand right now.
type ShiftMuteStats struct {
	CreatedInWindow int64 `json:"createdInWindow"`
	ActiveNow       int64 `json:"activeNow"`
}

// ShiftStatsReader is implemented by the EE episode store; nil elsewhere
// (the report endpoint 503s, like the rest of history).
type ShiftStatsReader interface {
	WindowStats(ctx context.Context, org string, from, to time.Time) (ShiftEpisodeStats, error)
	WorstEpisode(ctx context.Context, org string, from, to time.Time) (*ShiftWorstEpisode, error)
	MuteStats(ctx context.Context, org string, from time.Time) (ShiftMuteStats, error)
}
