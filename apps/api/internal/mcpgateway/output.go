package mcpgateway

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// MaxOutputBytes caps what one external tool call hands the model, wrapper
// included. It matches Kobi's own per-tool cap, so the executor passes the
// result through unchanged instead of cutting the closing tag off.
const MaxOutputBytes = 32 << 10

// Everything a provider returns is data, never instructions, and it may carry
// credentials a cloud API happens to echo: an access key id in an IAM
// listing, a connection string in an app setting, a key in a function's
// environment. Before it reaches the model — and through the model, the chat
// transcript and the provider's logs — the obvious shapes are redacted.
//
// This is a net for the obvious, not a data-loss-prevention engine: the real
// barriers are the allowlists (no secret-reading operation is exposed) and
// the read-only credentials the customer grants.
const redacted = "[REDACTED]"

// secretKeys are key names whose value is a credential wherever they appear:
// key=value (connection strings, query strings), key: value (YAML, headers)
// and "key": "value" (JSON).
const secretKeys = `aws_secret_access_key|aws_session_token|secret_?access_?key|session_?token|` +
	`password|passwd|pwd|account_?key|client_?secret|sig|x-api-key|api_?key|` +
	`access_?token|refresh_?token`

// secretName is a secretKeys name as it appears in environment variables and
// config keys: prefixed (DB_PASSWORD, MYSQL_ROOT_PASSWORD, AZURE_CLIENT_SECRET,
// spring.datasource.password, storageAccountKey). A bare \b before the key
// would miss all of them, because '_' is a word character — and log lines
// read through CloudWatch or Cloud Logging are full of exactly these. Every
// pattern that uses it ends in \b, which keeps PasswordLastUsed and
// password_last_changed out.
const secretName = `[A-Za-z0-9_.-]*?(?:` + secretKeys + `)`

// envSecretName is the wider set for the {"name": …, "value": …} shape of an
// environment list (ECS task definitions, Cloud Run, App Service settings):
// there the name alone says the value is a credential, so a JWT_SECRET or a
// GITHUB_TOKEN counts too.
const envSecretName = `[A-Za-z0-9_.-]*?(?:` + secretKeys + `|secret|token|key|credentials?)`

// secretValue is a quoted (plain or JSON-escaped) or bare value. A bare value
// never starts with '{' or '[': a key such as rootPassword or UserPassword
// that holds an object is a schema or a policy, not a credential, and eating
// its opening brace would hand the model broken JSON.
const secretValue = `(\\"(?:[^\\]|\\[^"])*\\"|"(?:[^"\\]|\\.)*"|'[^']*'|[^\s"'&;,{}\[<>\\\])][^\s"'&;,}<>\\\])]*)`

type redactor struct {
	re   *regexp.Regexp
	repl func(match string, sub []string) string
}

func fixed(s string) func(string, []string) string {
	return func(string, []string) string { return s }
}

