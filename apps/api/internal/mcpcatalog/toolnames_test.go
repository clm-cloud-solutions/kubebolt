package mcpcatalog

import (
	"reflect"
	"sort"
	"testing"
)

type toolNamer interface{ ToolNames(Consumer) []string }

// What the admin view promises before a bind must be valid names, and
// Autopilot must never be promised a tool Kobi does not have.
func TestToolNamesAreValidAndAutopilotIsASubset(t *testing.T) {
	cat, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for _, def := range cat.List() {
		n, ok := def.Binder.(toolNamer)
		if !ok {
			t.Fatalf("%s: binder does not list its tools", def.ID)
		}
		kobi := map[string]bool{}
		for _, name := range n.ToolNames(ConsumerKobi) {
			if err := ValidateLocalName(def.ID, name); err != nil {
				t.Errorf("%s: %v", def.ID, err)
			}
			kobi[name] = true
		}
		if len(kobi) == 0 {
			t.Errorf("%s: no tools for Kobi", def.ID)
		}
		for _, name := range n.ToolNames(ConsumerAutopilot) {
			if !kobi[name] {
				t.Errorf("%s: Autopilot gets %q, which Kobi does not", def.ID, name)
			}
		}
	}
}

func TestAWSToolNamesMatchWhatTheBindingServes(t *testing.T) {
	for _, c := range []Consumer{ConsumerKobi, ConsumerAutopilot} {
		var served []string
		for _, s := range awsToolSpecs(c, "us-east-1") {
			served = append(served, s.Name)
		}
		sort.Strings(served)
		if got := (&awsBinder{}).ToolNames(c); !reflect.DeepEqual(got, served) {
			t.Errorf("%s: ToolNames %v, binding serves %v", c, got, served)
		}
	}
}

// Reach counts in each provider's own unit, from the tables the binding
// serves: Autopilot never reads more than Kobi, AWS counts the services its
// rules open and Google Cloud its tools.
func TestReachPerProvider(t *testing.T) {
	cat, err := NewWithPreview()
	if err != nil {
		t.Fatal(err)
	}
	units := map[string]string{"aws": "AWS services", "azure": "commands", "gcp": "tools"}
	for _, d := range cat.List() {
		r, ok := d.Binder.(interface{ Reach(Consumer) (string, int) })
		if !ok {
			t.Errorf("%s: the binder cannot say its reach", d.ID)
			continue
		}
		unit, kobi := r.Reach(ConsumerKobi)
		_, autopilot := r.Reach(ConsumerAutopilot)
		if unit != units[d.ID] {
			t.Errorf("%s: unit %q, want %q", d.ID, unit, units[d.ID])
		}
		if kobi == 0 || autopilot == 0 || autopilot > kobi {
			t.Errorf("%s: reach kobi=%d autopilot=%d", d.ID, kobi, autopilot)
		}
	}
	if _, n := (&awsBinder{}).Reach(ConsumerAutopilot); n != len(awsRulesFor(ConsumerAutopilot)) {
		t.Errorf("AWS reach %d, its Autopilot rules open %d services", n, len(awsRulesFor(ConsumerAutopilot)))
	}
	if _, n := (gcpBinder{}).Reach(ConsumerKobi); n != len(gcpToolNames(ConsumerKobi)) {
		t.Errorf("Google Cloud reach %d, it has %d tools", n, len(gcpToolNames(ConsumerKobi)))
	}
}
