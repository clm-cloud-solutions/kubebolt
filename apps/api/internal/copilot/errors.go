package copilot

import (
	"errors"
	"fmt"
	"net"
	"net/url"
)

// ProviderHTTPError is returned by provider adapters when the upstream LLM
// API responds with a non-2xx status code.
type ProviderHTTPError struct {
	StatusCode int
	Provider   string
	Body       string
}

func (e *ProviderHTTPError) Error() string {
	return fmt.Sprintf("%s API error %d: %s", e.Provider, e.StatusCode, e.Body)
}

// IsRecoverable returns true if the error is one that should trigger fallback
// retry: rate limits (429), 5xx, 404 (primary model/endpoint unavailable), and
// network issues. Auth errors (401/403) and other 4xx validation errors are
// NOT recoverable — they propagate to the user.
func IsRecoverable(err error) bool {
	if err == nil {
		return false
	}
	var herr *ProviderHTTPError
	if errors.As(err, &herr) {
		// 429 (rate limit) and 5xx are recoverable.
		// 404 too: the primary's model (or endpoint) is unavailable for this
		// account — the fallback's different provider/model is exactly the
		// remedy, and the "answered by fallback" badge makes the switch visible
		// so the misconfiguration isn't silently masked.
		if herr.StatusCode == 429 || herr.StatusCode == 404 || herr.StatusCode >= 500 {
			return true
		}
		// Other 4xx (401/403 auth, validation) propagate — those are "fix the
		// key/request", not something a retry on the fallback can resolve.
		return false
	}
	// Network-level errors (timeouts, DNS, connection refused) are recoverable
	var netErr *net.OpError
	if errors.As(err, &netErr) {
		return true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}
	// Anything else (parse errors etc.) — not recoverable
	return false
}
