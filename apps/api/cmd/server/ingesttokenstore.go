package main

import (
	"fmt"
	"log/slog"

	bolt "go.etcd.io/bbolt"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
)

// newBoltIngestTokenStore opens the BoltDB ingest-token store and runs the
// one-time, idempotent MigrateInlinedTokens cutover (legacy tokens inlined in
// tenant records → dedicated buckets). Shared by both the OSS and EE factories
// (the EE factory uses it for its BoltDB fallback when no Postgres DSN is set),
// so the migrate-and-log behavior lives in exactly one place. Not build-tagged.
func newBoltIngestTokenStore(db *bolt.DB) (auth.IngestTokenStore, error) {
	its, err := auth.NewIngestTokenStore(db)
	if err != nil {
		return nil, fmt.Errorf("open ingest token store: %w", err)
	}
	if migrated, err := its.MigrateInlinedTokens(); err != nil {
		return nil, fmt.Errorf("migrate inlined ingest tokens: %w", err)
	} else if migrated > 0 {
		slog.Info("migrated inlined ingest tokens to dedicated store",
			slog.Int("count", migrated),
		)
	}
	return its, nil
}
