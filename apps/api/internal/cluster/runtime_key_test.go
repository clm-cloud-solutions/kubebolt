package cluster

import (
	"context"
	"testing"
)

func TestRuntimeKeyFromContext_AbsentIsZero(t *testing.T) {
	got := RuntimeKeyFromContext(context.Background())
	if got.Tenant != "" || got.Cluster != "" {
		t.Fatalf("absent key = %+v, want zero value", got)
	}
}

func TestRuntimeKey_RoundTrip(t *testing.T) {
	key := RuntimeKey{Tenant: "acme", Cluster: "prod-eks"}
	ctx := WithRuntimeKey(context.Background(), key)
	got := RuntimeKeyFromContext(ctx)
	if got != key {
		t.Fatalf("round-trip = %+v, want %+v", got, key)
	}
}

func TestRuntimeKey_OSSDefaultShape(t *testing.T) {
	// OSS resolves tenant="default" and an empty cluster (→ active context).
	ctx := WithRuntimeKey(context.Background(), RuntimeKey{Tenant: "default"})
	got := RuntimeKeyFromContext(ctx)
	if got.Tenant != "default" || got.Cluster != "" {
		t.Fatalf("OSS-shape key = %+v", got)
	}
}

func TestResolveRuntime_NilSafe(t *testing.T) {
	m := &Manager{} // zero value: no active connector, nil pool

	// Empty key → active branch; no connector → nil (and getters → nil).
	if rt := m.resolveRuntime(context.Background()); rt != nil {
		t.Fatalf("empty key, no connector = %+v, want nil", rt)
	}
	if c := m.Connector(context.Background()); c != nil {
		t.Fatalf("Connector with no active runtime = %v, want nil", c)
	}

	// A non-active, unknown cluster → getOrSpinPooled returns nil (access
	// can't be resolved) and must NOT leave a placeholder in the pool, so a
	// later request retries cleanly.
	ctx := WithRuntimeKey(context.Background(), RuntimeKey{Tenant: "default", Cluster: "other"})
	if rt := m.resolveRuntime(ctx); rt != nil {
		t.Fatalf("non-active unknown cluster = %+v, want nil", rt)
	}
	if n := len(m.runtimes); n != 0 {
		t.Fatalf("pool has %d entries after a failed spin, want 0 (no leaked placeholder)", n)
	}
}
