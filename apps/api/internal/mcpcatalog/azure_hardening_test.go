package mcpcatalog

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// azZeroSub is the all-zero subscription the review aimed its live escapes
// at: ARM answered SubscriptionNotFound for it, so the request had left the
// pinned subscription.
const azZeroSub = "00000000-0000-0000-0000-000000000000"

// azRefused runs each call and requires KubeBolt to refuse it before the
// sidecar sees anything, with a message that names the parameter and does
// not echo the value.
func azRefused(t *testing.T, f *fakeAzmcp, b *azureBinding, cases []azCase) {
	t.Helper()
	before := len(f.recorded())
	for _, c := range cases {
		res := azCall(t, b, c.consumer, c.tool, c.args)
		if !res.IsError || res.Audit["denied"] != true {
			t.Errorf("%s %s %s: not refused: %+v", c.consumer, c.tool, c.args, res)
			continue
		}
		if strings.Contains(res.Text, azZeroSub) || strings.Contains(res.Text, "..%2F") {
			t.Errorf("%s: refusal echoes the value: %s", c.args, res.Text)
		}
		if c.why != "" && !strings.Contains(res.Text, c.why) {
			t.Errorf("%s: refusal %q does not say %q", c.args, res.Text, c.why)
		}
	}
	if n := len(f.recorded()) - before; n != 0 {
		t.Fatalf("%d refused calls reached the sidecar: %+v", n, f.recorded()[before:])
	}
}

type azCase struct {
	consumer   Consumer
	tool, args string
	why        string // a fragment the refusal must carry
}

