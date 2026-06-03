package api

import "testing"

// rewriteUpstreamRedirect must keep a backend's redirect under the /pf/{id}
// prefix. The regression that motivated it: an nginx backend with
// absolute_redirect on emitted `Location: http://127.0.0.1/login` (port
// omitted), and the old exact host:port compare left it unrewritten, sending
// the browser to 127.0.0.1.
func TestRewriteUpstreamRedirect(t *testing.T) {
	const prefix = "/pf/abc123"
	const upstream = "127.0.0.1" // target.Hostname() — no port

	cases := []struct {
		name string
		loc  string
		want string
	}{
		{"relative path", "/login", "/pf/abc123/login"},
		{"relative with query", "/login?next=/d", "/pf/abc123/login?next=/d"},
		// The regression: absolute redirect to loopback WITHOUT the dial port.
		{"absolute loopback, no port", "http://127.0.0.1/login", "/pf/abc123/login"},
		// And WITH the dial port — both must be caught (hostname compare).
		{"absolute loopback, with port", "http://127.0.0.1:33309/login", "/pf/abc123/login"},
		// External host: the proxy can't know it maps back in — leave it.
		{"external host", "https://grafana.example.com/login", ""},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rewriteUpstreamRedirect(tc.loc, prefix, upstream); got != tc.want {
				t.Errorf("rewriteUpstreamRedirect(%q) = %q, want %q", tc.loc, got, tc.want)
			}
		})
	}
}
