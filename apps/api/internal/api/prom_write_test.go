package api

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/golang/snappy"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
)

// newTenantsStoreWithToken spins a fresh BoltDB + TenantsStore +
// IngestTokenStore in t.TempDir() and issues a single non-expiring ingest
// token. Returns both stores + the plaintext token for use in Authorization
// headers. (Tokens live in their own store now, not inlined in the tenant.)
func newTenantsStoreWithToken(t *testing.T) (*auth.TenantsStore, *auth.BoltIngestTokenStore, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := auth.NewStore(dir)
	if err != nil {
		t.Fatalf("auth.NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ts, err := auth.NewTenantsStore(store.DB())
	if err != nil {
		t.Fatalf("auth.NewTenantsStore: %v", err)
	}
	its, err := auth.NewIngestTokenStore(store.DB())
	if err != nil {
		t.Fatalf("auth.NewIngestTokenStore: %v", err)
	}
	tn, err := ts.CreateTenant("test-tenant", "team")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	plaintext, _, err := its.Issue(tn.ID, "", "scrape", "admin", nil)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	return ts, its, plaintext
}

// lookupBearerTenant resolves the tenant a plaintext ingest token maps to,
// going through the ingest store (token → TenantID) then the tenant store.
// Replaces the old TenantsStore.LookupByToken which returned the tenant
// directly when tokens were inlined.
func lookupBearerTenant(t *testing.T, ts *auth.TenantsStore, its *auth.BoltIngestTokenStore, plaintext string) *auth.Tenant {
	t.Helper()
	tok, err := its.Lookup(plaintext)
	if err != nil {
		t.Fatalf("ingest Lookup: %v", err)
	}
	tn, err := ts.GetTenant(tok.TenantID)
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	return tn
}

// fakeUpstream is a minimal stand-in for VictoriaMetrics' /api/v1/write.
// Captures the last request the handler proxied and replies with whatever
// status + body the test set up.
type fakeUpstream struct {
	mu             sync.Mutex
	lastMethod     string
	lastPath       string
	lastBody       []byte
	lastHeaders    http.Header
	respStatus     int
	respBody       string
	respContentTyp string
}

func (f *fakeUpstream) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.lastMethod = r.Method
		f.lastPath = r.URL.Path
		f.lastBody, _ = io.ReadAll(r.Body)
		f.lastHeaders = r.Header.Clone()
		if f.respContentTyp != "" {
			w.Header().Set("Content-Type", f.respContentTyp)
		}
		status := f.respStatus
		if status == 0 {
			status = http.StatusNoContent
		}
		w.WriteHeader(status)
		if f.respBody != "" {
			_, _ = io.WriteString(w, f.respBody)
		}
	}
}

// withPromWriteEnabled flips the env var on for the duration of the
// test. t.Setenv restores the prior value on cleanup automatically.
func withPromWriteEnabled(t *testing.T) {
	t.Helper()
	t.Setenv("KUBEBOLT_REMOTE_WRITE_ENABLED", "true")
}

// pointStorageAt swaps KUBEBOLT_METRICS_STORAGE_URL to the test
// upstream's URL. The handler reads this var via metricsStorageURL().
func pointStorageAt(t *testing.T, ts *httptest.Server) {
	t.Helper()
	t.Setenv("KUBEBOLT_METRICS_STORAGE_URL", ts.URL)
}

func TestHandlePromWrite_DisabledByDefault(t *testing.T) {
	// No env vars set → endpoint MUST 404.
	t.Setenv("KUBEBOLT_REMOTE_WRITE_ENABLED", "")
	h := &handlers{}
	req := httptest.NewRequest(http.MethodPost, "/prom/write", strings.NewReader("payload"))
	rec := httptest.NewRecorder()

	h.handlePromWrite(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 when disabled, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "KUBEBOLT_REMOTE_WRITE_ENABLED") {
		t.Errorf("expected error to hint at the env var, got %q", rec.Body.String())
	}
}

