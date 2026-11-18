package cluster

import (
	"regexp"
	"strings"

	sigsyaml "sigs.k8s.io/yaml"

	"github.com/kubebolt/kubebolt/apps/api/internal/helm"
)

// What a reader of KubeBolt never sees, by kind.
//
// The YAML viewer, the Helm release view, describe and Kobi's YAML read all
// hide the same things, decided here and nowhere else:
//
//   - a Secret's data and stringData, every value;
//   - a ConfigMap's values that look like credentials (configMapValueHidden);
//   - a secret store's credentials written inline (#91);
//   - an Argo CD Application's Helm values, parameters and plugin env.
//
// Everything else a Kubernetes `view` role could read stays visible: a
// Deployment's literal env included.
//
// The editor round-trips what the viewer shows: whatever is hidden here must
// come back from the live object on save (yaml_roundtrip.go).

// hideFromViewer hides, in a decoded object, what the viewer does not show.
// It reports whether anything changed.
func hideFromViewer(obj map[string]interface{}) bool {
	group, kind := objectGroupKind(obj)
	if group == "" {
		switch kind {
		case "Secret":
			return hideSecretData(obj)
		case "ConfigMap":
			return hideConfigMapData(obj)
		}
	}
	return hideSpecFromViewer(obj)
}

// hideSpecFromViewer is hideFromViewer for the kinds whose spec, not their
// data, carries a credential in plain sight. Their hidden values come back on
// save through restoreSpecHidden.
func hideSpecFromViewer(obj map[string]interface{}) bool {
	group, kind := objectGroupKind(obj)
	spec, _ := obj["spec"].(map[string]interface{})
	if spec == nil {
		return false
	}
	switch {
	case group == "external-secrets.io" && (kind == "SecretStore" || kind == "ClusterSecretStore"):
		return redactInlineCredentials(spec)
	case group == "argoproj.io" && kind == "Application":
		return hideArgoAppSpec(spec)
	case group == "argoproj.io" && kind == "ApplicationSet":
		tmpl, _ := spec["template"].(map[string]interface{})
		tspec, _ := tmpl["spec"].(map[string]interface{})
		return tspec != nil && hideArgoAppSpec(tspec)
	}
	return false
}

func objectGroupKind(obj map[string]interface{}) (group, kind string) {
	kind, _ = obj["kind"].(string)
	apiVersion, _ := obj["apiVersion"].(string)
	if i := strings.Index(apiVersion, "/"); i >= 0 {
		group = apiVersion[:i]
	}
	return group, kind
}

// hideSecretData hides every value of a Secret: data, and stringData in a
// manifest (the API server never returns stringData).
func hideSecretData(obj map[string]interface{}) bool {
	changed := false
	for _, field := range []string{"data", "stringData"} {
		data, _ := obj[field].(map[string]interface{})
		for k, v := range data {
			if s, ok := v.(string); ok && s == redactedPlaceholder {
				continue
			}
			data[k] = redactedPlaceholder
			changed = true
		}
	}
	return changed
}

func hideConfigMapData(obj map[string]interface{}) bool {
	changed := false
	data, _ := obj["data"].(map[string]interface{})
	for k, v := range data {
		if s, ok := v.(string); ok && s != redactedPlaceholder && configMapValueHidden(k, s) {
			data[k] = redactedPlaceholder
			changed = true
		}
	}
	return changed
}

// ---- Argo CD ----

// hideArgoAppSpec hides the values an Application hands to its sources: a
// chart's values (as text or as an object) and parameters, a plugin's env and
// parameters, jsonnet variables. They are where a password lands when nobody
// put it in a Secret.
func hideArgoAppSpec(spec map[string]interface{}) bool {
	changed := false
	if src, ok := spec["source"].(map[string]interface{}); ok {
		changed = hideArgoSource(src) || changed
	}
	if srcs, ok := spec["sources"].([]interface{}); ok {
		for _, s := range srcs {
			if src, ok := s.(map[string]interface{}); ok {
				changed = hideArgoSource(src) || changed
			}
		}
	}
	return changed
}

