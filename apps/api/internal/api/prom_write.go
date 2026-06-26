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
	"github.com/kubebolt/kubebolt/apps/api/internal/usage"
)

// promWriteEnabled returns true when the operator opted into the
// remote_write receiver via env var. Default false: Phase 2 stages
// the path without auth and we don't want to expose an unauthenticated
// metrics ingest port by accident on existing installs. Phase 3 will
// remove the gate and add bearer-token auth + rate limiting.
//
// Retained as a free function for tests + the fallback path in
// promWriteEnabledNow when the settings runtime isn't available.
func promWriteEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("KUBEBOLT_REMOTE_WRITE_ENABLED")))
	return v == "1" || v == "true" || v == "yes"
}

// promWriteEnabledNow is the per-request gate that prefers the
// settings runtime (UI-editable) over the env-only baseline. Spec
// #09 V2 — operators can flip the receiver on/off without redeploying
// the API. The runtime-resolved value already merged env baseline +
// BoltDB override at construction; we just read it here.
func (h *handlers) promWriteEnabledNow() bool {
	if h.settingsRuntime != nil {
		return h.settingsRuntime.IngestChannel().RemoteWriteEnabled
	}
	return promWriteEnabled()
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

// promWriteFallbackWarnOnce gates the per-request permissive-fallback
// WARNs (missing / empty / bad bearer) so they fire exactly once per
// process — same intent as promWriteUnauthWarnOnce above. Without
// this, a vmagent / external Prometheus shipping every 15s without a
// bearer fills the log with thousands of identical lines: the
// 5-day-in-vivo run saw 12,880 WARNs from one source. The ongoing
// signal lives in the metric kubebolt_prom_write_requests_total
// labeled tenant_id="anonymous" — every fallback bumps it whether the
// log is silent or not. Subsequent fallback events drop to DEBUG so
// they remain accessible via KUBEBOLT_LOG_LEVEL=debug when an
// operator is actively diagnosing.
var promWriteFallbackWarnOnce sync.Once

// logPromWriteFallback emits the first fallback per process at WARN
// (with a hint pointing at the metric) and every subsequent fallback
// at DEBUG. `variant` identifies which fallback path engaged so the
// metric labels and the log message stay consistent.
func logPromWriteFallback(variant, remote string, errMsg string) {
	promWriteFallbackWarnOnce.Do(func() {
		attrs := []any{
			slog.String("variant", variant),
			slog.String("remote", remote),
			slog.String("hint", "ongoing fallback rate observable at kubebolt_prom_write_requests_total{tenant_id=\"anonymous\"}; subsequent fallbacks logged at DEBUG"),
		}
		if errMsg != "" {
			attrs = append(attrs, slog.String("error", errMsg))
		}
		slog.Warn("prom remote_write permissive-fallback engaged", attrs...)
	})
	attrs := []any{
		slog.String("variant", variant),
		slog.String("remote", remote),
	}
	if errMsg != "" {
		attrs = append(attrs, slog.String("error", errMsg))
	}
	slog.Debug("prom remote_write permissive-fallback", attrs...)
}

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
// resolveDefaultIngestTenant returns the install's default tenant so
// unauthenticated traffic in disabled/permissive modes is bucketed
// against the operator's custom overrides (rate limit, cardinality
// cap) instead of the synthetic "anonymous" bucket. Returns nil when
// TenantsStore is unwired (the unusual auth-disabled install) — the
// caller's downstream code then preserves the "anonymous" path.
//
// The default tenant is created by TenantsStore.ensureDefaultTenant
// at startup, so this lookup is cheap (a single BoltDB read of an
// indexed key) and effectively constant per process.
func (h *handlers) resolveDefaultIngestTenant() *auth.Tenant {
	if h.tenantsStore == nil {
		return nil
	}
	t, err := h.tenantsStore.GetDefaultTenant()
	if err != nil {
		// Should not happen in a healthy install — the default tenant
		// is seeded by ensureDefaultTenant at TenantsStore
		// construction. Log once so an operator who somehow ends up
		// in this state has a breadcrumb, then fall back to the
		// pre-fix "anonymous" bucket so writes still flow.
		promWriteDefaultTenantWarnOnce.Do(func() {
			slog.Warn("prom remote_write: could not resolve default tenant; falling back to anonymous bucket",
				slog.String("error", err.Error()))
		})
		return nil
	}
	return t
}

// promWriteDefaultTenantWarnOnce throttles the WARN emitted when the
// default tenant lookup fails. See resolveDefaultIngestTenant.
var promWriteDefaultTenantWarnOnce sync.Once

func (h *handlers) authenticatePromWrite(w http.ResponseWriter, r *http.Request) (*auth.Tenant, bool) {
	mode := h.promWriteAuthMode
	if mode == "" {
		mode = promWriteAuthDisabled
	}

	// disabled: ignore the header entirely. No WARN (the operator
	// asked for this mode explicitly).
	//
	// When TenantsStore is wired, we STILL resolve to the default
	// tenant so the rate limiter + metric labels see a real tenant
	// UUID rather than the "anonymous" synthetic. The operator's
	// custom overrides on the default tenant apply to all
	// unauthenticated traffic out of the box. Without this, custom
	// limits set via the UI on the default tenant would be dead code
	// in the typical OSS install (auth enabled at the user-login
	// layer, disabled/permissive at the ingest layer).
	if mode == promWriteAuthDisabled {
		return h.resolveDefaultIngestTenant(), true
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
		// No store → cannot resolve default; preserve the
		// "anonymous" path. This branch only fires in the unusual
		// configuration where KUBEBOLT_AUTH_ENABLED=false.
		return nil, true
	}

	authz := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(authz, prefix) {
		if mode == promWriteAuthPermissive {
			logPromWriteFallback("missing-bearer", r.RemoteAddr, "")
			return h.resolveDefaultIngestTenant(), true
		}
		respondError(w, http.StatusUnauthorized, "missing Bearer token")
		return nil, false
	}
	token := strings.TrimSpace(authz[len(prefix):])
	if subtle.ConstantTimeCompare([]byte(token), []byte("")) == 1 {
		if mode == promWriteAuthPermissive {
			logPromWriteFallback("empty-bearer", r.RemoteAddr, "")
			return h.resolveDefaultIngestTenant(), true
		}
		respondError(w, http.StatusUnauthorized, "empty Bearer token")
		return nil, false
	}
	// Validate the token (ingest store), then gate on the owning tenant
	// (the tenant store owns Disabled now that tokens aren't inlined).
	tok, lookErr := h.ingestTokens.Lookup(r.Context(), token)
	var tenant *auth.Tenant
	if lookErr == nil {
		tenant, lookErr = h.tenantsStore.GetTenant(tok.TenantID)
		if lookErr == nil && tenant.Disabled {
			lookErr = auth.ErrTenantDisabled
		}
	}
	if lookErr != nil {
		if mode == promWriteAuthPermissive {
			logPromWriteFallback("bad-bearer", r.RemoteAddr, lookErr.Error())
			return h.resolveDefaultIngestTenant(), true
		}
		respondError(w, http.StatusUnauthorized, "invalid ingest token")
		return nil, false
	}
	// Mark the token recently used so the Prometheus integration card can
	// resolve "is this Prom currently pushing?". Debounced internally —
	// high-rate ingest doesn't pound BoltDB; a miss isn't worth failing on.
	_ = h.ingestTokens.MarkUsed(r.Context(), tok.TenantID, tok.ID, time.Now())
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

	// Spec #09 V2 — UI override wins over env. When the settings
	// runtime is wired, it returns the resolved enabled flag (env
	// baseline + BoltDB override). Falls back to the free-function
	// env-only read when the runtime isn't available (auth disabled).
	if !h.promWriteEnabledNow() {
		respondError(w, http.StatusNotFound, "remote_write receiver disabled — enable it via Settings → Ingest, or set KUBEBOLT_REMOTE_WRITE_ENABLED=true")
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

	// W1 metering: record the billable samples through the usage seam. OSS's
	// no-op makes this free; EE's impl buffers it toward the monthly roll-up.
	// Distinct from the Prometheus counter above (ephemeral observability) —
	// this is the durable, invoice-reconciling signal. nil-guarded for raw
	// test fixtures that build handlers without the seam.
	if h.usage != nil && sampleCount > 0 {
		_ = h.usage.Record(r.Context(), usage.UsageRecord{
			TenantID: tenantID,
			Metric:   usage.MetricSamplesIngested,
			Quantity: int64(sampleCount),
			At:       time.Now(),
		})
	}

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
