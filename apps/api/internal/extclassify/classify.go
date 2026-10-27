// Package extclassify names what a workload talks to outside the cluster.
//
// Hubble tells KubeBolt that a pod sends traffic to 10.0.3.17 (and, with DNS
// visibility, that it asked for mydb.c9x.us-east-1.rds.amazonaws.com). An
// operator reads that as "the orders service depends on the `mydb` RDS
// instance"; Kobi needs the same reading to pick its first cloud tool instead
// of spending three calls on discovery. Classify turns the (fqdn, ip) pair plus
// the cluster's own cloud profile into that reading: provider, managed service,
// the resource name the hostname encodes, its region, and the first local tool
// names of the matching MCP connector.
//
// It also marks cluster plumbing (IMDS, the cloud resolver, the managed control
// plane, credential exchange) as Infrastructure: those show up in every
// cluster's external flows and are never the dependency an incident is about.
//
// The package is pure and stdlib-only on purpose: the api handler, the copilot
// executor and the connector catalog all import it, and none of them should
// drag the others in through it.
package extclassify

import (
	"net/netip"
	"strings"
)

// Provider ids. They are the MCP connector ids, so a Classification can be
// routed to a connector without a mapping table.
const (
	ProviderAWS   = "aws"
	ProviderGCP   = "gcp"
	ProviderAzure = "azure"
)

// Kinds describe what role the destination plays for the workload.
const (
	KindDatabase       = "database"
	KindCache          = "cache"
	KindQueue          = "queue"
	KindLoadBalancer   = "loadbalancer"
	KindStorage        = "storage"
	KindRegistry       = "registry"
	KindPlatform       = "platform"
	KindInfrastructure = "infrastructure"
	KindUnknown        = "unknown"
)

// Confidence says how the classification was reached, so the caller can tell
// a certain answer from a guess.
const (
	// ConfidenceFQDN: the hostname matched a documented endpoint pattern.
	ConfidenceFQDN = "fqdn"
	// ConfidenceWellKnownIP: the address is a fixed, documented platform
	// address (IMDS, the Azure WireServer, the Amazon resolver).
	ConfidenceWellKnownIP = "well-known-ip"
	// ConfidenceIPInProvider: a private address with no usable hostname; the
	// provider is inferred from the cluster's own cloud, the service is not.
	ConfidenceIPInProvider = "ip-in-provider"
	// ConfidencePrivateName: a name in a private domain; the provider is
	// inferred from the cluster's own cloud, what the name points at is not.
	ConfidencePrivateName = "private-name-in-provider"
	// ConfidenceUnknown: nothing to go on.
	ConfidenceUnknown = "unknown"
)

// Classification is what Classify knows about one external destination.
// Every field is safe to show to the model: it is derived from a hostname or
// an address, never from credentials or payloads.
type Classification struct {
	// Provider is aws, gcp, azure, or "" when it cannot be attributed.
	Provider string `json:"provider,omitempty"`
	// Service is the managed service (rds, elasticache, azure-sql, cloudsql,
	// imds, eks-control-plane…), "" when unknown.
	Service string `json:"service,omitempty"`
	// Kind is one of the Kind* constants.
	Kind string `json:"kind"`
	// ResourceHint is the resource name the hostname encodes (RDS instance or
	// cluster id, Azure SQL server, bucket, load balancer…), "" when the
	// hostname does not carry one.
	ResourceHint string `json:"resourceHint,omitempty"`
	// Region is the region the hostname encodes; "" when it encodes none. An
	// address-only guess never carries one: a private endpoint can front a
	// resource in another region and a Google VPC is global, and a region here
	// reads as the resource's — the cluster's region was the eastus2 the model
	// gave for a server in centralus. Where to look goes in the Note.
	Region string `json:"region,omitempty"`
	// Infrastructure marks cluster plumbing (control plane, IMDS, cloud DNS,
	// credential exchange): never investigate it as a dependency.
	Infrastructure bool `json:"infrastructure,omitempty"`
	// Confidence is one of the Confidence* constants.
	Confidence string `json:"confidence"`
	// FirstTools are the connector's LOCAL tool names to start with, in order.
	// For aws the single tool is `call`, so each entry reads
	// "call <boto3 service> <Operation>".
	FirstTools []string `json:"firstTools,omitempty"`
	// Note is plain prose for the model, a sentence or a short paragraph:
	// what to do next, or why the classification is uncertain.
	Note string `json:"note,omitempty"`
}

