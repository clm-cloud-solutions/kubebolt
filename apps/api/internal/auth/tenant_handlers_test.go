package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"
)

// mockInvalidator counts InvalidateCache invocations so tests can pin
// the contract: every mutating endpoint must trigger a cache flush.
type mockInvalidator struct{ calls atomic.Int32 }

func (m *mockInvalidator) InvalidateCache() { m.calls.Add(1) }

func newTestHandlers(t *testing.T) (*TenantHandlers, *mockInvalidator) {
	t.Helper()
	store := newTestStore(t)
	ts, err := NewTenantsStore(store.DB())
	if err != nil {
		t.Fatalf("NewTenantsStore: %v", err)
	}
	inv := &mockInvalidator{}
	return NewTenantHandlers(ts, inv), inv
}

// withAdminUser fakes a chi-routed request as an authenticated admin.
// Routes use chi.URLParam, so we route through a real chi mux instead
// of httptest.NewRequest + chi.RouteContext directly.
func mountAdmin(t *testing.T, h *TenantHandlers) http.Handler {
	t.Helper()
	r := chi.NewRouter()
	r.Route("/admin/tenants", func(r chi.Router) {
		// Inject a synthetic admin user_id into the context so the
		// IssueToken handler can stamp CreatedBy.
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				ctx := context.WithValue(req.Context(), claimsKey, &Claims{
					UserID:   "test-admin",
					Username: "admin",
					Role:     RoleAdmin,
				})
				next.ServeHTTP(w, req.WithContext(ctx))
			})
		})
		h.RegisterRoutes(r)
	})
	return r
}

func decodeJSON(t *testing.T, body []byte, into any) {
	t.Helper()
	if err := json.Unmarshal(body, into); err != nil {
		t.Fatalf("decode JSON %q: %v", body, err)
	}
}

// ─── List + create ────────────────────────────────────────────────────

func TestHandlers_ListTenants_IncludesDefaultSeed(t *testing.T) {
	h, _ := newTestHandlers(t)
	srv := mountAdmin(t, h)

	req := httptest.NewRequest(http.MethodGet, "/admin/tenants", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body)
	}
	var got []tenantResponse
	decodeJSON(t, rr.Body.Bytes(), &got)
	if len(got) != 1 || got[0].Name != DefaultTenantName {
		t.Errorf("expected exactly the default tenant, got %+v", got)
	}
}

func TestHandlers_CreateTenant_Conflict(t *testing.T) {
	h, _ := newTestHandlers(t)
	srv := mountAdmin(t, h)

	body := []byte(`{"name":"acme","plan":"team"}`)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/admin/tenants", bytes.NewReader(body)))
	if rr.Code != http.StatusCreated {
		t.Fatalf("first create status = %d, want 201; body=%s", rr.Code, rr.Body)
	}

	rr2 := httptest.NewRecorder()
	srv.ServeHTTP(rr2, httptest.NewRequest(http.MethodPost, "/admin/tenants", bytes.NewReader(body)))
	if rr2.Code != http.StatusConflict {
		t.Errorf("duplicate create status = %d, want 409; body=%s", rr2.Code, rr2.Body)
	}
}

func TestHandlers_CreateTenant_BadJSON(t *testing.T) {
	h, _ := newTestHandlers(t)
	srv := mountAdmin(t, h)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/admin/tenants", strings.NewReader("not json")))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

// ─── Get / Update ────────────────────────────────────────────────────

func TestHandlers_GetTenant_IncludesTokensWithoutHash(t *testing.T) {
	h, _ := newTestHandlers(t)
	srv := mountAdmin(t, h)

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/admin/tenants",
		strings.NewReader(`{"name":"acme","plan":"team"}`)))
	var created tenantResponse
	decodeJSON(t, rr.Body.Bytes(), &created)

	rr2 := httptest.NewRecorder()
	srv.ServeHTTP(rr2, httptest.NewRequest(http.MethodPost, "/admin/tenants/"+created.ID+"/tokens",
		strings.NewReader(`{"label":"prod-east"}`)))
	if rr2.Code != http.StatusCreated {
		t.Fatalf("issue token status = %d, body=%s", rr2.Code, rr2.Body)
	}

	rr3 := httptest.NewRecorder()
	srv.ServeHTTP(rr3, httptest.NewRequest(http.MethodGet, "/admin/tenants/"+created.ID, nil))
	if rr3.Code != http.StatusOK {
		t.Fatalf("get tenant status = %d", rr3.Code)
	}
	var got tenantWithTokensResponse
	decodeJSON(t, rr3.Body.Bytes(), &got)
	if len(got.IngestTokens) != 1 {
		t.Fatalf("expected 1 token, got %d", len(got.IngestTokens))
	}
	// Hash must NEVER appear in the response payload.
	if strings.Contains(rr3.Body.String(), `"hash"`) {
		t.Errorf("response leaked token hash: %s", rr3.Body)
	}
	// Plaintext also must never appear in any response other than the
	// issue/rotate ones.
	if strings.Contains(rr3.Body.String(), `"token"`) {
		t.Errorf("response leaked plaintext token: %s", rr3.Body)
	}
}

