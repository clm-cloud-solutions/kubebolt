package mcpgateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/kubebolt/kubebolt/apps/api/internal/audit"
	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
	"github.com/kubebolt/kubebolt/apps/api/internal/mcp/wire"
	"github.com/kubebolt/kubebolt/apps/api/internal/mcpcatalog"
	"github.com/kubebolt/kubebolt/apps/api/internal/mcpclient"
)

// Call results, used as the metric label and in the audit params. The audit
// record's own Result stays "success" | "error", as the trail expects.
const (
	resultOK           = "ok"
	resultToolError    = "tool_error"
	resultDenied       = "denied"
	resultRateLimited  = "rate_limited"
	resultBusy         = "busy"
	resultUnavailable  = "unavailable"
	resultUnauthorized = "unauthorized"
	resultThrottled    = "provider_rate_limited"
	resultTimeout      = "timeout"
	resultCanceled     = "canceled"
	resultError        = "error"
)

// AuditAction is the audit action of every external tool call.
const AuditAction = "mcp_tool_call"

// ToolsFor returns the external tools the org in ctx exposes to a consumer,
// under their model-facing names, sorted by (connector, name). For Kobi it
// leaves out the connectors whose role floor is above the caller's role.
// Connectors that cannot be bound right now are skipped (and logged), never
// fatal: the assistant works without them. It waits at most BindTimeout,
// binding connectors in parallel.
func (g *Gateway) ToolsFor(ctx context.Context, consumer mcpcatalog.Consumer) ([]copilot.ToolDefinition, error) {
	org, err := orgOf(ctx)
	if err != nil {
		return nil, err
	}
	if !validConsumer(consumer) {
		return nil, fmt.Errorf("mcpgateway: unknown consumer %q", consumer)
	}
	targets, err := g.usableConnectors(ctx, org, consumer)
	if err != nil {
		return nil, err
	}
	per := make([][]copilot.ToolDefinition, len(targets))
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func(i int, t target) {
			defer wg.Done()
			per[i] = g.toolsOf(ctx, org, consumer, t)
		}(i, t)
	}
	wg.Wait()

	var out []copilot.ToolDefinition
	for _, defs := range per {
		// Targets are already in connector order; within one, by name. A
		// reordering would rewrite the prompt cache on every request.
		sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
		out = append(out, defs...)
	}
	return out, nil
}

// toolsOf binds one connector and lists its tools for a consumer.
func (g *Gateway) toolsOf(ctx context.Context, org string, consumer mcpcatalog.Consumer, t target) []copilot.ToolDefinition {
	e, err := g.acquire(ctx, org, t)
	if err != nil {
		// A fresh bind failure was logged by bind; this is a skip.
		g.log.Debug("mcp gateway: connector skipped for this request",
			slog.String("org", org), slog.String("connector", t.def.ID), slog.String("error", shortRedacted(err.Error(), 200)))
		return nil
	}
	defer g.release(e)
	specs, ok := safeTools(e.b, consumer)
	if !ok {
		g.log.Warn("mcp gateway: connector failed to list its tools", slog.String("connector", t.def.ID))
		return nil
	}
	out := make([]copilot.ToolDefinition, 0, len(specs))
	seen := make(map[string]bool, len(specs))
	for _, s := range specs {
		if err := mcpcatalog.ValidateLocalName(t.def.ID, s.Name); err != nil {
			// A catalog bug, caught by the catalog's own tests; never
			// expose a name the model API would reject on every request.
			g.log.Warn("mcp gateway: dropping a tool with an invalid name", slog.String("connector", t.def.ID), slog.String("error", err.Error()))
			continue
		}
		if seen[s.Name] {
			continue
		}
		seen[s.Name] = true
		schema := s.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, copilot.ToolDefinition{
			Name:        mcpcatalog.ToolName(consumer, t.def.ID, s.Name),
			Description: s.Description,
			InputSchema: schema,
		})
	}
	return out
}

func validConsumer(c mcpcatalog.Consumer) bool {
	return c == mcpcatalog.ConsumerKobi || c == mcpcatalog.ConsumerAutopilot
}

