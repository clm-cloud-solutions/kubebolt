package integrations

import (
	"strings"
	"testing"
)

// The catalog's "Learn more" links point at kubebolt.io,
// with the trailing slash the site serves without a redirect. The two
// Prometheus cards keep the repository guide until the site carries what it
// does; nothing else may point at GitHub.
func TestCatalogDocsLinksPointAtTheSite(t *testing.T) {
	providers := []Provider{
		NewAgent(nil, nil),
		NewTrivy(),
		NewKyverno(),
		NewFalco(),
		NewOpenCost(nil, nil),
		NewPrometheus(nil, nil, nil, nil),
		NewPrometheusRead(nil, nil),
	}
	for _, p := range providers {
		meta := p.Meta()
		u := meta.DocsURL
		switch {
		case u == prometheusGuide:
			if !strings.HasPrefix(meta.ID, "prometheus") {
				t.Errorf("%s: links the repository's Prometheus guide", meta.ID)
			}
		case strings.HasPrefix(u, siteDocs):
			path := strings.TrimPrefix(u, siteDocs)
			if i := strings.Index(path, "#"); i >= 0 {
				path = path[:i]
			}
			if !strings.HasSuffix(path, "/") || strings.Count(path, "/") != 1 {
				t.Errorf("%s: %q is not /docs/<slug>/", meta.ID, u)
			}
		default:
			t.Errorf("%s: docs link %q is neither the site nor the Prometheus guide", meta.ID, u)
		}
	}
}
