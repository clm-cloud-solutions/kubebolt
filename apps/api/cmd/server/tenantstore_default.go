//go:build !ee

package main

import (
	bolt "go.etcd.io/bbolt"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
)

// newTenantStore is the OSS (community) factory for the tenant (= org) store:
// the BoltDB *TenantsStore (it satisfies auth.TenantStore, auto-seeding the
// "default" tenant). The Enterprise build (`-tags ee`) overrides this to return
// a Postgres-backed store when KUBEBOLT_DB_DSN is set. Keeping the seam here
// means main.go stays identical between OSS and EE.
func newTenantStore(db *bolt.DB) (auth.TenantStore, error) {
	return auth.NewTenantsStore(db)
}
