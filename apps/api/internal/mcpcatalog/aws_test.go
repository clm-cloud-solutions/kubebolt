package mcpcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestAWSConnectorDefinition(t *testing.T) {
	def := newAWSConnector()
	if _, err := NewWith(def); err != nil {
		t.Fatalf("catalog rejects the aws connector: %v", err)
	}
	if def.ID != "aws" || def.Name != "Amazon Web Services" || def.Category != CategoryCloud || def.MinPlan != "team" {
		t.Fatalf("identity = %q %q %q %q", def.ID, def.Name, def.Category, def.MinPlan)
	}
	if !strings.HasPrefix(def.DocsURL, "https://docs.aws.amazon.com/") {
		t.Fatalf("docs url %q", def.DocsURL)
	}
	for _, tag := range def.Tags {
		if strings.Contains(strings.ToLower(tag), "read-only") || strings.Contains(strings.ToLower(tag), "read only") {
			t.Fatalf("tag %q: read-only is a property of every connector, not a tag", tag)
		}
	}
	if len(def.Never) == 0 || len(def.Families) == 0 || def.Guide == nil || def.Binder == nil {
		t.Fatal("definition is missing never, families, guide or binder")
	}

	role, ok := def.Mode(awsModeAssumeRole)
	if !ok || role.SelfHostedOnly {
		t.Fatalf("assume-role mode = %+v", role)
	}
	want := map[string]struct{ secret, required, advanced bool }{
		"roleArn":    {false, true, false},
		"externalId": {false, true, false},
		"region":     {false, true, false},
		"hostRegion": {false, false, true},
	}
	checkFields(t, role, want)
	// The external ID is KubeBolt's, generated per org: the admin copies it
	// into the trust policy and never chooses it (the confused deputy).
	for _, f := range role.Fields {
		if f.Generated != (f.Name == "externalId") {
			t.Errorf("assume-role field %s generated = %v", f.Name, f.Generated)
		}
	}
	amb, ok := def.Mode(awsModeAmbient)
	if !ok || !amb.SelfHostedOnly {
		t.Fatalf("ambient mode = %+v", amb)
	}
	checkFields(t, amb, map[string]struct{ secret, required, advanced bool }{
		"region":     {false, true, false},
		"hostRegion": {false, false, true},
		"profile":    {false, false, true},
	})
	if got := def.ModesFor(false); len(got) != 1 || got[0].ID != awsModeAssumeRole {
		t.Fatalf("multi-tenant install offers %+v", got)
	}

	// Every local tool name must fit both consumer forms.
	for _, c := range []Consumer{ConsumerKobi, ConsumerAutopilot} {
		for _, spec := range awsToolSpecs(c, "us-east-2") {
			if err := ValidateLocalName(def.ID, spec.Name); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func checkFields(t *testing.T, m AuthMode, want map[string]struct{ secret, required, advanced bool }) {
	t.Helper()
	if len(m.Fields) != len(want) {
		t.Fatalf("mode %s has %d fields, want %d", m.ID, len(m.Fields), len(want))
	}
	for _, f := range m.Fields {
		w, ok := want[f.Name]
		if !ok {
			t.Fatalf("mode %s: unexpected field %q", m.ID, f.Name)
		}
		if f.Secret != w.secret || f.Required != w.required || f.Advanced != w.advanced {
			t.Fatalf("mode %s field %s = secret %v required %v advanced %v", m.ID, f.Name, f.Secret, f.Required, f.Advanced)
		}
		if f.Name == "hostRegion" && (f.Default != "us-east-1" || len(f.Options) != 2) {
			t.Fatalf("hostRegion = %+v", f)
		}
	}
}

func TestAWSFieldValidation(t *testing.T) {
	role, _ := newAWSConnector().Mode(awsModeAssumeRole)
	good := map[string]string{
		"roleArn":    "arn:aws:iam::123456789012:role/team/KubeBoltReadOnly",
		"externalId": testGeneratedExternalID,
		"region":     "us-east-2",
		"hostRegion": "eu-central-1",
	}
	if err := ValidateConfig(role, good, nil, nil); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	with := func(k, v string) map[string]string {
		m := map[string]string{}
		for kk, vv := range good {
			m[kk] = vv
		}
		m[k] = v
		return m
	}
	bad := []struct {
		name   string
		fields map[string]string
		field  string
	}{
		{"arn in another partition", with("roleArn", "arn:aws-cn:iam::123456789012:role/x"), "roleArn"},
		{"arn of a user", with("roleArn", "arn:aws:iam::123456789012:user/x"), "roleArn"},
		{"arn short account", with("roleArn", "arn:aws:iam::12345:role/x"), "roleArn"},
		{"arn with a url", with("roleArn", "https://evil.example.com/arn:aws:iam::123456789012:role/x"), "roleArn"},
		{"arn trailing space", with("roleArn", "arn:aws:iam::123456789012:role/x y"), "roleArn"},
		{"region hostname", with("region", "evil.example.com"), "region"},
		{"region path", with("region", "us-east-1/../x"), "region"},
		{"host region not offered", with("hostRegion", "us-west-2"), "hostRegion"},
		{"external id missing", with("externalId", ""), "externalId"},
		{"external id too short", with("externalId", "x"), "externalId"},
		{"external id under 16", with("externalId", "0123456789abcde"), "externalId"},
		{"external id bad chars", with("externalId", "abc def"), "externalId"},
		{"external id too long", with("externalId", strings.Repeat("a", 1225)), "externalId"},
	}
	for _, c := range bad {
		err := ValidateConfig(role, c.fields, nil, nil)
		var fe *FieldError
		if !errors.As(err, &fe) || fe.Field != c.field {
			t.Errorf("%s: err = %v, want field %q", c.name, err, c.field)
		}
	}
	for _, n := range []int{16, 1224} {
		if err := ValidateConfig(role, with("externalId", strings.Repeat("a", n)), nil, nil); err != nil {
			t.Errorf("%d-character external id rejected: %v", n, err)
		}
	}
	// The external ID is a generated setting, no longer a secret the admin
	// types: one sent as a secret is refused, not stored.
	noID := map[string]string{"roleArn": good["roleArn"], "region": good["region"]}
	err := ValidateConfig(role, noID, map[string]bool{"externalId": true}, map[string]string{"externalId": "chosen-by-the-customer-0123"})
	var fe *FieldError
	if !errors.As(err, &fe) || fe.Field != "externalId" {
		t.Errorf("external id as a secret: err = %v", err)
	}

	amb, _ := newAWSConnector().Mode(awsModeAmbient)
	if err := ValidateConfig(amb, map[string]string{"region": "eu-west-1", "profile": "kubebolt-readonly"}, nil, nil); err != nil {
		t.Fatalf("valid ambient config rejected: %v", err)
	}
	for _, p := range []string{"../etc/passwd", "a b", "x;y", strings.Repeat("p", 65)} {
		if ValidateConfig(amb, map[string]string{"region": "eu-west-1", "profile": p}, nil, nil) == nil {
			t.Errorf("profile %q accepted", p)
		}
	}
}

// exampleOp turns an allowlist pattern into a concrete operation it admits.
func exampleOp(pattern string) string {
	if strings.HasSuffix(pattern, "*") {
		return strings.TrimSuffix(pattern, "*") + "Things"
	}
	return pattern
}

func TestAWSEveryFamilyServiceHasAnAllowedOperation(t *testing.T) {
	for _, f := range awsFamilyTable() {
		for _, g := range f.grants {
			if len(g.ops) == 0 {
				t.Errorf("%s/%s grants no operation", f.Label, g.service)
			}
			for _, c := range []Consumer{ConsumerKobi, ConsumerAutopilot} {
				if !awsGrantFor(f, g, c) {
					continue
				}
				for _, p := range g.ops {
					if _, err := awsCheck(c, g.service, exampleOp(p)); err != nil {
						t.Errorf("%s: %s.%s (from %s) refused: %v", c, g.service, exampleOp(p), p, err)
					}
				}
			}
		}
	}
	// The canary and the three live reads must stay allowed.
	for _, op := range [][2]string{{"sts", "GetCallerIdentity"}, {"ec2", "DescribeRegions"}, {"rds", "DescribeDBInstances"}} {
		for _, c := range []Consumer{ConsumerKobi, ConsumerAutopilot} {
			if _, err := awsCheck(c, op[0], op[1]); err != nil {
				t.Errorf("%s %s.%s: %v", c, op[0], op[1], err)
			}
		}
	}
}

func TestAWSDenySetWinsOverVerbs(t *testing.T) {
	refused := []struct {
		consumer  Consumer
		service   string
		operation string
	}{
		// Read verbs that return secrets, credentials or data.
		{ConsumerKobi, "secretsmanager", "GetSecretValue"},
		{ConsumerKobi, "secretsmanager", "BatchGetSecretValue"},
		{ConsumerKobi, "secretsmanager", "GetResourcePolicy"}, // a Get, but not enabled
		{ConsumerKobi, "ssm", "GetParameter"},
		{ConsumerKobi, "ssm", "GetParameters"},
		{ConsumerKobi, "ssm", "GetParametersByPath"},
		{ConsumerKobi, "ssm", "GetParameterHistory"},
		{ConsumerKobi, "ssm", "DescribeDocument"},
		{ConsumerKobi, "s3", "GetObject"},
		{ConsumerKobi, "s3", "GetObjectAcl"},
		{ConsumerKobi, "s3", "SelectObjectContent"},
		{ConsumerKobi, "dynamodb", "GetItem"},
		{ConsumerKobi, "dynamodb", "BatchGetItem"},
		{ConsumerKobi, "dynamodb", "Query"},
		{ConsumerKobi, "dynamodb", "Scan"},
		{ConsumerKobi, "dynamodb", "ExecuteStatement"},
		{ConsumerKobi, "rds-data", "ExecuteStatement"},
		{ConsumerKobi, "rds-data", "GetThings"},
		{ConsumerKobi, "kms", "Decrypt"},
		{ConsumerKobi, "kms", "GetParametersForImport"},
		{ConsumerKobi, "kms", "ListKeys"},       // a List, but not enabled: keys are found from the error or the bucket
		{ConsumerKobi, "lambda", "GetFunction"}, // a pre-signed URL to the code
		{ConsumerAutopilot, "lambda", "GetFunction"},
		{ConsumerKobi, "lambda", "GetLayerVersion"},
		{ConsumerKobi, "lambda", "GetLayerVersionByArn"},
		{ConsumerKobi, "lambda", "ListFunctions"}, // every function's environment at once: not enabled
		{ConsumerKobi, "iam", "GetCredentialReport"},
		{ConsumerKobi, "ecr", "GetAuthorizationToken"},
		{ConsumerKobi, "ecr", "GetDownloadUrlForLayer"},
		{ConsumerKobi, "sts", "GetSessionToken"},
		{ConsumerKobi, "sts", "GetFederationToken"},
		{ConsumerKobi, "sts", "AssumeRole"},
		{ConsumerKobi, "ec2", "GetPasswordData"},
		{ConsumerKobi, "ec2", "GetLaunchTemplateData"},
		{ConsumerKobi, "ec2", "DescribeInstanceAttribute"}, // user data: not enabled
		// User data behind a service-level Describe*: autoscaling grants
		// Describe*, so only the deny set stops these.
		{ConsumerKobi, "autoscaling", "DescribeLaunchConfigurations"},
		{ConsumerAutopilot, "autoscaling", "DescribeLaunchConfigurations"},
		// ...in any casing: the deny set is matched on the normalized name,
		// or autoscaling's Describe* would admit a case variant and only the
		// upstream sandbox's exact lookup would stand in the way.
		{ConsumerKobi, "autoscaling", "DescribeLaunchCONFIGURATIONS"},
		{ConsumerKobi, "autoscaling", "DescribeLaunchconfigurations"},
		{ConsumerAutopilot, "autoscaling", "DescribeLaunchCONFIGURATIONS"},
		{ConsumerAutopilot, "autoscaling", "DescribeLaunchconfigurations"},
		{ConsumerKobi, "route53", "GetHostedZoneTOKEN"}, // Get*Token* behind route53's Get*
		{ConsumerKobi, "rds", "DescribeInstanceATTRIBUTE"},
		{ConsumerKobi, "ec2", "DescribeLaunchTemplateVersions"},
		{ConsumerKobi, "redshift", "GetClusterCredentials"},
		// Writes.
		{ConsumerKobi, "rds", "DeleteDBInstance"},
		{ConsumerKobi, "rds", "ModifyDBInstance"},
		{ConsumerKobi, "rds", "RebootDBInstance"},
		{ConsumerKobi, "ec2", "RunInstances"},
		{ConsumerKobi, "ec2", "AuthorizeSecurityGroupIngress"},
		{ConsumerKobi, "sqs", "ReceiveMessage"},
		{ConsumerKobi, "logs", "PutLogEvents"},
		{ConsumerKobi, "ssm", "SendCommand"},
		{ConsumerKobi, "lambda", "Invoke"},
		// Shapes.
		{ConsumerKobi, "rds", "describe_db_instances"},
		{ConsumerKobi, "RDS", "DescribeDBInstances"},
		{ConsumerKobi, `rds", operation_name="DeleteDBInstance`, "DescribeDBInstances"},
		{ConsumerKobi, "rds", `DescribeDBInstances")`},
		{ConsumerKobi, "rds", ""},
		{ConsumerKobi, "", "DescribeDBInstances"},
	}
	for _, c := range refused {
		if _, err := awsCheck(c.consumer, c.service, c.operation); err == nil {
			t.Errorf("%s %s.%s allowed", c.consumer, c.service, c.operation)
		}
	}
	// The deny set is consulted before the verb rule: GetSecretValue is a Get,
	// and the message says why it is refused, not that it is unknown.
	_, err := awsCheck(ConsumerKobi, "secretsmanager", "GetSecretValue")
	if err == nil || !strings.Contains(err.Error(), "never allowed") {
		t.Fatalf("GetSecretValue: %v", err)
	}
	for _, c := range []Consumer{ConsumerKobi, ConsumerAutopilot} {
		for svc, rule := range awsRulesFor(c) {
			for _, p := range rule.ops {
				for _, d := range awsDeniedOperations {
					if !strings.Contains(p, "*") && awsOpMatch(d, p) {
						t.Errorf("%s: %s.%s is listed but denied by %s", c, svc, p, d)
					}
				}
			}
		}
	}
}

// TestAWSDenySetIgnoresCase checks every denied operation in three casings
// against every service each consumer may call: a service-level wildcard
// (autoscaling Describe*, route53 Get*) must not admit a variant of a name the
// deny set refuses. The variants keep the read verb and the capital after it,
// as the verb rule requires, and change the case of the rest.
func TestAWSDenySetIgnoresCase(t *testing.T) {
	variants := func(op string) []string {
		keep := len(awsVerbOf(op)) + 1
		head, tail := op[:keep], op[keep:]
		swapped := []rune(tail)
		for i, r := range swapped {
			switch {
			case r >= 'a' && r <= 'z':
				swapped[i] = r - 'a' + 'A'
			case r >= 'A' && r <= 'Z':
				swapped[i] = r - 'A' + 'a'
			}
		}
		return []string{head + strings.ToUpper(tail), head + strings.ToLower(tail), head + string(swapped)}
	}
	for _, c := range []Consumer{ConsumerKobi, ConsumerAutopilot} {
		for svc := range awsRulesFor(c) {
			for _, d := range awsDeniedOperations {
				for _, op := range variants(strings.ReplaceAll(d, "*", "Things")) {
					if _, err := awsCheck(c, svc, op); err == nil {
						t.Errorf("%s: %s.%s allowed (denied as %s)", c, svc, op, d)
					}
				}
			}
		}
	}
	// Case-insensitive matching narrows the deny side only: the operations
	// the table lists stay allowed as AWS spells them.
	if _, err := awsCheck(ConsumerKobi, "autoscaling", "DescribeAutoScalingGroups"); err != nil {
		t.Fatalf("DescribeAutoScalingGroups: %v", err)
	}
}

func TestAWSAutopilotIsASubsetOfKobi(t *testing.T) {
	kobi := awsRulesFor(ConsumerKobi)
	for svc, rule := range awsRulesFor(ConsumerAutopilot) {
		k := kobi[svc]
		if k == nil {
			t.Fatalf("autopilot service %s missing for kobi", svc)
		}
		for _, op := range rule.ops {
			if !awsContains(k.ops, op) {
				t.Errorf("autopilot %s.%s not granted to kobi", svc, op)
			}
		}
	}
	for _, f := range awsFamilyTable() {
		if f.Autopilot && !f.Kobi {
			t.Errorf("family %q is Autopilot-only", f.Label)
		}
	}
	kobiOnly := [][2]string{
		{"ce", "GetCostAndUsage"}, {"compute-optimizer", "GetEC2InstanceRecommendations"}, {"pricing", "GetProducts"},
		{"support", "DescribeTrustedAdvisorChecks"}, {"secretsmanager", "DescribeSecret"}, {"ssm", "DescribeParameters"},
		{"s3", "ListBuckets"}, {"xray", "GetTraceSummaries"}, {"iam", "SimulatePrincipalPolicy"},
	}
	for _, op := range kobiOnly {
		if _, err := awsCheck(ConsumerKobi, op[0], op[1]); err != nil {
			t.Errorf("kobi %s.%s: %v", op[0], op[1], err)
		}
		if _, err := awsCheck(ConsumerAutopilot, op[0], op[1]); err == nil {
			t.Errorf("autopilot may call %s.%s", op[0], op[1])
		}
	}
	if _, err := awsCheck(Consumer("someone"), "rds", "DescribeDBInstances"); err == nil {
		t.Error("an unknown consumer may call AWS")
	}
}

// AWSCallAllowed is the exported face of awsCheck: get_external_connections
// asks it before suggesting a call, so the two must never disagree.
func TestAWSCallAllowedIsTheBindingsAllowlist(t *testing.T) {
	for _, tc := range []struct {
		c           Consumer
		service, op string
		wantAllowed bool
	}{
		{ConsumerKobi, "s3", "GetBucketLocation", true},
		{ConsumerAutopilot, "s3", "GetBucketLocation", false},
		{ConsumerAutopilot, "rds", "DescribeDBInstances", true},
		{ConsumerAutopilot, "cloudwatch", "GetMetricData", true},
		{ConsumerKobi, "secretsmanager", "GetSecretValue", false},
		{ConsumerKobi, "rds", "DeleteDBInstance", false},
		{Consumer("someone"), "rds", "DescribeDBInstances", false},
	} {
		_, err := awsCheck(tc.c, tc.service, tc.op)
		if got := AWSCallAllowed(tc.c, tc.service, tc.op); got != tc.wantAllowed || got != (err == nil) {
			t.Errorf("AWSCallAllowed(%s, %s, %s) = %v, want %v (awsCheck: %v)", tc.c, tc.service, tc.op, got, tc.wantAllowed, err)
		}
	}
}

var awsActionRE = regexp.MustCompile(`^[a-z0-9-]+:[A-Za-z0-9*]+$`)

// awsIAMCovers reports whether IAM action pattern k grants action a (a may
// itself be a wildcard, then k must grant everything it does).
func awsIAMCovers(k, a string) bool {
	kp, kn, _ := strings.Cut(k, ":")
	ap, an, _ := strings.Cut(a, ":")
	if kp != ap {
		return false
	}
	if kn == an {
		return true
	}
	return strings.HasSuffix(kn, "*") && strings.HasPrefix(strings.TrimSuffix(an, "*"), strings.TrimSuffix(kn, "*"))
}

func awsIAMCoveredBy(set []string, a string) bool {
	for _, k := range set {
		if awsIAMCovers(k, a) {
			return true
		}
	}
	return false
}

func TestAWSSessionPoliciesFitSTSAndMatchTheAllowlist(t *testing.T) {
	actions := map[Consumer][]string{}
	for _, c := range []Consumer{ConsumerKobi, ConsumerAutopilot} {
		p, err := awsSessionPolicy(c)
		if err != nil {
			t.Fatal(err)
		}
		if len(p) > awsMaxSessionPolicyChars {
			t.Fatalf("%s session policy is %d characters", c, len(p))
		}
		t.Logf("%s session policy: %d characters", c, len(p))
		var doc struct {
			Version   string
			Statement []struct {
				Effect   string
				Action   []string
				Resource string
			}
		}
		if err := json.Unmarshal([]byte(p), &doc); err != nil {
			t.Fatalf("%s: invalid JSON: %v", c, err)
		}
		if doc.Version != "2012-10-17" || len(doc.Statement) != 2 || doc.Statement[0].Effect != "Allow" || doc.Statement[0].Resource != "*" {
			t.Fatalf("%s: unexpected policy %s", c, p)
		}
		// The session's own Deny of user data: the Allow's Describe*
		// wildcards cover it, and the customer's role may be ReadOnlyAccess.
		if d := doc.Statement[1]; d.Effect != "Deny" || d.Resource != "*" || !reflect.DeepEqual(d.Action, awsUserDataIAMActions) {
			t.Fatalf("%s: user-data deny = %+v", c, d)
		}
		actions[c] = doc.Statement[0].Action
		for _, a := range actions[c] {
			if !awsActionRE.MatchString(a) {
				t.Errorf("%s: malformed action %q", c, a)
			}
			for _, d := range awsDeniedIAMActions {
				if awsIAMCovers(a, d) || awsIAMCovers(d, a) {
					t.Errorf("%s: %s overlaps the denied %s", c, a, d)
				}
			}
		}
		// Everything the typed tool may call is granted by the session.
		for svc, rule := range awsRulesFor(c) {
			for _, op := range rule.ops {
				a := rule.iam + ":" + op
				if o, ok := awsIAMActionOverrides[svc+":"+op]; ok {
					a = o
				}
				if !awsIAMCoveredBy(actions[c], a) {
					t.Errorf("%s: %s (for %s.%s) not in the session policy", c, a, svc, op)
				}
			}
		}
	}
	for _, a := range actions[ConsumerAutopilot] {
		if !awsIAMCoveredBy(actions[ConsumerKobi], a) {
			t.Errorf("autopilot action %s not granted to kobi", a)
		}
	}
	for _, a := range []string{"ce:GetCostAndUsage", "secretsmanager:DescribeSecret", "ssm:DescribeParameters", "s3:ListBucket", "iam:SimulatePrincipalPolicy", "xray:GetTraceSummaries", "pricing:GetProducts"} {
		if awsIAMCoveredBy(actions[ConsumerAutopilot], a) {
			t.Errorf("autopilot session grants the Kobi-only %s", a)
		}
		if !awsIAMCoveredBy(actions[ConsumerKobi], a) {
			t.Errorf("kobi session lacks %s", a)
		}
	}
	// The typed allowlist refuses every user-data read the IAM policies deny,
	// so the two barriers cannot drift apart.
	for _, a := range awsUserDataIAMActions {
		svc, op, _ := strings.Cut(a, ":")
		for _, c := range []Consumer{ConsumerKobi, ConsumerAutopilot} {
			if _, err := awsCheck(c, svc, op); err == nil {
				t.Errorf("%s may call %s.%s, which returns user data", c, svc, op)
			}
		}
	}
	// Deterministic: the same table always yields the same bytes.
	a, _ := awsSessionPolicy(ConsumerKobi)
	b, _ := awsSessionPolicy(ConsumerKobi)
	if a != b {
		t.Fatal("session policy is not deterministic")
	}
}

func TestAWSRoleAndTrustPolicies(t *testing.T) {
	role, err := awsRolePolicy()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Statement []struct {
			Sid       string
			Effect    string
			Action    []string
			NotAction []string
			Condition map[string]map[string]string
		}
	}
	if err := json.Unmarshal([]byte(role), &doc); err != nil {
		t.Fatalf("role policy: %v", err)
	}
	if len(doc.Statement) != 3 {
		t.Fatalf("role policy has %d statements", len(doc.Statement))
	}
	allow, deny, mcp := doc.Statement[0], doc.Statement[1], doc.Statement[2]
	if allow.Effect != "Allow" || !reflect.DeepEqual(allow.Action, awsIAMActions(ConsumerKobi)) {
		t.Fatalf("allow statement = %+v", allow)
	}
	if deny.Effect != "Deny" || !reflect.DeepEqual(deny.Action, awsRoleDeniedIAMActions()) {
		t.Fatalf("deny statement = %+v", deny)
	}
	for _, must := range []string{"secretsmanager:GetSecretValue", "ssm:GetParameter*", "kms:Decrypt", "rds-data:*", "s3:GetObject*", "dynamodb:GetItem", "dynamodb:Query", "dynamodb:Scan", "dynamodb:BatchGetItem",
		"autoscaling:DescribeLaunchConfigurations", "ec2:DescribeInstanceAttribute", "ec2:DescribeLaunchTemplateVersions"} {
		if !awsContains(deny.Action, must) {
			t.Errorf("role policy does not deny %s", must)
		}
	}
	if mcp.Effect != "Deny" || !reflect.DeepEqual(mcp.NotAction, allow.Action) || mcp.Condition["Bool"]["aws:ViaAWSMCPService"] != "true" {
		t.Fatalf("MCP deny statement = %+v", mcp)
	}

	type trustDoc struct {
		Statement []struct {
			Principal map[string]string
			Action    string
			Condition map[string]map[string]string
		}
	}
	parseTrust := func(principal, externalID string) (string, trustDoc) {
		t.Helper()
		trust, err := awsTrustPolicy(principal, externalID)
		if err != nil {
			t.Fatal(err)
		}
		var td trustDoc
		if err := json.Unmarshal([]byte(trust), &td); err != nil {
			t.Fatal(err)
		}
		if len(td.Statement) != 1 || td.Statement[0].Action != "sts:AssumeRole" {
			t.Fatalf("trust policy = %s", trust)
		}
		return trust, td
	}

	// Nothing known yet: both placeholders, readable as such.
	trust, td := parseTrust("", "")
	if td.Statement[0].Principal["AWS"] != awsPrincipalPlaceholder ||
		td.Statement[0].Condition["StringEquals"]["sts:ExternalId"] != awsExternalIDPlaceholder {
		t.Fatalf("trust policy = %s", trust)
	}
	if strings.Contains(trust, `\u003c`) || !strings.Contains(trust, "<KUBEBOLT_PRINCIPAL_ARN>") || !strings.Contains(trust, "<EXTERNAL_ID_FROM_KUBEBOLT>") {
		t.Fatal("placeholders are HTML-escaped or unclear")
	}

	// The org's generated external ID is the condition.
	trust, td = parseTrust("", testGeneratedExternalID)
	if td.Statement[0].Condition["StringEquals"]["sts:ExternalId"] != testGeneratedExternalID || strings.Contains(trust, awsExternalIDPlaceholder) {
		t.Fatalf("trust policy does not require the org's external ID: %s", trust)
	}
	// A stored value that is not an external ID is never printed into a
	// policy the admin pastes.
	for _, bad := range []string{"short", "has space in it 0123456789", `x","Effect":"Allow`, strings.Repeat("a", 1225)} {
		if _, td := parseTrust("", bad); td.Statement[0].Condition["StringEquals"]["sts:ExternalId"] != awsExternalIDPlaceholder {
			t.Errorf("%q was printed into the trust policy", bad)
		}
	}
}

// testGeneratedExternalID has the shape the gateway generates: 32 random
// bytes, hex.
const testGeneratedExternalID = "5be0c3a1f2d94e8b7a6c5d4e3f2a1b0c9d8e7f6a5b4c3d2e1f0a9b8c7d6e5f4a"

func TestAWSGuide(t *testing.T) {
	role, _ := awsRolePolicy()
	trust, _ := awsTrustPolicy("", testGeneratedExternalID)
	g := awsGuide(Env{}, map[string]string{"roleArn": "arn:aws:iam::123456789012:role/platform/KubeBoltProd", "externalId": testGeneratedExternalID})
	if len(g.Steps) != 4 {
		t.Fatalf("guide has %d steps", len(g.Steps))
	}
	if g.Steps[0].Code != role || g.Steps[0].Language != "json" {
		t.Fatal("step 1 is not the generated role policy")
	}
	if g.Steps[1].Code != trust || g.Steps[1].Language != "json" {
		t.Fatal("step 2 is not the generated trust policy")
	}
	if g.Steps[2].Language != "shell" || !strings.Contains(g.Steps[2].Code, "--role-name KubeBoltProd ") {
		t.Fatalf("step 3 = %q", g.Steps[2].Code)
	}
	// The org's external ID is KubeBolt's to give: the trust policy already
	// requires it, the text names it for an admin working in the console,
	// and the CLI prints what the role requires so the two can be compared.
	// Nothing asks the admin to make one up.
	if !strings.Contains(g.Steps[1].Body, "`"+testGeneratedExternalID+"`") || !strings.Contains(g.Steps[2].Body, "`"+testGeneratedExternalID+"`") {
		t.Fatalf("the guide does not name the org's external ID:\n%s\n%s", g.Steps[1].Body, g.Steps[2].Body)
	}
	if !strings.Contains(g.Steps[2].Code, `"sts:ExternalId"`) {
		t.Fatalf("the CLI does not print the role's external ID: %q", g.Steps[2].Code)
	}
	all, _ := json.Marshal(g)
	for _, stale := range []string{"openssl rand", "Generate a random external ID", "the external ID and the region"} {
		if strings.Contains(string(all), stale) {
			t.Errorf("the guide still asks the admin for their own external ID (%q)", stale)
		}
	}
	if strings.Contains(string(all), "root user") {
		t.Fatal("the ambient step is shown on an install that does not allow it")
	}
	// The UI numbers the steps; a title never carries its own number, and
	// only an alternative path is unnumbered.
	numbered := regexp.MustCompile(`^\s*(\d+|[a-z])[.)]\s`)
	for _, st := range g.Steps {
		if numbered.MatchString(st.Title) {
			t.Errorf("title %q carries its own number", st.Title)
		}
		if st.Alternative {
			t.Errorf("step %q is marked as an alternative", st.Title)
		}
	}

	amb := awsGuide(Env{AmbientAllowed: true}, nil)
	if len(amb.Steps) != 5 || !strings.Contains(amb.Steps[4].Body, "does not restrict") {
		t.Fatalf("ambient guide = %+v", amb.Steps[len(amb.Steps)-1])
	}
	// "Or use this machine's credentials" replaces steps 1-4; it is not a
	// fifth step after them.
	for i, st := range amb.Steps {
		if st.Alternative != (i == 4) || numbered.MatchString(st.Title) {
			t.Errorf("ambient guide step %d %q alternative = %v", i, st.Title, st.Alternative)
		}
	}
	if !strings.Contains(amb.Steps[2].Code, "--role-name "+awsDefaultRoleName+" ") {
		t.Fatal("default role name not used")
	}

	// Before the org has its external ID, or with a stored value that is not
	// one, the policy carries a placeholder the text explains.
	for _, fields := range []map[string]string{nil, {"externalId": "x\ny"}, {"externalId": "kb-too-short"}} {
		g := awsGuide(Env{}, fields)
		if !strings.Contains(g.Steps[1].Code, awsExternalIDPlaceholder) || !strings.Contains(g.Steps[1].Body, awsExternalIDPlaceholder) {
			t.Errorf("fields %q: step 2 = %q / %q", fields, g.Steps[1].Body, g.Steps[1].Code)
		}
		if v := fields["externalId"]; v != "" {
			if all, _ := json.Marshal(g); strings.Contains(string(all), v) {
				t.Errorf("fields %q: the guide printed a value that is not an external ID", fields)
			}
		}
	}
}

func TestAWSToolSpecs(t *testing.T) {
	names := func(specs []ToolSpec) []string {
		var out []string
		for _, s := range specs {
			out = append(out, s.Name)
		}
		return out
	}
	if got := names(awsToolSpecs(ConsumerKobi, "us-east-2")); !reflect.DeepEqual(got, []string{"call", "search_documentation", "read_documentation", "list_regions", "get_regional_availability"}) {
		t.Fatalf("kobi tools = %v", got)
	}
	if got := names(awsToolSpecs(ConsumerAutopilot, "us-east-2")); !reflect.DeepEqual(got, []string{"call"}) {
		t.Fatalf("autopilot tools = %v", got)
	}
	for _, c := range []Consumer{ConsumerKobi, ConsumerAutopilot} {
		a, _ := json.Marshal(awsToolSpecs(c, "us-east-2"))
		b, _ := json.Marshal(awsToolSpecs(c, "us-east-2"))
		if string(a) != string(b) {
			t.Fatalf("%s tool list is not deterministic", c)
		}
		for _, s := range awsToolSpecs(c, "us-east-2") {
			// None of the upstream prompt material is forwarded.
			for _, bad := range []string{"ALL-REGIONS", "Return ALL", "run_script", "Bash", "aws___", "call_boto3", "aws_mcp"} {
				if strings.Contains(s.Description, bad) {
					t.Errorf("%s/%s description carries %q", c, s.Name, bad)
				}
			}
			if s.InputSchema["type"] != "object" {
				t.Errorf("%s/%s schema is not an object", c, s.Name)
			}
		}
		call := awsToolSpecs(c, "us-east-2")[0]
		enum := call.InputSchema["properties"].(map[string]any)["service"].(map[string]any)["enum"].([]string)
		if !reflect.DeepEqual(enum, awsServices(c)) {
			t.Errorf("%s: call enum %v", c, enum)
		}
		if !strings.Contains(call.Description, "us-east-2") || !strings.Contains(call.Description, "Never ask for secrets") {
			t.Errorf("%s: call description misses the region or the secrets rule", c)
		}
		t.Logf("%s call description: %d characters", c, len(call.Description))
	}
	auto := awsToolSpecs(ConsumerAutopilot, "us-east-2")[0]
	if strings.Contains(auto.Description, "Cost and right-sizing") || strings.Contains(auto.Description, "secretsmanager") {
		t.Error("autopilot's call description lists Kobi-only services")
	}
}

// ----- script rendering -----

// parsePyStr decodes one literal as awsPyStr writes it and returns the rest.
// It is deliberately strict: any escape awsPyStr never emits is an error, so
// a literal that parses here is exactly one Python str token.
func parsePyStr(s string) (string, string, error) {
	if !strings.HasPrefix(s, `"`) {
		return "", "", fmt.Errorf("no opening quote in %q", s)
	}
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			return b.String(), s[i+1:], nil
		case c == '\\':
			if i+1 >= len(s) {
				return "", "", fmt.Errorf("dangling backslash")
			}
			switch s[i+1] {
			case '"', '\\':
				b.WriteByte(s[i+1])
				i++
			case 'u', 'U':
				n := 4
				if s[i+1] == 'U' {
					n = 8
				}
				if i+2+n > len(s) {
					return "", "", fmt.Errorf("short escape")
				}
				var r rune
				if _, err := fmt.Sscanf(s[i+2:i+2+n], "%x", &r); err != nil {
					return "", "", err
				}
				b.WriteRune(r)
				i += 1 + n
			default:
				return "", "", fmt.Errorf("unexpected escape \\%c", s[i+1])
			}
		case c < 0x20 || c > 0x7e:
			return "", "", fmt.Errorf("raw byte 0x%02x inside a literal", c)
		default:
			b.WriteByte(c)
		}
	}
	return "", "", fmt.Errorf("unterminated literal")
}

