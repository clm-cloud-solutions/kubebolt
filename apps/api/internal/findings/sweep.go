package findings

import (
	"context"
	"log/slog"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"

	"github.com/kubebolt/kubebolt/apps/api/internal/integrations"
)

// The ingest sweep (E2 SEC-C): every interval, for every live cluster
// runtime, list each CRD provider's objects, Normalize them, and
// reconcile the store — upsert what's reported, resolve what stopped
// being reported (a CVE disappearing from the reports means the image
// was patched or the workload is gone).
//
// The sweep — not the provider — stamps tenant/cluster identity
// (anti-spoof stance, same as metric ingestion), preserves FirstSeen
// across re-ingests, and tolerates per-cluster failures (one broken
// cluster never stalls the fleet's findings).

// ConnectorIterator abstracts cluster.Manager.ForEachLiveConnector so
// the sweeper is testable with fakes and the findings package doesn't
// couple to the manager's full surface.
type ConnectorIterator func(fn func(tenant, cluster string, dyn dynamic.Interface, owner OwnerResolver))

// OwnerResolver collapses a resource to the workload an operator reasons about
// — in practice a ReplicaSet to its Deployment. Satisfied by cluster.Connector,
// which answers from its running informer cache at zero API cost.
//
// It exists because the resource name is part of a finding's fingerprint and
// Trivy only ever reports the ReplicaSet. That name carries a pod-template-hash
// that changes on every rollout, so without this the same CVE on the same image
// resolves and reappears as new after each deploy, resetting firstSeen. Proven
// on a live cluster: a no-op `rollout restart` moved 8 findings from one
// ReplicaSet to another 20 minutes later, with the clock back at zero.
//
// nil is a valid resolver — findings then keep whatever the scanner reported.
//
// The third return says whether the answer is SETTLED. False means the lookup
// could not be performed on this pass (a cold or incomplete informer cache) and
// a later pass would answer differently — so the caller must NOT fingerprint on
// it. See Connector.WorkloadOwner for why a permanently-absent informer and a
// bare ReplicaSet both count as settled.
type OwnerResolver interface {
	WorkloadOwner(namespace, kind, name string) (string, string, bool)
}

// SweepDelta is what ONE COMPLETE pass changed for a (tenant, cluster, source).
//
// The sweep has always computed both halves of this and thrown them away:
// `firstSeen` tells a reported fingerprint apart from one already stored, and
// the reconcile loop knows exactly which actives stopped being reported. This
// type is that knowledge, published.
//
// Only emitted for a pass that listed EVERY GVR successfully — the same gate
// that guards resolution. A partial view must not claim anything is new any
// more than it may claim something is gone.
type SweepDelta struct {
	TenantID, ClusterID, Source string

	// New are findings whose fingerprint was not stored before this pass.
	New []Record
	// Resolved are findings that stopped being reported.
	Resolved []Record
	// Active is the total after this pass — the denominator a summary needs.
	Active int

	// Seeding marks the FIRST pass ever for this (tenant, cluster, source):
	// the scanner was just installed and everything it found is "new" only in
	// the trivial sense. A consumer must send one summary here, never one
	// message per finding — a first Trivy scan on a real cluster is hundreds
	// of findings, and the mailbox that receives hundreds of messages in one
	// afternoon is muted by the evening.
	//
	// Derived from the store being empty for this triple rather than from a
	// "seeded" flag of its own. No extra table, no migration, and one cheap
	// indexed lookup per pass. The trade is deliberate: if a cluster keeps no
	// finding at all for a whole retention window, the next batch seeds again
	// — and treating that as a fresh baseline is the correct reading anyway.
	Seeding bool
}

// DeltaFunc receives one delta per (tenant, cluster, source) per complete pass.
// It runs INSIDE the sweep, so an implementation must not block: hand off to a
// queue and return.
type DeltaFunc func(SweepDelta)

// ClusterFunc runs ONCE per cluster per pass, after every provider has been
// swept — so what it observes in the store is the whole cluster, every scanner,
// not one source's slice of it.
//
// That distinction is the entire reason it exists. A consumer that reacts per
// (cluster, source) speaks in a unit the product does not have: the Security
// page has no "Trivy" tab, it has lenses that each aggregate several scanners.
// A message built from one source's delta therefore quotes numbers no screen
// displays, and a customer installing three scanners lives one event and
// receives three notifications.
//
// Same non-blocking contract as DeltaFunc: it runs inside the sweep.
type ClusterFunc func(tenantID, clusterID string)

