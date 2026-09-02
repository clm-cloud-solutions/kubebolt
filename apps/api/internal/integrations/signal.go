package integrations

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// This file defines the SignalProvider contract — the optional
// interface an integration implements when it contributes structured
// signals (findings, runtime events, metric samples) beyond the
// detection card that the base Provider interface covers.
//
// Design (E1 WS-C, sequenced 2026-07-11): the framework contract
// ships in E1 with OpenCost as the first implementation; the E2
// security providers (Trivy, kube-bench, Falco, Kyverno) implement
// it WITHOUT touching the framework. Storage for findings /
// runtime_events (the FindingsStore) intentionally lands with its
// first consumer in E2 — see the E1 integrations plan, decision D2.
//
// Pattern for future providers:
//
//	type trivyProvider struct{ ... }
//	func (p *trivyProvider) IngestMode() IngestPattern { return IngestCRD }
//	func (p *trivyProvider) Normalize(ctx, raw) (Signals, error) {
//	    // raw = *unstructured.Unstructured VulnerabilityReport
//	    // → []Finding{Kind: FindingCVE, ...}
//	}

// IngestPattern declares HOW an integration's signal reaches
// KubeBolt. It drives which generic machinery the backend / agent
// wires for the provider — one of four patterns, per the
// third-party integrations spec:
type IngestPattern string

const (
	// IngestScrape — the agent's exporter collector scrapes the
	// integration's Prometheus /metrics endpoint (leader-elected;
	// see packages/agent/internal/collector/exporter.go). Samples
	// ship the normal ring-buffer → gRPC path. OpenCost.
	IngestScrape IngestPattern = "scrape"

	// IngestCRD — a backend informer watches the integration's CRDs
	// and hands each object to Normalize. Trivy Operator
	// (VulnerabilityReport…), Kyverno/Gatekeeper (PolicyReport…).
	IngestCRD IngestPattern = "crd"

	// IngestHTTPSink — the integration pushes events to an HTTP
	// receiver (agent-side sink). Falco via falcosidekick.
	IngestHTTPSink IngestPattern = "httpSink"

	// IngestCronParse — a periodic job's output is fetched and
	// parsed. kube-bench (CIS benchmark CronJob, JSON per node).
	IngestCronParse IngestPattern = "cronParse"
)

// FindingKind classifies a Finding for filtering and dashboards.
type FindingKind string

const (
	FindingCVE             FindingKind = "cve"
	FindingMisconfig       FindingKind = "misconfig"
	FindingPolicyViolation FindingKind = "policy_violation"
	FindingRBACIssue       FindingKind = "rbac_issue"

	// FindingExposedSecret is a credential baked into an image. Its own kind
	// rather than a CVE: both are fixed by rebuilding, which is why they share a
	// lens, but a CVE is a package to upgrade while this is a key to rotate —
	// and counting them together would bury one row that matters among hundreds
	// that matter less.
	FindingExposedSecret FindingKind = "exposed_secret"
)

// FindingSeverity is the normalized severity ladder — providers map
// their native scales onto it (Trivy CVSS bands, Kyverno audit/
// enforce, kube-bench pass/fail).
type FindingSeverity string

const (
	SeverityCritical FindingSeverity = "critical"
	SeverityHigh     FindingSeverity = "high"
	SeverityMedium   FindingSeverity = "medium"
	SeverityLow      FindingSeverity = "low"
	SeverityInfo     FindingSeverity = "info"
)

