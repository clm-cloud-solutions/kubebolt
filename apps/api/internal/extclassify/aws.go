package extclassify

import (
	"regexp"
	"strings"
)

// awsRegionRE matches a full AWS region id (us-east-1, ap-southeast-2,
// us-gov-west-1, cn-northwest-1, us-isob-east-1).
var awsRegionRE = regexp.MustCompile(`^[a-z]{2}(-gov|-iso[a-z]?)?-(east|west|north|south|central|northeast|northwest|southeast|southwest)-[0-9]+$`)

// awsShortRegionRE matches the compact region code ElastiCache puts in its
// endpoints ("use1", "apse2", "euc1").
var awsShortRegionRE = regexp.MustCompile(`^([a-z]{2})(ne|nw|se|sw|n|s|e|w|c)([0-9]+)$`)

var awsDirections = map[string]string{
	"n": "north", "s": "south", "e": "east", "w": "west", "c": "central",
	"ne": "northeast", "nw": "northwest", "se": "southeast", "sw": "southwest",
}

// awsRegion returns the label when it is a region id, else "".
func awsRegion(label string) string {
	if awsRegionRE.MatchString(label) {
		return label
	}
	return ""
}

// awsShortRegion decodes "use1" → "us-east-1"; a label that is already a
// full region id passes through. Unknown shapes return "".
func awsShortRegion(label string) string {
	if r := awsRegion(label); r != "" {
		return r
	}
	m := awsShortRegionRE.FindStringSubmatch(label)
	if m == nil {
		return ""
	}
	return m[1] + "-" + awsDirections[m[2]] + "-" + m[3]
}

// AWS first-tool sequences. The aws connector exposes one typed tool, `call`
// {service, operation}; each entry names the boto3 service and operation.
var (
	awsToolsRDSInstance = []string{"call rds DescribeDBInstances", "call rds DescribeEvents", "call cloudwatch GetMetricData"}
	awsToolsRDSCluster  = []string{"call rds DescribeDBClusters", "call rds DescribeDBInstances", "call cloudwatch GetMetricData"}
	awsToolsRDSCustom   = []string{"call rds DescribeDBClusterEndpoints", "call rds DescribeDBClusters", "call cloudwatch GetMetricData"}
	awsToolsRDSProxy    = []string{"call rds DescribeDBProxies", "call rds DescribeDBProxyTargets", "call cloudwatch GetMetricData"}
	awsToolsRDSProxyEP  = []string{"call rds DescribeDBProxyEndpoints", "call rds DescribeDBProxyTargets", "call cloudwatch GetMetricData"}
	awsToolsElastiCache = []string{"call elasticache DescribeReplicationGroups", "call elasticache DescribeCacheClusters", "call cloudwatch GetMetricData"}
	awsToolsServerless  = []string{"call elasticache DescribeServerlessCaches", "call cloudwatch GetMetricData"}
	awsToolsMemoryDB    = []string{"call memorydb DescribeClusters", "call cloudwatch GetMetricData"}
	awsToolsELB         = []string{"call elbv2 DescribeLoadBalancers", "call elbv2 DescribeTargetHealth", "call cloudwatch GetMetricData"}
	awsToolsSQS         = []string{"call sqs ListQueues", "call sqs GetQueueAttributes", "call cloudwatch GetMetricData"}
	awsToolsSNS         = []string{"call sns GetTopicAttributes", "call cloudwatch GetMetricData"}
	awsToolsS3          = []string{"call s3 GetBucketLocation", "call s3 GetBucketEncryption", "call s3 GetBucketPolicy"}
	awsToolsDynamoDB    = []string{"call dynamodb ListTables", "call dynamodb DescribeTable", "call cloudwatch GetMetricData"}
	awsToolsKafka       = []string{"call kafka ListClustersV2", "call kafka DescribeClusterV2", "call cloudwatch GetMetricData"}
	awsToolsECR         = []string{"call ecr DescribeRepositories", "call ecr DescribeImages"}
	awsToolsEFS         = []string{"call efs DescribeFileSystems", "call efs DescribeMountTargets"}
	awsToolsRedshift    = []string{"call redshift DescribeClusters", "call cloudwatch GetMetricData"}
	awsToolsEC2         = []string{"call ec2 DescribeInstances"}
	awsToolsMetrics     = []string{"call cloudwatch GetMetricData"}
)

