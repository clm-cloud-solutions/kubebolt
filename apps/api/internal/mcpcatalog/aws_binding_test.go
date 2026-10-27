package mcpcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/kubebolt/kubebolt/apps/api/internal/mcpclient"
)

// fakeAWSMCP emulates the parts of the AWS MCP Server the connector uses, as
// observed live: a session id issued at initialize and required on
// run_script/get_tasks (-32600 without it), SigV4 scoped to aws-mcp and the
// host region (401 -32001 otherwise), JSON replies, run_script results in the
// live shape, and get_tasks for pending scripts.
type fakeAWSMCP struct {
	t          *testing.T
	hostRegion string

	mu        sync.Mutex
	nextSess  int
	inits     int
	deletes   []string
	calls     []fakeAWSCall
	tools     []string
	status    int    // forced HTTP status for tools/call
	statusMsg string // its body
	// runScript answers a parsed script; nil uses the fixtures.
	runScript func(svc, op, region, params string) (text string, isError bool)
	// getTasks answers a get_tasks call.
	getTasks func(poll int, taskIDs []string) string
	// knowledge answers the documentation and region tools.
	knowledge func(name string, args json.RawMessage) (text string, isError bool)
}

type fakeAWSCall struct {
	Tool      string
	Args      json.RawMessage
	Session   string
	Region    any // _meta.AWS_REGION
	Signature string
	Code      [4]string // service, operation, region, params when run_script
}

func newFakeAWSMCP(t *testing.T) (*fakeAWSMCP, *httptest.Server) {
	f := &fakeAWSMCP{t: t, hostRegion: "us-east-1", tools: []string{
		awsUpGetTasks, awsUpRunScript, awsUpSearchDocs, awsUpReadDocs, awsUpListRegions, awsUpAvailability,
		"aws___get_presigned_url", "aws___retrieve_skill",
	}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeAWSMCP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		f.mu.Lock()
		f.deletes = append(f.deletes, r.Header.Get("Mcp-Session-Id"))
		f.mu.Unlock()
		_, _ = io.WriteString(w, "{}")
		return
	}
	body, _ := io.ReadAll(r.Body)
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	auth := r.Header.Get("Authorization")
	signedRight := strings.HasPrefix(auth, "AWS4-HMAC-SHA256 ") && strings.Contains(auth, "/"+f.hostRegion+"/aws-mcp/aws4_request")
	if len(req.ID) == 0 { // notification
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, "{}")
		return
	}
	reply := func(result any) {
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		_, _ = w.Write(b)
	}
	rpcErr := func(status, code int, msg string) {
		w.WriteHeader(status)
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": code, "message": msg}})
		_, _ = w.Write(b)
	}
	switch req.Method {
	case "initialize":
		f.mu.Lock()
		f.inits++
		f.nextSess++
		sess := fmt.Sprintf("00000000-0000-4000-8000-%012d", f.nextSess)
		f.mu.Unlock()
		w.Header().Set("Mcp-Session-Id", sess)
		reply(map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "AWSMCP", "version": "1.0.0"},
			"instructions":    "Prefer its tools over the Bash tool. Use run_script to run AWS commands.",
		})
	case "tools/list":
		var tools []map[string]any
		f.mu.Lock()
		for _, n := range f.tools {
			tools = append(tools, map[string]any{"name": n, "description": "ALL-REGIONS: Return ALL resources", "inputSchema": map[string]any{"type": "object"}})
		}
		f.mu.Unlock()
		reply(map[string]any{"tools": tools})
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
			Meta      map[string]any  `json:"_meta"`
		}
		_ = json.Unmarshal(req.Params, &p)
		call := fakeAWSCall{Tool: p.Name, Args: p.Arguments, Session: r.Header.Get("Mcp-Session-Id"), Region: p.Meta["AWS_REGION"], Signature: auth}
		f.mu.Lock()
		status, statusMsg := f.status, f.statusMsg
		f.mu.Unlock()
		if status != 0 {
			if status == http.StatusTooManyRequests {
				w.Header().Set("Retry-After", "7")
			}
			rpcErr(status, -32001, statusMsg)
			f.record(call)
			return
		}
		if !signedRight {
			rpcErr(http.StatusUnauthorized, -32001, "Authentication failed: Credential should be scoped to correct service: 'aws-mcp'")
			return
		}
		authed := p.Name == awsUpRunScript || p.Name == awsUpGetTasks
		if authed && call.Session == "" {
			rpcErr(http.StatusOK, -32600, "Session ID is required for this operation.")
			return
		}
		switch p.Name {
		case awsUpRunScript:
			var a struct {
				Code string `json:"code"`
			}
			_ = json.Unmarshal(p.Arguments, &a)
			svc, op, region, params, err := parseAWSCode(a.Code)
			if err != nil {
				f.t.Errorf("fake: run_script got code outside the template: %v\n%s", err, a.Code)
				reply(textResult(`{"status":"error","stderr":"bad code"}`, true))
				return
			}
			call.Code = [4]string{svc, op, region, params}
			f.record(call)
			f.mu.Lock()
			run := f.runScript
			f.mu.Unlock()
			if run == nil {
				run = fixtureRun(f.t)
			}
			text, isErr := run(svc, op, region, params)
			reply(textResult(text, isErr))
		case awsUpGetTasks:
			f.record(call)
			var a struct {
				TaskIDs []string `json:"task_ids"`
				Poll    int      `json:"poll_iteration"`
			}
			_ = json.Unmarshal(p.Arguments, &a)
			f.mu.Lock()
			gt := f.getTasks
			f.mu.Unlock()
			// Like the real server, an unknown task comes back failed under
			// the id that was asked for.
			text := strings.ReplaceAll(string(readFixture(f.t, "aws_get_tasks_not_found.json")), "00000000-0000-0000-0000-000000000000", a.TaskIDs[0])
			if gt != nil {
				text = gt(a.Poll, a.TaskIDs)
			}
			reply(textResult(text, false))
		default:
			f.record(call)
			f.mu.Lock()
			kn := f.knowledge
			f.mu.Unlock()
			text, isErr := `{"content":{"result":[]}}`, false
			if kn != nil {
				text, isErr = kn(p.Name, p.Arguments)
			}
			reply(textResult(text, isErr))
		}
	default:
		rpcErr(http.StatusOK, -32601, "method not found")
	}
}

