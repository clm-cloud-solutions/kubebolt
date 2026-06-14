//go:build !ee

package main

import "github.com/kubebolt/kubebolt/apps/api/internal/auth"

// newSettingStore is the OSS factory for the global install settings store:
// the BoltDB *auth.Store. The Enterprise build (`-tags ee`) overrides this to
// return a Postgres-backed store when KUBEBOLT_DB_DSN is set, so multi-replica
// Cloud deployments share one settings table (e.g. the JWT secret). Keeping the
// seam here means main.go stays identical between OSS and EE.
func newSettingStore(s *auth.Store) auth.SettingStore {
	return s
}
