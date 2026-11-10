package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/kubebolt/kubebolt/apps/api/internal/agent/channel"
)

// Finding #73: handleRenameCluster must URL-decode the {context} path param. The
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

	// Through a real chi router, with the PERCENT-ENCODED context exactly as the
	// browser puts it in the URL path: the request carries a RawPath, chi matches
	// it and hands the segment over still encoded.
	router := chi.NewRouter()
	router.Put("/clusters/{context}/rename", h.handleRenameCluster)
	req := httptest.NewRequest(http.MethodPut, "/clusters/agent%3A"+uid+"/rename",
		strings.NewReader(`{"displayName":"Prod EU"}`))
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	// The fix decodes agent%3Auid-x → agent:uid-x, a registered context, so the
	// handler gets PAST the "context not found" check (this bare manager then
	// fails only because it has no storage wired). Without the decode the lookup
	// uses the still-encoded name and returns "context ... not found" — the exact
	// error finding #73 reproduced.
	if strings.Contains(rec.Body.String(), "not found") {
		t.Fatalf("rename did not decode the agent context (body: %q) — finding #73", rec.Body.String())
	}
}
