//go:build !ee

package main

import (
	bolt "go.etcd.io/bbolt"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
)

// newIngestTokenStore is the OSS (community) factory for the ingest-token store
// (kb_ bearer tokens): the BoltDB store, with the inlined-token cutover run at
// boot. The Enterprise build (`-tags ee`) overrides this to return a
// Postgres-backed store when KUBEBOLT_DB_DSN is set. Keeping the seam here means
// main.go stays identical between OSS and EE.
func newIngestTokenStore(db *bolt.DB) (auth.IngestTokenStore, error) {
	return newBoltIngestTokenStore(db)
}
