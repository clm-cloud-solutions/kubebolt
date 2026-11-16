package cluster

import (
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// ExternalNameTarget follows a full in-cluster Service name
// ("<svc>.<ns>.svc.<cluster domain>") through ExternalName Services and
// returns the host outside the cluster the chain ends at, with the chain
// spelled "ns/svc → ns2/svc2". ok is false for an ordinary or selectorless
// Service, a name that is not a full Service name, a Service that cannot be
// read, a loop, more than maxServiceHops hops, or a chain that stays inside
// the cluster.
//
// The flows name a dependency behind an ExternalName by the Service it was
// dialed through, so the classifier read it as in-cluster and told the model
// not to use a cloud connector. In the lab, orders-api's database
// (shop-data/shop-db → db.shop.internal, an RDS instance) was classified
// that way, and the investigation did not look in AWS (finding #88).
func (c *Connector) ExternalNameTarget(host string) (target, via string, ok bool) {
	if c == nil || c.serviceLister == nil {
		return "", "", false
	}
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	visited := map[string]bool{}
	var chain []string
	for hop := 0; hop <= maxServiceHops; hop++ {
		svcNS, name, _, isSvc := targetServiceName(host)
		if !isSvc {
			// Out of the cluster domain: the chain's end, unless it never left.
			if hop == 0 || isInClusterHost(host, func(string) bool { return false }) {
				return "", "", false
			}
			return host, strings.Join(chain, " → "), true
		}
		key := svcNS + "/" + name
		if visited[key] || !c.readableIn("services", svcNS) {
			return "", "", false
		}
		visited[key] = true
		svc, err := c.serviceLister.Services(svcNS).Get(name)
		if err != nil || svc == nil || svc.Spec.Type != corev1.ServiceTypeExternalName {
			return "", "", false
		}
		next, ok := parseHostPort("", svc.Spec.ExternalName, true)
		if !ok {
			return "", "", false
		}
		chain = append(chain, key)
		host = strings.TrimSuffix(next.host, ".")
	}
	return "", "", false
}
