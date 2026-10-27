package mcpcatalog

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// A binding that came up with an endpoint unlisted says so through
// PartialBinding, which is what lets the gateway bind it again later and keep
// the binding that misses less; once the endpoint answers, a new bind lacks
// nothing and carries its tools. The Test hint promises that return and
// nothing an admin would have to do.
func TestGCPBindingReportsWhatIsMissing(t *testing.T) {
	f := gcpFilterFake(t)
	f.mu.Lock()
	f.listFail["redis"] = http.StatusServiceUnavailable
	f.mu.Unlock()
	partial := f.bind(t, gcpTestResolved(gcpTestProject))
	pb, ok := Binding(partial).(PartialBinding)
	if !ok {
		t.Fatal("the Google Cloud binding does not implement PartialBinding")
	}
	got := pb.Missing()
	if !reflect.DeepEqual(got, []string{"redis"}) {
		t.Fatalf("missing = %v", got)
	}
	got[0] = "tampered"
	if partial.Missing()[0] != "redis" {
		t.Fatal("Missing returned the binding's own slice")
	}
	tr, _ := partial.Test(t.Context())
	if !tr.OK || !strings.Contains(tr.Hint, "redis") || !strings.Contains(tr.Hint, "come back") || strings.Contains(tr.Hint, "reload") {
		t.Fatalf("test = %+v", tr)
	}

	f.mu.Lock()
	delete(f.listFail, "redis")
	f.mu.Unlock()
	whole := f.bind(t, gcpTestResolved(gcpTestProject))
	if m := Binding(whole).(PartialBinding).Missing(); len(m) != 0 || len(whole.Tools(ConsumerKobi)) <= len(partial.Tools(ConsumerKobi)) {
		t.Fatalf("rebind: missing %v, %d tools (partial had %d)", m, len(whole.Tools(ConsumerKobi)), len(partial.Tools(ConsumerKobi)))
	}
	var redis bool
	for _, s := range whole.Tools(ConsumerKobi) {
		redis = redis || strings.HasPrefix(s.Name, "redis_")
	}
	if !redis {
		t.Fatal("the redis tools did not come back on a new bind")
	}
}