type Sweeper struct {
	store     Store
	providers []integrations.CRDSignalProvider
	iterate   ConnectorIterator
	interval  time.Duration
	onDelta   DeltaFunc
	onCluster ClusterFunc
}

// WithDelta registers the delta sink. nil (the default) keeps the sweep exactly
// as it was — OSS and every existing test take that path.
//
// NOTHING WIRES THIS TODAY. The first-scan summary used to, and moved to
// WithClusterPass when it became one message per cluster instead of one per
// scanner. What remains here is the per-source "these appeared / these closed"
// signal, which is what the event lanes need and the notification rail cannot
// carry yet. Left standing rather than deleted and rebuilt: the seeding probe
// costs nothing while onDelta is nil, and the semantics it encodes (only for a
// pass that listed every GVR; FirstSeen preserved across re-ingests) are the
// expensive part to get right, not the plumbing.
func (s *Sweeper) WithDelta(fn DeltaFunc) *Sweeper {
	s.onDelta = fn
	return s
}

// WithClusterPass registers the end-of-cluster hook. Same nil default.
func (s *Sweeper) WithClusterPass(fn ClusterFunc) *Sweeper {
	s.onCluster = fn
	return s
}

// NewSweeper builds the sweep loop. interval<=0 defaults to 10m —
// vulnerability reports move at scanner cadence, not sub-minute.
func NewSweeper(store Store, providers []integrations.CRDSignalProvider, iterate ConnectorIterator, interval time.Duration) *Sweeper {
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	return &Sweeper{store: store, providers: providers, iterate: iterate, interval: interval}
}

// bootRetry is how often the boot sweep is retried while NO live connector
// exists yet. See Run.
const bootRetry = 15 * time.Second

// Run blocks until ctx cancels: an immediate sweep (a fresh boot shouldn't wait
// 10 minutes to show findings), then the ticker.
//
// The boot sweep RACES cluster connection, and on agent-proxy it usually loses:
// the agent registers seconds-to-minutes after the API comes up, so the sweep
// fires against an empty connector list, does nothing, and the operator stares
// at an empty Security page for a full interval. Observed exactly that on
// 2026-08-04 — API up at 08:28:37, agent connected moments later, next findings
// only due at 08:38:38.
//
// So the boot sweep retries on a short cadence until it actually visits a
// connector, then hands over to the normal ticker. A cluster that never
// connects just keeps retrying cheaply: the retry costs one iteration over an
// empty list, no I/O.
func (s *Sweeper) Run(ctx context.Context) {
	for !s.SweepOnce(ctx) {
		select {
		case <-ctx.Done():
			return
		case <-time.After(bootRetry):
		}
	}
	tick := time.NewTicker(s.interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.SweepOnce(ctx)
		}
	}
}

// SweepOnce visits every live runtime once and reports whether it reached ANY —
// which is what lets Run tell "swept, nothing to report" apart from "there was
// nothing to sweep yet". Exported for tests and for a future manual "rescan
// now" action.
func (s *Sweeper) SweepOnce(ctx context.Context) bool {
	sweepStart := time.Now()
	swept := false
	s.iterate(func(tenant, clusterName string, dyn dynamic.Interface, owner OwnerResolver) {
		if dyn == nil {
			return // connector without dynamic client (mode limitations)
		}
		swept = true
		for _, p := range s.providers {
			s.sweepProvider(ctx, p, tenant, clusterName, dyn, owner)
		}
		// AFTER every provider, never inside the loop: a consumer here is
		// entitled to read the store and see this cluster whole.
		if s.onCluster != nil {
			s.onCluster(tenant, clusterName)
		}
	})
	// One line per pass, always — including the pass that found nothing. A job
	// that only speaks when it acts cannot be told apart from a broken one.
	// Milliseconds, not slog.Duration: the JSON handler serializes a Duration as
	// raw nanoseconds, and "374897250" is a number nobody reads as 375ms.
	slog.Info("findings sweep done",
		slog.Bool("visited_any", swept),
		slog.Int64("ms", time.Since(sweepStart).Milliseconds()))
	return swept
}

