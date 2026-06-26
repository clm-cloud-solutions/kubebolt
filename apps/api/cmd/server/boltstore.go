package main

import (
	bolt "go.etcd.io/bbolt"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
)

// boltHandle returns the underlying *bolt.DB, or nil when the BoltDB store was
// not opened (EE running fully on Postgres — see openBoltStore). The store
// factories accept this handle but ignore it when KUBEBOLT_DB_DSN is set, so
// passing nil is safe.
func boltHandle(s *auth.Store) *bolt.DB {
	if s == nil {
		return nil
	}
	return s.DB()
}
