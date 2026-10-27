package copilot

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/models"
)

func eventItem(first, last time.Time) map[string]interface{} {
	return map[string]interface{}{
		"reason":    "Unhealthy",
		"object":    "Pod/orders-api-1",
		"count":     int32(374),
		"firstSeen": first,
		"lastSeen":  last,
	}
}

// The readiness event of the EKS lab's route53 round: the same 503 on the
// same pod during an RDS reboot at 11:15 and the DNS drift at 12:27, folded
// into one event whose firstSeen Kobi read as the outage's start.
func TestEventsWithSpans_MarksAnEventThatSpansEpisodes(t *testing.T) {
	first := time.Date(2026, 10, 22, 11, 15, 57, 0, time.UTC)
	last := time.Date(2026, 10, 22, 12, 57, 2, 0, time.UTC)
	list := models.ResourceList{Kind: "events", Items: []map[string]interface{}{eventItem(first, last)}, Total: 1}

	var got struct {
		Items []map[string]interface{} `json:"items"`
		Total int                      `json:"total"`
		Note  string                   `json:"note"`
	}
	if err := json.Unmarshal([]byte(jsonString(eventsWithSpans(list))), &got); err != nil {
		t.Fatal(err)
	}
	if got.Total != 1 || len(got.Items) != 1 {
		t.Fatalf("the list must keep its fields: %+v", got)
	}
	if span := got.Items[0]["span"]; span != "1h41m" {
		t.Errorf("span = %v, want 1h41m", span)
	}
	if !strings.Contains(got.Note, "separate episodes") {
		t.Errorf("note missing: %q", got.Note)
	}
}

func TestEventsWithSpans_LeavesShortEventsAlone(t *testing.T) {
	first := time.Date(2026, 10, 22, 12, 27, 5, 0, time.UTC)
	short := models.ResourceList{Kind: "events", Items: []map[string]interface{}{
		eventItem(first, first.Add(29*time.Minute)),
		eventItem(first, first),
		{"reason": "Scheduled"}, // no dates at all
	}, Total: 3}

	out := jsonString(eventsWithSpans(short))
	if strings.Contains(out, `"span"`) || strings.Contains(out, `"note"`) {
		t.Errorf("events under the span bar must go out unchanged: %s", out)
	}
	if out != jsonString(short) {
		t.Errorf("answer changed:\n got %s\nwant %s", out, jsonString(short))
	}
}

func TestEventsWithSpans_MarksOnlyTheLongOnes(t *testing.T) {
	first := time.Date(2026, 10, 22, 11, 0, 0, 0, time.UTC)
	long, brief := eventItem(first, first.Add(30*time.Minute)), eventItem(first, first.Add(time.Minute))
	eventsWithSpans(models.ResourceList{Items: []map[string]interface{}{long, brief}})
	if long["span"] != "30m" {
		t.Errorf("span at the bar = %v, want 30m", long["span"])
	}
	if _, ok := brief["span"]; ok {
		t.Errorf("a one-minute event got a span: %v", brief["span"])
	}
}
