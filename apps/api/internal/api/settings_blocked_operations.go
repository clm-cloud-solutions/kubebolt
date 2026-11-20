package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/mcpcatalog"
	"github.com/kubebolt/kubebolt/apps/api/internal/settings"
)

// Blocked operations (debt-09) — the platform's emergency list of cloud
// connector operations no call may run, edited without a release. Mounted in
// the platform-admin /admin/settings group; a PUT applies on the next call.

// blockedOperationsView is the GET/PUT shape: the stored lists plus the
// connectors this install has, which the form offers.
type blockedOperationsView struct {
	Connectors map[string][]string `json:"connectors"`
	// Available lists the connectors this install registers, with their names.
	Available []blockableConnector `json:"available"`
}

type blockableConnector struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (h *handlers) blockedOperationsView(list settings.BlockedOperations) blockedOperationsView {
	view := blockedOperationsView{Connectors: list.Connectors, Available: []blockableConnector{}}
	if view.Connectors == nil {
		view.Connectors = map[string][]string{}
	}
	blockable := map[string]bool{}
	for _, id := range settings.BlockableConnectors {
		blockable[id] = true
	}
	if h.mcpCatalog != nil {
		for _, d := range h.mcpCatalog.List() {
			if blockable[d.ID] {
				view.Available = append(view.Available, blockableConnector{ID: d.ID, Name: d.Name})
			}
		}
	}
	sort.Slice(view.Available, func(i, j int) bool { return view.Available[i].ID < view.Available[j].ID })
	return view
}

func (h *handlers) handleGetSettingsBlockedOperations(w http.ResponseWriter, r *http.Request) {
	if h.settingsRuntime == nil {
		respondError(w, http.StatusServiceUnavailable, "settings runtime not available (persistence disabled)")
		return
	}
	list, err := h.settingsRuntime.BlockedOperations()
	if err != nil {
		// The gateway refuses every connector call while this fails; say so.
		slog.Error("settings blocked operations read failed", slog.String("error", err.Error()))
		respondError(w, http.StatusServiceUnavailable, "the blocked operations cannot be read: cloud connector calls are refused until they can")
		return
	}
	respondJSON(w, http.StatusOK, h.blockedOperationsView(list))
}

func (h *handlers) handlePutSettingsBlockedOperations(w http.ResponseWriter, r *http.Request) {
	if h.settingsRuntime == nil {
		respondError(w, http.StatusServiceUnavailable, "settings runtime not available (persistence disabled)")
		return
	}
	var body settings.BlockedOperations
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	list, err := h.putBlockedOperations(body)
	if err != nil {
		var ve *settings.ValidationError
		if errors.As(err, &ve) {
			respondJSON(w, http.StatusBadRequest, map[string]any{
				"error":   "validation failed",
				"field":   ve.Field,
				"message": ve.Message,
			})
			return
		}
		slog.Error("settings blocked operations put failed", slog.String("error", err.Error()))
		respondError(w, http.StatusInternalServerError, "failed to persist the blocked operations")
		return
	}
	if uid := auth.ContextUserID(r); uid != "" {
		// Which connectors changed and how many entries each has now; the
		// entries themselves are in the setting, not in the log.
		counts := map[string]int{}
		for conn := range body.Connectors {
			counts[conn] = len(list.Connectors[conn])
		}
		slog.Info("admin settings change",
			slog.String("domain", "blocked_operations"),
			slog.String("actor_id", uid),
			slog.String("source", "platform_settings_ui"),
			slog.Any("entries", counts),
		)
	}
	respondJSON(w, http.StatusOK, h.blockedOperationsView(list))
}

// putBlockedOperations stores the lists after checking what the store cannot
// see: a list for a connector this install does not register, and an entry
// that matches no name the connector gives a call (an AWS one without its
// service). Either would block nothing while the tab reads as if it did. An
// empty list always passes: it clears.
func (h *handlers) putBlockedOperations(body settings.BlockedOperations) (settings.BlockedOperations, error) {
	norm, err := settings.NormalizeBlockedOperations(body)
	if err != nil {
		return settings.BlockedOperations{}, err
	}
	conns := make([]string, 0, len(norm.Connectors))
	for conn := range norm.Connectors {
		conns = append(conns, conn)
	}
	sort.Strings(conns)
	for _, conn := range conns {
		var def *mcpcatalog.ConnectorDef
		if h.mcpCatalog != nil {
			def, _ = h.mcpCatalog.Get(conn)
		}
		if def == nil {
			return settings.BlockedOperations{}, &settings.ValidationError{Field: "connectors." + conn,
				Message: fmt.Sprintf("this installation has no %s connector: nothing would be blocked", conn)}
		}
		if def.BlockEntry == nil {
			continue
		}
		for _, e := range norm.Connectors[conn] {
			if msg := def.BlockEntry(e); msg != "" {
				return settings.BlockedOperations{}, &settings.ValidationError{Field: "connectors." + conn, Message: msg}
			}
		}
	}
	return h.settingsRuntime.PutBlockedOperations(body)
}
