package cluster

import (
	"context"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// The initial informer LIST defaults to resourceVersion=0, which the API server
// serves from its watch cache — ignoring limit and shipping the whole collection
// in one body. The tweak blanks it so the server pages. It runs on the
// reflector's List AND Watch, so it must rewrite only the initial LIST page
// (rv=0 with the pager's limit) and leave everything else as it came: a watch
// rewritten to rv="" would start from "now" and miss events, and the
// expired-continue fallback (rv=0, no limit) rewritten to rv="" would become a
// full read of the collection from storage instead of the watch cache.
func TestListFromServerNotWatchCache_RewritesOnlyTheInitialListPage(t *testing.T) {
	cases := []struct {
		name   string
		in     metav1.ListOptions
		wantRV string
	}{
		{"initial list page: rv=0 with the pager's limit", metav1.ListOptions{ResourceVersion: "0", Limit: 500}, ""},
		{"expired-continue fallback: rv=0 and no limit stays on the watch cache", metav1.ListOptions{ResourceVersion: "0"}, "0"},
		{"watch from the list's version", metav1.ListOptions{ResourceVersion: "12345", Watch: true}, "12345"},
		{"a watch carrying rv=0 is still left alone (no limit)", metav1.ListOptions{ResourceVersion: "0", Watch: true}, "0"},
		{"continuation page or consistent relist: rv empty", metav1.ListOptions{Limit: 500}, ""},
		{"relist from a known version", metav1.ListOptions{ResourceVersion: "12345", Limit: 500}, "12345"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := tc.in
			listFromServerNotWatchCache(&opts)
			if opts.ResourceVersion != tc.wantRV {
				t.Errorf("resourceVersion = %q, want %q", opts.ResourceVersion, tc.wantRV)
			}
			if opts.Limit != tc.in.Limit {
				t.Errorf("limit = %d, want it untouched at %d", opts.Limit, tc.in.Limit)
			}
		})
	}
}

// The wiring: both of the connector's factories are built by newInformerFactory,
// and the first LIST its ReplicaSet informer sends must reach the apiserver
// paginated and without resourceVersion=0. Dropping paginateInitialListOption
// from the helper fails this test.
func TestNewInformerFactory_InitialListIsPaginated(t *testing.T) {
	cs := fake.NewClientset()
	var (
		mu    sync.Mutex
		lists []metav1.ListOptions
	)
	cs.PrependReactor("list", "replicasets", func(a k8stesting.Action) (bool, runtime.Object, error) {
		mu.Lock()
		lists = append(lists, a.(k8stesting.ListActionImpl).ListOptions)
		mu.Unlock()
		return false, nil, nil // let the fake's tracker answer
	})

	f := newInformerFactory(cs)
	f.Apps().V1().ReplicaSets().Informer()
	stop := make(chan struct{})
	defer f.Shutdown()
	defer close(stop)
	f.Start(stop)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for typ, synced := range f.WaitForCacheSync(ctx.Done()) {
		if !synced {
			t.Fatalf("informer %v did not sync", typ)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(lists) == 0 {
		t.Fatal("the ReplicaSet informer sent no LIST")
	}
	if first := lists[0]; first.ResourceVersion != "" || first.Limit != 500 {
		t.Errorf("first LIST = resourceVersion %q, limit %d; want resourceVersion \"\" and limit 500, so the apiserver pages instead of answering whole from its watch cache",
			first.ResourceVersion, first.Limit)
	}
}
