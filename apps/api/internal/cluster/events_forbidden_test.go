package cluster

import "testing"

// Without access to events the informer never starts and there is no
// lister. GetEvents used to dereference it and panic, which Kobi's
// get_events and GET /events both reached on such a cluster.
func TestGetEvents_WithoutAccessSaysForbidden(t *testing.T) {
	c := &Connector{}

	got := c.GetEvents("", "", "", "", 10)
	if !got.Forbidden {
		t.Fatalf("Forbidden = false: an unreadable list must not read as an empty one")
	}
	if got.Items == nil || len(got.Items) != 0 || got.Total != 0 {
		t.Fatalf("items = %v, total = %d; want an empty, non-nil list", got.Items, got.Total)
	}
	if items := c.listEventsAsResources(""); len(items) != 0 {
		t.Fatalf("listEventsAsResources = %v; want nothing", items)
	}
}
