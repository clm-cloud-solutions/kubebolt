package cluster

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	sigsyaml "sigs.k8s.io/yaml"
)

// Secret redaction for what Kobi and Autopilot READ.
//
// Every tool result travels three places: to the model provider, into
// Autopilot's run_events, and onto the incident timeline any reader of the
// incident can expand. So a credential that reaches a tool result has leaked
// three times. GetResourceYAML already hides Secret data and sensitive
// ConfigMap values; everything free-text did not pass through anything: pod
// logs, a workload's literal env (DB_PASSWORD: "…" in the spec), its args,
// describe output, event messages.
//
// This file is applied at the TOOL boundary (copilot executor), never in the
// shared readers: the YAML editor round-trips what it shows, and a redacted
// env value saved back would overwrite the real one. A person reading a pod's
// logs or YAML in the UI already has the RBAC to read them directly.
//
// The detectors are the ones get_workload_history grew on the connectors
// branch (6f207d58), moved here so every tool shares them. When that commit
// lands, its copies in revision_changes.go must be deleted in favour of these —
// the duplicate names will fail the build, on purpose.
//
// These only ever hide more. A false positive costs a value the model did not
// need; a false negative is the leak.

// RedactedValue replaces whatever was hidden.
const RedactedValue = "[REDACTED]"

// ---- detectors (ported from revision_changes.go, 6f207d58) ----

var credentialValuePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://[^/?#@\s]+@`),       // userinfo in a URL
	regexp.MustCompile(`eyJ[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}\.`), // a JWT
	regexp.MustCompile(`-----BEGIN`),                                // a PEM block
	regexp.MustCompile(`(?:^|[^A-Za-z0-9])(?:gh[pousr]_|github_pat_|glpat-|xox[abprse]-|xapp-|[sr]k_(?:live|test)_|sk-|AIza|AKIA|ASIA)[A-Za-z0-9_-]{8,}`),
	regexp.MustCompile(`(?i)hooks\.slack\.com/|/api/webhooks/`),
	regexp.MustCompile(`(?i)[?&;][a-z0-9_.-]*(?:token|key|sig|signature|password|passwd|pwd|secret|auth)[a-z0-9_.-]*=`),
	regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:password|passwd|pwd|secret|token|api[_-]?key)\s*[=:]`),
	regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:bearer|basic)\s+\S`),
}

// looksLikeCredentialValue: a credential shape anywhere in the value.
func looksLikeCredentialValue(v string) bool {
	for _, re := range credentialValuePatterns {
		if re.MatchString(v) {
			return true
		}
	}
	return highEntropyRun(v)
}

// highEntropyRun: a run of 32+ base64 / hex characters that mixes upper case,
// lower case and digits, or is all hex with letters and digits — a key, not a
// word. A UUID (dashes every few characters) or a kebab-case name is neither.
func highEntropyRun(v string) bool {
	run, upper, lower, digit, hexOnly := 0, false, false, false, true
	for _, r := range v + " " {
		inRun := r < utf8.RuneSelf && (unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("+/=_-", r))
		if !inRun {
			if run >= 32 && ((upper && lower && digit) || (hexOnly && digit && (upper || lower))) {
				return true
			}
			run, upper, lower, digit, hexOnly = 0, false, false, false, true
			continue
		}
		run++
		switch {
		case unicode.IsUpper(r):
			upper = true
		case unicode.IsLower(r):
			lower = true
		case unicode.IsDigit(r):
			digit = true
		}
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			hexOnly = false
		}
	}
	return false
}

// Name words that mark a credential. A word matches when it starts with one
// of credentialNamePrefixes ("PASSPHRASE", "CREDS", "BASIC_AUTH"), equals one
// of credentialNameWords ("KEY", "API-KEY") or ends with "key" ("SSHKEY").
var (
	credentialNamePrefixes = []string{
		"auth", "cred", "pass", "pwd", "secret", "token", "jwt", "cert", "pem",
		"private", "signing", "signature", "hmac", "webhook", "bearer", "cookie",
	}
	credentialNameWords = []string{"key", "keys", "pw", "sig", "dsn", "salt"}
)

// looksLikeCredentialName splits a name into words (at anything not a letter
// or digit, and at a lower-to-upper case change: "dbPassword") and checks each.
func looksLikeCredentialName(name string) bool {
	for _, w := range nameWords(name) {
		if strings.HasSuffix(w, "key") {
			return true
		}
		for _, p := range credentialNamePrefixes {
			if strings.HasPrefix(w, p) {
				return true
			}
		}
		for _, x := range credentialNameWords {
			if w == x {
				return true
			}
		}
	}
	return false
}

