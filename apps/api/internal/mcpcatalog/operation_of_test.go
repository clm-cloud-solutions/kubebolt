package mcpcatalog

import (
	"encoding/json"
	"testing"
)

// debt-09: the platform's blocked list names AWS calls `service.operation`
// and Azure calls by their command, lowercased, and never a malformed name.
func TestOperationOf(t *testing.T) {
	aws := &awsBinding{}
	az := &azureBinding{}
	cases := []struct {
		name string
		b    OperationNamer
		tool string
		args string
		want string
	}{
		{"aws typed call", aws, awsToolCall, `{"service":" S3 ","operation":"GetObject","params":{}}`, "s3.getobject"},
		{"aws malformed operation", aws, awsToolCall, `{"service":"s3","operation":"Get Object"}`, ""},
		{"aws knowledge tool", aws, "search_documentation", `{"search_phrase":"x"}`, ""},
		{"azure command", az, "storage", `{"command":"Storage_Account_Get","parameters":{}}`, "storage_account_get"},
		{"azure learn", az, "storage", `{"learn":true}`, ""},
		{"azure not json", az, "storage", `nope`, ""},
	}
	for _, c := range cases {
		if got := c.b.OperationOf(c.tool, json.RawMessage(c.args)); got != c.want {
			t.Errorf("%s: OperationOf = %q, want %q", c.name, got, c.want)
		}
	}
}
