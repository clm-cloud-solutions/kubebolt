//go:build !ee

package main

import (
	bolt "go.etcd.io/bbolt"

	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
)

// newSessionStore is the OSS factory for the Kobi usage/analytics store: the
// BoltDB *copilot.UsageStore. EE (`-tags ee`) overrides with Postgres when
// KUBEBOLT_DB_DSN is set. main.go stays identical across editions.
func newSessionStore(db *bolt.DB, bucket []byte) copilot.SessionStore {
	return copilot.NewUsageStore(db, bucket)
}
