package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"sync"
	"time"
	"unicode/utf8"

	"crypto/sha256"
	"encoding/hex"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
)

// Secret reveal — Tier 2 #9. Exposes a focused, audited path for
// decoding Secret values inline, replacing the operator's "drop to a
// terminal and run kubectl get secret -o yaml | base64 -d" workflow.
//
// Two design choices worth surfacing:
//
//   1. POST, not GET. A reveal is a state-change-equivalent — it
//      emits a side effect (audit log entry) AND returns sensitive
//      payload. GET is cacheable by every layer between client and
//      server (browser, CDN, proxy, intermediate storage); the
//      threat model can't tolerate a revealed Secret value sitting
//      in a CloudFront access log. POST traverses Authorization
//      cleanly and is non-cacheable by spec.
//
//   2. Apiserver Get, NOT informer cache. The standard resource-
//      detail endpoint reads from the informer cache (≤1s stale)
//      because list endpoints would otherwise hammer the apiserver.
//      For reveal, freshness matters — operators triaging a rotation
//      need to see the post-rotation value, not the cached pre-
//      rotation value. The cost is one apiserver round-trip per
//      reveal; that's fine for a human-driven action.
//
// Auth model: Editor+ for non-prod namespaces, Admin for prod
// (configurable pattern, default `^(prod|production|prd)([-_].+)?$`).
// The route-level middleware enforces Editor+; this handler escalates
// to Admin internally when the namespace matches the prod pattern.
//
// Audit: every reveal emits TWO log entries — one to the standard
// `auditMutation` channel (so it shows up in the general action log
// next to restarts/scales/etc., for incident-response queries) and
// one to a dedicated `auditSecretReveal` channel that carries the
// reason, key list, and request metadata. Splitting these is
// intentional: the secret-reveal channel can carry business-
// justification text that doesn't belong in the general action log.
//
// Critical invariant: NEITHER audit channel ever contains the
// revealed values, hashes of values, or any derivative. Only the
// keys requested + the reason. The values flow ONLY in the response
// body, TLS-encrypted to the browser. They never persist server-
// side beyond the request scope.

const (
	// minReasonLen — the operator-supplied reason must be at least
	// 10 characters. Forces a beat of intentionality without being
	// onerous; "rotation check" is 14 chars and works fine. A 0-char
	// reason gate would let "." through, which defeats the audit
	// story.
	minReasonLen = 10

	// maxReasonLen — guard against pasted JSON / log fragments / etc.
	// landing in the audit channel. 500 chars is way more than any
	// sane business justification needs.
	maxReasonLen = 500

	// defaultProdNamespacePattern matches the most common prod-
	// namespace conventions. Override via KUBEBOLT_PROD_NAMESPACE_PATTERN.
	defaultProdNamespacePattern = `^(prod|production|prd)([-_].+)?$`

	prodNamespacePatternEnv = "KUBEBOLT_PROD_NAMESPACE_PATTERN"
)

var (
	prodNSRegexOnce sync.Once
	prodNSRegex     *regexp.Regexp
)

// productionNamespaceRegex returns the compiled regex used to
// classify a namespace as "production" for role-escalation. Reads
// the env var lazily on first use (so test code can SetEnv before
// the first call). On invalid regex, falls back to the default and
// logs a warning.
func productionNamespaceRegex() *regexp.Regexp {
	prodNSRegexOnce.Do(func() {
		pat := os.Getenv(prodNamespacePatternEnv)
		if pat == "" {
			pat = defaultProdNamespacePattern
		}
		re, err := regexp.Compile(pat)
		if err != nil {
			slog.Warn("invalid KUBEBOLT_PROD_NAMESPACE_PATTERN, falling back to default",
				slog.String("attempted", pat),
				slog.String("error", err.Error()))
			re = regexp.MustCompile(defaultProdNamespacePattern)
		}
		prodNSRegex = re
	})
	return prodNSRegex
}

// resetProdNSRegexForTest clears the cached compiled regex so tests
// can change KUBEBOLT_PROD_NAMESPACE_PATTERN between cases. Not used
// outside of tests.
func resetProdNSRegexForTest() {
	prodNSRegexOnce = sync.Once{}
	prodNSRegex = nil
}

func isProductionNamespace(namespace string) bool {
	if namespace == "" {
		return false
	}
	return productionNamespaceRegex().MatchString(namespace)
}

