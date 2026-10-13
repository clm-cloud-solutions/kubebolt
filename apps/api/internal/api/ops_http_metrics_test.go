package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/kubebolt/kubebolt/apps/api/internal/config"
)

// Every route the router serves must fall in a named group: "other" is the
// bucket for a route added without one, and it hides that route's errors and
// latency among everything else nobody classified.
func TestRouteGroups_EveryRouteIsClassified(t *testing.T) {
	r := NewRouter(nil, nil, nil, config.CopilotConfig{}, nil, nil, nil, nil, nil, nil, "",
		nil, nil, "", nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	n := 0
	_ = chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		n++
		if g := routeGroup(method, route); g == "other" {
			t.Errorf("%s %s has no route group — add it to routeGroup (ops_http_metrics.go)", method, route)
		}
		return nil
	})
	if n < 100 {
		t.Fatalf("walked only %d routes; the guard is not seeing the router", n)
	}
}

func TestRouteGroup(t *testing.T) {
	cases := []struct{ method, pattern, want string }{
		{"GET", "/api/v1/resources/pods/{namespace}/{name}/logs", "pod_logs"},
		{"GET", "/api/v1/resources/{type}/{namespace}/{name}", "resources"}, // a pod named "logs" is still a resource
		{"GET", "/api/v1/resources/pods/{namespace}/{name}/files/content", "pod_files"},
		{"POST", "/api/v1/resources/{type}/{namespace}/{name}/restart", "actions"},
		{"DELETE", "/api/v1/resources/{type}/{namespace}/{name}", "actions"},
		{"POST", "/api/v1/copilot/chat", "copilot_chat"},
		{"POST", "/api/v1/copilot/feedback", "copilot"},
		{"GET", "/api/v1/metrics/query_range", "metrics_query"},
		{"GET", "/api/v1/metrics/{type}/{namespace}/{name}", "cluster_views"},
		{"POST", "/api/v1/mcp/autopilot/{profile}", "mcp"},
		{"GET", "/api/v1/ws/exec/{namespace}/{name}", "terminal"},
		{"GET", "/api/v1/clusters", "clusters"},
		{"GET", "/api/v1/cluster/overview", "cluster_views"},
		{"POST", "/api/v1/prom/write", "ingest"},
		{"GET", "", "unmatched"},
	}
	for _, c := range cases {
		if got := routeGroup(c.method, c.pattern); got != c.want {
			t.Errorf("routeGroup(%s %q) = %q, want %q", c.method, c.pattern, got, c.want)
		}
	}
}

func TestIsStreamPath(t *testing.T) {
	for path, want := range map[string]bool{
		"/api/v1/ws":                      true,
		"/api/v1/ws/exec/default/web-0":   true,
		"/api/v1/pf/abc/index.html":       true,
		"/api/v1/copilot/chat":            true,
		"/api/v1/mcp":                     true,
		"/api/v1/copilot/conversations":   false,
		"/api/v1/resources/pods/a/b/logs": false,
	} {
		if got := isStreamPath(path); got != want {
			t.Errorf("isStreamPath(%q) = %v, want %v", path, got, want)
		}
	}
}

// The middleware reads the group from the matched route and still counts a
// panic as the 500 Recoverer answers.
func TestOpsHTTPMetrics_CountsByMatchedRoute(t *testing.T) {
	r := chi.NewRouter()
	r.Use(opsHTTPMetrics)
	r.Use(func(next http.Handler) http.Handler { // Recoverer stand-in
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			defer func() {
				if recover() != nil {
					w.WriteHeader(http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, req)
		})
	})
	r.Get("/api/v1/resources/{type}/{namespace}/{name}", func(w http.ResponseWriter, _ *http.Request) {})
	r.Post("/api/v1/resources/{type}/{namespace}/{name}/restart", func(http.ResponseWriter, *http.Request) { panic("boom") })

	count := func(group, code string) float64 {
		return gatheredCounter(t, "kubebolt_http_requests_total", map[string]string{"group": group, "code": code})
	}
	okBefore, failBefore := count("resources", "2xx"), count("actions", "5xx")
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/v1/resources/pods/default/logs", nil))
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/v1/resources/deployments/default/web/restart", nil))
	if count("resources", "2xx") != okBefore+1 {
		t.Error("GET of a pod named logs was not counted under resources/2xx")
	}
	if count("actions", "5xx") != failBefore+1 {
		t.Error("a panicking action was not counted as actions/5xx")
	}
}

// gatheredCounter reads one counter from the default registry, where the
// process registers the ops series.
func gatheredCounter(t *testing.T, name string, labels map[string]string) float64 {
	t.Helper()
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
	next:
		for _, m := range mf.GetMetric() {
			have := map[string]string{}
			for _, lp := range m.GetLabel() {
				have[lp.GetName()] = lp.GetValue()
			}
			for k, v := range labels {
				if have[k] != v {
					continue next
				}
			}
			return m.GetCounter().GetValue()
		}
	}
	return 0
}
