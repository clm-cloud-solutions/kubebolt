package api

import (
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/findings"
	"github.com/kubebolt/kubebolt/apps/api/internal/integrations"
)

// Security findings API (E2 SEC-C) — the read surface behind the
// Security & Compliance dashboard and, later, Kobi's query_findings
// read-tool.

// findingsResponse carries the capped table rows plus a summary computed
// over the whole SCOPE — not over the filtered rows.
//
// The distinction is load-bearing. Two of the request's params describe
// WHAT WE ARE LOOKING AT (scope: cluster, lifecycle status; plus the org,
// which is never client-supplied); the rest are FACETS the user toggles
// in the UI (source, kind, severity). Folding facets into the summary
// broke the dashboard two ways:
//
//   - the source pills erased themselves. They are rendered from the keys
//     of BySource, so filtering to "trivy" returned a map with one key and
//     every other pill vanished from the DOM — you had to go back through
//     "all" to switch source.
//   - the KPIs silently re-scoped to the active filter, contradicting the
//     contract this type is named for. "Uncapped" was never the whole
//     promise: uncapped ≠ unfiltered.
//
// So the store is read ONCE with the scope, the summary is computed over
// that, and the facets are applied in memory to produce the rows. One
// round-trip, and the KPIs keep meaning the same thing while you click.
type findingsResponse struct {
	// Findings are the facet-filtered rows, capped at findingsTableCap.
	Findings []findings.Record `json:"findings"`
	// Total is the facet-filtered count BEFORE the cap — it drives the
	// "showing 200 of 259" footer, which with a hard cap and no pagination
	// is correctness, not polish.
	Total int `json:"total"`

	// Everything below is scope-wide and facet-independent.
	BySeverity map[string]int `json:"bySeverity"`

	// BySeverityCluster is the same tally broken out per cluster, so a caller can
	// FOLD it instead of trusting the org-wide sum.
	//
	// It exists because the team selector is a LENS, not a permission: it lives in
	// localStorage and never travels to the server (see the two-scope navigation
	// doc §4). A page that focuses one team therefore has to narrow client-side —
	// and it cannot recompute a summary from a page of 25 rows. Without this,
	// Home's KPIs described the whole entitlement while its list showed one team,
	// which is exactly the bug Fleet fixed in bf55e4e by folding per-cluster rows.
	BySeverityCluster map[string]map[string]int `json:"bySeverityCluster,omitempty"`
	BySource          map[string]int            `json:"bySource"`
	ByKind            map[string]int            `json:"byKind"`
	ActiveResolvable  int                       `json:"activeWithRemediation"`
	// NewLast24h counts findings first seen in the last day. Cannot be
	// derived client-side: the table caps at 200 rows and sorts by LastSeen,
	// not FirstSeen, so the newest arrivals may not even be in the payload.
	NewLast24h int `json:"newLast24h"`
	// ScopeTotal is the summary's denominator — the count the KPIs describe,
	// which is >= Total whenever a facet is active.
	ScopeTotal int `json:"scopeTotal"`
	// Rollups counts the aggregate rows excluded from every number above, so
	// the UI can say WHY the KPIs do not add up to the table's row count
	// instead of leaving the operator to notice the gap.
	Rollups int `json:"rollups"`

	// Posture — the numbers that answer "how bad is this?", which a pile of
	// raw counts does not. 449 findings means nothing on its own; 449 findings
	// across 18 workloads, all of them fixable, is a morning's work.
	//
	// AffectedResources is the distinct workloads carrying at least one finding
	// in this scope. It cannot be derived client-side: the table is paginated,
	// so the browser only ever sees a slice.
	AffectedResources int `json:"affectedResources"`
	// TopResource is where to start — the single workload with the most
	// findings. A ranked list of 449 rows never says that.
	TopResource      string `json:"topResource,omitempty"`
	TopResourceCount int    `json:"topResourceCount,omitempty"`

	// Page / PageSize describe the slice returned in Findings.
	Page     int `json:"page"`
	PageSize int `json:"pageSize"`
}

// findingsPageSize is a screenful, not a memory bound: a security list is read
// row by row, and 200 at once is a wall to scroll past rather than a list to
// work through.
const findingsPageSize = 25

