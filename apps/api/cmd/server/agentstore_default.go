//go:build !ee

package main

import (
	bolt "go.etcd.io/bbolt"

	"github.com/kubebolt/kubebolt/apps/api/internal/agent/channel"
)

// newAgentStore is the OSS factory for the persistent agent registry: the
// BoltDB store. The Enterprise build (`-tags ee`) overrides this to return a
// Postgres-backed store when KUBEBOLT_DB_DSN is set, so a multi-replica Cloud
// deployment shares one agents table (cross-pod visibility of connected
// clusters). Keeping the seam here means main.go stays identical OSS↔EE.
func newAgentStore(db *bolt.DB, bucket []byte) channel.AgentStore {
	return channel.NewBoltAgentStore(db, bucket)
}
