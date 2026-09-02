package api

import (
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/findings"
	"github.com/kubebolt/kubebolt/apps/api/internal/integrations"
)

// Workload-first view of the findings.
//
// The unit of work is the WORKLOAD, not the finding. Forty-seven CVEs on one
// image are fixed by rebuilding that image once; a list ranked by CVE makes the
// operator visit the same workload forty-seven times to make a single change.
// Aggregating first also turns 449 rows into 18 — a list that fits on a screen
// and can be divided between people.
//
// Not every finding has a workload: a compliance control describes the cluster.
// Those are excluded here rather than bucketed under a synthetic "cluster" row,
// because a benchmark control is not a thing you rebuild — it belongs in the
// compliance view, which stays control-shaped.
type workloadRow struct {
	// ClusterID scopes the row. It is part of the identity, not decoration:
	// `coredns` in kube-system exists in EVERY cluster, so a key without it
	// merges them into one row with summed counts and a drill-down that mixes
	// findings from machines that have nothing to do with each other.
	ClusterID string `json:"clusterId,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`

	// Counts per severity — the badges. Kept as explicit fields rather than a
	// map so the shape is stable for the UI and a missing band renders as 0
	// instead of vanishing.
	Critical int `json:"critical"`
	High     int `json:"high"`
	Medium   int `json:"medium"`
	Low      int `json:"low"`
	Total    int `json:"total"`
	// Fixable is how many carry a concrete remedy — the share of this workload's
	// problems that can be closed without a judgement call.
	Fixable int `json:"fixable"`
	// Kinds counts the finding KINDS on this row — the answer to "what sort of
	// problem is this", which severity alone never gives: five criticals could be
	// packages to upgrade or a credential to rotate, and those are different
	// mornings.
	//
	// This replaced a `Sources` field that could not answer it. Sources named the
	// SCANNER, and CVE, misconfiguration, RBAC and exposed secrets all report
	// `trivy` — so every row read "trivy" and the field, though computed and
	// shipped, was never rendered. Wrong axis: the discriminator is the kind.
	//
	// A map rather than fields so a new kind (Kyverno, infra assessments) shows up
	// without a schema change; the UI just renders what it gets.
	Kinds map[string]int `json:"kinds,omitempty"`

	// Secrets is Kinds["exposed_secret"], hoisted because it drives the SORT and
	// the sort must not depend on map lookups scattered through a comparator.
	Secrets int `json:"secrets,omitempty"`
	// Image is what gets rebuilt. When a workload runs several, the first by
	// name is shown and Images says how many — the row must not imply there is
	// only one thing to fix when there are three.
	Image  string `json:"image,omitempty"`
	Images int    `json:"images,omitempty"`
	// OldestSeen is when the longest-standing finding here first appeared:
	// "open for 40 days" is the column that turns a list into a priority.
	OldestSeen time.Time `json:"oldestSeen,omitempty"`
}

type workloadsResponse struct {
	Workloads []workloadRow `json:"workloads"`
	// Total is the workload count in scope, before the page slice.
	Total    int `json:"total"`
	Page     int `json:"page"`
	PageSize int `json:"pageSize"`
	// Findings is how many individual findings those workloads carry — the
	// number the old list showed, kept so the two views can be reconciled.
	Findings int `json:"findings"`
	// Unassigned counts findings with no workload (compliance controls). Stated
	// rather than silently dropped: a total that does not add up is exactly the
	// kind of gap that erodes trust in the page.
	Unassigned int `json:"unassigned"`

	// TopImages ranks the images carrying the most findings. One bad image
	// explains several workloads — rebuilding it closes all of them at once,
	// which a per-workload list can never show. Empty for lenses with no image
	// data (config audit describes the manifest, not the artifact).
	TopImages []imageRow `json:"topImages,omitempty"`

	// TopChecks is the same idea for CONFIGURATION, where there is no image to
	// rebuild: the checks that fail across the most workloads. "Runs as root
	// user" failing on 44 of them is one manifest pattern to change, not 44
	// separate problems — the leverage a per-workload list cannot show, exactly
	// as with images.
	TopChecks []checkRow `json:"topChecks,omitempty"`

	// Benchmarks breaks compliance down by STANDARD. Fifty-five failing controls
	// presented as one pile treats CIS, the NSA guide and the two Pod Security
	// profiles as interchangeable; they are not, and an operator is usually held
	// to one of them specifically.
	//
	// Not a "top N": there are four, and hiding any would answer the question
	// wrong.
	Benchmarks []benchmarkRow `json:"benchmarks,omitempty"`
}

