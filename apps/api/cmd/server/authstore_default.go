//go:build !ee

package main

import "github.com/kubebolt/kubebolt/apps/api/internal/auth"

// newAuthStore is the OSS (community) factory for the user/refresh-token store:
// the BoltDB *auth.Store itself (it satisfies auth.AuthStore), behavior
// unchanged. The Enterprise build (`-tags ee`) overrides this to return a
// Postgres-backed AuthStore when KUBEBOLT_DB_DSN is set. Keeping the seam here
// means main.go stays identical between OSS and EE.
func newAuthStore(s *auth.Store) auth.AuthStore {
	return s
}
