package cluster

import (
	"fmt"
	"sort"
)

// The YAML viewer (resourceYAML) hides three things: every Secret value, the
// ConfigMap values that look like credentials, and the
// last-applied-configuration annotation. The editor sends the same text back
// (ApplyResourceYAML), and an Update replaces the whole object. Before this
// step a save wrote the placeholder over every hidden value — "REDACTED" is
// valid base64, so the API server took it as the Secret's new bytes — and
// dropped the annotation the next `kubectl apply` merges against.

const redactedPlaceholder = "REDACTED"

// EditRefusedError is an edit ApplyResourceYAML does not send. The message is
// shown to the person who wrote it.
type EditRefusedError struct{ Reason string }

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

// restoreHiddenValues puts back into submitted what the viewer hid of live.
// A Secret value, or a hidden ConfigMap value, that still reads REDACTED takes
// the stored value. REDACTED under a key the object does not have is refused:
// there is nothing to keep, and sending it would store the word itself. The
// last-applied-configuration annotation comes back unless the YAML carries one.
func restoreHiddenValues(resourceType string, submitted, live map[string]interface{}) error {
	switch resourceType {
	case "secrets":
		if err := restoreData(submitted, live, "Secret", func(string, interface{}) bool { return true }); err != nil {
			return err
		}
	case "configmaps":
		hidden := func(k string, v interface{}) bool {
			s, ok := v.(string)
			return ok && configMapValueHidden(k, s)
		}
		if err := restoreData(submitted, live, "ConfigMap", hidden); err != nil {
			return err
		}
	}
	restoreLastApplied(submitted, live)
	return nil
}

// restoreData replaces each placeholder in submitted.data with live.data's
// value under the same key, when the viewer hid that value. A placeholder over
// a value the viewer showed is what the person wrote, and is kept.
func restoreData(submitted, live map[string]interface{}, kind string, hidden func(key string, liveValue interface{}) bool) error {
	data, _ := submitted["data"].(map[string]interface{})
	liveData, _ := live["data"].(map[string]interface{})
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if s, ok := data[k].(string); !ok || s != redactedPlaceholder {
			continue
		}
		stored, ok := liveData[k]
		if !ok {
			return &EditRefusedError{Reason: fmt.Sprintf("%q reads %s, which stands for a hidden value, and this %s has no value under that key to keep. Write the real value.", k, redactedPlaceholder, kind)}
		}
		if hidden(k, stored) {
			data[k] = stored
		}
	}
	return nil
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
