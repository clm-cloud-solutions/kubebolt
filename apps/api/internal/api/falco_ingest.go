package api

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/findings"
	"github.com/kubebolt/kubebolt/apps/api/internal/integrations"
)

// Falco ingest (E2 SEC-E, Decision B — reversal criteria recorded in
// the E2 wave plan): falcosidekick POSTs one event per rule hit,
// authenticated with a bearer INGEST token — the same credential
// class /prom/write uses, STRICT mode only (runtime security events
// get no permissive legacy fallback). Tenant and cluster identity
// come from the TOKEN, never from the payload — the W3a anti-spoof
// stance: a compromised sender cannot write into another org's feed.
//
// falcosidekick values:
//
//	webhook:
//	  address: https://<kubebolt>/api/v1/ingest/falco
//	  customHeaders: "Authorization:Bearer <ingest-token>"

const falcoMaxBody = 1 << 20 // one event per POST; 1MiB is generous

func (h *handlers) handleFalcoIngest(w http.ResponseWriter, r *http.Request) {
	if h.eventStore == nil {
		respondError(w, http.StatusServiceUnavailable, "runtime events are not available (persistence disabled)")
		return
	}
	if h.ingestTokens == nil || h.tenantsStore == nil {
		respondError(w, http.StatusServiceUnavailable, "ingest auth is not configured")
		return
	}

	authz := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(authz, prefix) {
		respondError(w, http.StatusUnauthorized, "missing Bearer token")
		return
	}
	tok, err := h.ingestTokens.Lookup(r.Context(), strings.TrimSpace(authz[len(prefix):]))
	if err != nil {
		respondError(w, http.StatusUnauthorized, "invalid ingest token")
		return
	}
	tenant, err := h.tenantsStore.GetTenant(tok.TenantID)
	if err != nil || tenant.Disabled {
		respondError(w, http.StatusUnauthorized, "invalid ingest token")
		return
	}

	// The token MUST name a cluster. Falco has no handshake — unlike the agent,
	// which announces its cluster in a Hello, the token is the only identity a
	// pushed event ever carries. An unscoped one produces events that cannot say
	// which cluster they came from, and in a fleet that is not a cosmetic gap:
	// it is a security alert nobody can route.
	//
	// Rejecting rather than accepting-and-degrading is affordable BECAUSE this
	// door is new — nothing was shipping through it, so there is no installed
	// base to break — and it matches the stance this handler already declares:
	// STRICT only, no permissive legacy fallback for runtime security.
	//
	// The alternative considered and dropped: learn the binding from the agent's
	// Hello. It would have fixed existing installs with no operator action, but
	// writing a learned cluster into the token turns Sec #13 against it — a token
	// shared by two clusters, legitimate today, would start rejecting the second
	// agent on upgrade. A silent regression in someone else's cluster is a worse
	// trade than one explicit error here.
	if tok.ClusterID == "" {
		respondError(w, http.StatusForbidden,
			"this ingest token is not scoped to a cluster; issue a cluster-scoped token for Falco "+
				"so its events can be attributed")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, falcoMaxBody))
	if err != nil || len(body) == 0 {
		respondError(w, http.StatusBadRequest, "empty body")
		return
	}

	prov, ok := integrations.NewFalco().(integrations.SignalProvider)
	if !ok {
		respondError(w, http.StatusInternalServerError, "falco provider unavailable")
		return
	}
	sig, err := prov.Normalize(r.Context(), body)
	if err != nil {
		respondError(w, http.StatusBadRequest, "unrecognized falco payload")
		return
	}

	// Cross-check, mirroring Sec #13 on the agent door (agent/server.go): the
	// TOKEN is the authority and a cluster the sender asserts is only a claim, so
	// a disagreement is refused rather than resolved in the payload's favour.
	//
	// Be precise about what this buys: it does NOT stop an attacker, who holding
	// cluster A's token would simply assert A. It catches the mistake that
	// actually happens — pasting one cluster's token into another cluster's Falco
	// values, which otherwise files B's events under A in silence, forever.
	//
	// The assertion is OPTIONAL. falcosidekick carries it via `customfields`
	// (verified: it arrives in output_fields), and an install that has not set it
	// keeps working — same shape as the agent's empty ClusterHint.
	for i := range sig.Events {
		asserted := sig.Events[i].Fields["cluster_id"]
		if asserted != "" && asserted != tok.ClusterID {
			respondError(w, http.StatusForbidden,
				"cluster_id in the event does not match the token's cluster scope")
			return
		}
	}

	for i := range sig.Events {
		rec := &findings.EventRecord{
			// Deterministic id: a push source RETRIES, and falcosidekick reposts
			// byte-for-byte on any non-2xx or network error. With the default
			// random id every retry landed as a new row, so the unique index on
			// (tenant, cluster, id) never fired and an API blip silently doubled
			// both the feed and the 24h threat count.
			ID:       findings.EventIDFromPayload(sig.Events[i].At, body, i),
			TenantID: tok.TenantID,
			// Cluster identity: the token's binding when scoped at
			// issue-time; unscoped legacy tokens leave it empty (the
			// feed shows the hostname from the event fields instead).
			ClusterID:    tok.ClusterID,
			RuntimeEvent: sig.Events[i],
		}
		if err := h.eventStore.Append(rec); err != nil {
			respondError(w, http.StatusInternalServerError, "failed to store event")
			return
		}
	}
	w.WriteHeader(http.StatusAccepted)
}

