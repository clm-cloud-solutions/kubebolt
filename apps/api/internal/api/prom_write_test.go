package api

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

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