// NormalizeProvider maps the cluster profile's display names ("AWS", "GCP",
// "Azure", as detected from node providerIDs and labels) and a few common
// synonyms to connector ids. Anything else — "kind (local)", "on-prem",
// "cloud (unrecognised)", another cloud — maps to "".
func NormalizeProvider(display string) string {
	switch strings.ToLower(strings.TrimSpace(display)) {
	case "aws", "amazon", "amazon web services", "eks", "amazon eks":
		return ProviderAWS
	case "gcp", "gce", "gke", "google", "google cloud", "google cloud platform":
		return ProviderGCP
	case "azure", "aks", "microsoft azure", "azure kubernetes service":
		return ProviderAzure
	}
	return ""
}

// Classify names an external destination from its hostname (as resolved by
// the pod, or as written in its config) and/or its address, using the
// cluster's own provider and region as the fallback for bare private
// addresses. Either fqdn or ip may be empty; an IP literal passed as fqdn is
// treated as the address.
//
// Precedence: well-known platform addresses first (IMDS answers the same
// whatever name was asked for), then hostname patterns, then the address
// rules. A hostname that matches nothing (a custom DNS name that CNAMEs to a
// managed endpoint, say) falls through to the address rules, because the
// CNAME chain is not visible in the flow data.
func Classify(fqdn, ip, clusterProvider, clusterRegion string) Classification {
	host := normalizeHost(fqdn)
	addr, addrOK := parseAddr(ip)
	if !addrOK && host != "" {
		// A literal address in the hostname slot (env vars often carry one).
		if a, ok := parseAddr(host); ok {
			addr, addrOK = a, true
			host = ""
		}
	}
	provider := NormalizeProvider(clusterProvider)
	region := strings.ToLower(strings.TrimSpace(clusterRegion))

	if addrOK {
		if c, ok := classifyWellKnownAddr(addr, provider); ok {
			return c
		}
	}
	if host != "" {
		if c, ok := classifyHost(host); ok {
			return c
		}
	}
	if addrOK {
		c := classifyAddr(addr, provider, region)
		if host != "" && c.Confidence == ConfidenceIPInProvider {
			c.Note = "\"" + host + "\" is not a known cloud endpoint name (it may be a custom DNS alias). " + c.Note
		}
		return c
	}
	if host != "" {
		if c, ok := classifyPrivateName(host, provider); ok {
			return c
		}
		return Classification{
			Kind:       KindUnknown,
			Confidence: ConfidenceUnknown,
			Note:       "The hostname does not match a known cloud endpoint pattern. It may be a custom DNS alias of a managed service, or a service outside any cloud; check its DNS record or the connector inventory.",
		}
	}
	return Classification{Kind: KindUnknown, Confidence: ConfidenceUnknown}
}

// normalizeHost lower-cases, trims whitespace and the root dot, and strips
// IPv6 brackets and a stray ":port" (a name passed as "host:5432"). Cloud SQL
// connection names and bare IPv6 literals have more than one ':' and are left
// alone.
func normalizeHost(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if strings.HasPrefix(s, "[") {
		if end := strings.IndexByte(s, ']'); end > 0 {
			s = s[1:end]
		}
	} else if h, p, ok := strings.Cut(s, ":"); ok && !strings.Contains(p, ":") && isDigits(p) {
		s = h
	}
	return strings.TrimSuffix(s, ".")
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// parseAddr accepts IPv4, IPv6 (bracketed or not, with or without a zone) and
// IPv4-mapped IPv6, returning the unmapped address.
func parseAddr(s string) (netip.Addr, bool) {
	s = normalizeHost(s)
	if s == "" {
		return netip.Addr{}, false
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}, false
	}
	return a.Unmap().WithZone(""), true
}

// labels splits a normalized hostname. Empty labels ("a..b") make the
// hostname invalid for every pattern, so they are reported as nil.
func labels(host string) []string {
	parts := strings.Split(host, ".")
	for _, p := range parts {
		if p == "" {
			return nil
		}
	}
	return parts
}

// trimSuffixLabels returns the labels before the given dotted suffix, or
// (nil, false) when host does not end with it on a label boundary.
func trimSuffixLabels(host, suffix string) ([]string, bool) {
	if host == suffix {
		return []string{}, true
	}
	if !strings.HasSuffix(host, "."+suffix) {
		return nil, false
	}
	l := labels(strings.TrimSuffix(host, "."+suffix))
	if l == nil {
		return nil, false
	}
	return l, true
}
