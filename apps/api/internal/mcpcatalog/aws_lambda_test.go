package mcpcatalog

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// The reads the lab's scenarios need (docs/incident-simulations/
// three-tier-shop-aws): a disabled KMS key behind an S3 bucket, a rotated
// secret, a Lambda consumer throttled to zero.
func TestAWSKeysFunctionsAndBucketConfigAreReadable(t *testing.T) {
	both := [][2]string{
		{"kms", "DescribeKey"}, {"kms", "GetKeyPolicy"}, {"kms", "GetKeyRotationStatus"},
		{"kms", "ListAliases"}, {"kms", "ListGrants"}, {"kms", "ListResourceTags"},
		{"lambda", "GetFunctionConfiguration"}, {"lambda", "GetFunctionConcurrency"}, {"lambda", "ListEventSourceMappings"},
		{"s3", "GetBucketEncryption"}, {"s3", "GetBucketPolicy"},
	}
	for _, op := range both {
		for _, c := range []Consumer{ConsumerKobi, ConsumerAutopilot} {
			if _, err := awsCheck(c, op[0], op[1]); err != nil {
				t.Errorf("%s %s.%s: %v", c, op[0], op[1], err)
			}
		}
	}
	if _, err := awsCheck(ConsumerKobi, "secretsmanager", "ListSecretVersionIds"); err != nil {
		t.Errorf("kobi ListSecretVersionIds: %v", err)
	}
	if _, err := awsCheck(ConsumerAutopilot, "secretsmanager", "ListSecretVersionIds"); err == nil {
		t.Error("autopilot may list secret versions, a Kobi-only family")
	}
}

// The role policy denies Lambda's code URLs: the session's lambda:Get*
// would otherwise cover them on a role as broad as ReadOnlyAccess.
func TestAWSRolePolicyDeniesFunctionCode(t *testing.T) {
	deny := awsRoleDeniedIAMActions()
	for _, must := range []string{"lambda:GetFunction", "lambda:GetLayerVersion"} {
		if !awsContains(deny, must) {
			t.Errorf("role policy does not deny %s", must)
		}
	}
	for _, a := range awsIAMActions(ConsumerKobi) {
		if strings.HasPrefix(a, "lambda:") && strings.HasSuffix(a, "*") {
			t.Errorf("kobi session grants the wildcard %s, which covers GetFunction", a)
		}
	}
}

func TestAWSHideEnvironment(t *testing.T) {
	conf := `{"FunctionName":"order-processor","State":"Active","Timeout":30,
		"Environment":{"Variables":{"DB_PASSWORD":"s3cr3t","QUEUE_URL":"https://sqs.us-east-1.amazonaws.com/111122223333/order-events"}},
		"MemorySize":256}`
	v, n, err := awsHideEnvironment(json.RawMessage(conf))
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	s := string(v)
	if strings.Contains(s, "s3cr3t") || strings.Contains(s, "order-events") {
		t.Fatalf("a value survived: %s", s)
	}
	for _, keep := range []string{`"DB_PASSWORD":"[REDACTED]"`, `"QUEUE_URL":"[REDACTED]"`, `"Timeout":30`, `"State":"Active"`} {
		if !strings.Contains(s, keep) {
			t.Errorf("missing %s in %s", keep, s)
		}
	}

	// A list of functions, and a CloudTrail request in its own casing.
	list := `{"Functions":[{"FunctionName":"a","Environment":{"Variables":{"K":"v1"}}},{"FunctionName":"b"},
		{"FunctionName":"c","Environment":{"Variables":{"K":"v2","L":"v3"},"Error":{"ErrorCode":"KMSAccessDenied"}}}]}`
	if v, n, err := awsHideEnvironment(json.RawMessage(list)); err != nil || n != 3 || strings.Contains(string(v), `"v`) {
		t.Fatalf("list: n=%d err=%v %s", n, err, v)
	}
	req := `{"functionName":"order-processor","environment":{"variables":{"API_KEY":"abc123"}}}`
	if v, n, err := awsHideEnvironment(json.RawMessage(req)); err != nil || n != 1 || strings.Contains(string(v), "abc123") {
		t.Fatalf("request: n=%d err=%v %s", n, err, v)
	}

	// Nothing to hide: the bytes come back untouched, numbers included.
	plain := `{"ReservedConcurrentExecutions":0,"Big":12345678901234567890}`
	if v, n, err := awsHideEnvironment(json.RawMessage(plain)); err != nil || n != 0 || string(v) != plain {
		t.Fatalf("plain: n=%d err=%v %s", n, err, v)
	}
	// AWS's own mask is a string, not a map: left alone.
	masked := `{"environment":{"variables":"HIDDEN_DUE_TO_SECURITY_REASONS"}}`
	if _, n, err := awsHideEnvironment(json.RawMessage(masked)); err != nil || n != 0 {
		t.Fatalf("masked: n=%d err=%v", n, err)
	}
	if _, _, err := awsHideEnvironment(json.RawMessage(`{"Environment":`)); err == nil {
		t.Fatal("a truncated answer was accepted")
	}
}

func TestAWSCompactTrailHidesFunctionEnvironment(t *testing.T) {
	rec, _ := json.Marshal(map[string]any{
		"eventTime": "2026-10-21T10:00:00Z", "eventSource": "lambda.amazonaws.com", "readOnly": false,
		"userIdentity":      map[string]any{"type": "IAMUser", "userName": "ops", "accountId": "111122223333"},
		"requestParameters": map[string]any{"functionName": "order-processor", "environment": map[string]any{"variables": map[string]string{"DB_PASSWORD": "s3cr3t"}}},
	})
	ans, _ := json.Marshal(map[string]any{"Events": []map[string]any{{
		"EventName": "UpdateFunctionConfiguration20150331v2", "EventTime": "2026-10-21T10:00:00Z", "CloudTrailEvent": string(rec),
	}}})
	got, _, ok := awsCompactTrail(ans)
	if !ok || len(got.Events) != 1 {
		t.Fatalf("ok=%v %+v", ok, got)
	}
	if r := string(got.Events[0].Request); strings.Contains(r, "s3cr3t") || !strings.Contains(r, `"DB_PASSWORD":"[REDACTED]"`) {
		t.Fatalf("request = %s", r)
	}
}

func TestAWSBindingHidesFunctionEnvironment(t *testing.T) {
	rig := newAWSRig(t)
	reply := func(ret string) string {
		b, err := json.Marshal(map[string]any{
			"status": "success", "return_value": json.RawMessage(ret),
			"api_calls": []map[string]string{{"service": "lambda", "operation": "GetFunctionConfiguration", "status": "success"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	rig.fake.with(func(f *fakeAWSMCP) {
		f.runScript = func(svc, op, region, params string) (string, bool) {
			return reply(`{"FunctionName":"order-processor","Environment":{"Variables":{"DB_PASSWORD":"s3cr3t"}}}`), false
		}
	})
	b := rig.bind(t, awsModeAmbient)
	res, err := b.Call(context.Background(), ConsumerKobi, awsToolCall,
		callArgs("lambda", "GetFunctionConfiguration", `{"FunctionName":"order-processor"}`, ""))
	if err != nil || res.IsError {
		t.Fatalf("call: %v %+v", err, res)
	}
	if strings.Contains(res.Text, "s3cr3t") || !strings.Contains(res.Text, "DB_PASSWORD") {
		t.Fatalf("text = %s", res.Text)
	}
	if want := fmt.Sprintf(awsLambdaHiddenNote, 1); len(res.Notes) != 1 || res.Notes[0] != want {
		t.Fatalf("notes = %q", res.Notes)
	}
}
