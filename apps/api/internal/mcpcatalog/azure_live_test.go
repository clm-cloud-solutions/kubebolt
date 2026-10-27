package mcpcatalog

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/mcpclient"
)

// TestAzureLiveSidecar runs the binding against a real Azure MCP Server,
// started with `make -f Makefile.ee dev-azmcp` (read-only service principal).
// It is off unless KUBEBOLT_MCP_LIVE_AZURE=1. The subscription comes from
// KUBEBOLT_MCP_LIVE_AZURE_SUBSCRIPTION or, when unset, from the sidecar's own
// subscription list; ids are only ever logged masked, and result contents
// only as counts.
func TestAzureLiveSidecar(t *testing.T) {
	if os.Getenv("KUBEBOLT_MCP_LIVE_AZURE") != "1" {
		t.Skip("set KUBEBOLT_MCP_LIVE_AZURE=1 with the sidecar running (make -f Makefile.ee dev-azmcp)")
	}
	endpoint := os.Getenv("KUBEBOLT_MCP_AZMCP_URL")
	if endpoint == "" {
		endpoint = DefaultAzmcpURL
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	sub := strings.ToLower(os.Getenv("KUBEBOLT_MCP_LIVE_AZURE_SUBSCRIPTION"))
	if sub == "" {
		sub = azLiveDefaultSubscription(ctx, t, endpoint)
	}
	t.Logf("live: subscription %s", azMask(sub))

	rec := &azRecorder{}
	start := time.Now()
	bd, err := azureBinder{wrapTransport: rec.wrap}.Bind(ctx,
		Resolved{Org: "live", Mode: azureModeSidecar, Fields: map[string]string{"subscriptionId": sub}},
		Env{AmbientAllowed: true, AzmcpURL: endpoint})
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	b := bd.(*azureBinding)
	defer b.Close(ctx)
	t.Logf("live: bind (initialize + tools/list + %d learn) %dms; kobi %d tools, autopilot %d tools",
		len(azureNamespaces), time.Since(start).Milliseconds(), len(b.Tools(ConsumerKobi)), len(b.Tools(ConsumerAutopilot)))
	if len(b.missing) > 0 {
		t.Errorf("the running sidecar lacks allowlisted namespaces or commands (version drift from %s?): %v", azmcpVersion, b.missing)
	}
	kd, _ := json.Marshal(b.Tools(ConsumerKobi))
	ad, _ := json.Marshal(b.Tools(ConsumerAutopilot))
	t.Logf("live: tool definitions kobi %d B, autopilot %d B", len(kd), len(ad))

	tr, err := b.Test(ctx)
	if err != nil || !tr.OK || tr.Canary == nil || !tr.Canary.OK {
		t.Fatalf("Test() = %+v, %v", tr, err)
	}
	t.Logf("live: Test() ok=%v tools=%d canary=%s ok=%v latency=%dms identity=%t hint=%q",
		tr.OK, tr.Tools, tr.Canary.Tool, tr.Canary.OK, tr.LatencyMs, strings.Contains(tr.Identity, " · tenant "), tr.Hint)

	call := func(c Consumer, tool, args string) CallResult {
		t.Helper()
		start := time.Now()
		res, err := b.Call(ctx, c, tool, json.RawMessage(args))
		if err != nil {
			t.Fatalf("%s %s: %v", c, tool, err)
		}
		status, items := azLiveShape(res.Text)
		t.Logf("live: %-9s %-20s %-34v %6dms isError=%-5v status=%d items=%d bytes=%d",
			c, tool, res.Audit["command"], time.Since(start).Milliseconds(), res.IsError, status, items, len(res.Text))
		return res
	}

	groups := call(ConsumerKobi, "group_list", `{}`)
	if groups.IsError {
		t.Errorf("group_list failed")
	}
	if res := call(ConsumerKobi, "aks", `{"command":"aks_cluster_get","parameters":{}}`); res.IsError {
		t.Errorf("aks_cluster_get failed")
	}
	if res := call(ConsumerKobi, "monitor", `{"command":"monitor_workspace_list","parameters":{}}`); res.IsError {
		t.Errorf("monitor_workspace_list failed")
	}
	if rg := azLiveFirstGroup(groups.Text); rg != "" {
		if res := call(ConsumerAutopilot, "group_resource_list", `{"resource-group":`+string(azureJSONString(rg))+`}`); res.IsError {
			t.Errorf("group_resource_list failed")
		}
	}
	call(ConsumerAutopilot, "resourcehealth", `{"command":"resourcehealth_availability-status_get","parameters":{}}`)

	// A made-up resource group answers 404 ResourceGroupNotFound, which
	// carries the note on where a group comes from. The list is logged only
	// as whether it carried the names-only note.
	if res := call(ConsumerAutopilot, "postgres", `{"command":"postgres_server_config_get","parameters":{"server":"kubebolt-nonexistent-zz","resource-group":"kubebolt-nonexistent-zz"}}`); !res.IsError || !reflect.DeepEqual(res.Notes, []string{azureGroupNotFoundNote}) {
		t.Errorf("a made-up resource group got no group note (isError=%v)", res.IsError)
	}
	pg := call(ConsumerKobi, "postgres", `{"command":"postgres_list","parameters":{}}`)
	t.Logf("live: postgres_list carries the names-only note: %v", reflect.DeepEqual(pg.Notes, []string{azureNamesOnlyNote}))
	// The sidecar answers a blank group with 400 ArgumentNullException; it
	// is not sent, so the list is the one without a group.
	if res := call(ConsumerAutopilot, "postgres", `{"command":"postgres_list","parameters":{"resource-group":"  "}}`); res.IsError || !reflect.DeepEqual(res.Notes, pg.Notes) {
		t.Errorf("postgres_list with a blank group: isError=%v, notes %q, want %q", res.IsError, res.Notes, pg.Notes)
	}

	// Denied commands are refused by the binding: the transport must not see
	// a tools/call for them.
	before := rec.toolCalls()
	for _, c := range []struct {
		consumer   Consumer
		tool, args string
	}{
		{ConsumerKobi, "keyvault", `{"command":"keyvault_secret_get","parameters":{"vault":"kubebolt-nonexistent-zz"}}`},
		{ConsumerKobi, "sql", `{"command":"sql_db_delete","parameters":{"server":"kubebolt-nonexistent-zz","resource-group":"kubebolt-nonexistent-zz","database":"kubebolt-nonexistent-zz"}}`},
		{ConsumerAutopilot, "keyvault", `{"command":"keyvault_key_get","parameters":{"vault":"kubebolt-nonexistent-zz"}}`},
		{ConsumerKobi, "aks", `{"command":"aks_cluster_get","parameters":{"subscription":"99999999-8888-7777-6666-555555555555"}}`},
		// A value the sidecar's command-line parser would read as a file
		// or an option (the path does not exist either way).
		{ConsumerKobi, "group_resource_list", `{"resource-group":"@/nonexistent/kubebolt-probe"}`},
		{ConsumerKobi, "aks", `{"command":"aks_cluster_get","parameters":{"cluster":"--subscription=99999999-8888-7777-6666-555555555555"}}`},
	} {
		res := call(c.consumer, c.tool, c.args)
		if !res.IsError || res.Audit["denied"] != true {
			t.Errorf("%s %s not denied: %+v", c.consumer, c.args, res)
		}
	}
	if after := rec.toolCalls(); after != before {
		t.Fatalf("denied commands reached the sidecar: %d tools/call before, %d after", before, after)
	}
	t.Logf("live: denied commands reached the sidecar 0 times (tools/call seen by the transport: %d)", rec.toolCalls())
}

// azRecorder counts the tools/call requests that leave the process.
type azRecorder struct {
	mu    sync.Mutex
	calls int
}

type azRoundTripFunc func(*http.Request) (*http.Response, error)

func (f azRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func (r *azRecorder) wrap(next http.RoundTripper) http.RoundTripper {
	return azRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Body != nil {
			body, err := io.ReadAll(req.Body)
			_ = req.Body.Close()
			if err != nil {
				return nil, err
			}
			req.Body = io.NopCloser(bytes.NewReader(body))
			var m struct {
				Method string `json:"method"`
			}
			if json.Unmarshal(body, &m) == nil && m.Method == "tools/call" {
				r.mu.Lock()
				r.calls++
				r.mu.Unlock()
			}
		}
		return next.RoundTrip(req)
	})
}

