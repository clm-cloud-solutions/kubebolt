package auth

import (
	"encoding/base64"
	"log/slog"
	"net/http"
	"strings"
	"sync"
)

// A browser cannot set an Authorization header on a WebSocket upgrade. The web
// used to put the access token in the URL (?token=), and a URL ends up in places
// nobody meant to keep a credential: a proxy's access log, browser history, a
// support screenshot. The token now travels in Sec-WebSocket-Protocol, the way
// the Kubernetes API server authenticates WebSockets.
//
// The browser offers two subprotocols: WSProtocol and WSBearerPrefix followed by
// the token in unpadded base64url. The server selects WSProtocol, so the token
// is never echoed back in the response.
const (
	// WSProtocol is the subprotocol the server answers every upgrade with.
	WSProtocol = "kubebolt.v1"
	// WSBearerPrefix marks the subprotocol that carries the access token.
	WSBearerPrefix = "base64url.bearer.kubebolt.io."
)

var wsQueryTokenOnce sync.Once

// WSToken returns the access token of a WebSocket upgrade: from
// Sec-WebSocket-Protocol, or from the deprecated ?token= query parameter that
// web bundles still cached in browsers send. Empty when neither carries one.
func WSToken(r *http.Request) string {
	for _, header := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, p := range strings.Split(header, ",") {
			p = strings.TrimSpace(p)
			if !strings.HasPrefix(p, WSBearerPrefix) {
				continue
			}
			raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(p, WSBearerPrefix))
			if err != nil {
				return "" // a malformed bearer is not a token; it is not retried from the URL
			}
			return string(raw)
		}
	}
	if t := r.URL.Query().Get("token"); t != "" {
		wsQueryTokenOnce.Do(func() {
			slog.Warn("a WebSocket authenticated with ?token= in its URL — deprecated; current web versions send it in Sec-WebSocket-Protocol",
				slog.String("path", r.URL.Path))
		})
		return t
	}
	return ""
}
