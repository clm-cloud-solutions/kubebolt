//go:build !ee

package main

import "github.com/kubebolt/kubebolt/apps/api/internal/usage"

// newUsageStore is the OSS (community) factory for the metering seam: the
// no-op store (OSS is a free, unmetered single-tenant install). The Enterprise
// build (`-tags ee`) overrides this to return a Postgres-backed store when
// KUBEBOLT_DB_DSN is set. Keeping the seam here means main.go stays identical
// between OSS and EE.
func newUsageStore() usage.UsageStore {
	return usage.NewNoopUsageStore()
}
