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
