//go:build !ee

package main

import (
	bolt "go.etcd.io/bbolt"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
)

// newAPITokenStore is the OSS (community) factory for the long-lived REST
// API-token store (kbs_ service tokens + kbk_ customer keys): the BoltDB
// *APITokenStore (it satisfies auth.APITokenStorer), behavior unchanged. The
// Enterprise build (`-tags ee`) overrides this to return a Postgres-backed
// store when KUBEBOLT_DB_DSN is set. Keeping the seam here means main.go stays
// identical between OSS and EE.
func newAPITokenStore(db *bolt.DB) (auth.APITokenStorer, error) {
	return auth.NewAPITokenStore(db)
}
