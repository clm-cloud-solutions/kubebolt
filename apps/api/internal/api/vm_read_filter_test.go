package api

import (
	"net/url"
	"regexp"
	"testing"
)

func regexpQuote(s string) string { return regexp.QuoteMeta(s) }

// ONE extra_filters[] carrying every pin: VM ORs several of them, so a second
// parameter would widen the read. And nothing already in the params survives.
func TestVMReadFilter_OneParameterReplacingAnyOther(t *testing.T) {
	p := url.Values{}
	p.Add("extra_filters[]", `{tenant_id="other"}`)
	p.Add("extra_label", "tenant_id=other")
	vmReadFilter{tenant: "org-a", cluster: "uid-a"}.apply(p)
	if got := p["extra_filters[]"]; len(got) != 1 || got[0] != `{tenant_id="org-a",cluster_id="uid-a"}` {
		t.Fatalf("extra_filters[] = %v", got)
	}
	if _, ok := p["extra_label"]; ok {
		t.Fatal("a stray extra_label survived")
	}
}

func TestVMReadFilter_Shapes(t *testing.T) {
	cases := []struct {
		f    vmReadFilter
		want string
	}{
		{vmReadFilter{}, ""},
		{vmReadFilter{cluster: "uid-a"}, `{cluster_id="uid-a"}`},
		{vmReadFilter{tenant: "t", clusters: []string{"b", "a.x"}, setOn: true}, `{tenant_id="t",cluster_id=~"^(a\\.x|b)$"}`},
		// An authoritative empty set reads nothing, never everything.
		{vmReadFilter{tenant: "t", setOn: true}, `{tenant_id="t",cluster_id=~"^(` + regexp.QuoteMeta(noClusterUIDSentinel) + `)$"}`},
	}
	for _, c := range cases {
		if got := c.f.matcher(); got != c.want {
			t.Errorf("%+v: got %s want %s", c.f, got, c.want)
		}
	}
}
