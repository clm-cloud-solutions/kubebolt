package mcpcatalog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// The AWS MCP Server has no read-only mode and its only API tool,
// run_script, executes whatever Python it is given with the caller's IAM.
// The connector therefore never forwards model code: it
// exposes one typed tool, `call`, and generates the script itself from a
// (service, operation) pair that must pass the tables below.
//
// These tables are the single source of what the connector may read. The
// families the admin approves, the typed allowlist, the per-consumer STS
// session policies and the IAM policy printed in the setup guide are all
// generated from them, so no two of those can disagree.

// awsGrant is what one family lets a consumer call on one AWS service.
type awsGrant struct {
	// service is the boto3 client name the model passes ("elbv2").
	service string
	// iam is the IAM action prefix when it differs from service
	// ("elasticloadbalancing" for elbv2).
	iam string
	// aliases are other names the sandbox may report for the service in
	// api_calls (the botocore service id, the endpoint prefix).
	aliases []string
	// ops are operation names, exact or a "Verb*" prefix pattern.
	ops []string
	// kobiOnly narrows a grant of a family both consumers share.
	kobiOnly bool
}

type awsFamily struct {
	Family
	grants []awsGrant
}

// awsFamilyTable is the reviewed AWS families, with the operations the live discovery
// showed are needed around it (DescribeRegions, DescribeNetworkInterfaces to
// find a resource by IP, GetLogEvents).
func awsFamilyTable() []awsFamily {
	return []awsFamily{
		{
			Family: Family{
				Label:     "Kubernetes clusters (EKS)",
				Detail:    "Clusters, node groups, add-ons, Fargate profiles, Pod Identity associations and EKS upgrade insights, plus the Auto Scaling groups and EC2 instances behind the nodes.",
				Kobi:      true,
				Autopilot: true,
			},
			grants: []awsGrant{
				{service: "eks", ops: []string{"Describe*", "List*"}},
				{service: "autoscaling", ops: []string{"Describe*"}},
				{service: "ec2", ops: []string{"DescribeInstances", "DescribeInstanceStatus", "DescribeInstanceTypes"}},
			},
		},
		{
			Family: Family{
				Label:     "Databases (RDS, Aurora, DocumentDB)",
				Detail:    "Instance and cluster status, maintenance windows, events, failovers, parameter groups and Performance Insights load.",
				Kobi:      true,
				Autopilot: true,
			},
			grants: []awsGrant{
				{service: "rds", ops: []string{"Describe*", "ListTagsForResource"}},
				{service: "docdb", iam: "rds", ops: []string{"Describe*", "ListTagsForResource"}},
				{service: "pi", ops: []string{"DescribeDimensionKeys", "GetResourceMetrics", "ListAvailableResourceMetrics"}},
			},
		},
		{
			Family: Family{
				Label:     "CloudWatch metrics, alarms and logs",
				Detail:    "Metric data and statistics, alarms and their history, log groups and streams, filtered log events and Logs Insights queries.",
				Kobi:      true,
				Autopilot: true,
			},
			grants: []awsGrant{
				{service: "cloudwatch", aliases: []string{"monitoring"}, ops: []string{
					"GetMetricData", "GetMetricStatistics", "ListMetrics",
					"DescribeAlarms", "DescribeAlarmHistory", "DescribeAlarmsForMetric",
				}},
				{service: "logs", aliases: []string{"cloudwatchlogs"}, ops: []string{
					"DescribeLogGroups", "DescribeLogStreams", "DescribeQueries", "DescribeQueryDefinitions",
					"FilterLogEvents", "GetLogEvents", "StartQuery", "GetQueryResults", "StopQuery",
				}},
			},
		},
		{
			Family: Family{
				Label:     "Application Signals and X-Ray",
				Detail:    "Service health and SLOs. Kobi also reads X-Ray trace summaries, traces and the service graph.",
				Kobi:      true,
				Autopilot: true,
			},
			grants: []awsGrant{
				{service: "application-signals", ops: []string{"BatchGet*", "Get*", "List*"}},
				{service: "xray", ops: []string{"GetTraceSummaries", "BatchGetTraces", "GetServiceGraph"}, kobiOnly: true},
			},
		},
		{
			Family: Family{
				Label:     "Caches, queues and streaming",
				Detail:    "ElastiCache and MemoryDB clusters, SQS queue attributes and dead-letter queues, SNS topics and subscriptions, MSK (Kafka) clusters. Never message contents.",
				Kobi:      true,
				Autopilot: true,
			},
			grants: []awsGrant{
				{service: "elasticache", ops: []string{"Describe*", "ListTagsForResource"}},
				{service: "memorydb", ops: []string{"Describe*", "ListTags"}},
				{service: "sqs", ops: []string{"GetQueueAttributes", "GetQueueUrl", "ListQueues", "ListDeadLetterSourceQueues"}},
				{service: "sns", ops: []string{"GetTopicAttributes", "ListTopics", "ListSubscriptionsByTopic"}},
				{service: "kafka", ops: []string{"Describe*", "List*"}},
			},
		},
		{
			Family: Family{
				Label:     "DynamoDB and Redshift",
				Detail:    "Table and cluster status, capacity, backups and configuration. Never items or query results.",
				Kobi:      true,
				Autopilot: true,
			},
			grants: []awsGrant{
				{service: "dynamodb", ops: []string{"Describe*", "List*"}},
				{service: "redshift", ops: []string{"Describe*"}},
			},
		},
		{
			Family: Family{
				Label:     "Networking and load balancers",
				Detail:    "VPCs, subnets, route tables, security groups, network ACLs, NAT and internet gateways, VPC endpoints, peering and network interfaces; load balancers and target health; Route 53 zones and records; regions and availability zones.",
				Kobi:      true,
				Autopilot: true,
			},
			grants: []awsGrant{
				{service: "ec2", ops: []string{
					"DescribeVpcs", "DescribeSubnets", "DescribeRouteTables",
					"DescribeSecurityGroups", "DescribeSecurityGroupRules", "DescribeNetworkAcls",
					"DescribeNatGateways", "DescribeInternetGateways", "DescribeVpcEndpoints",
					"DescribeVpcPeeringConnections", "DescribeNetworkInterfaces", "DescribeAddresses",
					"DescribeRegions", "DescribeAvailabilityZones",
				}},
				{service: "elbv2", iam: "elasticloadbalancing", aliases: []string{"elasticloadbalancingv2"}, ops: []string{"Describe*"}},
				{service: "elb", iam: "elasticloadbalancing", ops: []string{"Describe*"}},
				{service: "route53", ops: []string{"Get*", "List*"}},
			},
		},
		{
			Family: Family{
				Label:     "Storage",
				Detail:    "EBS volumes and their status, EFS file systems and mount targets, S3 bucket encryption and bucket policies. Kobi also lists S3 buckets and object names. Never object contents.",
				Kobi:      true,
				Autopilot: true,
			},
			grants: []awsGrant{
				{service: "ec2", ops: []string{"DescribeVolumes", "DescribeVolumeStatus"}},
				{service: "efs", iam: "elasticfilesystem", ops: []string{"Describe*"}},
				{service: "s3", ops: []string{"GetBucketEncryption", "GetBucketPolicy"}},
				{service: "s3", ops: []string{"ListBuckets", "GetBucketLocation", "ListObjectsV2"}, kobiOnly: true},
			},
		},
		{
			Family: Family{
				Label:     "Encryption keys (KMS)",
				Detail:    "Whether a key is enabled, disabled or pending deletion, its rotation, aliases, key policy and grants. Never key material, and nothing is decrypted.",
				Kobi:      true,
				Autopilot: true,
			},
			grants: []awsGrant{
				{service: "kms", ops: []string{
					"DescribeKey", "GetKeyPolicy", "GetKeyRotationStatus",
					"ListAliases", "ListGrants", "ListResourceTags",
				}},
			},
		},
		{
			Family: Family{
				Label:     "Functions (Lambda)",
				Detail:    "A function's state and configuration, its reserved concurrency and the queues and streams that feed it. Never its code, and environment variables by name only.",
				Kobi:      true,
				Autopilot: true,
			},
			grants: []awsGrant{
				{service: "lambda", ops: []string{"GetFunctionConfiguration", "GetFunctionConcurrency", "ListEventSourceMappings"}},
			},
		},
		{
			Family: Family{
				Label:     "Change history (CloudTrail)",
				Detail:    "Who changed what and when: the management events of the last 90 days.",
				Kobi:      true,
				Autopilot: true,
			},
			grants: []awsGrant{
				{service: "cloudtrail", ops: []string{"LookupEvents"}},
			},
		},
		{
			Family: Family{
				Label:     "AWS Health and quotas",
				Detail:    "AWS Health events for your services and regions, and Service Quotas limits.",
				Kobi:      true,
				Autopilot: true,
			},
			grants: []awsGrant{
				{service: "health", ops: []string{"Describe*"}},
				{service: "service-quotas", iam: "servicequotas", ops: []string{"GetServiceQuota", "GetAWSDefaultServiceQuota", "ListServiceQuotas"}},
			},
		},
		{
			Family: Family{
				Label:     "Identity and container images",
				Detail:    "The identity KubeBolt uses, IAM roles and their policies (for IRSA and Pod Identity), ECR repositories, images and repository policies. Kobi can also simulate what a role is allowed to do.",
				Kobi:      true,
				Autopilot: true,
			},
			grants: []awsGrant{
				{service: "sts", ops: []string{"GetCallerIdentity"}},
				{service: "iam", ops: []string{
					"GetRole", "GetRolePolicy", "ListRolePolicies", "ListAttachedRolePolicies",
					"GetPolicy", "GetPolicyVersion", "GetInstanceProfile",
				}},
				{service: "iam", ops: []string{"SimulatePrincipalPolicy"}, kobiOnly: true},
				{service: "ecr", aliases: []string{"api.ecr"}, ops: []string{"DescribeRepositories", "DescribeImages", "GetRepositoryPolicy"}},
			},
		},
		{
			Family: Family{
				Label:  "Cost and right-sizing",
				Detail: "Cost Explorer spend and anomalies, Compute Optimizer and Cost Optimization Hub recommendations, public pricing, and Trusted Advisor checks (Business support or higher).",
				Kobi:   true,
			},
			grants: []awsGrant{
				{service: "ce", aliases: []string{"costexplorer"}, ops: []string{"GetCostAndUsage", "GetAnomalies", "GetDimensionValues"}},
				{service: "compute-optimizer", ops: []string{"Get*"}},
				{service: "cost-optimization-hub", ops: []string{"List*"}},
				{service: "pricing", aliases: []string{"api.pricing"}, ops: []string{"Describe*", "Get*", "List*"}},
				{service: "support", ops: []string{"DescribeTrustedAdvisor*"}},
			},
		},
		{
			Family: Family{
				Label:  "Secret and parameter names",
				Detail: "Whether a Secrets Manager secret or a Parameter Store parameter exists, its metadata, and when each version of a secret was created and which stages it holds. Never their values.",
				Kobi:   true,
			},
			grants: []awsGrant{
				{service: "secretsmanager", ops: []string{"DescribeSecret", "ListSecrets", "ListSecretVersionIds"}},
				{service: "ssm", ops: []string{"DescribeParameters"}},
			},
		},
		{
			Family: Family{
				Label:  "AWS documentation",
				Detail: "Search and read the official AWS documentation, list regions and check which services a region offers. Reads nothing from your account.",
				Kobi:   true,
			},
		},
	}
}