func hideArgoSource(src map[string]interface{}) bool {
	changed := false
	if h, ok := src["helm"].(map[string]interface{}); ok {
		if v, ok := h["values"].(string); ok {
			if nv, ch := RedactValuesText(v); ch {
				h["values"] = nv
				changed = true
			}
		}
		if vo, ok := h["valuesObject"].(map[string]interface{}); ok {
			changed = redactValuesTree(vo) || changed
		}
		changed = hideNamedValues(h["parameters"], "value") || changed
	}
	if p, ok := src["plugin"].(map[string]interface{}); ok {
		changed = hideNamedValues(p["env"], "value") || changed
		changed = hideNamedValues(p["parameters"], "string") || changed
		if params, ok := p["parameters"].([]interface{}); ok {
			for _, it := range params {
				if m, ok := it.(map[string]interface{}); ok {
					for _, field := range []string{"array", "map"} {
						if v, ok := m[field]; ok {
							changed = redactValuesTree(map[string]interface{}{nameOf(m): v}) || changed
						}
					}
				}
			}
		}
	}
	if d, ok := src["directory"].(map[string]interface{}); ok {
		if j, ok := d["jsonnet"].(map[string]interface{}); ok {
			changed = hideNamedValues(j["extVars"], "value") || changed
			changed = hideNamedValues(j["tlas"], "value") || changed
		}
	}
	return changed
}

func nameOf(m map[string]interface{}) string {
	s, _ := m["name"].(string)
	return s
}

// hideNamedValues hides, in a list of {name, <field>} entries — an env list, a
// parameter list — each value whose name or shape is a credential's.
func hideNamedValues(list interface{}, field string) bool {
	items, ok := list.([]interface{})
	if !ok {
		return false
	}
	changed := false
	for _, it := range items {
		m, ok := it.(map[string]interface{})
		if !ok {
			continue
		}
		v, ok := m[field].(string)
		if !ok || v == "" || v == redactedPlaceholder {
			continue
		}
		if sensitiveEnv(nameOf(m), v) {
			m[field] = redactedPlaceholder
			changed = true
		}
	}
	return changed
}

// ---- values trees: Helm release values, Argo CD helm values ----

// RedactValuesText is RedactValues for values written as YAML text. Text that
// does not decode to a mapping is filtered line by line instead.
func RedactValuesText(s string) (string, bool) {
	var tree interface{}
	if err := sigsyaml.Unmarshal([]byte(s), &tree); err != nil {
		r := RedactText(s)
		return r, r != s
	}
	m, ok := tree.(map[string]interface{})
	if !ok {
		r := RedactText(s)
		return r, r != s
	}
	if !redactValuesTree(m) {
		return s, false
	}
	out, err := sigsyaml.Marshal(m)
	if err != nil {
		r := RedactText(s)
		return r, true
	}
	return string(out), true
}

// redactValuesTree hides, in place, the credentials in a chart's values. A
// chart decides where a value ends up — often a Secret the viewer would hide —
// and nothing here can tell which, so the rule is by name and by shape:
//
//   - a value under a key that names a credential (dbPassword, apiKey, token,
//     auth…), except a key that names where the credential lives
//     (existingSecret, secretName, *Ref, *File): those are pointers;
//   - an env-like {name, value} entry whose name or value is a credential's;
//   - a single-line value shaped like one (a URL with userinfo, a JWT, a PEM,
//     a known token prefix, a mixed-case 32+ character run);
//   - multi-line text (a config file) loses, line by line, what RedactText hides;
//   - a number under a key that ends in password, pin, secret, token or key.
//
// Booleans stay: automountServiceAccountToken is a switch, not a token.
func redactValuesTree(v interface{}) bool {
	changed := false
	switch t := v.(type) {
	case map[string]interface{}:
		if name, ok := t["name"].(string); ok {
			if val, ok := t["value"].(string); ok && val != "" && val != redactedPlaceholder && sensitiveEnv(name, val) {
				t["value"] = redactedPlaceholder
				changed = true
			}
		}
		for k, child := range t {
			switch c := child.(type) {
			case string:
				if nv, ch := redactValueString(k, c); ch {
					t[k] = nv
					changed = true
				}
			case map[string]interface{}, []interface{}:
				changed = redactValuesTree(c) || changed
			case bool, nil:
			default:
				if numericCredentialKey(k) {
					t[k] = redactedPlaceholder
					changed = true
				}
			}
		}
	case []interface{}:
		for i, child := range t {
			switch c := child.(type) {
			case string:
				if r := RedactText(c); r != c {
					t[i] = r
					changed = true
				}
			case map[string]interface{}, []interface{}:
				changed = redactValuesTree(c) || changed
			}
		}
	}
	return changed
}

