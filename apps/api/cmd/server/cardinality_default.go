//go:build !ee

package main

import (
	"context"

	"github.com/kubebolt/kubebolt/apps/api/internal/usage"
)

// startCardinalityCollector is a no-op in this edition: per-org active-series
// cardinality is a multi-tenant metering dimension with no meaning in
// single-tenant.
func startCardinalityCollector(_ context.Context, _ usage.UsageStore, _ string) {}
