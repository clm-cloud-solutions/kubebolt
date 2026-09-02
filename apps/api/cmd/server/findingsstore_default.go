//go:build !ee

package main

import (
	bolt "go.etcd.io/bbolt"

	"github.com/kubebolt/kubebolt/apps/api/internal/findings"
)

// newFindingsStore (community build) always returns the BoltDB store.
// The EE build swaps a Postgres impl in when KUBEBOLT_DB_DSN is set —
// same seam pattern as newInsightStore.
func newFindingsStore(db *bolt.DB, bucket []byte) findings.Store {
	return findings.NewBoltStore(db, bucket)
}

// newEventStore (community build) — BoltDB runtime-events store.
func newEventStore(db *bolt.DB, bucket []byte) findings.EventStore {
	return findings.NewBoltEventStore(db, bucket)
}
