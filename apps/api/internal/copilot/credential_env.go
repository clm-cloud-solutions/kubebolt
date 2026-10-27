package copilot

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
)

// hiddenEnvValue is what Kobi reads in place of a credential's literal value,
// the same words the revision history uses.
const hiddenEnvValue = "(value hidden)"

// urlUserinfoRE is the user:password part of a URL in an env value.
var urlUserinfoRE = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)[^/?#@\s]+@`)

// shownEnvValue is what Kobi reads for one env var: the value, the value
// with a URL's credentials cut out when that is all that made it sensitive
// (the host of a DSN is what a dependency investigation follows), or nothing.
func shownEnvValue(name, value string) (string, bool) {
	if !cluster.SensitiveEnv(name, value) {
		return value, false
	}
	if !cluster.SensitiveEnv(name, "") {
		masked := urlUserinfoRE.ReplaceAllString(value, "${1}(credentials hidden)@")
		if masked != value && !cluster.SensitiveEnv(name, masked) {
			return masked, true
		}
	}
	return hiddenEnvValue, true
}

// HideCredentialEnv hides the literal value of every env var that holds a
// credential (cluster.SensitiveEnv) in what get_resource_detail and
// get_resource_describe return to Kobi. The prompt tells it never to repeat
// one, and Haiku still did, about once in fifty runs (DB_PASSWORD: tunnel in
// oom-limit-lowered); a value it never reads it cannot repeat. Knowing that a
// credential sits in the spec as a literal is what the diagnosis needs, and
// that stays. The UI's own detail view is not this path.
func HideCredentialEnv(tool, content string) string {
	switch tool {
	case "get_resource_detail":
		var v any
		if json.Unmarshal([]byte(content), &v) != nil {
			return content
		}
		if !hideEnvValues(v) {
			return content
		}
		b, err := json.Marshal(v)
		if err != nil {
			return content
		}
		return string(b)
	case "get_resource_describe":
		var v map[string]string
		if json.Unmarshal([]byte(content), &v) != nil || v["describe"] == "" {
			return content
		}
		out, changed := hideDescribeEnv(v["describe"])
		if !changed {
			return content
		}
		return jsonString(map[string]string{"describe": out})
	}
	return content
}

// hideEnvValues walks a resource detail and hides the value of each literal
// env entry ({"kind":"literal","name":…,"value":…}) that holds a credential.
func hideEnvValues(v any) bool {
	changed := false
	switch t := v.(type) {
	case map[string]any:
		name, _ := t["name"].(string)
		value, isStr := t["value"].(string)
		if kind, _ := t["kind"].(string); kind == "literal" && isStr && value != "" {
			if shown, hid := shownEnvValue(name, value); hid {
				t["value"] = shown
				changed = true
			}
		}
		for _, c := range t {
			if hideEnvValues(c) {
				changed = true
			}
		}
	case []any:
		for _, c := range t {
			if hideEnvValues(c) {
				changed = true
			}
		}
	}
	return changed
}

// hideDescribeEnv hides credential values in the "Environment:" blocks of
// kubectl describe output ("      DB_PASSWORD:  tunnel"). Values that come
// from a Secret or a ConfigMap describe already prints as a reference.
func hideDescribeEnv(text string) (string, bool) {
	lines := strings.Split(text, "\n")
	changed := false
	block := -1 // indentation of the current "Environment:" header, -1 outside one
	for i, line := range lines {
		trimmed := strings.TrimLeft(line, " ")
		indent := len(line) - len(trimmed)
		if block >= 0 && (trimmed == "" || indent <= block) {
			block = -1
		}
		if trimmed == "Environment:" {
			block = indent
			continue
		}
		if block < 0 {
			continue
		}
		name, value, ok := strings.Cut(trimmed, ":")
		value = strings.TrimSpace(value)
		if !ok || value == "" || strings.HasPrefix(value, "<set to the key") || strings.HasPrefix(value, "<none>") {
			continue
		}
		if shown, hid := shownEnvValue(name, value); hid {
			lines[i] = line[:indent] + name + ":  " + shown
			changed = true
		}
	}
	return strings.Join(lines, "\n"), changed
}
