package config

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMetricsStorageURL(t *testing.T) {
	t.Setenv("KUBEBOLT_METRICS_STORAGE_URL", "")
	if u, explicit := MetricsStorageURL(); u != DefaultMetricsStorageURL || explicit {
		t.Fatalf("unset = (%q, %v), want the default, not explicit", u, explicit)
	}

	t.Setenv("KUBEBOLT_METRICS_STORAGE_URL", "https://vm.example.com/select/0/prometheus/")
	if u, explicit := MetricsStorageURL(); u != "https://vm.example.com/select/0/prometheus" || !explicit {
		t.Fatalf("set = (%q, %v), want the URL without its trailing slash, explicit", u, explicit)
	}
}

func TestMetricsStorageAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name, token, user, pass, want string
	}{
		{name: "none", want: ""},
		{name: "basic", user: "kb", pass: "s3cret", want: "Basic a2I6czNjcmV0"},
		{name: "password only", pass: "s3cret", want: "Basic OnMzY3JldA=="},
		{name: "token wins", token: "tok", user: "kb", pass: "s3cret", want: "Bearer tok"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("KUBEBOLT_METRICS_STORAGE_TOKEN", tc.token)
			t.Setenv("KUBEBOLT_METRICS_STORAGE_USERNAME", tc.user)
			t.Setenv("KUBEBOLT_METRICS_STORAGE_PASSWORD", tc.pass)
			if got := MetricsStorageAuthorization(); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRedactedURL(t *testing.T) {
	if got := RedactedURL("https://kb:s3cret@vm.example.com:8428"); got != "https://kb:xxxxx@vm.example.com:8428" {
		t.Fatalf("got %q", got)
	}
	if got := RedactedURL("http://localhost:8428"); got != "http://localhost:8428" {
		t.Fatalf("got %q", got)
	}
}

// Every client that talks to VictoriaMetrics is built on this transport, so the
// credentials configured for the store reach all of them; a caller that sets
// its own Authorization keeps it, and the caller's request is never modified.
func TestMetricsStorageTransport_AddsTheStoreCredentials(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
	}))
	defer srv.Close()
	client := &http.Client{Timeout: 5 * time.Second, Transport: MetricsStorageTransport(nil)}

	for _, tc := range []struct {
		name, token, user, pass, own, want string
	}{
		{name: "no credentials", want: ""},
		{name: "bearer token", token: "tok", want: "Bearer tok"},
		{name: "basic auth", user: "kb", pass: "s3cret", want: "Basic a2I6czNjcmV0"},
		{name: "caller's own header wins", token: "tok", own: "Bearer mine", want: "Bearer mine"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("KUBEBOLT_METRICS_STORAGE_TOKEN", tc.token)
			t.Setenv("KUBEBOLT_METRICS_STORAGE_USERNAME", tc.user)
			t.Setenv("KUBEBOLT_METRICS_STORAGE_PASSWORD", tc.pass)
			req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/query", nil)
			if tc.own != "" {
				req.Header.Set("Authorization", tc.own)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if got != tc.want {
				t.Fatalf("server saw Authorization %q, want %q", got, tc.want)
			}
			if tc.own == "" && req.Header.Get("Authorization") != "" {
				t.Fatal("the caller's request was modified")
			}
		})
	}
}
