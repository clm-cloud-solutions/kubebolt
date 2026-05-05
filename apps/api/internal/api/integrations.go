package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/kubebolt/kubebolt/apps/api/internal/integrations"
)

// refuseProxyWithoutAuth is the agent-integration pre-flight that
// catches the most common config foot-gun before it reaches the
// cluster: proxy enabled, but the agent will dial the backend
// without credentials against an enforced auth mode. Without this
// the install / configure succeeds, the DaemonSet rolls, and the
// agent then crash-loops on a Welcome with "unknown auth mode" —
// confusing because the symptom (cluster never appears in the
// switcher) is far from the cause (auth mode mismatch).
//
// Only fires for the agent integration; other adapters get a
// no-op pass-through. Returns (errorMessage, false) when the body
// describes a misconfiguration; (_, true) means proceed.
func (h *handlers) refuseProxyWithoutAuth(id string, raw json.RawMessage) (string, bool) {
	if id != "agent" {
		return "", true
	}
	if h.agentAuthEnforcement != "enforced" {
		return "", true
	}
	if len(raw) == 0 {
		return "", true
	}
	// Surface-level peek — the provider re-unmarshals into the full
	// shape later. We only care about two fields, and tolerating
	// missing/extra fields keeps us in sync with provider evolution.
	var probe struct {
		ProxyEnabled *bool  `json:"proxyEnabled,omitempty"`
		AuthMode     string `json:"authMode,omitempty"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		// Bad JSON — let the provider produce the precise parser
		// error. Pre-flight is opportunistic, not a replacement.
		return "", true
	}
	if probe.ProxyEnabled != nil && *probe.ProxyEnabled && probe.AuthMode == "" {
		return "Backend is running with KUBEBOLT_AGENT_AUTH_MODE=enforced. Enable proxy together with an auth method (ingest-token or tokenreview) — agents that dial without credentials are rejected at the welcome handshake.", false
	}
	return "", true
}

// handleListIntegrations returns every registered integration with
// its current detected state against the active cluster. Read-only;
// safe for any authenticated role.
//
// Shape: []integrations.Integration — the UI renders one card per
// entry. Individual detection failures surface as StatusUnknown on
// that entry rather than failing the whole list, so a single broken
// adapter doesn't blank the page.
func (h *handlers) handleListIntegrations(w http.ResponseWriter, r *http.Request) {
	conn := h.manager.Connector()
	if conn == nil {
		// No cluster connected — surface the catalog with metadata
		// only so the user sees what's available. Status is the
		// neutral "not installed" rather than "unknown" because the
		// answer is definitive: there's nowhere to detect into.
		// The UI gates Install on cluster presence separately.
		out := make([]integrations.Integration, 0, len(h.integrations.IDs()))
		for _, id := range h.integrations.IDs() {
			p, _ := h.integrations.Get(id)
			meta := p.Meta()
			meta.Status = integrations.StatusNotInstalled
			meta.Health = &integrations.Health{Message: "No cluster connected"}
			out = append(out, meta)
		}
		respondJSON(w, http.StatusOK, out)
		return
	}
	out := h.integrations.List(r.Context(), conn.Clientset())
	respondJSON(w, http.StatusOK, out)
}

// handleGetIntegration returns the detail for a single integration.
// Identical payload shape to the list endpoint — just scoped to one
// entry, and 404s on an unknown id.
func (h *handlers) handleGetIntegration(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	provider, ok := h.integrations.Get(id)
	if !ok {
		respondError(w, http.StatusNotFound, "integration not found")
		return
	}
	conn := h.manager.Connector()
	if conn == nil {
		// Mirror the list endpoint — surface metadata-only so the UI
		// can render the detail panel. Install actions stay gated by
		// the requireConnector group on the install routes.
		meta := provider.Meta()
		meta.Status = integrations.StatusNotInstalled
		meta.Health = &integrations.Health{Message: "No cluster connected"}
		respondJSON(w, http.StatusOK, meta)
		return
	}
	snap, err := provider.Detect(r.Context(), conn.Clientset())
	if err != nil {
		// Detection errors aren't HTTP errors — they're "we couldn't
		// tell" which is a state the UI needs to show. Mirror the
		// list-endpoint behavior: return Meta with StatusUnknown and
		// the error in Health.Message.
		meta := provider.Meta()
		meta.Status = integrations.StatusUnknown
		meta.Health = &integrations.Health{Message: err.Error()}
		respondJSON(w, http.StatusOK, meta)
		return
	}
	respondJSON(w, http.StatusOK, snap)
}

// handleInstallIntegration applies the manifests an integration
// needs to function against the active cluster. Admin-only — the
// router guards that. Returns:
//
//   200 OK   + the post-install detection snapshot
//   400 BadRequest  if the config payload is invalid (e.g. missing backendUrl)
//   404 NotFound    if the integration id isn't registered
//   405 MethodNotAllowed if the integration doesn't implement Installable
//   409 Conflict    if a resource exists but wasn't put there by us
//   500 ...         on any other error
func (h *handlers) handleInstallIntegration(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	provider, ok := h.integrations.Get(id)
	if !ok {
		respondError(w, http.StatusNotFound, "integration not found")
		return
	}
	installable, ok := provider.(integrations.Installable)
	if !ok {
		respondError(w, http.StatusMethodNotAllowed, "integration does not support install")
		return
	}
	conn := h.manager.Connector()
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}

	// Accept an empty body as "install with all defaults". Real
	// requests will include at least backendUrl, but we validate
	// that in the provider so the error message matches the
	// provider's own field names.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		respondError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	var raw json.RawMessage
	if len(body) > 0 {
		raw = json.RawMessage(body)
	}

	if msg, ok := h.refuseProxyWithoutAuth(id, raw); !ok {
		respondError(w, http.StatusBadRequest, msg)
		return
	}

	if err := installable.Install(r.Context(), conn.Clientset(), raw); err != nil {
		var conflict *integrations.ConflictError
		if errors.As(err, &conflict) {
			respondJSON(w, http.StatusConflict, map[string]interface{}{
				"error":     conflict.Error(),
				"conflict":  conflict,
			})
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Return the fresh state so the UI can update its card without
	// a follow-up GET.
	snap, _ := provider.Detect(r.Context(), conn.Clientset())
	respondJSON(w, http.StatusOK, snap)
}

// handleGetIntegrationConfig returns the current live config of an
// integration — the shape the Configure endpoint accepts back.
// Used by the UI to pre-populate the configure form so the operator
// sees what's actually running before editing.
//
//	200 OK   + provider-specific JSON config
//	404      integration id unknown
//	405      integration doesn't implement Configurable
//	409      integration exists but isn't managed by KubeBolt
//	503      cluster not connected / not installed
func (h *handlers) handleGetIntegrationConfig(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	provider, ok := h.integrations.Get(id)
	if !ok {
		respondError(w, http.StatusNotFound, "integration not found")
		return
	}
	configurable, ok := provider.(integrations.Configurable)
	if !ok {
		respondError(w, http.StatusMethodNotAllowed, "integration does not support configure")
		return
	}
	conn := h.manager.Connector()
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}
	cfg, err := configurable.GetConfig(r.Context(), conn.Clientset())
	if err != nil {
		var notInstalled *integrations.NotInstalledError
		var notManaged *integrations.NotManagedError
		switch {
		case errors.As(err, &notInstalled):
			respondError(w, http.StatusServiceUnavailable, err.Error())
		case errors.As(err, &notManaged):
			respondJSON(w, http.StatusConflict, map[string]interface{}{
				"error":      err.Error(),
				"notManaged": notManaged,
			})
		default:
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	// cfg is already JSON — stream it through verbatim.
	w.Header().Set("Content-Type", "application/json")
	// Configure-time warning: if the integration being configured is
	// the agent AND the active cluster reaches its apiserver via
	// THIS agent's proxy, the rolling restart of the DaemonSet that
	// happens after Configure will briefly drop our session. Tell
	// the UI so it can render a banner instead of letting the user
	// click Save and watch the connection error mid-rollout.
	if id == "agent" {
		if proxyID := h.manager.ActiveAgentProxyClusterID(); proxyID != "" {
			w.Header().Set("X-Self-Targeted-Proxy", proxyID)
		}
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(cfg)
}

// handlePutIntegrationConfig applies a full new config to an
// existing managed install. Not a partial patch — the UI reads the
// current config via GET, edits in place, and PUTs the whole thing.
// This keeps the semantics simple (no merging) and matches the
// Install shape exactly.
func (h *handlers) handlePutIntegrationConfig(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	provider, ok := h.integrations.Get(id)
	if !ok {
		respondError(w, http.StatusNotFound, "integration not found")
		return
	}
	configurable, ok := provider.(integrations.Configurable)
	if !ok {
		respondError(w, http.StatusMethodNotAllowed, "integration does not support configure")
		return
	}
	conn := h.manager.Connector()
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		respondError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	if msg, ok := h.refuseProxyWithoutAuth(id, json.RawMessage(body)); !ok {
		respondError(w, http.StatusBadRequest, msg)
		return
	}
	if err := configurable.Configure(r.Context(), conn.Clientset(), json.RawMessage(body)); err != nil {
		// Log the full error before mapping to HTTP status — keeps
		// the wrapped chain visible (errors.As-discriminated 4xx
		// codes still go to operators, but the underlying cause
		// stays in the server log for diagnosis).
		slog.Error("integration Configure failed",
			slog.String("integration", id),
			slog.String("error", err.Error()),
		)
		var notInstalled *integrations.NotInstalledError
		var notManaged *integrations.NotManagedError
		switch {
		case errors.As(err, &notInstalled):
			respondError(w, http.StatusServiceUnavailable, err.Error())
		case errors.As(err, &notManaged):
			respondJSON(w, http.StatusConflict, map[string]interface{}{
				"error":      err.Error(),
				"notManaged": notManaged,
			})
		default:
			respondError(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	// Return the fresh snapshot so the UI updates without a
	// follow-up GET — same shape as Install's success response.
	snap, _ := provider.Detect(r.Context(), conn.Clientset())
	respondJSON(w, http.StatusOK, snap)
}

// handleUninstallIntegration removes the resources an integration
// owns. Only touches resources labeled as managed-by=kubebolt —
// external Helm installs are left alone.
func (h *handlers) handleUninstallIntegration(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	provider, ok := h.integrations.Get(id)
	if !ok {
		respondError(w, http.StatusNotFound, "integration not found")
		return
	}
	installable, ok := provider.(integrations.Installable)
	if !ok {
		respondError(w, http.StatusMethodNotAllowed, "integration does not support uninstall")
		return
	}
	conn := h.manager.Connector()
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}
	// `force=true` bypasses the managed-by safety check so admins
	// can remove agents installed via helm / kubectl without
	// exiting the UI. The wizard always surfaces this as an
	// explicit opt-in — never defaulted.
	force := r.URL.Query().Get("force") == "true"

	// Self-DoS guard: when the agent integration is being uninstalled
	// AND the active cluster is reached via THIS agent's proxy, we
	// would sever the only path to the cluster mid-action. Refuse
	// with 409 unless the operator has explicitly confirmed.
	// The UI surfaces this as a typed-name confirmation modal that
	// flips force=true on submit.
	if id == "agent" && !force {
		if proxyID := h.manager.ActiveAgentProxyClusterID(); proxyID != "" {
			respondJSON(w, http.StatusConflict, map[string]interface{}{
				"error":              "Refusing to uninstall the agent that backs the active cluster session — this would make the cluster unreachable from KubeBolt.",
				"selfTargetedProxy":  true,
				"activeContext":      h.manager.ActiveContext(),
				"proxyClusterId":     proxyID,
				"hint":               "Switch to a different cluster context before uninstalling, or pass force=true with a typed-name confirmation if you have an alternate path to this cluster.",
			})
			return
		}
	}

	opts := integrations.UninstallOptions{
		Force: force,
	}
	if err := installable.Uninstall(r.Context(), conn.Clientset(), opts); err != nil {
		// "Not managed by KubeBolt" is an operator-actionable
		// state, not a server error — map to 409 so the UI can
		// render the "confirm force uninstall" flow inline.
		var notManaged *integrations.NotManagedError
		if errors.As(err, &notManaged) {
			respondJSON(w, http.StatusConflict, map[string]interface{}{
				"error":      err.Error(),
				"notManaged": notManaged,
			})
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	snap, _ := provider.Detect(r.Context(), conn.Clientset())
	respondJSON(w, http.StatusOK, snap)
}

