package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
	"github.com/kubebolt/kubebolt/apps/api/internal/mcpcatalog"
	"github.com/kubebolt/kubebolt/apps/api/internal/mcpgateway"
	"github.com/kubebolt/kubebolt/apps/api/internal/settings"
)

// Admin API for the org's external MCP connectors:
//
//	GET    /admin/mcp-connectors            → {connectors: ConnectorView[]}
//	GET    /admin/mcp-connectors/{id}       → ConnectorView
//	PUT    /admin/mcp-connectors/{id}       → ConnectorView            (partial update)
//	POST   /admin/mcp-connectors/{id}/test  → TestResult               (stored config, or a draft over it)
//	DELETE /admin/mcp-connectors/{id}       → 204
//
// Mounted by the router for admins, outside requireConnector (a cloud account
// is not a cluster), with auditAdminRoutes recording route and outcome — never
// the body, which carries credentials. Every answer comes from the gateway,
// which masks secrets: nothing here reads or echoes one.

// mcpConnectorsMaxBody caps a request body. The largest legitimate one is a
// pasted Google service-account key, a few KiB.
const mcpConnectorsMaxBody = 64 << 10

const errConnectorsDisabled = "connectors are not enabled on this installation"

func (h *handlers) handleListMCPConnectors(w http.ResponseWriter, r *http.Request) {
	if h.mcpGateway == nil {
		respondError(w, http.StatusNotFound, errConnectorsDisabled)
		return
	}
	views, err := h.mcpGateway.Connectors(r.Context())
	if err != nil {
		respondMCPConnectorError(w, r, "list", err)
		return
	}
	if views == nil {
		views = []mcpgateway.ConnectorView{}
	}
	respondJSON(w, http.StatusOK, map[string]any{"connectors": views, "comingSoon": comingSoonConnectors()})
}

// comingSoonConnector is a connector that is built but not offered yet
// (mcpcatalog.Preview). The page lists it so it reads as next, not as never;
// nothing about it can be set up, so it carries no plan, mode or status.
type comingSoonConnector struct {
	ID          string              `json:"id"`
	Name        string              `json:"name"`
	Category    mcpcatalog.Category `json:"category"`
	Description string              `json:"description"`
	// Families are the labels of what it will read.
	Families []string `json:"families"`
}

func comingSoonConnectors() []comingSoonConnector {
	out := []comingSoonConnector{}
	for _, d := range mcpcatalog.Preview() {
		families := make([]string, 0, len(d.Families))
		for _, f := range d.Families {
			families = append(families, f.Label)
		}
		out = append(out, comingSoonConnector{ID: d.ID, Name: d.Name, Category: d.Category, Description: d.Description, Families: families})
	}
	return out
}

func (h *handlers) handleGetMCPConnector(w http.ResponseWriter, r *http.Request) {
	if h.mcpGateway == nil {
		respondError(w, http.StatusNotFound, errConnectorsDisabled)
		return
	}
	view, err := h.mcpGateway.Connector(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		respondMCPConnectorError(w, r, "get", err)
		return
	}
	respondJSON(w, http.StatusOK, view)
}