func (s *Sweeper) sweepProvider(ctx context.Context, p integrations.CRDSignalProvider, tenant, clusterName string, dyn dynamic.Interface, owner OwnerResolver) {
	source := p.Meta().ID
	now := time.Now().UTC()

	// Existing actives for THIS (tenant, cluster, source): the baseline
	// for FirstSeen preservation and for resolving the disappeared.
	existing, err := s.store.List(Query{TenantID: tenant, ClusterID: clusterName, Source: source, Status: StatusActive})
	if err != nil {
		slog.Warn("findings sweep: list existing failed",
			slog.String("source", source), slog.String("cluster", clusterName), slog.String("error", err.Error()))
		return
	}
	firstSeen := make(map[string]time.Time, len(existing))
	for i := range existing {
		firstSeen[existing[i].Fingerprint] = existing[i].FirstSeen
	}

	// Has this triple EVER stored a finding? Asked without a status filter and
	// capped at one row, so it stays an indexed lookup — and asked separately
	// from `existing` on purpose: that one is scoped to actives because a
	// finding that went away and came back must get a fresh FirstSeen, which is
	// a different question from whether we have ever seen this cluster's
	// scanner at all. Only computed when someone is listening.
	seeding := false
	if s.onDelta != nil {
		any, err := s.store.List(Query{TenantID: tenant, ClusterID: clusterName, Source: source, Limit: 1})
		if err != nil {
			// Not fatal to the sweep, but the delta would be a lie: an
			// unreadable store cannot tell a first scan from a routine pass,
			// and guessing wrong sends hundreds of alerts as if they were new.
			slog.Warn("findings sweep: seeding probe failed — delta suppressed for this pass",
				slog.String("source", source), slog.String("cluster", clusterName),
				slog.String("error", err.Error()))
			return
		}
		seeding = len(any) == 0
	}

	allListed := true
	unsettled := 0 // findings skipped because the owner cache could not answer yet
	seen := make(map[string]bool)
	var fresh []Record // fingerprints this pass stored for the first time
	for _, gvr := range p.IngestGVRs() {
		lctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		listStart := time.Now()
		list, err := dyn.Resource(gvr).Namespace(metav1.NamespaceAll).List(lctx, metav1.ListOptions{})
		listMS := time.Since(listStart).Milliseconds()
		cancel()
		if err != nil {
			// CRD not installed in this cluster (or transient failure):
			// optional, same stance as listOptionalCRD. Crucially we do
			// NOT resolve anything off a failed list — a flaky apiserver
			// must not mass-resolve real findings. And because findings
			// are keyed by SOURCE (not by GVR), one failed list poisons
			// the whole provider's reconcile: a vuln-list hiccup while
			// the compliance list succeeds must not resolve the CVEs.
			//
			// Logged at DEBUG, not WARN: "CRD absent" is the common, healthy
			// case on most clusters and would be pure noise at WARN. But
			// SOMETHING must be emitted — without it a 403 (missing RBAC on an
			// in-cluster deploy), an apiserver hiccup, and "scanner not
			// installed" are indistinguishable from the outside, and the
			// operator debugs an empty dashboard with zero signal. Keep this
			// probe: it is the only thing that tells those three apart.
			slog.Debug("findings sweep: list failed (CRD absent, RBAC denied, or transient)",
				slog.String("source", source),
				slog.String("cluster", clusterName),
				slog.String("gvr", gvr.String()),
				slog.String("error", err.Error()))
			allListed = false
			continue
		}
		// Cost, per GVR, on every sweep. These LISTs are cluster-wide and SERIAL,
		// with a 15s timeout each, and on an agent-proxy cluster every one of
		// them crosses the gRPC tunnel. That is the documented over-fetch
		// anti-pattern, and it grew from 2 GVRs to 3 when config-audit landed —
		// so the number has to be visible before anyone adds a fourth.
		//
		// Logged at INFO with the object count, because "slow" and "many
		// objects" are different problems and a duration alone cannot tell them
		// apart.
		slog.Info("findings sweep: listed",
			slog.String("source", source),
			slog.String("cluster", clusterName),
			slog.String("gvr", gvr.Resource),
			slog.Int("objects", len(list.Items)),
			slog.Int64("ms", listMS))

		for i := range list.Items {
			sig, err := p.Normalize(ctx, &list.Items[i])
			if err != nil {
				slog.Warn("findings sweep: normalize failed",
					slog.String("source", source), slog.String("object", list.Items[i].GetName()), slog.String("error", err.Error()))
				continue
			}
			for _, f := range sig.Findings {
				// Collapse to the owning workload BEFORE fingerprinting: the
				// resource name is part of the identity, so doing it after
				// would leave the rollout churn in place and merely relabel it.
				if owner != nil && f.ResourceKind != "" {
					var settled bool
					f.ResourceKind, f.ResourceName, settled = owner.WorkloadOwner(
						f.ResourceNamespace, f.ResourceKind, f.ResourceName)
					if !settled {
						// The cache could not answer THIS time. Writing now would
						// fingerprint on a ReplicaSet name carrying a pod-template
						// hash, minting a second identity for a finding that already
						// has one — and resolving the first as "gone". Skip it: the
						// next pass, ten minutes later, writes it once and correctly.
						//
						// allListed goes false for the same reason a failed list does:
						// this pass no longer knows the full set, so nothing may be
						// resolved off it.
						unsettled++
						allListed = false
						continue
					}
				}
				fp := Fingerprint(f)
				// Whether THIS pass already reported this fingerprint. `seen` is
				// a map so the reconcile has always been immune to repeats, but
				// `fresh` is a slice: without this the same finding lands in the
				// delta once per object that reports it.
				//
				// Repeats are the norm, not the exception. Trivy emits a report
				// per ReplicaSet and the owner resolver collapses them onto one
				// Deployment, so a CVE on a workload with three revisions arrives
				// three times with the same fingerprint. Measured on the first
				// live baseline: 1857 reported against 963 stored, a 1.9×
				// inflation in a message whose entire job is to state the size of
				// the problem accurately.
				repeat := seen[fp]
				seen[fp] = true
				first := now
				if prev, ok := firstSeen[fp]; ok {
					first = prev
				}
				rec := &Record{
					TenantID: tenant, ClusterID: clusterName, Fingerprint: fp,
					Finding: f, Status: StatusActive, FirstSeen: first, LastSeen: now,
				}
				if err := s.store.Upsert(rec); err != nil {
					slog.Warn("findings sweep: upsert failed",
						slog.String("source", source), slog.String("fingerprint", fp), slog.String("error", err.Error()))
					continue // a row that failed to store is not news
				}
				// New = absent from the ACTIVE baseline. That includes a
				// finding returning after it was resolved, and it should: the
				// image was patched and regressed, which is exactly what the
				// owner needs told again.
				if _, known := firstSeen[fp]; !known && !repeat && s.onDelta != nil {
					fresh = append(fresh, *rec)
				}
			}
		}
	}

	// One line per pass that skipped anything, at WARN: a cold cache on the
	// first pass after a reconnect is expected and self-heals, but the SAME
	// count every ten minutes means the informer never filled — a real fault
	// that would otherwise show up only as a permanently short dashboard.
	if unsettled > 0 {
		slog.Warn("findings sweep: skipped findings whose owner could not be resolved yet",
			slog.String("source", source), slog.String("cluster", clusterName),
			slog.Int("skipped", unsettled))
	}

	// Reconcile: actives no longer reported → resolved. Gated on EVERY
	// list having succeeded so failures never mass-resolve.
	if !allListed {
		return
	}
	resolved := 0
	var closed []Record
	byFingerprint := make(map[string]int, len(existing))
	for i := range existing {
		byFingerprint[existing[i].Fingerprint] = i
	}
	for fp := range firstSeen {
		if !seen[fp] {
			if err := s.store.MarkResolved(tenant, clusterName, fp, now); err == nil {
				resolved++
				if s.onDelta != nil {
					// The record as it was while active — MarkResolved has
					// already flipped the stored copy, and a consumer writing
					// "these closed" needs what closed, not an empty shell.
					if i, ok := byFingerprint[fp]; ok {
						closed = append(closed, existing[i])
					}
				}
			}
		}
	}
	if len(seen) > 0 || resolved > 0 {
		slog.Info("findings sweep", slog.String("source", source), slog.String("cluster", clusterName),
			slog.Int("active", len(seen)), slog.Int("resolved", resolved))
	}

	// Published last, and only from here: everything above this line either
	// listed completely or returned early. Nothing downstream can be told that
	// a finding is new or gone off a pass that did not see the whole picture.
	if s.onDelta != nil && (len(fresh) > 0 || len(closed) > 0) {
		s.onDelta(SweepDelta{
			TenantID: tenant, ClusterID: clusterName, Source: source,
			New: fresh, Resolved: closed, Active: len(seen), Seeding: seeding,
		})
	}
}