// isProductionNamespaceNow prefers the settings runtime over the
// cached env-only regex. Spec #09 V2 — operators can edit the prod
// namespace pattern via Settings → General and Secret Reveal picks up
// the new pattern on the next request (no restart). Pattern is
// validated at PUT time so a compile failure here would mean the
// stored value bypassed validation — we log a WARN and fall back to
// the env-only regex, which is more conservative than blocking the
// request. Falls through to the env-only path entirely when
// settingsRuntime is nil (auth-disabled mode).
func (h *handlers) isProductionNamespaceNow(namespace string) bool {
	if namespace == "" {
		return false
	}
	if h.settingsRuntime != nil {
		pat := h.settingsRuntime.General().ProdNamespacePattern
		if pat != "" {
			re, err := regexp.Compile(pat)
			if err != nil {
				slog.Warn("stored prod namespace pattern failed to compile, falling back to env baseline",
					slog.String("pattern", pat),
					slog.String("error", err.Error()))
			} else {
				return re.MatchString(namespace)
			}
		}
	}
	return productionNamespaceRegex().MatchString(namespace)
}

type secretRevealRequest struct {
	Keys   []string `json:"keys,omitempty"` // empty/missing means "reveal all keys"
	Reason string   `json:"reason"`
}

// secretRevealedValue is one row of the response. `Kind` lets the UI
// decide whether to render the value as text or as a download
// affordance (binary blobs render as a sha256 + size, with a separate
// download endpoint).
type secretRevealedValue struct {
	Key    string `json:"key"`
	Kind   string `json:"kind"`            // "text" | "binary"
	Value  string `json:"value,omitempty"` // present when kind=text
	SHA256 string `json:"sha256,omitempty"` // present when kind=binary
	Bytes  int    `json:"bytes,omitempty"`  // present when kind=binary
}

func (h *handlers) handleSecretReveal(w http.ResponseWriter, r *http.Request) {
	resourceType := chi.URLParam(r, "type")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	if namespace == "_" {
		namespace = ""
	}
	if resourceType != "secrets" {
		respondError(w, http.StatusBadRequest, "reveal is only supported for secrets")
		return
	}

	var body secretRevealRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(body.Reason) < minReasonLen {
		respondError(w, http.StatusBadRequest, fmt.Sprintf(
			"reason is required and must be at least %d characters — describe why this reveal is needed (this goes to the audit log)", minReasonLen))
		return
	}
	if len(body.Reason) > maxReasonLen {
		respondError(w, http.StatusBadRequest, fmt.Sprintf("reason exceeds %d characters", maxReasonLen))
		return
	}

	// Production-namespace gating. Editor+ is enforced by the route
	// middleware; we escalate to Admin here for prod namespaces. The
	// auth middleware has already validated the JWT and stashed the
	// role on the request context, so this is a cheap in-memory check.
	if h.isProductionNamespaceNow(namespace) {
		role := auth.ContextRole(r)
		if role != auth.RoleAdmin {
			// Audit even denied attempts — failed reveals on prod
			// namespaces are a strong incident-response signal and
			// must not slip past the audit pipeline silently.
			auditSecretReveal(r, namespace, name, body.Keys, body.Reason, "denied_prod_namespace_requires_admin", "")
			respondError(w, http.StatusForbidden, fmt.Sprintf(
				"reveal in production namespace %q requires Admin role (current: %s)", namespace, role))
			return
		}
	}

	conn := h.manager.Connector()
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}

	clientset := conn.Clientset()
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	// Apiserver Get (not informer cache) — see file-level comment
	// about freshness. A reveal that lags by even a few hundred ms
	// can confuse a rotation-validation workflow.
	secret, err := clientset.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			auditSecretReveal(r, namespace, name, body.Keys, body.Reason, "not_found", "")
			respondError(w, http.StatusNotFound, fmt.Sprintf("Secret %s/%s not found", namespace, name))
			return
		}
		auditSecretReveal(r, namespace, name, body.Keys, body.Reason, "apiserver_error", "")
		auditMutation(r, "secret_reveal", resourceType, namespace, name, nil, err)
		log.Printf("Secret reveal failed for %s/%s: %v", namespace, name, err)
		respondMutationError(w, err)
		return
	}

	// Resolve the requested key set. Empty / missing keys means
	// "reveal everything", in which case we emit a sentinel "<all>"
	// label to the audit log so the auditor sees the breadth of the
	// reveal even though we don't enumerate every key in that case.
	requestedKeys := body.Keys
	allKeys := false
	if len(requestedKeys) == 0 {
		requestedKeys = make([]string, 0, len(secret.Data))
		for k := range secret.Data {
			requestedKeys = append(requestedKeys, k)
		}
		allKeys = true
	}

	values := make([]secretRevealedValue, 0, len(requestedKeys))
	missing := []string{}
	for _, k := range requestedKeys {
		raw, ok := secret.Data[k]
		if !ok {
			missing = append(missing, k)
			continue
		}
		values = append(values, decodeSecretValue(k, raw))
	}

	// Audit: TWO entries per reveal — the dedicated channel carries
	// the full context for compliance review; the mutation channel
	// makes the action visible to general "what did operator X do"
	// queries. Neither carries the values.
	auditKeysLabel := "<all>"
	if !allKeys {
		// Use a JSON-encoded slice so log parsers can split it back.
		if b, err := json.Marshal(body.Keys); err == nil {
			auditKeysLabel = string(b)
		}
	}
	auditSecretReveal(r, namespace, name, body.Keys, body.Reason, "success", auditKeysLabel)
	auditMutation(r, "secret_reveal", resourceType, namespace, name, map[string]any{
		"keys":     auditKeysLabel,
		"reasonLen": len(body.Reason),
		"missing":   missing,
	}, nil)

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"name":       name,
		"namespace":  namespace,
		"type":       string(secret.Type),
		"revealedAt": time.Now().UTC().Format(time.RFC3339),
		"values":     values,
		"missing":    missing,
	})
}