// awsPrivateTLDs are top-level labels no public zone uses (.internal is
// reserved for private use): a name under one resolves only inside a
// network, on AWS through a Route 53 private hosted zone of the VPC.
var awsPrivateTLDs = map[string]bool{
	"internal": true, "corp": true, "lan": true, "intranet": true, "private": true, "localdomain": true,
}

// awsToolsPrivateZone finds the zone first by the cluster's VPC: that lists only
// the private zones its pods resolve, never a public zone of the same name, and
// it is the one way in when a role may read one zone but not list them all.
var awsToolsPrivateZone = []string{"call route53 ListHostedZonesByVPC", "call route53 ListHostedZonesByName", "call route53 ListResourceRecordSets"}

// classifyPrivateName attributes a name in a private domain (db.shop.internal)
// to the cluster's own cloud when that cloud is AWS: there it is usually a
// record in a Route 53 private hosted zone, and the record says what it points
// at. Without this the name stayed unknown, with no connector, and Kobi was
// told to leave it to the operator. Elsewhere it stays unknown: no other
// connector reads private DNS zones yet. The EC2 internal names
// (ec2.internal, compute.internal) are matched before, as instances.
func classifyPrivateName(host, clusterProvider string) (Classification, bool) {
	l := labels(host)
	if clusterProvider != ProviderAWS || len(l) < 2 || !awsPrivateTLDs[l[len(l)-1]] {
		return Classification{}, false
	}
	return Classification{
		Provider:     ProviderAWS,
		Service:      "route53",
		Kind:         KindUnknown,
		ResourceHint: host,
		Confidence:   ConfidencePrivateName,
		FirstTools:   awsToolsPrivateZone,
		Note: "A name in a private domain on an EKS cluster: usually a record in a Route 53 private hosted zone of the cluster's VPC. " +
			"Find the zone with ListHostedZonesByVPC (VPCId and VPCRegion of the cluster's VPC: eks DescribeCluster gives it, and so does any answer that names it, an instance, a load balancer, a database), " +
			"which lists only the private zones that VPC resolves, or with ListHostedZonesByName and the zone's name. " +
			"Then ListResourceRecordSets with that HostedZoneId and StartRecordName says what the name points at, an RDS endpoint, a load balancer or an instance: follow that; the record itself may be what changed.",
	}, true
}

// awsControlPlaneAPIs are service API endpoints that cluster add-ons call
// (load balancer controller, autoscaler, CSI drivers, IRSA): plumbing.
var awsControlPlaneAPIs = map[string]bool{
	"sts": true, "ec2": true, "autoscaling": true, "elasticloadbalancing": true,
	"eks": true, "iam": true, "route53": true, "tagging": true, "ebs": true,
	"servicediscovery": true, "eks-auth": true,
}

