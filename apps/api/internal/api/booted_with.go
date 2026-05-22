package api

import (
	"net/http"
	"sort"
	"strings"
)

// sensitiveSubstrings flags any env var whose name contains one of these
// fragments as a probable secret. Default-deny posture: we'd rather
// over-redact a benign value than leak a credential. Substring match is
// case-sensitive on the full uppercase env name (KUBEBOLT_* by
// convention) so "_KEY" matches "_KEYRING" too — fine, neither leak.
var sensitiveSubstrings = []string{
	"TOKEN",
	"SECRET",
	"PASSWORD",
	"PASSWD",
	"KEY",
	"WEBHOOK_URL",
	"CREDENTIAL",
	"PRIVATE",
}

// notReallySensitive is the explicit allowlist of "looks sensitive by
// substring rule but isn't a credential" env vars. Adding entries here
// is the safe direction (UN-redacting a known-benign var); leaving the
// list short keeps the secret-leak risk minimal.
//
//   - KUBEBOLT_AGENT_TOKEN_AUDIENCE: a JWT audience identifier the
//     apiserver checks against — name string, not a credential.
//   - KUBEBOLT_AI_MAX_TOKENS / KUBEBOLT_AI_SESSION_BUDGET_TOKENS:
//     numeric token COUNTS for the LLM context window, not credentials.
//   - KUBEBOLT_DEFAULT_REFRESH_INTERVAL_SECONDS: an integer, but the
//     bare "KEY" substring could falsely match a future "_KEYWORD" var
//     — listed defensively though it doesn't currently contain "KEY".
var notReallySensitive = map[string]struct{}{
	"KUBEBOLT_AGENT_TOKEN_AUDIENCE":      {},
	"KUBEBOLT_AI_MAX_TOKENS":             {},
	"KUBEBOLT_AI_SESSION_BUDGET_TOKENS":  {},
	"KUBEBOLT_AI_COMPACT_PRESERVE_TURNS": {},
}

// isSensitiveEnv decides whether an env var's value should be redacted
// in the /booted-with response. Heuristic first (substring match),
// allowlist second (override for known-benign hits). New env vars
// flagged by the heuristic are redacted automatically — that's the
// safe default. To un-redact a benign one, add it to
// notReallySensitive. To force-redact one the heuristic misses, the
// caller should rename the var to include a sensitive substring (the
// heuristic IS the contract).
func isSensitiveEnv(name string) bool {
	if _, ok := notReallySensitive[name]; ok {
		return false
	}
	upper := strings.ToUpper(name)
	for _, frag := range sensitiveSubstrings {
		if strings.Contains(upper, frag) {
			return true
		}
	}
	return false
}

// SnapshotKubeboltEnv captures every KUBEBOLT_* environment variable
// that was set when the process started. Intended to be called from
// main() before any code can mutate os.Setenv (boot-time snapshot).
// Returns a copy — the caller stores it and passes to the router.
//
// The motivation is "what env config did the process actually see at
// boot?" — when a UI override is in effect, operators need to know
// the env baseline that's BEING overridden. Without this endpoint
// they'd be guessing at what the Helm values translated to.
func SnapshotKubeboltEnv(envPairs []string) map[string]string {
	out := make(map[string]string, 32)
	for _, kv := range envPairs {
		if !strings.HasPrefix(kv, "KUBEBOLT_") {
			continue
		}
		idx := strings.IndexByte(kv, '=')
		if idx < 0 {
			continue
		}
		out[kv[:idx]] = kv[idx+1:]
	}
	return out
}

// BootedWithEntry is one row in the response. Sensitive=true means
// the value field is a fixed placeholder ("(set)"); the UI uses the
// flag to render a distinct icon and skip clipboard-copy affordances.
type BootedWithEntry struct {
	Name      string `json:"name"`
	Value     string `json:"value"`
	Sensitive bool   `json:"sensitive"`
}

// handleGetBootedWith returns the boot-time KUBEBOLT_* env var snapshot
// captured at process start. Admin-only via the route group middleware.
// Stable sort order so diffs across reloads are clean to read.
func (h *handlers) handleGetBootedWith(w http.ResponseWriter, r *http.Request) {
	entries := make([]BootedWithEntry, 0, len(h.bootEnv))
	for k, v := range h.bootEnv {
		sensitive := isSensitiveEnv(k)
		entry := BootedWithEntry{Name: k, Sensitive: sensitive}
		if sensitive {
			// Don't leak even a masked preview — the env baseline secret
			// is already managed elsewhere (Settings tabs surface their
			// own masked previews where editable). Here we just confirm
			// presence so the operator can answer "did Helm wire this in?".
			entry.Value = "(set)"
		} else {
			entry.Value = v
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	respondJSON(w, http.StatusOK, map[string]any{
		"env":   entries,
		"count": len(entries),
	})
}