const (
	// Feed page size. Runtime events are a stream, not a table — the panel
	// shows the recent tail, it does not paginate.
	defaultRuntimeEventLimit = 50
	// Ceiling for an explicit ?limit=. Falco can emit thousands of hits a day
	// per cluster, so an unbounded read would be a self-inflicted DoS on a
	// dashboard refresh.
	maxRuntimeEventLimit = 500
)

// handleListRuntimeEvents is the dashboard read: org-scoped, newest
// first. Accepts ?since= (Go duration like "24h", or RFC3339) and
// ?limit= (capped) so the panel can drive both its feed and its
// "threats in the last 24h" KPI off one endpoint.
func (h *handlers) handleListRuntimeEvents(w http.ResponseWriter, r *http.Request) {
	if h.eventStore == nil {
		respondError(w, http.StatusServiceUnavailable, "runtime events are not available (persistence disabled)")
		return
	}
	q := r.URL.Query()

	// `since` lets the dashboard ask "threats in the last 24h" for its KPI
	// instead of counting whatever happened to fit in the feed's page. The
	// store already understood the field; the handler just never read it.
	var since time.Time
	if v := q.Get("since"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			since = time.Now().UTC().Add(-d)
		} else if t, err := time.Parse(time.RFC3339, v); err == nil {
			since = t.UTC()
		}
		// Unparseable → zero time → no lower bound. A bad param must not
		// silently hide events; it degrades to the previous behaviour.
	}

	limit := defaultRuntimeEventLimit
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = min(n, maxRuntimeEventLimit)
		}
	}

	// Team narrowing, same resolver as /clusters and the metrics reads. A runtime
	// event carries the command line that ran, the file it touched and the user
	// it ran as — the most revealing payload in the pillar. See findings_scope.go.
	requestedCluster, mayRead := h.findingsClusterFilter(r, q.Get("cluster"))
	events, err := h.eventStore.ListEvents(findings.EventQuery{
		TenantID:  h.activeTenantID(r),
		ClusterID: requestedCluster,
		Source:    q.Get("source"),
		Priority:  q.Get("priority"),
		Since:     since,
		Limit:     limit,
	})
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to list runtime events")
		return
	}
	// Filtered AFTER the read because EventQuery takes one cluster, not a set.
	// The limit is applied by the store, so a caller entitled to a subset can see
	// fewer than `limit` rows — correct: the alternative is reading more of
	// somebody else's data to fill a page.
	visible := events[:0]
	for _, e := range events {
		if mayRead(e.ClusterID) {
			visible = append(visible, e)
		}
	}
	events = visible
	// `total` is the returned count, so the UI can tell "50 shown" from
	// "50 is everything" — it is capped by `limit`, not a scope-wide count.
	respondJSON(w, http.StatusOK, map[string]any{"events": events, "total": len(events)})
}

// auth import anchors the token-class documentation above.
var _ = auth.ModeIngestToken
