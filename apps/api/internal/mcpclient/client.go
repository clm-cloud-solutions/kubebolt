// Package mcpclient is KubeBolt's client for remote MCP servers over the
// Streamable HTTP transport: the AWS MCP Server, Google's per-service MCP
// endpoints and the Azure MCP Server sidecar.
//
// It speaks the small part of the protocol a read-only tool consumer needs —
// initialize, tools/list, tools/call, the initialized notification and the
// session DELETE — and nothing a server could use to reach back into KubeBolt:
// it advertises no client capabilities and never answers server-to-client
// requests.
//
// Every request is built in full, then signed (SigV4 hashes the body), then
// sent through a dial guard that refuses loopback, metadata and private
// addresses. Replies are read under byte caps and mapped status-first, because
// providers put different things in the body of the same failure.
package mcpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/mcp/wire"
)

// Signer authenticates one outgoing request. It runs after the final body is
// built — SigV4 hashes it — and again for every retry; body is nil for a
// request without one.
type Signer interface {
	Sign(ctx context.Context, req *http.Request, body []byte) error
}

const (
	// DefaultCallTimeout bounds one Initialize, ListTools or CallTool,
	// including a re-initialization after a lost session. The AWS server's
	// first authenticated call in a session is a 6–9 s cold start.
	DefaultCallTimeout = 30 * time.Second

	// DefaultMaxListBytes caps initialize and each tools/list page. Google's
	// catalogs reach 531 KB (sqladmin), mostly outputSchema.
	DefaultMaxListBytes int64 = 2 << 20

	// DefaultMaxCallBytes caps a tools/call reply. The gateway hands the
	// model far less (32 KiB); this bounds what is read at all.
	DefaultMaxCallBytes int64 = 256 << 10

	// CallTimeoutEnv overrides DefaultCallTimeout: a Go duration ("45s") or
	// a number of seconds ("45").
	CallTimeoutEnv = "KUBEBOLT_MCP_TOOL_TIMEOUT"

	// maxListPages bounds tools/list pagination against a server that never
	// stops handing out cursors.
	maxListPages = 10

	// maxSessionID bounds the Mcp-Session-Id we accept and echo.
	maxSessionID = 1024

	clientName    = "kubebolt"
	clientVersion = "1.0"
	userAgent     = "kubebolt-mcpclient/1.0"
)

// CallTimeoutFromEnv returns the tools/call timeout configured in
// KUBEBOLT_MCP_TOOL_TIMEOUT, or DefaultCallTimeout when it is unset or not a
// positive duration.
func CallTimeoutFromEnv() time.Duration {
	v := strings.TrimSpace(os.Getenv(CallTimeoutEnv))
	if v == "" {
		return DefaultCallTimeout
	}
	if d, err := time.ParseDuration(v); err == nil && d > 0 {
		return d
	}
	if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	return DefaultCallTimeout
}

// Option configures a Client.
type Option func(*Client)

// WithSigner authenticates every request (SigV4, Google bearer...).
func WithSigner(s Signer) Option { return func(c *Client) { c.signer = s } }

// WithHTTPClient replaces the guarded default client. The caller then owns
// dial safety and redirect policy: the DialGuard still checks the endpoint
// URL at New, but cannot reach into a transport it did not build — pass
// DialGuard.HTTPClient() to share a pool and keep the guard.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.httpc = h } }

// WithCallTimeout bounds each Initialize, ListTools and CallTool. Zero keeps
// the default (CallTimeoutFromEnv).
func WithCallTimeout(d time.Duration) Option { return func(c *Client) { c.callTimeout = d } }

// WithMaxListBytes caps initialize and each tools/list page.
func WithMaxListBytes(n int64) Option { return func(c *Client) { c.maxList = n } }

// WithMaxCallBytes caps a tools/call reply; a bigger one is truncated and
// flagged, not failed.
func WithMaxCallBytes(n int64) Option { return func(c *Client) { c.maxCall = n } }

// WithMeta supplies request metadata merged into params._meta of every
// request (never notifications). Keys the params already carry win. The AWS
// MCP Server reads the target region from _meta.AWS_REGION.
func WithMeta(f func() map[string]any) Option { return func(c *Client) { c.meta = f } }