// findingsMaxPageSize caps what a caller may ask for, so a script cannot turn
// the endpoint into a full-table dump by accident.
const findingsMaxPageSize = 200

func (h *handlers) handleListFindings(w http.ResponseWriter, r *http.Request) {
	if h.findingsStore == nil {
		respondError(w, http.StatusServiceUnavailable, "findings are not available (persistence disabled)")
		return
	}
	q := r.URL.Query()

	// Scope only. Org scoping uses the same discriminator as the metrics
	// handlers — empty in OSS/single-tenant (one tenant, nothing to filter),
	// the caller's org UUID in multi-tenant.
	// Team narrowing BEFORE the read, so an entitled-to-nothing caller does not
	// even pull the rows. See findings_scope.go.
	requestedCluster, mayRead := h.findingsClusterFilter(r, q.Get("cluster"))
	scope := findings.Query{
		TenantID:  h.activeTenantID(r),
		ClusterID: requestedCluster,
		Status:    q.Get("status"),
	}
	if scope.Status == "" {
		scope.Status = findings.StatusActive // dashboard default: what needs attention
	}

	all, err := h.findingsStore.List(scope)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to list findings")
		return
	}

	// Facets — the chips. Empty means "don't narrow on this dimension".
	fSource, fKind, fSeverity := q.Get("source"), q.Get("kind"), q.Get("severity")

	// Resource narrowing, for the workload drill-down. It has to be SERVER-side:
	// the list is paginated, so filtering a page in the browser asks "are this
	// workload's findings among the 25 I happen to be holding?" — and for all
	// but the first workload the answer is no, which renders as "nothing here"
	// on a row that just said 20 findings.
	fResource, fResourceNS := q.Get("resourceName"), q.Get("resourceNamespace")

	// group is the SCOPE of the Security sub-tab, not a chip: it selects which
	// question the page is answering, and the summary is computed within it.
	//
	// It exists because a CVE and a CIS control are not comparable. They are
	// fixed by different actions and triaged by different people, so ranking
	// them in one list forces an equivalence that does not exist — and because a
	// compliance control's count IS the sum of workload checks stored
	// separately, one list also added the same problem twice.
	//
	// "compliance" and "configuration" both live under kind=misconfig; what
	// separates them is whether the finding names a CONTROL (cluster posture) or
	// a RESOURCE (something to edit). Splitting on cisControl rather than
	// minting a new kind keeps every existing fingerprint stable — kind is part
	// of the identity, so a new one would resolve and recreate every compliance
	// finding for nothing.
	group := q.Get("group")

	page, pageSize := 1, findingsPageSize
	if v, err := strconv.Atoi(q.Get("page")); err == nil && v > 1 {
		page = v
	}
	if v, err := strconv.Atoi(q.Get("pageSize")); err == nil && v > 0 && v <= findingsMaxPageSize {
		pageSize = v
	}

	resp := findingsResponse{
		// ScopeTotal is filled in the loop below, NOT from len(all): the group
		// narrows the scope itself, so counting the raw store read would report
		// every finding in the org as the denominator of a single sub-tab.
		BySeverity:        map[string]int{},
		BySeverityCluster: map[string]map[string]int{},
		BySource:          map[string]int{},
		ByKind:            map[string]int{},
	}
	dayAgo := time.Now().UTC().Add(-24 * time.Hour)
	perResource := map[string]int{}

	rows := make([]findings.Record, 0, len(all))
	for i := range all {
		rec := &all[i]
		if !mayRead(rec.ClusterID) {
			continue
		}
		if !matchesSecurityGroup(rec, group) {
			continue
		}
		resp.ScopeTotal++

		// Summary: scope-wide, facet-independent — and rollup-free.
		//
		// A compliance control marked Rollup aggregates findings we ALSO store
		// individually: its "N failing" is arithmetically the count of failing
		// results from checks the sweep ingests one by one (verified on a live
		// cluster — 35 of 35 comparable controls matched EXACTLY). Counting both
		// inflates every KPI by the same problem twice: "52 root containers"
		// once as a control and again as 44 workload rows.
		//
		// The row still SHIPS — compliance posture is a real question, and
		// controls with no ingested check (control-plane, node) are the only way
		// to see that class at all. It is the counting that stops, not the
		// finding. Nothing actionable is lost either: a control's remediation is
		// a pointer ("see CIS … control 5.2.6"), never an instruction, and it is
		// the per-workload check that carries the actual fix.
		// Rollups leave the totals ONLY in the mixed view. Inside the compliance
		// tab the control IS the unit of work and nothing else is being counted
		// alongside it, so excluding aggregates there would report 30 of 55 rows
		// and invent a shortfall the operator cannot explain. The double-count
		// exists between GROUPS, so the correction belongs where they meet.
		if !rec.Rollup || group != "" {
			resp.BySeverity[string(rec.Severity)]++
			// Same rollup exclusion as the totals above — a per-cluster tally that
			// counted aggregates would fold back into an inflated sum.
			if rec.ClusterID != "" {
				per := resp.BySeverityCluster[rec.ClusterID]
				if per == nil {
					per = map[string]int{}
					resp.BySeverityCluster[rec.ClusterID] = per
				}
				per[string(rec.Severity)]++
			}
			resp.BySource[rec.Source]++
			resp.ByKind[string(rec.Kind)]++
			if rec.Remediation != "" {
				resp.ActiveResolvable++
			}
			if rec.FirstSeen.After(dayAgo) {
				resp.NewLast24h++
			}
		}
		if rec.Rollup {
			resp.Rollups++
		}
		// Posture is counted over the SCOPE, before facets and before the page
		// slice — the same reason the severity counts are.
		if rec.ResourceName != "" {
			// Keyed by CLUSTER too: the same workload name exists in every
			// cluster, and without it "workloads affected" collapses them and
			// under-reports the blast radius it exists to measure.
			perResource[rec.ClusterID+"|"+rec.ResourceNamespace+"/"+rec.ResourceName]++
		}

		// Rows: facet-filtered.
		if fResource != "" && rec.ResourceName != fResource {
			continue
		}
		if fResourceNS != "" && rec.ResourceNamespace != fResourceNS {
			continue
		}
		if fSource != "" && rec.Source != fSource {
			continue
		}
		if fKind != "" && string(rec.Kind) != fKind {
			continue
		}
		if fSeverity != "" && string(rec.Severity) != fSeverity {
			continue
		}
		rows = append(rows, *rec)
	}

	resp.Total = len(rows)
	// Real pagination, not a hard truncation. The old behaviour cut at 200 and
	// told the operator "showing 200 of 449" — honest, but it left the other 249
	// unreachable, which for a security list means findings that literally
	// cannot be looked at.
	resp.AffectedResources = len(perResource)
	resp.TopResource, resp.TopResourceCount = topResource(perResource)
	resp.Page, resp.PageSize = page, pageSize
	start := (page - 1) * pageSize
	switch {
	case start >= len(rows):
		rows = nil
	default:
		end := start + pageSize
		if end > len(rows) {
			end = len(rows)
		}
		rows = rows[start:end]
	}
	resp.Findings = rows
	respondJSON(w, http.StatusOK, resp)
}

// integrations import is load-bearing for the response docs even
// though only types flow through findings.Record's embedding.
var _ = integrations.FindingCVE

// matchesSecurityGroup narrows a finding to one Security sub-tab. An empty
// group means "everything", preserving the pre-split behaviour for any caller
// that has not adopted the tabs (Kobi's read-tool, scripts).
//
// The classification itself moved to findings.SecurityGroup, which the
// first-scan email also reads. Two implementations of "which lens is this"
// meant the email could quote a number no screen showed — and it did.
func matchesSecurityGroup(rec *findings.Record, group string) bool {
	if group == "" {
		return true
	}
	return findings.SecurityGroup(rec) == group
}

// topResource picks the workload carrying the most findings — the answer to
// "where do I start", which no ranking of individual rows provides. Ties break
// on name so the pick is stable across polls; a KPI that reshuffles every 15
// seconds reads as noise.
func topResource(counts map[string]int) (string, int) {
	type pair struct {
		name string
		n    int
	}
	best := pair{}
	names := make([]string, 0, len(counts))
	for k := range counts {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, name := range names {
		if counts[name] > best.n {
			best = pair{name, counts[name]}
		}
	}
	return best.name, best.n
}
