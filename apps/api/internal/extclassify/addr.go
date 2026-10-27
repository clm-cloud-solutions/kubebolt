package extclassify

import "net/netip"

// Fixed platform addresses. They are documented by each cloud and appear in
// the external flows of every cluster on it.
var (
	// Instance metadata: the same link-local address on AWS, Azure and GCP;
	// AWS also serves it on IPv6 inside its ULA block.
	imdsV4    = netip.MustParseAddr("169.254.169.254")
	awsIMDSV6 = netip.MustParseAddr("fd00:ec2::254")
	// Amazon-provided DNS (Route 53 Resolver) and Time Sync.
	awsDNSV4 = netip.MustParseAddr("169.254.169.253")
	awsDNSV6 = netip.MustParseAddr("fd00:ec2::253")
	awsNTPV4 = netip.MustParseAddr("169.254.169.123")
	awsNTPV6 = netip.MustParseAddr("fd00:ec2::123")
	// Container credential endpoints: ECS-style (169.254.170.2) and the EKS
	// Pod Identity agent (169.254.170.23 / fd00:ec2::23).
	awsCredsV4      = netip.MustParseAddr("169.254.170.2")
	awsPodIdentity4 = netip.MustParseAddr("169.254.170.23")
	awsPodIdentity6 = netip.MustParseAddr("fd00:ec2::23")
	// Azure WireServer: platform DNS, DHCP and load-balancer health probes.
	azureWireServer = netip.MustParseAddr("168.63.129.16")
	// NodeLocal DNSCache's conventional listen address.
	nodeLocalDNS = netip.MustParseAddr("169.254.20.10")

	cgnat = netip.MustParsePrefix("100.64.0.0/10")
)

// classifyWellKnownAddr recognises the fixed platform addresses. These win
// over any hostname, because the address is what the pod actually reached.
func classifyWellKnownAddr(a netip.Addr, clusterProvider string) (Classification, bool) {
	infra := func(provider, service, note string) (Classification, bool) {
		return Classification{
			Provider:       provider,
			Service:        service,
			Kind:           KindInfrastructure,
			Infrastructure: true,
			Confidence:     ConfidenceWellKnownIP,
			Note:           note,
		}, true
	}
	switch a {
	case imdsV4:
		// The address alone does not say which cloud; the cluster does.
		return infra(clusterProvider, "imds", "Instance metadata service (node identity and credentials): cluster plumbing, not a dependency.")
	case awsIMDSV6:
		return infra(ProviderAWS, "imds", "Instance metadata service (node identity and credentials): cluster plumbing, not a dependency.")
	case awsDNSV4, awsDNSV6:
		return infra(ProviderAWS, "cluster-dns", "Amazon-provided DNS resolver: cluster plumbing, not a dependency.")
	case awsNTPV4, awsNTPV6:
		return infra(ProviderAWS, "ntp", "Amazon Time Sync Service: cluster plumbing, not a dependency.")
	case awsCredsV4, awsPodIdentity4, awsPodIdentity6:
		return infra(ProviderAWS, "pod-identity", "Container credential endpoint (EKS Pod Identity agent): identity plumbing, not a dependency.")
	case azureWireServer:
		return infra(ProviderAzure, "cluster-dns", "Azure platform endpoint (WireServer: DNS, DHCP, health probes): cluster plumbing, not a dependency.")
	case nodeLocalDNS:
		return infra("", "cluster-dns", "NodeLocal DNSCache on the node: cluster plumbing, not a dependency.")
	}
	switch {
	case a.IsLoopback():
		return infra("", "loopback", "Loopback address: the pod is talking to itself.")
	case a.IsLinkLocalUnicast():
		return infra("", "link-local", "Link-local address: node-local plumbing (metadata, DNS cache, CNI), not a dependency.")
	}
	return Classification{}, false
}

// classifyAddr handles an address with no usable hostname. A private address
// sits in the cluster's network, so its provider is the cluster's; which
// managed service owns it is not in the address and needs an inventory
// lookup. A public address with no name cannot be attributed at all.
func classifyAddr(a netip.Addr, clusterProvider, clusterRegion string) Classification {
	if a.IsUnspecified() || a.IsMulticast() || a.IsInterfaceLocalMulticast() || a.IsLinkLocalMulticast() {
		return Classification{
			Kind:       KindUnknown,
			Confidence: ConfidenceUnknown,
			Note:       "Not a unicast destination; nothing to investigate.",
		}
	}
	if !a.IsPrivate() && !(a.Is4() && cgnat.Contains(a)) {
		return Classification{
			Kind:       KindUnknown,
			Confidence: ConfidenceUnknown,
			Note:       "Public address with no hostname: it cannot be attributed to a provider or service from the address alone.",
		}
	}
	c := Classification{
		Provider:   clusterProvider,
		Kind:       KindUnknown,
		Confidence: ConfidenceIPInProvider,
	}
	switch clusterProvider {
	case ProviderAWS:
		c.FirstTools = []string{"call ec2 DescribeNetworkInterfaces"}
		c.Note = "Private address in the cluster's AWS network; the owner is not in the address. Look it up with ec2 DescribeNetworkInterfaces filtered on addresses.private-ip-address" + inRegion(clusterRegion) + ": the interface description names the RDS instance, load balancer, cache or instance behind it. Take the resource's region from the resource you find."
	case ProviderAzure:
		c.FirstTools = []string{"group_list", "group_resource_list"}
		c.Note = "Private address in the cluster's Azure network; the owner is not in the address. List the resource groups and their resources (private endpoints, databases, caches) and match the address. A private endpoint can front a resource in another region: take the region from the resource you find, not from the cluster."
	case ProviderGCP:
		c.FirstTools = []string{"sqladmin_list_instances", "redis_list_instances", "cloudasset_list_assets"}
		c.Note = "Private address in the cluster's Google Cloud network; the owner is not in the address. Cloud SQL, Memorystore and AlloyDB are reached by private IP: list them and match the address, or list the project's assets. A Google VPC spans regions: take the region from the resource you find, not from the cluster."
	default:
		c.Confidence = ConfidenceUnknown
		c.Note = "Private address outside the cluster, and the cluster's cloud is not known: it may be a VM, an on-prem service or a managed endpoint in a peered network."
	}
	return c
}

// inRegion says where an AWS lookup has to run: the interface sits in the
// cluster's VPC, so it is found in the cluster's region, which is not
// necessarily the connector's default. It is where to look, not a claim about
// the resource behind the address.
func inRegion(clusterRegion string) string {
	if clusterRegion == "" {
		return ""
	}
	return " in " + clusterRegion + " (the cluster's region, where its network is)"
}
