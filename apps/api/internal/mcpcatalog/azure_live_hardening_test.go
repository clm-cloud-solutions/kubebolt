package mcpcatalog

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// TestAzureLiveHardening replays the review's escapes against a real Azure
// MCP Server, aimed only at the all-zero subscription, and checks that none
// of them leaves the process, while ordinary reads of the pinned
// subscription still answer. Off unless KUBEBOLT_MCP_LIVE_AZURE=1, like
// TestAzureLiveSidecar; names and ids are never logged, only counts.
func TestAzureLiveHardening(t *testing.T) {
	if os.Getenv("KUBEBOLT_MCP_LIVE_AZURE") != "1" {
		t.Skip("set KUBEBOLT_MCP_LIVE_AZURE=1 with the Azure MCP Server running")
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
	rec := &azRecorder{}
	bd, err := azureBinder{wrapTransport: rec.wrap}.Bind(ctx,
		Resolved{Org: "live", Mode: azureModeSidecar, Fields: map[string]string{"subscriptionId": sub}},
		Env{AmbientAllowed: true, AzmcpURL: endpoint})
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	b := bd.(*azureBinding)
	defer b.Close(ctx)

	call := func(c Consumer, tool, args string) CallResult {
		t.Helper()
		res, err := b.Call(ctx, c, tool, json.RawMessage(args))
		if err != nil {
			t.Fatalf("%s %s: %v", c, tool, err)
		}
		status, items := azLiveShape(res.Text)
		t.Logf("live: %-9s %-20s %-40v isError=%-5v denied=%-5v status=%d items=%d", c, tool, res.Audit["command"], res.IsError, res.Audit["denied"] == true, status, items)
		return res
	}
	pinned := "/subscriptions/" + sub

	before := rec.toolCalls()
	for _, c := range []struct {
		consumer   Consumer
		tool, args string
	}{
		{ConsumerAutopilot, "group_resource_list", `{"resource-group":"kubebolt-probe/../../../` + azZeroSub + `/resourcegroups/kubebolt-probe"}`},
		{ConsumerAutopilot, "group_resource_list", `{"resource-group":"kubebolt-probe%2F..%2F..%2F..%2F` + azZeroSub + `"}`},
		{ConsumerAutopilot, "resourcehealth", `{"command":"resourcehealth_availability-status_get","parameters":{"resourceId":"` + pinned + `/resourceGroups/../../` + azZeroSub + `/resourceGroups/kubebolt-probe/providers/Microsoft.Storage/storageAccounts/kubebolt-probe"}}`},
		{ConsumerKobi, "role", `{"command":"role_assignment_list","parameters":{"scope":"` + pinned + `/../` + azZeroSub + `"}}`},
		{ConsumerAutopilot, "monitor", `{"command":"monitor_workspace_log_query","parameters":{"workspace":"kubebolt-probe","table":"AzureActivity","query":"workspace(\"` + azZeroSub + `\").AzureActivity | take 1"}}`},
		{ConsumerAutopilot, "monitor", `{"command":"monitor_resource_log_query","parameters":{"resource-id":"` + pinned + `/resourceGroups/kubebolt-probe","table":"AzureActivity","query":"arg(\"\").Resources | count"}}`},
		{ConsumerAutopilot, "servicebus", `{"command":"servicebus_queue_details","parameters":{"namespace":"kubebolt-probe.invalid","queue":"q"}}`},
		// The second review's escapes: an object the sidecar renders as its
		// raw JSON, and lists it joins into one query or table.
		{ConsumerAutopilot, "group_resource_list", `{"resource-group":{"kubebolt-probe/../../../` + azZeroSub + `/resourcegroups/kubebolt-probe":1}}`},
		{ConsumerAutopilot, "resourcehealth", `{"command":"resourcehealth_availability-status_get","parameters":{"resource-group":{"kubebolt-probe/../../../` + azZeroSub + `/resourceGroups/kubebolt-probe":1}}}`},
		{ConsumerAutopilot, "monitor", `{"command":"monitor_workspace_log_query","parameters":{"workspace":"kubebolt-probe","table":"AzureActivity","query":["workspace","(\"` + azZeroSub + `\").AzureActivity | take 1"]}}`},
		{ConsumerAutopilot, "monitor", `{"command":"monitor_resource_log_query","parameters":{"resource-id":"` + pinned + `/resourceGroups/kubebolt-probe","table":"AzureActivity","query":["arg","(\"\").Resources | count"]}}`},
		{ConsumerAutopilot, "monitor", `{"command":"monitor_workspace_log_query","parameters":{"workspace":"kubebolt-probe","table":["workspace","(\"` + azZeroSub + `\").AzureActivity"],"query":"recent"}}`},
		{ConsumerKobi, "monitor", `{"command":"monitor_workspace_log_query","parameters":{"workspace":"kubebolt-probe","table":"AzureActivity","query":{"workspace(\"` + azZeroSub + `\").AzureActivity | take 1":1}}}`},
		// A single string for a list is checked as the list's item, and a
		// number or boolean written as a string goes out only when it is
		// exactly one.
		{ConsumerKobi, "advisor", `{"command":"advisor_recommendation_list","parameters":{"tracking-ids":"kubebolt-probe/../../` + azZeroSub + `"}}`},
		{ConsumerKobi, "advisor", `{"command":"advisor_recommendation_list","parameters":{"tracking-ids":".."}}`},
		{ConsumerKobi, "advisor", `{"command":"advisor_recommendation_list","parameters":{"tracking-ids":"-q"}}`},
		{ConsumerKobi, "advisor", `{"command":"advisor_recommendation_list","parameters":{"top":" 2"}}`},
		{ConsumerKobi, "advisor", `{"command":"advisor_recommendation_list","parameters":{"top":"1e9"}}`},
		{ConsumerAutopilot, "monitor", `{"command":"monitor_activitylog_list","parameters":{"resource-name":"kubebolt-probe","hours":"0x10"}}`},
	} {
		if res := call(c.consumer, c.tool, c.args); !res.IsError || res.Audit["denied"] != true {
			t.Errorf("%s %s not refused", c.consumer, c.tool)
		}
	}
	sent := rec.toolCalls()
	if sent != before {
		t.Fatalf("refused calls reached the sidecar: %d tools/call before, %d after", before, sent)
	}

	// Ordinary reads of the pinned subscription still go out and answer.
	groups := call(ConsumerKobi, "group_list", `{}`)
	rg := azLiveFirstGroup(groups.Text)
	if groups.IsError || rg == "" {
		t.Fatal("group_list returned no resource group to read")
	}
	res := call(ConsumerAutopilot, "group_resource_list", `{"resource-group":`+string(azureJSONString(rg))+`}`)
	if res.IsError {
		t.Fatal("group_resource_list of a real group failed")
	}
	var body any
	_ = json.Unmarshal([]byte(res.Text), &body)
	id := azLiveFirstID(body)
	if !azureIDInSubscription(id, sub) {
		t.Fatal("group_resource_list answered no resource id in the subscription")
	}
	if res := call(ConsumerAutopilot, "resourcehealth", `{"command":"resourcehealth_availability-status_get","parameters":{"resourceId":`+string(azureJSONString(id))+`}}`); res.Audit["denied"] == true {
		t.Error("a real resource id of the subscription was refused")
	}
	if res := call(ConsumerAutopilot, "monitor", `{"command":"monitor_activitylog_list","parameters":{"resource-group":`+string(azureJSONString(rg))+`,"resource-name":`+string(azureJSONString(id[strings.LastIndexByte(id, '/')+1:]))+`,"hours":1,"top":1}}`); res.Audit["denied"] == true {
		t.Error("an activity log read of a real resource was refused")
	}
	// Ids and scopes written with one trailing slash, and Advisor's filter
	// given the tail of an id, are ordinary reads too.
	if res := call(ConsumerAutopilot, "resourcehealth", `{"command":"resourcehealth_availability-status_get","parameters":{"resourceId":`+string(azureJSONString(id+"/"))+`}}`); res.Audit["denied"] == true {
		t.Error("a real resource id with a trailing slash was refused")
	}
	if res := call(ConsumerKobi, "role", `{"command":"role_assignment_list","parameters":{"scope":`+string(azureJSONString(pinned+"/"))+`}}`); res.IsError {
		t.Error("role assignments at the subscription, written with a trailing slash, failed")
	}
	if res := call(ConsumerKobi, "advisor", `{"command":"advisor_recommendation_list","parameters":{"resource":`+string(azureJSONString("resourceGroups/"+rg))+`,"top":1}}`); res.IsError {
		t.Error("Advisor recommendations filtered by a resource group path failed")
	}
	if res := call(ConsumerKobi, "advisor", `{"command":"advisor_recommendation_list","parameters":{"tracking-ids":["QNY1-HB8"],"top":null}}`); res.IsError {
		t.Error("Advisor recommendations filtered by tracking id failed")
	}
	// Numbers and a single tracking id written as strings, the way the
	// sidecar takes them, go out as the declared types: top caps the answer.
	top := call(ConsumerKobi, "advisor", `{"command":"advisor_recommendation_list","parameters":{"top":"2"}}`)
	if _, items := azLiveShape(top.Text); top.IsError || items > 2 {
		t.Errorf("Advisor recommendations with top written as a string: isError=%v items=%d", top.IsError, items)
	}
	if res := call(ConsumerKobi, "advisor", `{"command":"advisor_recommendation_list","parameters":{"tracking-ids":"QNY1-HB8","top":"1"}}`); res.IsError {
		t.Error("Advisor recommendations filtered by one tracking id written as a string failed")
	}
	if res := call(ConsumerAutopilot, "monitor", `{"command":"monitor_activitylog_list","parameters":{"resource-group":`+string(azureJSONString(rg))+`,"resource-name":`+string(azureJSONString(id[strings.LastIndexByte(id, '/')+1:]))+`,"hours":"1","top":"1"}}`); res.Audit["denied"] == true {
		t.Error("an activity log read with hours and top written as strings was refused")
	}
	// Plain KQL, slashes, percent signs and a comment included, still runs.
	var ws struct {
		Results struct {
			Workspaces []struct {
				Name string `json:"name"`
			} `json:"workspaces"`
		} `json:"results"`
	}
	_ = json.Unmarshal([]byte(call(ConsumerKobi, "monitor", `{"command":"monitor_workspace_list","parameters":{}}`).Text), &ws)
	if len(ws.Results.Workspaces) > 0 {
		args, _ := json.Marshal(map[string]any{"command": "monitor_workspace_log_query", "parameters": map[string]any{
			"workspace": ws.Results.Workspaces[0].Name, "table": "AzureActivity", "hours": 1, "limit": 1,
			"query": "AzureActivity // what changed\n| where OperationNameValue has \"/write\" or ActivityStatusValue has \"50%\"\n| summarize count()",
		}})
		if res := call(ConsumerAutopilot, "monitor", string(args)); res.IsError {
			t.Error("a plain KQL query on the subscription's workspace failed")
		}
	}
	if rec.toolCalls() == sent {
		t.Fatal("no ordinary read reached the sidecar")
	}
}

// azLiveFirstID finds the first "id" in an answer that is an ARM id.
func azLiveFirstID(x any) string {
	switch v := x.(type) {
	case map[string]any:
		if id, ok := v["id"].(string); ok && strings.HasPrefix(strings.ToLower(id), "/subscriptions/") {
			return id
		}
		for _, k := range azureSortedKeys(v) {
			if id := azLiveFirstID(v[k]); id != "" {
				return id
			}
		}
	case []any:
		for _, e := range v {
			if id := azLiveFirstID(e); id != "" {
				return id
			}
		}
	}
	return ""
}