// hasTool reports whether the binding lists local for the consumer right now.
func hasTool(b mcpcatalog.Binding, consumer mcpcatalog.Consumer, id, local string) bool {
	if mcpcatalog.ValidateLocalName(id, local) != nil {
		return false
	}
	specs, ok := safeTools(b, consumer)
	if !ok {
		return false
	}
	for _, s := range specs {
		if s.Name == local {
			return true
		}
	}
	return false
}

// callRecord accumulates what the audit record and the metrics say about one
// call. It never holds arguments or response content.
type callRecord struct {
	org       string
	consumer  mcpcatalog.Consumer
	connector string // catalog id, "" when the name did not resolve to one
	tool      string // local name as requested (capped), for the audit
	listed    bool   // the tool is in the org's current set
	result    string
	reason    string // short, KubeBolt-authored
	called    bool   // the binding was invoked
	bytes     int
	truncated bool
	redacted  int
	extra     map[string]any // the binding's CallResult.Audit
}

// Call runs one external tool call for the org in ctx. It never panics and
// never returns a Go error: every refusal and failure is an IsError result
// phrased for the model, and every call — refused ones included — is audited
// without its arguments or its response.
func (g *Gateway) Call(ctx context.Context, consumer mcpcatalog.Consumer, name string, args json.RawMessage) (out copilot.ToolResult) {
	start := g.now()
	rec := &callRecord{org: auth.TenantIDFromContext(ctx), consumer: consumer, result: resultError}
	defer func() {
		if p := recover(); p != nil {
			g.log.Error("mcp gateway: tool call panicked", slog.String("connector", rec.connector), slog.String("tool", rec.tool), slog.String("panic", fmt.Sprintf("%T", p)))
			out = copilot.ToolResult{Content: "The external tool failed inside KubeBolt. Continue without it.", IsError: true}
			rec.result, rec.reason = resultError, "internal error"
		}
		g.finishCall(ctx, rec, g.now().Sub(start))
	}()

	id, local, parsed := mcpcatalog.ParseToolName(consumer, name)
	rec.tool = cutRunes(local, 64, "")
	if !parsed {
		// Nothing to route: record what was asked for, bounded.
		rec.tool = cutRunes(name, 64, "")
	}
	if !parsed || rec.org == "" || !validConsumer(consumer) {
		return g.deny(rec, name)
	}
	def, ok := g.cat.Get(id)
	if !ok {
		return g.deny(rec, name)
	}
	rec.connector = def.ID

	// Recompute the org's current set for this connector: enabled, not
	// paused, open to this caller, entitled, usable — exactly what ToolsFor
	// would list now.
	all, err := g.st.MCP(ctx)
	if err != nil {
		g.log.Warn("mcp gateway: could not read connector settings", slog.String("org", rec.org), slog.String("error", err.Error()))
		rec.result, rec.reason = resultUnavailable, "settings unavailable"
		return errResult(fmt.Sprintf("The %s connector is unavailable right now. Continue without it.", def.Name))
	}
	cfg, configured := all.Connectors[def.ID]
	if !configured || !cfg.Enabled || cfg.Paused {
		return g.deny(rec, name)
	}
	if !reachable(ctx, consumer, cfg) {
		// A viewer's conversation cannot name its way past the floor: the
		// tool was never listed for it, and the refusal is on the trail.
		res := g.deny(rec, name)
		rec.reason = "role below the connector's minimum for Kobi"
		return res
	}
	if entitled, _ := g.entitlement(g.planOf(rec.org), def); !entitled {
		return g.deny(rec, name)
	}
	mode, prob := g.readiness(def, cfg)
	if prob != nil {
		return g.deny(rec, name)
	}
	e, err := g.acquire(ctx, rec.org, target{def: def, cfg: cfg, mode: mode})
	if errors.Is(err, context.Canceled) {
		rec.result, rec.reason = resultCanceled, "canceled"
		return errResult("The call was canceled.")
	}
	if err != nil {
		rec.result, rec.reason = resultUnavailable, "connector unavailable"
		return errResult(fmt.Sprintf("The %s connector is unavailable right now. Continue without it.", def.Name))
	}
	defer g.release(e)
	if !hasTool(e.b, consumer, def.ID, local) {
		return g.deny(rec, name)
	}
	rec.listed = true

	if !g.allowCall(rec.org) {
		rec.result, rec.reason = resultRateLimited, "rate limited"
		return errResult("Rate limited, try again later: this organization made too many external tool calls in the last minute.")
	}

	callCtx, cancel := context.WithTimeout(ctx, g.o.CallTimeout)
	defer cancel()
	select {
	case e.sem <- struct{}{}:
	case <-callCtx.Done():
		if errors.Is(ctx.Err(), context.Canceled) {
			// The turn was stopped while waiting: that is not the
			// connector being busy, and the audit should not say so.
			rec.result, rec.reason = resultCanceled, "canceled"
			return errResult("The call was canceled.")
		}
		rec.result, rec.reason = resultBusy, "too many concurrent calls"
		return errResult(fmt.Sprintf("The %s connector is busy with other calls from this organization. Try again shortly.", def.Name))
	}
	rec.called = true
	res, err := invoke(callCtx, e, consumer, local, args)

	if err != nil {
		if errors.Is(err, mcpclient.ErrUnauthorized) {
			// Cached credentials (STS, OAuth) could stay in use until they
			// expire: rebuild from the stored configuration next time.
			g.retire(e)
		}
		content, result := g.callError(def, local, err)
		rec.result, rec.reason = result, reasonFor(result)
		g.log.Info("mcp gateway: tool call failed",
			slog.String("org", rec.org), slog.String("connector", def.ID), slog.String("tool", local),
			slog.String("result", result), slog.String("error", shortRedacted(err.Error(), 300)))
		return errResult(content)
	}

	var (
		text       string
		truncated  bool
		redactions int
	)
	if res.Own {
		text = finishOwn(res.Text, res.Notes...)
	} else {
		text, truncated, redactions = finishOutput(def.ID, local, res.Text, res.Notes...)
	}
	rec.bytes, rec.truncated, rec.redacted, rec.extra = len(res.Text), truncated, redactions, res.Audit
	rec.result, rec.reason = resultOK, ""
	if res.IsError {
		rec.result, rec.reason = resultToolError, "the tool returned an error"
	}
	// Own is the binding's refusal, written before anything reached the
	// provider.
	return copilot.ToolResult{Content: text, IsError: res.IsError, NotSent: res.Own, Digest: res.Digest}
}

