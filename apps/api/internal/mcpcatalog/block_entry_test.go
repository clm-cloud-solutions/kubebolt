package mcpcatalog

import (
	"encoding/json"
	"strings"
	"testing"
)

// debt-09, found live: `describeinstances` was stored for AWS and blocked
// nothing, since the gateway names a typed call ec2.describeinstances. Each
// connector refuses an entry that matches none of its names and accepts the
// names its calls really get.
func TestBlockEntry_RefusesWhatMatchesNothing(t *testing.T) {
	cases := []struct {
		conn, entry string
		ok          bool
		says        string
	}{
		{"aws", "*", true, ""},
		{"aws", "ec2.describeinstances", true, ""},
		{"aws", "ec2.*", true, ""},
		{"aws", "s3.get*", true, ""},
		{"aws", "search_documentation", true, ""},
		{"aws", "list_*", true, ""},
		{"aws", "describeinstances", false, "names no AWS service"},
		{"aws", "ec2*", false, "names no AWS service"},
		{"aws", "ec2.", false, "is not an AWS operation name"},
		{"aws", "ec2.describe-instances", false, "is not an AWS operation name"},
		{"aws", "ec2.describe.instances", false, "is not an AWS operation name"},
		{"aws", "ec2_x.describeinstances", false, "is not an AWS service name"},
		{"azure", "*", true, ""},
		{"azure", "storage_account_get", true, ""},
		{"azure", "storage_*", true, ""},
		{"azure", "group_list", true, ""},
		{"azure", "storage", true, ""},
		{"azure", "storage_account_delete", false, "is not an Azure command KubeBolt runs"},
		{"azure", "keyvault_secret_*", false, "is not an Azure command KubeBolt runs"},
		{"gcp", "*", true, ""},
		{"gcp", "compute_list_instances", true, ""},
		{"gcp", "compute_*", true, ""},
		{"gcp", "list_instances", false, "is not a Google Cloud tool KubeBolt runs"},
	}
	defs := map[string]*ConnectorDef{"aws": newAWSConnector(), "azure": newAzureDef(), "gcp": newGCPDef()}
	for _, c := range cases {
		msg := defs[c.conn].BlockEntry(c.entry)
		if c.ok != (msg == "") {
			t.Errorf("%s %q: refusal %q, want ok=%v", c.conn, c.entry, msg, c.ok)
			continue
		}
		if !c.ok && !strings.Contains(msg, c.says) {
			t.Errorf("%s %q: refusal %q, want it to say %q", c.conn, c.entry, msg, c.says)
		}
	}
}

// Every name the gateway gives a call is an entry the check accepts: the AWS
// typed call through OperationOf, each AWS tool, every Azure command and flat
// tool, every GCP tool of either consumer.
func TestBlockEntry_AcceptsEveryNameACallGets(t *testing.T) {
	aws := newAWSConnector()
	op := (&awsBinding{}).OperationOf(awsToolCall, json.RawMessage(`{"service":"ec2","operation":"DescribeInstances"}`))
	if op != "ec2.describeinstances" {
		t.Fatalf("OperationOf = %q", op)
	}
	for _, name := range append([]string{op}, awsToolNames...) {
		if msg := aws.BlockEntry(name); msg != "" {
			t.Errorf("aws %q refused: %s", name, msg)
		}
	}
	azure := newAzureDef()
	for _, name := range azureBlockNames() {
		if msg := azure.BlockEntry(name); msg != "" {
			t.Errorf("azure %q refused: %s", name, msg)
		}
	}
	gcp := newGCPDef()
	for _, c := range []Consumer{ConsumerKobi, ConsumerAutopilot} {
		for _, name := range gcpToolNames(c) {
			if msg := gcp.BlockEntry(name); msg != "" {
				t.Errorf("gcp %q refused: %s", name, msg)
			}
		}
	}
}

// Every connector of the catalog says what it accepts: a new one without
// BlockEntry would let any entry through again.
func TestEveryConnectorChecksItsBlockedEntries(t *testing.T) {
	cat, err := NewWithPreview()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range cat.List() {
		if d.BlockEntry == nil {
			t.Errorf("connector %q has no BlockEntry", d.ID)
		}
	}
}
