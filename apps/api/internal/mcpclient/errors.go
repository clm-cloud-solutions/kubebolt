package mcpclient

import (
	"bytes"
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kubebolt/kubebolt/apps/api/internal/mcp/wire"
)

var (
	// ErrUnauthorized means the server rejected the credentials (HTTP 401).
	// Providers disagree on the body — GCP answers a JSON-RPC *result* with
	// isError, AWS a JSON-RPC error -32001 — so the status decides, and the
	// body only contributes a short human detail to the wrapping error.
	ErrUnauthorized = errors.New("mcpclient: unauthorized")

	// ErrRateLimited means the server answered HTTP 429. The concrete error is
	// a *RateLimitError carrying the Retry-After the server asked for.
	ErrRateLimited = errors.New("mcpclient: rate limited")

	// ErrResponseTooLarge means an initialize or tools/list reply exceeded the
	// list cap. A truncated catalog would silently drop tools, so it fails
	// instead of being returned partially (tools/call truncates and flags).
	ErrResponseTooLarge = errors.New("mcpclient: response exceeds the size cap")
)

// maxErrorBody bounds the text an *HTTPError carries: enough for a provider's
// one-line reason, never a whole HTML error page.
const maxErrorBody = 512

// errorReadLimit bounds how much of an error body is read before shortening.
const errorReadLimit = 64 << 10

// HTTPError is a non-2xx answer that is neither 401 nor 429. Body is a short
// plain-text rendering of what the server sent (HTML stripped, at most 512
// bytes), fit for a log line or an admin-facing message.
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	msg := "mcpclient: HTTP " + strconv.Itoa(e.Status)
	if t := http.StatusText(e.Status); t != "" {
		msg += " " + t
	}
	if e.Body != "" {
		msg += ": " + e.Body
	}
	return msg
}

// RateLimitError is the concrete ErrRateLimited. RetryAfter is zero when the
// server did not say.
type RateLimitError struct {
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	if e.RetryAfter > 0 {
		return "mcpclient: rate limited (retry after " + e.RetryAfter.String() + ")"
	}
	return "mcpclient: rate limited"
}

func (e *RateLimitError) Unwrap() error { return ErrRateLimited }

// unauthorizedError wraps ErrUnauthorized with the server's short reason.
type unauthorizedError struct{ detail string }

func (e *unauthorizedError) Error() string {
	if e.detail == "" {
		return ErrUnauthorized.Error()
	}
	return ErrUnauthorized.Error() + ": " + e.detail
}

func (e *unauthorizedError) Unwrap() error { return ErrUnauthorized }

// parseRetryAfter reads a Retry-After header in either of its two forms:
// delay-seconds or an HTTP date.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d.Round(time.Second)
		}
	}
	return 0
}

// unauthorizedDetail extracts the reason from a 401 body, whichever shape
// the provider used: a JSON-RPC error (AWS), a JSON-RPC result whose text is
// the reason (GCP), or anything else shortened.
func unauthorizedDetail(body []byte, contentType string) string {
	var resp wire.RawResponse
	if json.Unmarshal(body, &resp) == nil {
		if resp.Error != nil && resp.Error.Message != "" {
			return shorten(resp.Error.Message)
		}
		if len(resp.Result) > 0 {
			var res wire.CallToolResult
			if json.Unmarshal(resp.Result, &res) == nil {
				for _, c := range res.Content {
					if c.Text != "" {
						return shorten(c.Text)
					}
				}
			}
		}
	}
	return shortBody(body, contentType)
}

var (
	reScriptStyle = regexp.MustCompile(`(?is)<(script|style)\b.*?</(script|style)\s*>`)
	reTag         = regexp.MustCompile(`(?s)<[^>]*>`)
	reSpace       = regexp.MustCompile(`\s+`)
)

// shortBody renders an error body as short plain text. Google answers 404
// and 405 with a styled HTML page; the reason is in its text, not its markup.
func shortBody(body []byte, contentType string) string {
	s := string(body)
	trimmed := bytes.TrimSpace(body)
	if strings.Contains(strings.ToLower(contentType), "html") || bytes.HasPrefix(trimmed, []byte("<")) {
		s = reScriptStyle.ReplaceAllString(s, " ")
		s = reTag.ReplaceAllString(s, " ")
		s = html.UnescapeString(s)
	}
	return shorten(s)
}

// shorten collapses whitespace, drops invalid UTF-8 and caps the text at
// maxErrorBody bytes on a rune boundary.
func shorten(s string) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.TrimSpace(reSpace.ReplaceAllString(s, " "))
	if len(s) <= maxErrorBody {
		return s
	}
	const ellipsis = "…"
	cut := maxErrorBody - len(ellipsis)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + ellipsis
}