// parseAWSCode checks that code is exactly the fixed template and returns the
// four values it carries. The fake MCP server uses it too.
func parseAWSCode(code string) (service, operation, region, params string, err error) {
	rest := code
	expect := func(lit string) error {
		if !strings.HasPrefix(rest, lit) {
			return fmt.Errorf("expected %q at %q", lit, rest)
		}
		rest = rest[len(lit):]
		return nil
	}
	take := func(dst *string) error {
		v, r, err := parsePyStr(rest)
		if err != nil {
			return err
		}
		*dst, rest = v, r
		return nil
	}
	steps := []func() error{
		func() error { return expect("result = await call_boto3(service_name=") },
		func() error { return take(&service) },
		func() error { return expect(", operation_name=") },
		func() error { return take(&operation) },
		func() error { return expect(", region_name=") },
		func() error { return take(&region) },
		func() error { return expect(", params=json.loads(") },
		func() error { return take(&params) },
		func() error { return expect("))\nresult") },
	}
	for _, s := range steps {
		if err = s(); err != nil {
			return "", "", "", "", err
		}
	}
	if rest != "" {
		return "", "", "", "", fmt.Errorf("trailing code %q", rest)
	}
	return service, operation, region, params, nil
}

var awsHostileParams = []string{
	`{}`,
	`{"DBInstanceIdentifier":"x\"))\nimport os\nos.system(\"id\")\n#"}`,
	`{"a":"\\\" + __import__('os').system('id') + \""}`,
	`{"a":")) ; result = 1 ; ((("}`,
	`{"a":"line1\nline2\r\n  \u0000end"}`,
	`{"a":"line1\nline2\r\n\u2028\u2029\u0000\u001f end"}`,
	`{"a":"ünïcødé 日本語 😀"}`,
	`{"a":"\ud83d\ude00 \u00fc \ufeff"}`,
	`{"a":"\\u0022 not a quote"}`,
	`{"a":"'''\"\"\" triple quotes"}`,
	`{"a":"</script><b>&amp;"}`,
	`{"Filters":[{"Name":"tag:env","Values":["prod","\\\\server\\share"]}],"MaxRecords":20}`,
}

