package auth

import (
	"context"
	"reflect"
	"testing"
)

// The cluster list survives the round trip through the store, reaches the
// token a request authenticates with, and can be widened back to "every
// cluster" by clearing it.
func TestAPIToken_SetClustersRoundTrip(t *testing.T) {
	s := newTestAPIStore(t)
	ctx := context.Background()
	plain, tok, err := s.Issue(ctx, TokenTypeAPIKey, RoleViewer, []string{ScopeMCP}, "mcp", "admin", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetClusters(ctx, tok.ID, []string{"uid-a", "", "uid-b", "uid-a"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Lookup(ctx, plain)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"uid-a", "uid-b"}; !reflect.DeepEqual(got.Clusters, want) {
		t.Fatalf("clusters = %v, want %v (blanks and duplicates dropped)", got.Clusters, want)
	}
	if err := s.SetClusters(ctx, tok.ID, nil); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Lookup(ctx, plain)
	if len(got.Clusters) != 0 {
		t.Fatalf("cleared list still narrows: %v", got.Clusters)
	}
	if err := s.SetClusters(ctx, "no-such-id", []string{"uid-a"}); err != ErrTokenNotFound {
		t.Fatalf("unknown id: err = %v, want ErrTokenNotFound", err)
	}
}
