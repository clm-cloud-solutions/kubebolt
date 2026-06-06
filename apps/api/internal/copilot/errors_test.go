package copilot

import (
	"errors"
	"net"
	"testing"
)

func TestIsRecoverable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"429 rate limit", &ProviderHTTPError{StatusCode: 429}, true},
		{"500 server", &ProviderHTTPError{StatusCode: 500}, true},
		{"503 unavailable", &ProviderHTTPError{StatusCode: 503}, true},
		// 404 (primary model/endpoint unavailable for the account) now falls
		// over to the secondary provider — the fallback's whole reason to exist.
		{"404 model unavailable", &ProviderHTTPError{StatusCode: 404}, true},
		// Auth/validation stay non-recoverable: a retry on the fallback can't fix them.
		{"401 auth", &ProviderHTTPError{StatusCode: 401}, false},
		{"403 forbidden", &ProviderHTTPError{StatusCode: 403}, false},
		{"400 bad request", &ProviderHTTPError{StatusCode: 400}, false},
		{"network error", &net.OpError{Op: "dial", Err: errors.New("connection refused")}, true},
		{"other error", errors.New("parse failure"), false},
	}
	for _, c := range cases {
		if got := IsRecoverable(c.err); got != c.want {
			t.Errorf("IsRecoverable(%s) = %v, want %v", c.name, got, c.want)
		}
	}
}