// errBindingPanic stands in for a binding that panicked mid-call.
var errBindingPanic = errors.New("mcpgateway: the connector failed while running the tool")

// invoke runs the binding's Call holding a concurrency slot, and gives the
// slot back even if the binding panics — a leaked slot would wedge the
// connector for the org until the binding is replaced.
func invoke(ctx context.Context, e *entry, consumer mcpcatalog.Consumer, local string, args json.RawMessage) (res mcpcatalog.CallResult, err error) {
	defer func() {
		<-e.sem
		if p := recover(); p != nil {
			res, err = mcpcatalog.CallResult{}, errBindingPanic
		}
	}()
	return e.b.Call(ctx, consumer, local, args)
}

// deny refuses a name that is not in the org's current tool set. Anthropic
// tool results carry no error flag, so the sentence itself says it.
func (g *Gateway) deny(rec *callRecord, name string) copilot.ToolResult {
	rec.result, rec.reason = resultDenied, "tool not allowed"
	res := errResult(fmt.Sprintf("Tool not allowed: %q is not available to this assistant in this organization. Do not call it again.", cutRunes(name, 96, "…")))
	res.NotSent = true
	return res
}

func errResult(content string) copilot.ToolResult {
	return copilot.ToolResult{Content: content, IsError: true}
}

func reasonFor(result string) string {
	switch result {
	case resultUnauthorized:
		return "credentials rejected"
	case resultThrottled:
		return "provider rate limited"
	case resultTimeout:
		return "timed out"
	case resultCanceled:
		return "canceled"
	}
	return "provider error"
}