func redactValueString(key, s string) (string, bool) {
	if s == "" || s == redactedPlaceholder {
		return s, false
	}
	if !isReferenceKey(key) && (looksLikeSensitiveKey(key) || looksLikeCredentialName(key)) {
		return redactedPlaceholder, true
	}
	if strings.Contains(s, "\n") {
		r := RedactText(s)
		return r, r != s
	}
	if valueLooksLikeCredential(s) {
		return redactedPlaceholder, true
	}
	return s, false
}

// isReferenceKey: the key names where a credential is kept, not the
// credential — existingSecret, secretName, passwordSecretRef, passwordFile.
func isReferenceKey(key string) bool {
	words := nameWords(key)
	if len(words) == 0 {
		return false
	}
	for _, w := range words {
		if w == "existing" {
			return true
		}
	}
	switch words[len(words)-1] {
	case "name", "names", "ref", "refs", "file", "path", "mount", "enabled":
		return true
	}
	return false
}

// valueLooksLikeCredential is the single-line shape test. Unlike
// looksLikeCredentialValue it lets a hex-only run through: in values those are
// image digests and checksums.
func valueLooksLikeCredential(s string) bool {
	if looksLikeSensitiveValue(s) {
		return true
	}
	for _, re := range credentialValuePatterns {
		if re.MatchString(s) {
			return true
		}
	}
	for _, run := range entropyRun.FindAllString(s, -1) {
		if mixedCaseKey(run) {
			return true
		}
	}
	return false
}

func numericCredentialKey(key string) bool {
	words := nameWords(key)
	if len(words) == 0 {
		return false
	}
	switch words[len(words)-1] {
	case "password", "passwd", "pwd", "pass", "pin", "secret", "token", "key", "apikey":
		return true
	}
	return false
}

// ---- Helm ----

// RedactHelmRelease hides, in a release's detail, what the viewer hides of the
// objects themselves: the values by name and shape, the rendered manifest
// document by document (a Secret's data and stringData whole), and the notes
// line by line.
func RedactHelmRelease(r *helm.Release) {
	if r == nil {
		return
	}
	if r.Values != nil {
		redactValuesTree(r.Values)
	}
	r.Manifest = RedactManifest(r.Manifest)
	r.Notes = RedactText(r.Notes)
}

var manifestDocSeparator = regexp.MustCompile(`(?m)^---[ \t]*\r?$`)

// RedactManifest applies hideFromViewer to every document of a multi-document
// manifest. A document with nothing to hide comes back byte for byte, its
// "# Source:" line and key order included; one that changed is re-encoded
// under its leading comments. A document that does not decode is filtered as
// text.
func RedactManifest(manifest string) string {
	if manifest == "" {
		return manifest
	}
	var b strings.Builder
	prev := 0
	for _, loc := range manifestDocSeparator.FindAllStringIndex(manifest, -1) {
		b.WriteString(redactManifestDoc(manifest[prev:loc[0]]))
		b.WriteString(manifest[loc[0]:loc[1]])
		prev = loc[1]
	}
	b.WriteString(redactManifestDoc(manifest[prev:]))
	return b.String()
}

func redactManifestDoc(doc string) string {
	if strings.TrimSpace(doc) == "" {
		return doc
	}
	header, body := splitLeadingComments(doc)
	if strings.TrimSpace(body) == "" {
		return doc
	}
	var obj map[string]interface{}
	if err := sigsyaml.Unmarshal([]byte(body), &obj); err != nil || obj == nil {
		return header + RedactText(body)
	}
	if !hideFromViewer(obj) {
		return doc
	}
	out, err := sigsyaml.Marshal(obj)
	if err != nil {
		return header + RedactText(body)
	}
	return header + string(out)
}

// splitLeadingComments splits off the blank and comment lines a document opens
// with — Helm's "# Source: chart/templates/x.yaml" — so they survive a
// re-encode.
func splitLeadingComments(doc string) (header, body string) {
	rest := doc
	for rest != "" {
		line, after, found := strings.Cut(rest, "\n")
		t := strings.TrimSpace(line)
		if t != "" && !strings.HasPrefix(t, "#") {
			break
		}
		if !found {
			return doc, ""
		}
		rest = after
	}
	return doc[:len(doc)-len(rest)], rest
}
