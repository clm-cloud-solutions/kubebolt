package api

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Manager exposes two parallel families of API, and both compile equally
// well at the call site:
//
//   - ctx-aware, resolved per (org, cluster): Connector(ctx), Collector(ctx),
//     Engine(ctx), MetricsOnlyClusterID(ctx). These read the RuntimeKey the
//     middleware stashed, so they answer for the ORG making the request.
//
//   - the global slot, which takes no arguments and answers for whoever
//     switched cluster most recently — ANY org: ActiveContext(), ConnError(),
//     ActiveAgentProxyClusterID(), and the raw m.connector field behind them.
//
// Nothing in the type system, the naming, or code review reliably tells the two
// apart, so every new handler that needs "which cluster am I on?" is a coin
// flip. That coin came up wrong at least ten times before anyone noticed, and
// each one surfaced separately, in production, disguised as a different symptom
// (a fleet card showing another tenant's cloud provider; a 409 handing a
// customer admin another org's cluster UID; Kobi told it was looking at a
// cluster that wasn't the caller's).
//
// This test makes the rule executable instead of customary: inside package api,
// there is NO legitimate reason to read the global slot. Every request carries a
// context; use the ctx-aware family.
//
// The budget is now ZERO: every site has been migrated to the ctx-aware family,
// so any read of the global slot in this package fails the build outright. The
// tag + budget machinery stays because it is what let the debt be paid down in
// steps, and because a future exemption must be argued in writing rather than
// merged quietly. See docs/55-the-shared-seat.md.
func TestNoSharedSeatReadsInHandlers(t *testing.T) {
	// Reading any of these from package api yields the global active runtime,
	// not the caller's.
	forbidden := []string{
		".ActiveContext(",
		".ConnError(",
		".ActiveAgentProxyClusterID(",
	}

	// A tagged line is KNOWN debt, not an exemption. The tag must carry a reason
	// after it so the diff shows why the line still exists.
	const debtTag = "kb:shared-seat-debt"

	// Lower this when you fix a site. Never raise it: a new read of the global
	// slot in this package is the bug this test exists to stop.
	const debtBudget = 0

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	var untagged []string
	tagged := 0

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		lines := strings.Split(string(src), "\n")
		for i, line := range lines {
			if !containsAny(line, forbidden) {
				continue
			}
			// The tag may sit on the offending line or on the line above it —
			// the call is sometimes inside a multi-line expression where a
			// trailing comment would not fit.
			prev := ""
			if i > 0 {
				prev = lines[i-1]
			}
			if strings.Contains(line, debtTag) || strings.Contains(prev, debtTag) {
				tagged++
				continue
			}
			untagged = append(untagged,
				fmt.Sprintf("%s:%d\t%s", filepath.Join("internal/api", name), i+1, strings.TrimSpace(line)))
		}
	}

	if len(untagged) > 0 {
		t.Errorf(`%d read(s) of the global active runtime in package api.

This answers for whichever org switched cluster most recently, NOT the caller.
Use the ctx-aware family instead:

    h.manager.ActiveContext()             -> h.manager.ActiveContextFor(r.Context())
    h.manager.ConnError()                 -> h.manager.ConnErrorFor(r.Context())
    h.manager.ActiveAgentProxyClusterID() -> h.manager.ActiveAgentProxyClusterIDFor(r.Context())

If you genuinely cannot, tag the line "// %s <reason>" and lower nothing —
but be aware every existing tag is a bug waiting to be reported.

%s`, len(untagged), debtTag, strings.Join(untagged, "\n"))
	}

	if tagged > debtBudget {
		t.Errorf("tagged shared-seat debt grew to %d (budget %d) — tagging a NEW site is not a fix; "+
			"the budget exists so the number can only go down", tagged, debtBudget)
	}
	if tagged < debtBudget {
		t.Errorf("tagged shared-seat debt is down to %d but the budget still says %d — "+
			"lower debtBudget to %d so the progress is locked in", tagged, debtBudget, tagged)
	}
}

func containsAny(s string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}
