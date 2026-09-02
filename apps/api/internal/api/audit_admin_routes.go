package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/kubebolt/kubebolt/apps/api/internal/audit"
	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
)

// auditAdminRoutes audits every MUTATING request under the subtree it wraps.
//
// The administrative surface is wide — sixteen settings endpoints alone — and it
// grows. A per-handler audit call is something a new endpoint can simply be
// written without, and nothing fails when it is: the endpoint works, the trail
// just quietly has a hole. Wrapping the subtree inverts that, so the seventeenth
// settings endpoint is audited the day it is added, by nobody remembering
// anything.
//
// The trade is detail. A middleware knows the route and the outcome, not which
// field changed. For THIS surface that bound is not just acceptable, it is what
// we want: these bodies carry SMTP passwords, AI provider keys and OAuth
// secrets, and a trail that copied them would become the longest-lived and
// least-guarded copy of every credential in the install. Cluster mutations,
// where the payload is safe and the detail is the point, keep their explicit
// per-handler auditMutation calls.
//
// clusterScoped decides whether the active cluster is stamped. Installing an
// integration happens ON a cluster; changing the SMTP settings does not, and
// stamping whichever cluster the operator had selected would invent an
// association that reads as evidence later.
func (h *handlers) auditAdminRoutes(targetType string, clusterScoped bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
			default:
				next.ServeHTTP(w, r)
				return
			}

			ww := chimiddleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)

			// Recorded after the handler so the outcome is real rather than
			// intended: a rejected change must not read like an applied one.
			status := ww.Status()
			if status == 0 {
				status = http.StatusOK // handler wrote nothing; chi defaults to 200
			}
			var err error
			if status >= 400 {
				err = fmt.Errorf("http %d", status)
			}
			if !audit.Enabled() {
				return
			}

			pattern := "" // e.g. /admin/settings/copilot
			if rctx := chi.RouteContext(r.Context()); rctx != nil {
				pattern = rctx.RoutePattern()
			}
			source := r.Header.Get("X-KubeBolt-Action-Source")
			if source == "" {
				source = "ui"
			}
			var userID, username string
			if claims := auth.ContextClaims(r); claims != nil {
				userID = claims.UserID
				username = claims.Username
			}
			result := "success"
			if err != nil {
				result = "error"
			}
			clusterID := ""
			if clusterScoped {
				clusterID = audit.ClusterID()
			}

			rec := &audit.Record{
				ID:         uuid.New().String(),
				Timestamp:  time.Now().UTC(),
				TenantID:   auth.ContextTenantID(r),
				Source:     source,
				UserID:     userID,
				Username:   username,
				Role:       string(auth.ContextRole(r)),
				ClusterID:  clusterID,
				Action:     targetType + "_" + adminVerb(r.Method),
				TargetType: targetType,
				TargetName: adminTargetName(r, pattern),
				Params:     map[string]any{"path": pattern, "status": status},
				Result:     result,
			}
			if err != nil {
				rec.Error = err.Error()
			}
			audit.Emit(rec)
		})
	}
}

// adminVerb maps the HTTP method onto the vocabulary the action-history UI
// already groups by, so an admin row sorts next to a cluster row instead of
// forming its own dialect.
func adminVerb(method string) string {
	switch method {
	case http.MethodDelete:
		return "delete"
	case http.MethodPost:
		return "create"
	default:
		return "update"
	}
}

// adminTargetName names the thing that changed: the route's identifying param
// when it has one, otherwise the last literal path segment, which for the
// settings tree is the section — "copilot", "notifications", "auth".
//
// Several names are tried because the identifying param is not spelled the same
// everywhere: integrations use {id}, cluster routes use {context} or
// {clusterId}. Falling through to the literal segment would name the collection
// ("clusters") instead of the cluster, which is the one thing the record exists
// to say.
func adminTargetName(r *http.Request, pattern string) string {
	for _, key := range []string{"id", "context", "clusterId", "name"} {
		if v := chi.URLParam(r, key); v != "" {
			return v
		}
	}
	segs := strings.Split(strings.Trim(pattern, "/"), "/")
	for i := len(segs) - 1; i >= 0; i-- {
		if s := segs[i]; s != "" && !strings.HasPrefix(s, "{") {
			return s
		}
	}
	return pattern
}