// callError turns a binding error into a sentence for the model and a result
// label. The model gets KubeBolt's words; what a provider said is redacted,
// shortened and wrapped as external data.
func (g *Gateway) callError(def *mcpcatalog.ConnectorDef, local string, err error) (string, string) {
	var (
		httpErr    *mcpclient.HTTPError
		rpcErr     *wire.Error
		blockedErr *mcpclient.BlockedError
	)
	switch {
	case errors.Is(err, errBindingPanic):
		return "The external tool failed inside KubeBolt. Continue without it.", resultError
	case errors.Is(err, mcpclient.ErrUnauthorized):
		return fmt.Sprintf("%s rejected KubeBolt's credentials for this connector. Do not retry; tell the user an administrator should check the %s connector in Settings.", def.Name, def.Name), resultUnauthorized
	case errors.Is(err, mcpclient.ErrRateLimited):
		return fmt.Sprintf("%s is throttling requests right now. Wait before retrying, or continue without it.", def.Name), resultThrottled
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Sprintf("The %s call did not finish within %s. Try a narrower request or continue without it.", def.Name, g.o.CallTimeout.Round(time.Second)), resultTimeout
	case errors.Is(err, context.Canceled):
		return "The call was canceled.", resultCanceled
	case errors.As(err, &blockedErr):
		return fmt.Sprintf("KubeBolt refused to connect to the %s endpoint because its address is not allowed.", def.Name), resultError
	case errors.Is(err, mcpclient.ErrResponseTooLarge):
		return fmt.Sprintf("The %s response was too large to process. Ask for less.", def.Name), resultError
	case errors.As(err, &httpErr):
		return fmt.Sprintf("%s answered HTTP %d. Continue without it or try again later.", def.Name, httpErr.Status), resultError
	case errors.As(err, &rpcErr):
		detail, _, _ := finishOutput(def.ID, local, cutRunes(rpcErr.Message, 1024, "…"))
		return fmt.Sprintf("The %s server rejected the call:\n%s", def.Name, detail), resultError
	}
	detail, _, _ := finishOutput(def.ID, local, cutRunes(err.Error(), 1024, "…"))
	return fmt.Sprintf("The %s call failed:\n%s", def.Name, detail), resultError
}

// finishCall records the call in the audit trail and the metrics.
func (g *Gateway) finishCall(ctx context.Context, rec *callRecord, took time.Duration) {
	metricConnector, metricTool := rec.connector, rec.tool
	if metricConnector == "" {
		metricConnector = "unknown"
	}
	if !rec.listed {
		metricTool = "unlisted"
	}
	g.m.observe(metricConnector, metricTool, rec.result, took.Seconds(), rec.bytes, rec.called)

	if !audit.Enabled() {
		return
	}
	params := make(map[string]any, len(rec.extra)+7)
	for k, v := range rec.extra {
		params[k] = v
	}
	// Set after the binding's entries so they cannot be overridden.
	params["connector"] = rec.connector
	params["tool"] = rec.tool
	params["bytes"] = rec.bytes
	params["durationMs"] = took.Milliseconds()
	params["result"] = rec.result
	if rec.truncated {
		params["truncated"] = true
	}
	if rec.redacted > 0 {
		params["redactions"] = rec.redacted
	}
	r := &audit.Record{
		ID:         uuid.New().String(),
		Timestamp:  g.now().UTC(),
		TenantID:   rec.org,
		Class:      audit.ClassAccess,
		Source:     string(rec.consumer),
		Action:     AuditAction,
		TargetType: "mcp-connector",
		TargetName: rec.connector,
		Params:     params,
		Result:     "success",
	}
	if rec.result != resultOK {
		r.Result, r.Error = "error", rec.reason
	}
	if c := claimsFrom(ctx); c != nil {
		r.UserID, r.Username, r.Role = c.UserID, c.Username, string(c.Role)
	}
	audit.Emit(r)
}

// claimsFrom reads the caller's JWT claims from a request context. auth only
// exposes them through *http.Request; the gateway is always called with the
// request's own context, so wrapping it is enough.
func claimsFrom(ctx context.Context) *auth.Claims {
	return auth.ContextClaims((&http.Request{}).WithContext(ctx))
}