var redactors = []redactor{
	// PEM key blocks, also when a cap cut them before the END line. Public
	// keys match too; losing one costs nothing.
	{
		re:   regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*KEY(?: BLOCK)?-----.*?(?:-----END [A-Z0-9 ]*KEY(?: BLOCK)?-----|$)`),
		repl: fixed("[REDACTED PRIVATE KEY]"),
	},
	// A GCP service-account key's private_key field, plain or JSON-escaped.
	{
		re:   regexp.MustCompile(`(\\?"private_key\\?"\s*:\s*)(\\?"(?:[^"\\]|\\[^"])*\\?")`),
		repl: func(_ string, sub []string) string { return sub[1] + quoteLike(sub[2], redacted) },
	},
	// An Azure SAS in a connection string: the whole token, not only sig=.
	{
		re:   regexp.MustCompile(`(?i)(SharedAccessSignature\s*=\s*)[^;\s"'<>]+`),
		repl: func(_ string, sub []string) string { return sub[1] + redacted },
	},
	// key=value / key: value / "key": "value" for well-known secret names.
	{
		re:   regexp.MustCompile(`(?i)((?:\\?["'])?\b` + secretName + `\b(?:\\?["'])?\s*[:=]\s*)` + secretValue),
		repl: func(_ string, sub []string) string { return sub[1] + quoteLike(sub[2], redacted) },
	},
	// {"name": "DB_PASSWORD", "value": "…"}, plain or JSON-escaped.
	{
		re: regexp.MustCompile(`(?i)(\\?"name\\?"\s*:\s*\\?"` + envSecretName + `\\?"\s*,\s*\\?"value\\?"\s*:\s*)` +
			`(\\"(?:[^\\]|\\[^"])*\\"|"(?:[^"\\]|\\.)*")`),
		repl: func(_ string, sub []string) string { return sub[1] + quoteLike(sub[2], redacted) },
	},
	// The password of a URL's userinfo: postgres://app:<password>@db,
	// redis://:<password>@cache — the usual DATABASE_URL / REDIS_URL shape. A
	// port is not a password: "/" cannot appear before the '@'.
	{
		re:   regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://[^\s:/?#@"'<>\\]*:)[^\s@/?#"'<>\\]+@`),
		repl: func(_ string, sub []string) string { return sub[1] + redacted + "@" },
	},
	// x-api-key style: any *key / *token / *secret / *signature = long opaque
	// value (also X-Amz-Security-Token and X-Amz-Signature in presigned URLs).
	{
		re:   regexp.MustCompile(`(?i)\b([a-z0-9_.-]*(?:key|token|secret|signature))=([A-Za-z0-9/+_.~%-]{20,}={0,2})`),
		repl: func(_ string, sub []string) string { return sub[1] + "=" + redacted },
	},
	{
		re:   regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/-]{20,}=*`),
		repl: fixed("Bearer " + redacted),
	},
	{
		re:   regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`),
		repl: fixed("[REDACTED JWT]"),
	},
	// AWS access key ids (long-term AKIA, temporary ASIA).
	{
		re:   regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`),
		repl: fixed("[REDACTED AWS KEY ID]"),
	},
}

// quoteLike replaces a value keeping the quoting it had, so redacted JSON
// stays JSON.
func quoteLike(orig, with string) string {
	switch {
	case strings.HasPrefix(orig, `\"`):
		return `\"` + with + `\"`
	case strings.HasPrefix(orig, `"`):
		return `"` + with + `"`
	case strings.HasPrefix(orig, `'`):
		return `'` + with + `'`
	}
	return with
}

// redact removes secret-looking values and reports how many it replaced.
func redact(s string) (string, int) {
	n := 0
	for _, r := range redactors {
		re, repl := r.re, r.repl
		s = re.ReplaceAllStringFunc(s, func(m string) string {
			n++
			return repl(m, re.FindStringSubmatch(m))
		})
	}
	return s, n
}

// shortRedacted is a redacted, bounded one-liner of an error, for logs and
// for admin messages that quote a provider.
func shortRedacted(s string, max int) string {
	s, _ = redact(s)
	s = strings.Join(strings.Fields(s), " ")
	return cutRunes(s, max, "…")
}

// cutRunes shortens s to at most max bytes on a rune boundary, appending tail
// (within the budget) when it cut.
func cutRunes(s string, max int, tail string) string {
	if len(s) <= max {
		return s
	}
	cut := max - len(tail)
	if cut < 0 {
		cut = 0
	}
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + tail
}

// wrapperTag is how the model tells external data from instructions; the
// system prompt says what it means.
const (
	wrapperOpen  = `<external_tool_output connector="%s" tool="%s">`
	wrapperClose = `</external_tool_output>`
)

// wrapperRE finds anything in a provider's text that would open or close
// our wrapper, so a resource description cannot end the block early and
// continue as if it were KubeBolt speaking. A model reads straight through
// what a byte comparison does not: Go's \s is ASCII only, so the gaps also
// admit Unicode spaces (NBSP, ideographic space), the invisible format
// characters (zero-width space and joiners, bidi marks, soft hyphen, BOM),
// which may also sit inside the name, and the full-width forms of '<' and '/'.
var wrapperRE = regexp.MustCompile(`(?i)[<\x{FF1C}\x{FE64}]` + wrapperGap + `([/\x{FF0F}]?)` + wrapperGap + invisiblyJoined("external_tool_output"))

const wrapperGap = `[\s\x0b\p{Z}\p{Cf}]*`

// invisiblyJoined matches word with any run of format characters between its
// letters.
func invisiblyJoined(word string) string {
	var b strings.Builder
	for i, r := range word {
		if i > 0 {
			b.WriteString(`\p{Cf}*`)
		}
		b.WriteString(regexp.QuoteMeta(string(r)))
	}
	return b.String()
}

// redactWindow is how much of a reply is redacted at all. The redactors are
// about ten regular expressions over the whole text, and a reply can be far
// larger than what the model is shown — 1 MiB of AWS JSON cost ~360 ms of CPU
// on the request path, nearly all of it on text thrown away by the cap. Only
// a margin past the budget is scanned.
const redactWindow = 2 * MaxOutputBytes