// classifyAWS matches *.amazonaws.com (and the China partition), the
// dual-stack *.api.aws domain, Lambda function URLs and CloudFront.
func classifyAWS(host string) (Classification, bool) {
	if l, ok := trimSuffixLabels(host, "cloudfront.net"); ok && len(l) > 0 {
		return fqdnClass(ProviderAWS, "cloudfront", KindLoadBalancer, l[0], "", awsToolsMetrics,
			"CloudFront distribution: its request and error rates are in CloudWatch (AWS/CloudFront, region us-east-1)."), true
	}
	if l, ok := trimSuffixLabels(host, "on.aws"); ok {
		if len(l) == 3 && l[1] == "lambda-url" {
			return fqdnClass(ProviderAWS, "lambda", KindPlatform, "", awsRegion(l[2]), awsToolsMetrics,
				"Lambda function URL: the function name is not in the hostname; its errors and throttles are in CloudWatch (AWS/Lambda)."), true
		}
		return fqdnClass(ProviderAWS, "", KindUnknown, "", "", nil, "An AWS endpoint whose service KubeBolt does not recognise."), true
	}
	if c, ok := awsEC2Internal(host); ok {
		return c, true
	}
	var l []string
	var ok bool
	for _, suffix := range []string{"amazonaws.com", "amazonaws.com.cn", "api.aws"} {
		if l, ok = trimSuffixLabels(host, suffix); ok {
			break
		}
	}
	if !ok {
		return Classification{}, false
	}
	n := len(l)
	if n == 0 {
		return fqdnClass(ProviderAWS, "", KindPlatform, "", "", nil, "The AWS apex domain."), true
	}
	last := l[n-1]
	before := ""
	if n >= 2 {
		before = l[n-2]
	}

	switch {
	// <res>.<region>.rds / docdb / neptune.amazonaws.com
	case (last == "rds" || last == "docdb" || last == "neptune") && n >= 3 && awsRegion(before) != "":
		return awsRDSLike(last, l[:n-2], before), true
	case last == "docdb-elastic" && n >= 3:
		return fqdnClass(ProviderAWS, "docdb", KindDatabase, stripHashSuffix(l[0]), awsRegion(before), awsToolsMetrics,
			"DocumentDB elastic cluster: its health is in CloudWatch (AWS/DocDB-Elastic)."), true
	// ElastiCache: <res>.<short-region>.cache.amazonaws.com
	case last == "cache" && n >= 3:
		return awsElastiCache(l[:n-2], awsShortRegion(before)), true
	// EKS control plane: <id>.<gr7|yl4|…>.<region>.eks.amazonaws.com
	case last == "eks" && n >= 2:
		return plumbing(ProviderAWS, "eks-control-plane", awsRegion(before),
			"The EKS API server (control plane): cluster plumbing, not a dependency."), true
	// oidc.eks.<region> (IRSA issuer) and eks.<region> (management API)
	case before == "eks" && awsRegion(last) != "":
		return plumbing(ProviderAWS, "eks-control-plane", last,
			"EKS management / OIDC endpoint: identity and control-plane plumbing, not a dependency."), true
	// ALB / Classic: [dualstack.][internal-]<name>-<id>.<region>.elb.amazonaws.com
	case last == "elb" && n >= 3:
		return fqdnClass(ProviderAWS, "elb", KindLoadBalancer, awsLBName(l[:n-2]), awsRegion(before), awsToolsELB,
			"Application or Classic Load Balancer: if elbv2 DescribeLoadBalancers does not find it, it is a Classic one (call elb DescribeLoadBalancers)."), true
	// NLB / GWLB: <name>-<hash>.elb.<region>.amazonaws.com
	case before == "elb" && n >= 3:
		return fqdnClass(ProviderAWS, "elb", KindLoadBalancer, awsLBName(l[:n-2]), awsRegion(last), awsToolsELB,
			"Network Load Balancer."), true
	case before == "memorydb" && n >= 3:
		res := l[:n-2]
		hint := res[0]
		if hint == "clustercfg" && len(res) > 1 {
			hint = res[1]
		}
		return fqdnClass(ProviderAWS, "memorydb", KindCache, hint, awsRegion(last), awsToolsMemoryDB, ""), true
	// MSK: b-1.<cluster>.<id>.c2.kafka.<region>.amazonaws.com
	case (before == "kafka" || before == "kafka-serverless") && n >= 3:
		res := l[:n-2]
		hint := ""
		if before == "kafka" && len(res) >= 2 && (strings.HasPrefix(res[0], "b-") || strings.HasPrefix(res[0], "boot-")) {
			hint = res[1]
		}
		return fqdnClass(ProviderAWS, "kafka", KindQueue, hint, awsRegion(last), awsToolsKafka, "Amazon MSK (Kafka) cluster."), true
	// ECR: <account>.dkr.ecr.<region> and api.ecr.<region>
	case before == "ecr":
		hint := ""
		if n >= 4 && l[n-3] == "dkr" {
			hint = l[0]
		}
		return fqdnClass(ProviderAWS, "ecr", KindRegistry, hint, awsRegion(last), awsToolsECR,
			"ECR registry (the hint is the AWS account id): check the repository and image exist and the node role can pull."), true
	// EFS: [<az>.]fs-<id>.efs.<region>
	case before == "efs" && n >= 3:
		hint := ""
		for _, lb := range l[:n-2] {
			if strings.HasPrefix(lb, "fs-") {
				hint = lb
			}
		}
		return fqdnClass(ProviderAWS, "efs", KindStorage, hint, awsRegion(last), awsToolsEFS, ""), true
	// Amazon MQ: b-<uuid>-1.mq.<region>
	case before == "mq" && n >= 3:
		hint := l[0]
		if i := strings.LastIndexByte(hint, '-'); i > 0 && len(hint)-i <= 2 {
			hint = hint[:i]
		}
		return fqdnClass(ProviderAWS, "mq", KindQueue, hint, awsRegion(last), awsToolsMetrics,
			"Amazon MQ broker: its health is in CloudWatch (AWS/AmazonMQ)."), true
	// OpenSearch: vpc-<domain>-<hash>.<region>.es / <id>.<region>.aoss
	case last == "es" && n >= 3:
		// One prefix only: a domain may itself be called "search-…".
		hint := l[0]
		if h, ok := strings.CutPrefix(hint, "vpc-"); ok {
			hint = h
		} else {
			hint = strings.TrimPrefix(hint, "search-")
		}
		return fqdnClass(ProviderAWS, "opensearch", KindDatabase, stripHashSuffix(hint), awsRegion(before), awsToolsMetrics,
			"OpenSearch domain: its health is in CloudWatch (AWS/ES)."), true
	case last == "aoss" && n >= 3:
		return fqdnClass(ProviderAWS, "opensearch", KindDatabase, l[0], awsRegion(before), awsToolsMetrics,
			"OpenSearch Serverless collection: its health is in CloudWatch (AWS/AOSS)."), true
	case last == "redshift" && n >= 3:
		return fqdnClass(ProviderAWS, "redshift", KindDatabase, l[0], awsRegion(before), awsToolsRedshift, ""), true
	case last == "redshift-serverless" && n >= 3:
		return fqdnClass(ProviderAWS, "redshift", KindDatabase, l[0], awsRegion(before), awsToolsMetrics,
			"Redshift Serverless workgroup: its health is in CloudWatch (AWS/Redshift-Serverless)."), true
	case before == "execute-api" && n >= 3:
		return fqdnClass(ProviderAWS, "apigateway", KindLoadBalancer, l[0], awsRegion(last), awsToolsMetrics,
			"API Gateway API (the hint is the API id): its latency and 4xx/5xx are in CloudWatch (AWS/ApiGateway)."), true
	// Public EC2 DNS: ec2-1-2-3-4.compute-1.amazonaws.com / ….<region>.compute.amazonaws.com
	case last == "compute-1" || (last == "compute" && n >= 3):
		region := "us-east-1"
		if last == "compute" {
			region = awsRegion(before)
		}
		return fqdnClass(ProviderAWS, "ec2", KindUnknown, l[0], region, awsToolsEC2,
			"An EC2 instance's public DNS name: find the instance and what runs on it."), true
	// Interface VPC endpoint: vpce-<id>[-<az>].<svc>.<region>.vpce.amazonaws.com
	case last == "vpce" && n >= 4:
		svc, region := l[n-3], l[n-2]
		c, _ := classifyAWS(svc + "." + region + ".amazonaws.com")
		c.Note = strings.TrimSpace(c.Note + " Reached through an interface VPC endpoint (PrivateLink).")
		return c, true
	}
	if c, ok := awsS3(l); ok {
		return c, true
	}
	return awsServiceAPI(l), true
}