func TestHandlePromWrite_WrongMethodReturns405(t *testing.T) {
	withPromWriteEnabled(t)
	h := &handlers{}
	req := httptest.NewRequest(http.MethodGet, "/prom/write", nil)
	rec := httptest.NewRecorder()

	h.handlePromWrite(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for GET, got %d", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != http.MethodPost {
		t.Errorf("expected Allow: POST, got %q", got)
	}
}

func TestHandlePromWrite_ForwardsToUpstream(t *testing.T) {
	upstream := &fakeUpstream{respStatus: http.StatusNoContent}
	ts := httptest.NewServer(upstream.handler())
	defer ts.Close()

	withPromWriteEnabled(t)
	pointStorageAt(t, ts)

	body := []byte("\x00\x01\x02fake-snappy-protobuf")
	h := &handlers{}
	req := httptest.NewRequest(http.MethodPost, "/prom/write", bytes.NewReader(body))
	req.Header.Set("Content-Encoding", "snappy")
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("X-Prometheus-Remote-Write-Version", "0.1.0")
	rec := httptest.NewRecorder()

	h.handlePromWrite(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("expected upstream's 204 to pass through, got %d (body=%q)", rec.Code, rec.Body.String())
	}
	upstream.mu.Lock()
	defer upstream.mu.Unlock()
	if upstream.lastMethod != http.MethodPost {
		t.Errorf("upstream method = %q, want POST", upstream.lastMethod)
	}
	if upstream.lastPath != "/api/v1/write" {
		t.Errorf("upstream path = %q, want /api/v1/write", upstream.lastPath)
	}
	if !bytes.Equal(upstream.lastBody, body) {
		t.Errorf("upstream body bytes mismatched\n got: %x\nwant: %x", upstream.lastBody, body)
	}
	for _, h := range []string{"Content-Encoding", "Content-Type", "X-Prometheus-Remote-Write-Version"} {
		if upstream.lastHeaders.Get(h) == "" {
			t.Errorf("upstream missing header %q", h)
		}
	}
}

func TestHandlePromWrite_PassesThroughUpstreamError(t *testing.T) {
	upstream := &fakeUpstream{
		respStatus: http.StatusBadRequest,
		respBody:   "cardinality limit exceeded for tenant",
	}
	ts := httptest.NewServer(upstream.handler())
	defer ts.Close()

	withPromWriteEnabled(t)
	pointStorageAt(t, ts)

	h := &handlers{}
	req := httptest.NewRequest(http.MethodPost, "/prom/write", strings.NewReader("payload"))
	rec := httptest.NewRecorder()

	h.handlePromWrite(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected upstream's 400 to pass through, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "cardinality limit") {
		t.Errorf("expected upstream body to pass through, got %q", rec.Body.String())
	}
}

func TestHandlePromWrite_UnreachableUpstreamReturns502(t *testing.T) {
	withPromWriteEnabled(t)
	// Point at a port nobody's listening on. 127.0.0.1:1 is reserved
	// and the OS rejects the connection immediately on most platforms.
	t.Setenv("KUBEBOLT_METRICS_STORAGE_URL", "http://127.0.0.1:1")

	h := &handlers{}
	req := httptest.NewRequest(http.MethodPost, "/prom/write", strings.NewReader("payload"))
	rec := httptest.NewRecorder()

	h.handlePromWrite(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Errorf("expected 502 when upstream unreachable, got %d", rec.Code)
	}
}