// awsReadVerbs are the operation-name prefixes of AWS read APIs. Anything
// else — Create, Modify, Put, Start, Invoke, Execute, Receive — is refused
// unless it is one of awsVerbExceptions.
var awsReadVerbs = []string{"BatchGet", "Describe", "Filter", "Get", "List", "Lookup", "Search"}

// awsVerbExceptions are reads whose names do not start with a read verb.
// Logs Insights runs a query in three steps, and SimulatePrincipalPolicy
// evaluates a policy without changing anything.
var awsVerbExceptions = map[string]bool{
	"logs:StartQuery":                true,
	"logs:GetQueryResults":           true,
	"logs:StopQuery":                 true,
	"cloudwatch:GetMetricData":       true,
	"cloudwatch:GetMetricStatistics": true,
	"iam:SimulatePrincipalPolicy":    true,
}

// awsDeniedOperations are operation-name patterns refused on every service,
// before the verb rule: several of them look like reads (Get, BatchGet) but
// return a secret, a credential or the data itself.
var awsDeniedOperations = []string{
	"GetSecretValue", "BatchGetSecretValue", "Get*Secret*",
	"GetParameter*",
	"Decrypt",
	"GetObject*", "SelectObjectContent",
	"GetItem", "BatchGetItem", "Query", "Scan",
	"ExecuteStatement", "BatchExecuteStatement", "ExecuteTransaction",
	"GetRecords", "GetShardIterator",
	"GetCredentialReport", "Get*Credential*",
	"Get*Token*", "Get*Password*",
	"GetAuthorizationToken", "GetSessionToken", "GetFederationToken",
	"AssumeRole*",
	"GetDownloadUrlForLayer", "GetConsoleOutput", "GetConsoleScreenshot", "GetLaunchTemplateData",
	// Lambda hands out a pre-signed URL to a function's or a layer's code.
	"GetFunction", "GetLayerVersion*",
	// Instance user data (see awsUserDataIAMActions). autoscaling's
	// Describe* would otherwise admit DescribeLaunchConfigurations.
	"DescribeLaunchConfigurations", "DescribeInstanceAttribute", "DescribeLaunchTemplateVersions",
	"DescribeSpotFleetRequests", "DescribeSpotInstanceRequests",
}

