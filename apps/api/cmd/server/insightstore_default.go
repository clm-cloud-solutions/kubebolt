//go:build !ee

package main

import (
	bolt "go.etcd.io/bbolt"

	"github.com/kubebolt/kubebolt/apps/api/internal/insights"
)

// newInsightStore is the OSS (community) factory for the persistent insight
// store: BoltDB, single-tenant default. The Enterprise build (`-tags ee`)
// replaces this with insightstore_ee.go, which returns a Postgres-backed store
// when KUBEBOLT_DB_DSN is set (and falls back to Bolt otherwise). Keeping the
// seam here means main.go stays identical between OSS and EE.
func newInsightStore(db *bolt.DB, bucket []byte) insights.InsightStore {
	return insights.NewBoltInsightStore(db, bucket)
}