// awsRDSLike parses the endpoint shapes shared by RDS, Aurora, DocumentDB and
// Neptune (all managed through the RDS API, so the rds operations cover them).
func awsRDSLike(service string, res []string, region string) Classification {
	hint, tools, note := "", awsToolsRDSInstance, "DB instance endpoint."
	if len(res) > 0 {
		hint = res[0]
	}
	switch {
	case len(res) == 2 && strings.HasPrefix(res[1], "cluster-ro-"):
		tools, note = awsToolsRDSCluster, "Cluster reader endpoint."
	case len(res) == 2 && strings.HasPrefix(res[1], "cluster-custom-"):
		tools, note = awsToolsRDSCustom, "Cluster custom endpoint (the hint is the endpoint name, not the cluster)."
	case len(res) == 2 && strings.HasPrefix(res[1], "cluster-"):
		tools, note = awsToolsRDSCluster, "Cluster writer endpoint."
	case len(res) == 2 && strings.HasPrefix(res[1], "shardgrp-"):
		tools, note = awsToolsRDSCluster, "Aurora Limitless shard group endpoint."
	case len(res) == 2 && strings.HasPrefix(res[1], "proxy-"):
		tools, note = awsToolsRDSProxy, "RDS Proxy endpoint: check the proxy's targets as well as the database behind it."
	case len(res) == 3 && res[1] == "endpoint" && strings.HasPrefix(res[2], "proxy-"):
		tools, note = awsToolsRDSProxyEP, "RDS Proxy custom endpoint (the hint is the endpoint name)."
	}
	switch service {
	case "docdb":
		note = "DocumentDB " + lowerFirst(note) + " DocumentDB is managed through the RDS API."
	case "neptune":
		note = "Neptune " + lowerFirst(note) + " Neptune is managed through the RDS API."
	}
	return fqdnClass(ProviderAWS, service, KindDatabase, hint, region, tools, note)
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// awsElastiCache parses the ElastiCache endpoint shapes: primary / reader
// ("master.<id>…", "replica.<id>…"), configuration ("<id>.<hash>.clustercfg…",
// "<id>.<hash>.cfg…"), node ("<id>.<hash>.ng.0001…", "<id>.<hash>.0001…") and
// serverless ("<name>-<id>.serverless…").
func awsElastiCache(res []string, region string) Classification {
	if len(res) >= 2 && res[1] == "serverless" {
		return fqdnClass(ProviderAWS, "elasticache", KindCache, stripHashSuffix(res[0]), region, awsToolsServerless,
			"ElastiCache Serverless cache.")
	}
	hint := ""
	if len(res) > 0 {
		hint = res[0]
		if (hint == "master" || hint == "replica") && len(res) > 1 {
			hint = res[1]
		}
	}
	return fqdnClass(ProviderAWS, "elasticache", KindCache, hint, region, awsToolsElastiCache, "")
}

// awsLBName recovers the load balancer name from the first endpoint label:
// drop "dualstack." and "internal-", then the generated id suffix.
func awsLBName(res []string) string {
	if len(res) > 0 && res[0] == "dualstack" {
		res = res[1:]
	}
	if len(res) == 0 {
		return ""
	}
	return stripHashSuffix(strings.TrimPrefix(res[len(res)-1], "internal-"))
}

// awsS3 matches the S3 endpoint shapes (virtual-hosted, path-style, legacy
// dash-region, website, dual-stack, access points). The bucket is every label
// before the S3 label — bucket names may contain dots. Only the last three
// labels are searched, so a bucket named "logs.s3.backup" does not confuse it.
func awsS3(l []string) (Classification, bool) {
	n := len(l)
	for i := n - 1; i >= 0 && i >= n-3; i-- {
		lb := l[i]
		if lb != "s3" && !strings.HasPrefix(lb, "s3-") {
			continue
		}
		if lb == "s3-control" {
			return fqdnClass(ProviderAWS, "s3", KindPlatform, "", regionIn(l[i+1:]), nil,
				"S3 Control API (account-level configuration)."), true
		}
		region := regionIn(l[i+1:])
		if region == "" {
			for _, p := range []string{"s3-website-", "s3-fips-", "s3-"} {
				if r := awsRegion(strings.TrimPrefix(lb, p)); r != "" && strings.HasPrefix(lb, p) {
					region = r
					break
				}
			}
		}
		bucket := strings.Join(l[:i], ".")
		note := ""
		if lb == "s3-accesspoint" {
			note = "S3 access point (the hint is \"<access point>-<account id>\")."
		}
		if bucket == "" {
			note = "S3 path-style endpoint: the bucket is in the request path, not the hostname; take it from the workload's configuration."
		}
		return fqdnClass(ProviderAWS, "s3", KindStorage, bucket, region, awsToolsS3, note), true
	}
	return Classification{}, false
}

// regionIn returns the first AWS region id among the labels.
func regionIn(l []string) string {
	for _, lb := range l {
		if r := awsRegion(lb); r != "" {
			return r
		}
	}
	return ""
}

// awsEC2Internal matches the VPC-internal EC2 DNS names
// (ip-10-0-1-2.ec2.internal, ip-10-0-1-2.us-west-2.compute.internal).
func awsEC2Internal(host string) (Classification, bool) {
	if l, ok := trimSuffixLabels(host, "ec2.internal"); ok && len(l) > 0 {
		return fqdnClass(ProviderAWS, "ec2", KindUnknown, l[0], "us-east-1", awsToolsEC2,
			"An EC2 instance's private DNS name: find the instance and what runs on it."), true
	}
	if l, ok := trimSuffixLabels(host, "compute.internal"); ok && len(l) >= 2 {
		return fqdnClass(ProviderAWS, "ec2", KindUnknown, l[0], awsRegion(l[len(l)-1]), awsToolsEC2,
			"An EC2 instance's private DNS name: find the instance and what runs on it."), true
	}
	return Classification{}, false
}

// awsServiceAPI handles regional or global service API endpoints
// (sqs.us-east-1, dynamodb.eu-west-1, sts, secretsmanager-fips.us-east-2,
// the legacy us-east-1.queue for SQS). Anything longer is an AWS hostname
// KubeBolt does not recognise; saying so beats guessing a service from it.
func awsServiceAPI(l []string) Classification {
	n := len(l)
	if n > 3 {
		return fqdnClass(ProviderAWS, "", KindUnknown, "", regionIn(l), nil, "An AWS endpoint whose service KubeBolt does not recognise.")
	}
	svc := strings.TrimSuffix(l[0], "-fips")
	region := regionIn(l[1:])
	if awsRegion(l[0]) != "" && n == 2 && l[1] == "queue" {
		svc, region = "queue", l[0]
	}
	switch svc {
	case "sqs", "queue":
		return fqdnClass(ProviderAWS, "sqs", KindQueue, "", region, awsToolsSQS,
			"SQS API endpoint: the queue name is not in the hostname; take it from the workload's configuration.")
	case "sns":
		return fqdnClass(ProviderAWS, "sns", KindQueue, "", region, awsToolsSNS,
			"SNS API endpoint: the topic is not in the hostname; take its ARN from the workload's configuration.")
	case "dynamodb":
		return fqdnClass(ProviderAWS, "dynamodb", KindDatabase, "", region, awsToolsDynamoDB,
			"DynamoDB API endpoint: the table is not in the hostname.")
	case "kinesis":
		return fqdnClass(ProviderAWS, "kinesis", KindQueue, "", region, awsToolsMetrics,
			"Kinesis Data Streams API endpoint: the stream is not in the hostname.")
	case "monitoring":
		return fqdnClass(ProviderAWS, "cloudwatch", KindPlatform, "", region, nil, "CloudWatch API (telemetry export).")
	}
	if awsControlPlaneAPIs[svc] {
		return plumbing(ProviderAWS, svc, region,
			"AWS control-plane or identity API, typically called by a cluster add-on (load balancer controller, autoscaler, CSI driver, IRSA): plumbing, not a dependency.")
	}
	return fqdnClass(ProviderAWS, svc, KindPlatform, "", region, nil,
		"AWS "+svc+" API endpoint: the workload calls the service API directly, and the resource is not in the hostname.")
}