// cutTerminator is appended to a reply cut to redactWindow. It closes what
// the cut may have left open — a quoted value ("…", \"…\", '…') or a URL's
// userinfo (…@) — so a secret longer than the margin whose start is still
// visible is redacted up to the cut, instead of slipping past a pattern that
// needs its closing character. A redactor that needs another closing
// character must find it here too. Whatever of it a redaction does not
// swallow is cut off with the rest.
const cutTerminator = `@\"'"`

// truncatedNote is KubeBolt's note on a reply cut to fit: a note, before the
// wrapper like the others, because inside it the same words would be data the
// prompts tell the model never to follow.
const truncatedNote = "the answer below is cut: it shows the first %d of %d bytes; ask for less"

// notePrefix starts each line of KubeBolt's own notes on a call. The notes
// come before the wrapper, never inside it: inside, the same words would be
// data like the rest, and a provider could write them too.
const notePrefix = "[KubeBolt] "

// A binding's notes are a few short sentences; the bounds only stop a
// runaway one from eating the budget of the provider's answer.
const (
	maxNotes     = 4
	maxNoteBytes = 1 << 10
)

// noteLines renders a binding's notes (mcpcatalog.CallResult.Notes) as the
// lines that precede the wrapper. A note is KubeBolt's own words by
// contract; the rendering only makes sure that one breaking the contract
// cannot pass for more than a note: each is one bounded line that cannot
// open or close the wrapper. Notes are not redacted: nothing in them comes
// from a provider.
func noteLines(notes []string) string {
	var b strings.Builder
	for i, n := range notes {
		if i == maxNotes {
			break
		}
		n = strings.Join(strings.Fields(n), " ")
		n = wrapperRE.ReplaceAllString(n, "&lt;${1}external_tool_output")
		if n == "" {
			continue
		}
		b.WriteString(notePrefix + cutRunes(n, maxNoteBytes, "…") + "\n")
	}
	return b.String()
}

// finishOutput redacts, caps and wraps a provider's text for the model, after
// KubeBolt's own notes on the call. The result, notes and wrapper included,
// is at most MaxOutputBytes.
func finishOutput(connector, tool, text string, notes ...string) (out string, truncated bool, redactions int) {
	return finishOutputWithin(connector, tool, text, redactWindow, notes...)
}

// finishOutputWithin is finishOutput redacting at most window bytes of text.
func finishOutputWithin(connector, tool, text string, window int, notes ...string) (out string, truncated bool, redactions int) {
	head := noteLines(notes)
	body, precut := text, len(text) > window
	if precut {
		cut := window
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		body = text[:cut] + cutTerminator
	}
	body, redactions = redact(body)
	body = wrapperRE.ReplaceAllString(body, "&lt;${1}external_tool_output")
	open := fmt.Sprintf(wrapperOpen, connector, tool)
	budget := MaxOutputBytes - len(head) - len(open) - len(wrapperClose) - 2 // the two newlines
	if precut || len(body) > budget {
		total, limit := len(body), len(body)
		if precut {
			// The reply's own size; and never the terminator, which (or
			// what is left of it) sits at the very end.
			total, limit = len(text), len(body)-len(cutTerminator)
		}
		// The note is sized for the largest numbers it can carry, so the cut
		// never has to be redone.
		note := notePrefix + fmt.Sprintf(truncatedNote, budget, total) + "\n"
		keep := max(0, min(budget-len(note), limit))
		for keep > 0 && keep < len(body) && !utf8.RuneStart(body[keep]) {
			keep--
		}
		head += notePrefix + fmt.Sprintf(truncatedNote, keep, total) + "\n"
		body = body[:keep]
		truncated = true
	}
	return head + open + "\n" + body + "\n" + wrapperClose, truncated, redactions
}

// finishOwn renders a binding's own words (mcpcatalog.CallResult.Own): a
// refusal written before anything reached the provider. They go out as
// KubeBolt's, with no wrapper, after the call's notes. A refusal can quote
// what the model sent, so the rendering still makes sure the text cannot open
// or close the wrapper, and caps it like any other result.
func finishOwn(text string, notes ...string) string {
	head := noteLines(notes)
	text = wrapperRE.ReplaceAllString(strings.TrimSpace(text), "&lt;${1}external_tool_output")
	return head + notePrefix + cutRunes(text, MaxOutputBytes-len(head)-len(notePrefix), "…")
}
