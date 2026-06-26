//go:build !ee

package main

import "github.com/kubebolt/kubebolt/apps/api/internal/auth"

// openBoltStore opens the BoltDB-backed auth store. OSS always uses BoltDB, so
// this always opens it. The Enterprise build (`-tags ee`) overrides this to
// SKIP opening BoltDB entirely when KUBEBOLT_DB_DSN is set (returns nil), so a
// fully Postgres-backed EE never creates or locks a kubebolt.db file.
func openBoltStore(dataDir string) (*auth.Store, error) {
	return auth.NewStore(dataDir)
}
