package kobiplan

import "testing"

func TestDiscard(t *testing.T) {
	p := examplePlan()
	if err := p.Discard("Ana", t0); err != nil {
		t.Fatal(err)
	}
	if p.Status != StatusExpired || p.ExpiredReason != ExpiredDiscarded || p.Status.Live() {
		t.Fatalf("status %s, reason %s", p.Status, p.ExpiredReason)
	}
	if n := p.Notes[len(p.Notes)-1]; n.Text != "Discarded by Ana." || n.Version != p.Version {
		t.Fatalf("note = %+v", n)
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	// Once ended, it cannot be discarded again.
	if err := p.Discard("Ana", t0); err == nil {
		t.Fatal("a discarded plan was discarded again")
	}
}

// A plan with every step applied is done: it stays to measure its impact.
func TestDiscard_ADonePlanIsKept(t *testing.T) {
	p := examplePlan()
	p.Status = StatusExported
	p.Steps[0].Status = StepApplied
	if err := p.Discard("Ana", t0); err != ErrPlanDone || p.Status != StatusExported {
		t.Fatalf("err %v, status %s", err, p.Status)
	}
}