// WithHeaders adds headers to every request. The protocol headers
// (Content-Type, Accept, Mcp-Session-Id, MCP-Protocol-Version) and the
// signer's always win over them.
func WithHeaders(h map[string]string) Option {
	return func(c *Client) {
		c.headers = make(map[string]string, len(h))
		for k, v := range h {
			c.headers[k] = v
		}
	}
}

// WithProtocolVersion sets the MCP revision requested in initialize.
func WithProtocolVersion(v string) Option { return func(c *Client) { c.protocolVersion = v } }

// WithGuard sets the dial guard. Without it New uses a guard that allows no
// exceptions.
func WithGuard(g *DialGuard) Option { return func(c *Client) { c.guard = g } }

// WithLogger sets the logger. The client logs protocol events at debug level
// and never logs bodies, headers, credentials or session ids.
func WithLogger(l *slog.Logger) Option { return func(c *Client) { c.log = l } }

// Client is a connection to one MCP endpoint. It is safe for concurrent use:
// calls share one session, initialized lazily and re-established once per
// call when the server loses it.
type Client struct {
	endpoint        string
	host            string // for logs: never the path or query
	httpc           *http.Client
	signer          Signer
	callTimeout     time.Duration
	maxList         int64
	maxCall         int64
	meta            func() map[string]any
	headers         map[string]string
	protocolVersion string
	guard           *DialGuard
	log             *slog.Logger

	ids atomic.Int64

	// initSlot serializes initialize round trips so concurrent first calls,
	// or concurrent callers that all saw the session die, handshake once. A
	// one-slot channel rather than a mutex, so a caller whose context ends
	// stops waiting for somebody else's handshake instead of sitting out
	// that caller's timeout.
	initSlot chan struct{}

	mu          sync.Mutex // guards the fields below
	initialized bool
	gen         uint64 // bumped by every initialize
	sessionID   string
	negotiated  string
}

// session is a snapshot of the state a request is sent under.
type session struct {
	id         string
	negotiated string
	gen        uint64
}

// CallResult is a tools/call reply. When the reply exceeded the call cap,
// Truncated is set and Content holds the one text block salvaged from the
// partial body — the start of the tool's output, not a failure.
type CallResult struct {
	wire.CallToolResult
	Truncated bool
	// Bytes is how much of the reply was read, for audit and metrics.
	Bytes int64
}

// Text joins the result's text blocks. A block of another type is named, not
// dropped silently, so the reader knows something was left out.
func (r *CallResult) Text() string {
	var b strings.Builder
	for i, c := range r.Content {
		if i > 0 {
			b.WriteByte('\n')
		}
		if c.Type == "text" {
			b.WriteString(c.Text)
		} else {
			b.WriteString("[" + c.Type + " content omitted]")
		}
	}
	return b.String()
}

// New builds a client for endpoint. The URL must be https, or plain http to a
// host:port the guard allowlists (the local Azure sidecar).
func New(endpoint string, opts ...Option) (*Client, error) {
	c := &Client{
		initSlot:        make(chan struct{}, 1),
		endpoint:        endpoint,
		maxList:         DefaultMaxListBytes,
		maxCall:         DefaultMaxCallBytes,
		protocolVersion: wire.ProtocolVersion,
	}
	for _, o := range opts {
		o(c)
	}
	if c.guard == nil {
		c.guard = &DialGuard{}
	}
	if c.log == nil {
		c.log = slog.Default()
	}
	if c.callTimeout <= 0 {
		c.callTimeout = CallTimeoutFromEnv()
	}
	if c.maxList <= 0 {
		c.maxList = DefaultMaxListBytes
	}
	if c.maxCall <= 0 {
		c.maxCall = DefaultMaxCallBytes
	}
	if c.protocolVersion == "" {
		c.protocolVersion = wire.ProtocolVersion
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("mcpclient: invalid endpoint: %w", err)
	}
	if err := c.guard.CheckURL(u); err != nil {
		return nil, err
	}
	c.host = u.Host
	if c.httpc == nil {
		c.httpc = c.guard.HTTPClient()
	}
	return c, nil
}

