package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
)

// History retention — one hourly pass, per org, over every table that
// accumulates without bound.
//
// It is per-ORG rather than one global sweep because the stores' delete verbs
// are org-scoped by contract (audit.Store, insights.InsightStore,
// copilot.ConversationStore all expose PruneOrg): the EE build runs these
// stores on Postgres under row-level security, where a DELETE issued with no
// org matches ZERO rows and reports success — the previous per-store tickers
// did exactly that and had therefore never pruned anything there. Sharing the
// per-org shape keeps this file and the stores identical across editions; in
// OSS there is exactly one org and the loop runs once.
//
// The WINDOWS are where the editions differ. EE resolves each org's window
// from its plan (with a post-downgrade grace); OSS has no plans, so every org
// gets the operator-configured horizons below, read from the environment on
// every pass so a change is picked up without a code edit.
//
// Cost is trivial: N orgs × 3 deletes, once an hour, off the request path.
const retentionInterval = 1 * time.Hour

// retentionStartDelay is how long after boot the FIRST pass runs. It exists
// because a ticker-only job never fires on a process that restarts more often
// than its interval — a crash-looping or frequently-redeployed API would carry
// an hourly ticker it never reaches, and retention would silently never happen
// again. Short enough to be observable when validating, late enough to stay out
// of the way of everything else coming up at boot. Var, not const, so a test can
// shrink it.
var retentionStartDelay = 1 * time.Minute

// The deps are the narrowest interfaces this job actually uses, not the full
// stores. Retention needs three verbs, and depending on three verbs is what
// makes a pass testable without standing up the persistence engines.
type orgLister interface {
	ListTenants() ([]auth.Tenant, error)
}

type orgPruner interface {
	PruneOrg(orgID string, before time.Time) (int, error)
}

type orgEventPruner interface {
	PruneEventsOrg(orgID string, before time.Time) (int, error)
}

type retentionDeps struct {
	tenants       orgLister
	insights      orgPruner
	findings      orgPruner
	events        orgEventPruner
	audit         orgPruner
	conversations orgPruner
	// episodes prunes insight episodes + transitions (2.1.0). The store only
	// ever deletes NON-firing episodes — an old active episode is a live
	// problem, not garbage — so the cutoff here bounds history, never
	// detection. Nil when the lifecycle store is not wired.
	episodes orgPruner
}

// envHorizon reads a Go duration from env, falling back to def when unset,
// unparsable or non-positive.
func envHorizon(name string, def time.Duration) time.Duration {
	if v := os.Getenv(name); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return def
}

// auditRetentionHorizon is how long the mutation trail is kept.
//
// It is deliberately independent of the other windows. Everything else this
// pass deletes is observability data; the audit trail answers "who changed
// what", which is a compliance record about the account. 90 days is long
// enough for a quarter of history without unbounded growth.
func auditRetentionHorizon() time.Duration {
	return envHorizon("KUBEBOLT_AUDIT_RETENTION_HORIZON", 90*24*time.Hour)
}

// insightsRetentionHorizon bounds RESOLVED insights (active ones never expire).
// The same knob the standalone ticker read since Sprint 0, so an operator who
// set it keeps exactly the behaviour they configured.
func insightsRetentionHorizon() time.Duration {
	return envHorizon("KUBEBOLT_INSIGHTS_RETENTION_HORIZON", 7*24*time.Hour)
}

// findingsRetentionHorizon bounds security findings and runtime events (the
// stores arrive with the Security pillar; the knob is declared with the pass
// so the operator learns about all three windows in one place).
func findingsRetentionHorizon() time.Duration {
	return envHorizon("KUBEBOLT_FINDINGS_RETENTION_HORIZON", 30*24*time.Hour)
}

// conversationsRetentionHorizon bounds Kobi transcripts — the highest-PII
// store in the product. Same env the conversation store has always read; the
// horizon simply moved from "on write, only for users who keep chatting" to
// this pass, so a user who stops talking to Kobi still expires.
func conversationsRetentionHorizon() time.Duration {
	return envHorizon("KUBEBOLT_COPILOT_CONVERSATION_RETENTION_HORIZON", copilot.DefaultConversationRetention)
}

