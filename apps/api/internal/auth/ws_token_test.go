package auth

import (
	"encoding/base64"
	"net/http/httptest"
	"testing"
)

func TestWSToken(t *testing.T) {
	const jwt = "eyJhbGciOiJIUzI1NiJ9.eyJ1aWQiOiJ1MSJ9.c2lnbmF0dXJl"
	bearer := WSBearerPrefix + base64.RawURLEncoding.EncodeToString([]byte(jwt))

	cases := []struct {
		name, protocols, query, want string
	}{
		{"subprotocol, as the web sends it", WSProtocol + ", " + bearer, "", jwt},
		{"subprotocol wins over the URL", WSProtocol + ", " + bearer, "other", jwt},
		{"deprecated URL token", "", jwt, jwt},
		{"neither", WSProtocol, "", ""},
		{"malformed bearer is not retried from the URL", WSProtocol + ", " + WSBearerPrefix + "%%%", jwt, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			target := "/api/v1/ws"
			if c.query != "" {
				target += "?token=" + c.query
			}
			r := httptest.NewRequest("GET", target, nil)
			if c.protocols != "" {
				r.Header.Set("Sec-WebSocket-Protocol", c.protocols)
			}
			if got := WSToken(r); got != c.want {
				t.Errorf("WSToken = %q, want %q", got, c.want)
			}
		})
	}
}
