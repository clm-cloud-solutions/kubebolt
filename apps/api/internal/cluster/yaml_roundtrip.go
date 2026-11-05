package cluster

import (
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// The YAML viewer (resourceYAML) hides three things: every Secret value, the
// ConfigMap values that look like credentials, and the
// last-applied-configuration annotation. The editor sends the same text back
// (ApplyResourceYAML), and an Update replaces the whole object. Before this
// step a save wrote the placeholder over every hidden value — "REDACTED" is
// valid base64, so the API server took it as the Secret's new bytes — and
// dropped the annotation the next `kubectl apply` merges against.

const redactedPlaceholder = "REDACTED"

// EditRefusedError is an edit ApplyResourceYAML, or an object CreateResource,
// does not send. The message is shown to the person who wrote it.
type EditRefusedError struct {
	Reason string
	// BinaryValues are the keys refused only because their value decodes to
	// binary data (checkTextValues). The editor offers to send them again
	// with WriteOptions.KeepBinary.
	BinaryValues []string
}

// WriteOptions are what the person confirmed for a write from the editor or
// from New resource.
type WriteOptions struct {
	// KeepBinary sends Secret values that decode to binary data as they are:
	// a keystore or a key file, binary on purpose.
	KeepBinary bool
}

func (e *EditRefusedError) Error() string { return e.Reason }

// configMapValueHidden reports whether the viewer hides this ConfigMap value.
func configMapValueHidden(key, value string) bool {
	return value != "" && (looksLikeSensitiveKey(key) || looksLikeSensitiveValue(value))
}

// checkEditTarget refuses YAML that names another object than the one being
// edited: the Update goes to the name in the YAML, and the hidden values put
// back are the ones of the object being edited.
func checkEditTarget(submitted map[string]interface{}, name, namespace string, clusterScoped bool) error {
	md, _ := submitted["metadata"].(map[string]interface{})
	gotName, _ := md["name"].(string)
	if gotName == "" {
		return &EditRefusedError{Reason: fmt.Sprintf("The YAML has no metadata.name. Keep the name of the object being edited, %q.", name)}
	}
	if gotName != name {
		return &EditRefusedError{Reason: fmt.Sprintf("The YAML is for %q, not %q. An object can't be renamed from the editor.", gotName, name)}
	}
	if clusterScoped {
		return nil
	}
	if gotNS, _ := md["namespace"].(string); gotNS != "" && gotNS != namespace {
		return &EditRefusedError{Reason: fmt.Sprintf("The YAML is in namespace %q, not %q. An object can't be moved from the editor.", gotNS, namespace)}
	}
	return nil
}

// refusals gathers every reason an edit or a create is not sent, so one
// message says them all: fixing one problem to be told the next made a person
// read the REDACTED refusal, whose last words are "base64 under data", as the
// answer to a value that was not base64. Each key lands in one reason only:
// REDACTED is valid base64 and is skipped by the text check, and a value that
// is not base64 is not decoded.
type refusals struct {
	reasons []string
	binary  []string // keys refused only because their value decodes to binary
}

func (f *refusals) add(err error) {
	var r *EditRefusedError
	if !errors.As(err, &r) {
		return
	}
	if len(r.BinaryValues) > 0 {
		f.binary = append(f.binary, r.BinaryValues...)
		return
	}
	f.reasons = append(f.reasons, r.Reason)
}

// err is the one refusal, nil when there is nothing to refuse. The binary
// reason goes last, and only when it is the sole reason does the refusal name
// the keys for "Keep as binary": with anything else wrong, resending them as
// binary would be refused again.
func (f *refusals) err() error {
	if len(f.reasons) == 0 && len(f.binary) == 0 {
		return nil
	}
	if len(f.binary) == 0 {
		return &EditRefusedError{Reason: strings.Join(f.reasons, "\n")}
	}
	alone := len(f.reasons) == 0
	reasons := append(append([]string{}, f.reasons...), binaryReason(f.binary, alone))
	e := &EditRefusedError{Reason: strings.Join(reasons, "\n")}
	if alone {
		e.BinaryValues = f.binary
	}
	return e
}

// quotedKeys is keys sorted and quoted for a message, and whether there is
// more than one.
func quotedKeys(keys []string) (string, bool) {
	sorted := append([]string{}, keys...)
	sort.Strings(sorted)
	for i, k := range sorted {
		sorted[i] = fmt.Sprintf("%q", k)
	}
	return strings.Join(sorted, ", "), len(sorted) > 1
}

// restoreHiddenValues puts back into submitted what the viewer hid of live.
// A Secret value, or a hidden ConfigMap value, that still reads REDACTED takes
// the stored value. REDACTED under a key the object does not have was refused
// before this (checkPlaceholdersKept). The last-applied-configuration
// annotation comes back unless the YAML carries one.
func restoreHiddenValues(resourceType string, submitted, live map[string]interface{}) {
	switch resourceType {
	case "secrets":
		restoreData(submitted, live, func(string, interface{}) bool { return true })
	case "configmaps":
		restoreData(submitted, live, func(k string, v interface{}) bool {
			s, ok := v.(string)
			return ok && configMapValueHidden(k, s)
		})
	}
	restoreLastApplied(submitted, live)
}

// restoreData replaces each placeholder in submitted.data with live.data's
// value under the same key, when the viewer hid that value. A placeholder over
// a value the viewer showed is what the person wrote, and is kept.
func restoreData(submitted, live map[string]interface{}, hidden func(key string, liveValue interface{}) bool) {
	data, _ := submitted["data"].(map[string]interface{})
	liveData, _ := live["data"].(map[string]interface{})
	for k, v := range data {
		if s, ok := v.(string); !ok || s != redactedPlaceholder {
			continue
		}
		if stored, ok := liveData[k]; ok && hidden(k, stored) {
			data[k] = stored
		}
	}
}

// checkPlaceholdersKept refuses, in an edit, REDACTED under a key the object
// does not have: there is no stored value to put back, and sending it would
// store the word itself.
func checkPlaceholdersKept(resourceType string, submitted, live map[string]interface{}) error {
	var kind string
	switch resourceType {
	case "secrets":
		kind = "Secret"
	case "configmaps":
		kind = "ConfigMap"
	default:
		return nil
	}
	data, _ := submitted["data"].(map[string]interface{})
	liveData, _ := live["data"].(map[string]interface{})
	var keys []string
	for k, v := range data {
		if s, ok := v.(string); !ok || s != redactedPlaceholder {
			continue
		}
		if _, ok := liveData[k]; !ok {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	list, many := quotedKeys(keys)
	verb, under := "reads", "that key"
	if many {
		verb, under = "read", "those keys"
	}
	return &EditRefusedError{Reason: fmt.Sprintf("%s %s %s, which stands for a hidden value, and this %s has no value under %s to keep. Write the real value.",
		list, verb, redactedPlaceholder, kind, under)}
}

func restoreLastApplied(submitted, live map[string]interface{}) {
	liveMD, _ := live["metadata"].(map[string]interface{})
	liveAnn, _ := liveMD["annotations"].(map[string]interface{})
	stored, ok := liveAnn[lastAppliedAnnotation]
	if !ok {
		return
	}
	md, _ := submitted["metadata"].(map[string]interface{})
	if md == nil {
		md = map[string]interface{}{}
		submitted["metadata"] = md
	}
	ann, _ := md["annotations"].(map[string]interface{})
	if ann == nil {
		ann = map[string]interface{}{}
		md["annotations"] = ann
	}
	if _, present := ann[lastAppliedAnnotation]; !present {
		ann[lastAppliedAnnotation] = stored
	}
}

// checkNoPlaceholders refuses a new Secret or ConfigMap whose data still reads
// REDACTED: YAML copied from the viewer to create the object somewhere else, a
// pull secret in another namespace. An edit takes the stored value back; a new
// object has none, and the API server would store the word itself. Under a
// Secret's data that is six bytes of garbage, since REDACTED is valid base64.
func checkNoPlaceholders(resourceType string, obj map[string]interface{}) error {
	var kind, how string
	switch resourceType {
	case "secrets":
		kind, how = "Secret", "Write the real value, in plain text under stringData or base64 under data."
	case "configmaps":
		kind, how = "ConfigMap", "Write the real value."
	default:
		return nil
	}
	data, _ := obj["data"].(map[string]interface{})
	var keys []string
	for k, v := range data {
		if s, ok := v.(string); ok && s == redactedPlaceholder {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	list, many := quotedKeys(keys)
	verb := "reads"
	if many {
		verb = "read"
	}
	return &EditRefusedError{Reason: fmt.Sprintf("%s %s %s, which stands for a hidden value, and a new %s has no value to keep. %s",
		list, verb, redactedPlaceholder, kind, how)}
}

// checkBase64Values refuses a value the API server cannot decode: what goes
// under a Secret's data, or a ConfigMap's binaryData, is base64. The API
// server answers anything else with "illegal base64 data at input byte N"
// and names no key; refused here, the person reads which key and how to
// write it. Being base64 does not make a value right: REDACTED is valid
// base64 (an edit puts the stored value back, a create refuses it:
// checkNoPlaceholders), and so is any plain text whose length is a multiple
// of four in the base64 alphabet, "hola" or "password" (checkTextValues).
func checkBase64Values(resourceType string, obj map[string]interface{}) error {
	var field, kind, how string
	switch resourceType {
	case "secrets":
		field, kind, how = "data", "Secret", "To write a value in plain text, put it under stringData instead."
	case "configmaps":
		field, kind, how = "binaryData", "ConfigMap", "Plain text goes under data."
	default:
		return nil
	}
	values, _ := obj[field].(map[string]interface{})
	var keys []string
	for k, v := range values {
		if v == nil {
			continue // an empty value
		}
		s, ok := v.(string)
		if ok {
			// What the API server's JSON decoder does with a []byte field.
			if _, err := base64.StdEncoding.DecodeString(s); err == nil {
				continue
			}
		}
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return nil
	}
	list, many := quotedKeys(keys)
	verb := "is"
	if many {
		verb = "are"
	}
	return &EditRefusedError{Reason: fmt.Sprintf("%s under %s %s not base64, and every value under a %s's %s must be. %s",
		list, field, verb, kind, field, how)}
}

// checkTextValues refuses a value under a Secret's data that is valid base64
// and decodes to binary data. That is almost always plain text written where
// base64 goes: "hola" or "password" are valid base64, and the API server
// stores them as a few bytes of garbage without a word. Binary secrets exist
// (a keystore, a key file), so the refusal names the keys and the person can
// send them again as binary (WriteOptions.KeepBinary). A placeholder is not
// checked: it is the stored value, put back or refused elsewhere, and a
// stored value may be binary.
func checkTextValues(resourceType string, obj map[string]interface{}, opts WriteOptions) error {
	if resourceType != "secrets" || opts.KeepBinary {
		return nil
	}
	data, _ := obj["data"].(map[string]interface{})
	var keys []string
	for k, v := range data {
		s, ok := v.(string)
		if !ok || s == redactedPlaceholder {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(s)
		if err != nil || IsPrintableText(raw) {
			continue // not base64 is checkBase64Values' refusal
		}
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return nil
	}
	sort.Strings(keys)
	return &EditRefusedError{Reason: binaryReason(keys, true), BinaryValues: keys}
}

// binaryReason says which values decode to binary. Alone, it offers to keep
// them as binary; next to other problems, only once those are fixed.
func binaryReason(keys []string, alone bool) string {
	list, many := quotedKeys(keys)
	verb := "decodes"
	if many {
		verb = "decode"
	}
	keep := "keep it as binary."
	if !alone {
		keep = "you can keep it as binary once the rest is fixed."
	}
	return fmt.Sprintf("%s under data %s to binary data, not text. If you typed the value in plain text, put it under stringData instead. If it is binary on purpose, such as a keystore or a key file, %s",
		list, verb, keep)
}

// IsPrintableText reports whether b is text: valid UTF-8 with no control
// characters other than tab, newline and carriage return. Single-line tokens
// are text, and so are PEM blocks and JSON, which embed newlines. The Secret
// reveal uses the same rule to show a value or say "binary", so the two never
// disagree on what is text.
func IsPrintableText(b []byte) bool {
	if !utf8.Valid(b) {
		return false
	}
	for _, r := range string(b) {
		if r == '\n' || r == '\r' || r == '\t' || r == ' ' {
			continue
		}
		if r < 0x20 || (r >= 0x7f && r < 0xa0) {
			return false
		}
	}
	return true
}