func TestHandlers_UpdateTenant_DisableTriggersInvalidate(t *testing.T) {
	h, inv := newTestHandlers(t)
	srv := mountAdmin(t, h)

	// create
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/admin/tenants",
		strings.NewReader(`{"name":"acme","plan":"team"}`)))
	var t1 tenantResponse
	decodeJSON(t, rr.Body.Bytes(), &t1)

	// disable
	before := inv.calls.Load()
	rr2 := httptest.NewRecorder()
	srv.ServeHTTP(rr2, httptest.NewRequest(http.MethodPut, "/admin/tenants/"+t1.ID,
		strings.NewReader(`{"disabled":true}`)))
	if rr2.Code != http.StatusOK {
		t.Fatalf("update status = %d, body=%s", rr2.Code, rr2.Body)
	}
	if inv.calls.Load() != before+1 {
		t.Errorf("disable must invalidate caches once, calls = %d (was %d)", inv.calls.Load(), before)
	}
}

func TestHandlers_UpdateTenant_NoOpDisableDoesNotInvalidate(t *testing.T) {
	h, inv := newTestHandlers(t)
	srv := mountAdmin(t, h)

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/admin/tenants",
		strings.NewReader(`{"name":"acme"}`)))
	var t1 tenantResponse
	decodeJSON(t, rr.Body.Bytes(), &t1)

	// Update without flipping disabled (just rename).
	before := inv.calls.Load()
	rr2 := httptest.NewRecorder()
	srv.ServeHTTP(rr2, httptest.NewRequest(http.MethodPut, "/admin/tenants/"+t1.ID,
		strings.NewReader(`{"name":"Acme Inc."}`)))
	if rr2.Code != http.StatusOK {
		t.Fatalf("update status = %d, body=%s", rr2.Code, rr2.Body)
	}
	if inv.calls.Load() != before {
		t.Errorf("non-disabling update should not invalidate caches, calls = %d (was %d)", inv.calls.Load(), before)
	}
}

// ─── Tokens ──────────────────────────────────────────────────────────

func TestHandlers_IssueToken_ReturnsPlaintextOnceAndStampsCreatedBy(t *testing.T) {
	h, _ := newTestHandlers(t)
	srv := mountAdmin(t, h)

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/admin/tenants",
		strings.NewReader(`{"name":"acme"}`)))
	var t1 tenantResponse
	decodeJSON(t, rr.Body.Bytes(), &t1)

	rr2 := httptest.NewRecorder()
	srv.ServeHTTP(rr2, httptest.NewRequest(http.MethodPost, "/admin/tenants/"+t1.ID+"/tokens",
		strings.NewReader(`{"label":"prod","ttlSeconds":3600}`)))
	if rr2.Code != http.StatusCreated {
		t.Fatalf("issue status = %d, body=%s", rr2.Code, rr2.Body)
	}
	var issued issuedTokenResponse
	decodeJSON(t, rr2.Body.Bytes(), &issued)
	if !strings.HasPrefix(issued.Token, TokenPrefix) {
		t.Errorf("plaintext token missing prefix: %q", issued.Token)
	}
	if issued.Info.CreatedBy != "test-admin" {
		t.Errorf("CreatedBy = %q, want test-admin (from claims)", issued.Info.CreatedBy)
	}
	if issued.Info.ExpiresAt == nil {
		t.Error("ExpiresAt should be set when ttlSeconds > 0")
	}

	// Subsequent list does not include the plaintext.
	rr3 := httptest.NewRecorder()
	srv.ServeHTTP(rr3, httptest.NewRequest(http.MethodGet, "/admin/tenants/"+t1.ID+"/tokens", nil))
	if strings.Contains(rr3.Body.String(), issued.Token) {
		t.Errorf("list leaked plaintext token: %s", rr3.Body)
	}
}

func TestHandlers_IssueToken_RequiresLabel(t *testing.T) {
	h, _ := newTestHandlers(t)
	srv := mountAdmin(t, h)

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/admin/tenants",
		strings.NewReader(`{"name":"acme"}`)))
	var t1 tenantResponse
	decodeJSON(t, rr.Body.Bytes(), &t1)

	rr2 := httptest.NewRecorder()
	srv.ServeHTTP(rr2, httptest.NewRequest(http.MethodPost, "/admin/tenants/"+t1.ID+"/tokens",
		strings.NewReader(`{}`)))
	if rr2.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for missing label; body=%s", rr2.Code, rr2.Body)
	}
}

