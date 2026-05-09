package api

import (
	"bytes"
	"crypto/subtle"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
)

// promWriteEnabled returns true when the operator opted into the
// remote_write receiver via env var. Default false: Phase 2 stages
// the path without auth and we don't want to expose an unauthenticated
// metrics ingest port by accident on existing installs. Phase 3 will
// remove the gate and add bearer-token auth + rate limiting.
func promWriteEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("KUBEBOLT_REMOTE_WRITE_ENABLED")))
	return v == "1" || v == "true" || v == "yes"
}

// promWriteUpstreamPath is the relative path on the metrics storage
// (VictoriaMetrics) that accepts Prometheus remote_write protocol.
// Encapsulated so the test can swap it indirectly via metricsStorageURL.
const promWriteUpstreamPath = "/api/v1/write"

// promWriteMaxBodyBytes caps the request body size to keep a single
// abusive client from exhausting memory on the backend. 16 MiB is
// generous for vmagent's default scrape window (~1m of samples) but
// firmly bounded; vmagent will retry the next batch if we 413.
const promWriteMaxBodyBytes = 16 << 20

// promWriteUnauthWarnOnce ensures the "running unauthenticated" log
// fires once per process — not per request. Without it, every vmagent
// scrape cycle (every 30s) would emit a WARN; the operator stops
// noticing within minutes.
var promWriteUnauthWarnOnce sync.Once

// authenticatePromWrite gates the remote_write endpoint on the
// existing agent ingest-token infrastructure (the same TenantsStore
// that BearerIngestAuth uses for the gRPC channel). Two operating
// modes:
//
//   tenantsStore != nil   bearer header REQUIRED. Lookup against the
//                         store; mismatch / missing / disabled tenant
//                         all return 401. This is the production
//                         posture and what prevents an open metrics
//                         ingest port on tagged releases.
//
//   tenantsStore == nil   no token validation possible (KubeBolt was
//                         booted without auth wiring, e.g. dev mode
//                         with KUBEBOLT_AUTH_ENABLED=false). The
//                         request is allowed through with a one-time
//                         WARN log so the operator knows the channel
//                         is unauthenticated. Backwards-compat for
//                         existing dev installs that haven't migrated
//                         to auth yet.
//
// The bearer comparison goes through TenantsStore.LookupByToken which
// hashes the plaintext and compares against the stored hash — same
// constant-time semantics as gRPC. The subtle.ConstantTimeCompare
// guard at the entry is for early-rejection of empty bearers (avoids
// hitting the store) and is not security-load-bearing on its own.
//
// Returns true when the request is authorized (or when the dev-mode
// bypass kicks in). Returns false after writing a 401 to w.
func (h *handlers) authenticatePromWrite(w http.ResponseWriter, r *http.Request) bool {
	if h.tenantsStore == nil {
		promWriteUnauthWarnOnce.Do(func() {
			slog.Warn("prom remote_write running UNAUTHENTICATED — TenantsStore not wired",
				slog.String("hint", "set KUBEBOLT_AUTH_ENABLED=true and provision an ingest token via the admin UI / API to gate this endpoint"))
		})
		return true
	}
	authz := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(authz, prefix) {
		respondError(w, http.StatusUnauthorized, "missing Bearer token")
		return false
	}
	token := strings.TrimSpace(authz[len(prefix):])
	if subtle.ConstantTimeCompare([]byte(token), []byte("")) == 1 {
		respondError(w, http.StatusUnauthorized, "empty Bearer token")
		return false
	}
	if _, _, err := h.tenantsStore.LookupByToken(token); err != nil {
		respondError(w, http.StatusUnauthorized, "invalid ingest token")
		return false
	}
	return true
}

// handlePromWrite forwards a Prometheus remote_write request to the
// underlying metrics storage. The wire format is opaque from the
// backend's perspective — Snappy-framed protobuf carrying TimeSeries
// records — so we don't deserialize, just stream the body upstream.
//
// Phase 2 (this commit) gates the endpoint on
// KUBEBOLT_REMOTE_WRITE_ENABLED and trusts the cluster_id label that
// vmagent attaches via relabel_config. Phase 3 will require a bearer
// token, validate the asserted cluster_id against the token's tenant
// scope, and apply per-tenant rate limits + cardinality caps.
//
// Errors and their semantics:
//
//	405  any method other than POST.
//	404  KUBEBOLT_REMOTE_WRITE_ENABLED not set — the route stays
//	     registered so a misconfigured client gets a clean 404 instead
//	     of timing out, but the path is otherwise inert.
//	413  body exceeds promWriteMaxBodyBytes.
//	502  upstream VictoriaMetrics unreachable.
//	2xx  whatever vminsert returned (typically 204 No Content).
func (h *handlers) handlePromWrite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		respondError(w, http.StatusMethodNotAllowed, "POST required for remote_write")
		return
	}

	if !promWriteEnabled() {
		respondError(w, http.StatusNotFound, "remote_write receiver disabled — set KUBEBOLT_REMOTE_WRITE_ENABLED=true")
		return
	}

	// Auth gate. Bearer token validated against the same TenantsStore
	// that the gRPC channel's BearerIngestAuth uses — operators can
	// reuse the agent's existing ingest token Secret instead of
	// provisioning a separate credential just for the scrape sidecar.
	if !h.authenticatePromWrite(w, r) {
		return
	}

	// Cap the body. We read the whole thing into a buffer so the
	// upstream request can set Content-Length explicitly — vminsert
	// rejects chunked transfer with no length on /api/v1/write
	// in some configurations.
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, promWriteMaxBodyBytes))
	if err != nil {
		var maxErr *http.MaxBytesError
		if asErr := err.Error(); strings.Contains(asErr, "http: request body too large") {
			respondError(w, http.StatusRequestEntityTooLarge, "remote_write body exceeds limit")
			return
		}
		_ = maxErr // silence unused if go version doesn't expose the typed error
		respondError(w, http.StatusBadRequest, "read remote_write body")
		return
	}

	target, err := url.Parse(metricsStorageURL() + promWriteUpstreamPath)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "invalid storage URL")
		return
	}

	upstream, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target.String(), bytes.NewReader(body))
	if err != nil {
		respondError(w, http.StatusInternalServerError, "build upstream request")
		return
	}
	// Pass through the headers vmagent uses on the wire so
	// vminsert decodes the payload correctly (Snappy + protobuf).
	for _, k := range []string{
		"Content-Encoding",
		"Content-Type",
		"X-Prometheus-Remote-Write-Version",
	} {
		if v := r.Header.Get(k); v != "" {
			upstream.Header.Set(k, v)
		}
	}

	resp, err := metricsHTTPClient.Do(upstream)
	if err != nil {
		slog.Warn("remote_write upstream failed", slog.String("error", err.Error()))
		respondError(w, http.StatusBadGateway, "metrics storage unreachable")
		return
	}
	defer resp.Body.Close()

	// Forward the upstream response verbatim. vminsert returns 204 on
	// success; on 4xx it includes a small text body explaining the
	// rejection (cardinality limit, parse error, etc.) — we want the
	// client (vmagent) to see that so its retry policy makes sense.
	for k, vs := range resp.Header {
		// Only the headers a Prom client actually inspects. Skipping
		// hop-by-hop headers (Connection, Transfer-Encoding) is the
		// usual reverse-proxy hygiene.
		if k == "Content-Type" || k == "Content-Length" {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
	}
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		slog.Debug("remote_write response copy", slog.String("error", err.Error()))
	}
}