// SessionID returns the Mcp-Session-Id of the current session, "" when the
// server issued none or no session is open.
func (c *Client) SessionID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionID
}

// Initialize performs the handshake: initialize, keeping the Mcp-Session-Id
// the server issues, then notifications/initialized. Calling it is optional —
// ListTools and CallTool initialize lazily — and calling it again starts a new
// session. The server's instructions are returned for inspection; callers must
// not forward them to a model (they are the provider's prompt, not ours).
func (c *Client) Initialize(ctx context.Context) (*wire.InitializeResult, error) {
	ctx, cancel := context.WithTimeout(ctx, c.callTimeout)
	defer cancel()
	if err := c.lockInit(ctx); err != nil {
		return nil, err
	}
	defer c.unlockInit()
	return c.initializeLocked(ctx)
}

// lockInit takes the handshake slot, or gives up when ctx ends first.
func (c *Client) lockInit(ctx context.Context) error {
	select {
	case c.initSlot <- struct{}{}:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("mcpclient: waiting for the session handshake: %w", ctx.Err())
	}
}

func (c *Client) unlockInit() { <-c.initSlot }

// initializeLocked runs the handshake. The caller holds the handshake slot.
func (c *Client) initializeLocked(ctx context.Context) (*wire.InitializeResult, error) {
	c.mu.Lock()
	c.initialized = false
	c.sessionID = ""
	c.negotiated = ""
	c.mu.Unlock()

	params := wire.InitializeParams{
		ProtocolVersion: c.protocolVersion,
		Capabilities:    map[string]any{},
		ClientInfo:      wire.Implementation{Name: clientName, Version: clientVersion},
	}
	rep, err := c.send(ctx, "initialize", params, c.maxList, false, session{})
	if err != nil {
		var lost *sessionLostError
		if errors.As(err, &lost) {
			return nil, lost.cause // a fresh handshake has no session to lose
		}
		return nil, err
	}
	var res wire.InitializeResult
	if err := json.Unmarshal(rep.result, &res); err != nil {
		return nil, fmt.Errorf("mcpclient: decode initialize result: %w", err)
	}
	negotiated := res.ProtocolVersion
	if negotiated == "" {
		negotiated = c.protocolVersion
	}

	c.mu.Lock()
	c.gen++
	c.sessionID = rep.sessionID
	c.negotiated = negotiated
	st := session{id: c.sessionID, negotiated: negotiated, gen: c.gen}
	c.mu.Unlock()

	if err := c.notify(ctx, "notifications/initialized", st); err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.initialized = true
	c.mu.Unlock()
	return &res, nil
}

// ensureInitialized initializes once, on first use.
func (c *Client) ensureInitialized(ctx context.Context) error {
	if c.isInitialized() {
		return nil
	}
	if err := c.lockInit(ctx); err != nil {
		return err
	}
	defer c.unlockInit()
	if c.isInitialized() {
		return nil
	}
	_, err := c.initializeLocked(ctx)
	return err
}

func (c *Client) isInitialized() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.initialized
}

func (c *Client) current() session {
	c.mu.Lock()
	defer c.mu.Unlock()
	return session{id: c.sessionID, negotiated: c.negotiated, gen: c.gen}
}

// reinitialize replaces the session generation stale. When a concurrent caller
// already replaced it, the new session is reused instead of opening another.
func (c *Client) reinitialize(ctx context.Context, stale uint64) error {
	if err := c.lockInit(ctx); err != nil {
		return err
	}
	defer c.unlockInit()
	c.mu.Lock()
	fresh := c.initialized && c.gen != stale
	c.mu.Unlock()
	if fresh {
		return nil
	}
	c.log.Debug("mcp: session lost, re-initializing", "host", c.host)
	_, err := c.initializeLocked(ctx)
	return err
}