// awsDeniedServices are refused whole: their read-looking operations are the
// data plane (rds-data runs SQL, dynamodbstreams returns items) or
// credentials (sso). KMS is not among them: no KMS read returns key material, and its
// one operation that returns data, Decrypt, is in awsDeniedOperations.
var awsDeniedServices = map[string]bool{
	"rds-data":        true,
	"dynamodbstreams": true,
	"sso":             true,
	"sso-oidc":        true,
}

// awsDeniedIAMActions is the explicit Deny of the role policy in the setup
// guide: the secret values and data-plane reads neither consumer may ever
// reach, stated in IAM so the barrier holds even if the
// customer later attaches a broader policy to the role.
var awsDeniedIAMActions = []string{
	"dynamodb:BatchGetItem",
	"dynamodb:GetItem",
	"dynamodb:PartiQLSelect",
	"dynamodb:Query",
	"dynamodb:Scan",
	"ec2:GetPasswordData",
	"ecr:GetAuthorizationToken",
	"ecr:GetDownloadUrlForLayer",
	"iam:GetCredentialReport",
	"kms:Decrypt",
	"lambda:GetFunction",
	"lambda:GetLayerVersion",
	"rds-data:*",
	"redshift:GetClusterCredentials*",
	"s3:GetObject*",
	"secretsmanager:BatchGetSecretValue",
	"secretsmanager:GetSecretValue",
	"ssm:GetParameter*",
}

