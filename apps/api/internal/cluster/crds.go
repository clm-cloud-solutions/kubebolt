package cluster

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Optional standard CRDs surfaced as first-class resources (Sprint 3),
// accessed via the dynamic client — the Gateway/HTTPRoute pattern — because
// they may not be installed (no typed listers, list errors → empty, not a
// failure). cert-manager Certificate, ArgoCD Application, VPA, and the Cilium
// L3-L7 policy CRDs (present only on Cilium clusters).
var optionalCRDGVRs = map[string]schema.GroupVersionResource{
	"certificates": {Group: "cert-manager.io", Version: "v1", Resource: "certificates"},
	"argocdapps":   {Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"},
	"vpas":         {Group: "autoscaling.k8s.io", Version: "v1", Resource: "verticalpodautoscalers"},
	// Cilium policy CRDs (cilium.io/v2). CNP is namespaced; CCNP is
	// cluster-scoped (see isClusterScoped). Both carry L3/L4 + L7 (http/dns/
	// kafka) rules — the layer the standard networking.k8s.io NetworkPolicy
	// can't express and that KubeBolt previously couldn't surface.
	"ciliumnetworkpolicies":            {Group: "cilium.io", Version: "v2", Resource: "ciliumnetworkpolicies"},
	"ciliumclusterwidenetworkpolicies": {Group: "cilium.io", Version: "v2", Resource: "ciliumclusterwidenetworkpolicies"},
}

// isOptionalCRD reports whether a resource type is one of the dynamic-client
// optional CRDs (used by the GetResources/GetResourceDetail dispatch).
func isOptionalCRD(rtype string) bool {
	_, ok := optionalCRDGVRs[rtype]
	return ok
}

// listOptionalCRD lists one optional CRD type via the dynamic client and maps
// each item to the frontend shape. Returns nil when the dynamic client is
// absent or the CRD isn't installed (list error) — these are optional.
func (c *Connector) listOptionalCRD(rtype, namespace string) []map[string]interface{} {
	gvr, ok := optionalCRDGVRs[rtype]
	if !ok || c.dynamicClient == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var list *unstructured.UnstructuredList
	var err error
	if namespace != "" {
		list, err = c.dynamicClient.Resource(gvr).Namespace(namespace).List(ctx, metav1.ListOptions{})
	} else {
		list, err = c.dynamicClient.Resource(gvr).List(ctx, metav1.ListOptions{})
	}
	if err != nil {
		return nil // CRD not installed in this cluster
	}
	items := make([]map[string]interface{}, 0, len(list.Items))
	for i := range list.Items {
		items = append(items, optionalCRDToMap(rtype, &list.Items[i]))
	}
	return items
}

// getOptionalCRD fetches one optional CRD instance for the detail view.
func (c *Connector) getOptionalCRD(rtype, namespace, name string) (map[string]interface{}, error) {
	gvr, ok := optionalCRDGVRs[rtype]
	if !ok {
		return nil, fmt.Errorf("unknown optional CRD %q", rtype)
	}
	if c.dynamicClient == nil {
		return nil, fmt.Errorf("%s not available (CRD not installed or dynamic client unavailable)", rtype)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Cluster-scoped optional CRDs (CCNP) have no namespace — calling
	// .Namespace("").Get() on a cluster-scoped resource errors, so branch the
	// same way GetResourceYAML does.
	var obj *unstructured.Unstructured
	var err error
	if isClusterScoped(rtype) {
		obj, err = c.dynamicClient.Resource(gvr).Get(ctx, name, metav1.GetOptions{})
	} else {
		obj, err = c.dynamicClient.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	}
	if err != nil {
		return nil, err
	}
	base := optionalCRDToMap(rtype, obj)
	// Cilium policies get the same "which pods does this select?" affordance as
	// NetworkPolicy — the endpointSelector resolved against live pod labels.
	if rtype == "ciliumnetworkpolicies" || rtype == "ciliumclusterwidenetworkpolicies" {
		if sel, nsFilter := ciliumEndpointLabelSelector(obj.Object); sel != nil {
			matchNS := namespace // CNP: pods in the policy's own namespace
			if rtype == "ciliumclusterwidenetworkpolicies" {
				matchNS = nsFilter // CCNP: cluster-wide ("") unless the policy pins a namespace
			}
			matched := c.podsMatchingSelector(matchNS, sel)
			base["matchedPods"] = matched
			base["matchedPodCount"] = len(matched)
		}
	}
	return base, nil
}

// optionalCRDToMap renders one optional CRD's unstructured object into the
// frontend map shape, with per-type status/field extraction.
func optionalCRDToMap(rtype string, item *unstructured.Unstructured) map[string]interface{} {
	m := map[string]interface{}{
		"name":        item.GetName(),
		"namespace":   item.GetNamespace(),
		"labels":      safeLabels(item.GetLabels()),
		"annotations": safeAnnotations(item.GetAnnotations()),
		"createdAt":   item.GetCreationTimestamp().Time.Format(time.RFC3339),
		"age":         formatAge(item.GetCreationTimestamp().Time),
	}
	spec, _ := item.Object["spec"].(map[string]interface{})
	status, _ := item.Object["status"].(map[string]interface{})

	switch rtype {
	case "certificates":
		m["status"] = "Unknown"
		if ready := conditionStatus(status, "Ready"); ready != "" {
			if ready == "True" {
				m["status"] = "Ready"
			} else {
				m["status"] = "NotReady"
			}
		}
		m["issuer"] = nestedString(spec, "issuerRef", "name")
		m["secretName"] = stringField(spec, "secretName")
		m["commonName"] = stringField(spec, "commonName")
		if dns := stringSlice(spec, "dnsNames"); len(dns) > 0 {
			m["dnsNames"] = dns
		}
		if na := stringField(status, "notAfter"); na != "" {
			m["notAfter"] = na
			if t, err := time.Parse(time.RFC3339, na); err == nil {
				m["expiresInDays"] = int(time.Until(t).Hours() / 24)
			}
		}
		m["renewalTime"] = stringField(status, "renewalTime")

	case "argocdapps":
		sync := nestedString(status, "sync", "status")     // Synced | OutOfSync
		health := nestedString(status, "health", "status") // Healthy | Degraded | ...
		m["syncStatus"] = sync
		m["healthStatus"] = health
		m["project"] = nestedString(spec, "project")
		m["revision"] = nestedString(status, "sync", "revision")
		// One-word status for the list: surface health, fall back to sync.
		if health != "" {
			m["status"] = health
		} else if sync != "" {
			m["status"] = sync
		} else {
			m["status"] = "Unknown"
		}

	case "ciliumnetworkpolicies", "ciliumclusterwidenetworkpolicies":
		// Cilium policies carry rules either as a single `spec` or a list of
		// `specs`. Collect both, then summarize for the list and render a
		// structured breakdown for the detail view.
		rules := collectCiliumRuleSets(item.Object)
		var ingressN, egressN int
		l7 := map[string]bool{}
		selector := ""
		for _, r := range rules {
			if selector == "" {
				selector = ciliumSelectorString(r)
			}
			ingressN += len(asMapSlice(r["ingress"])) + len(asMapSlice(r["ingressDeny"]))
			egressN += len(asMapSlice(r["egress"])) + len(asMapSlice(r["egressDeny"]))
			collectCiliumL7Protocols(r, l7)
		}
		if selector == "" {
			selector = "all pods" // empty endpointSelector ({}) selects everything
		}
		m["endpointSelector"] = selector
		m["ingressRules"] = ingressN
		m["egressRules"] = egressN
		m["l7Protocols"] = sortedBoolKeys(l7) // e.g. ["http"], ["dns","http"]
		m["hasL7"] = len(l7) > 0
		// Cilium reports enforcement health in status.conditions (Valid) or, on
		// older versions, status.derivativePolicies; fall back to a benign label.
		if v := conditionStatus(status, "Valid"); v == "False" {
			m["status"] = "Invalid"
		} else {
			m["status"] = "Enforcing"
		}
		// Structured peers/ports/L7 per direction for the detail Overview tab.
		m["policyRules"] = summarizeCiliumRules(rules)

	case "vpas":
		m["status"] = "Active"
		m["targetRef"] = nestedString(spec, "targetRef", "name")
		m["updateMode"] = nestedString(spec, "updatePolicy", "updateMode")
		// Recommendation summary: container → target cpu/memory.
		if rec, ok := status["recommendation"].(map[string]interface{}); ok {
			if crs, ok := rec["containerRecommendations"].([]interface{}); ok {
				var recs []map[string]interface{}
				for _, cr := range crs {
					crm, ok := cr.(map[string]interface{})
					if !ok {
						continue
					}
					target, _ := crm["target"].(map[string]interface{})
					recs = append(recs, map[string]interface{}{
						"container": stringField(crm, "containerName"),
						"targetCPU": stringField(target, "cpu"),
						"targetMem": stringField(target, "memory"),
					})
				}
				m["recommendations"] = recs
			}
		}
	}
	return m
}

// ─── unstructured field helpers ───────────────────────────────────

func stringField(m map[string]interface{}, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

// nestedString walks a chain of map keys and returns the terminal string.
func nestedString(m map[string]interface{}, keys ...string) string {
	cur := m
	for i, k := range keys {
		if cur == nil {
			return ""
		}
		if i == len(keys)-1 {
			return stringField(cur, k)
		}
		cur, _ = cur[k].(map[string]interface{})
	}
	return ""
}

func stringSlice(m map[string]interface{}, key string) []string {
	if m == nil {
		return nil
	}
	raw, ok := m[key].([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// ─── Cilium policy (CNP / CCNP) helpers ───────────────────────────

// collectCiliumRuleSets returns every rule object on a Cilium policy. Cilium
// accepts either a single `spec` (one rule) or a `specs` array (many) — this
// flattens both into one slice so callers don't special-case the shape.
func collectCiliumRuleSets(obj map[string]interface{}) []map[string]interface{} {
	var out []map[string]interface{}
	if spec, ok := obj["spec"].(map[string]interface{}); ok && spec != nil {
		out = append(out, spec)
	}
	for _, s := range asMapSlice(obj["specs"]) {
		out = append(out, s)
	}
	return out
}

// asMapSlice casts an unstructured []interface{} into []map[string]interface{},
// skipping any non-map entries.
func asMapSlice(v interface{}) []map[string]interface{} {
	raw, ok := v.([]interface{})
	if !ok {
		return nil
	}
	out := make([]map[string]interface{}, 0, len(raw))
	for _, e := range raw {
		if m, ok := e.(map[string]interface{}); ok {
			out = append(out, m)
		}
	}
	return out
}

// ciliumSelectorString renders a rule's endpointSelector (CNP/CCNP) or
// nodeSelector (CCNP) matchLabels into a compact "k=v, k=v" string. Empty
// matchLabels ({}) means "select everything" and returns "".
func ciliumSelectorString(rule map[string]interface{}) string {
	sel, ok := rule["endpointSelector"].(map[string]interface{})
	if !ok {
		sel, _ = rule["nodeSelector"].(map[string]interface{})
	}
	if sel == nil {
		return ""
	}
	ml, _ := sel["matchLabels"].(map[string]interface{})
	if len(ml) == 0 {
		return ""
	}
	parts := make([]string, 0, len(ml))
	for k, v := range ml {
		parts = append(parts, fmt.Sprintf("%s=%v", k, v))
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

// collectCiliumL7Protocols records which L7 parsers (http/dns/kafka) appear in
// any toPorts[].rules block across both ingress and egress directions.
func collectCiliumL7Protocols(rule map[string]interface{}, into map[string]bool) {
	for _, dir := range []string{"ingress", "egress", "ingressDeny", "egressDeny"} {
		for _, entry := range asMapSlice(rule[dir]) {
			for _, tp := range asMapSlice(entry["toPorts"]) {
				rules, ok := tp["rules"].(map[string]interface{})
				if !ok {
					continue
				}
				for _, proto := range []string{"http", "dns", "kafka"} {
					if len(asMapSlice(rules[proto])) > 0 {
						into[proto] = true
					}
				}
			}
		}
	}
}

// summarizeCiliumRules builds a per-direction structured breakdown (peers,
// ports, L7) for the detail Overview tab — compact, derived entirely from the
// data the dynamic client already returns.
func summarizeCiliumRules(rules []map[string]interface{}) []map[string]interface{} {
	var out []map[string]interface{}
	directions := []struct {
		key, label string
		deny       bool
	}{
		{"ingress", "ingress", false},
		{"egress", "egress", false},
		{"ingressDeny", "ingress", true},
		{"egressDeny", "egress", true},
	}
	for _, ruleset := range rules {
		for _, d := range directions {
			for _, entry := range asMapSlice(ruleset[d.key]) {
				out = append(out, map[string]interface{}{
					"direction": d.label,
					"deny":      d.deny,
					"peers":     ciliumPeers(entry, d.label),
					"ports":     ciliumPorts(entry),
					"l7":        ciliumL7Summary(entry),
				})
			}
		}
	}
	return out
}

// ciliumPeers renders the from/to selectors of one ingress/egress entry into
// human-readable strings (endpoint labels, CIDRs, FQDNs, entities).
func ciliumPeers(entry map[string]interface{}, direction string) []string {
	prefix := "to"
	if direction == "ingress" {
		prefix = "from"
	}
	var peers []string
	for _, eps := range asMapSlice(entry[prefix+"Endpoints"]) {
		ml, _ := eps["matchLabels"].(map[string]interface{})
		if len(ml) == 0 {
			peers = append(peers, "endpoint: any")
			continue
		}
		parts := make([]string, 0, len(ml))
		for k, v := range ml {
			parts = append(parts, fmt.Sprintf("%s=%v", k, v))
		}
		sort.Strings(parts)
		peers = append(peers, "endpoint: "+strings.Join(parts, ", "))
	}
	for _, cidr := range stringSlice(entry, prefix+"CIDR") {
		peers = append(peers, "cidr: "+cidr)
	}
	for _, cs := range asMapSlice(entry[prefix+"CIDRSet"]) {
		if c := stringField(cs, "cidr"); c != "" {
			peers = append(peers, "cidr: "+c)
		}
	}
	for _, ent := range stringSlice(entry, prefix+"Entities") {
		peers = append(peers, "entity: "+ent)
	}
	for _, fqdn := range asMapSlice(entry["toFQDNs"]) {
		if p := stringField(fqdn, "matchPattern"); p != "" {
			peers = append(peers, "fqdn: "+p)
		} else if n := stringField(fqdn, "matchName"); n != "" {
			peers = append(peers, "fqdn: "+n)
		}
	}
	if len(peers) == 0 {
		peers = append(peers, "any")
	}
	return peers
}

// ciliumPorts renders toPorts[].ports[] into "proto/port" strings.
func ciliumPorts(entry map[string]interface{}) []string {
	var ports []string
	for _, tp := range asMapSlice(entry["toPorts"]) {
		for _, p := range asMapSlice(tp["ports"]) {
			port := stringField(p, "port")
			proto := stringField(p, "protocol")
			if proto == "" {
				proto = "TCP"
			}
			if port != "" {
				ports = append(ports, fmt.Sprintf("%s/%s", proto, port))
			}
		}
	}
	return ports
}

// ciliumL7Summary renders the L7 rules of one entry: HTTP method+path, DNS
// patterns, or a kafka marker.
func ciliumL7Summary(entry map[string]interface{}) []string {
	var out []string
	for _, tp := range asMapSlice(entry["toPorts"]) {
		rules, ok := tp["rules"].(map[string]interface{})
		if !ok {
			continue
		}
		for _, h := range asMapSlice(rules["http"]) {
			method := stringField(h, "method")
			path := stringField(h, "path")
			host := stringField(h, "host")
			label := strings.TrimSpace(strings.Join([]string{method, path}, " "))
			if label == "" {
				label = "any"
			}
			if host != "" {
				label += " (host " + host + ")"
			}
			out = append(out, "http: "+label)
		}
		for _, d := range asMapSlice(rules["dns"]) {
			if p := stringField(d, "matchPattern"); p != "" {
				out = append(out, "dns: "+p)
			} else if n := stringField(d, "matchName"); n != "" {
				out = append(out, "dns: "+n)
			}
		}
		if len(asMapSlice(rules["kafka"])) > 0 {
			out = append(out, "kafka: "+fmt.Sprintf("%d rule(s)", len(asMapSlice(rules["kafka"]))))
		}
	}
	return out
}

// ciliumEndpointLabelSelector converts a Cilium policy's endpointSelector into
// a metav1.LabelSelector for matching live pod labels, plus an optional
// namespace constraint pulled from the reserved namespace label. Returns nil
// when the policy selects nodes (nodeSelector) rather than endpoints, or has no
// selector. Cilium prefixes source labels with "k8s:" (and others) — strip the
// prefix so keys line up with the plain labels on Pod objects.
func ciliumEndpointLabelSelector(obj map[string]interface{}) (*metav1.LabelSelector, string) {
	sets := collectCiliumRuleSets(obj)
	if len(sets) == 0 {
		return nil, ""
	}
	raw, ok := sets[0]["endpointSelector"].(map[string]interface{})
	if !ok {
		return nil, "" // nodeSelector-only or absent — not pod-scoped
	}
	sel := &metav1.LabelSelector{MatchLabels: map[string]string{}}
	nsFilter := ""
	if ml, ok := raw["matchLabels"].(map[string]interface{}); ok {
		for k, v := range ml {
			key := stripCiliumLabelPrefix(k)
			val := fmt.Sprintf("%v", v)
			if key == "io.kubernetes.pod.namespace" {
				nsFilter = val // pin the search to this namespace (CCNP)
				continue
			}
			sel.MatchLabels[key] = val
		}
	}
	for _, e := range asMapSlice(raw["matchExpressions"]) {
		key := stripCiliumLabelPrefix(stringField(e, "key"))
		op := stringField(e, "operator")
		if key == "" || op == "" {
			continue
		}
		sel.MatchExpressions = append(sel.MatchExpressions, metav1.LabelSelectorRequirement{
			Key:      key,
			Operator: metav1.LabelSelectorOperator(op),
			Values:   stringSlice(e, "values"),
		})
	}
	return sel, nsFilter
}

// stripCiliumLabelPrefix removes Cilium's source prefix ("k8s:", "any:",
// "reserved:", …) from a label key so it matches the plain key on a Pod.
// Kubernetes label keys can't contain ':', so splitting on the first one is safe.
func stripCiliumLabelPrefix(k string) string {
	if i := strings.IndexByte(k, ':'); i >= 0 {
		return k[i+1:]
	}
	return k
}

// sortedBoolKeys returns the true keys of a set as a sorted slice.
func sortedBoolKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		if v {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// conditionStatus returns the status string ("True"/"False") of the named
// condition in a status.conditions[] array, or "" if absent.
func conditionStatus(status map[string]interface{}, condType string) string {
	if status == nil {
		return ""
	}
	conds, ok := status["conditions"].([]interface{})
	if !ok {
		return ""
	}
	for _, cond := range conds {
		cm, ok := cond.(map[string]interface{})
		if !ok {
			continue
		}
		if stringField(cm, "type") == condType {
			return stringField(cm, "status")
		}
	}
	return ""
}