// handlePutMCPConnector applies a partial update. Secrets follow
// keep/clear/set: absent or null keeps, "" clears, a value sets — so the UI
// never has to send back a secret it cannot see. The body is the gateway's
// PutRequest as is, so a key the gateway adds (kobiMinRole, the connector's
// minimum role for Kobi) reaches it without a change here.
func (h *handlers) handlePutMCPConnector(w http.ResponseWriter, r *http.Request) {
	if h.mcpGateway == nil {
		respondError(w, http.StatusNotFound, errConnectorsDisabled)
		return
	}
	body, ok := readMCPConnectorBody(w, r)
	if !ok {
		return
	}
	var req mcpgateway.PutRequest
	if err := json.Unmarshal(body, &req); err != nil {
		// The decoder's message can quote the body, which may hold a secret.
		respondError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	id := chi.URLParam(r, "id")
	if req.ChangesNothing() {
		// Nothing to change: answer with the current view rather than write
		// an empty record for a connector the org never configured. Such a
		// record is not harmless — a later test without a draft would find a
		// "stored" configuration, fail it and pin its error on a connector
		// nobody set up. The gateway's own reading of the request decides,
		// so a key it learns (the role floor) is never mistaken for nothing.
		view, err := h.mcpGateway.Connector(r.Context(), id)
		if err != nil {
			respondMCPConnectorError(w, r, "get", err)
			return
		}
		respondJSON(w, http.StatusOK, view)
		return
	}
	view, err := h.mcpGateway.Put(r.Context(), id, req, mcpActor(r))
	if err != nil {
		respondMCPConnectorError(w, r, "save", err)
		return
	}
	respondJSON(w, http.StatusOK, view)
}

// handleTestMCPConnector tests the stored configuration (empty body or {}),
// or a draft merged over it and never saved. Only a test of the stored
// configuration records its outcome on the connector.
func (h *handlers) handleTestMCPConnector(w http.ResponseWriter, r *http.Request) {
	if h.mcpGateway == nil {
		respondError(w, http.StatusNotFound, errConnectorsDisabled)
		return
	}
	body, ok := readMCPConnectorBody(w, r)
	if !ok {
		return
	}
	var draft *mcpgateway.Draft
	if len(bytes.TrimSpace(body)) > 0 {
		draft = &mcpgateway.Draft{}
		if err := json.Unmarshal(body, draft); err != nil {
			respondError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
	}
	res, err := h.mcpGateway.Test(r.Context(), chi.URLParam(r, "id"), draft)
	if err != nil {
		respondMCPConnectorError(w, r, "test", err)
		return
	}
	respondJSON(w, http.StatusOK, res)
}

// handleDeleteMCPConnector removes the org's configuration of a connector,
// secrets included, and drops its live session. The values KubeBolt generated
// for the org (the AWS external ID) are kept, so setting the connector up
// again prints the same one and the customer's trust policy still matches.
func (h *handlers) handleDeleteMCPConnector(w http.ResponseWriter, r *http.Request) {
	if h.mcpGateway == nil {
		respondError(w, http.StatusNotFound, errConnectorsDisabled)
		return
	}
	if err := h.mcpGateway.Delete(r.Context(), chi.URLParam(r, "id")); err != nil {
		respondMCPConnectorError(w, r, "delete", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// readMCPConnectorBody reads a capped body. An oversized one is refused
// whole rather than parsed from a truncated prefix.
func readMCPConnectorBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, mcpConnectorsMaxBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			respondError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return nil, false
		}
		respondError(w, http.StatusBadRequest, "could not read the request body")
		return nil, false
	}
	return body, true
}

// mcpActor is who made an admin change, as recorded on the connector: the
// signed-in username, else the user id.
func mcpActor(r *http.Request) string {
	c := auth.ContextClaims(r)
	if c == nil {
		return ""
	}
	if c.Username != "" {
		return c.Username
	}
	return c.UserID
}

// respondMCPConnectorError maps the gateway's errors to the answers the UI
// reads.
func respondMCPConnectorError(w http.ResponseWriter, r *http.Request, op string, err error) {
	var (
		planErr *mcpgateway.PlanError
		rateErr *mcpgateway.RateLimitError
		valErr  *settings.ValidationError
	)
	switch {
	case errors.Is(err, mcpgateway.ErrNotFound):
		respondError(w, http.StatusNotFound, "unknown connector")
	case errors.As(err, &planErr):
		respondJSON(w, http.StatusConflict, map[string]any{
			"error":        "This connector is not included in your plan",
			"reason":       "requires_upgrade",
			"requiredPlan": planErr.RequiredPlan,
			"upgradeTo":    planErr.UpgradeTo,
		})
	case errors.As(err, &valErr):
		respondJSON(w, http.StatusBadRequest, map[string]any{
			"error":   "validation failed",
			"field":   valErr.Field,
			"message": valErr.Message,
		})
	case errors.As(err, &rateErr):
		secs := int(math.Ceil(rateErr.RetryAfter.Seconds()))
		if secs < 1 {
			secs = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		respondJSON(w, http.StatusTooManyRequests, map[string]any{
			"error":             "rate_limited",
			"retryAfterSeconds": secs,
		})
	case errors.Is(err, mcpgateway.ErrNoOrg):
		// ResolveTenant stamps an org on every authenticated request, so this
		// is a request that never went through it: unauthenticated, or
		// authenticated with no organization to act for.
		if auth.ContextClaims(r) == nil {
			respondError(w, http.StatusUnauthorized, "sign in to manage connectors")
			return
		}
		respondError(w, http.StatusForbidden, "no organization is selected for this request")
	default:
		slog.Error("mcp connectors admin request failed",
			slog.String("op", op),
			slog.String("connector", chi.URLParam(r, "id")),
			slog.String("error", err.Error()))
		respondError(w, http.StatusInternalServerError, "failed to "+op+" the connector")
	}
}

// ----- Kobi seams over the gateway -----
//
// The chat handler and the executors are built elsewhere; these keep the
// nil-gateway rule in one place so a disabled feature can never surface as a
// typed-nil interface that looks enabled.

// gatewayMCPRouter adapts the gateway to copilot.MCPRouter for Kobi.
type gatewayMCPRouter struct{ gw *mcpgateway.Gateway }

func (g gatewayMCPRouter) Call(ctx context.Context, call copilot.ToolCall) copilot.ToolResult {
	res := g.gw.Call(ctx, mcpcatalog.ConsumerKobi, call.Name, call.Input)
	res.ToolCallID = call.ID
	return res
}

// kobiMCPRouter is what Executor.WithMCP takes: nil (an untyped nil
// interface) when connectors are off.
func (h *handlers) kobiMCPRouter() copilot.MCPRouter {
	if h.mcpGateway == nil {
		return nil
	}
	return gatewayMCPRouter{gw: h.mcpGateway}
}

// kobiConnectorTools returns the org's connector tools for Kobi, sorted by
// connector and name. ctx must be the chat request's own: the gateway leaves
// out a connector whose minimum role is above the caller's, and reads that
// role from the request's claims. A failure costs the turn its external
// tools, never the turn itself.
func (h *handlers) kobiConnectorTools(ctx context.Context) []copilot.ToolDefinition {
	if h.mcpGateway == nil {
		return nil
	}
	defs, err := h.mcpGateway.ToolsFor(ctx, mcpcatalog.ConsumerKobi)
	if err != nil {
		slog.Warn("kobi: external connector tools unavailable for this turn", slog.String("error", err.Error()))
		return nil
	}
	return defs
}