type benchmarkRow struct {
	Name     string `json:"name"`
	Failing  int    `json:"failing"`
	Critical int    `json:"critical"`
	High     int    `json:"high"`
	Medium   int    `json:"medium"`
	// Rollups is how many of these are aggregates of workload checks listed
	// under Configuration — stated so the number reconciles with the totals,
	// which exclude them.
	Rollups int `json:"rollups"`
}

type checkRow struct {
	// Title as stored ("AVD-KSV-0012: Runs as root user"); the UI splits the id
	// from the name the way it splits repository from tag.
	Title     string `json:"title"`
	Workloads int    `json:"workloads"`
	Findings  int    `json:"findings"`
	Critical  int    `json:"critical"`
	High      int    `json:"high"`
	Medium    int    `json:"medium"`
	// WorkloadNames (namespace-qualified) and Clusters place the check, same
	// contract as imageRow — counts alone leave "44 workloads" unfindable.
	WorkloadNames []string `json:"workloadNames,omitempty"`
	Clusters      []string `json:"clusters,omitempty"`
}

type imageRow struct {
	Image string `json:"image"`
	// Workloads is the leverage: how many rows this ONE rebuild would clear.
	Workloads int `json:"workloads"`
	Findings  int `json:"findings"`
	Critical  int `json:"critical"`
	High      int `json:"high"`
	// WorkloadNames (namespace-qualified) and Clusters name WHERE this image
	// runs. The counts alone
	// leave the operator with "7 workloads" and no way to find them — and on a
	// multi-cluster view, no way to tell whether that is seven places in one
	// cluster or the same Deployment in seven. Capped, because the point is to
	// place the image, not to reproduce the table below.
	WorkloadNames []string `json:"workloadNames,omitempty"`
	Clusters      []string `json:"clusters,omitempty"`
}

// imageContextCap bounds the names carried per image. Past a handful the
// tooltip stops placing the image and becomes a second list.
const imageContextCap = 6

