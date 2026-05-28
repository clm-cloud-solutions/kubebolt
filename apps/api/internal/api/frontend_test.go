package api

import "testing"

// TestIsReservedAPIPath is the regression test for the security fix
// applied 2026-05-28 session 11-A v3. Before the fix, MountFrontend's
// catch-all served the SPA shell for any URL — including paths under
// /api/ that didn't match a more specific handler. A scanner hitting
// `/api/.env` got HTTP 200 + ~580 bytes of HTML, which is misleading
// signal (and bypasses auth namespacing). The fix added
// isReservedAPIPath; this test pins its contract.
func TestIsReservedAPIPath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		// Exact match of each reserved prefix should match.
		{"/api", true},
		{"/ws", true},
		{"/pf", true},
		{"/health", true},
		{"/metrics", true},
		// Sub-paths of reserved prefixes should match. The .env / config.env
		// variants are real scanner probes seen in production logs during
		// session 11-A v3 (timestamps 23:56:57, 23:57:34, 23:57:34 within
		// 37s — consistent with an automated reconnaissance tool).
		{"/api/.env", true}, // the security probe that triggered the fix
		{"/api/shared/.env", true},
		{"/api/shared/config.env", true},
		{"/api/v1/clusters", true},
		{"/api/admin/users", true},
		{"/api/anything/at/all", true},
		{"/ws/exec/foo/bar", true},
		{"/pf/some-tunnel", true},
		{"/health/sub", true}, // even though /health is a single endpoint, treat sub-paths as reserved
		// SPA routes that LOOK similar to reserved prefixes but aren't
		// (the bug we're explicitly avoiding: don't 404 these).
		{"/applications", false}, // starts with "/a" but not /api/ — must NOT match
		{"/apidocs", false},      // starts with /api but no slash boundary
		{"/wsclient", false},
		{"/pflow-view", false},
		{"/healthcheck", false}, // similar — no slash boundary
		{"/metricsboard", false},
		// Real SPA routes that SHOULD serve the index.html.
		{"/cluster/topology", false},
		{"/admin/agents", false},
		{"/", false},
		{"/login", false},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			if got := isReservedAPIPath(tc.path); got != tc.want {
				t.Errorf("isReservedAPIPath(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}
