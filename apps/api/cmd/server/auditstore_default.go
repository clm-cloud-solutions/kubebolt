//go:build !ee

package main

import (
	bolt "go.etcd.io/bbolt"

	"github.com/kubebolt/kubebolt/apps/api/internal/audit"
)

func newAuditStore(db *bolt.DB, bucket []byte) audit.Store {
	return audit.NewBoltStore(db, bucket)
}
