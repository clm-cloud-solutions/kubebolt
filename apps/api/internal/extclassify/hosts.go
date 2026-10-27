package extclassify

import "strings"

// classifyHost tries the hostname rules in order: cluster-internal names
// first (they are never a cloud dependency), then each provider's endpoint
// patterns. The provider domains do not overlap, so the order among them does
// not matter.
func classifyHost(host string) (Classification, bool) {
	if labels(host) == nil {
		return Classification{}, false
	}
	// Before the in-cluster rule: a connection name has no dot and would
	// otherwise read as a bare single-label Service name.
	if c, ok := classifyCloudSQLConnName(host); ok {
		return c, true
	}
	if c, ok := classifyInCluster(host); ok {
		return c, true
	}
	if c, ok := classifyAWS(host); ok {
		return c, true
	}
	if c, ok := classifyAzure(host); ok {
		return c, true
	}
	if c, ok := classifyGCP(host); ok {
		return c, true
	}
	return Classification{}, false
}

// fqdnClass is the common shape of a hostname match.
func fqdnClass(provider, service, kind, hint, region string, tools []string, note string) Classification {
	return Classification{
		Provider:     provider,
		Service:      service,
		Kind:         kind,
		ResourceHint: hint,
		Region:       region,
		Confidence:   ConfidenceFQDN,
		FirstTools:   tools,
		Note:         note,
	}
}

// plumbing is a hostname match for cluster plumbing (control plane, identity
// exchange, metadata): named, but never investigated as a dependency.
func plumbing(provider, service, region, note string) Classification {
	return Classification{
		Provider:       provider,
		Service:        service,
		Kind:           KindPlatform,
		Region:         region,
		Infrastructure: true,
		Confidence:     ConfidenceFQDN,
		Note:           note,
	}
}

// classifyInCluster recognises names that resolve inside the cluster: Service
// DNS names (default and custom cluster domains) and bare single-label names,
// which only resolve through the pod's search path. They are not external
// dependencies, so a cloud connector has nothing to say about them.
func classifyInCluster(host string) (Classification, bool) {
	switch {
	case host == "kubernetes" || host == "kubernetes.default" ||
		strings.HasPrefix(host, "kubernetes.default.svc"):
		return Classification{
			Service:        "kube-apiserver",
			Kind:           KindPlatform,
			Infrastructure: true,
			Confidence:     ConfidenceFQDN,
			Note:           "The cluster's own API server: cluster plumbing, not a dependency.",
		}, true
	case host == "metadata" || host == "metadata.google.internal":
		return plumbing(ProviderGCP, "imds", "", "Google Cloud metadata server (node identity and credentials): cluster plumbing, not a dependency."), true
	case host == "instance-data" || host == "instance-data.ec2.internal":
		return plumbing(ProviderAWS, "imds", "", "Instance metadata service (node identity and credentials): cluster plumbing, not a dependency."), true
	}
	if !strings.Contains(host, ".") ||
		strings.HasSuffix(host, ".svc") ||
		strings.HasSuffix(host, ".cluster.local") ||
		strings.Contains(host, ".svc.") {
		return Classification{
			Service:    "in-cluster",
			Kind:       KindUnknown,
			Confidence: ConfidenceFQDN,
			Note:       "A name that resolves inside the cluster: inspect it with the Kubernetes tools, not a cloud connector.",
		}, true
	}
	return Classification{}, false
}

// stripHashSuffix drops a trailing "-<generated id>" segment that managed
// services append to a resource name in its endpoint ("my-alb-1234567890",
// "orders-cache-x1y2z3"). The segment must look generated (≥ 6 alphanumerics
// with at least one digit), so a hyphenated name with no id stays whole.
func stripHashSuffix(s string) string {
	i := strings.LastIndexByte(s, '-')
	if i <= 0 {
		return s
	}
	tail := s[i+1:]
	if len(tail) < 6 {
		return s
	}
	hasDigit := false
	for _, r := range tail {
		switch {
		case r >= '0' && r <= '9':
			hasDigit = true
		case r >= 'a' && r <= 'z':
		default:
			return s
		}
	}
	if !hasDigit {
		return s
	}
	return s[:i]
}
