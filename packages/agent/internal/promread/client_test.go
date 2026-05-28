package promread

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeProm builds a httptest.Server that asserts auth + query params,
// returns the canned body on /api/v1/query_range, and 404 elsewhere.
func fakeProm(t *testing.T, body string, status int, wantAuth string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query_range" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if wantAuth != "" && r.Header.Get("Authorization") != wantAuth {
			t.Errorf("missing/wrong Authorization header: got %q want %q",
				r.Header.Get("Authorization"), wantAuth)
		}
		for _, p := range []string{"query", "start", "end", "step"} {
			if r.URL.Query().Get(p) == "" {
				t.Errorf("missing query param: %s", p)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

const successBody = `{
  "status":"success",
  "data":{
    "resultType":"matrix",
    "result":[
      {"metric":{"__name__":"up","instance":"a"},
       "values":[[1700000000,"1"],[1700000030,"0"]]}
    ]
  }
}`

func TestQueryRange_HappyPath(t *testing.T) {
	srv := fakeProm(t, successBody, http.StatusOK, "")
	auth, _ := NewProvider(AuthConfig{Mode: AuthNone})
	c := NewClient(srv.URL, auth)

	resp, err := c.QueryRange(context.Background(), "up", time.Unix(1700000000, 0), time.Unix(1700000060, 0), 30*time.Second)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if resp.Status != "success" {
		t.Errorf("status: got %q want success", resp.Status)
	}
	if len(resp.Data.Result) != 1 {
		t.Fatalf("expected 1 series, got %d", len(resp.Data.Result))
	}
}

func TestQueryRange_BearerAuthHeaderSent(t *testing.T) {
	srv := fakeProm(t, successBody, http.StatusOK, "Bearer hunter2")
	auth, _ := NewProvider(AuthConfig{Mode: AuthBearer, BearerToken: "hunter2"})
	c := NewClient(srv.URL, auth)

	if _, err := c.QueryRange(context.Background(), "up", time.Now().Add(-time.Minute), time.Now(), 30*time.Second); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestQueryRange_HTTPErrorSurfaced(t *testing.T) {
	srv := fakeProm(t, `{"status":"error","errorType":"bad_data","error":"parse error"}`, http.StatusBadRequest, "")
	auth, _ := NewProvider(AuthConfig{Mode: AuthNone})
	c := NewClient(srv.URL, auth)

	_, err := c.QueryRange(context.Background(), "garbage", time.Now().Add(-time.Minute), time.Now(), 30*time.Second)
	if err == nil {
		t.Fatal("expected error for HTTP 400")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("expected 400 in error, got %v", err)
	}
}

func TestQueryRange_PromErrorStatusSurfaced(t *testing.T) {
	// Prom can return HTTP 200 with status:"error" in the body —
	// the client must catch that, not only HTTP status.
	srv := fakeProm(t, `{"status":"error","errorType":"timeout","error":"query took too long"}`, http.StatusOK, "")
	auth, _ := NewProvider(AuthConfig{Mode: AuthNone})
	c := NewClient(srv.URL, auth)

	_, err := c.QueryRange(context.Background(), "slow", time.Now().Add(-time.Minute), time.Now(), 30*time.Second)
	if err == nil {
		t.Fatal("expected error for prom status=error")
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("expected error to mention timeout, got %v", err)
	}
}

func TestNewClient_TrimsTrailingSlash(t *testing.T) {
	auth, _ := NewProvider(AuthConfig{Mode: AuthNone})
	c := NewClient("http://example.com/", auth)
	if c.baseURL != "http://example.com" {
		t.Errorf("baseURL: got %q want http://example.com", c.baseURL)
	}
}
