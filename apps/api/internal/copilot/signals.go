package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"strings"
)

// Health signals of a Kobi session (doc #67, phase 1). Every value below is a
// small closed set on purpose: they become metric labels in phase 1b, and an
// open set (a raw provider string, a tool's own error text) would turn into
// one series per spelling. Nothing here looks at what the user asked or what
// the model answered beyond the shape of a tool result.

// Normalized stop reasons — how one model call ended, across providers.
const (
	StopEndTurn   = "end_turn"
	StopToolUse   = "tool_use"
	StopMaxTokens = "max_tokens"
	StopRefusal   = "refusal"
	StopOther     = "other"
)

// NormalizeStopReason maps a provider's raw stop reason onto the closed set.
// Anthropic: end_turn, stop_sequence, tool_use, max_tokens,
// model_context_window_exceeded, refusal, pause_turn. OpenAI-compatible:
// stop, tool_calls, function_call, length, content_filter.
func NormalizeStopReason(raw string) string {
	switch raw {
	case "end_turn", "stop_sequence", "stop":
		return StopEndTurn
	case "tool_use", "tool_calls", "function_call":
		return StopToolUse
	case "max_tokens", "length", "model_context_window_exceeded":
		return StopMaxTokens
	case "refusal", "content_filter":
		return StopRefusal
	}
	return StopOther
}

// Provider error kinds — why one model call failed.
const (
	ProviderErrRateLimit  = "rate_limit"
	ProviderErrOverloaded = "overloaded"
	ProviderErrServer     = "server"
	ProviderErrTimeout    = "timeout"
	ProviderErrNetwork    = "network"
	ProviderErrAuth       = "auth"
	ProviderErrNotFound   = "not_found"
	ProviderErrBadRequest = "bad_request"
	ProviderErrOther      = "other"
)

// ClassifyProviderError names the kind of a failed model call. A cancelled
// request is not a provider failure and the caller must not count it: the
// http.Client reports it as a *url.Error just like a timeout, so check the
// request context first (see CallSignals.Observe).
func ClassifyProviderError(err error) string {
	if err == nil {
		return ""
	}
	var herr *ProviderHTTPError
	if errors.As(err, &herr) {
		switch {
		case herr.StatusCode == 429:
			return ProviderErrRateLimit
		// Anthropic answers an overload with 529 and an `overloaded_error`
		// body; some gateways relay it as a 503 with the same body.
		case herr.StatusCode == 529 || strings.Contains(herr.Body, "overloaded_error"):
			return ProviderErrOverloaded
		case herr.StatusCode == 408 || herr.StatusCode == 504:
			return ProviderErrTimeout
		case herr.StatusCode >= 500:
			return ProviderErrServer
		case herr.StatusCode == 401 || herr.StatusCode == 403:
			return ProviderErrAuth
		case herr.StatusCode == 404:
			return ProviderErrNotFound
		case herr.StatusCode >= 400:
			return ProviderErrBadRequest
		}
		return ProviderErrOther
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ProviderErrTimeout
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Timeout() {
		return ProviderErrTimeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return ProviderErrTimeout
		}
		return ProviderErrNetwork
	}
	return ProviderErrOther
}

// CallSignals accumulates how each model call of one session ended. Zero value
// ready to use; not safe for concurrent use (the chat loop is sequential).
type CallSignals struct {
	StopReasons    map[string]int
	ProviderErrors map[string]int
}

// Observe records one model call. canceled = the request context was already
// done when the call returned: the client went away, which is neither a stop
// reason nor a provider failure, so it is not counted.
func (s *CallSignals) Observe(resp *ChatResponse, err error, canceled bool) {
	switch {
	case canceled:
		return
	case err != nil:
		if s.ProviderErrors == nil {
			s.ProviderErrors = map[string]int{}
		}
		s.ProviderErrors[ClassifyProviderError(err)]++
	case resp != nil:
		if s.StopReasons == nil {
			s.StopReasons = map[string]int{}
		}
		s.StopReasons[NormalizeStopReason(resp.StopReason)]++
	}
}

