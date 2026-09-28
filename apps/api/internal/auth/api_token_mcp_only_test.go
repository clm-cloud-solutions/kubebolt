package auth

import "testing"

// A token scoped only to the MCP endpoint is stored as viewer; any other scope
// in the set (or none) leaves the requested role alone.
func TestIsMCPOnly(t *testing.T) {
	cases := []struct {
		scopes []string
		want   bool
	}{
		{[]string{"/api/v1/mcp"}, true},
		{[]string{"/api/v1/mcp", "/api/v1/mcp"}, true},
		{[]string{"/api/v1/mcp", "/api/v1/resources"}, false},
		{[]string{ScopeAll}, false},
		{nil, false},
	}
	for _, c := range cases {
		if got := isMCPOnly(c.scopes); got != c.want {
			t.Errorf("isMCPOnly(%v) = %v, want %v", c.scopes, got, c.want)
		}
	}
}

func TestAPITokenForbiddenPath(t *testing.T) {
	svc := &APIPrincipal{Type: TokenTypeService}
	open := &APIPrincipal{Type: TokenTypeAPIKey}
	narrow := &APIPrincipal{Type: TokenTypeAPIKey, Clusters: []string{"uid-a"}}
	cases := []struct {
		p      *APIPrincipal
		method string
		path   string
		denied bool
	}{
		{svc, "POST", "/api/v1/admin/api-tokens", true},
		{open, "PATCH", "/api/v1/admin/api-tokens/x/clusters", true},
		{open, "GET", "/api/v1/admin/api-tokens", true},
		{open, "GET", "/api/v1/admin/actions", false},
		{narrow, "GET", "/api/v1/admin/actions", true},
		{narrow, "GET", "/api/v1/admin", true},
		{narrow, "GET", "/api/v1/account/usage", true},
		{narrow, "GET", "/api/v1/users", true},
		{narrow, "POST", "/api/v1/clusters/switch", true},
		{narrow, "PUT", "/api/v1/clusters/uid-b/team", true},
		{narrow, "GET", "/api/v1/clusters", false},
		{narrow, "POST", "/api/v1/mcp", false},
		{narrow, "GET", "/api/v1/administrators", false}, // prefix on a segment boundary only
	}
	for _, c := range cases {
		if got := apiTokenForbiddenPath(c.p, c.method, c.path) != ""; got != c.denied {
			t.Errorf("%s %s (%v clusters=%v): denied=%v, want %v", c.method, c.path, c.p.Type, c.p.Clusters, got, c.denied)
		}
	}
}
