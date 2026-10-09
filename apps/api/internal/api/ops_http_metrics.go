package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"

	"github.com/kubebolt/kubebolt/apps/api/internal/opsmetrics"
)

// opsHTTPMetrics counts and times every request by route group (doc #67, O2).
// It sits outermost, ahead of Recoverer, so a handler panic still counts as
// the 500 Recoverer answers. The group comes from the route chi matched — the
// pattern, never the path, so a pod named "logs" is not its own logs and the
// label set stays closed.
func opsHTTPMetrics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stream := isStreamPath(r.URL.Path)
		done := opsmetrics.HTTPStarted(stream)
		defer done()
		start := time.Now()
		ww := chimiddleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		pattern := ""
		if rctx := chi.RouteContext(r.Context()); rctx != nil {
			pattern = rctx.RoutePattern()
		}
		opsmetrics.ObserveHTTP(routeGroup(r.Method, pattern), ww.Status(), time.Since(start), stream)
	})
}

// isStreamPath reports the long-lived requests: the live-update and terminal
// WebSockets, port-forward traffic, Kobi's SSE chat and MCP. Their duration is
// how long someone kept them open, not how fast the API answered.
func isStreamPath(path string) bool {
	p := strings.TrimPrefix(path, "/api/v1")
	return p == "/ws" || strings.HasPrefix(p, "/ws/") || strings.HasPrefix(p, "/pf/") ||
		p == "/copilot/chat" || p == "/mcp" || strings.HasPrefix(p, "/mcp/")
}

// routeGroup folds a matched route into one of ~25 groups. A route nobody
// classified lands in "other" (TestRouteGroups_EveryRouteIsClassified fails on
// it); a request no route matched is "unmatched".
func routeGroup(method, pattern string) string {
	if pattern == "" {
		return "unmatched"
	}
	p := strings.TrimPrefix(pattern, "/api/v1")
	has := func(prefixes ...string) bool {
		for _, pre := range prefixes {
			if p == pre || strings.HasPrefix(p, pre+"/") {
				return true
			}
		}
		return false
	}
	switch {
	case p == "/health" || pattern == "/metrics":
		return "health"
	case p == "/ws":
		return "ws"
	case has("/ws/exec", "/pf", "/portforward"):
		return "terminal"
	case strings.HasPrefix(p, "/resources/pods/") && strings.HasSuffix(p, "/logs"):
		return "pod_logs"
	case strings.HasPrefix(p, "/resources/pods/") && strings.Contains(p, "/files"):
		return "pod_files"
	case has("/resources"):
		if method == http.MethodGet {
			return "resources"
		}
		return "actions"
	case p == "/copilot/chat":
		return "copilot_chat"
	case has("/copilot", "/admin/copilot"):
		return "copilot"
	case has("/mcp"):
		return "mcp"
	case has("/autopilot", "/internal/autopilot"):
		return "autopilot"
	case has("/metrics/query", "/metrics/query_range", "/admin/metrics"):
		return "metrics_query"
	case has("/platform"):
		return "platform"
	case has("/insights", "/admin/insight-policies"):
		return "insights"
	case has("/findings", "/runtime-events"):
		return "security"
	case has("/prom", "/ingest"):
		return "ingest"
	case has("/internal"):
		return "internal"
	case has("/clusters"):
		return "clusters"
	case has("/cluster", "/topology", "/events", "/search", "/deploys", "/right-sizing", "/coverage",
		"/flows", "/helm", "/metrics"):
		return "cluster_views"
	case has("/auth"):
		return "auth"
	case has("/account", "/billing"):
		return "account"
	case has("/admin", "/users", "/teams", "/orgs", "/integrations", "/notifications"):
		return "admin"
	case has("/config", "/update-check", "/onboarding"):
		return "app"
	default:
		return "other"
	}
}
