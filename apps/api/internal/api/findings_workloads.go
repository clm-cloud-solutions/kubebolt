package api

import (
	"net/http"
	"strconv"

	"github.com/kubebolt/kubebolt/apps/api/internal/findings"
)

// Workload-first view of the findings — GET /findings/workloads.
//
// The aggregation itself lives in findings.AggregateWorkloads, shared with
// Kobi's get_finding_workloads so the chat and the Security page rank the same
// workload the same way. This handler owns only what is HTTP: the scope, the
// lens and facets from the query string, and the page.
type workloadsResponse struct {
	Workloads []findings.WorkloadRow `json:"workloads"`
	// Total is the workload count in scope, before the page slice.
	Total    int `json:"total"`
	Page     int `json:"page"`
	PageSize int `json:"pageSize"`
	// The rest is findings.WorkloadView as-is; see its fields for why each exists.
	Findings   int                     `json:"findings"`
	Unassigned int                     `json:"unassigned"`
	TopImages  []findings.ImageRow     `json:"topImages,omitempty"`
	TopChecks  []findings.CheckRow     `json:"topChecks,omitempty"`
	Benchmarks []findings.BenchmarkRow `json:"benchmarks,omitempty"`
}

const workloadsPageSize = 25

func (h *handlers) handleListFindingWorkloads(w http.ResponseWriter, r *http.Request) {
	if h.findingsStore == nil {
		respondError(w, http.StatusServiceUnavailable, "findings are not available (persistence disabled)")
		return
	}
	q := r.URL.Query()
	requestedCluster, mayRead := h.findingsClusterFilter(r, q.Get("cluster"))
	scope := findings.Query{
		TenantID:  h.activeTenantID(r),
		ClusterID: requestedCluster,
		Status:    q.Get("status"),
	}
	if scope.Status == "" {
		scope.Status = findings.StatusActive
	}
	all, err := h.findingsStore.List(scope)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to list findings")
		return
	}

	group := q.Get("group")
	fSeverity := q.Get("severity")
	// The KIND facet has to be applied HERE, not just on the finding list.
	// Without it the chips changed the summary and left the table untouched:
	// picking "Secrets 2" still listed every workload in the lens, which reads as
	// a filter that quietly does nothing. The severity facet was already wired;
	// kind was not, and the two must behave the same way.
	//
	// Filtering the RECORDS rather than the finished rows is deliberate: under a
	// Secrets filter a workload with 30 CVEs and one leaked key must show the
	// KEY'S severities, not the CVEs'. Otherwise the bar describes findings the
	// filter just excluded.
	fKind := q.Get("kind")

	view := findings.AggregateWorkloads(all, func(rec *findings.Record) bool {
		return mayRead(rec.ClusterID) &&
			matchesSecurityGroup(rec, group) &&
			(fSeverity == "" || string(rec.Severity) == fSeverity) &&
			(fKind == "" || string(rec.Kind) == fKind)
	})

	resp := workloadsResponse{
		Total:      len(view.Workloads),
		Findings:   view.Findings,
		Unassigned: view.Unassigned,
		TopImages:  view.TopImages,
		TopChecks:  view.TopChecks,
		Benchmarks: view.Benchmarks,
	}
	page, pageSize := 1, workloadsPageSize
	if v, err := strconv.Atoi(q.Get("page")); err == nil && v > 1 {
		page = v
	}
	if v, err := strconv.Atoi(q.Get("pageSize")); err == nil && v > 0 && v <= findingsMaxPageSize {
		pageSize = v
	}
	resp.Page, resp.PageSize = page, pageSize
	rows := view.Workloads
	start := (page - 1) * pageSize
	if start >= len(rows) {
		rows = nil
	} else {
		end := start + pageSize
		if end > len(rows) {
			end = len(rows)
		}
		rows = rows[start:end]
	}
	resp.Workloads = rows

	respondJSON(w, http.StatusOK, resp)
}
