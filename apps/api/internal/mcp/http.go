package mcp

import (
	"io"
	"net/http"
)

// writeHTTPError replies with a JSON body and an application/json Content-Type
// (not http.Error's text/plain), so EVERY response from this MCP endpoint is
// JSON — strict MCP clients can parse failures the same way they parse success.
func writeHTTPError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"error":` + strconvQuote(msg) + `}`))
}

// strconvQuote JSON-quotes a short, known message without pulling encoding/json
// into the hot path. The inputs here are all literal strings, so a minimal
// escape of the quote/backslash is sufficient.
func strconvQuote(s string) string {
	out := make([]byte, 0, len(s)+2)
	out = append(out, '"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '"' || c == '\\' {
			out = append(out, '\\')
		}
		out = append(out, c)
	}
	out = append(out, '"')
	return string(out)
}

// httpMaxRequestBytes bounds the request body the HTTP transport will read.
// MCP requests are small; this just guards against an oversized POST.
const httpMaxRequestBytes = 4 << 20

// Handler returns an http.Handler implementing the MCP Streamable HTTP
// transport in its single-response form: the client POSTs one JSON-RPC message
// and receives one JSON response (Content-Type: application/json). This server
// never initiates server-to-client streaming, so it does not implement the
// optional SSE (text/event-stream) response or the GET listening channel.
//
// The request context flows straight into tool execution. Mount this inside
// the authenticated route group (after RequireAuth + ResolveTenant +
// resolveCluster) so the context already carries the (tenant, cluster)
// RuntimeKey and tool calls resolve to the caller's authorized runtime. The
// handler is NOT wrapped by requireConnector on purpose: initialize and
// tools/list must work even when the cluster is momentarily disconnected, and
// tools/call degrades gracefully (the executor returns an isError result).
func Handler(srv *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			// handled below
		case http.MethodGet:
			// Streamable HTTP allows a GET to open a server->client SSE
			// channel; we don't push, so advertise POST-only.
			w.Header().Set("Allow", "POST")
			writeHTTPError(w, http.StatusMethodNotAllowed, "this MCP server does not support server-initiated streaming; use POST")
			return
		default:
			w.Header().Set("Allow", "POST")
			writeHTTPError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, httpMaxRequestBytes))
		if err != nil {
			writeHTTPError(w, http.StatusBadRequest, "failed to read request body")
			return
		}

		resp, _ := srv.HandleMessage(r.Context(), body)
		if resp == nil {
			// Notification — accepted, nothing to return. Still advertise JSON
			// so a client never sees an unexpected content type from us.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(resp)
	}
}
