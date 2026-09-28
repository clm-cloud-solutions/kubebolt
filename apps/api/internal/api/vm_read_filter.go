package api

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// vmReadFilter is the isolation a caller-written PromQL read carries to
// VictoriaMetrics as ONE `extra_filters[]` parameter, which VM applies to every
// series selector of the query itself.
//
// Why this and not only the rewriter (injectMatcherExpr): the rewriter is a
// regex over the query text, and a query an attacker writes is not one of "our
// query shapes". It pinned only `{...}` selectors and bare names under a list
// of prefixes (so `up`, `process_*`, `go_*` went out unscoped), it skipped any
// selector whose text merely CONTAINED the label name
// (`{x_tenant_id=""}`), and a MetricsQL or-filter (`{a="" or b=""}`) kept the
// pins on its first branch only. Any of them read every org's series. VM's
// extra_filters is enforced by the storage on the parsed query; verified on
// v1.148 against bare names, decoy labels, or-filters, label_replace,
// subqueries, `@` and `or on()`.
//
// It MUST be a single parameter: VM joins several extra_filters[] with OR, so
// a second one would widen the read instead of narrowing it. The rewriter still
// runs, as a second layer and so the query text keeps saying what it reads.
type vmReadFilter struct {
	tenant   string   // tenant_id to pin; "" = no tenant axis (OSS)
	cluster  string   // exact cluster_id; used when clusters is unset
	clusters []string // allowed cluster_id set (fleet reads)
	setOn    bool     // clusters is authoritative, even when empty
}

// matcher renders the filter as one selector, or "" when it pins nothing.
func (f vmReadFilter) matcher() string {
	var parts []string
	if f.tenant != "" {
		parts = append(parts, fmt.Sprintf("tenant_id=%q", f.tenant))
	}
	switch {
	case f.setOn:
		ids := append([]string(nil), f.clusters...)
		if len(ids) == 0 {
			ids = []string{noClusterUIDSentinel}
		}
		sort.Strings(ids)
		quoted := make([]string, len(ids))
		for i, id := range ids {
			quoted[i] = regexp.QuoteMeta(id)
		}
		parts = append(parts, fmt.Sprintf("cluster_id=~%q", "^("+strings.Join(quoted, "|")+")$"))
	case f.cluster != "":
		parts = append(parts, fmt.Sprintf("cluster_id=%q", f.cluster))
	}
	if len(parts) == 0 {
		return ""
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// apply sets the filter on the upstream request's parameters, replacing any
// extra_filters / extra_label already there (the params are built by the
// server, but a stray copy must never survive to widen the read).
func (f vmReadFilter) apply(params url.Values) {
	params.Del("extra_filters[]")
	params.Del("extra_filters")
	params.Del("extra_label")
	if m := f.matcher(); m != "" {
		params.Set("extra_filters[]", m)
	}
}