// decodeSecretValue inspects a Secret value and returns either the
// decoded text (when it's printable UTF-8) or a binary descriptor
// (sha256 + byte count) when it isn't. Binary values include things
// like compiled certificate bundles, kubeconfig blobs with embedded
// PEMs, gpg keys, etc. — rendering them as text in the UI would
// either crash the render (control chars) or dump unhelpful bytes.
func decodeSecretValue(key string, raw []byte) secretRevealedValue {
	if isPrintableUTF8(raw) {
		return secretRevealedValue{
			Key:   key,
			Kind:  "text",
			Value: string(raw),
		}
	}
	sum := sha256.Sum256(raw)
	return secretRevealedValue{
		Key:    key,
		Kind:   "binary",
		SHA256: hex.EncodeToString(sum[:]),
		Bytes:  len(raw),
	}
}

// isPrintableUTF8 returns true when the byte slice is valid UTF-8
// AND every code point is either printable or a common whitespace
// character (space, tab, newline, CR). The "common whitespace"
// allowance is important — single-line tokens are clearly text, but
// so are multi-line PEMs and JSON blobs which embed newlines.
func isPrintableUTF8(b []byte) bool {
	if !utf8.Valid(b) {
		return false
	}
	for _, r := range string(b) {
		if r == '\n' || r == '\r' || r == '\t' || r == ' ' {
			continue
		}
		// utf8.RuneError catches replacement chars; the printable
		// check (>= 0x20 and not C1 controls) catches everything
		// else.
		if r < 0x20 || (r >= 0x7f && r < 0xa0) {
			return false
		}
	}
	return true
}

// auditSecretReveal emits a structured log entry to the dedicated
// secret-reveal audit channel. The `outcome` field discriminates
// success / denied / not_found / apiserver_error so compliance
// queries can trivially filter to the cases that matter.
//
// Critical: this function NEVER logs the revealed values, hashes of
// values, or anything derivative. Only the keys requested + the
// reason + the user identity + the outcome.
func auditSecretReveal(r *http.Request, namespace, name string, keys []string, reason, outcome, keysLabel string) {
	source := r.Header.Get("X-KubeBolt-Action-Source")
	if source == "" {
		source = "ui"
	}
	var userID, username string
	if claims := auth.ContextClaims(r); claims != nil {
		userID = claims.UserID
		username = claims.Username
	}
	role := string(auth.ContextRole(r))

	if keysLabel == "" {
		// Caller didn't pre-stringify the keys (denial paths). Inline
		// here so the audit entry always has a keys field.
		if len(keys) == 0 {
			keysLabel = "<all>"
		} else if b, err := json.Marshal(keys); err == nil {
			keysLabel = string(b)
		}
	}

	attrs := []any{
		slog.String("audit", "secret_reveal"),
		slog.String("source", source),
		slog.String("user_id", userID),
		slog.String("username", username),
		slog.String("role", role),
		slog.String("namespace", namespace),
		slog.String("name", name),
		slog.String("keys", keysLabel),
		slog.String("reason", reason),
		slog.String("outcome", outcome),
		slog.String("remote_addr", r.RemoteAddr),
		slog.String("user_agent", r.UserAgent()),
	}
	if outcome == "success" {
		slog.Info("secret reveal", attrs...)
	} else {
		slog.Warn("secret reveal", attrs...)
	}
}
