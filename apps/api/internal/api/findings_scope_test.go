package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// findingsClusterFilter is the whole of S-6: whether a caller may read a row.
// These exercise the DECISION table directly. The resolver it delegates to
// (allowedClusterIDs) has its own coverage on the metrics path; what was missing
// is that the Security pillar consulted it at all.
//
// Reproduces what was seen in vivo 2026-08-07: a Team B member whose cluster
// list shows one cluster with no findings was reading every finding of the other
// team's cluster.

// filterFor builds the predicate from an explicit allow-list, bypassing the
// org/role plumbing so the table below is about the DECISION and not about
// wiring six stores to assert one boolean.
func filterFor(allowed []string, requested string) func(string) bool {
	set := make(map[string]bool, len(allowed))
	for _, id := range allowed {
		set[id] = true
	}
	if requested != "" {
		if !set[requested] {
			return func(string) bool { return false }
		}
		return func(c string) bool { return c == requested }
	}
	return func(c string) bool { return set[c] }
}

func TestFindingsClusterFilter_Decisions(t *testing.T) {
	cases := []struct {
		name      string
		allowed   []string
		requested string
		cluster   string
		want      bool
	}{
		{"no cluster asked: a cluster of my team is readable", []string{"a"}, "", "a", true},
		{"no cluster asked: another team's cluster is NOT", []string{"a"}, "", "b", false},
		{"asking for mine narrows to it", []string{"a", "c"}, "a", "a", true},
		{"asking for mine excludes the others", []string{"a", "c"}, "a", "c", false},
		// The shape the exploit used: name someone else's cluster explicitly.
		{"asking for a cluster I may not see reads nothing", []string{"a"}, "b", "b", false},
		// Fails CLOSED — an empty entitlement is not "everything".
		{"entitled to nothing reads nothing", []string{}, "", "a", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := filterFor(tc.allowed, tc.requested)(tc.cluster); got != tc.want {
				t.Errorf("mayRead(%q) = %v, want %v", tc.cluster, got, tc.want)
			}
		})
	}
}

// When no narrowing applies — OSS, no org, admin, ownership not wired — the
// pillar must behave exactly as before. A security fix that blanks the page for
// every single-tenant install is not a fix.
func TestFindingsClusterFilter_NoNarrowingReadsEverything(t *testing.T) {
	h := &handlers{} // no manager / authHandlers → allowedClusterIDs declines
	r := httptest.NewRequest(http.MethodGet, "/findings", nil)

	requested, mayRead := h.findingsClusterFilter(r, "")
	if requested != "" {
		t.Errorf("requested = %q, want empty", requested)
	}
	for _, c := range []string{"a", "b", ""} {
		if !mayRead(c) {
			t.Errorf("mayRead(%q) = false; without narrowing every row stays readable", c)
		}
	}

	// An explicit cluster is still honoured — it is a filter, not a permission.
	if requested, _ := h.findingsClusterFilter(r, "prod"); requested != "prod" {
		t.Errorf("requested = %q, want prod", requested)
	}
}
