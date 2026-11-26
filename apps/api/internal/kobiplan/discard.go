package kobiplan

import (
	"errors"
	"fmt"
	"time"
)

// ErrPlanDone: every step is applied, so there is nothing left to discard.
var ErrPlanDone = errors.New("every step is applied: the plan is kept to measure its impact")

// Discard ends a live plan (P21, plans-06): it stops being live, so it no
// longer holds Free's one plan per cluster nor keeps its workloads out of a
// new plan. Nothing changes in the cluster: a step already applied stays
// applied, and the plan keeps its history. A plan with every step applied is
// done, not discarded: it stays to measure its impact (plans-07).
func (p *Plan) Discard(by string, at time.Time) error {
	if !p.Status.Live() {
		return fmt.Errorf("the plan is %s, not live", p.Status)
	}
	if !p.HasPendingStep() {
		return ErrPlanDone
	}
	p.Status = StatusExpired
	p.ExpiredReason = ExpiredDiscarded
	text := "Discarded."
	if by != "" {
		text = "Discarded by " + by + "."
	}
	p.Notes = append(p.Notes, Note{Version: p.Version, At: at, Text: text})
	return nil
}
