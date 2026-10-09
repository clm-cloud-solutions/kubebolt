//go:build !ee

package main

import (
	bolt "go.etcd.io/bbolt"

	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
)

func newFeedbackStore(db *bolt.DB, bucket []byte) (copilot.FeedbackStore, error) {
	return copilot.NewBoltFeedbackStore(db, bucket)
}
