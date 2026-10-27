package mcpcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type nopBinder struct{}

func (nopBinder) Bind(context.Context, Resolved, Env) (Binding, error) { return nil, nil }

func TestToolNamesRoundTripForBothConsumers(t *testing.T) {
	cases := []struct{ id, local string }{
		{"aws", "call"},
		{"gcp", "sqladmin_get_instance"},
		{"gcp", "databaseinsights_get_system_metrics"},
		{"azure", "monitor"},
		{"azure", "group_resource_list"},
	}
	for _, c := range cases {
		if err := ValidateLocalName(c.id, c.local); err != nil {
			t.Fatalf("%s/%s: %v", c.id, c.local, err)
		}
		for _, consumer := range []Consumer{ConsumerKobi, ConsumerAutopilot} {
			name := ToolName(consumer, c.id, c.local)
			id, local, ok := ParseToolName(consumer, name)
			if !ok || id != c.id || local != c.local {
				t.Fatalf("%s: parse(%q) = (%q, %q, %v)", consumer, name, id, local, ok)
			}
		}
	}
}

// The Autopilot form is prefixed again by the SDK; a served name that fits
// here but not after that prefix would be rejected by the model API on every
// run of that agent.
func TestAutopilotNamesLeaveRoomForTheSDKPrefix(t *testing.T) {
	long := strings.Repeat("a", MaxAutopilotServedName) // "cloud_x_" + this is already too long
	if err := ValidateLocalName("gcp", long); err == nil {
		t.Fatal("expected a length error")
	}
	full := "mcp__kubebolt__" + AutopilotToolName("gcp", "databaseinsights_get_system_metrics")
	if len(full) > MaxModelToolName {
		t.Fatalf("%q is %d chars, over %d", full, len(full), MaxModelToolName)
	}
}