// with changes the fake's behaviour under its lock: handler goroutines read
// the same fields.
func (f *fakeAWSMCP) with(fn func(f *fakeAWSMCP)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeAWSMCP) initCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inits
}

func (f *fakeAWSMCP) deleted() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.deletes...)
}

func (f *fakeAWSMCP) record(c fakeAWSCall) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c)
}

func (f *fakeAWSMCP) snapshot() []fakeAWSCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeAWSCall(nil), f.calls...)
}

func textResult(text string, isError bool) map[string]any {
	res := map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": isError}
	var obj map[string]any
	if json.Unmarshal([]byte(text), &obj) == nil {
		res["structuredContent"] = obj
	}
	return res
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return []byte(strings.TrimSpace(string(b)))
}

// fixtureRun answers the three live-verified reads from the fixtures and
// any other allowed read with an empty success in the same shape.
func fixtureRun(t *testing.T) func(svc, op, region, params string) (string, bool) {
	return func(svc, op, region, params string) (string, bool) {
		switch svc + "." + op {
		case "sts.GetCallerIdentity":
			return string(readFixture(t, "aws_run_script_sts.json")), false
		case "ec2.DescribeRegions":
			return string(readFixture(t, "aws_run_script_describe_regions.json")), false
		case "rds.DescribeDBInstances":
			return string(readFixture(t, "aws_run_script_rds.json")), false
		}
		return fmt.Sprintf(`{"status":"success","return_value":{},"api_calls":[{"service":%q,"operation":%q,"status":"success"}],"stdout":"","stderr":null,"warning":null}`, svc, op), false
	}
}

type awsTestRig struct {
	fake    *fakeAWSMCP
	binder  *awsBinder
	assumed *sync.Map // consumer session name -> policy
}

func newAWSRig(t *testing.T) *awsTestRig {
	t.Helper()
	fake, srv := newFakeAWSMCP(t)
	u, _ := url.Parse(srv.URL)
	rig := &awsTestRig{fake: fake, assumed: &sync.Map{}}
	rig.binder = &awsBinder{
		endpoint:       func(hostRegion string) string { return srv.URL + "/mcp" },
		allowHostPorts: []string{u.Host},
		loadConfig: func(ctx context.Context, region, profile string) (aws.Config, error) {
			return aws.Config{Region: region, Credentials: staticAWSCreds("AKIDBASE")}, nil
		},
		assumeRole: func(base aws.Config, roleARN, externalID, policy, sessionName string) (aws.CredentialsProvider, error) {
			rig.assumed.Store(sessionName, [3]string{roleARN, externalID, policy})
			return staticAWSCreds("ASIA" + strings.ToUpper(strings.TrimPrefix(sessionName, "kubebolt-"))), nil
		},
		pollWait: time.Millisecond,
	}
	return rig
}

func staticAWSCreds(keyID string) aws.CredentialsProvider {
	return aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: keyID, SecretAccessKey: "test-secret", SessionToken: "test-token", Source: "test"}, nil
	})
}

const testExternalID = "ext-9f8e7d6c5b4a3210"