func TestHandlePromWrite_BodyTooLargeReturns413(t *testing.T) {
	withPromWriteEnabled(t)
	// We don't need a real upstream — the body cap fires before we
	// ever build the upstream request.
	body := bytes.Repeat([]byte("x"), promWriteMaxBodyBytes+1)
	h := &handlers{}
	req := httptest.NewRequest(http.MethodPost, "/prom/write", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	h.handlePromWrite(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413, got %d (body=%q)", rec.Code, rec.Body.String())
	}
}

// ─── Auth (three-tier enforcement) ────────────────────────────────
//
// Mirrors the gRPC channel's AuthEnforcement semantics. The authoritative
// behavior table:
//
//                       no bearer    bad bearer    valid bearer
//   disabled   (*)      pass         pass          pass
//   permissive (*)      WARN+pass    WARN+pass     pass
//   enforced            401          401           pass
//
// (*) "disabled" ignores the header entirely; "permissive" still
// validates the header when present, but also accepts when missing.

func TestHandlePromWrite_Enforced_NoBearerReturns401(t *testing.T) {
	withPromWriteEnabled(t)
	store, its, _ := newTenantsStoreWithToken(t)
	h := &handlers{tenantsStore: store, ingestTokens: its, promWriteAuthMode: promWriteAuthEnforced}

	req := httptest.NewRequest(http.MethodPost, "/prom/write", strings.NewReader("payload"))
	rec := httptest.NewRecorder()

	h.handlePromWrite(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 with no bearer in enforced mode, got %d", rec.Code)
	}
}

func TestHandlePromWrite_Enforced_BadBearerReturns401(t *testing.T) {
	withPromWriteEnabled(t)
	store, its, _ := newTenantsStoreWithToken(t)
	h := &handlers{tenantsStore: store, ingestTokens: its, promWriteAuthMode: promWriteAuthEnforced}

	req := httptest.NewRequest(http.MethodPost, "/prom/write", strings.NewReader("payload"))
	req.Header.Set("Authorization", "Bearer kbtok_v1_not-a-real-token")
	rec := httptest.NewRecorder()

	h.handlePromWrite(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 with bad bearer in enforced mode, got %d", rec.Code)
	}
}

func TestHandlePromWrite_Enforced_EmptyBearerReturns401(t *testing.T) {
	withPromWriteEnabled(t)
	store, its, _ := newTenantsStoreWithToken(t)
	h := &handlers{tenantsStore: store, ingestTokens: its, promWriteAuthMode: promWriteAuthEnforced}

	req := httptest.NewRequest(http.MethodPost, "/prom/write", strings.NewReader("payload"))
	req.Header.Set("Authorization", "Bearer ")
	rec := httptest.NewRecorder()

	h.handlePromWrite(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 with empty bearer in enforced mode, got %d", rec.Code)
	}
}

func TestHandlePromWrite_Enforced_ValidBearerForwards(t *testing.T) {
	upstream := &fakeUpstream{respStatus: http.StatusNoContent}
	ts := httptest.NewServer(upstream.handler())
	defer ts.Close()
	withPromWriteEnabled(t)
	pointStorageAt(t, ts)

	store, its, plaintext := newTenantsStoreWithToken(t)
	h := &handlers{tenantsStore: store, ingestTokens: its, promWriteAuthMode: promWriteAuthEnforced}

	body := []byte("snappy-protobuf-bytes")
	req := httptest.NewRequest(http.MethodPost, "/prom/write", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+plaintext)
	rec := httptest.NewRecorder()

	h.handlePromWrite(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("expected upstream's 204 to pass through, got %d (body=%q)", rec.Code, rec.Body.String())
	}
	upstream.mu.Lock()
	defer upstream.mu.Unlock()
	if !bytes.Equal(upstream.lastBody, body) {
		t.Errorf("upstream did not receive the original body bytes")
	}
}

func TestHandlePromWrite_Enforced_NoStoreReturns500(t *testing.T) {
	// Defense in depth: enforced mode without TenantsStore is a
	// misconfiguration. main.go downgrades to disabled at startup
	// and logs a WARN, but if the field somehow gets set without a
	// store (future code path, programmatic test), the handler must
	// fail closed rather than accepting silently.
	withPromWriteEnabled(t)
	h := &handlers{tenantsStore: nil, promWriteAuthMode: promWriteAuthEnforced}

	req := httptest.NewRequest(http.MethodPost, "/prom/write", strings.NewReader("payload"))
	req.Header.Set("Authorization", "Bearer something")
	rec := httptest.NewRecorder()

	h.handlePromWrite(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 (misconfig) when enforced+no-store, got %d", rec.Code)
	}
}

// ─── Permissive — header is optional, validated when present ──────

func TestHandlePromWrite_Permissive_NoBearerLogsWarnAndForwards(t *testing.T) {
	upstream := &fakeUpstream{respStatus: http.StatusNoContent}
	ts := httptest.NewServer(upstream.handler())
	defer ts.Close()
	withPromWriteEnabled(t)
	pointStorageAt(t, ts)

	store, its, _ := newTenantsStoreWithToken(t)
	h := &handlers{tenantsStore: store, ingestTokens: its, promWriteAuthMode: promWriteAuthPermissive}

	req := httptest.NewRequest(http.MethodPost, "/prom/write", strings.NewReader("payload"))
	rec := httptest.NewRecorder()

	h.handlePromWrite(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("expected 204 (permissive accepts no-bearer), got %d", rec.Code)
	}
}

func TestHandlePromWrite_Permissive_BadBearerLogsWarnAndForwards(t *testing.T) {
	upstream := &fakeUpstream{respStatus: http.StatusNoContent}
	ts := httptest.NewServer(upstream.handler())
	defer ts.Close()
	withPromWriteEnabled(t)
	pointStorageAt(t, ts)

	store, its, _ := newTenantsStoreWithToken(t)
	h := &handlers{tenantsStore: store, ingestTokens: its, promWriteAuthMode: promWriteAuthPermissive}

	req := httptest.NewRequest(http.MethodPost, "/prom/write", strings.NewReader("payload"))
	req.Header.Set("Authorization", "Bearer kbtok_v1_garbage")
	rec := httptest.NewRecorder()

	h.handlePromWrite(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("expected 204 (permissive accepts bad-bearer), got %d", rec.Code)
	}
}

func TestHandlePromWrite_Permissive_ValidBearerForwards(t *testing.T) {
	upstream := &fakeUpstream{respStatus: http.StatusNoContent}
	ts := httptest.NewServer(upstream.handler())
	defer ts.Close()
	withPromWriteEnabled(t)
	pointStorageAt(t, ts)

	store, its, plaintext := newTenantsStoreWithToken(t)
	h := &handlers{tenantsStore: store, ingestTokens: its, promWriteAuthMode: promWriteAuthPermissive}

	req := httptest.NewRequest(http.MethodPost, "/prom/write", strings.NewReader("payload"))
	req.Header.Set("Authorization", "Bearer "+plaintext)
	rec := httptest.NewRecorder()

	h.handlePromWrite(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("expected 204 (valid bearer always accepted), got %d", rec.Code)
	}
}

// ─── Disabled — header ignored, store unused ──────────────────────

func TestHandlePromWrite_Disabled_BadBearerStillForwards(t *testing.T) {
	upstream := &fakeUpstream{respStatus: http.StatusNoContent}
	ts := httptest.NewServer(upstream.handler())
	defer ts.Close()
	withPromWriteEnabled(t)
	pointStorageAt(t, ts)

	store, its, _ := newTenantsStoreWithToken(t)
	h := &handlers{tenantsStore: store, ingestTokens: its, promWriteAuthMode: promWriteAuthDisabled}

	req := httptest.NewRequest(http.MethodPost, "/prom/write", strings.NewReader("payload"))
	req.Header.Set("Authorization", "Bearer kbtok_v1_garbage")
	rec := httptest.NewRecorder()

	h.handlePromWrite(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("expected 204 (disabled ignores header), got %d", rec.Code)
	}
}

func TestHandlePromWrite_Disabled_DefaultsWhenModeEmpty(t *testing.T) {
	// Empty string promWriteAuthMode falls back to "disabled" —
	// matches the parser default in main.go for unset env var. Same
	// code path as Sprint A migration default.
	upstream := &fakeUpstream{respStatus: http.StatusNoContent}
	ts := httptest.NewServer(upstream.handler())
	defer ts.Close()
	withPromWriteEnabled(t)
	pointStorageAt(t, ts)

	h := &handlers{tenantsStore: nil, promWriteAuthMode: ""}

	req := httptest.NewRequest(http.MethodPost, "/prom/write", strings.NewReader("payload"))
	rec := httptest.NewRecorder()

	h.handlePromWrite(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("expected 204 (empty mode → disabled), got %d", rec.Code)
	}
}

// ─── Truthy / falsy parsing of the env-var gate ───────────────────

func TestPromWriteEnabled_Truthy(t *testing.T) {
	for _, v := range []string{"true", "TRUE", "True", "1", "yes", "Yes"} {
		t.Setenv("KUBEBOLT_REMOTE_WRITE_ENABLED", v)
		if !promWriteEnabled() {
			t.Errorf("promWriteEnabled() with %q = false, want true", v)
		}
	}
}

func TestPromWriteEnabled_Falsy(t *testing.T) {
	for _, v := range []string{"", "false", "0", "no", "anything-else"} {
		t.Setenv("KUBEBOLT_REMOTE_WRITE_ENABLED", v)
		if promWriteEnabled() {
			t.Errorf("promWriteEnabled() with %q = true, want false", v)
		}
	}
}

// ─── Phase 3 Day 4.3 — enforced mode rejects missing tenant_id ────────

// limitDefaultsForTest is a small fixed-config EffectiveLimits used by
// the Day 4 / 4.3 tests. Keeps the rate-limit bucket roomy so the
// tenant_id validation path is the one being tested, not the rate
// limit gate.
func limitDefaultsForTest() auth.EffectiveLimits {
	return auth.EffectiveLimits{
		WriteSamplesPerSec: 100_000,
		WriteBurstSamples:  1_000_000,
		MaxActiveSeries:    10_000_000,
	}
}

// buildSnappyWriteRequest constructs a real snappy-compressed
// WriteRequest body for the test. labels are applied to a single
// TimeSeries; if labels is empty the series has no labels (so
// readTenantIDFromFirstSeries reports absent).
func buildSnappyWriteRequest(t *testing.T, labels [][2]string) []byte {
	t.Helper()
	body := buildWriteRequestRich([]struct {
		Labels  [][2]string
		Samples int
	}{
		{Labels: labels, Samples: 1},
	})
	return snappy.Encode(nil, body)
}

func TestHandlePromWrite_Enforced_MissingTenantIDReturns401(t *testing.T) {
	// Day 4.3: in enforced mode, samples WITHOUT a tenant_id label
	// are rejected outright (no auto-stamp fallback). Operator must
	// configure tenant.id via helm.
	withPromWriteEnabled(t)
	store, its, plaintext := newTenantsStoreWithToken(t)

	h := &handlers{
		tenantsStore:      store,
		ingestTokens:      its,
		promWriteAuthMode: promWriteAuthEnforced,
		// promRateLimiter MUST be non-nil to activate the decode +
		// validation path. Day 4.1 wires the entire tenant_id check
		// inside `if h.promRateLimiter != nil`.
		promRateLimiter: NewPromRateLimiter(limitDefaultsForTest()),
	}

	// Body has labels but NO tenant_id.
	body := buildSnappyWriteRequest(t, [][2]string{{"__name__", "up"}, {"job", "node"}})
	req := httptest.NewRequest(http.MethodPost, "/prom/write", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+plaintext)
	rec := httptest.NewRecorder()

	h.handlePromWrite(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("enforced + missing tenant_id should 401, got %d (body=%q)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "tenant_id label required in enforced mode") {
		t.Errorf("error body should explain the missing tenant_id requirement, got %q", rec.Body.String())
	}
}

func TestHandlePromWrite_Permissive_MissingTenantIDAutoStamps(t *testing.T) {
	// Day 4.1's auto-stamp safety net stays alive in permissive mode.
	// Missing tenant_id → receiver injects it server-side, forwards
	// the stamped body. Transitional path for legacy agents that
	// haven't been re-deployed with `tenant.id` set yet.
	upstream := &fakeUpstream{respStatus: http.StatusNoContent}
	ts := httptest.NewServer(upstream.handler())
	defer ts.Close()
	withPromWriteEnabled(t)
	pointStorageAt(t, ts)

	store, its, plaintext := newTenantsStoreWithToken(t)

	h := &handlers{
		tenantsStore:      store,
		ingestTokens:      its,
		promWriteAuthMode: promWriteAuthPermissive,
		promRateLimiter:   NewPromRateLimiter(limitDefaultsForTest()),
	}

	body := buildSnappyWriteRequest(t, [][2]string{{"__name__", "up"}, {"job", "node"}})
	req := httptest.NewRequest(http.MethodPost, "/prom/write", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+plaintext)
	rec := httptest.NewRecorder()

	h.handlePromWrite(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("permissive + missing tenant_id should auto-stamp and forward (204), got %d (body=%q)", rec.Code, rec.Body.String())
	}
	// Verify upstream received a body different from the original —
	// the auto-stamp re-encoded, so byte equality must fail.
	upstream.mu.Lock()
	defer upstream.mu.Unlock()
	if bytes.Equal(upstream.lastBody, body) {
		t.Errorf("auto-stamp should have rewritten the body before forwarding; upstream received original bytes")
	}
	// Decode the upstream body to confirm tenant_id is now present.
	// Use LookupByToken to get the ACTUAL bearer tenant (not the
	// auto-seeded "default" — the helper creates a separate
	// "test-tenant" and issues against that).
	decoded, err := snappy.Decode(nil, upstream.lastBody)
	if err != nil {
		t.Fatalf("upstream body should be valid snappy, got: %v", err)
	}
	bearerTenant := lookupBearerTenant(t, store, its, plaintext)
	tid, found := readTenantIDFromFirstSeries(decoded)
	if !found {
		t.Errorf("tenant_id should be stamped on the forwarded body, got absent")
	} else if tid != bearerTenant.ID {
		t.Errorf("tenant_id should match the bearer's tenant ID; got %q want %q", tid, bearerTenant.ID)
	}
}

func TestHandlePromWrite_Enforced_MatchingTenantIDForwards(t *testing.T) {
	// Fast path: enforced mode + tenant_id label present and matching
	// the bearer's tenant. No auto-stamp, no rewrite — original body
	// forwarded byte-for-byte.
	upstream := &fakeUpstream{respStatus: http.StatusNoContent}
	ts := httptest.NewServer(upstream.handler())
	defer ts.Close()
	withPromWriteEnabled(t)
	pointStorageAt(t, ts)

	store, its, plaintext := newTenantsStoreWithToken(t)
	// Resolve the actual tenant the bearer maps to (the helper
	// creates "test-tenant", not the auto-seeded "default").
	bearerTenant := lookupBearerTenant(t, store, its, plaintext)

	h := &handlers{
		tenantsStore:      store,
		ingestTokens:      its,
		promWriteAuthMode: promWriteAuthEnforced,
		promRateLimiter:   NewPromRateLimiter(limitDefaultsForTest()),
	}

	// Body's first TimeSeries already carries tenant_id matching the
	// bearer's tenant.
	body := buildSnappyWriteRequest(t, [][2]string{
		{"__name__", "up"},
		{TenantIDLabelName, bearerTenant.ID},
	})
	req := httptest.NewRequest(http.MethodPost, "/prom/write", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+plaintext)
	rec := httptest.NewRecorder()

	h.handlePromWrite(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("enforced + matching tenant_id should forward (204), got %d (body=%q)", rec.Code, rec.Body.String())
	}
	upstream.mu.Lock()
	defer upstream.mu.Unlock()
	if !bytes.Equal(upstream.lastBody, body) {
		t.Errorf("matching tenant_id should forward the original body byte-for-byte (no re-encode)")
	}
}