// topImagesLimit keeps the panel a shortlist. Beyond a handful it stops being
// "where the leverage is" and becomes another list to scroll.
const topImagesLimit = 5

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
	// KEY'"'"'S severities, not the CVEs'"'"'. Otherwise the bar describes findings the
	// filter just excluded.
	fKind := q.Get("kind")

	byKey := map[string]*workloadRow{}
	kinds := map[string]map[string]int{}
	images := map[string]map[string]bool{}
	byImage := map[string]*imageRow{}
	byCheck := map[string]*checkRow{}
	byBenchmark := map[string]*benchmarkRow{}
	checkWorkloads := map[string]map[string]bool{}
	checkNames := map[string]map[string]bool{}
	checkClusters := map[string]map[string]bool{}
	imageWorkloads := map[string]map[string]bool{}
	imageClusters := map[string]map[string]bool{}
	imageNames := map[string]map[string]bool{}
	resp := workloadsResponse{}

	for i := range all {
		rec := &all[i]
		if !mayRead(rec.ClusterID) {
			continue
		}
		if !matchesSecurityGroup(rec, group) {
			continue
		}
		if fSeverity != "" && string(rec.Severity) != fSeverity {
			continue
		}
		if fKind != "" && string(rec.Kind) != fKind {
			continue
		}
		if rec.ResourceName == "" {
			resp.Unassigned++
			// Compliance controls carry no workload, so they are counted HERE,
			// before the skip — the benchmark breakdown is the only shape this
			// lens has, and dropping it with the resource would leave the
			// compliance tab with nothing beside its donut.
			if rec.Benchmark != "" {
				b, ok := byBenchmark[rec.Benchmark]
				if !ok {
					b = &benchmarkRow{Name: rec.Benchmark}
					byBenchmark[rec.Benchmark] = b
				}
				b.Failing++
				switch rec.Severity {
				case integrations.SeverityCritical:
					b.Critical++
				case integrations.SeverityHigh:
					b.High++
				case integrations.SeverityMedium:
					b.Medium++
				}
				if rec.Rollup {
					b.Rollups++
				}
			}
			continue
		}
		resp.Findings++
		key := rec.ClusterID + "|" + rec.ResourceNamespace + "/" + rec.ResourceKind + "/" + rec.ResourceName
		row, ok := byKey[key]
		if !ok {
			row = &workloadRow{
				ClusterID: rec.ClusterID,
				Kind:      rec.ResourceKind,
				Namespace: rec.ResourceNamespace,
				Name:      rec.ResourceName,
			}
			byKey[key] = row
			kinds[key] = map[string]int{}
		}
		row.Total++
		switch rec.Severity {
		case integrations.SeverityCritical:
			row.Critical++
		case integrations.SeverityHigh:
			row.High++
		case integrations.SeverityMedium:
			row.Medium++
		case integrations.SeverityLow:
			row.Low++
		}
		if rec.Remediation != "" {
			row.Fixable++
		}
		kinds[key][string(rec.Kind)]++
		if rec.Kind == integrations.FindingExposedSecret {
			row.Secrets++
		}

		// Group by CHECK too. Only meaningful where a finding names a repeatable
		// rule rather than a unique defect: a config-audit check fails the same
		// way on many workloads, while a CVE is specific to what an image ships.
		//
		// RBAC checks belong here for the same reason and even more strongly —
		// "Manage secrets" failing on thirteen ClusterRoles is one permission
		// pattern that got copied, and seeing the thirteen together is what makes
		// that visible.
		if isRepeatableCheck(rec.Kind) && rec.CISControl == "" && rec.Title != "" {
			chk, ok := byCheck[rec.Title]
			if !ok {
				chk = &checkRow{Title: rec.Title}
				byCheck[rec.Title] = chk
				checkWorkloads[rec.Title] = map[string]bool{}
				checkNames[rec.Title] = map[string]bool{}
				checkClusters[rec.Title] = map[string]bool{}
			}
			chk.Findings++
			switch rec.Severity {
			case integrations.SeverityCritical:
				chk.Critical++
			case integrations.SeverityHigh:
				chk.High++
			case integrations.SeverityMedium:
				chk.Medium++
			}
			checkWorkloads[rec.Title][key] = true
			checkNames[rec.Title][qualifiedName(rec.ResourceNamespace, rec.ResourceName)] = true
			checkClusters[rec.Title][rec.ClusterID] = true
		}
		if rec.Image != "" {
			if images[key] == nil {
				images[key] = map[string]bool{}
			}
			images[key][rec.Image] = true

			agg, ok := byImage[rec.Image]
			if !ok {
				agg = &imageRow{Image: rec.Image}
				byImage[rec.Image] = agg
				imageWorkloads[rec.Image] = map[string]bool{}
			}
			agg.Findings++
			switch rec.Severity {
			case integrations.SeverityCritical:
				agg.Critical++
			case integrations.SeverityHigh:
				agg.High++
			}
			imageWorkloads[rec.Image][key] = true
			if imageClusters[rec.Image] == nil {
				imageClusters[rec.Image] = map[string]bool{}
			}
			imageClusters[rec.Image][rec.ClusterID] = true
			if imageNames[rec.Image] == nil {
				imageNames[rec.Image] = map[string]bool{}
			}
			// Namespace-qualified: a bare name does not place the workload, and
			// the same name in two namespaces is two different things to fix.
			imageNames[rec.Image][rec.ResourceNamespace+"/"+rec.ResourceName] = true
		}
		if row.OldestSeen.IsZero() || rec.FirstSeen.Before(row.OldestSeen) {
			row.OldestSeen = rec.FirstSeen
		}
	}

	rows := make([]workloadRow, 0, len(byKey))
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		row := *byKey[k]
		if len(kinds[k]) > 0 {
			row.Kinds = kinds[k]
		}
		if imgs := images[k]; len(imgs) > 0 {
			names := make([]string, 0, len(imgs))
			for img := range imgs {
				names = append(names, img)
			}
			sort.Strings(names)
			row.Image, row.Images = names[0], len(names)
		}
		rows = append(rows, row)
	}

	// Worst first, by SEVERITY before volume: one critical outranks fifty highs,
	// because the question this list answers is "what do I touch first", not
	// "who has the biggest pile". Name breaks ties so the order is stable across
	// polls — a list that reshuffles every 15 seconds cannot be worked through.
	//
	// EXPOSED SECRETS OUTRANK EVERYTHING, whatever the counts. Severity alone put
	// them in the wrong place and it was not close: measured on the dev cluster,
	// a workload carrying a leaked AWS credential pair ranked 29th of 37 — page
	// TWO at 25 rows a page — because it had one critical while twenty-eight
	// workloads had two or more. Those are not comparable problems. A critical
	// CVE is a rebuild you schedule; a credential in a registry is already out,
	// and the clock started when the layer was pushed. A list that hides it
	// behind a wall of packages to upgrade is answering the wrong question.
	//
	// Only the FIRST key changes: inside each group the severity order is
	// untouched, so nothing else about the ranking moves.
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		switch {
		case (a.Secrets > 0) != (b.Secrets > 0):
			return a.Secrets > 0
		case a.Critical != b.Critical:
			return a.Critical > b.Critical
		case a.High != b.High:
			return a.High > b.High
		case a.Medium != b.Medium:
			return a.Medium > b.Medium
		case a.Total != b.Total:
			return a.Total > b.Total
		default:
			return a.Name < b.Name
		}
	})

	resp.Total = len(rows)
	page, pageSize := 1, workloadsPageSize
	if v, err := strconv.Atoi(q.Get("page")); err == nil && v > 1 {
		page = v
	}
	if v, err := strconv.Atoi(q.Get("pageSize")); err == nil && v > 0 && v <= findingsMaxPageSize {
		pageSize = v
	}
	resp.Page, resp.PageSize = page, pageSize
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

	// Ranked by findings, then by how many workloads it unlocks: between two
	// images with the same pile, the one running in more places is the better
	// hour spent.
	imgs := make([]imageRow, 0, len(byImage))
	for ref, agg := range byImage {
		agg.Workloads = len(imageWorkloads[ref])
		agg.WorkloadNames = cappedSorted(imageNames[ref], imageContextCap)
		agg.Clusters = cappedSorted(imageClusters[ref], imageContextCap)
		imgs = append(imgs, *agg)
	}
	sort.SliceStable(imgs, func(i, j int) bool {
		if imgs[i].Findings != imgs[j].Findings {
			return imgs[i].Findings > imgs[j].Findings
		}
		if imgs[i].Workloads != imgs[j].Workloads {
			return imgs[i].Workloads > imgs[j].Workloads
		}
		return imgs[i].Image < imgs[j].Image
	})
	if len(imgs) > topImagesLimit {
		imgs = imgs[:topImagesLimit]
	}
	resp.TopImages = imgs

	// Ranked by WORKLOADS first, not findings: a check is one manifest pattern,
	// so the number of places it has to be changed is the work — unlike an
	// image, where the pile of CVEs is what one rebuild clears.
	chks := make([]checkRow, 0, len(byCheck))
	for title, agg := range byCheck {
		agg.Workloads = len(checkWorkloads[title])
		agg.WorkloadNames = cappedSorted(checkNames[title], imageContextCap)
		agg.Clusters = cappedSorted(checkClusters[title], imageContextCap)
		chks = append(chks, *agg)
	}
	sort.SliceStable(chks, func(i, j int) bool {
		if chks[i].Workloads != chks[j].Workloads {
			return chks[i].Workloads > chks[j].Workloads
		}
		if chks[i].Findings != chks[j].Findings {
			return chks[i].Findings > chks[j].Findings
		}
		return chks[i].Title < chks[j].Title
	})
	if len(chks) > topImagesLimit {
		chks = chks[:topImagesLimit]
	}
	resp.TopChecks = chks

	// Worst first, by failing count; name breaks ties so the order holds across
	// polls. All of them ship — four standards is a list, not a ranking.
	bms := make([]benchmarkRow, 0, len(byBenchmark))
	for _, b := range byBenchmark {
		bms = append(bms, *b)
	}
	sort.SliceStable(bms, func(i, j int) bool {
		if bms[i].Failing != bms[j].Failing {
			return bms[i].Failing > bms[j].Failing
		}
		return bms[i].Name < bms[j].Name
	})
	resp.Benchmarks = bms

	respondJSON(w, http.StatusOK, resp)
}

// isRepeatableCheck reports whether a finding names a rule that can fail the
// same way in many places — which is what makes ranking by REACH meaningful. A
// CVE is a property of one image and would rank as a list of ones.
func isRepeatableCheck(kind integrations.FindingKind) bool {
	return kind == integrations.FindingMisconfig || kind == integrations.FindingRBACIssue
}

// qualifiedName places a resource for a human. Namespace-qualified when there is
// one, because the same name in two namespaces is two different things to fix —
// and bare when there is not, since a ClusterRole rendered as "/cluster-admin"
// reads as a path with a missing segment.
func qualifiedName(namespace, name string) string {
	if namespace == "" {
		return name
	}
	return namespace + "/" + name
}

// cappedSorted turns a set into a stable, bounded slice. Sorted so a tooltip
// does not reshuffle between polls, capped so it stays context rather than
// becoming a list of its own.
func cappedSorted(set map[string]bool, limit int) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for k := range set {
		if k != "" {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}
