// A browser cannot set an Authorization header on a WebSocket upgrade, and a
// token in the URL ends up in proxy access logs, browser history and support
// screenshots. The token travels as a subprotocol instead, the way the
// Kubernetes API server authenticates WebSockets; the server answers with
// WS_PROTOCOL and never echoes the token (apps/api/internal/auth/ws_token.go).

export const WS_PROTOCOL = 'kubebolt.v1'
const WS_BEARER_PREFIX = 'base64url.bearer.kubebolt.io.'

function base64url(s: string): string {
  // A JWT is ASCII, so btoa takes it as is.
  return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

// wsProtocols is the second argument of `new WebSocket(url, protocols)`.
export function wsProtocols(token: string | null | undefined): string[] {
  return token ? [WS_PROTOCOL, WS_BEARER_PREFIX + base64url(token)] : [WS_PROTOCOL]
}
