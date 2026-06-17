//go:build !ee

package main

import "github.com/kubebolt/kubebolt/apps/api/internal/auth"

// newOrgSettingStore is the OSS factory for the PER-ORG settings store: the
// BoltDB *auth.Store (single default tenant → one row per key). The Enterprise
// build (`-tags ee`) overrides this to return a Postgres-backed RLS store when
// KUBEBOLT_DB_DSN is set, so a second org's settings are invisible at the engine
// level. Keeping the seam here means main.go stays identical between OSS and EE.
func newOrgSettingStore(s *auth.Store) auth.OrgSettingStore {
	return s
}
