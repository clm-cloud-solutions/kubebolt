package collector

import (
	"fmt"
	"strings"
)

// InterfaceMatcher decides whether an interface name should be dropped
// from container_network_* / node_network_* emission. Exact entries
// match by map lookup (O(1)); entries ending in "*" match by prefix
// (O(n) over the small prefix set — typically 4-10 entries).
//
// Prefix support exists because the real cardinality bloat on cloud
// CNIs is per-pod veth peers whose names carry a hash suffix
// (azv<hash> on Azure CNI, veth<hash> on kind and many CNIs, eni<hash>
// on AWS VPC CNI, cali<hash> on Calico) — different for every pod, so
// no exact-match list can capture them. Measured on a 2,183-pod AKS
// cluster: 5,061 distinct interface values, ~85% of total active
// series (kubebolt-infra-azure docs/backend-proposals/
// agent-network-interface-prefix-match.md).
type InterfaceMatcher struct {
	exact    map[string]struct{}
	prefixes []string
}

// NewInterfaceMatcher builds a matcher from the parsed drop list.
// A bare "*" (would drop everything) and a "*" anywhere but the end
// (regex is deliberately unsupported) are configuration errors — the
// agent must refuse to boot rather than silently over- or under-drop.
func NewInterfaceMatcher(entries []string) (*InterfaceMatcher, error) {
	m := &InterfaceMatcher{exact: map[string]struct{}{}}
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if e == "*" {
			return nil, fmt.Errorf("dropNetworkInterfaces entry %q would drop every interface", e)
		}
		if i := strings.IndexByte(e, '*'); i >= 0 && i != len(e)-1 {
			return nil, fmt.Errorf("dropNetworkInterfaces entry %q: '*' is only supported as a trailing character", e)
		}
		if strings.HasSuffix(e, "*") {
			m.prefixes = append(m.prefixes, strings.TrimSuffix(e, "*"))
			continue
		}
		m.exact[e] = struct{}{}
	}
	return m, nil
}

// Drops reports whether the given interface name is in the drop set.
// Nil-safe: a nil matcher keeps every interface.
func (m *InterfaceMatcher) Drops(name string) bool {
	if m == nil {
		return false
	}
	if _, ok := m.exact[name]; ok {
		return true
	}
	for _, p := range m.prefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// Size returns the number of configured entries (exact + prefix), for
// boot logging. Nil-safe.
func (m *InterfaceMatcher) Size() int {
	if m == nil {
		return 0
	}
	return len(m.exact) + len(m.prefixes)
}
