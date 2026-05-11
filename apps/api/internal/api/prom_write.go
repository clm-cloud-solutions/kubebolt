package api

import (
	"bytes"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/golang/snappy"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
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

// Promwrite enforcement modes — string constants chosen to match the
// agent gRPC channel's AuthEnforcement values so a single env-var
// document covers both surfaces. Empty / unrecognized falls back to
// "disabled" at the call site (parsed in main.go via the same
// agent.ParseEnforcement helper).
const (
	promWriteAuthDisabled   = "disabled"
	promWriteAuthPermissive = "permissive"
	promWriteAuthEnforced   = "enforced"
)

// authenticatePromWrite gates the remote_write endpoint with the
// same three-tier enforcement the agent's gRPC channel uses
// (Sprint A's AuthEnforcement). The mode is selected per-deployment
// via KUBEBOLT_REMOTE_WRITE_AUTH_MODE and passed through NewRouter:
//
//	disabled    bearer header IGNORED. The endpoint accepts every
//	            request that passes the env-var gate. Sprint A
//	            migration default — keeps existing dev installs
//	            working while operators learn the feature.
//
//	permissive  bearer header OPTIONAL. If present, it is validated
//	            against TenantsStore; bad/missing bearers log a WARN
//	            (rate-limited via Once) and the call is allowed
//	            through with the synthetic identity. Same semantics
//	            as the gRPC permissive-fallback. Useful while
//	            rolling out tokens to an existing fleet.
//
//	enforced    bearer header REQUIRED. Missing/bad bearer → 401.
//	            Production posture for tagged releases. Backend
//	            startup fails loud if enforced is selected without
//	            a TenantsStore — silent acceptance would mask the
//	            misconfiguration.
//
// The bearer comparison goes through TenantsStore.LookupByToken
// which hashes the plaintext and compares against the stored hash
// (same constant-time semantics as the gRPC bearer auth). The
// subtle.ConstantTimeCompare guard at the entry is for early-
// rejection of empty bearers (avoids hitting the store) and is not
// security-load-bearing on its own.
//
// Returns the tenant the request authenticated as (or nil on
// "disabled" / permissive-fallback / no TenantsStore wired) and a
// boolean indicating whether the caller may continue. On false the
// response has already been written.
//
// Day 3 of Phase 3 (rate limiting) requires the resolved tenant to
// look up per-tenant overrides — the bool-only signature of the
// prior version threw that information away. Permissive-fallback
// returns (nil, true) which means "use fleet defaults" downstream:
// without a verified tenant identity, the conservative posture is
// to apply the system-wide limits as if the request belonged to an
// anonymous bucket.
func (h *handlers) authenticatePromWrite(w http.ResponseWriter, r *http.Request) (*auth.Tenant, bool) {
	mode := h.promWriteAuthMode
	if mode == "" {
		mode = promWriteAuthDisabled
	}

	// disabled: ignore the header entirely. No WARN (the operator
	// asked for this mode explicitly).
	if mode == promWriteAuthDisabled {
		return nil, true
	}

	// Configuration sanity: enforced mode without a TenantsStore is
	// a misconfiguration we caught at startup, but defend in depth
	// — accepting silently here would defeat the point of enforced.
	// permissive without a store is fine: there's nothing to
	// validate against, so the WARN-and-accept branch fires.
	if h.tenantsStore == nil {
		if mode == promWriteAuthEnforced {
			respondError(w, http.StatusInternalServerError, "remote_write auth enforced but TenantsStore not wired")
			return nil, false
		}
		promWriteUnauthWarnOnce.Do(func() {
			slog.Warn("prom remote_write permissive but TenantsStore not wired — calls allowed without validation",
				slog.String("hint", "set KUBEBOLT_AUTH_ENABLED=true to enable token validation"))
		})
		return nil, true
	}

	authz := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(authz, prefix) {
		if mode == promWriteAuthPermissive {
			slog.Warn("prom remote_write permissive-fallback: missing bearer",
				slog.String("remote", r.RemoteAddr))
			return nil, true
		}
		respondError(w, http.StatusUnauthorized, "missing Bearer token")
		return nil, false
	}
	token := strings.TrimSpace(authz[len(prefix):])
	if subtle.ConstantTimeCompare([]byte(token), []byte("")) == 1 {
		if mode == promWriteAuthPermissive {
			slog.Warn("prom remote_write permissive-fallback: empty bearer",
				slog.String("remote", r.RemoteAddr))
			return nil, true
		}
		respondError(w, http.StatusUnauthorized, "empty Bearer token")
		return nil, false
	}
	tenant, _, err := h.tenantsStore.LookupByToken(token)
	if err != nil {
		if mode == promWriteAuthPermissive {
			slog.Warn("prom remote_write permissive-fallback: bad bearer",
				slog.String("remote", r.RemoteAddr),
				slog.String("error", err.Error()))
			return nil, true
		}
		respondError(w, http.StatusUnauthorized, "invalid ingest token")
		return nil, false
	}
	return tenant, true
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
	//
	// tenant may be nil under disabled / permissive-fallback / no-
	// TenantsStore paths; downstream rate limit treats nil as the
	// "anonymous" bucket which uses fleet defaults. That's the same
	// posture every unauthenticated client falls under.
	tenant, ok := h.authenticatePromWrite(w, r)
	if !ok {
		// Auth rejections all bucket as "auth" — granularity of
		// missing/empty/invalid bearer lives in the logs; the
		// /metrics surface stays at the "is auth failing at all?"
		// level. Anonymous tenant label since we couldn't resolve
		// the request to a real tenant.
		h.promWriteMetrics.RecordRequest(PromWriteAnonymousTenant, PromWriteStatusRejectedAuth)
		return
	}

	// Tenant label for metrics — "anonymous" when auth was disabled
	// or permissive-fallback (no resolved tenant). Same synthetic
	// identity the rate limiter uses, kept consistent so dashboard
	// queries can join cleanly.
	tenantID := PromWriteAnonymousTenant
	if tenant != nil {
		tenantID = tenant.ID
	}

	// Cap the body. We read the whole thing into a buffer so the
	// upstream request can set Content-Length explicitly — vminsert
	// rejects chunked transfer with no length on /api/v1/write
	// in some configurations.
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, promWriteMaxBodyBytes))
	if err != nil {
		var maxErr *http.MaxBytesError
		if asErr := err.Error(); strings.Contains(asErr, "http: request body too large") {
			h.promWriteMetrics.RecordRequest(tenantID, PromWriteStatusRejectedBodySize)
			respondError(w, http.StatusRequestEntityTooLarge, "remote_write body exceeds limit")
			return
		}
		_ = maxErr // silence unused if go version doesn't expose the typed error
		h.promWriteMetrics.RecordRequest(tenantID, PromWriteStatusRejectedMalformed)
		respondError(w, http.StatusBadRequest, "read remote_write body")
		return
	}

	// Decode + scan (Phase 3 Day 3 + 4). We decode the snappy body
	// ONCE here and reuse the decoded bytes for:
	//   - sample counting (rate limit gate)
	//   - tenant_id label validation (Day 4 anti-spoof)
	//   - auto-stamp fallback (Day 4, when tenant_id is missing)
	// nil rate limiter == feature disabled (transitional envs);
	// without it we skip all the per-tenant logic and just forward.
	var decoded []byte
	// sampleCount is captured outside the if-block so the success
	// path can pass it to RecordAcceptedSamples (the Day 5 billing
	// counter). Zero when the rate limiter isn't wired — that's the
	// transitional path, no metric emitted.
	var sampleCount int
	if h.promRateLimiter != nil {
		var dec []byte
		var parseErr error
		sampleCount, dec, parseErr = countWriteRequestSamples(body)
		if parseErr != nil {
			if errors.Is(parseErr, ErrPromWriteTooLarge) {
				h.promWriteMetrics.RecordRequest(tenantID, PromWriteStatusRejectedBodySize)
				respondError(w, http.StatusRequestEntityTooLarge, "remote_write payload too large after decompression")
				return
			}
			h.promWriteMetrics.RecordRequest(tenantID, PromWriteStatusRejectedMalformed)
			respondError(w, http.StatusBadRequest, "remote_write payload parse: "+parseErr.Error())
			return
		}
		decoded = dec

		// Rate limit gate (Day 3).
		var overrides *auth.TenantLimits
		if tenant != nil {
			overrides = tenant.Limits
		}
		if allowed, retryAfter := h.promRateLimiter.Allow(tenantID, overrides, sampleCount); !allowed {
			seconds := int(retryAfter.Round(time.Second) / time.Second)
			if seconds < 1 {
				seconds = 1
			}
			w.Header().Set("Retry-After", fmt.Sprintf("%d", seconds))
			h.promWriteMetrics.RecordRequest(tenantID, PromWriteStatusRejectedRateLimit)
			respondError(w, http.StatusTooManyRequests, "remote_write rate limit exceeded for tenant")
			return
		}

		// Tenant identity gate (Day 4): only when we have a real
		// authenticated tenant. permissive-fallback / disabled mode
		// (tenant == nil) ships samples unchanged — no label
		// validation possible without a known tenant identity.
		if tenant != nil {
			asserted, found := readTenantIDFromFirstSeries(decoded)
			if found && asserted != tenant.ID {
				// Anti-spoofing: client claimed a tenant_id that
				// doesn't match its bearer. Strict reject — no
				// permissive fallback for spoof attempts (the
				// mode doesn't matter for mismatches; this is an
				// active attack).
				slog.Warn("prom remote_write tenant_id mismatch",
					slog.String("asserted", asserted),
					slog.String("bearer_tenant", tenant.ID),
					slog.String("remote", r.RemoteAddr))
				h.promWriteMetrics.RecordRequest(tenantID, PromWriteStatusRejectedTenantMismatch)
				respondError(w, http.StatusUnauthorized, "tenant_id label does not match authenticated tenant")
				return
			}
			if !found {
				// Missing tenant_id label. Behavior is mode-sensitive
				// (Day 4.3): enforced mode rejects — operator must
				// configure tenant.id via helm at install. Permissive
				// auto-stamps as a transitional safety net for
				// legacy agents / external Prom installs that
				// haven't been migrated yet.
				if h.promWriteAuthMode == promWriteAuthEnforced {
					slog.Warn("prom remote_write tenant_id label missing in enforced mode",
						slog.String("bearer_tenant", tenant.ID),
						slog.String("remote", r.RemoteAddr))
					h.promWriteMetrics.RecordRequest(tenantID, PromWriteStatusRejectedTenantMissing)
					respondError(w, http.StatusUnauthorized,
						"tenant_id label required in enforced mode — set helm value `tenant.id` "+
							"on kubebolt-agent or `external_labels.tenant_id` on your external Prometheus")
					return
				}
				// Permissive mode auto-stamp. Day 4.2 makes the
				// agent stamp proactively, after which this path
				// becomes rare — used only for legacy installs
				// that haven't redeployed with `tenant.id` set.
				stamped, injErr := injectTenantID(decoded, tenant.ID)
				if injErr != nil {
					slog.Warn("prom remote_write tenant_id auto-stamp failed",
						slog.String("tenant_id", tenant.ID),
						slog.String("error", injErr.Error()))
					h.promWriteMetrics.RecordRequest(tenantID, PromWriteStatusInjectionFailed)
					respondError(w, http.StatusInternalServerError, "tenant_id injection failed")
					return
				}
				// Re-encode for the forward path. Original `body`
				// (snappy-compressed) is discarded.
				body = snappy.Encode(nil, stamped)
				// decoded is now stale but we don't read it again
				// past this point.
				decoded = stamped
			}

			// Cardinality gate (Day 4): VM-authoritative count vs
			// per-tenant cap. Permissive boot semantics inside
			// Allow() handle the not-yet-fresh-cache case.
			if h.promCardinality != nil {
				if allowed, retryAfter := h.promCardinality.Allow(tenant.ID, overrides); !allowed {
					seconds := int(retryAfter.Round(time.Second) / time.Second)
					if seconds < 1 {
						seconds = 3600
					}
					w.Header().Set("Retry-After", fmt.Sprintf("%d", seconds))
					h.promWriteMetrics.RecordRequest(tenantID, PromWriteStatusRejectedCardinality)
					respondError(w, http.StatusRequestEntityTooLarge, "tenant active series cardinality exceeded")
					return
				}
			}
		}
	}

	target, err := url.Parse(metricsStorageURL() + promWriteUpstreamPath)
	if err != nil {
		h.promWriteMetrics.RecordRequest(tenantID, PromWriteStatusUpstreamError)
		respondError(w, http.StatusInternalServerError, "invalid storage URL")
		return
	}

	upstream, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target.String(), bytes.NewReader(body))
	if err != nil {
		h.promWriteMetrics.RecordRequest(tenantID, PromWriteStatusUpstreamError)
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
		h.promWriteMetrics.RecordRequest(tenantID, PromWriteStatusUpstreamError)
		respondError(w, http.StatusBadGateway, "metrics storage unreachable")
		return
	}
	defer resp.Body.Close()

	// Success path observability (Day 5): record the accepted request
	// + the sample / byte counts now that we know the upstream
	// accepted them. Note we count the FORWARDED body bytes (post
	// auto-stamp, if any) — that's the bandwidth that actually moved.
	// upstream-non-2xx still counts as "accepted by us" because the
	// receiver did its job; vminsert's rejection (cardinality / etc.)
	// is its own dimension. The wire-back of upstream's body to the
	// client preserves the granular error.
	h.promWriteMetrics.RecordRequest(tenantID, PromWriteStatusAccepted)
	h.promWriteMetrics.RecordAcceptedSamples(tenantID, sampleCount, len(body))

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