func TestAWSScriptLiteralsCannotBreakOut(t *testing.T) {
	for _, raw := range awsHostileParams {
		params, err := awsParams(json.RawMessage(raw))
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		code := awsScript("rds", "DescribeDBInstances", "us-east-2", params)
		lines := strings.Split(code, "\n")
		if len(lines) != 2 || lines[1] != "result" {
			t.Fatalf("%s: script has %d lines: %q", raw, len(lines), code)
		}
		for i := 0; i < len(lines[0]); i++ {
			if lines[0][i] < 0x20 || lines[0][i] > 0x7e {
				t.Fatalf("%s: non-printable byte in the script", raw)
			}
		}
		svc, op, region, got, err := parseAWSCode(code)
		if err != nil {
			t.Fatalf("%s: %v\n%s", raw, err, code)
		}
		if svc != "rds" || op != "DescribeDBInstances" || region != "us-east-2" || got != string(params) {
			t.Fatalf("%s: round trip gave %q %q %q %q", raw, svc, op, region, got)
		}
		var v any
		if err := json.Unmarshal([]byte(got), &v); err != nil {
			t.Fatalf("%s: the params literal is not the params JSON: %v", raw, err)
		}
	}
}

// When python3 is available, parse the generated script with Python's own
// parser and check it is one assignment of one call with constant string
// arguments, followed by the bare name — nothing else can be in it.
func TestAWSScriptParsesAsPython(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	const checker = `
import ast, json, sys
code = sys.stdin.read()
src = "async def _f():\n" + "".join("    " + l + "\n" for l in code.split("\n"))
mod = ast.parse(src)
body = mod.body[0].body
assert len(body) == 2, len(body)
a, e = body
assert isinstance(a, ast.Assign) and len(a.targets) == 1 and a.targets[0].id == "result"
aw = a.value
assert isinstance(aw, ast.Await)
call = aw.value
assert isinstance(call, ast.Call) and call.func.id == "call_boto3" and not call.args
kw = {k.arg: k.value for k in call.keywords}
assert list(kw) == ["service_name", "operation_name", "region_name", "params"], list(kw)
out = {}
for k in ["service_name", "operation_name", "region_name"]:
    assert isinstance(kw[k], ast.Constant) and isinstance(kw[k].value, str)
    out[k] = kw[k].value
p = kw["params"]
assert isinstance(p, ast.Call) and isinstance(p.func, ast.Attribute) and p.func.attr == "loads" and p.func.value.id == "json"
assert len(p.args) == 1 and not p.keywords and isinstance(p.args[0], ast.Constant) and isinstance(p.args[0].value, str)
out["params"] = json.loads(p.args[0].value)
assert isinstance(e, ast.Expr) and isinstance(e.value, ast.Name) and e.value.id == "result"
print(json.dumps(out))
`
	for _, raw := range awsHostileParams {
		params, _ := awsParams(json.RawMessage(raw))
		code := awsScript("rds", "DescribeDBInstances", "us-east-2", params)
		cmd := exec.Command(py, "-c", checker)
		cmd.Stdin = strings.NewReader(code)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: python rejected the script: %v\n%s\n%s", raw, err, out, code)
		}
		var got struct {
			Service   string          `json:"service_name"`
			Operation string          `json:"operation_name"`
			Region    string          `json:"region_name"`
			Params    json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("%s: %v (%s)", raw, err, out)
		}
		var want, have any
		_ = json.Unmarshal(params, &want)
		_ = json.Unmarshal(got.Params, &have)
		if got.Service != "rds" || got.Operation != "DescribeDBInstances" || got.Region != "us-east-2" || !reflect.DeepEqual(want, have) {
			t.Fatalf("%s: python read %+v", raw, got)
		}
	}
}

