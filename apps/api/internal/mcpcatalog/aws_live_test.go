package mcpcatalog

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/mcpclient"
)

// TestAWSLive runs the connector against the real AWS MCP Server with this
// machine's own credentials (ambient mode). It is opt-in — CI never sets
// KUBEBOLT_MCP_LIVE_AWS — and it runs exactly three reads through the typed
// path plus Binding.Test's canary (sts GetCallerIdentity): no other tool and
// no free-form script. The local default identity may be powerful, so the
// list is fixed here, not taken from the environment.
//
// Optional: KUBEBOLT_MCP_LIVE_AWS_REGION (default us-east-1),
// KUBEBOLT_MCP_LIVE_AWS_HOST_REGION (default us-east-1),
// KUBEBOLT_MCP_LIVE_AWS_PROFILE, and KUBEBOLT_MCP_LIVE_AWS_DUMP, a file that
// receives the redacted reply shapes (keys and types, never values).
func TestAWSLive(t *testing.T) {
	if os.Getenv("KUBEBOLT_MCP_LIVE_AWS") != "1" {
		t.Skip("set KUBEBOLT_MCP_LIVE_AWS=1 to run against the real AWS MCP Server")
	}
	region := awsEnvOr("KUBEBOLT_MCP_LIVE_AWS_REGION", "us-east-1")
	hostRegion := awsEnvOr("KUBEBOLT_MCP_LIVE_AWS_HOST_REGION", awsDefaultHostRegion)
	fields := map[string]string{"region": region, "hostRegion": hostRegion}
	if p := os.Getenv("KUBEBOLT_MCP_LIVE_AWS_PROFILE"); p != "" {
		fields["profile"] = p
	}

	var (
		mu       sync.Mutex
		observed []map[string]any
	)
	binder := &awsBinder{observe: func(upstream string, res *mcpclient.CallResult) {
		mu.Lock()
		defer mu.Unlock()
		observed = append(observed, map[string]any{
			"upstream":   upstream,
			"isError":    res.IsError,
			"truncated":  res.Truncated,
			"structured": len(res.StructuredContent) > 0,
			"shape":      awsLiveShape(res.Text()),
		})
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	env := Env{AmbientAllowed: true, CallTimeout: 45 * time.Second}
	r := Resolved{Org: "live", Mode: awsModeAmbient, Fields: fields}

	b, err := binder.Bind(ctx, r, env)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	defer b.Close(ctx)

	calls := []struct{ service, operation, params string }{
		{"sts", "GetCallerIdentity", `{}`},
		{"ec2", "DescribeRegions", `{}`},
		{"rds", "DescribeDBInstances", `{"MaxRecords":20}`},
		{"sts", "GetCallerIdentity", `{}`}, // warm, same session
	}
	for _, c := range calls {
		args, _ := json.Marshal(map[string]any{"service": c.service, "operation": c.operation, "params": json.RawMessage(c.params)})
		t0 := time.Now()
		res, err := b.Call(ctx, ConsumerKobi, awsToolCall, args)
		d := time.Since(t0)
		if err != nil {
			t.Fatalf("%s.%s: %v (after %v)", c.service, c.operation, awsRedact(err.Error()), d)
		}
		var parsed any
		_ = json.Unmarshal([]byte(res.Text), &parsed)
		t.Logf("%s.%s: %v isError=%v audit=%v bytes=%d shape=%v", c.service, c.operation, d.Round(time.Millisecond),
			res.IsError, res.Audit, len(res.Text), awsSkeleton(parsed, 0))
		if res.IsError {
			t.Errorf("%s.%s failed: %s", c.service, c.operation, awsRedact(res.Text))
		}
	}

	// Test on a fresh binding: the cold path an admin's first Test takes.
	b2, err := (&awsBinder{}).Bind(ctx, r, env)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	defer b2.Close(ctx)
	t0 := time.Now()
	tr, err := b2.Test(ctx)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	t.Logf("Binding.Test: %v ok=%v tools=%d latencyMs=%d identity=%s canary=%+v error=%q hint=%q warning=%q", time.Since(t0).Round(time.Millisecond),
		tr.OK, tr.Tools, tr.LatencyMs, awsRedact(tr.Identity), tr.Canary, awsRedact(tr.Error), tr.Hint, tr.Warning)
	if !tr.OK {
		t.Errorf("Binding.Test failed")
	}

	if path := os.Getenv("KUBEBOLT_MCP_LIVE_AWS_DUMP"); path != "" {
		mu.Lock()
		out, _ := json.MarshalIndent(observed, "", "  ")
		mu.Unlock()
		if err := os.WriteFile(path, []byte(awsRedact(string(out))), 0o600); err != nil {
			t.Errorf("dump: %v", err)
		}
	}
}

func awsEnvOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

var (
	awsLiveAccountRE = regexp.MustCompile(`\b[0-9]{8}([0-9]{4})\b`)
	awsLiveKeyIDRE   = regexp.MustCompile(`\b(AKIA|ASIA|AIDA|AROA)[A-Z0-9]{12,}\b`)
)

// awsRedact keeps the last four digits of an account id and hides IAM
// unique ids, so logs and dumps carry nothing that identifies the account.
func awsRedact(s string) string {
	s = awsLiveAccountRE.ReplaceAllString(s, "********$1")
	return awsLiveKeyIDRE.ReplaceAllString(s, "$1<redacted>")
}

// awsLiveShape describes a reply's structure. api_calls, status and the keys
// that carry no account data are kept; every value under return_value is
// reduced to its type.
func awsLiveShape(text string) any {
	var v any
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &v); err != nil {
		if len(text) > 300 {
			text = text[:300]
		}
		return "non-JSON: " + awsRedact(text)
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return awsSkeleton(v, 0)
	}
	out := map[string]any{}
	for k, val := range obj {
		switch k {
		case "return_value", "result", "structuredContent":
			out[k] = awsSkeleton(val, 0)
		case "tasks":
			out[k] = awsSkeletonTasks(val)
		default:
			out[k] = val
		}
	}
	return out
}

func awsSkeletonTasks(v any) any {
	list, ok := v.([]any)
	if !ok {
		return awsSkeleton(v, 0)
	}
	var out []any
	for _, t := range list {
		if m, ok := t.(map[string]any); ok {
			tm := map[string]any{}
			for k, val := range m {
				if k == "status" || k == "api_calls" {
					tm[k] = val
				} else {
					tm[k] = awsSkeleton(val, 0)
				}
			}
			out = append(out, tm)
		}
	}
	return out
}

func awsSkeleton(v any, depth int) any {
	switch x := v.(type) {
	case map[string]any:
		if depth > 3 {
			return "object"
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := map[string]any{}
		for _, k := range keys {
			out[k] = awsSkeleton(x[k], depth+1)
		}
		return out
	case []any:
		if len(x) == 0 {
			return []any{}
		}
		return []any{awsSkeleton(x[0], depth+1), "len=" + strconv.Itoa(len(x))}
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "bool"
	case nil:
		return "null"
	}
	return "?"
}
