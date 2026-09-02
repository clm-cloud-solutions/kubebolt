//go:build !ee

package main

import (
	"log"

	bolt "go.etcd.io/bbolt"

	"github.com/kubebolt/kubebolt/apps/api/internal/api"
	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/insights"
)

// newInsightPolicyService (OSS build) wires #44 step 1 on BoltDB: the
// rule_policies store, the snapshot cache, and the engine's policy source, so
// every evaluation resolves the operator's thresholds and severities instead
// of the shipped defaults. envFor resolves a cluster's environment category
// for the per-environment layers; OSS passes nil (environments are Enterprise
// billing metadata) and every cluster evaluates with the global layer. The
// Enterprise build (-tags ee) replaces this with the Postgres store.
func newInsightPolicyService(db *bolt.DB, envFor func(tenant, cluster string) string) *api.InsightPolicyService {
	if db == nil {
		return nil
	}
	store := insights.NewBoltPolicyStore(db, auth.RulePoliciesBucket())
	cache := insights.NewPolicyCache(store, envFor)
	insights.SetPolicySource(cache.SnapshotFor)
	log.Printf("insights: rule-policy source installed (BoltDB)")
	return &api.InsightPolicyService{Store: store, Invalidate: cache.Invalidate}
}