func TestAWSPyStr(t *testing.T) {
	cases := map[string]string{
		``:           `""`,
		`plain`:      `"plain"`,
		`a"b`:        `"a\"b"`,
		`a\b`:        `"a\\b"`,
		"a\nb":       `"a\u000ab"`,
		"\u2028":     `"\u2028"`,
		"é":          `"\u00e9"`,
		"\U0001F600": `"\U0001f600"`,
		"\xff":       `"\ufffd"`,
		"\x7f":       `"\u007f"`,
		"\x00":       `"\u0000"`,
		`'''`:        `"'''"`,
		`{"a":"b"}`:  `"{\"a\":\"b\"}"`,
	}
	for in, want := range cases {
		if got := awsPyStr(in); got != want {
			t.Errorf("awsPyStr(%q) = %s, want %s", in, got, want)
		}
		if got := awsPyStr(in); !utf8.ValidString(got) {
			t.Errorf("awsPyStr(%q) is not valid UTF-8", in)
		}
	}
}

func TestAWSParams(t *testing.T) {
	for _, bad := range []string{`[]`, `"x"`, `1`, `{"a":`, `{"a":1}{"b":2}`} {
		if _, err := awsParams(json.RawMessage(bad)); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	big := `{"a":"` + strings.Repeat("x", awsMaxParamsBytes) + `"}`
	if _, err := awsParams(json.RawMessage(big)); err == nil {
		t.Error("params over 16 KiB accepted")
	}
	for in, want := range map[string]string{``: `{}`, `null`: `{}`, ` { "MaxRecords" : 20 } `: `{"MaxRecords":20}`} {
		got, err := awsParams(json.RawMessage(in))
		if err != nil || string(got) != want {
			t.Errorf("awsParams(%q) = %s, %v", in, got, err)
		}
	}
}

func TestAWSAPICallNames(t *testing.T) {
	rule, _ := awsCheck(ConsumerKobi, "elbv2", "DescribeTargetHealth")
	ok := []string{
		`{"service":"elbv2","operation":"DescribeTargetHealth","status":"success"}`,
		`{"service_name":"elasticloadbalancingv2","operation_name":"describe_target_health"}`,
		`"elbv2.describe_target_health"`,
		`"elasticloadbalancing:DescribeTargetHealth"`,
	}
	for _, raw := range ok {
		svc, op, found := awsAPICallName(json.RawMessage(raw))
		if !found || !rule.names[awsNorm(svc)] || awsNorm(op) != awsNorm("DescribeTargetHealth") {
			t.Errorf("%s not recognized as elbv2.DescribeTargetHealth (%q %q %v)", raw, svc, op, found)
		}
	}
	for _, raw := range []string{`{"operation":"DescribeTargetHealth"}`, `"DescribeTargetHealth"`, `42`, `{"service":"elbv2"}`} {
		if _, _, found := awsAPICallName(json.RawMessage(raw)); found {
			t.Errorf("%s parsed as a call", raw)
		}
	}
}

func TestAWSTrustPolicyNamesTheConfiguredPrincipal(t *testing.T) {
	const principal = "arn:aws:iam::123456789012:role/kubebolt-api"
	trust, err := awsTrustPolicy(principal, testGeneratedExternalID)
	if err != nil || !strings.Contains(trust, principal) || strings.Contains(trust, awsPrincipalPlaceholder) {
		t.Fatalf("trust policy does not name the configured principal: %v\n%s", err, trust)
	}
	// Anything that is not a plain IAM ARN is never printed into a policy.
	for _, bad := range []string{"*", "arn:aws:iam::123:role/x", "arn:aws:iam::123456789012:role/x\n\"Effect\":\"Allow\"", "https://evil.example.com"} {
		trust, _ := awsTrustPolicy(bad, testGeneratedExternalID)
		if !strings.Contains(trust, awsPrincipalPlaceholder) {
			t.Fatalf("%q was printed into the trust policy", bad)
		}
	}
	g := awsGuide(Env{AWSPrincipalARN: principal}, nil)
	var body string
	for _, st := range g.Steps {
		body += st.Body + st.Code
	}
	if !strings.Contains(body, "already names KubeBolt's AWS principal") || !strings.Contains(body, principal) {
		t.Fatal("the guide does not say the principal is filled in")
	}
}

// TestAWSRefusesARoleInKubeBoltsOwnAccount: KubeBolt's principal can reach
// roles in its own account for reasons of its own, and STS ignores an
// external ID a trust policy does not ask for, so a role there that trusts
// the account would open to every org. On a multi-tenant install the save,
// the draft test and Bind all refuse it; a self-hosted install serves one org
// and usually reads its own account, so there it is admitted.
func TestAWSRefusesARoleInKubeBoltsOwnAccount(t *testing.T) {
	v, ok := newAWSConnector().Binder.(ConfigValidator)
	if !ok {
		t.Fatal("the aws binder does not validate configurations")
	}
	own := "arn:aws:iam::111122223333:role/KubeBoltReadOnly"
	other := "arn:aws:iam::444455556666:role/KubeBoltReadOnly"
	fields := func(roleARN string) map[string]string {
		return map[string]string{"roleArn": roleARN, "externalId": testGeneratedExternalID, "region": "us-east-2"}
	}
	for _, principal := range []string{
		"arn:aws:iam::111122223333:role/kubebolt-api",
		"arn:aws:iam::111122223333:root",
		"arn:aws:sts::111122223333:assumed-role/kubebolt-api/i-0abc",
	} {
		env := Env{MultiTenant: true, AWSPrincipalARN: principal}
		err := v.ValidateConfig(awsModeAssumeRole, fields(own), nil, env)
		var fe *FieldError
		if !errors.As(err, &fe) || fe.Field != "roleArn" || fe.Message != "KubeBolt cannot assume a role in its own AWS account." {
			t.Errorf("principal %s: own-account role err = %v", principal, err)
		}
		if err := v.ValidateConfig(awsModeAssumeRole, fields(other), nil, env); err != nil {
			t.Errorf("principal %s: a customer's role refused: %v", principal, err)
		}
		_, err = (&awsBinder{}).Bind(context.Background(), Resolved{Mode: awsModeAssumeRole, Fields: fields(own)}, env)
		if !errors.As(err, &fe) || fe.Field != "roleArn" {
			t.Errorf("principal %s: Bind err = %v", principal, err)
		}
	}
	// Without a configured principal there is no account to compare with,
	// and a roleArn left over from the other mode is not checked.
	if err := v.ValidateConfig(awsModeAssumeRole, fields(own), nil, Env{MultiTenant: true}); err != nil {
		t.Errorf("no principal configured: %v", err)
	}
	selfHosted := Env{AWSPrincipalARN: "arn:aws:iam::111122223333:role/kubebolt-api"}
	if err := v.ValidateConfig(awsModeAssumeRole, fields(own), nil, selfHosted); err != nil {
		t.Errorf("self-hosted, same account: %v", err)
	}
	if err := v.ValidateConfig(awsModeAmbient, map[string]string{"roleArn": own, "region": "us-east-2"}, nil, Env{AmbientAllowed: true, AWSPrincipalARN: own}); err != nil {
		t.Errorf("ambient mode: %v", err)
	}
}