// ListTools returns the server's whole catalog, following nextCursor for at
// most 10 pages. Each page is read under the list cap; a page over it fails
// the listing rather than silently dropping tools.
func (c *Client) ListTools(ctx context.Context) ([]wire.Tool, error) {
	ctx, cancel := context.WithTimeout(ctx, c.callTimeout)
	defer cancel()
	var (
		tools  []wire.Tool
		cursor string
		seen   = map[string]bool{}
	)
	for page := 0; page < maxListPages; page++ {
		rep, err := c.request(ctx, "tools/list", wire.ListToolsParams{Cursor: cursor}, c.maxList, false)
		if err != nil {
			return nil, err
		}
		var res wire.ListToolsResult
		if err := json.Unmarshal(rep.result, &res); err != nil {
			return nil, fmt.Errorf("mcpclient: decode tools/list result: %w", err)
		}
		tools = append(tools, res.Tools...)
		if res.NextCursor == "" {
			return tools, nil
		}
		if seen[res.NextCursor] {
			return nil, fmt.Errorf("mcpclient: tools/list repeated a cursor")
		}
		seen[res.NextCursor] = true
		cursor = res.NextCursor
	}
	return nil, fmt.Errorf("mcpclient: tools/list did not end within %d pages", maxListPages)
}

// CallTool calls one tool. args must be a JSON object or empty. A tool that
// fails reports it in the result (IsError); the error return is for the
// transport and the protocol: *HTTPError, ErrUnauthorized, ErrRateLimited, a
// *wire.Error from the server, or a context error on timeout. A reply over
// the call cap comes back with Truncated set.
func (c *Client) CallTool(ctx context.Context, name string, args json.RawMessage) (*CallResult, error) {
	if name == "" {
		return nil, errors.New("mcpclient: tools/call needs a tool name")
	}
	args = bytes.TrimSpace(args)
	if len(args) == 0 || bytes.Equal(args, []byte("null")) {
		args = nil
	} else if args[0] != '{' || !json.Valid(args) {
		return nil, errors.New("mcpclient: tool arguments must be a JSON object")
	}
	ctx, cancel := context.WithTimeout(ctx, c.callTimeout)
	defer cancel()

	rep, err := c.request(ctx, "tools/call", wire.CallToolParams{Name: name, Arguments: args}, c.maxCall, true)
	if err != nil {
		return nil, err
	}
	if rep.truncated {
		c.log.Debug("mcp: tools/call reply truncated", "host", c.host, "tool", name, "cap", c.maxCall)
		return &CallResult{
			CallToolResult: wire.CallToolResult{Content: wire.TextContent(salvageText(rep.partial))},
			Truncated:      true,
			Bytes:          rep.bytes,
		}, nil
	}
	var res wire.CallToolResult
	if err := json.Unmarshal(rep.result, &res); err != nil {
		return nil, fmt.Errorf("mcpclient: decode tools/call result: %w", err)
	}
	return &CallResult{CallToolResult: res, Bytes: rep.bytes}, nil
}

// Close ends the session with a DELETE when the server issued one. A server
// that does not support it (405) is fine. The client stays usable: the next
// call opens a new session.
func (c *Client) Close(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, c.callTimeout)
	defer cancel()
	if err := c.lockInit(ctx); err != nil {
		return err
	}
	defer c.unlockInit()
	c.mu.Lock()
	st := session{id: c.sessionID, negotiated: c.negotiated, gen: c.gen}
	c.initialized = false
	c.sessionID = ""
	c.negotiated = ""
	c.mu.Unlock()
	if st.id == "" {
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.endpoint, nil)
	if err != nil {
		return fmt.Errorf("mcpclient: build DELETE: %w", err)
	}
	c.setHeaders(req, st)
	if c.signer != nil {
		if err := c.signer.Sign(ctx, req, nil); err != nil {
			return fmt.Errorf("mcpclient: sign DELETE: %w", err)
		}
	}
	resp, err := c.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("mcpclient: DELETE session: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, errorReadLimit))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300,
		resp.StatusCode == http.StatusMethodNotAllowed,
		resp.StatusCode == http.StatusNotFound: // already gone
		return nil
	default:
		return c.statusError(resp, body)
	}
}

// ----- transport -----

// reply is one decoded JSON-RPC response.
type reply struct {
	result    json.RawMessage
	sessionID string // the Mcp-Session-Id response header
	bytes     int64
	truncated bool
	partial   []byte // the body read so far when truncated
}