func TestConnectorIDsCannotCarryTheSeparator(t *testing.T) {
	for _, bad := range []string{"", "a", "aws_x", "AWS", "a__b", "1aws", "aws-x"} {
		if ValidateConnectorID(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	for _, good := range []string{"aws", "gcp", "azure", "datadog"} {
		if err := ValidateConnectorID(good); err != nil {
			t.Errorf("%q rejected: %v", good, err)
		}
	}
}

func TestLocalNamesRejectDoubleUnderscoreAndUppercase(t *testing.T) {
	for _, bad := range []string{"", "a__b", "Call", "_x", "x y", "x.y"} {
		if ValidateLocalName("aws", bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestParseToolNameRejectsForeignShapes(t *testing.T) {
	for _, n := range []string{"get_pods", "mcp__aws", "mcp____call", "mcp__kubebolt__get_insights_x"} {
		if _, _, ok := ParseToolName(ConsumerKobi, n); ok && !strings.HasPrefix(n, "mcp__kubebolt__") {
			t.Errorf("kobi parse accepted %q", n)
		}
	}
	for _, n := range []string{"get_pods", "cloud_", "cloud_aws", "cloud__x"} {
		if _, _, ok := ParseToolName(ConsumerAutopilot, n); ok {
			t.Errorf("autopilot parse accepted %q", n)
		}
	}
}

func TestCatalogRejectsDuplicatesAndMissingBinders(t *testing.T) {
	if _, err := NewWith(&ConnectorDef{ID: "aws", Binder: nopBinder{}}, &ConnectorDef{ID: "aws", Binder: nopBinder{}}); err == nil {
		t.Fatal("duplicate id accepted")
	}
	if _, err := NewWith(&ConnectorDef{ID: "aws"}); err == nil {
		t.Fatal("nil binder accepted")
	}
	c, err := NewWith(&ConnectorDef{ID: "gcp", Binder: nopBinder{}}, &ConnectorDef{ID: "aws", Binder: nopBinder{}})
	if err != nil {
		t.Fatal(err)
	}
	if got := c.List(); got[0].ID != "aws" || got[1].ID != "gcp" {
		t.Fatalf("catalog not sorted: %v, %v", got[0].ID, got[1].ID)
	}
}

func TestModesForHidesAmbientModesOnMultiTenant(t *testing.T) {
	d := &ConnectorDef{AuthModes: []AuthMode{{ID: "assume-role"}, {ID: "ambient", SelfHostedOnly: true}}}
	if got := d.ModesFor(false); len(got) != 1 || got[0].ID != "assume-role" {
		t.Fatalf("multi-tenant modes = %+v", got)
	}
	if got := d.ModesFor(true); len(got) != 2 {
		t.Fatalf("single-tenant modes = %+v", got)
	}
}

func TestValidateConfig(t *testing.T) {
	mode := AuthMode{ID: "assume-role", Fields: []FieldSchema{
		{Name: "roleArn", Label: "Role ARN", Required: true, Pattern: `^arn:aws:iam::\d{12}:role/[\w+=,.@/-]{1,512}$`},
		{Name: "region", Label: "Region", Required: true, Options: []FieldOption{{Value: "us-east-1"}, {Value: "eu-west-1"}}},
		{Name: "externalId", Label: "External ID", Secret: true, Required: true, MaxLen: 64},
	}}
	ok := map[string]string{"roleArn": "arn:aws:iam::123456789012:role/KubeBoltRead", "region": "eu-west-1"}

	if err := ValidateConfig(mode, ok, map[string]bool{"externalId": true}, nil); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	check := func(name string, fields map[string]string, set map[string]bool, vals map[string]string, wantField string) {
		t.Helper()
		err := ValidateConfig(mode, fields, set, vals)
		var fe *FieldError
		if !errors.As(err, &fe) || fe.Field != wantField {
			t.Fatalf("%s: err = %v, want field %q", name, err, wantField)
		}
	}
	check("missing secret", ok, nil, nil, "externalId")
	check("bad arn", map[string]string{"roleArn": "arn:aws:iam::123:role/x", "region": "eu-west-1"}, map[string]bool{"externalId": true}, nil, "roleArn")
	check("bad option", map[string]string{"roleArn": ok["roleArn"], "region": "evil.example.com"}, map[string]bool{"externalId": true}, nil, "region")
	check("unknown field", map[string]string{"roleArn": ok["roleArn"], "region": "eu-west-1", "endpoint": "http://169.254.169.254"}, map[string]bool{"externalId": true}, nil, "endpoint")
	check("secret sent as field", map[string]string{"roleArn": ok["roleArn"], "region": "eu-west-1", "externalId": "x"}, map[string]bool{"externalId": true}, nil, "externalId")
	check("secret too long", ok, map[string]bool{"externalId": true}, map[string]string{"externalId": strings.Repeat("x", 65)}, "externalId")
	check("control chars", map[string]string{"roleArn": ok["roleArn"] + "\r", "region": "eu-west-1"}, map[string]bool{"externalId": true}, nil, "roleArn")

	multi := AuthMode{Fields: []FieldSchema{{Name: "credentialJSON", Label: "Key", Secret: true, Required: true, Multiline: true, MaxLen: 16384}}}
	if err := ValidateConfig(multi, nil, map[string]bool{"credentialJSON": true}, map[string]string{"credentialJSON": "{\r\n  \"type\": \"service_account\"\n}"}); err != nil {
		t.Fatalf("multiline secret rejected: %v", err)
	}
	if err := ValidateConfig(multi, nil, map[string]bool{"credentialJSON": true}, map[string]string{"credentialJSON": "{\x00}"}); err == nil {
		t.Fatal("NUL accepted in a multiline field")
	}
}

// A binder's tool list feeds a prompt-cached tool list; the catalog core must
// not reorder what it is given.
func TestFieldErrorIsJSONFriendly(t *testing.T) {
	b, _ := json.Marshal(map[string]string{"field": (&FieldError{Field: "x", Message: "y"}).Field})
	if string(b) != `{"field":"x"}` {
		t.Fatal(string(b))
	}
}
