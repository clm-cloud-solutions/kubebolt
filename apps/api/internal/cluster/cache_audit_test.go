package cluster

import (
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The cache audit must ask for exactly one item and must NOT pin
// resourceVersion=0. With rv=0 the apiserver serves from its watch cache,
// ignores Limit and ships the whole list with no RemainingItemCount — so the
// "count one" audit silently downloads tens of MB per type every 5 minutes
// through the agent, enough to exhaust its memory on a large cluster.
func TestCountOnly_AsksForOneItemWithoutResourceVersionZero(t *testing.T) {
	o := countOnly()
	if o.Limit != 1 {
		t.Errorf("Limit = %d, want 1 (the audit must not download a full list)", o.Limit)
	}
	if o.ResourceVersion != "" {
		t.Errorf("ResourceVersion = %q, want empty: rv=0 makes the apiserver ignore Limit and return every object", o.ResourceVersion)
	}
}

// With limit=1 the size is len(Items)+RemainingItemCount. When the apiserver
// paginates (a continue token) but omits RemainingItemCount — a label or field
// selector, or a storage backend that does not compute it — the size is
// unknown: reporting len(Items)=1 would log a false divergence of thousands
// every 5 minutes, so the count must come back as unavailable.
func TestListCount(t *testing.T) {
	remaining := func(n int64) *int64 { return &n }
	cases := []struct {
		name    string
		items   int
		meta    metav1.ListMeta
		want    int
		wantErr bool
	}{
		{"paginated, remainingItemCount reported", 1, metav1.ListMeta{Continue: "tok", RemainingItemCount: remaining(5502)}, 5503, false},
		{"the whole collection fit in one item", 1, metav1.ListMeta{}, 1, false},
		{"empty collection", 0, metav1.ListMeta{}, 0, false},
		{"paginated without remainingItemCount", 1, metav1.ListMeta{Continue: "tok"}, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := listCount(tc.items, tc.meta)
			if tc.wantErr {
				if !errors.Is(err, errCountUnavailable) {
					t.Fatalf("listCount = (%d, %v), want errCountUnavailable", got, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("listCount = (%d, %v), want %d", got, err, tc.want)
			}
		})
	}
}
