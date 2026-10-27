package copilot

import (
	"strings"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/models"
)

// eventSpanNoteAfter is how far apart an event's first and last occurrence
// must be before get_events marks its span. An event's count and firstSeen
// are kept by its emitter, not by the episode: the kubelet folds every
// repetition of the same message on the same object into one event, across
// separate episodes, and re-creates an event the API expired with the old
// firstSeen and count. Past this span the first occurrence may belong to an
// earlier episode.
const eventSpanNoteAfter = 30 * time.Minute

const eventSpanNote = "count and firstSeen add up every repetition of the same message on that object since its emitter first saw it, which can span separate episodes: an event with a span may have started long before the current one. Date the current episode from the object's logs (get_pod_logs reports their bursts) or its own conditions, not from firstSeen."

// eventsWithSpans marks the events of a get_events answer whose first and
// last occurrence are at least eventSpanNoteAfter apart, and adds the note
// when any is. An answer with none goes out unchanged.
func eventsWithSpans(list models.ResourceList) any {
	marked := false
	for _, item := range list.Items {
		first, _ := item["firstSeen"].(time.Time)
		last, _ := item["lastSeen"].(time.Time)
		if first.IsZero() || last.IsZero() {
			continue
		}
		span := last.Sub(first)
		if span < eventSpanNoteAfter {
			continue
		}
		// "1h41m", not "1h41m0s".
		item["span"] = strings.TrimSuffix(span.Round(time.Minute).String(), "0s")
		marked = true
	}
	if !marked {
		return list
	}
	return struct {
		models.ResourceList
		Note string `json:"note"`
	}{list, eventSpanNote}
}
