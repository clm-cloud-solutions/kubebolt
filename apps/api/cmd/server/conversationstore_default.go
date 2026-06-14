//go:build !ee

package main

import (
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
)

func newConversationStore(db *bolt.DB, bucket []byte, retention time.Duration, maxPerUser int) copilot.ConversationStore {
	return copilot.NewBoltConversationStore(db, bucket, retention, maxPerUser)
}