func TestHandlers_RotateToken_InvalidatesCache(t *testing.T) {
	h, inv := newTestHandlers(t)
	srv := mountAdmin(t, h)

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/admin/tenants",
		strings.NewReader(`{"name":"acme"}`)))
	var t1 tenantResponse
	decodeJSON(t, rr.Body.Bytes(), &t1)

	rr2 := httptest.NewRecorder()
	srv.ServeHTTP(rr2, httptest.NewRequest(http.MethodPost, "/admin/tenants/"+t1.ID+"/tokens",
		strings.NewReader(`{"label":"prod"}`)))
	var issued issuedTokenResponse
	decodeJSON(t, rr2.Body.Bytes(), &issued)

	before := inv.calls.Load()
	rr3 := httptest.NewRecorder()
	srv.ServeHTTP(rr3, httptest.NewRequest(http.MethodPost,
		"/admin/tenants/"+t1.ID+"/tokens/"+issued.Info.ID+"/rotate", nil))
	if rr3.Code != http.StatusOK {
		t.Fatalf("rotate status = %d, body=%s", rr3.Code, rr3.Body)
	}
	if inv.calls.Load() != before+1 {
		t.Errorf("rotate must invalidate cache once, calls = %d (was %d)", inv.calls.Load(), before)
	}
	var rotated issuedTokenResponse
	decodeJSON(t, rr3.Body.Bytes(), &rotated)
	if rotated.Token == issued.Token {
		t.Error("rotation must produce a new plaintext")
	}
	if rotated.Info.Label != "prod" {
		t.Errorf("rotation should preserve label, got %q", rotated.Info.Label)
	}
}

func TestHandlers_RevokeToken_InvalidatesCacheAnd204(t *testing.T) {
	h, inv := newTestHandlers(t)
	srv := mountAdmin(t, h)

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/admin/tenants",
		strings.NewReader(`{"name":"acme"}`)))
	var t1 tenantResponse
	decodeJSON(t, rr.Body.Bytes(), &t1)

	rr2 := httptest.NewRecorder()
	srv.ServeHTTP(rr2, httptest.NewRequest(http.MethodPost, "/admin/tenants/"+t1.ID+"/tokens",
		strings.NewReader(`{"label":"prod"}`)))
	var issued issuedTokenResponse
	decodeJSON(t, rr2.Body.Bytes(), &issued)

	before := inv.calls.Load()
	rr3 := httptest.NewRecorder()
	srv.ServeHTTP(rr3, httptest.NewRequest(http.MethodDelete,
		"/admin/tenants/"+t1.ID+"/tokens/"+issued.Info.ID, nil))
	if rr3.Code != http.StatusNoContent {
		t.Fatalf("revoke status = %d, body=%s", rr3.Code, rr3.Body)
	}
	if inv.calls.Load() != before+1 {
		t.Errorf("revoke must invalidate cache once, calls = %d (was %d)", inv.calls.Load(), before)
	}
}

func TestHandlers_RevokeToken_NotFound(t *testing.T) {
	h, _ := newTestHandlers(t)
	srv := mountAdmin(t, h)

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/admin/tenants",
		strings.NewReader(`{"name":"acme"}`)))
	var t1 tenantResponse
	decodeJSON(t, rr.Body.Bytes(), &t1)

	rr2 := httptest.NewRecorder()
	srv.ServeHTTP(rr2, httptest.NewRequest(http.MethodDelete,
		"/admin/tenants/"+t1.ID+"/tokens/does-not-exist", nil))
	if rr2.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr2.Code)
	}
}

func TestHandlers_DeleteTenant_InvalidatesCache(t *testing.T) {
	h, inv := newTestHandlers(t)
	srv := mountAdmin(t, h)

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/admin/tenants",
		strings.NewReader(`{"name":"acme"}`)))
	var t1 tenantResponse
	decodeJSON(t, rr.Body.Bytes(), &t1)

	before := inv.calls.Load()
	rr2 := httptest.NewRecorder()
	srv.ServeHTTP(rr2, httptest.NewRequest(http.MethodDelete, "/admin/tenants/"+t1.ID, nil))
	if rr2.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, body=%s", rr2.Code, rr2.Body)
	}
	if inv.calls.Load() != before+1 {
		t.Errorf("delete must invalidate cache once, calls = %d", inv.calls.Load())
	}
}