// sessionLostError marks a reply that means "this session is gone": a 404
// sent to a request that carried a session, or the AWS server's -32600
// "Session ID is required". cause is what the caller sees if the retry on a
// fresh session fails the same way.
type sessionLostError struct{ cause error }

func (e *sessionLostError) Error() string { return e.cause.Error() }
func (e *sessionLostError) Unwrap() error { return e.cause }

// request sends one request on the current session, initializing first, and
// re-initializes and retries once when the server lost the session. The
// retry is safe for tools/call because a lost session means the server never
// ran it.
func (c *Client) request(ctx context.Context, method string, params any, limit int64, allowTruncate bool) (*reply, error) {
	if err := c.ensureInitialized(ctx); err != nil {
		return nil, err
	}
	st := c.current()
	rep, err := c.send(ctx, method, params, limit, allowTruncate, st)
	var lost *sessionLostError
	if !errors.As(err, &lost) {
		return rep, err
	}
	if err := c.reinitialize(ctx, st.gen); err != nil {
		return nil, err
	}
	rep, err = c.send(ctx, method, params, limit, allowTruncate, c.current())
	if errors.As(err, &lost) {
		return nil, lost.cause
	}
	return rep, err
}

// send performs one JSON-RPC round trip: build the body (with _meta), sign it,
// POST it, map the status, and decode the matching response from a JSON or an
// SSE reply.
func (c *Client) send(ctx context.Context, method string, params any, limit int64, allowTruncate bool, st session) (*reply, error) {
	rawParams, err := c.encodeParams(params)
	if err != nil {
		return nil, fmt.Errorf("mcpclient: encode %s params: %w", method, err)
	}
	id := json.RawMessage(strconv.FormatInt(c.ids.Add(1), 10))
	body, err := json.Marshal(wire.Request{JSONRPC: wire.JSONRPCVersion, ID: id, Method: method, Params: rawParams})
	if err != nil {
		return nil, fmt.Errorf("mcpclient: encode %s: %w", method, err)
	}

	resp, err := c.post(ctx, body, st)
	if err != nil {
		return nil, fmt.Errorf("mcpclient: %s: %w", method, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, errorReadLimit))
		if resp.StatusCode == http.StatusNotFound && st.id != "" {
			return nil, &sessionLostError{cause: c.statusError(resp, errBody)}
		}
		return nil, c.statusError(resp, errBody)
	}

	sessionID := resp.Header.Get("Mcp-Session-Id")
	if len(sessionID) > maxSessionID || !visibleASCII(sessionID) {
		return nil, fmt.Errorf("mcpclient: %s: server sent an invalid Mcp-Session-Id", method)
	}

	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	lr := &io.LimitedReader{R: resp.Body, N: limit + 1}
	var (
		msg     *inbound
		partial []byte
	)
	switch mediaType {
	case "text/event-stream":
		msg, partial, err = readSSE(lr, id, c.log)
		if err != nil && !errors.Is(err, errNoResponse) {
			return nil, fmt.Errorf("mcpclient: %s: read stream: %w", method, err)
		}
	case "application/json", "":
		data, rerr := io.ReadAll(lr)
		if rerr != nil {
			return nil, fmt.Errorf("mcpclient: %s: read body: %w", method, rerr)
		}
		if int64(len(data)) <= limit {
			if len(bytes.TrimSpace(data)) == 0 {
				return nil, fmt.Errorf("mcpclient: %s: empty response (HTTP %d)", method, resp.StatusCode)
			}
			msg, err = matchInbound(data, id, c.log)
			if err != nil {
				return nil, fmt.Errorf("mcpclient: %s: decode response: %w", method, err)
			}
		} else {
			partial = data[:limit]
		}
	default:
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, errorReadLimit))
		return nil, fmt.Errorf("mcpclient: %s: unexpected content type %q: %s", method, mediaType, shortBody(errBody, mediaType))
	}
	read := limit + 1 - lr.N

	if msg == nil {
		if lr.N <= 0 {
			if !allowTruncate {
				return nil, fmt.Errorf("%w: %s reply over %d bytes", ErrResponseTooLarge, method, limit)
			}
			if int64(len(partial)) > limit {
				partial = partial[:limit]
			}
			return &reply{sessionID: sessionID, bytes: limit, truncated: true, partial: partial}, nil
		}
		return nil, fmt.Errorf("mcpclient: %s: no response with id %s", method, id)
	}
	if msg.Error != nil {
		if msg.Error.Code == wire.CodeInvalidRequest && strings.Contains(strings.ToLower(msg.Error.Message), "session id") {
			return nil, &sessionLostError{cause: msg.Error}
		}
		return nil, msg.Error
	}
	if len(msg.Result) == 0 {
		return nil, fmt.Errorf("mcpclient: %s: response has neither result nor error", method)
	}
	return &reply{result: msg.Result, sessionID: sessionID, bytes: read}, nil
}