// awsUserDataIAMActions return an instance's user data — bootstrap scripts
// that routinely carry join tokens and passwords — under names that read
// like any other Describe. The session policies keep the service-level
// wildcards (autoscaling:Describe*, ec2:Describe*) that make them fit STS's
// 2,048 characters, and those wildcards cover these; so both session policies
// deny them explicitly, and so does the role policy in the setup guide,
// which holds even when the customer's role is a broad ReadOnlyAccess.
var awsUserDataIAMActions = []string{
	"autoscaling:DescribeLaunchConfigurations",
	"ec2:DescribeInstanceAttribute",
	"ec2:DescribeLaunchTemplateVersions",
	"ec2:DescribeSpotFleetRequests",
	"ec2:DescribeSpotInstanceRequests",
}

// awsIAMActionOverrides maps the few operations whose IAM action is not
// named after them.
var awsIAMActionOverrides = map[string]string{
	"s3:ListBuckets":         "s3:ListAllMyBuckets",
	"s3:ListObjectsV2":       "s3:ListBucket",
	"s3:GetBucketEncryption": "s3:GetEncryptionConfiguration",
}

var (
	awsServiceRE   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
	awsOperationRE = regexp.MustCompile(`^[A-Z][A-Za-z0-9]{1,127}$`)
	awsRegionRE    = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]{1,2}$`)
)

// awsServiceRule is what one consumer may call on one service, merged over
// every family that grants it.
type awsServiceRule struct {
	service string
	iam     string
	// names are the normalized names api_calls may report the service as.
	names map[string]bool
	ops   []string
}

func (r *awsServiceRule) allows(op string) bool {
	for _, p := range r.ops {
		if awsOpMatch(p, op) {
			return true
		}
	}
	return false
}

var (
	awsRulesOnce sync.Once
	awsRules     map[Consumer]map[string]*awsServiceRule
)

// awsRulesFor returns the per-service rules of a consumer. The table is
// immutable, so it is merged once.
func awsRulesFor(c Consumer) map[string]*awsServiceRule {
	awsRulesOnce.Do(func() {
		awsRules = map[Consumer]map[string]*awsServiceRule{
			ConsumerKobi:      buildAWSRules(ConsumerKobi),
			ConsumerAutopilot: buildAWSRules(ConsumerAutopilot),
		}
	})
	return awsRules[c]
}

func buildAWSRules(c Consumer) map[string]*awsServiceRule {
	rules := map[string]*awsServiceRule{}
	for _, f := range awsFamilyTable() {
		for _, g := range f.grants {
			if !awsGrantFor(f, g, c) {
				continue
			}
			r := rules[g.service]
			if r == nil {
				r = &awsServiceRule{service: g.service, iam: g.iamPrefix(), names: map[string]bool{}}
				rules[g.service] = r
			}
			r.names[awsNorm(g.service)] = true
			r.names[awsNorm(g.iamPrefix())] = true
			for _, a := range g.aliases {
				r.names[awsNorm(a)] = true
			}
			for _, op := range g.ops {
				if !awsContains(r.ops, op) {
					r.ops = append(r.ops, op)
				}
			}
		}
	}
	return rules
}

func awsGrantFor(f awsFamily, g awsGrant, c Consumer) bool {
	switch c {
	case ConsumerKobi:
		return f.Kobi
	case ConsumerAutopilot:
		return f.Autopilot && !g.kobiOnly
	}
	return false
}

func (g awsGrant) iamPrefix() string {
	if g.iam != "" {
		return g.iam
	}
	return g.service
}

// awsServices returns the services a consumer may call, sorted.
func awsServices(c Consumer) []string {
	rules := awsRulesFor(c)
	out := make([]string, 0, len(rules))
	for s := range rules {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// awsDenied is a refusal the model can act on: the message says what is
// allowed instead. It never reaches AWS.
type awsDenied struct{ msg string }

func (e *awsDenied) Error() string { return e.msg }

func awsDenyf(format string, args ...any) *awsDenied {
	return &awsDenied{msg: fmt.Sprintf(format, args...)}
}

// awsCheck decides whether consumer may run service.operation. The order
// matters: the deny set is checked before the verb rule, because the
// dangerous reads (GetSecretValue, BatchGetItem) carry read verbs.
func awsCheck(c Consumer, service, operation string) (*awsServiceRule, error) {
	if !awsServiceRE.MatchString(service) {
		return nil, awsDenyf("service must be a boto3 client name such as \"rds\", \"cloudwatch\" or \"elbv2\"")
	}
	if !awsOperationRE.MatchString(operation) {
		return nil, awsDenyf("operation must be the AWS API operation name in PascalCase, such as \"DescribeDBInstances\"")
	}
	if awsDeniedServices[service] {
		return nil, awsDenyf("%s is never available: it returns data or key material, not configuration", service)
	}
	for _, p := range awsDeniedOperations {
		if awsOpMatch(p, operation) {
			return nil, awsDenyf("%s is never allowed: it can return secret values, credentials or data contents", operation)
		}
	}
	if awsVerbOf(operation) == "" && !awsVerbExceptions[service+":"+operation] {
		return nil, awsDenyf("%s is not a read operation; this connector only reads (Describe, List, Get, BatchGet, Lookup, Search, Filter)", operation)
	}
	rule := awsRulesFor(c)[service]
	if rule == nil {
		return nil, awsDenyf("service %q is not available; available services: %s", service, strings.Join(awsServices(c), ", "))
	}
	if !rule.allows(operation) {
		return nil, awsDenyf("%s.%s is not enabled; enabled for %s: %s", service, operation, service, strings.Join(rule.ops, ", "))
	}
	return rule, nil
}

// AWSCallAllowed reports whether consumer may run service.operation through
// the typed call tool: the allowlist the binding enforces, decided without
// contacting AWS. get_external_connections uses it so a first-tool
// suggestion ("call s3 GetBucketLocation") names only calls the asking
// consumer can make.
func AWSCallAllowed(c Consumer, service, operation string) bool {
	_, err := awsCheck(c, service, operation)
	return err == nil
}

// awsVerbOf returns the read verb an operation starts with, "" when none.
// The verb must be followed by an upper-case letter so "Getaway" is not a Get.
func awsVerbOf(op string) string {
	for _, v := range awsReadVerbs {
		if len(op) > len(v) && strings.HasPrefix(op, v) && op[len(v)] >= 'A' && op[len(v)] <= 'Z' {
			// "BatchGet" is listed before "Get" and no verb is a prefix of
			// another at a word boundary, so the first match is the verb.
			return v
		}
	}
	return ""
}

// awsOpMatch matches an operation against an exact name or a glob, on the
// normalized name: awsOperationRE admits any casing after the first letter, so
// matched exactly, autoscaling's Describe* admitted
// "DescribeLaunchCONFIGURATIONS" past the deny set, and only the upstream
// sandbox's exact name lookup stood in the way. The api_calls post-check
// compares normalized names too, so the two agree. Operation names contain no
// '/', so path.Match's '*' spans the whole name.
func awsOpMatch(pattern, op string) bool {
	pattern, op = strings.ToLower(pattern), awsNorm(op)
	if !strings.Contains(pattern, "*") {
		return pattern == op
	}
	ok, err := path.Match(pattern, op)
	return err == nil && ok
}

// awsNorm reduces a service or operation name to lower-case letters and
// digits, so "rds.describe_db_instances" and "rds:DescribeDBInstances" meet.
func awsNorm(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func awsContains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// ----- IAM policies -----

// awsIAMActions returns the IAM actions a consumer's session may exercise,
// sorted and compressed: exact operations of one verb collapse into
// "prefix:Verb*" when there are two or more of them and the wildcard would
// not cover an action of awsDeniedIAMActions. STS caps an inline session
// policy at 2,048 characters, and the typed allowlist — not IAM — is what
// narrows a wildcard back to the listed operations.
func awsIAMActions(c Consumer) []string {
	byPrefix := map[string]map[string]bool{}
	add := func(action string) {
		prefix, name, _ := strings.Cut(action, ":")
		if byPrefix[prefix] == nil {
			byPrefix[prefix] = map[string]bool{}
		}
		byPrefix[prefix][name] = true
	}
	for _, s := range awsServices(c) {
		rule := awsRulesFor(c)[s]
		for _, op := range rule.ops {
			if o, ok := awsIAMActionOverrides[rule.service+":"+op]; ok {
				add(o)
				continue
			}
			add(rule.iam + ":" + op)
		}
	}

	var out []string
	prefixes := make([]string, 0, len(byPrefix))
	for p := range byPrefix {
		prefixes = append(prefixes, p)
	}
	sort.Strings(prefixes)
	for _, prefix := range prefixes {
		names := byPrefix[prefix]
		groups := map[string][]string{}
		var kept []string
		for name := range names {
			v := awsVerbOf(name)
			if strings.HasSuffix(name, "*") || v == "" {
				kept = append(kept, name)
				continue
			}
			groups[v] = append(groups[v], name)
		}
		for v, g := range groups {
			if len(g) >= 2 && !awsWildcardHitsDeny(prefix, v) {
				kept = append(kept, v+"*")
			} else {
				kept = append(kept, g...)
			}
		}
		// Drop exact names a kept wildcard already covers.
		var final []string
		for _, name := range kept {
			covered := false
			for _, w := range kept {
				if w != name && strings.HasSuffix(w, "*") && awsOpMatch(w, strings.TrimSuffix(name, "*")) {
					covered = true
					break
				}
			}
			if !covered && !awsContains(final, name) {
				final = append(final, name)
			}
		}
		sort.Strings(final)
		for _, name := range final {
			out = append(out, prefix+":"+name)
		}
	}
	return out
}

// awsWildcardHitsDeny reports whether "prefix:verb*" would also grant an
// action the role policy denies.
func awsWildcardHitsDeny(prefix, verb string) bool {
	for _, d := range awsDeniedIAMActions {
		p, name, _ := strings.Cut(d, ":")
		if p == prefix && (name == "*" || strings.HasPrefix(name, verb)) {
			return true
		}
	}
	return false
}

type awsIAMPolicy struct {
	Version   string            `json:"Version"`
	Statement []awsIAMStatement `json:"Statement"`
}

type awsIAMStatement struct {
	Sid       string                       `json:"Sid,omitempty"`
	Effect    string                       `json:"Effect"`
	Principal map[string]string            `json:"Principal,omitempty"`
	Action    any                          `json:"Action,omitempty"`
	NotAction []string                     `json:"NotAction,omitempty"`
	Resource  string                       `json:"Resource,omitempty"`
	Condition map[string]map[string]string `json:"Condition,omitempty"`
}

const awsIAMPolicyVersion = "2012-10-17"

// awsSessionPolicy is the inline policy passed to AssumeRole for one
// consumer. The effective permissions are its intersection with the role's
// own policy, so Autopilot's credentials cannot read what only Kobi may,
// whatever the role allows. Minified: the STS limit counts characters.
func awsSessionPolicy(c Consumer) (string, error) {
	p := awsIAMPolicy{Version: awsIAMPolicyVersion, Statement: []awsIAMStatement{
		{Effect: "Allow", Action: awsIAMActions(c), Resource: "*"},
		{Effect: "Deny", Action: awsUserDataIAMActions, Resource: "*"},
	}}
	b, err := awsMarshalPolicy(p, "")
	if err != nil {
		return "", err
	}
	if len(b) > awsMaxSessionPolicyChars {
		return "", fmt.Errorf("mcpcatalog: aws session policy for %s is %d characters, over the STS limit of %d", c, len(b), awsMaxSessionPolicyChars)
	}
	return b, nil
}

// awsMaxSessionPolicyChars mirrors mcpclient.MaxSessionPolicyChars (STS's
// limit), kept here so the table tests do not need the transport.
const awsMaxSessionPolicyChars = 2048

// awsRolePolicy is the permissions policy the setup guide asks the customer
// to attach to the role: every read either consumer may use, the explicit
// Deny of secret values, data-plane reads and user data, and a Deny of anything else
// that arrives through the AWS MCP Server (aws:ViaAWSMCPService), which
// keeps the role read-only even if a broader policy is attached later.
func awsRolePolicy() (string, error) {
	reads := awsIAMActions(ConsumerKobi)
	p := awsIAMPolicy{Version: awsIAMPolicyVersion, Statement: []awsIAMStatement{
		{Sid: "KubeBoltReadOnly", Effect: "Allow", Action: reads, Resource: "*"},
		{Sid: "NeverSecretsOrData", Effect: "Deny", Action: awsRoleDeniedIAMActions(), Resource: "*"},
		{
			Sid:       "NothingElseThroughMCP",
			Effect:    "Deny",
			NotAction: reads,
			Resource:  "*",
			Condition: map[string]map[string]string{"Bool": {"aws:ViaAWSMCPService": "true"}},
		},
	}}
	return awsMarshalPolicy(p, "  ")
}

// awsRoleDeniedIAMActions is the role policy's explicit Deny: secret values,
// data-plane reads and instance user data, sorted.
func awsRoleDeniedIAMActions() []string {
	out := append(append([]string(nil), awsDeniedIAMActions...), awsUserDataIAMActions...)
	sort.Strings(out)
	return out
}

// awsTrustPolicy lets KubeBolt's principal assume the role only with the
// org's external ID — the confused-deputy guard that stops another KubeBolt
// org from pointing at this role.
//
// principal is KubeBolt's own AWS identity when the operator configured it
// (Env.AWSPrincipalARN), and externalID the one KubeBolt generated for the
// org. Anything that is not a plain IAM ARN or an external ID falls back to
// a placeholder rather than printing something the admin would paste.
func awsTrustPolicy(principal, externalID string) (string, error) {
	if !awsPrincipalARNRE.MatchString(principal) {
		principal = awsPrincipalPlaceholder
	}
	if !awsIsExternalID(externalID) {
		externalID = awsExternalIDPlaceholder
	}
	p := awsIAMPolicy{Version: awsIAMPolicyVersion, Statement: []awsIAMStatement{{
		Sid:       "KubeBoltAssumeRole",
		Effect:    "Allow",
		Principal: map[string]string{"AWS": principal},
		Action:    "sts:AssumeRole",
		Condition: map[string]map[string]string{"StringEquals": {"sts:ExternalId": externalID}},
	}}}
	return awsMarshalPolicy(p, "  ")
}

// awsPrincipalARNRE admits the IAM principals a trust policy can name: a
// role, a user, or an account root.
var awsPrincipalARNRE = regexp.MustCompile(`^arn:aws(-[a-z]+)*:iam::\d{12}:(root|(role|user)/[\w+=,.@/-]{1,512})$`)

// awsIsExternalID reports whether v has the shape of an external ID: the
// field's pattern, within STS's limit.
func awsIsExternalID(v string) bool {
	return len(v) <= awsExternalIDMaxLen && compiledPattern(awsExternalIDPattern).MatchString(v)
}

const (
	awsPrincipalPlaceholder = "<KUBEBOLT_PRINCIPAL_ARN>"
	// awsExternalIDPlaceholder stands in until the org has its external ID;
	// the name says where the real one comes from.
	awsExternalIDPlaceholder = "<EXTERNAL_ID_FROM_KUBEBOLT>"
)

// awsMarshalPolicy encodes without HTML escaping: the guide's placeholders
// contain '<' and '>', which json.Marshal would turn into \u003c.
func awsMarshalPolicy(p awsIAMPolicy, indent string) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if indent != "" {
		enc.SetIndent("", indent)
	}
	if err := enc.Encode(p); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}
