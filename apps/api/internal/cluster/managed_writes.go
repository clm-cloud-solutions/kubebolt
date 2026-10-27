package cluster

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// When a resource last changed, and through what, is in Kubernetes' own
// record of its field managers (metadata.managedFields), which every read for
// a person strips: it is long and mostly noise. Without it Kobi could not say
// whether a Secret changed today — it compared resourceVersions and left it at
// "it does not seem to" — nor that a Deployment's latest revision came from a
// `kubectl rollout restart` (db-role-password-rotated, 2026-10-12). A
// ManagedWrite keeps the part that answers: who wrote, how, when and which
// fields, never the values.

// ManagedWrite is one field manager's latest write to an object.
type ManagedWrite struct {
	// Manager is the client's field-manager name (kubectl-rollout,
	// kubectl-client-side-apply, argocd-controller, …), not a person.
	Manager     string `json:"manager"`
	Operation   string `json:"operation,omitempty"`
	Subresource string `json:"subresource,omitempty"`
	Time        string `json:"time,omitempty"`
	// Fields are the paths it owns, three levels deep at most.
	Fields []string `json:"fields,omitempty"`
}

const (
	managedWriteDepth  = 3
	managedWriteFields = 8
)

// summarizeManagedFields reads metadata.managedFields into one ManagedWrite
// per entry, newest first.
func summarizeManagedFields(raw interface{}) []ManagedWrite {
	entries, _ := raw.([]interface{})
	out := make([]ManagedWrite, 0, len(entries))
	for _, e := range entries {
		m, ok := e.(map[string]interface{})
		if !ok {
			continue
		}
		w := ManagedWrite{}
		w.Manager, _ = m["manager"].(string)
		w.Operation, _ = m["operation"].(string)
		w.Subresource, _ = m["subresource"].(string)
		w.Time, _ = m["time"].(string)
		if w.Manager == "" {
			w.Manager = "(unnamed)"
		}
		if f, ok := m["fieldsV1"].(map[string]interface{}); ok {
			w.Fields = managedPaths(f)
		}
		out = append(out, w)
	}
	sort.SliceStable(out, func(i, j int) bool { return writeTime(out[i]).After(writeTime(out[j])) })
	return out
}

func writeTime(w ManagedWrite) time.Time {
	t, _ := time.Parse(time.RFC3339, w.Time)
	return t
}

// managedPaths turns a fieldsV1 set into dotted paths ("data.password",
// "spec.template.metadata"), cut at managedWriteDepth, at most
// managedWriteFields of them, the rest counted in a last entry. List items
// ("k:{…}", "v:…") stand for their list.
func managedPaths(set map[string]interface{}) []string {
	seen := map[string]bool{}
	var paths []string
	var walk func(node map[string]interface{}, prefix []string)
	walk = func(node map[string]interface{}, prefix []string) {
		for key, child := range node {
			if key == "." {
				continue // the field itself; its children name it
			}
			if !strings.HasPrefix(key, "f:") {
				// A list item (k:, v:, i:): its list is the path.
				if len(prefix) > 0 {
					add(seen, &paths, strings.Join(prefix, "."))
				}
				continue
			}
			path := append(append([]string{}, prefix...), strings.TrimPrefix(key, "f:"))
			sub, _ := child.(map[string]interface{})
			if len(sub) == 0 || len(path) >= managedWriteDepth {
				add(seen, &paths, strings.Join(path, "."))
				continue
			}
			walk(sub, path)
		}
	}
	walk(set, nil)
	sort.Strings(paths)
	if len(paths) > managedWriteFields {
		rest := len(paths) - managedWriteFields
		paths = append(paths[:managedWriteFields], "… "+strconv.Itoa(rest)+" more")
	}
	return paths
}

func add(seen map[string]bool, paths *[]string, p string) {
	if !seen[p] {
		seen[p] = true
		*paths = append(*paths, p)
	}
}