// notify sends a notification. Servers answer 202 with an empty body
// (KubeBolt, Google) or "{}" (AWS); any 2xx is accepted and the body ignored.
func (c *Client) notify(ctx context.Context, method string, st session) error {
	body, err := json.Marshal(wire.Request{JSONRPC: wire.JSONRPCVersion, Method: method})
	if err != nil {
		return fmt.Errorf("mcpclient: encode %s: %w", method, err)
	}
	resp, err := c.post(ctx, body, st)
	if err != nil {
		return fmt.Errorf("mcpclient: %s: %w", method, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, errorReadLimit))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return c.statusError(resp, data)
	}
	return nil
}

// post builds, signs and sends one POST.
func (c *Client) post(ctx context.Context, body []byte, st session) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	c.setHeaders(req, st)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if c.signer != nil {
		if err := c.signer.Sign(ctx, req, body); err != nil {
			return nil, fmt.Errorf("sign request: %w", err)
		}
	}
	return c.httpc.Do(req)
}

// setHeaders applies the caller's headers, then the session headers over them.
func (c *Client) setHeaders(req *http.Request, st session) {
	req.Header.Set("User-Agent", userAgent)
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	// The session headers are the client's alone: a caller header must not
	// open or resume a session the client does not know about.
	req.Header.Del("Mcp-Session-Id")
	req.Header.Del("MCP-Protocol-Version")
	if st.id != "" {
		req.Header.Set("Mcp-Session-Id", st.id)
	}
	if st.negotiated != "" {
		req.Header.Set("MCP-Protocol-Version", st.negotiated)
	}
}

// statusError maps a non-2xx reply: 401 and 429 by status alone, whatever the
// body says; everything else as an *HTTPError with a short plain-text body.
func (c *Client) statusError(resp *http.Response, body []byte) error {
	ct := resp.Header.Get("Content-Type")
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return &unauthorizedError{detail: unauthorizedDetail(body, ct)}
	case http.StatusTooManyRequests:
		return &RateLimitError{RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())}
	default:
		return &HTTPError{Status: resp.StatusCode, Body: shortBody(body, ct)}
	}
}

// encodeParams marshals params and merges the WithMeta metadata into its
// _meta, keeping any key the params already set.
func (c *Client) encodeParams(params any) (json.RawMessage, error) {
	var meta map[string]any
	if c.meta != nil {
		meta = c.meta()
	}
	if len(meta) == 0 {
		if params == nil {
			return nil, nil
		}
		return json.Marshal(params)
	}
	obj := map[string]json.RawMessage{}
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(b, &obj); err != nil {
			return nil, fmt.Errorf("params are not a JSON object: %w", err)
		}
	}
	merged := make(map[string]any, len(meta))
	for k, v := range meta {
		merged[k] = v
	}
	if existing, ok := obj["_meta"]; ok {
		var ex map[string]any
		if err := json.Unmarshal(existing, &ex); err != nil {
			return nil, fmt.Errorf("params _meta is not an object: %w", err)
		}
		for k, v := range ex {
			merged[k] = v
		}
	}
	mb, err := json.Marshal(merged)
	if err != nil {
		return nil, err
	}
	obj["_meta"] = mb
	return json.Marshal(obj)
}

func visibleASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x21 || s[i] > 0x7e {
			return false
		}
	}
	return true
}
