package channel

import (
	"context"
	"log/slog"
	"time"
)

// LivenessHeartbeat is how often a replica refreshes the records of the agents
// it holds. Far below any prune horizon on purpose: a record must miss many
// heartbeats in a row before PruneUnseen may take it.
const LivenessHeartbeat = 5 * time.Minute

// MinUnseenHorizon is the shortest horizon PruneUnseen is ever given, whatever
// the configured prune horizon: twelve missed heartbeats.
const MinUnseenHorizon = 12 * LivenessHeartbeat

// UnseenHorizon is the horizon for connected records nobody refreshes: the
// registry's prune horizon, never below MinUnseenHorizon.
func UnseenHorizon(pruneHorizon time.Duration) time.Duration {
	if pruneHorizon < MinUnseenHorizon {
		return MinUnseenHorizon
	}
	return pruneHorizon
}

// RunLivenessHeartbeat refreshes the records of the agents this replica holds
// every `every`, until ctx ends. A failed refresh is logged and retried on the
// next beat; the horizon is many beats long.
func RunLivenessHeartbeat(ctx context.Context, reg *AgentRegistry, store LivenessStore, every time.Duration) {
	if reg == nil || store == nil || every <= 0 {
		return
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := store.TouchConnected(reg.LiveKeys(), time.Now().UTC()); err != nil {
				slog.Warn("agent liveness heartbeat failed", slog.String("error", err.Error()))
			}
		}
	}
}