// startRetention launches the hourly pass. Returns immediately; the goroutine
// stops when ctx is cancelled. A nil tenant store (auth disabled) means there is
// no org list to iterate and nothing runs.
func startRetention(ctx context.Context, d retentionDeps) {
	if d.tenants == nil {
		return
	}
	go func() {
		first := time.NewTimer(retentionStartDelay)
		defer first.Stop()
		select {
		case <-ctx.Done():
			return
		case <-first.C:
			runRetentionPass(d, time.Now().UTC())
		}
		ticker := time.NewTicker(retentionInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runRetentionPass(d, time.Now().UTC())
			}
		}
	}()
}

// runRetentionPass is the body of one tick, split out so a test can call it
// directly without waiting an hour.
func runRetentionPass(d retentionDeps, now time.Time) {
	orgs, err := d.tenants.ListTenants()
	if err != nil {
		slog.Warn("retention: cannot list orgs", slog.String("error", err.Error()))
		return
	}
	var totalInsights, totalFindings, totalEvents, totalAudit, totalConversations, totalEpisodes int
	auditCutoff := now.Add(-auditRetentionHorizon())
	insightsCutoff := now.Add(-insightsRetentionHorizon())
	findingsCutoff := now.Add(-findingsRetentionHorizon())
	conversationsCutoff := now.Add(-conversationsRetentionHorizon())
	for _, org := range orgs {
		if d.audit != nil {
			if n, err := d.audit.PruneOrg(org.ID, auditCutoff); err != nil {
				slog.Warn("retention: audit prune failed",
					slog.String("org", org.ID), slog.String("error", err.Error()))
			} else {
				totalAudit += n
			}
		}
		if d.insights != nil {
			if n, err := d.insights.PruneOrg(org.ID, insightsCutoff); err != nil {
				slog.Warn("retention: insights prune failed",
					slog.String("org", org.ID), slog.String("error", err.Error()))
			} else {
				totalInsights += n
			}
		}
		if d.episodes != nil {
			if n, err := d.episodes.PruneOrg(org.ID, findingsCutoff); err != nil {
				slog.Warn("retention: episodes prune failed",
					slog.String("org", org.ID), slog.String("error", err.Error()))
			} else {
				totalEpisodes += n
			}
		}
		if d.findings != nil {
			if n, err := d.findings.PruneOrg(org.ID, findingsCutoff); err != nil {
				slog.Warn("retention: findings prune failed",
					slog.String("org", org.ID), slog.String("error", err.Error()))
			} else {
				totalFindings += n
			}
		}
		if d.events != nil {
			if n, err := d.events.PruneEventsOrg(org.ID, findingsCutoff); err != nil {
				slog.Warn("retention: runtime-events prune failed",
					slog.String("org", org.ID), slog.String("error", err.Error()))
			} else {
				totalEvents += n
			}
		}
		if d.conversations != nil {
			if n, err := d.conversations.PruneOrg(org.ID, conversationsCutoff); err != nil {
				slog.Warn("retention: conversations prune failed",
					slog.String("org", org.ID), slog.String("error", err.Error()))
			} else {
				totalConversations += n
			}
		}
	}
	// Logged on EVERY pass, including the all-zero one. A retention job that
	// only speaks up when it deletes something cannot be distinguished from one
	// that is broken.
	slog.Info("retention pass",
		slog.Int("orgs", len(orgs)),
		slog.Int("insights_pruned", totalInsights),
		slog.Int("episodes_pruned", totalEpisodes),
		slog.Int("findings_pruned", totalFindings),
		slog.Int("runtime_events_pruned", totalEvents),
		slog.Int("audit_records_pruned", totalAudit),
		slog.Int("conversations_pruned", totalConversations),
	)
}
