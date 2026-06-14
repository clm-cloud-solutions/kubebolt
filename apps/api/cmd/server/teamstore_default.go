//go:build !ee

package main

import (
	bolt "go.etcd.io/bbolt"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
)

// newTeamStore is the OSS (community) factory for the team/membership store:
// the BoltDB *BoltTeamStore (it satisfies auth.TeamStore). The Enterprise build
// (`-tags ee`) overrides this to return a Postgres-backed store when
// KUBEBOLT_DB_DSN is set. Keeping the seam here means main.go stays identical
// between OSS and EE.
func newTeamStore(db *bolt.DB) (auth.TeamStore, error) {
	return auth.NewTeamStore(db)
}
