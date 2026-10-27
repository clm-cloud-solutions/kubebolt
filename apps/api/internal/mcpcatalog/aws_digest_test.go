package mcpcatalog

import (
	"encoding/json"
	"testing"
)

// The rarest event comes first: the one DeleteRoute among the controller's
// repeated writes is the line worth reading, with when it happened.
func TestAWSTrailDigest(t *testing.T) {
	ans := awsTrailAnswer{Events: []awsTrailRow{
		{Time: "2026-10-22T20:30:00Z", Event: "AuthorizeSecurityGroupIngress"},
		{Time: "2026-10-22T20:31:00Z", Event: "AuthorizeSecurityGroupIngress"},
		{Time: "2026-10-22T22:05:12Z", Event: "DeleteRoute"},
		{Time: "2026-10-22T21:40:00Z", Event: "CreateTags"},
	}}
	got := awsTrailDigest(ans, 31)
	want := "4 events · 31 read-only left out · rarest first: DeleteRoute @2026-10-22T22:05:12Z, CreateTags @2026-10-22T21:40:00Z, AuthorizeSecurityGroupIngress ×2"
	if got != want {
		t.Errorf("digest = %q\n          want %q", got, want)
	}
	if got := awsTrailDigest(awsTrailAnswer{MoreEvents: true}, 0); got != "0 events (more on the next page)" {
		t.Errorf("empty page = %q", got)
	}
}

func TestAWSDigest(t *testing.T) {
	cases := []struct {
		name, service, operation, answer, want string
	}{
		{"the route table after the NAT route went", "ec2", "DescribeRouteTables",
			`{"RouteTables":[{"Routes":[{"DestinationCidrBlock":"10.42.0.0/16","GatewayId":"local","State":"active"}]}]}`,
			"1 table · 1 route: 10.42.0.0/16 → local"},
		{"routes, one to a NAT that is gone", "ec2", "DescribeRouteTables",
			`{"RouteTables":[{"Routes":[{"DestinationCidrBlock":"10.42.0.0/16","GatewayId":"local"},{"DestinationCidrBlock":"0.0.0.0/0","NatGatewayId":"nat-0a1b","State":"blackhole"}]},{"Routes":[]}]}`,
			"2 tables · 2 routes: 10.42.0.0/16 → local, 0.0.0.0/0 → nat-0a1b (blackhole)"},
		{"instances by state", "ec2", "DescribeInstances",
			`{"Reservations":[{"Instances":[{"State":{"Name":"running"}},{"State":{"Name":"running"}}]},{"Instances":[{"State":{"Name":"stopped"}}]}]}`,
			"3 instances · running ×2, stopped"},
		{"NAT gateways", "ec2", "DescribeNatGateways",
			`{"NatGateways":[{"State":"available"}]}`,
			"1 NAT gateway · available"},
		{"any other list, with its statuses", "rds", "DescribeDBInstances",
			`{"DBInstances":[{"DBInstanceStatus":"storage-full","Status":"storage-full"}],"ResponseMetadata":{}}`,
			"1 DB instance · storage-full"},
		{"plural words", "ec2", "DescribeSecurityGroups",
			`{"SecurityGroups":[{},{},{}],"NextToken":"x"}`,
			"3 security groups"},
		{"no list: no digest", "s3", "GetBucketPolicy", `{"Policy":"{}"}`, ""},
		{"not an object", "s3", "ListBuckets", `[1,2]`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := awsDigest(c.service, c.operation, json.RawMessage(c.answer)); got != c.want {
				t.Errorf("digest = %q\n          want %q", got, c.want)
			}
		})
	}
}

func TestDigestWords(t *testing.T) {
	for key, want := range map[string]string{
		"NatGateways": "NAT gateway", "Policies": "policy", "Vpcs": "VPC", "Addresses": "address",
	} {
		if got := digestWords(key, 1); got != want {
			t.Errorf("digestWords(%q, 1) = %q, want %q", key, got, want)
		}
	}
	if got := digestWords("NatGateways", 2); got != "NAT gateways" {
		t.Errorf("plural = %q", got)
	}
}