// Finding is one normalized security/compliance signal — the row
// shape of the future `findings` table (E2) and the payload Kobi's
// query_findings read-tool returns. Tenant/cluster identity is NOT
// part of the struct: the ingest layer stamps it, same stance as
// metric samples (providers can't spoof another org's findings).
type Finding struct {
	// Kind + Source identify what this is and who said so.
	Kind   FindingKind `json:"kind"`
	Source string      `json:"source"` // integration ID, e.g. "trivy"

	Severity FindingSeverity `json:"severity"`
	Title    string          `json:"title"` // e.g. "CVE-2024-12345: privilege escalation"

	// Affected resource. Kind/Namespace/Name may be partially empty
	// for cluster-scoped findings (a CIS control has no namespace).
	ResourceKind      string `json:"resourceKind,omitempty"`
	ResourceNamespace string `json:"resourceNamespace,omitempty"`
	ResourceName      string `json:"resourceName,omitempty"`

	// CISControl is set by compliance sources (kube-bench), e.g.
	// "5.1.1". Empty otherwise.
	CISControl string `json:"cisControl,omitempty"`

	// NOTE: an ExposureScore field lived here — "deterministic 0..1 correlation
	// of the finding with real exposure" — declared since the first cut and never
	// computed by anything. Removed rather than carried: a field in a public
	// struct is a promise, and one that is always zero teaches readers to
	// distrust the rest of the shape.
	//
	// The idea is worth doing and the inputs now exist (Hubble gives
	// reachability, Falco gives runtime activity), but it is a MODEL, not a
	// field: it needs a defensible weighting someone can justify to a customer
	// asking why a CVE scored 0.7. See the KPI spec §6 — until that design
	// exists, an invented number in the security lens is worse than none.

	// Remediation is the operator-facing next step, when the source
	// provides one.
	Remediation string `json:"remediation,omitempty"`

	// Rollup marks a finding that AGGREGATES others already ingested — a
	// compliance control whose "N failing" is, arithmetically, the count of
	// failing results from checks we store individually. Verified against a live
	// cluster: 35 of 35 comparable controls matched their own checks' failing
	// count EXACTLY.
	//
	// It stays a finding (compliance posture is a real question, and controls
	// with no ingested check are the only way to see control-plane issues), but
	// it must not be COUNTED alongside its own parts — that inflates every KPI
	// by the same problem twice.
	//
	// Nothing is lost by not counting it: a control's remediation is a pointer
	// ("see CIS Kubernetes Benchmarks v1.23 control 5.2.6"), never an
	// instruction. Measured: 0 of 55 compliance findings carried remediation
	// that was anything but such a pointer, while the per-workload check carries
	// the actual fix.
	Rollup bool `json:"rollup,omitempty"`

	// Image is the container image the finding was found IN, when the source
	// scans images. It is the thing an operator actually rebuilds, so a list of
	// affected workloads without it names the symptom and hides the cure — two
	// workloads sharing an image are one fix, and nothing in the row said so.
	//
	// Deliberately NOT part of the fingerprint: a tag moving from v1 to v2 with
	// the same CVE still unfixed is the same open problem, and forking identity
	// on it would resolve-and-recreate on every deploy — the churn the
	// ReplicaSet collapse exists to prevent.
	Image string `json:"image,omitempty"`

	// Benchmark names the standard a compliance control belongs to ("CIS
	// Kubernetes Benchmarks v1.23", "National Security Agency…"). Presenting
	// every failing control as one pile treats four standards as one, and they
	// do not oblige equally — a customer may be contractually held to one and
	// to none of the others.
	//
	// It lived only inside the remediation string ("see <benchmark> control X"),
	// which is prose, not a field you can group by.
	Benchmark string `json:"benchmark,omitempty"`

	DetectedAt time.Time `json:"detectedAt"`
}

// RuntimeEvent is one normalized runtime-security event — the row
// shape of the future `runtime_events` table (E2). Falco is the
// first source; priorities follow its syslog ladder.
type RuntimeEvent struct {
	At       time.Time `json:"at"`
	Priority string    `json:"priority"` // Emergency…Debug (syslog names)
	RuleName string    `json:"ruleName"`

	Namespace string `json:"namespace,omitempty"`
	PodName   string `json:"podName,omitempty"`

	// DetectedBehavior is the human-readable one-liner, e.g.
	// "exec syscall to /bin/sh in nginx-prod (user: root)".
	DetectedBehavior string `json:"detectedBehavior"`

	Source string `json:"source"` // integration ID, e.g. "falco"

	// Fields carries source-specific structured detail (syscall,
	// executable, user, …) without the schema having to know every
	// tool's vocabulary.
	Fields map[string]string `json:"fields,omitempty"`
}

// MetricSample is a backend-side normalized sample for providers
// whose ingest happens in the control plane (CRD counts, CIS score
// gauges). Agent-side scrape providers (OpenCost) ship through the
// agent proto instead and leave Signals.Samples empty.
type MetricSample struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
	Value  float64           `json:"value"`
	At     time.Time         `json:"at"`
}

// Signals is Normalize's output bundle. Any of the three slices may
// be empty — a cost provider emits no findings; a CIS provider emits
// findings + a score gauge but no runtime events.
type Signals struct {
	Findings []Finding
	Events   []RuntimeEvent
	Samples  []MetricSample
}

// CRDSignalProvider is the CRD-mode extension: the ingest sweep asks
// which CRDs to list per cluster and hands every object to Normalize.
// Only IngestCRD providers implement it.
type CRDSignalProvider interface {
	SignalProvider

	// IngestGVRs returns the CRD group/version/resources this
	// provider consumes. A cluster without the CRD lists with an
	// error — the sweep skips it (optional, same stance as
	// listOptionalCRD).
	IngestGVRs() []schema.GroupVersionResource
}

// SignalProvider is the optional contract for integrations that
// contribute structured signals. Optional in the same way
// Installable / Configurable are: the registry and REST surface work
// on plain Providers; signal machinery type-asserts for this
// interface when it needs to route raw payloads.
type SignalProvider interface {
	Provider

	// IngestMode declares which generic ingest machinery feeds this
	// provider. Constant per provider.
	IngestMode() IngestPattern

	// Normalize converts one raw payload (CRD object, webhook body,
	// job output — shape depends on IngestMode) into normalized
	// signals. Must be pure/deterministic: no cluster calls, no
	// stores — the ingest layer owns I/O, retries, and tenant
	// stamping.
	Normalize(ctx context.Context, raw any) (Signals, error)
}
