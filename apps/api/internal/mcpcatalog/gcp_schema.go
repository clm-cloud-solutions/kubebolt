package mcpcatalog

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// Google's catalogs are written for Google's own clients: descriptions of up
// to 40 KB, $defs/$ref graphs, per-enum-value prose, outputSchemas of 50 KB.
// Every byte handed to the model is paid on every prompt, and some model
// APIs reject keywords they do not know, so each inputSchema is rebuilt from
// the handful of keywords every consumer understands.

const (
	// gcpMaxToolDescription bounds one tool's description.
	gcpMaxToolDescription = 1200
	// gcpMaxPropDescription bounds one property's description: enough for
	// the format sentence Google puts first, not the essay after it.
	gcpMaxPropDescription = 250
	// gcpMaxSchemaDepth bounds nesting, with $ref expansion counted.
	gcpMaxSchemaDepth = 8
	// gcpMaxSchemaNodes bounds the expanded size of one schema. The depth
	// limit alone does not: $defs that each reference another several times
	// multiply at every level, so a 2 MiB catalog could expand into more
	// nodes than the API process has memory. Google's largest schema, a
	// write tool KubeBolt never binds, expands to about 740 nodes; the
	// allowlisted ones stay under 30.
	gcpMaxSchemaNodes = 2000
)

var (
	gcpMarkdownLinkRE = regexp.MustCompile(`\[([^\]\n]{1,200})\]\s?\((?:https?://|/)[^)\s]*\)`)
	gcpSpaceRE        = regexp.MustCompile(`\s+`)
)

// gcpCleanText collapses whitespace, drops markdown emphasis and keeps the
// text of markdown links without their URLs: the model needs "monitoring
// filter", not the docs URL.
func gcpCleanText(s string) string {
	s = strings.ToValidUTF8(s, "")
	s = gcpMarkdownLinkRE.ReplaceAllString(s, "$1")
	s = strings.ReplaceAll(s, "**", "")
	return strings.TrimSpace(gcpSpaceRE.ReplaceAllString(s, " "))
}

// gcpCapText cuts s to at most max bytes, preferring the end of a sentence
// in the second half, else a word boundary, and marks the cut with "…".
func gcpCapText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	const ellipsis = "…"
	limit := max - len(ellipsis)
	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}
	cut := s[:limit]
	if i := strings.LastIndex(cut, ". "); i >= max/2 {
		return cut[:i+1]
	}
	if i := strings.LastIndexByte(cut, ' '); i >= max/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,;:") + ellipsis
}

// gcpNormalizeSchema rebuilds a Google inputSchema from type, description,
// properties, required, items and enum only: $schema, additionalProperties,
// oneOf/anyOf, formats, patterns and the x-google-* extensions are dropped,
// and local $refs are expanded in place. The root description is dropped
// too — the tool description says it. An unresolvable or recursive $ref, or
// nesting past gcpMaxSchemaDepth, is an error: the tool is then not bound,
// rather than bound with a schema that lies about its arguments.
func gcpNormalizeSchema(in map[string]any) (map[string]any, error) {
	if in == nil {
		return map[string]any{"type": "object", "properties": map[string]any{}}, nil
	}
	defs := map[string]map[string]any{}
	for _, key := range []string{"$defs", "definitions"} {
		if d, ok := in[key].(map[string]any); ok {
			for name, v := range d {
				if m, ok := v.(map[string]any); ok {
					defs[key+"/"+name] = m
				}
			}
		}
	}
	budget := gcpMaxSchemaNodes
	out, err := gcpNormalizeNode(in, defs, 0, nil, &budget)
	if err != nil {
		return nil, err
	}
	delete(out, "description")
	out["type"] = "object"
	if _, ok := out["properties"]; !ok {
		out["properties"] = map[string]any{}
	}
	return out, nil
}

