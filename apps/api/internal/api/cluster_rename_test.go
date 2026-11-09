package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/kubebolt/kubebolt/apps/api/internal/agent/channel"
)

// Finding #72: handleRenameCluster must URL-decode the {context} path param. The
// browser sends an agent:<uid> context as agent%3A<uid> (encodeURIComponent on
// the ':'), and chi does not unescape path params — so without the decode,
// SetClusterDisplayName is handed "agent%3A..." and reports "not found". (EE
// fixed this in 2026-07; OSS was never homologated.)
func TestHandleRenameCluster_DecodesEncodedAgentContext(t *testing.T) {
	m := newIdleManager(t)
	m.SetAgentRegistry(channel.NewAgentRegistry())
	const uid = "uid-x"
	if _, err := m.AddAgentProxyCluster(uid, ""); err != nil { // registers context "agent:uid-x"
		t.Fatalf("AddAgentProxyCluster: %v", err)
	}
	h := &handlers{manager: m}

	// chi route param set to the PERCENT-ENCODED context, exactly as the browser
	// puts it in the URL path.
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("context", "agent%3A"+uid)
	req := httptest.NewRequest(http.MethodPut, "/clusters/x/rename",
		strings.NewReader(`{"displayName":"Prod EU"}`))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	h.handleRenameCluster(rec, req)

	// The fix decodes agent%3Auid-x → agent:uid-x, a registered context, so the
	// handler gets PAST the "context not found" check (this bare manager then
	// fails only because it has no storage wired). Without the decode the lookup
	// uses the still-encoded name and returns "context ... not found" — the exact
	// error finding #72 reproduced.
	if strings.Contains(rec.Body.String(), "not found") {
		t.Fatalf("rename did not decode the agent context (body: %q) — finding #72", rec.Body.String())
	}
}