func nameWords(s string) []string {
	var words []string
	var cur []rune
	var prev rune
	flush := func() {
		if len(cur) > 0 {
			words = append(words, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	for _, r := range s {
		switch {
		case !unicode.IsLetter(r) && !unicode.IsDigit(r):
			flush()
		case unicode.IsUpper(r) && unicode.IsLower(prev):
			flush()
			cur = append(cur, r)
		default:
			cur = append(cur, r)
		}
		prev = r
	}
	flush()
	return words
}

// sensitiveEnv is the shared helpers' verdict widened by the detectors above.
func sensitiveEnv(name, value string) bool {
	return looksLikeSensitiveKey(name) || looksLikeCredentialName(name) ||
		looksLikeSensitiveValue(value) || looksLikeCredentialValue(value)
}

// ---- free text: logs, describe, event messages ----

// Replacements for a credential INSIDE a line. Each keeps what identifies the
// shape (the scheme, the parameter name, "Bearer") and hides only the value,
// so the reader still learns that a credential was there and where.
var inlineRedactions = []struct {
	re   *regexp.Regexp
	repl string
}{
	// scheme://user:pass@host → scheme://[REDACTED]@host
	{regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)[^/?#@\s]+@`), "${1}" + RedactedValue + "@"},
	// a JWT
	{regexp.MustCompile(`eyJ[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]*`), RedactedValue},
	// tokens known by their prefix (GitHub, GitLab, Slack, Stripe, OpenAI-style, Google, AWS)
	{regexp.MustCompile(`(^|[^A-Za-z0-9])(?:gh[pousr]_|github_pat_|glpat-|xox[abprse]-|xapp-|[sr]k_(?:live|test)_|sk-|AIza|AKIA|ASIA)[A-Za-z0-9_-]{8,}`), "${1}" + RedactedValue},
	// webhook URLs carry their secret in the path
	{regexp.MustCompile(`(?i)(hooks\.slack\.com/)\S+`), "${1}" + RedactedValue},
	{regexp.MustCompile(`(?i)(/api/webhooks/)\S+`), "${1}" + RedactedValue},
	// ?token=… &sig=… ;password=…
	{regexp.MustCompile(`(?i)([?&;][a-z0-9_.-]*(?:token|key|sig|signature|password|passwd|pwd|secret|auth)[a-z0-9_.-]*=)[^&;\s"']+`), "${1}" + RedactedValue},
	// password=… secret: … "api_key":"…" (JSON logs included)
	{regexp.MustCompile(`(?i)((?:^|[^a-z0-9])(?:password|passwd|pwd|secret|token|api[_-]?key)["']?\s*[=:]\s*)("[^"]*"|'[^']*'|[^\s,;&}]+)`), "${1}" + RedactedValue},
	// Authorization: Bearer … / Basic …
	{regexp.MustCompile(`(?i)((?:^|[^a-z0-9])(?:bearer|basic)\s+)[A-Za-z0-9._~+/=-]{8,}`), "${1}" + RedactedValue},
}

// keyValueLine is "  NAME:  value" / "NAME=value" at the start of a line —
// describe's Environment block, config dumps, env printed at boot.
var keyValueLine = regexp.MustCompile(`^(\s*[-*]?\s*["']?)([A-Za-z_][A-Za-z0-9_.-]*)(["']?\s*[:=]\s*)(\S.*)$`)

// entropyRun finds candidate key-like runs for the mixed-case check below.
var entropyRun = regexp.MustCompile(`[A-Za-z0-9+/=_-]{32,}`)

// RedactText hides credentials in free text, line by line. A PEM block is
// hidden whole; a "KEY: value" line whose key or value looks like a credential
// loses its value; credential shapes inside any line lose theirs; and a
// 32+-character run mixing upper case, lower case and digits (a key, not a
// word) is hidden. Hex-only runs are kept on purpose: in logs those are trace
// ids, request ids and image digests, which an investigation needs.
func RedactText(s string) string {
	if s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	out := lines[:0]
	inPEM := false
	for _, line := range lines {
		if inPEM {
			if strings.Contains(line, "-----END") {
				inPEM = false
			}
			continue
		}
		if i := strings.Index(line, "-----BEGIN"); i >= 0 {
			out = append(out, line[:i]+RedactedValue+" (PEM block)")
			inPEM = !strings.Contains(line[i:], "-----END")
			continue
		}
		if m := keyValueLine.FindStringSubmatch(line); m != nil && sensitiveTextPair(m[2], m[4]) {
			out = append(out, m[1]+m[2]+m[3]+RedactedValue)
			continue
		}
		for _, r := range inlineRedactions {
			line = r.re.ReplaceAllString(line, r.repl)
		}
		line = entropyRun.ReplaceAllStringFunc(line, func(run string) string {
			if mixedCaseKey(run) {
				return RedactedValue
			}
			return run
		})
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// sensitiveTextPair is sensitiveEnv for a "KEY: value" line of free text: the
// same names and credential shapes, but hex-only runs are not a credential
// here — "trace_id=4bf92f35…" opens many a log line and must survive.
func sensitiveTextPair(name, value string) bool {
	if looksLikeSensitiveKey(name) || looksLikeCredentialName(name) || looksLikeSensitiveValue(value) {
		return true
	}
	for _, re := range credentialValuePatterns {
		if re.MatchString(value) {
			return true
		}
	}
	for _, run := range entropyRun.FindAllString(value, -1) {
		if mixedCaseKey(run) {
			return true
		}
	}
	return false
}

// mixedCaseKey: upper, lower and digits together — the shape of a random key.
// Unlike highEntropyRun it does not flag hex-only runs (trace ids, digests).
func mixedCaseKey(run string) bool {
	var upper, lower, digit bool
	for _, r := range run {
		switch {
		case r >= 'A' && r <= 'Z':
			upper = true
		case r >= 'a' && r <= 'z':
			lower = true
		case r >= '0' && r <= '9':
			digit = true
		}
	}
	return upper && lower && digit
}

// ---- structured objects: YAML, resource detail, events ----

// RedactObject walks a decoded Kubernetes object (or any JSON-shaped value)
// and hides credentials where a workload keeps them in plain sight:
//
//   - env entries {name, value}: the value, when the name or the value looks
//     like a credential (valueFrom only names a ref and is left alone);
//   - command / args: each element through RedactText, and the element after
//     a flag named like a credential ("--password", "x");
//   - annotations and any "message": through RedactText.
//
// It mutates maps and slices in place and returns the value for chaining.
func RedactObject(v interface{}) interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		for k, child := range t {
			switch k {
			case "env":
				redactEnvList(child)
			case "command", "args":
				redactArgList(child)
			case "annotations":
				if m, ok := child.(map[string]interface{}); ok {
					for ak, av := range m {
						if s, ok := av.(string); ok {
							m[ak] = RedactText(s)
						}
					}
					continue
				}
			case "message":
				if s, ok := child.(string); ok {
					t[k] = RedactText(s)
					continue
				}
			}
			RedactObject(child)
		}
	case []interface{}:
		for _, child := range t {
			RedactObject(child)
		}
	case []map[string]interface{}:
		for _, child := range t {
			RedactObject(child)
		}
	}
	return v
}

func redactEnvList(v interface{}) {
	items, ok := v.([]interface{})
	if !ok {
		return
	}
	for _, it := range items {
		e, ok := it.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := e["name"].(string)
		value, ok := e["value"].(string)
		if ok && value != "" && sensitiveEnv(name, value) {
			e["value"] = RedactedValue
		}
	}
}

func redactArgList(v interface{}) {
	items, ok := v.([]interface{})
	if !ok {
		return
	}
	hideNext := false
	for i, it := range items {
		s, ok := it.(string)
		if !ok {
			continue
		}
		if hideNext && !strings.HasPrefix(s, "-") {
			items[i] = RedactedValue
			hideNext = false
			continue
		}
		hideNext = false
		if k, val, found := strings.Cut(strings.TrimLeft(s, "-"), "="); found && val != "" && sensitiveEnv(k, val) {
			items[i] = s[:len(s)-len(val)] + RedactedValue
			continue
		}
		if strings.HasPrefix(s, "-") && !strings.Contains(s, "=") {
			flag := strings.TrimLeft(s, "-")
			hideNext = looksLikeCredentialName(flag) || looksLikeSensitiveKey(flag)
		}
		items[i] = RedactText(s)
	}
}

// RedactYAML applies RedactObject to a YAML document. If the document does not
// decode, the text itself goes through RedactText — never back unfiltered.
func RedactYAML(doc []byte) []byte {
	var obj interface{}
	if err := sigsyaml.Unmarshal(doc, &obj); err != nil {
		return []byte(RedactText(string(doc)))
	}
	out, err := sigsyaml.Marshal(RedactObject(obj))
	if err != nil {
		return []byte(RedactText(string(doc)))
	}
	return out
}
