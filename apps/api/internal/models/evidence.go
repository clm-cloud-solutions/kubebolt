package models

import "time"

// EvidenceKind classifies what a piece of evidence IS, so a reader — a human
// or a model — can weigh it without parsing prose. The four values mirror the
// vocabulary Autopilot's investigator already emits (types.ts EvidenceItem),
// on purpose: Insights and Autopilot describing the same cluster in two
// different grammars is how Kobi ends up with two vocabularies for one thing.
type EvidenceKind string

const (
	// EvidenceConfig — a value read off the spec. Standing state: it was true
	// before the incident and stays true until someone edits it. No moment.
	EvidenceConfig EvidenceKind = "config"
	// EvidenceEvent — something that HAPPENED, and therefore has a clock.
	EvidenceEvent EvidenceKind = "event"
	// EvidenceMetric — a measurement, with the bar it was measured against.
	EvidenceMetric EvidenceKind = "metric"
	// EvidenceLog — a line the workload itself emitted.
	EvidenceLog EvidenceKind = "log"
)

// Evidence is one fact a rule saw when it fired.
//
// It exists because the rules already hold these facts and throw them away:
// newInsight takes five strings, so a rule that read term.ExitCode,
// term.FinishedAt and the container's memory limit can only hand over a
// sentence it wrote about them. Audited 2026-09-20: 60 fmt.Sprintf calls
// feeding 27 newInsight calls across 24 rules.
type Evidence struct {
	Kind EvidenceKind `json:"kind"`
	// Label is what the fact is called, not what it says: "Memory limit",
	// "Last termination", "Threshold". Stable per rule, so a UI can align
	// two episodes of the same rule side by side.
	Label string `json:"label"`
	// Detail is the value: "64Mi", "OOMKilled (exit 137)", "0.87 of 0.85".
	Detail string `json:"detail"`
	// Source is where it was read from, in the cluster's own terms —
	// "pod.status.containerStatuses[0].lastState.terminated" — so a reader
	// can go verify it rather than trust the summary.
	Source string `json:"source,omitempty"`
	// At is when the FACT happened, taken from the fact itself
	// (term.FinishedAt, an event's lastTimestamp), never from time.Now().
	//
	// Nil is meaningful and common: a config value has no moment. It is the
	// difference between "this is state" and "I don't know when" — which is
	// why this is a pointer and not a zero Time.
	//
	// This is the field the insight timestamps cannot provide: newInsight
	// stamps FirstSeen/LastSeen with the EVALUATION's clock, so an OOM at
	// 03:10 discovered at 03:10:30 is recorded as 03:10:30. Moving FirstSeen
	// would move the lifecycle anchor (dedup, flaps, ReopenCooldown,
	// retention); putting the real clock here does not.
	At *time.Time `json:"at,omitempty"`
}
