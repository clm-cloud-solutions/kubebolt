//go:build !ee

package main

import (
	"context"
	"log"
	"log/slog"
	"os"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/insights"
)

// startEpisodeLifecycle (OSS build) wires the episode sink — the engines'
// lifecycle events become rows in the insight_episodes / insight_transitions
// buckets — and starts the expired watchdog: firing episodes whose cluster
// stopped evaluating (agent gone, cluster deleted) flip to `expired` instead
// of freezing as "active" forever. Single tenant, so one sweep per tick.
// The Enterprise build (-tags ee) replaces this with the Postgres store and a
// per-org sweep (episodes_ee.go).
//
// heads is the insight head store: an expiry flips the head row too, so the
// Active list stops showing a condition nobody can verify.
func startEpisodeLifecycle(ctx context.Context, db *bolt.DB, heads insights.InsightStore) insights.EpisodeReader {
	if db == nil {
		return nil
	}
	ep, tr, mu, mk, pr, op := auth.InsightEpisodeBuckets()
	boltHeads, _ := heads.(*insights.BoltInsightStore)
	store := insights.NewBoltEpisodeStore(db, boltHeads, insights.BoltEpisodeBuckets{
		Episodes: ep, Transitions: tr, Mutes: mu, MuteKeys: mk, Presence: pr, Operational: op,
	})
	insights.SetEpisodeSink(store)

	ttl := insights.ExpireTTL
	if raw := os.Getenv("KUBEBOLT_INSIGHT_EXPIRE_TTL"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			ttl = d
		} else {
			slog.Warn("invalid KUBEBOLT_INSIGHT_EXPIRE_TTL; using the default",
				slog.String("raw", raw), slog.Duration("default", ttl))
		}
	}
	log.Printf("insights: episode sink installed + expired watchdog (BoltDB, ttl %s)", ttl)

	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if n, err := store.ExpireStaleForOrg(ctx, auth.DefaultTenantName, ttl); err != nil {
					slog.Warn("episode watchdog: sweep failed", slog.String("error", err.Error()))
				} else if n > 0 {
					slog.Info("episode watchdog: expired stale episodes", slog.Int("count", n))
				}
			}
		}
	}()
	return store
}
