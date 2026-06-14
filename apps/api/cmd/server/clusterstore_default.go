//go:build !ee

package main

import (
	bolt "go.etcd.io/bbolt"

	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
)

// newClusterStore is the OSS (community) factory for cluster persistence: the
// BoltDB *cluster.Storage (it satisfies cluster.ClusterStore). The Enterprise
// build (`-tags ee`) overrides this to return a Postgres-backed store when
// KUBEBOLT_DB_DSN is set. Keeping the seam here means main.go stays identical
// between OSS and EE.
func newClusterStore(db *bolt.DB, configsBucket, displayBucket, uidBucket []byte) (cluster.ClusterStore, error) {
	return cluster.NewStorage(db, configsBucket, displayBucket, uidBucket), nil
}
