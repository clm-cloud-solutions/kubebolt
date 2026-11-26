package kobiplan

import (
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
)

// Where a Helm release keeps a container's resources (plans-05). A step on a
// Helm-owned workload is applied in the release's values: a change made in
// the cluster is undone by the next helm upgrade. The path is found in the
// release's own values, never guessed from the chart's name.

// FindResourcesPath finds the block in a release's values that sets one
// container's resources: a `resources` map whose cpu and memory requests and
// limits are exactly the container's live ones. Several blocks can match (two
// components with the same defaults): the one whose path names the workload
// or the container wins, and a tie is no answer. Only maps are walked: a list
// index is not a path anyone can hand to Helm. A container with no cpu or
// memory set matches nothing (an empty block would match every container).
func FindResourcesPath(values map[string]any, live ContainerResources, names ...string) ([]string, bool) {
	if live.Requests.empty() && live.Limits.empty() {
		return nil, false
	}
	var matches [][]string
	var walk func(m map[string]any, path []string)
	walk = func(m map[string]any, path []string) {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			child, ok := m[k].(map[string]any)
			if !ok {
				continue
			}
			next := append(append([]string{}, path...), k)
			if k == "resources" && sameResources(child, live) {
				matches = append(matches, next)
				continue
			}
			walk(child, next)
		}
	}
	walk(values, nil)
	switch len(matches) {
	case 0:
		return nil, false
	case 1:
		return matches[0], true
	}
	best, bestScore, tie := -1, 0, false
	for i, p := range matches {
		s := pathScore(p, names)
		switch {
		case s > bestScore:
			best, bestScore, tie = i, s, false
		case s == bestScore:
			tie = true
		}
	}
	if best < 0 || tie || bestScore == 0 {
		return nil, false
	}
	return matches[best], true
}

// pathScore counts the path's segments the workload or container name
// contains: admissionController in kyverno-admission-controller.
func pathScore(path, names []string) int {
	var ns []string
	for _, n := range names {
		if n = alnum(n); n != "" {
			ns = append(ns, n)
		}
	}
	score := 0
	for _, seg := range path {
		s := alnum(seg)
		switch s {
		case "resources", "container", "containers", "main":
			continue
		}
		if len(s) < 3 {
			continue
		}
		for _, n := range ns {
			if strings.Contains(n, s) {
				score++
				break
			}
		}
	}
	return score
}

func alnum(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// sameResources says whether a values block sets exactly the container's
// cpu and memory, request and limit (other resources are not compared).
func sameResources(block map[string]any, live ContainerResources) bool {
	return sameSide(block["requests"], live.Requests) && sameSide(block["limits"], live.Limits)
}

func sameSide(v any, live *Resources) bool {
	m, _ := v.(map[string]any)
	var lcpu, lmem *string
	if live != nil {
		lcpu, lmem = live.CPU, live.Memory
	}
	return sameQuantity(m["cpu"], lcpu) && sameQuantity(m["memory"], lmem)
}

func sameQuantity(v any, live *string) bool {
	if v == nil || v == "" {
		return live == nil
	}
	if live == nil {
		return false
	}
	a, err := resource.ParseQuantity(fmt.Sprint(v))
	if err != nil {
		return false
	}
	b, err := resource.ParseQuantity(*live)
	if err != nil {
		return false
	}
	return a.Cmp(b) == 0
}

// ValuesPatch is the values document that sets one container's change at
// path: only the dimensions the step changes.
func ValuesPatch(path []string, change ContainerResources) map[string]any {
	leaf := map[string]any{}
	if m := quantityMap(change.Requests); len(m) > 0 {
		leaf["requests"] = m
	}
	if m := quantityMap(change.Limits); len(m) > 0 {
		leaf["limits"] = m
	}
	var out any = leaf
	for i := len(path) - 1; i >= 0; i-- {
		out = map[string]any{path[i]: out}
	}
	m, _ := out.(map[string]any)
	return m
}

func quantityMap(r *Resources) map[string]any {
	if r == nil {
		return nil
	}
	m := map[string]any{}
	if r.CPU != nil {
		m["cpu"] = *r.CPU
	}
	if r.Memory != nil {
		m["memory"] = *r.Memory
	}
	return m
}

// mergePatch merges src into dst key by key (the steps of one release share
// one values file).
func mergePatch(dst, src map[string]any) {
	for k, v := range src {
		dm, dok := dst[k].(map[string]any)
		sm, sok := v.(map[string]any)
		if dok && sok {
			mergePatch(dm, sm)
			continue
		}
		dst[k] = v
	}
}