func gcpNormalizeNode(node map[string]any, defs map[string]map[string]any, depth int, expanding []string, budget *int) (map[string]any, error) {
	if depth > gcpMaxSchemaDepth {
		return nil, errors.New("schema nests too deeply")
	}
	if *budget--; *budget < 0 {
		return nil, errors.New("schema expands too far")
	}
	if ref, ok := node["$ref"].(string); ok {
		key, found := strings.CutPrefix(ref, "#/")
		target, known := defs[key]
		if !found || !known {
			return nil, fmt.Errorf("unresolvable $ref %q", ref)
		}
		for _, e := range expanding {
			if e == key {
				return nil, fmt.Errorf("recursive $ref %q", ref)
			}
		}
		expanding = append(expanding[:len(expanding):len(expanding)], key)
		// The referring node's own description is the more specific one
		// ("the time range to list", not "a time range").
		merged := make(map[string]any, len(target)+1)
		for k, v := range target {
			merged[k] = v
		}
		if d, ok := node["description"]; ok {
			merged["description"] = d
		}
		node = merged
	}

	out := map[string]any{}
	switch t := node["type"].(type) {
	case string:
		out["type"] = t
	case []any:
		types := make([]any, 0, len(t))
		for _, v := range t {
			if s, ok := v.(string); ok {
				types = append(types, s)
			}
		}
		if len(types) > 0 {
			out["type"] = types
		}
	}
	if d, ok := node["description"].(string); ok {
		if d = gcpCapText(gcpCleanText(d), gcpMaxPropDescription); d != "" {
			out["description"] = d
		}
	}
	if e, ok := node["enum"].([]any); ok && len(e) > 0 {
		vals := make([]any, 0, len(e))
		for _, v := range e {
			switch v.(type) {
			case string, float64, bool, nil:
				vals = append(vals, v)
			}
		}
		if len(vals) > 0 {
			out["enum"] = vals
		}
	}
	if props, ok := node["properties"].(map[string]any); ok {
		np := make(map[string]any, len(props))
		for name, raw := range props {
			pm, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			n, err := gcpNormalizeNode(pm, defs, depth+1, expanding, budget)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			np[name] = n
		}
		out["properties"] = np
		if req, ok := node["required"].([]any); ok {
			var keep []any
			for _, r := range req {
				if s, ok := r.(string); ok && np[s] != nil {
					keep = append(keep, s)
				}
			}
			if len(keep) > 0 {
				out["required"] = keep
			}
		}
	}
	if items, ok := node["items"].(map[string]any); ok {
		n, err := gcpNormalizeNode(items, defs, depth+1, expanding, budget)
		if err != nil {
			return nil, fmt.Errorf("items: %w", err)
		}
		out["items"] = n
	}
	if _, typed := out["type"]; !typed {
		switch {
		case out["properties"] != nil:
			out["type"] = "object"
		case out["items"] != nil:
			out["type"] = "array"
		}
	}
	return out, nil
}

// gcpSchemaProps returns the top-level properties of a normalized schema.
func gcpSchemaProps(s map[string]any) map[string]any {
	p, _ := s["properties"].(map[string]any)
	return p
}

// gcpShapeSchema applies a rule to a normalized schema: hidden and dropped
// fields go, and fields KubeBolt fills stop being required.
func gcpShapeSchema(s map[string]any, hidden map[string]bool, optional map[string]bool) {
	props := gcpSchemaProps(s)
	for name := range hidden {
		delete(props, name)
	}
	req, _ := s["required"].([]any)
	var keep []any
	for _, r := range req {
		name, _ := r.(string)
		if hidden[name] || optional[name] || props[name] == nil {
			continue
		}
		keep = append(keep, name)
	}
	if len(keep) == 0 {
		delete(s, "required")
	} else {
		s["required"] = keep
	}
}

// gcpCopyJSON deep-copies a decoded JSON value, so a consumer that edits the
// schema it was handed cannot change the binding's.
func gcpCopyJSON(v any) any {
	switch t := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(t))
		for k, x := range t {
			m[k] = gcpCopyJSON(x)
		}
		return m
	case []any:
		s := make([]any, len(t))
		for i, x := range t {
			s[i] = gcpCopyJSON(x)
		}
		return s
	}
	return v
}

// gcpSortedKeys returns a map's keys in order, for deterministic errors.
func gcpSortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
