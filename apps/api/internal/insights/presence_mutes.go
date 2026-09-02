package insights

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Fase 3 (PR 3.1): the two anchors of the shift report.
//
// Presence — «¿desde cuándo no ve el dashboard este usuario?» — is what the
// report's window hangs from. users.last_login_at was ruled out (refresh-token
// rotation freezes it); the truthful mark is the frontend saying "Home just
// rendered for this user".
//
// Mutes (#54) — the per-resource silence layer, keyed on cluster UID. The
// report is born counting them: «N silenced while you were away» is part of
// the story, which is why the schema ships in the same PR as presence.

// PresenceStore records and reads per-user dashboard presence. Implemented by
// the EE Postgres episode store; nil on installs without it.
type PresenceStore interface {
	MarkDashboardRendered(ctx context.Context, org, userID string, at time.Time) error
	// DashboardLastSeen returns (zero, false, nil) when the user has never
	// rendered the dashboard — a first shift, not an error.
	DashboardLastSeen(ctx context.Context, org, userID string) (time.Time, bool, error)
}

// Mute is one silence: this rule on this resource in this cluster stops
// surfacing in the default view. It is a DISPLAY layer — the engine keeps
// evaluating, episodes keep recording; only the operator's attention is
// spared. Severity escalation pierces it (#54), which is why the engine never
// consults mutes: piercing needs the underlying signal intact.
type Mute struct {
	ID        string `json:"id"`
	TenantID  string `json:"-"`
	ClusterID string `json:"clusterId"`
	// ClusterName is enriched by the API layer from persisted display names
	// (never stored) — the Silenced tab shows names, not UUIDs.
	ClusterName string    `json:"clusterName,omitempty"`
	RuleID      string    `json:"ruleId"`
	Resource    string    `json:"resource"`
	Reason      string    `json:"reason,omitempty"`
	CreatedBy   string    `json:"createdBy,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	// ExpiresAt nil + UntilResolved false = permanent (requires Reason).
	ExpiresAt     *time.Time `json:"expiresAt,omitempty"`
	UntilResolved bool       `json:"untilResolved"`
}

// MuteStore is the CRUD the API serves. Create upserts on the
// (cluster, rule, resource) key — re-muting updates the terms instead of
// stacking duplicates.
type MuteStore interface {
	CreateMute(ctx context.Context, org string, m Mute) (Mute, error)
	// DeleteMute lifts a silence; `by` names the actor for the episode
	// timeline's «unmuted» entry.
	DeleteMute(ctx context.Context, org, id, by string) error
	ListMutes(ctx context.Context, org, clusterID string) ([]Mute, error)
}

// ErrMuteNotFound distinguishes "already gone" from a store failure on delete.
var ErrMuteNotFound = errors.New("mute not found")

// ValidateMute enforces the #54 contract before anything touches the table:
// full key (cluster + rule + resource), one expiry mode at a time, and a
// permanent silence never without a written reason — the person who finds it
// in six months deserves to know why it exists.
func ValidateMute(m Mute) error {
	if m.ClusterID == "" || m.RuleID == "" || m.Resource == "" {
		return errors.New("a mute needs clusterId, ruleId and resource")
	}
	if m.ExpiresAt != nil && m.UntilResolved {
		return errors.New("choose an expiry date or until-resolved, not both")
	}
	if m.ExpiresAt != nil && !m.ExpiresAt.After(time.Now()) {
		return errors.New("expiresAt must be in the future")
	}
	if m.ExpiresAt == nil && !m.UntilResolved && strings.TrimSpace(m.Reason) == "" {
		return errors.New("a permanent mute requires a reason")
	}
	return nil
}