// Tool result kinds — what one tool call gave back.
const (
	ToolResultOK       = "ok"
	ToolResultEmpty    = "empty"
	ToolResultNotFound = "not_found"
	ToolResultDenied   = "denied"
	ToolResultTimeout  = "timeout"
	ToolResultError    = "error"
)

// ClassifyToolResult names the kind of a tool result from its shape. The
// executor has no structured status: errors are `{"error": "..."}` with
// IsError set, an empty list is `{"items": [], "total": 0}`, an integration
// that is not installed says `"notInstalled": true`. Only the error message
// and the top-level shape are read — never the rest of the content.
func ClassifyToolResult(r ToolResult) string {
	content := strings.TrimSpace(r.Content)
	var obj map[string]any
	isObj := json.Unmarshal([]byte(content), &obj) == nil

	if r.IsError {
		return classifyToolError(toolErrorText(obj, isObj, content), obj)
	}
	if isObj {
		if msg, ok := obj["error"].(string); ok && msg != "" {
			return classifyToolError(msg, obj)
		}
		if b, _ := obj["notInstalled"].(bool); b {
			return ToolResultNotFound
		}
	}
	if isEmptyToolContent(content, obj, isObj) {
		return ToolResultEmpty
	}
	return ToolResultOK
}

func toolErrorText(obj map[string]any, isObj bool, content string) string {
	if isObj {
		if msg, ok := obj["error"].(string); ok {
			return msg
		}
	}
	return content
}

func classifyToolError(msg string, obj map[string]any) string {
	if b, _ := obj["forbidden"].(bool); b {
		return ToolResultDenied
	}
	m := strings.ToLower(msg)
	switch {
	case strings.Contains(m, "forbidden"), strings.Contains(m, "permission denied"),
		strings.Contains(m, "unauthorized"), strings.Contains(m, "access denied"):
		return ToolResultDenied
	case strings.Contains(m, "not found"), strings.Contains(m, "notfound"):
		return ToolResultNotFound
	case strings.Contains(m, "timeout"), strings.Contains(m, "timed out"),
		strings.Contains(m, "deadline exceeded"):
		return ToolResultTimeout
	}
	return ToolResultError
}

// isEmptyToolContent: nothing came back. An object counts as empty when it
// says so (`"total": 0`) or when every value in it is empty — `{"items": []}`,
// `{"logs": ""}`. An object with any non-empty value is a real answer.
func isEmptyToolContent(content string, obj map[string]any, isObj bool) bool {
	switch content {
	case "", "null", "[]", "{}", `""`:
		return true
	}
	if !isObj {
		var arr []any
		if json.Unmarshal([]byte(content), &arr) == nil {
			return len(arr) == 0
		}
		return false
	}
	if total, ok := obj["total"].(float64); ok {
		return total == 0
	}
	for _, v := range obj {
		if !isEmptyJSONValue(v) {
			return false
		}
	}
	return true
}

func isEmptyJSONValue(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	case float64:
		return x == 0
	case bool:
		return !x
	}
	return false
}

// ToolSourceKubeBolt is the source of every built-in tool. A tool served by an
// MCP connector is named mcp__<connector>__<tool> and its source is
// mcp:<connector> — the label that tells one org's broken cloud connector
// apart from KubeBolt's own tools.
const ToolSourceKubeBolt = "kubebolt"

// ToolSource returns the source label of a tool by its name.
func ToolSource(name string) string {
	rest, ok := strings.CutPrefix(name, "mcp__")
	if !ok {
		return ToolSourceKubeBolt
	}
	connector, _, found := strings.Cut(rest, "__")
	if !found || connector == "" {
		return ToolSourceKubeBolt
	}
	return "mcp:" + connector
}
