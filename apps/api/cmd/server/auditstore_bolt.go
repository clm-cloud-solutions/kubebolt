package main

import (
	"log"

	bolt "go.etcd.io/bbolt"

	"github.com/kubebolt/kubebolt/apps/api/internal/audit"
)

// boltAuditStore opens the Bolt audit trail and drops the literal env values
// that set_env records carried before they were scrubbed at write time
// (finding #93). Every boot, idempotent: once clean, it rewrites nothing.
func boltAuditStore(db *bolt.DB, bucket []byte) audit.Store {
	store := audit.NewBoltStore(db, bucket)
	if n, err := store.DropEnvValues(); err != nil {
		log.Printf("audit: dropping env values from older records failed: %v", err)
	} else if n > 0 {
		log.Printf("audit: dropped literal env values from %d older records", n)
	}
	return store
}