func (r *azRecorder) toolCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func azLiveDefaultSubscription(ctx context.Context, t *testing.T, endpoint string) string {
	t.Helper()
	g, _, err := azureSidecarGuard(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	c, err := mcpclient.New(endpoint, mcpclient.WithGuard(g))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	res, err := c.CallTool(ctx, "subscription_list", json.RawMessage(`{}`))
	if err != nil || res.IsError {
		t.Fatalf("subscription_list: %v (isError=%v)", err, res != nil && res.IsError)
	}
	var body struct {
		Results struct {
			Subscriptions []struct {
				SubscriptionID string `json:"subscriptionId"`
				IsDefault      bool   `json:"isDefault"`
			} `json:"subscriptions"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(res.Text()), &body); err != nil || len(body.Results.Subscriptions) == 0 {
		t.Fatalf("subscription_list: no subscription visible to the sidecar's identity")
	}
	t.Logf("live: the sidecar's identity sees %d subscription(s)", len(body.Results.Subscriptions))
	for _, s := range body.Results.Subscriptions {
		if s.IsDefault {
			return strings.ToLower(s.SubscriptionID)
		}
	}
	return strings.ToLower(body.Results.Subscriptions[0].SubscriptionID)
}

// azLiveShape summarizes an answer as (status, number of items in its first
// result list) so the log carries no resource names.
func azLiveShape(text string) (status, items int) {
	var body struct {
		Status  int                        `json:"status"`
		Results map[string]json.RawMessage `json:"results"`
	}
	if json.Unmarshal([]byte(text), &body) != nil {
		return 0, -1
	}
	for _, k := range azureSortedKeys(body.Results) {
		var list []json.RawMessage
		if json.Unmarshal(body.Results[k], &list) == nil {
			return body.Status, len(list)
		}
	}
	return body.Status, 0
}

func azLiveFirstGroup(text string) string {
	var body struct {
		Results struct {
			Groups []struct {
				Name string `json:"name"`
			} `json:"groups"`
		} `json:"results"`
	}
	if json.Unmarshal([]byte(text), &body) != nil || len(body.Results.Groups) == 0 {
		return ""
	}
	return body.Results.Groups[0].Name
}

func azMask(id string) string {
	if len(id) <= 4 {
		return "…"
	}
	return "…" + id[len(id)-4:]
}

// TestAzureLiveActivityLog reads one resource's activity log through the
// binding, as KubeBolt merges it: over a day, over the 90 days the log keeps,
// and with match on one sub-resource. The resource and the match come from
// KUBEBOLT_MCP_LIVE_AZURE_ACTIVITY_RESOURCE and _MATCH; the log carries only
// counts and sizes.
func TestAzureLiveActivityLog(t *testing.T) {
	if os.Getenv("KUBEBOLT_MCP_LIVE_AZURE") != "1" {
		t.Skip("set KUBEBOLT_MCP_LIVE_AZURE=1 with the sidecar running (make -f Makefile.ee dev-azmcp)")
	}
	resource := os.Getenv("KUBEBOLT_MCP_LIVE_AZURE_ACTIVITY_RESOURCE")
	match := os.Getenv("KUBEBOLT_MCP_LIVE_AZURE_ACTIVITY_MATCH")
	if resource == "" {
		t.Skip("set KUBEBOLT_MCP_LIVE_AZURE_ACTIVITY_RESOURCE to a resource name (and _MATCH to one of its sub-resources)")
	}
	endpoint := os.Getenv("KUBEBOLT_MCP_AZMCP_URL")
	if endpoint == "" {
		endpoint = DefaultAzmcpURL
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	sub := strings.ToLower(os.Getenv("KUBEBOLT_MCP_LIVE_AZURE_SUBSCRIPTION"))
	if sub == "" {
		sub = azLiveDefaultSubscription(ctx, t, endpoint)
	}
	bd, err := azureBinder{}.Bind(ctx,
		Resolved{Org: "live", Mode: azureModeSidecar, Fields: map[string]string{"subscriptionId": sub}},
		Env{AmbientAllowed: true, AzmcpURL: endpoint})
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	b := bd.(*azureBinding)
	defer b.Close(ctx)

	read := func(params map[string]any) azureActivityAnswer {
		t.Helper()
		params["resource-name"] = resource
		args, _ := json.Marshal(map[string]any{"command": azureActivityLog, "parameters": params})
		start := time.Now()
		res, err := b.Call(ctx, ConsumerKobi, "monitor", args)
		if err != nil || res.IsError {
			t.Fatalf("%v: err=%v isError=%v", params, err, res.IsError)
		}
		var ans azureActivityAnswer
		if err := json.Unmarshal([]byte(res.Text), &ans); err != nil {
			t.Fatalf("%v: not the merged log: %v", params, err)
		}
		requests := 0
		for _, op := range ans.Operations {
			if len(op.Request) > 0 {
				requests++
			}
		}
		t.Logf("live: hours=%v top=%v match=%t %5dms entries=%v operations=%d with request=%d bytes=%d notes=%d",
			params["hours"], params["top"], params["match"] != nil, time.Since(start).Milliseconds(),
			res.Audit["entries"], len(ans.Operations), requests, len(res.Text), len(res.Notes))
		if len(res.Text) > 32<<10 {
			t.Errorf("%v: %d bytes, past what one tool call hands the model", params, len(res.Text))
		}
		return ans
	}

	read(map[string]any{"hours": 24, "top": 20})
	read(map[string]any{"hours": 2160, "top": 50})
	if match == "" {
		return
	}
	ans := read(map[string]any{"hours": 2160, "top": 5, "match": match})
	writes := 0
	for _, op := range ans.Operations {
		if len(op.Request) > 0 && strings.HasSuffix(strings.ToLower(op.Operation), "/write") {
			writes++
		}
	}
	if writes < 2 {
		t.Logf("live: %d write(s) with a request body to the matched sub-resource in 90 days: no earlier value to read", writes)
		return
	}
	newest, earlier := ans.Operations[0], ans.Operations[1]
	t.Logf("live: the earlier write is %s before the newest; requests differ: %t",
		azLiveSince(earlier.Started, newest.Started), string(newest.Request) != string(earlier.Request))
}

func azLiveSince(from, to string) string {
	a, errA := time.Parse(time.RFC3339, from)
	b, errB := time.Parse(time.RFC3339, to)
	if errA != nil || errB != nil {
		return "?"
	}
	return b.Sub(a).Round(time.Hour).String()
}
