package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
)

// An ingest-token store that does not answer must not refuse the batch as if
// its token were bad. Prometheus drops what a 401 refuses and retries a 5xx,
// so a Postgres restart used to lose remote_write samples — and in permissive
// mode it filed them under the default org instead.

var errIngestStoreDown = errors.New("dial tcp 10.0.0.5:5432: connect: connection refused")

// ingestTokensDown wraps a working store and fails every lookup.
type ingestTokensDown struct{ auth.IngestTokenStore }

func (ingestTokensDown) Lookup(context.Context, string) (*auth.IngestToken, error) {
	return nil, errIngestStoreDown
}

func TestAuthenticatePromWrite_StoreDownIs503InEveryMode(t *testing.T) {
	for _, mode := range []string{promWriteAuthPermissive, promWriteAuthEnforced} {
		store, its, _ := newTenantsStoreWithToken(t)
		h := &handlers{tenantsStore: store, ingestTokens: ingestTokensDown{its}, promWriteAuthMode: mode}
		req := httptest.NewRequest(http.MethodPost, "/prom/write", strings.NewReader(""))
		req.Header.Set("Authorization", "Bearer kbtok_v1_some-token")
		rec := httptest.NewRecorder()
		tenant, ok := h.authenticatePromWrite(rec, req)
		if ok || tenant != nil {
			t.Errorf("mode %v: store down must not admit the batch (tenant %v) — permissive would file it under the default org", mode, tenant)
		}
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("mode %v: status %d, want 503 so Prometheus retries", mode, rec.Code)
		}
		if rec.Header().Get("Retry-After") == "" {
			t.Errorf("mode %v: no Retry-After", mode)
		}
	}
}

func TestFalcoIngest_StoreDownIs503(t *testing.T) {
	h := &handlers{
		eventStore:   &memEvents{},
		ingestTokens: ingestTokensDown{&falcoTokens{}},
		tenantsStore: &fakeTenantStore{tenant: &auth.Tenant{ID: "org-a"}},
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/ingest/falco", strings.NewReader(falcoBody))
	r.Header.Set("Authorization", "Bearer kb_whatever")
	rr := httptest.NewRecorder()
	h.handleFalcoIngest(rr, r)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status %d, want 503 (body %s)", rr.Code, rr.Body.String())
	}
}