// azArgs builds a router call.
func azArgs(t *testing.T, command string, params map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"command": command, "parameters": params})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func azDirect(t *testing.T, args map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// The sidecar splices parameter values into ARM URL paths, and ARM decodes
// and normalizes them: the review read the zero subscription through a
// resource group and through a resource id KubeBolt had already checked.
// Every one of those values, and their encoded and backslashed variants,
// must be refused before it leaves the process.
func TestAzureValuesCannotWalkOutOfTheSubscription(t *testing.T) {
	f := newFakeAzmcp(t)
	b := bindAzure(t, f, nil)
	pinned := "/subscriptions/" + azTestSub
	azRefused(t, f, b, []azCase{
		// The three live vectors.
		{ConsumerAutopilot, "group_resource_list", azDirect(t, map[string]any{"resource-group": "x/../kubebolt-free-rg"}), `"resource-group" cannot contain '/'`},
		{ConsumerAutopilot, "group_resource_list", azDirect(t, map[string]any{"resource-group": "x/../../../" + azZeroSub + "/resourcegroups/rg1"}), `"resource-group"`},
		{ConsumerAutopilot, "resourcehealth", azArgs(t, "resourcehealth_availability-status_get", map[string]any{
			"resourceId": pinned + "/resourceGroups/../../" + azZeroSub + "/resourceGroups/x/providers/Microsoft.Storage/storageAccounts/y"}), `"resourceId" must be a resource name or a full resource id in subscription ` + azTestSub},
		// Tenant level, through the pinned prefix.
		{ConsumerKobi, "role", azArgs(t, "role_assignment_list", map[string]any{"scope": pinned + "/../../providers/Microsoft.Management/managementGroups/root"}), `"scope"`},
		{ConsumerKobi, "role", azArgs(t, "role_assignment_list", map[string]any{"scope": pinned + "/../" + azZeroSub}), `"scope"`},
		{ConsumerKobi, "monitor", azArgs(t, "monitor_resource_log_query", map[string]any{
			"resource-id": pinned + "/./../" + azZeroSub + "/resourceGroups/rg", "query": "recent", "table": "AzureDiagnostics"}), `"resource-id"`},
		// A resource id with an empty segment, two trailing slashes, or no
		// leading slash is not an id KubeBolt vouches for.
		{ConsumerKobi, "resourcehealth", azArgs(t, "resourcehealth_availability-status_get", map[string]any{"resourceId": pinned + "//resourceGroups/rg"}), `"resourceId"`},
		{ConsumerKobi, "resourcehealth", azArgs(t, "resourcehealth_availability-status_get", map[string]any{"resourceId": pinned + "//"}), `"resourceId"`},
		{ConsumerKobi, "resourcehealth", azArgs(t, "resourcehealth_availability-status_get", map[string]any{"resourceId": "subscriptions/" + azTestSub + "/resourceGroups/../../" + azZeroSub}), `"resourceId"`},
		{ConsumerKobi, "resourcehealth", azArgs(t, "resourcehealth_availability-status_get", map[string]any{"resourceId": ".."}), `"resourceId" cannot be`},
		// One entry of a list is enough.
		{ConsumerKobi, "monitor", azArgs(t, "monitor_metrics_batchquery", map[string]any{
			"resources": "vm1, " + pinned + "/resourceGroups/rg/../../" + azZeroSub, "metric-names": "cpu", "metric-namespace": "Microsoft.Compute/virtualMachines"}), `"resources"`},
		{ConsumerKobi, "advisor", azArgs(t, "advisor_recommendation_list", map[string]any{"resource": []string{"vm1", pinned + "/.."}}), `"resource"`},
		// The same walk through the other parts the sidecar builds an id
		// from: the resource type and the resource name.
		{ConsumerAutopilot, "monitor", azArgs(t, "monitor_metrics_query", map[string]any{
			"resource-group": "rg", "resource-type": "Microsoft.Storage/../../../../" + azZeroSub, "resource": "x", "metric-names": "Transactions", "metric-namespace": "Microsoft.Storage/storageAccounts"}), `"resource-type" cannot have an empty, "." or ".." part`},
		{ConsumerAutopilot, "monitor", azArgs(t, "monitor_metrics_definitions", map[string]any{
			"resource-group": "rg", "resource-type": "Microsoft.Storage//storageAccounts", "resource": "x"}), `"resource-type"`},
		{ConsumerAutopilot, "monitor", azArgs(t, "monitor_metrics_definitions", map[string]any{
			"resource-group": "rg", "metric-namespace": "Microsoft.Sql/../..", "resource": "x"}), `"metric-namespace"`},
		// Encodings ARM or the sidecar would undo, and URL delimiters.
		{ConsumerKobi, "group_resource_list", azDirect(t, map[string]any{"resource-group": "x%2F..%2F..%2F..%2F" + azZeroSub}), `"resource-group" cannot contain '%'`},
		{ConsumerKobi, "group_resource_list", azDirect(t, map[string]any{"resource-group": `x\..\..\..\` + azZeroSub}), `"resource-group" cannot contain '\'`},
		{ConsumerKobi, "group_resource_list", azDirect(t, map[string]any{"resource-group": "rg?api-version=2015-01-01"}), `cannot contain '?'`},
		{ConsumerKobi, "group_resource_list", azDirect(t, map[string]any{"resource-group": "rg#"}), `cannot contain '#'`},
		{ConsumerKobi, "resourcehealth", azArgs(t, "resourcehealth_availability-status_get", map[string]any{"resourceId": pinned + "/resourceGroups/rg%2F..%2F..%2F" + azZeroSub}), `"resourceId" cannot contain '%'`},
		// A name that is a step up the path on its own.
		{ConsumerKobi, "group_resource_list", azDirect(t, map[string]any{"resource-group": ".."}), `"resource-group" cannot be "." or ".."`},
		{ConsumerAutopilot, "aks", azArgs(t, "aks_nodepool_get", map[string]any{"resource-group": "rg", "cluster": " ... ", "nodepool": "np"}), `"cluster" cannot be`},
		// Array items become values of their own in the sidecar.
		{ConsumerKobi, "advisor", azArgs(t, "advisor_recommendation_list", map[string]any{"tracking-ids": []string{"QNY1-HB8", "a/../b"}}), `"tracking-ids" cannot contain '/'`},
		// Names with a slash are not names.
		{ConsumerAutopilot, "sql", azArgs(t, "sql_server_firewall-rule_list", map[string]any{"resource-group": "rg", "server": "s/../../../" + azZeroSub}), `"server" cannot contain '/'`},
		{ConsumerKobi, "keyvault", azArgs(t, "keyvault_key_get", map[string]any{"vault": "evil.example/x"}), `"vault"`},
	})
}

// The checks leave ordinary values alone: names with dots and parentheses,
// ids in the pinned subscription (a Service Bus topic subscription's id has
// a subscriptions segment of its own), resource types, Windows counter
// names, OData filters, KQL with slashes and percent signs, dates in either
// format, and a question for AppLens.
func TestAzureOrdinaryValuesStillPass(t *testing.T) {
	f := newFakeAzmcp(t)
	b := bindAzure(t, f, nil)
	pinned := "/subscriptions/" + azTestSub
	topicSub := pinned + "/resourceGroups/rg/providers/Microsoft.ServiceBus/namespaces/orders/topics/t1/subscriptions/billing"
	for _, c := range []azCase{
		{ConsumerAutopilot, "group_resource_list", azDirect(t, map[string]any{"resource-group": "rg.prod(eu)-01"}), ""},
		{ConsumerAutopilot, "resourcehealth", azArgs(t, "resourcehealth_availability-status_get", map[string]any{"resourceId": topicSub}), ""},
		{ConsumerKobi, "resourcehealth", azArgs(t, "resourcehealth_availability-status_get", map[string]any{"resourceId": " " + strings.ToUpper(pinned) + "/resourceGroups/rg/providers/Microsoft.Cache/redis/r.1"}), ""},
		{ConsumerKobi, "role", azArgs(t, "role_assignment_list", map[string]any{"scope": pinned + "/resourceGroups/rg"}), ""},
		{ConsumerAutopilot, "monitor", azArgs(t, "monitor_metrics_query", map[string]any{
			"resource-group": "rg", "resource-type": "Microsoft.Compute/virtualMachines", "resource": "vm-1",
			"metric-names":     `\Processor(_Total)\% Processor Time,Disk Read Bytes/sec`,
			"metric-namespace": "azure.vm.windows.guestmetrics", "filter": "LUN eq '0' and Name eq 'a/b?'",
			"start-time": "2026-09-24T00:00:00.000Z", "end-time": "09/24/2026 10:00", "interval": "PT5M"}), ""},
		{ConsumerAutopilot, "monitor", azArgs(t, "monitor_metrics_batchquery", map[string]any{
			"resources": "vm1, " + pinned + "/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/vm2", "metric-names": "Percentage CPU", "metric-namespace": "Microsoft.Compute/virtualMachines"}), ""},
		{ConsumerAutopilot, "monitor", azArgs(t, "monitor_workspace_log_query", map[string]any{
			"workspace": azZeroSub, "table": "ContainerLogV2",
			"query": "ContainerLogV2 // the app's own logs\n| where LogMessage has \"50%\" or LogMessage has \"GET /api/v1/orders?id=1#x\"\n| summarize arg_max(TimeGenerated, *) by PodName, app_s\n| where LogMessage has 'database connection refused'"}), ""},
		{ConsumerAutopilot, "applens", azArgs(t, "applens_resource_diagnose", map[string]any{
			"resource": "orders-api", "resource-type": "Microsoft.Web/sites", "question": "Why does the app (frontend) return 502s since 10:00?"}), ""},
		{ConsumerAutopilot, "quota", azArgs(t, "quota_usage_check", map[string]any{"region": "eastus2", "resource-types": "Microsoft.App/containerApps, Microsoft.Compute/virtualMachines"}), ""},
		{ConsumerAutopilot, "servicebus", azArgs(t, "servicebus_topic_subscription_details", map[string]any{"namespace": "orders-prod.servicebus.windows.net", "topic": "t1", "subscription-name": "billing"}), ""},
		{ConsumerKobi, "advisor", azArgs(t, "advisor_recommendation_list", map[string]any{"retirement-date": "ge:2026-03-31", "resource-type": "Microsoft.Storage/storageAccounts", "search": "TLS 1.0 / 1.1 (50%)"}), ""},
		{ConsumerKobi, "quota", azArgs(t, "quota_region_availability_list", map[string]any{"resource-types": "Microsoft.CognitiveServices/accounts", "cognitive-service-model-name": "gpt-4.1-mini", "cognitive-service-model-version": "2025-04-14"}), ""},
	} {
		before := len(f.recorded())
		if res := azCall(t, b, c.consumer, c.tool, c.args); res.IsError {
			t.Errorf("%s %s %s: refused: %s", c.consumer, c.tool, c.args, res.Text)
			continue
		}
		// A metrics query may read the metric definitions first (callMetrics).
		if azCallsBut(f.recorded()[before:], azureMetricsDefs) != 1 {
			t.Errorf("%s: did not reach the sidecar once", c.args)
		}
	}
	// What is forwarded is the value as the model wrote it.
	azCall(t, b, ConsumerAutopilot, "resourcehealth", azArgs(t, "resourcehealth_availability-status_get", map[string]any{"resourceId": topicSub}))
	if p := azParams(t, f.last(t)); p["resourceId"] != topicSub {
		t.Fatalf("forwarded %v", p)
	}
}

// KQL can name its own scope: workspace() and app() open another workspace
// or Application Insights app, resource() any resource, adx(), cluster() and
// database() an Azure Data Explorer cluster, arg() Resource Graph across
// every subscription the identity sees, externaldata a URL. The review ran
// workspace(), arg() and adx() through both log commands, as Autopilot.
func TestAzureKQLCannotNameAnotherScope(t *testing.T) {
	f := newFakeAzmcp(t)
	b := bindAzure(t, f, nil)
	queries := map[string]string{
		`workspace("` + azZeroSub + `").AzureActivity | take 1`:                             "workspace()",
		`workspace("/subscriptions/` + azZeroSub + `/resourcegroups/rg/providers/x/y/w").T`: "workspace()",
		`WORKSPACE ( "` + azZeroSub + `" ).AzureActivity`:                                   "workspace()",
		"Workspace\n\t(\"w\").T":             "workspace()",
		"workspace/* not a call */(\"w\").T": "workspace()",
		"workspace // not a call\n(\"w\").T": "workspace()",
		"workspace\u00a0\u200b(\"w\").T":     "workspace()",
		`['workspace']("w").T`:               "workspace()",
		`[ @"app" ]("w").requests`:           "app()",
		`app("my-app").requests | take 5`:    "app()",
		`resource("/subscriptions/` + azZeroSub + `/resourceGroups/rg").AzureActivity`: "resource()",
		`arg("").Resources | summarize count() by subscriptionId`:                      "arg()",
		`adx("https://help.kusto.windows.net/Samples").StormEvents | take 1`:           "adx()",
		`cluster('help').database('Samples').StormEvents`:                              "cluster()",
		`AzureActivity | union database("other").T`:                                    "database()",
		`AzureActivity | union (workspace("w").AzureActivity)`:                         "workspace()",
		`externaldata (line:string) [h@"https://example.com/x.txt"]`:                   "externaldata()",
		`let f = () { app("x").requests }; f()`:                                        "app()",
	}
	var cases []azCase
	for q, fn := range queries {
		for _, consumer := range []Consumer{ConsumerKobi, ConsumerAutopilot} {
			cases = append(cases,
				azCase{consumer, "monitor", azArgs(t, "monitor_workspace_log_query", map[string]any{"workspace": "w", "table": "AzureActivity", "query": q}), "cannot use " + fn},
				azCase{consumer, "monitor", azArgs(t, "monitor_resource_log_query", map[string]any{
					"resource-id": "/subscriptions/" + azTestSub + "/resourceGroups/rg/providers/Microsoft.OperationalInsights/workspaces/w", "table": "AzureActivity", "query": q}), "cannot use " + fn},
			)
		}
	}
	// The predefined queries ("recent", "errors") put the table in front of
	// the pipe, so the table is KQL too.
	cases = append(cases, azCase{ConsumerAutopilot, "monitor", azArgs(t, "monitor_workspace_log_query", map[string]any{
		"workspace": "w", "table": `workspace("` + azZeroSub + `").AzureActivity`, "query": "recent"}), `The KQL in "table" cannot use workspace()`})
	azRefused(t, f, b, cases)
}

// Control characters and slash look-alikes. Kusto rejects a NEL or a C0 byte
// between a function name and its parenthesis, and ARM answers a fullwidth
// solidus with a 500 rather than a walk, but the checks must not rest on
// either: the review of the type rule sent all of these out.
func TestAzureControlsAndSlashLookalikesAreRefused(t *testing.T) {
	f := newFakeAzmcp(t)
	b := bindAzure(t, f, nil)
	pinned := "/subscriptions/" + azTestSub
	var cases []azCase
	for _, gap := range []string{"\u0085", "\u001c", "\u001f", "\u0000", "\u007f"} {
		cases = append(cases, azCase{ConsumerAutopilot, "monitor", azArgs(t, "monitor_workspace_log_query", map[string]any{
			"workspace": "w", "table": "AzureActivity", "query": "workspace" + gap + `("` + azZeroSub + `").AzureActivity`}), `"query" cannot contain control characters`})
	}
	for _, slash := range []string{"\uff0f", "\u2215", "\u2044", "\u29f8", "\uff3c"} {
		walk := "kubebolt-probe" + slash + ".." + slash + ".." + slash + azZeroSub
		cases = append(cases,
			azCase{ConsumerAutopilot, "group_resource_list", azDirect(t, map[string]any{"resource-group": walk}), `"resource-group" cannot contain a character that looks like a slash`},
			azCase{ConsumerKobi, "resourcehealth", azArgs(t, "resourcehealth_availability-status_get", map[string]any{"resourceId": pinned + "/resourceGroups/" + walk}), `"resourceId" cannot contain a character that looks like a slash`},
			azCase{ConsumerKobi, "advisor", azArgs(t, "advisor_recommendation_list", map[string]any{"tracking-ids": []string{"QNY1-HB8", walk}}), `"tracking-ids"`},
		)
	}
	cases = append(cases,
		azCase{ConsumerAutopilot, "group_resource_list", azDirect(t, map[string]any{"resource-group": "..\u0000"}), `"resource-group" cannot contain control characters`},
		azCase{ConsumerAutopilot, "group_resource_list", azDirect(t, map[string]any{"resource-group": "rg\nprod"}), `"resource-group" cannot contain a tab or a line break`},
	)
	azRefused(t, f, b, cases)

	// Free text keeps its line breaks and may quote a look-alike.
	before := len(f.recorded())
	if res := azCall(t, b, ConsumerKobi, "monitor", azArgs(t, "monitor_workspace_log_query", map[string]any{
		"workspace": "w", "table": "AzureActivity", "query": "AzureActivity\n\t| where OperationName has \"a\uff0fb\"\r\n| take 5"})); res.IsError {
		t.Fatalf("free-text KQL refused: %s", res.Text)
	}
	if len(f.recorded()) != before+1 {
		t.Fatal("free-text KQL did not reach the sidecar")
	}
}

// The Service Bus commands take the namespace as a host name the sidecar
// connects to directly, with its Azure token. Only Service Bus hosts pass,
// and a blank one is not sent at all.
func TestAzureServiceBusNamespaceMustBeServiceBus(t *testing.T) {
	f := newFakeAzmcp(t)
	b := bindAzure(t, f, nil)
	var cases []azCase
	for _, host := range []string{
		"attacker.example.com", "orders.servicebus.windows.net.attacker.example", "servicebus.windows.net",
		"x-.servicebus.windows.net", "orders.servicebus.windows.net:8443", "a.servicebus.windows.net",
	} {
		cases = append(cases, azCase{ConsumerAutopilot, "servicebus", azArgs(t, "servicebus_queue_details", map[string]any{"namespace": host, "queue": "q"}), "namespace must be a Service Bus host name"})
	}
	cases = append(cases, azCase{ConsumerKobi, "servicebus", azArgs(t, "servicebus_topic_details", map[string]any{"namespace": "attacker.example/x", "topic": "t"}), `"namespace" cannot contain '/'`})
	azRefused(t, f, b, cases)

	for _, host := range []string{"", "  "} {
		azCall(t, b, ConsumerAutopilot, "servicebus", azArgs(t, "servicebus_queue_details", map[string]any{"namespace": host, "queue": "q"}))
		if p := azParams(t, f.last(t)); p["namespace"] != nil {
			t.Errorf("a blank namespace was sent: %v", p)
		}
	}

	for _, host := range []string{"orders-prod.servicebus.windows.net", " Orders-Prod.ServiceBus.Windows.Net", "gov-orders.servicebus.usgovcloudapi.net"} {
		if res := azCall(t, b, ConsumerAutopilot, "servicebus", azArgs(t, "servicebus_queue_details", map[string]any{"namespace": host, "queue": "q"})); res.IsError {
			t.Errorf("%s refused: %s", host, res.Text)
		}
	}
}

// The role scope check is also correct on its own, whoever calls it.
func TestAzureRoleScopeRefusesDotSegments(t *testing.T) {
	pinned := "/subscriptions/" + azTestSub
	acr := pinned + "/resourceGroups/rg/providers/Microsoft.ContainerRegistry/registries/acr1"
	for _, c := range []struct {
		consumer Consumer
		scope    string
		ok       bool
	}{
		{ConsumerKobi, pinned, true},
		{ConsumerKobi, pinned + "/resourceGroups/rg", true},
		{ConsumerAutopilot, acr, true},
		{ConsumerKobi, pinned + "/../" + azZeroSub, false},
		{ConsumerKobi, pinned + "/../../providers/Microsoft.Management/managementGroups/root", false},
		{ConsumerKobi, pinned + "/resourceGroups/rg/./..", false},
		{ConsumerKobi, pinned + "/resourceGroups/rg%2F..%2F..", false},
		{ConsumerAutopilot, pinned + "/resourceGroups/../../" + azZeroSub + "/resourceGroups/rg/providers/Microsoft.ContainerRegistry/registries/acr1", false},
		// One trailing slash is how models often write a scope; two leave
		// an empty segment, and a lone slash is no scope at all.
		{ConsumerKobi, pinned + "/", true},
		{ConsumerAutopilot, acr + "/", true},
		{ConsumerKobi, pinned + "//", false},
		{ConsumerAutopilot, acr + "//", false},
		{ConsumerKobi, "/", false},
	} {
		raw, _ := json.Marshal(c.scope)
		msg := azureCheckRoleScope(c.consumer, azTestSub, map[string]json.RawMessage{"scope": raw})
		if (msg == "") != c.ok {
			t.Errorf("%s %q: ok=%v, got %q", c.consumer, c.scope, c.ok, msg)
		}
	}
}

// The second review read past the checks through values of another JSON type
// than the parameter's. A resource group given as an object reached ARM as
// its raw JSON, keys and all, and the zero subscription answered; a query
// given as a list reached Log Analytics as its items joined, so "workspace"
// and ("<id>") passed one by one and ran workspace(), and "arg" and ("")
// ran arg() over every subscription. Every value must now be of the type
// the server declares for it, as either consumer.
func TestAzureValuesMustHaveTheDeclaredType(t *testing.T) {
	f := newFakeAzmcp(t)
	b := bindAzure(t, f, nil)
	workspaceLog := func(query, table any) string {
		return azArgs(t, "monitor_workspace_log_query", map[string]any{"workspace": "w", "table": table, "query": query})
	}
	resourceLog := func(query, table any) string {
		return azArgs(t, "monitor_resource_log_query", map[string]any{
			"resource-id": "/subscriptions/" + azTestSub + "/resourceGroups/rg/providers/Microsoft.OperationalInsights/workspaces/w", "table": table, "query": query})
	}
	splitWorkspace := []string{"workspace", `("` + azZeroSub + `").AzureActivity | take 1`}
	splitArg := []string{"arg", `("").Resources | count`}
	splitTable := []string{"workspace", `("` + azZeroSub + `").AzureActivity`}
	objectGroup := map[string]any{"x/../../../" + azZeroSub + "/resourcegroups/rg1": 1}

	var cases []azCase
	for _, c := range []Consumer{ConsumerKobi, ConsumerAutopilot} {
		cases = append(cases,
			// The live vectors.
			azCase{c, "group_resource_list", azDirect(t, map[string]any{"resource-group": objectGroup}), `"resource-group" must be a string, not a list or an object.`},
			azCase{c, "resourcehealth", azArgs(t, "resourcehealth_availability-status_get", map[string]any{"resource-group": objectGroup}), `"resource-group" must be a string, not a list or an object.`},
			azCase{c, "monitor", workspaceLog(splitWorkspace, "AzureActivity"), `"query" must be a string`},
			azCase{c, "monitor", resourceLog(splitWorkspace, "AzureActivity"), `"query" must be a string`},
			azCase{c, "monitor", workspaceLog(splitArg, "AzureActivity"), `"query" must be a string`},
			azCase{c, "monitor", resourceLog(splitArg, "AzureActivity"), `"query" must be a string`},
			azCase{c, "monitor", workspaceLog("recent", splitTable), `"table" must be a string`},
			azCase{c, "monitor", resourceLog("recent", splitTable), `"table" must be a string`},
			// Objects and lists wherever a single value goes: names, KQL,
			// resource ids, numbers.
			azCase{c, "group_resource_list", azDirect(t, map[string]any{"resource-group": []any{objectGroup}}), `"resource-group" must be a string`},
			azCase{c, "group_resource_list", azDirect(t, map[string]any{"resource-group": []string{"rg"}}), `"resource-group" must be a string`},
			azCase{c, "monitor", workspaceLog(map[string]any{`workspace("` + azZeroSub + `").AzureActivity | take 1`: 1}, "AzureActivity"), `"query" must be a string`},
			azCase{c, "resourcehealth", azArgs(t, "resourcehealth_availability-status_get", map[string]any{"resourceId": []string{"vm1"}}), `"resourceId" must be a string`},
			azCase{c, "monitor", azArgs(t, "monitor_workspace_log_query", map[string]any{"workspace": "w", "table": "T", "query": "T", "hours": "24h"}), `"hours" must be a number: a string is taken only when it is exactly a whole number`},
			azCase{c, "monitor", azArgs(t, "monitor_activitylog_list", map[string]any{"resource-name": "vm1", "hours": map[string]any{"a/../b": 1}}), `"hours" must be a number`},
		)
	}
	cases = append(cases,
		// A list parameter takes a list of its item type, each item a
		// single value.
		azCase{ConsumerKobi, "advisor", azArgs(t, "advisor_recommendation_list", map[string]any{"tracking-ids": []any{"QNY1-HB8", map[string]any{"a/../b": 1}}}), `"tracking-ids" must be a list in which each item is a string.`},
		azCase{ConsumerKobi, "advisor", azArgs(t, "advisor_recommendation_list", map[string]any{"tracking-ids": []any{[]string{"QNY1-HB8"}}}), `"tracking-ids" must be a list`},
		azCase{ConsumerKobi, "advisor", azArgs(t, "advisor_recommendation_list", map[string]any{"tracking-ids": 7}), `"tracking-ids" must be a list`},
		azCase{ConsumerKobi, "advisor", azArgs(t, "advisor_recommendation_list", map[string]any{"tracking-ids": []any{"QNY1-HB8", 7}}), `"tracking-ids" must be a list`},
		azCase{ConsumerKobi, "keyvault", azArgs(t, "keyvault_key_get", map[string]any{"vault": "kv1", "include-managed": "True"}), `"include-managed" must be true or false`},
	)
	azRefused(t, f, b, cases)

	// Values of the declared type still go out as written, and a null is a
	// parameter left unset: it is not forwarded at all.
	for _, c := range []struct {
		consumer   Consumer
		tool, args string
		want       map[string]any
	}{
		{ConsumerKobi, "advisor", azArgs(t, "advisor_recommendation_list", map[string]any{"tracking-ids": []string{"QNY1-HB8", "9G0V-_G8"}, "top": 5}),
			map[string]any{"tracking-ids": []any{"QNY1-HB8", "9G0V-_G8"}, "top": float64(5), "subscription": azTestSub}},
		{ConsumerKobi, "advisor", azArgs(t, "advisor_recommendation_list", map[string]any{"category": "Cost", "top": nil}),
			map[string]any{"category": "Cost", "subscription": azTestSub}},
		{ConsumerKobi, "keyvault", azArgs(t, "keyvault_key_get", map[string]any{"vault": "kv1", "include-managed": true}),
			map[string]any{"vault": "kv1", "include-managed": true}},
		// The activity log reads three entries for each operation asked for:
		// KubeBolt merges an operation's entries into one row.
		{ConsumerAutopilot, "monitor", azArgs(t, "monitor_activitylog_list", map[string]any{"resource-name": "vm1", "hours": 1.5, "top": 10}),
			map[string]any{"resource-name": "vm1", "hours": 1.5, "top": float64(30), "subscription": azTestSub}},
		{ConsumerAutopilot, "monitor", workspaceLog("AzureActivity | take 1", "AzureActivity"),
			map[string]any{"workspace": "w", "table": "AzureActivity", "query": "AzureActivity | take 1", "subscription": azTestSub}},
	} {
		if res := azCall(t, b, c.consumer, c.tool, c.args); res.IsError {
			t.Errorf("%s %s: refused: %s", c.consumer, c.args, res.Text)
			continue
		}
		if p := azParams(t, f.last(t)); !reflect.DeepEqual(p, c.want) {
			t.Errorf("%s: forwarded %v, want %v", c.args, p, c.want)
		}
	}

	// The schema the model reads names the item type a list takes.
	learn := azCall(t, b, ConsumerKobi, "advisor", `{"learn":true}`)
	if !strings.Contains(learn.Text, `"tracking-ids":{"description":`) || !strings.Contains(learn.Text, `"items":{"type":"string"},"type":"array"`) {
		t.Errorf("advisor learn does not say tracking-ids is a list of strings: %s", learn.Text)
	}
}

// The server renders every value as command-line text, so it takes "5" for a
// number, "true" for a boolean and one id for a list of ids, and models write
// them so: a router description gives a type only in words ("top
// (integer)"). Such a string goes out as the declared type, but only when it
// is exactly the value; and a single string for a list is a one-item list,
// which gets every check a list's item gets, the KQL one included.
func TestAzureStringsAreCoercedToTheDeclaredType(t *testing.T) {
	f := newFakeAzmcp(t)
	azRetypeLearned(t, f, "monitor", "monitor_workspace_log_query", "query", map[string]any{"type": "array", "items": map[string]any{"type": "string"}})
	azRetypeLearned(t, f, "advisor", "advisor_recommendation_list", "resource", map[string]any{"type": "array", "items": map[string]any{"type": "string"}})
	b := bindAzure(t, f, nil)
	pinned := "/subscriptions/" + azTestSub
	advisor := func(key string, v any) string {
		return azArgs(t, "advisor_recommendation_list", map[string]any{key: v})
	}
	activity := func(key string, v any) string {
		return azArgs(t, "monitor_activitylog_list", map[string]any{"resource-name": "vm1", key: v})
	}
	keyvault := func(v any) string {
		return azArgs(t, "keyvault_key_get", map[string]any{"vault": "kv1", "include-managed": v})
	}
	workspaceLog := func(key string, v any) string {
		params := map[string]any{"workspace": "w", "table": "AzureActivity", "query": "AzureActivity | take 1"}
		params[key] = v
		return azArgs(t, "monitor_workspace_log_query", params)
	}

	// What goes out is the coerced value, as JSON of the declared type.
	for _, c := range []struct {
		consumer   Consumer
		tool, args string
		key, want  string
	}{
		{ConsumerKobi, "advisor", advisor("top", "5"), "top", `5`},
		{ConsumerKobi, "advisor", advisor("top", "+007"), "top", `7`},
		{ConsumerKobi, "advisor", advisor("top", "-1"), "top", `-1`},
		// Three entries read for each operation asked for (azure_activitylog.go).
		{ConsumerAutopilot, "monitor", activity("top", "7"), "top", `21`},
		{ConsumerAutopilot, "monitor", workspaceLog("hours", "24"), "hours", `24`},
		{ConsumerAutopilot, "monitor", activity("hours", "1.5"), "hours", `1.5`},
		{ConsumerAutopilot, "monitor", activity("hours", ".5"), "hours", `0.5`},
		{ConsumerAutopilot, "monitor", activity("hours", "1e2"), "hours", `100`},
		{ConsumerKobi, "keyvault", keyvault("true"), "include-managed", `true`},
		{ConsumerKobi, "keyvault", keyvault("false"), "include-managed", `false`},
		{ConsumerKobi, "advisor", advisor("tracking-ids", "QNY1-HB8"), "tracking-ids", `["QNY1-HB8"]`},
		{ConsumerKobi, "monitor", workspaceLog("query", "AzureActivity | take 1"), "query", `["AzureActivity | take 1"]`},
		// A wrapped id goes through the id check, which drops one trailing
		// slash from each item.
		{ConsumerKobi, "advisor", advisor("resource", pinned+"/resourceGroups/rg/"), "resource", `["` + pinned + `/resourceGroups/rg"]`},
		{ConsumerKobi, "advisor", advisor("resource", "resourceGroups/rg"), "resource", `["resourceGroups/rg"]`},
		// Values already of the declared type are untouched.
		{ConsumerKobi, "advisor", advisor("top", 5), "top", `5`},
		{ConsumerKobi, "advisor", advisor("tracking-ids", []string{"QNY1-HB8"}), "tracking-ids", `["QNY1-HB8"]`},
	} {
		if res := azCall(t, b, c.consumer, c.tool, c.args); res.IsError {
			t.Errorf("%s %s: refused: %s", c.consumer, c.args, res.Text)
			continue
		}
		var got map[string]json.RawMessage
		if err := json.Unmarshal(f.last(t).Args["parameters"], &got); err != nil {
			t.Fatal(err)
		}
		if string(got[c.key]) != c.want {
			t.Errorf("%s: forwarded %s=%s, want %s", c.args, c.key, got[c.key], c.want)
		}
	}

	// A string that is not exactly the value stays refused, and a wrapped
	// string is refused wherever the same string would be as a list's item.
	var cases []azCase
	for _, v := range []string{"1e9", " 5", "5 ", "0x10", "0b101", "5.0", "", "five", "1_000", "9223372036854775808"} {
		cases = append(cases, azCase{ConsumerKobi, "advisor", advisor("top", v), `"top" must be a number: a string is taken only when it is exactly a whole number, such as "5".`})
	}
	for _, v := range []string{" 5", "0x10", "0x1p4", "Inf", "NaN", "1e400", "1_0", "1.5h", "--1", "1e"} {
		cases = append(cases, azCase{ConsumerAutopilot, "monitor", activity("hours", v), `"hours" must be a number: a string is taken only when it is exactly a number, such as "1.5".`})
	}
	for _, v := range []string{"True", "TRUE", "yes", "1", "t", " true", "false "} {
		cases = append(cases, azCase{ConsumerKobi, "keyvault", keyvault(v), `"include-managed" must be true or false: a string is taken only when it is exactly "true" or "false".`})
	}
	for _, c := range []Consumer{ConsumerKobi, ConsumerAutopilot} {
		cases = append(cases,
			azCase{c, "monitor", workspaceLog("query", `workspace("`+azZeroSub+`").AzureActivity | take 1`), "cannot use workspace()"},
			azCase{c, "monitor", workspaceLog("query", `arg("").Resources | count`), "cannot use arg()"},
			azCase{c, "monitor", workspaceLog("query", "-q"), "cannot start with '@' or '-'"},
		)
	}
	cases = append(cases,
		azCase{ConsumerKobi, "advisor", advisor("tracking-ids", "a/../b"), `"tracking-ids" cannot contain '/'`},
		azCase{ConsumerKobi, "advisor", advisor("tracking-ids", ".."), `"tracking-ids" cannot be "." or ".."`},
		azCase{ConsumerKobi, "advisor", advisor("tracking-ids", "QNY1%2F"), `"tracking-ids" cannot contain '%'`},
		azCase{ConsumerKobi, "advisor", advisor("tracking-ids", "@creds.json"), `"tracking-ids" cannot start with '@' or '-'`},
		azCase{ConsumerKobi, "advisor", advisor("resource", "/subscriptions/"+azZeroSub+"/resourceGroups/rg"), `"resource" must be a resource name or a full resource id`},
		azCase{ConsumerKobi, "advisor", advisor("resource", "subscriptions/"+azZeroSub), `"resource" must be a resource name or a full resource id`},
		azCase{ConsumerKobi, "advisor", advisor("resource", pinned+"//"), `"resource"`},
		// Objects, nested lists and lists for a single value are not coerced.
		azCase{ConsumerKobi, "advisor", advisor("top", []string{"5"}), `"top" must be a number, not a list or an object.`},
		azCase{ConsumerKobi, "advisor", advisor("top", map[string]any{"5": 1}), `"top" must be a number, not a list or an object.`},
		azCase{ConsumerKobi, "advisor", advisor("tracking-ids", []any{[]string{"QNY1-HB8"}}), `"tracking-ids" must be a list in which each item is a string.`},
		azCase{ConsumerKobi, "advisor", advisor("tracking-ids", map[string]any{"QNY1-HB8": 1}), `"tracking-ids" must be a list in which each item is a string.`},
	)
	azRefused(t, f, b, cases)
}

// A parameter the server leaves untyped takes a single string, number or
// boolean: KubeBolt cannot vouch for how the server would render anything
// else.
func TestAzureUntypedParametersTakeASingleValue(t *testing.T) {
	f := newFakeAzmcp(t)
	azRetypeLearned(t, f, "sql", "sql_server_get", "server", map[string]any{"description": "The server name."})
	b := bindAzure(t, f, nil)
	server := func(v any) string {
		return azArgs(t, "sql_server_get", map[string]any{"resource-group": "rg", "server": v})
	}
	azRefused(t, f, b, []azCase{
		{ConsumerAutopilot, "sql", server(map[string]any{"s/../../../" + azZeroSub: 1}), `"server" must be a single string, number or boolean, not a list or an object.`},
		{ConsumerAutopilot, "sql", server([]string{"s1"}), `"server" must be a single string, number or boolean`},
		{ConsumerKobi, "sql", server("s/../../" + azZeroSub), `"server" cannot contain '/'`},
	})
	for _, v := range []any{"s1", 1, true} {
		if res := azCall(t, b, ConsumerKobi, "sql", server(v)); res.IsError {
			t.Errorf("server %v refused: %s", v, res.Text)
		}
	}
}

// If a server version declares a KQL parameter as a list, the list reaches
// Kusto as one query: the cross-scope check reads the items joined, not
// only one by one.
func TestAzureKQLListIsCheckedAsOneQuery(t *testing.T) {
	f := newFakeAzmcp(t)
	azRetypeLearned(t, f, "monitor", "monitor_workspace_log_query", "query", map[string]any{"type": "array", "items": map[string]any{"type": "string"}})
	b := bindAzure(t, f, nil)
	query := func(items ...string) string {
		return azArgs(t, "monitor_workspace_log_query", map[string]any{"workspace": "w", "table": "AzureActivity", "query": items})
	}
	azRefused(t, f, b, []azCase{
		{ConsumerAutopilot, "monitor", query("workspace", `("`+azZeroSub+`").AzureActivity | take 1`), "cannot use workspace()"},
		{ConsumerKobi, "monitor", query("arg", `("").Resources | count`), "cannot use arg()"},
		{ConsumerKobi, "monitor", query("AzureActivity | union work", `space("w").T`), "cannot use workspace()"},
	})
	if res := azCall(t, b, ConsumerAutopilot, "monitor", query("AzureActivity", "| take 1")); res.IsError {
		t.Fatalf("a plain query split in two was refused: %s", res.Text)
	}
}

// One trailing slash on an id or a scope is the id without it, and the id
// without it is what goes out. Advisor's resource filter matches the
// recommendations it read by substring, so it also takes the part of an id
// after /subscriptions/<id>/, as long as it names no subscription and takes
// no step up the path.
func TestAzureIDsWithATrailingSlashAndAdvisorTails(t *testing.T) {
	f := newFakeAzmcp(t)
	b := bindAzure(t, f, nil)
	pinned := "/subscriptions/" + azTestSub
	server := pinned + "/resourceGroups/rg/providers/Microsoft.Sql/servers/s1"
	acr := pinned + "/resourceGroups/rg/providers/Microsoft.ContainerRegistry/registries/acr1"
	tail := "resourceGroups/rg/providers/Microsoft.Storage/storageAccounts/sa1"
	for _, c := range []struct {
		consumer   Consumer
		tool, args string
		key        string
		want       any
	}{
		{ConsumerKobi, "resourcehealth", azArgs(t, "resourcehealth_availability-status_get", map[string]any{"resourceId": server + "/"}), "resourceId", server},
		{ConsumerKobi, "role", azArgs(t, "role_assignment_list", map[string]any{"scope": pinned + "/"}), "scope", pinned},
		{ConsumerAutopilot, "role", azArgs(t, "role_assignment_list", map[string]any{"scope": acr + "/"}), "scope", acr},
		{ConsumerAutopilot, "monitor", azArgs(t, "monitor_metrics_batchquery", map[string]any{
			"resources": "vm1, " + server + "/", "metric-names": "cpu_percent", "metric-namespace": "Microsoft.Sql/servers"}), "resources", "vm1," + server},
		{ConsumerKobi, "resourcehealth", azArgs(t, "resourcehealth_availability-status_get", map[string]any{"resourceId": server}), "resourceId", server},
		{ConsumerKobi, "advisor", azArgs(t, "advisor_recommendation_list", map[string]any{"resource": tail}), "resource", tail},
		{ConsumerKobi, "advisor", azArgs(t, "advisor_recommendation_summary", map[string]any{"resource": "resourceGroups/rg/"}), "resource", "resourceGroups/rg"},
		{ConsumerKobi, "advisor", azArgs(t, "advisor_recommendation_list", map[string]any{"resource": server}), "resource", server},
		{ConsumerKobi, "advisor", azArgs(t, "advisor_recommendation_list", map[string]any{"resource": "sa1"}), "resource", "sa1"},
	} {
		if res := azCall(t, b, c.consumer, c.tool, c.args); res.IsError {
			t.Errorf("%s %s: refused: %s", c.consumer, c.args, res.Text)
			continue
		}
		if p := azParams(t, f.last(t)); p[c.key] != c.want {
			t.Errorf("%s: forwarded %s=%v, want %v", c.args, c.key, p[c.key], c.want)
		}
	}

	advisor := func(cmd, resource string) string {
		return azArgs(t, cmd, map[string]any{"resource": resource})
	}
	azRefused(t, f, b, []azCase{
		// Two trailing slashes, and a lone one.
		{ConsumerKobi, "resourcehealth", azArgs(t, "resourcehealth_availability-status_get", map[string]any{"resourceId": server + "//"}), `"resourceId"`},
		{ConsumerKobi, "role", azArgs(t, "role_assignment_list", map[string]any{"scope": pinned + "//"}), `"scope"`},
		{ConsumerKobi, "resourcehealth", azArgs(t, "resourcehealth_availability-status_get", map[string]any{"resourceId": "/"}), `"resourceId"`},
		// A tail is only a filter's: an id the server reads must be whole.
		{ConsumerKobi, "resourcehealth", azArgs(t, "resourcehealth_availability-status_get", map[string]any{"resourceId": tail}), `"resourceId" must be a resource name or a full resource id`},
		{ConsumerKobi, "role", azArgs(t, "role_assignment_list", map[string]any{"scope": "resourceGroups/rg"}), `"scope"`},
		// A tail that names a subscription, starts at the root, or walks.
		{ConsumerKobi, "advisor", advisor("advisor_recommendation_list", "subscriptions/"+azZeroSub+"/resourceGroups/rg"), "or the part of one after that prefix"},
		{ConsumerKobi, "advisor", advisor("advisor_recommendation_list", "SubScriptions/"+azZeroSub), `"resource"`},
		{ConsumerKobi, "advisor", advisor("advisor_recommendation_list", "/resourceGroups/rg"), `"resource"`},
		{ConsumerKobi, "advisor", advisor("advisor_recommendation_summary", "resourceGroups/../../"+azZeroSub), `"resource"`},
		{ConsumerKobi, "advisor", advisor("advisor_recommendation_list", "resourceGroups//rg"), `"resource"`},
		{ConsumerKobi, "advisor", advisor("advisor_recommendation_list", "resourceGroups/rg/.."), `"resource"`},
		{ConsumerKobi, "advisor", advisor("advisor_recommendation_list", "resourceGroups/rg%2F..%2F.."), `"resource" cannot contain '%'`},
		{ConsumerKobi, "advisor", advisor("advisor_recommendation_list", `resourceGroups\rg`), `"resource" cannot contain '\'`},
	})
}

// azRetypeLearned replaces one parameter's schema in the fake's learn answer
// for a router, as another server version might declare it.
func azRetypeLearned(t *testing.T, f *fakeAzmcp, ns, command, param string, schema map[string]any) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	var res map[string]any
	if err := json.Unmarshal(f.lrn[ns], &res); err != nil {
		t.Fatal(err)
	}
	content := res["content"].([]any)[0].(map[string]any)
	text := content["text"].(string)
	i := strings.Index(text, "\n[")
	var cmds []map[string]any
	if i < 0 || json.Unmarshal([]byte(text[i+1:]), &cmds) != nil {
		t.Fatalf("%s learn answer has no command list", ns)
	}
	found := false
	for _, c := range cmds {
		if c["command"] == command {
			c["inputSchema"].(map[string]any)["properties"].(map[string]any)[param] = schema
			found = true
		}
	}
	if !found {
		t.Fatalf("%s learn answer has no %s", ns, command)
	}
	list, _ := json.Marshal(cmds)
	content["text"] = text[:i+1] + string(list)
	f.lrn[ns], _ = json.Marshal(res)
}

// A binding that came up without some routers says what it lacks, so the
// gateway can bind it again later; a whole one lacks nothing.
func TestAzureBindingReportsWhatIsMissing(t *testing.T) {
	whole := bindAzure(t, newFakeAzmcp(t), nil)
	var bd Binding = whole
	pb, ok := bd.(PartialBinding)
	if !ok {
		t.Fatal("the Azure binding does not implement PartialBinding")
	}
	if m := pb.Missing(); len(m) != 0 {
		t.Fatalf("whole binding misses %v", m)
	}

	f := newFakeAzmcp(t, "keyvault", "subscription_list")
	f.mu.Lock()
	f.lrn["sql"] = azResultOf(t, []byte(`{"result":{"content":[{"type":"text","text":"learn failed"}],"isError":true}}`))
	f.mu.Unlock()
	partial := bindAzure(t, f, nil)
	got := Binding(partial).(PartialBinding).Missing()
	if want := []string{"keyvault", "sql", "subscription_list"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("missing = %v, want %v", got, want)
	}
	got[0] = "tampered"
	if partial.Missing()[0] == "tampered" {
		t.Fatal("Missing returned the binding's own slice")
	}
}
