package mcpcatalog

import (
	"encoding/json"
	"fmt"
	"strings"
)

// The digest of an Azure read (digest.go). The activity log and metrics,
// which KubeBolt reshapes itself, get their own line; every other command
// goes through digestJSON over the sidecar's results.

// azureActivityDigest: the operations kept, how many did not succeed, and
// their titles rarest first, with the time of one seen once.
func azureActivityDigest(ans azureActivityAnswer) string {
	if len(ans.Operations) == 0 {
		return "no operations"
	}
	out := []string{digestPlural(len(ans.Operations), "operation", "operations")}
	failed := 0
	events := make([]digestEvent, 0, len(ans.Operations))
	for _, op := range ans.Operations {
		if strings.EqualFold(op.Status, "Failed") {
			failed++
		}
		name := op.Title
		if name == "" {
			name = op.Operation
		}
		at := op.Ended
		if at == "" {
			at = op.Started
		}
		events = append(events, digestEvent{name: name, time: at})
	}
	if failed > 0 {
		out = append(out, fmt.Sprintf("%d failed", failed))
	}
	if rarest := digestRarestFirst(events); rarest != "" {
		out = append(out, rarest)
	}
	return strings.Join(out, " · ")
}

// azureMetricsDigest: the metrics read, by name, and how many could not be.
func azureMetricsDigest(items []json.RawMessage, failed int) string {
	var names []string
	for _, raw := range items {
		var m map[string]any
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		name, _ := m["name"].(string)
		if nm, ok := m["name"].(map[string]any); ok {
			name, _ = nm["value"].(string)
		}
		if name != "" {
			names = append(names, name)
		}
	}
	out := digestPlural(len(items), "metric", "metrics")
	if len(names) > 0 {
		shown := names
		if len(shown) > digestMaxNames {
			shown = append(append([]string(nil), names[:digestMaxNames]...), fmt.Sprintf("+%d more", len(names)-digestMaxNames))
		}
		out += ": " + strings.Join(shown, ", ")
	}
	if failed > 0 {
		out += fmt.Sprintf(" · %d failed", failed)
	}
	return out
}
