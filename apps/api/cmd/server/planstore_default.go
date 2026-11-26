//go:build !ee

package main

import (
	bolt "go.etcd.io/bbolt"

	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
)

func newPlanStore(db *bolt.DB, plansBucket, versionsBucket []byte) (copilot.PlanStore, error) {
	return copilot.NewBoltPlanStore(db, plansBucket, versionsBucket)
}