func (rig *awsTestRig) bind(t *testing.T, mode string) *awsBinding {
	t.Helper()
	r := Resolved{Org: "org-1", Mode: mode, Fields: map[string]string{"region": "us-east-2"}}
	if mode == awsModeAssumeRole {
		r.Fields["roleArn"] = "arn:aws:iam::123456789012:role/KubeBoltReadOnly"
		r.Fields["externalId"] = testExternalID
	}
	b, err := rig.binder.Bind(context.Background(), r, Env{AmbientAllowed: true, CallTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	t.Cleanup(func() { _ = b.Close(context.Background()) })
	return b.(*awsBinding)
}

func callArgs(service, operation string, params string, region string) json.RawMessage {
	m := map[string]any{"service": service, "operation": operation}
	if params != "" {
		m["params"] = json.RawMessage(params)
	}
	if region != "" {
		m["region"] = region
	}
	b, _ := json.Marshal(m)
	return b
}

func TestAWSBindingTypedCall(t *testing.T) {
	rig := newAWSRig(t)
	b := rig.bind(t, awsModeAmbient)
	ctx := context.Background()

	res, err := b.Call(ctx, ConsumerKobi, awsToolCall, callArgs("rds", "DescribeDBInstances", `{ "MaxRecords" : 20 }`, ""))
	if err != nil || res.IsError {
		t.Fatalf("call: %v %+v", err, res)
	}
	var fixture struct {
		ReturnValue json.RawMessage `json:"return_value"`
	}
	_ = json.Unmarshal(readFixture(t, "aws_run_script_rds.json"), &fixture)
	if res.Text != awsCompactValue(fixture.ReturnValue) {
		t.Fatalf("text = %s", res.Text)
	}
	wantAudit := map[string]any{"service": "rds", "operation": "DescribeDBInstances", "region": "us-east-2", "hostRegion": "us-east-1", "apiCalls": 1}
	if fmt.Sprint(res.Audit) != fmt.Sprint(wantAudit) {
		t.Fatalf("audit = %v", res.Audit)
	}
	calls := rig.fake.snapshot()
	if len(calls) != 1 {
		t.Fatalf("upstream calls = %d", len(calls))
	}
	c := calls[0]
	if c.Tool != awsUpRunScript || c.Session == "" || c.Region != "us-east-2" || !strings.Contains(c.Signature, "Credential=AKIDBASE/") {
		t.Fatalf("upstream call = %+v", c)
	}
	if c.Code != [4]string{"rds", "DescribeDBInstances", "us-east-2", `{"MaxRecords":20}`} {
		t.Fatalf("script carried %v", c.Code)
	}

	// Same session for the next call; the region override moves only
	// region_name, never the connector's _meta region.
	res, err = b.Call(ctx, ConsumerKobi, awsToolCall, callArgs("ec2", "DescribeRegions", "", "eu-west-1"))
	if err != nil || res.IsError {
		t.Fatalf("second call: %v %+v", err, res)
	}
	calls = rig.fake.snapshot()
	if calls[1].Session != c.Session || calls[1].Code[2] != "eu-west-1" || calls[1].Region != "us-east-2" || calls[1].Code[3] != "{}" {
		t.Fatalf("second upstream call = %+v", calls[1])
	}
	if rig.fake.initCount() != 1 {
		t.Fatalf("%d initializations for one consumer", rig.fake.inits)
	}
}

func TestAWSBindingRefusesBeforeContactingAWS(t *testing.T) {
	rig := newAWSRig(t)
	b := rig.bind(t, awsModeAmbient)
	ctx := context.Background()
	cases := []struct {
		name     string
		consumer Consumer
		tool     string
		args     json.RawMessage
	}{
		{"write", ConsumerKobi, awsToolCall, callArgs("rds", "DeleteDBInstance", `{"DBInstanceIdentifier":"orders-db"}`, "")},
		{"secret value", ConsumerKobi, awsToolCall, callArgs("secretsmanager", "GetSecretValue", `{"SecretId":"x"}`, "")},
		{"bad region", ConsumerKobi, awsToolCall, callArgs("rds", "DescribeDBInstances", "", "us-east-1\"), x=(\"")},
		{"region hostname", ConsumerKobi, awsToolCall, callArgs("rds", "DescribeDBInstances", "", "evil.example.com")},
		{"params array", ConsumerKobi, awsToolCall, callArgs("rds", "DescribeDBInstances", `[1]`, "")},
		{"params too big", ConsumerKobi, awsToolCall, callArgs("rds", "DescribeDBInstances", `{"a":"`+strings.Repeat("x", awsMaxParamsBytes)+`"}`, "")},
		{"unknown argument", ConsumerKobi, awsToolCall, json.RawMessage(`{"service":"rds","operation":"DescribeDBInstances","code":"import os"}`)},
		{"not json", ConsumerKobi, awsToolCall, json.RawMessage(`{"service":`)},
		{"kobi-only service for autopilot", ConsumerAutopilot, awsToolCall, callArgs("ce", "GetCostAndUsage", `{}`, "")},
		{"kobi-only op for autopilot", ConsumerAutopilot, awsToolCall, callArgs("iam", "SimulatePrincipalPolicy", `{}`, "")},
		{"s3 for autopilot", ConsumerAutopilot, awsToolCall, callArgs("s3", "ListBuckets", `{}`, "")},
		{"docs for autopilot", ConsumerAutopilot, awsToolSearchDocs, json.RawMessage(`{"search_phrase":"rds"}`)},
		{"regions for autopilot", ConsumerAutopilot, awsToolListRegions, json.RawMessage(`{}`)},
		{"upstream name", ConsumerKobi, awsUpRunScript, json.RawMessage(`{"code":"import os"}`)},
		{"get_tasks", ConsumerKobi, "get_tasks", json.RawMessage(`{"task_ids":["x"]}`)},
		{"presigned", ConsumerKobi, "get_presigned_url", json.RawMessage(`{}`)},
		{"unknown consumer", Consumer("other"), awsToolCall, callArgs("rds", "DescribeDBInstances", "", "")},
		{"docs extra key", ConsumerKobi, awsToolSearchDocs, json.RawMessage(`{"search_phrase":"rds","region":"x"}`)},
		{"docs bad topic", ConsumerKobi, awsToolSearchDocs, json.RawMessage(`{"search_phrase":"rds","topics":["everything"]}`)},
		{"read docs http", ConsumerKobi, awsToolReadDocs, json.RawMessage(`{"requests":[{"url":"http://169.254.169.254/latest"}]}`)},
		{"read docs look-alike host", ConsumerKobi, awsToolReadDocs, json.RawMessage(`{"requests":[{"url":"https://docs.aws.amazon.com.example.net/?d=cluster-data"}]}`)},
		{"availability bad region", ConsumerKobi, awsToolAvailability, json.RawMessage(`{"resource_type":"api","regions":["../x"]}`)},
	}
	for _, c := range cases {
		res, err := b.Call(ctx, c.consumer, c.tool, c.args)
		if err != nil || !res.IsError || res.Audit["denied"] == nil {
			t.Errorf("%s: err=%v result=%+v", c.name, err, res)
		}
		// KubeBolt wrote the refusal: it goes out as KubeBolt's, not
		// inside the wrapper that marks provider data.
		if !res.Own {
			t.Errorf("%s: a refusal written by KubeBolt is not marked Own", c.name)
		}
	}
	if n := len(rig.fake.snapshot()); n != 0 {
		t.Fatalf("%d refused calls reached AWS", n)
	}
	// A malformed name is model text: the audit records that it was invalid,
	// not the text.
	res, err := b.Call(ctx, ConsumerKobi, awsToolCall, callArgs(strings.Repeat("x", 500), "Describe\nThings", "", "../../etc"))
	if err != nil || !res.IsError {
		t.Fatalf("malformed call: %v %+v", err, res)
	}
	if res.Audit["service"] != "(invalid)" || res.Audit["operation"] != "(invalid)" || res.Audit["region"] != "(invalid)" {
		t.Fatalf("audit = %v", res.Audit)
	}
	// And the Kobi side of the separation does reach AWS.
	res, err = b.Call(ctx, ConsumerKobi, awsToolCall, callArgs("ce", "GetCostAndUsage", `{}`, "us-east-1"))
	if err != nil || res.IsError {
		t.Fatalf("kobi ce: %v %+v", err, res)
	}
}

func TestAWSBindingConsumersGetTheirOwnSessionsAndPolicies(t *testing.T) {
	rig := newAWSRig(t)
	b := rig.bind(t, awsModeAssumeRole)
	ctx := context.Background()
	for _, c := range []Consumer{ConsumerKobi, ConsumerAutopilot} {
		if res, err := b.Call(ctx, c, awsToolCall, callArgs("sts", "GetCallerIdentity", "", "")); err != nil || res.IsError {
			t.Fatalf("%s: %v %+v", c, err, res)
		}
	}
	for _, c := range []Consumer{ConsumerKobi, ConsumerAutopilot} {
		v, ok := rig.assumed.Load("kubebolt-" + string(c))
		if !ok {
			t.Fatalf("%s never assumed the role", c)
		}
		got := v.([3]string)
		want, _ := awsSessionPolicy(c)
		if got[0] != "arn:aws:iam::123456789012:role/KubeBoltReadOnly" || got[1] != testExternalID || got[2] != want {
			t.Fatalf("%s assumed with %q %q and a different policy", c, got[0], got[1])
		}
	}
	calls := rig.fake.snapshot()
	if len(calls) != 2 || calls[0].Session == calls[1].Session {
		t.Fatalf("consumers share a session: %+v", calls)
	}
	if !strings.Contains(calls[0].Signature, "Credential=ASIAKOBI/") || !strings.Contains(calls[1].Signature, "Credential=ASIAAUTOPILOT/") {
		t.Fatal("a consumer signed with the other consumer's credentials")
	}
}

func TestAWSBindingPollsPendingTasksOnTheSameSession(t *testing.T) {
	rig := newAWSRig(t)
	pending := string(readFixture(t, "aws_run_script_pending_assumed.json"))
	done := string(readFixture(t, "aws_get_tasks_done_assumed.json"))
	rig.fake.with(func(f *fakeAWSMCP) {
		f.runScript = func(svc, op, region, params string) (string, bool) { return pending, false }
		f.getTasks = func(poll int, ids []string) string {
			if poll < 3 {
				return `{"tasks":[{"task_id":"` + ids[0] + `","status":"working","recommended_wait_seconds":0.01}]}`
			}
			return done
		}
	})
	b := rig.bind(t, awsModeAmbient)
	res, err := b.Call(context.Background(), ConsumerKobi, awsToolCall, callArgs("rds", "DescribeDBInstances", "", ""))
	if err != nil || res.IsError {
		t.Fatalf("call: %v %+v", err, res)
	}
	if res.Text != `{"DBInstances":[{"DBInstanceIdentifier":"orders-db","DBInstanceStatus":"rebooting"}]}` {
		t.Fatalf("text = %s", res.Text)
	}
	calls := rig.fake.snapshot()
	if len(calls) != 4 {
		t.Fatalf("%d upstream calls, want run_script + 3 polls", len(calls))
	}
	for i, c := range calls[1:] {
		var a struct {
			TaskIDs []string `json:"task_ids"`
			Poll    int      `json:"poll_iteration"`
		}
		_ = json.Unmarshal(c.Args, &a)
		if c.Tool != awsUpGetTasks || c.Session != calls[0].Session || a.Poll != i+1 || len(a.TaskIDs) != 1 || a.TaskIDs[0] != "4f9c2d1e-0000-4000-8000-000000000001" {
			t.Fatalf("poll %d = %+v %+v", i+1, c, a)
		}
	}

	// A task the server no longer knows is an error for the model, not a hang.
	rig.fake.with(func(f *fakeAWSMCP) { f.getTasks = nil })
	res, err = b.Call(context.Background(), ConsumerKobi, awsToolCall, callArgs("rds", "DescribeDBInstances", "", ""))
	if err != nil || !res.IsError || !strings.Contains(res.Text, "not found") {
		t.Fatalf("lost task: %v %+v", err, res)
	}
}

func TestAWSBindingVerifiesAPICalls(t *testing.T) {
	cases := []struct {
		name  string
		reply string
		ok    bool
	}{
		{"exact", `{"status":"success","return_value":{"DBInstances":[]},"api_calls":[{"service":"rds","operation":"DescribeDBInstances","status":"success"}]}`, true},
		{"snake case, paginated", `{"status":"success","return_value":{"DBInstances":[]},"api_calls":["rds.describe_db_instances","rds.describe_db_instances"]}`, true},
		{"another operation", `{"status":"success","return_value":{"secret":"s3cr3t"},"api_calls":[{"service":"rds","operation":"DescribeDBInstances"},{"service":"secretsmanager","operation":"GetSecretValue"}]}`, false},
		{"another service", `{"status":"success","return_value":{"x":"s3cr3t"},"api_calls":[{"service":"rds-data","operation":"DescribeDBInstances"}]}`, false},
		{"write", `{"status":"success","return_value":{"x":"s3cr3t"},"api_calls":[{"service":"rds","operation":"DeleteDBInstance"}]}`, false},
		{"none reported", `{"status":"success","return_value":{"x":"s3cr3t"},"api_calls":[]}`, false},
		{"not a list", `{"status":"success","return_value":{"x":"s3cr3t"},"api_calls":"rds.DescribeDBInstances"}`, false},
		{"unnamed entry", `{"status":"success","return_value":{"x":"s3cr3t"},"api_calls":[{"status":"success"}]}`, false},
	}
	for _, c := range cases {
		rig := newAWSRig(t)
		reply := c.reply
		rig.fake.with(func(f *fakeAWSMCP) {
			f.runScript = func(svc, op, region, params string) (string, bool) { return reply, false }
		})
		b := rig.bind(t, awsModeAmbient)
		res, err := b.Call(context.Background(), ConsumerKobi, awsToolCall, callArgs("rds", "DescribeDBInstances", "", ""))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if c.ok != !res.IsError {
			t.Errorf("%s: isError=%v text=%s", c.name, res.IsError, res.Text)
		}
		if !c.ok {
			if strings.Contains(res.Text, "s3cr3t") {
				t.Errorf("%s: the unverified value reached the model", c.name)
			}
			if d := res.Audit["detail"]; d != "api_calls mismatch" && d != "api_calls missing" {
				t.Errorf("%s: audit detail = %v", c.name, d)
			}
		}
	}
}

func TestAWSBindingScriptErrors(t *testing.T) {
	cases := map[string]string{
		`{"status":"error","return_value":null,"api_calls":[{"service":"rds","operation":"DescribeDBInstances","status":"error","error":"An error occurred (DBInstanceNotFound) when calling the DescribeDBInstances operation: DBInstance nope not found."}],"stderr":null}`: "DBInstanceNotFound",
		`{"status":"error","api_calls":[],"stderr":"Traceback (most recent call last): botocore.exceptions.ClientError: AccessDenied"}`:                                                                                                                                       "AccessDenied",
		`{"errorMessage":"Something went wrong"}`: "Something went wrong",
		`not json at all`:                         "does not recognize",
		`isError: ExpiredToken`:                   "ExpiredToken",
	}
	for reply, want := range cases {
		rig := newAWSRig(t)
		r := reply
		rig.fake.with(func(f *fakeAWSMCP) {
			f.runScript = func(svc, op, region, params string) (string, bool) {
				return r, strings.HasPrefix(r, "isError:")
			}
		})
		b := rig.bind(t, awsModeAmbient)
		res, err := b.Call(context.Background(), ConsumerKobi, awsToolCall, callArgs("rds", "DescribeDBInstances", "", ""))
		if err != nil || !res.IsError || !strings.Contains(res.Text, want) {
			t.Errorf("%s: err=%v %+v", reply, err, res)
		}
	}
}

// An AccessDenied refuses one operation, not the service. The EKS lab's
// route53 round: ListHostedZonesByName denied, ListHostedZonesByVPC allowed,
// and Kobi told the operator it had no access to Route 53.
func TestAWSBindingAccessDeniedNamesTheOtherOperations(t *testing.T) {
	denied := func(svc, op, code string) string {
		return fmt.Sprintf(`{"status":"error","return_value":null,"api_calls":[{"service":%q,"operation":%q,"status":"error","error":"An error occurred (%s) when calling the %s operation: User: arn:aws:sts::111122223333:assumed-role/r/s is not authorized to perform: %s:%s with an explicit deny in an identity-based policy"}],"stderr":null}`,
			svc, op, code, op, svc, op)
	}
	for _, c := range []struct {
		service, operation, reply string
		note                      bool
		listed, left              []string
	}{
		{"route53", "ListHostedZonesByName", denied("route53", "ListHostedZonesByName", "AccessDenied"), true, []string{"Get*, List*"}, nil},
		{"ec2", "DescribeVpcs", denied("ec2", "DescribeVpcs", "UnauthorizedOperation"), true, []string{"DescribeInstances", "DescribeSubnets"}, []string{"DescribeVpcs,"}},
		{"rds", "DescribeDBInstances", `{"status":"error","return_value":null,"api_calls":[{"service":"rds","operation":"DescribeDBInstances","status":"error","error":"An error occurred (DBInstanceNotFound) when calling the DescribeDBInstances operation: DBInstance nope not found."}],"stderr":null}`, false, nil, nil},
	} {
		rig := newAWSRig(t)
		r := c.reply
		rig.fake.with(func(f *fakeAWSMCP) {
			f.runScript = func(svc, op, region, params string) (string, bool) { return r, false }
		})
		b := rig.bind(t, awsModeAmbient)
		res, err := b.Call(context.Background(), ConsumerKobi, awsToolCall, callArgs(c.service, c.operation, "", ""))
		if err != nil || !res.IsError {
			t.Fatalf("%s.%s: err=%v %+v", c.service, c.operation, err, res)
		}
		notes := strings.Join(res.Notes, "\n")
		if got := strings.Contains(notes, "not the "+c.service+" service"); got != c.note {
			t.Fatalf("%s.%s: note=%v, want %v: %q", c.service, c.operation, got, c.note, notes)
		}
		for _, s := range c.listed {
			if !strings.Contains(notes, s) {
				t.Errorf("%s.%s: the note does not offer %q: %q", c.service, c.operation, s, notes)
			}
		}
		for _, s := range c.left {
			if strings.Contains(notes, s) {
				t.Errorf("%s.%s: the note offers the refused operation back: %q", c.service, c.operation, notes)
			}
		}
		if strings.Contains(notes, "111122223333") {
			t.Errorf("%s.%s: provider text reached the note: %q", c.service, c.operation, notes)
		}
	}
}

func TestAWSBindingTransportErrors(t *testing.T) {
	rig := newAWSRig(t)
	b := rig.bind(t, awsModeAssumeRole)
	ctx := context.Background()

	rig.fake.with(func(f *fakeAWSMCP) {
		f.status, f.statusMsg = http.StatusUnauthorized, "Authentication failed: Unable to verify your user identity"
	})
	res, err := b.Call(ctx, ConsumerKobi, awsToolCall, callArgs("rds", "DescribeDBInstances", "", ""))
	if !errors.Is(err, mcpclient.ErrUnauthorized) || res.Audit["service"] != "rds" {
		t.Fatalf("401: err=%v audit=%v", err, res.Audit)
	}
	tr, err := b.Test(ctx)
	if err != nil || tr.OK || tr.Error != "AWS rejected KubeBolt's signature." {
		t.Fatalf("test on 401 = %+v %v", tr, err)
	}

	rig.fake.with(func(f *fakeAWSMCP) { f.status, f.statusMsg = http.StatusTooManyRequests, "Rate exceeded" })
	_, err = b.Call(ctx, ConsumerKobi, awsToolCall, callArgs("rds", "DescribeDBInstances", "", ""))
	var rl *mcpclient.RateLimitError
	if !errors.Is(err, mcpclient.ErrRateLimited) || !errors.As(err, &rl) || rl.RetryAfter != 7*time.Second {
		t.Fatalf("429: %v", err)
	}
	for _, e := range []error{err} {
		if strings.Contains(e.Error(), testExternalID) {
			t.Fatal("an error carries the external ID")
		}
	}
}

func TestAWSBindingKnowledgeTools(t *testing.T) {
	rig := newAWSRig(t)
	var got sync.Map
	knowledge := func(name string, args json.RawMessage) (string, bool) {
		got.Store(name, string(args))
		switch name {
		case awsUpAvailability:
			return `{"errorMessage":"regions is required"}`, false
		case awsUpSearchDocs:
			return `{"content":{"result":[{"rank_order":1,"title":"RDS","url":"https://docs.aws.amazon.com/x","context":"text"}]}}`, false
		}
		return `{"content":{"result":[{"region_id":"us-east-1","region_long_name":"US East (N. Virginia)"}]}}`, false
	}
	rig.fake.with(func(f *fakeAWSMCP) { f.knowledge = knowledge })
	b := rig.bind(t, awsModeAmbient)
	ctx := context.Background()

	res, err := b.Call(ctx, ConsumerKobi, awsToolSearchDocs, json.RawMessage(`{"search_phrase":" rds storage-full ","limit":2,"topics":["troubleshooting"]}`))
	if err != nil || res.IsError || res.Text != `[{"rank_order":1,"title":"RDS","url":"https://docs.aws.amazon.com/x","context":"text"}]` {
		t.Fatalf("search: %v %+v", err, res)
	}
	if v, _ := got.Load(awsUpSearchDocs); v != `{"search_phrase":"rds storage-full","limit":2,"topics":["troubleshooting"]}` {
		t.Fatalf("search forwarded %v", v)
	}
	res, err = b.Call(ctx, ConsumerKobi, awsToolListRegions, nil)
	if err != nil || res.IsError || !strings.HasPrefix(res.Text, `[{"region_id":"us-east-1"`) {
		t.Fatalf("regions: %v %+v", err, res)
	}
	// A failure reported as isError:false with an errorMessage is an error.
	res, err = b.Call(ctx, ConsumerKobi, awsToolAvailability, json.RawMessage(`{"resource_type":"api","regions":["eu-south-2"],"filters":["EKS+DescribeCluster"]}`))
	if err != nil || !res.IsError || res.Text != "regions is required" {
		t.Fatalf("availability: %v %+v", err, res)
	}
	res, err = b.Call(ctx, ConsumerKobi, awsToolReadDocs, json.RawMessage(`{"requests":[{"url":"https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/x.html","max_length":5000}]}`))
	if err != nil || res.IsError {
		t.Fatalf("read: %v %+v", err, res)
	}
	if v, _ := got.Load(awsUpReadDocs); v != `{"requests":[{"url":"https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/x.html","max_length":5000}]}` {
		t.Fatalf("read forwarded %v", v)
	}
}

// read_documentation hands its URL to a fetcher KubeBolt does not control, so
// only documentation hosts pass, matched on the parsed URL.
func TestAWSDocURL(t *testing.T) {
	ok := []string{
		"https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/USER_RebootInstance.html",
		"https://docs.aws.amazon.com/eks/latest/userguide/pod-identities.html#pod-id-limits",
		"https://aws.amazon.com/premiumsupport/knowledge-center/eks-node-status-ready/",
		"https://repost.aws/knowledge-center/rds-mysql-high-cpu",
		"https://karpenter.sh/docs/concepts/nodepools/",
		"https://github.com/aws-samples/aws-cdk-examples",
	}
	for _, u := range ok {
		if !awsDocURL(u) {
			t.Errorf("%s refused", u)
		}
	}
	bad := []string{
		"http://docs.aws.amazon.com/x",
		"https://docs.aws.amazon.com.example.net/x",
		"https://docs.aws.amazon.com@example.net/x",
		"https://user:pw@docs.aws.amazon.com/x",
		"https://docs.aws.amazon.com:8443/x",
		"https://DOCS.aws.amazon.com/x",
		"https://docs.aws.amazon.com./x",
		"https://example.net/docs.aws.amazon.com/x",
		"https://evil.readthedocs.io/x",
		"https://github.com/attacker/repo",
		"https://github.com/aws-samples/../attacker/repo",
		"https://github.com/aws-samples%2F..%2Fattacker/repo",
		"https://github.com/aws-samples/%2e%2e/attacker/repo",
		`https://docs.aws.amazon.com\@example.net/x`,
		"https://docs.aws.amazon.com/x y",
		"https:docs.aws.amazon.com/x",
		"//docs.aws.amazon.com/x",
		"",
		"https://" + strings.Repeat("a", 2050),
	}
	for _, u := range bad {
		if awsDocURL(u) {
			t.Errorf("%q accepted", u)
		}
	}
}

func TestAWSBindingTest(t *testing.T) {
	rig := newAWSRig(t)
	b := rig.bind(t, awsModeAssumeRole)
	tr, err := b.Test(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !tr.OK || tr.Tools != 5 || tr.Canary == nil || !tr.Canary.OK || tr.Canary.Tool != "call" || tr.Error != "" || tr.Hint != "" || tr.Warning != "" {
		t.Fatalf("test = %+v", tr)
	}
	if tr.Identity != "arn:aws:sts::123456789012:assumed-role/KubeBoltReadOnly/kubebolt-kobi" {
		t.Fatalf("identity = %q", tr.Identity)
	}

	// Root credentials pass, with a warning the gateway keeps with the
	// verification: a Hint is only shown once, under a green result.
	rig.fake.with(func(f *fakeAWSMCP) {
		f.runScript = func(svc, op, region, params string) (string, bool) {
			return `{"status":"success","return_value":{"UserId":"123456789012","Account":"123456789012","Arn":"arn:aws:iam::123456789012:root"},"api_calls":[{"service":"sts","operation":"GetCallerIdentity","status":"success"}]}`, false
		}
	})
	tr, _ = b.Test(context.Background())
	if !tr.OK || !strings.Contains(tr.Warning, "root user") || tr.Hint != "" || tr.Identity != "arn:aws:iam::123456789012:root" {
		t.Fatalf("root test = %+v", tr)
	}

	// The server stopped offering run_script.
	rig.fake.with(func(f *fakeAWSMCP) { f.tools = []string{awsUpListRegions} })
	tr, _ = b.Test(context.Background())
	if tr.OK || tr.Error == "" {
		t.Fatalf("missing run_script = %+v", tr)
	}
}

func TestAWSBindingTestReportsAssumeRoleFailures(t *testing.T) {
	rig := newAWSRig(t)
	rig.binder.assumeRole = func(base aws.Config, roleARN, externalID, policy, sessionName string) (aws.CredentialsProvider, error) {
		return aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{}, &fakeAPIError{code: "AccessDenied", msg: "not authorized to perform sts:AssumeRole with external id " + externalID}
		}), nil
	}
	b := rig.bind(t, awsModeAssumeRole)
	tr, err := b.Test(context.Background())
	if err != nil || tr.OK || tr.Error != "KubeBolt could not assume the role (AccessDenied)." || !strings.Contains(tr.Hint, "sts:ExternalId") {
		t.Fatalf("test = %+v %v", tr, err)
	}
	_, err = b.Call(context.Background(), ConsumerKobi, awsToolCall, callArgs("sts", "GetCallerIdentity", "", ""))
	if err == nil || strings.Contains(err.Error(), testExternalID) || strings.Contains(fmt.Sprintf("%+v", tr), testExternalID) {
		t.Fatalf("the external ID leaked: %v", err)
	}
	if n := len(rig.fake.snapshot()); n != 0 {
		t.Fatalf("%d calls reached AWS without credentials", n)
	}
}

type fakeAPIError struct{ code, msg string }

func (e *fakeAPIError) Error() string     { return e.code + ": " + e.msg }
func (e *fakeAPIError) ErrorCode() string { return e.code }

func TestAWSBindingTruncatedResultIsRefused(t *testing.T) {
	rig := newAWSRig(t)
	big := `{"status":"success","return_value":{"x":"` + strings.Repeat("a", awsMaxCallBytes) + `"},"api_calls":[{"service":"rds","operation":"DescribeDBInstances"}]}`
	rig.fake.with(func(f *fakeAWSMCP) {
		f.runScript = func(svc, op, region, params string) (string, bool) { return big, false }
	})
	b := rig.bind(t, awsModeAmbient)
	res, err := b.Call(context.Background(), ConsumerKobi, awsToolCall, callArgs("rds", "DescribeDBInstances", "", ""))
	if err != nil || !res.IsError || !strings.Contains(res.Text, "Narrow the call") || res.Audit["detail"] != "truncated" {
		t.Fatalf("truncated: %v isError=%v audit=%v", err, res.IsError, res.Audit)
	}
}

func TestAWSBindingConcurrentCalls(t *testing.T) {
	rig := newAWSRig(t)
	b := rig.bind(t, awsModeAssumeRole)
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := ConsumerKobi
			if i%2 == 1 {
				c = ConsumerAutopilot
			}
			res, err := b.Call(context.Background(), c, awsToolCall, callArgs("rds", "DescribeDBInstances", `{"MaxRecords":20}`, ""))
			if err == nil && res.IsError {
				err = errors.New(res.Text)
			}
			if err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if rig.fake.initCount() != 2 {
		t.Fatalf("%d initializations for two consumers", rig.fake.initCount())
	}
}

func TestAWSBindingCloseEndsSessions(t *testing.T) {
	rig := newAWSRig(t)
	b := rig.bind(t, awsModeAmbient)
	ctx := context.Background()
	for _, c := range []Consumer{ConsumerKobi, ConsumerAutopilot} {
		if _, err := b.Call(ctx, c, awsToolCall, callArgs("sts", "GetCallerIdentity", "", "")); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if d := rig.fake.deleted(); len(d) != 2 || d[0] == "" || d[0] == d[1] {
		t.Fatalf("deletes = %v", d)
	}
	if _, err := b.Call(ctx, ConsumerKobi, awsToolCall, callArgs("sts", "GetCallerIdentity", "", "")); !errors.Is(err, errAWSBindingClosed) {
		t.Fatalf("call after close: %v", err)
	}
}

// Closing the binding while a call waits on a pending task ends the call: the
// session it polled on was just DELETEd, and polling on would open a fresh
// one that nothing closes.
func TestAWSBindingStopsPollingWhenClosed(t *testing.T) {
	rig := newAWSRig(t)
	var bound atomic.Pointer[awsBinding]
	pending := string(readFixture(t, "aws_run_script_pending_assumed.json"))
	done := string(readFixture(t, "aws_get_tasks_done_assumed.json"))
	rig.fake.with(func(f *fakeAWSMCP) {
		f.runScript = func(svc, op, region, params string) (string, bool) { return pending, false }
		f.getTasks = func(poll int, ids []string) string {
			if poll == 1 {
				_ = bound.Load().Close(context.Background())
				return `{"tasks":[{"task_id":"` + ids[0] + `","status":"working"}]}`
			}
			return done
		}
	})
	bound.Store(rig.bind(t, awsModeAmbient))
	res, err := bound.Load().Call(context.Background(), ConsumerKobi, awsToolCall, callArgs("rds", "DescribeDBInstances", "", ""))
	if !errors.Is(err, errAWSBindingClosed) {
		t.Fatalf("call across Close: err=%v result=%+v", err, res)
	}
	polls := 0
	for _, c := range rig.fake.snapshot() {
		if c.Tool == awsUpGetTasks {
			polls++
		}
	}
	if polls != 1 || rig.fake.initCount() != 1 || len(rig.fake.deleted()) != 1 {
		t.Fatalf("after Close: %d polls, %d sessions opened, %d deleted", polls, rig.fake.initCount(), len(rig.fake.deleted()))
	}
}

func TestAWSBind(t *testing.T) {
	rig := newAWSRig(t)
	ctx := context.Background()
	fields := map[string]string{"region": "us-east-2"}
	if _, err := rig.binder.Bind(ctx, Resolved{Mode: awsModeAmbient, Fields: fields}, Env{}); err == nil {
		t.Fatal("ambient bound on an install that does not allow it")
	}
	// A roleArn left over from the other mode does not break ambient.
	stale := map[string]string{"region": "us-east-2", "roleArn": "arn:aws:iam::123456789012:role/x"}
	if _, err := rig.binder.Bind(ctx, Resolved{Mode: awsModeAmbient, Fields: stale}, Env{AmbientAllowed: true}); err != nil {
		t.Fatalf("stale field: %v", err)
	}
	var fe *FieldError
	_, err := rig.binder.Bind(ctx, Resolved{Mode: awsModeAssumeRole, Fields: map[string]string{"region": "us-east-2", "roleArn": "arn:aws:iam::123456789012:role/x"}}, Env{})
	if !errors.As(err, &fe) || fe.Field != "externalId" {
		t.Fatalf("missing external id: %v", err)
	}
	_, err = rig.binder.Bind(ctx, Resolved{Mode: awsModeAssumeRole, Fields: map[string]string{"region": "us-east-2", "roleArn": "arn:aws:iam::123456789012:role/x", "externalId": "bad id"}}, Env{})
	if !errors.As(err, &fe) || fe.Field != "externalId" || strings.Contains(err.Error(), "bad id") {
		t.Fatalf("bad external id: %v", err)
	}
	// The external ID is read from the org's generated settings, never from
	// its secrets: one stored as a secret (a customer-chosen value) is not
	// used.
	_, err = rig.binder.Bind(ctx, Resolved{Mode: awsModeAssumeRole, Fields: map[string]string{"region": "us-east-2", "roleArn": "arn:aws:iam::123456789012:role/x"}, Secrets: map[string]string{"externalId": testExternalID}}, Env{})
	if !errors.As(err, &fe) || fe.Field != "externalId" {
		t.Fatalf("external id from secrets: %v", err)
	}
	if _, err := rig.binder.Bind(ctx, Resolved{Mode: "oauth", Fields: fields}, Env{}); err == nil {
		t.Fatal("unknown mode bound")
	}
	// The production endpoint is the AWS host of the chosen host region.
	if got := (&awsBinder{}).endpointFor("eu-central-1"); got != "https://aws-mcp.eu-central-1.api.aws/mcp" {
		t.Fatalf("endpoint = %s", got)
	}
	b, err := rig.binder.Bind(ctx, Resolved{Mode: awsModeAmbient, Fields: map[string]string{"region": "us-east-2", "hostRegion": "eu-central-1"}}, Env{AmbientAllowed: true})
	if err != nil {
		t.Fatal(err)
	}
	if ab := b.(*awsBinding); ab.cfg.hostRegion != "eu-central-1" {
		t.Fatalf("host region = %s", ab.cfg.hostRegion)
	}
}
