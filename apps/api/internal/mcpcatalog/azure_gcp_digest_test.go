package mcpcatalog

import (
	"encoding/json"
	"testing"
)

// The generic digest over the answers Azure's sidecar and Google's servers
// send: lists counted with their items' states, a single object by its state.
func TestDigestJSON(t *testing.T) {
	cases := []struct{ name, answer, want string }{
		{"Azure servers, through the sidecar's envelope",
			`{"status":200,"message":"Success","results":{"servers":[{"name":"orders-db","state":"Ready"},{"name":"reports","state":"Stopped"}]}}`,
			"2 servers · Ready, Stopped"},
		{"Azure Resource Health of one resource",
			`{"status":200,"results":{"status":{"availabilityState":"Unavailable","summary":"..."}}}`,
			"Unavailable"},
		{"Azure AKS cluster wrapped under one field",
			`{"status":200,"results":{"cluster":{"name":"aks-shop","provisioningState":"Succeeded"}}}`,
			"Succeeded"},
		{"Azure Service Bus queues by status",
			`{"results":{"queues":[{"status":"Active"},{"status":"Active"},{"status":"Disabled"}]}}`,
			"3 queues · Active ×2, Disabled"},
		{"GCP Cloud SQL instance", `{"name":"orders","state":"RUNNABLE","databaseVersion":"POSTGRES_16"}`, "RUNNABLE"},
		{"GCP log entries", `{"entries":[{},{}],"nextPageToken":"x"}`, "2 entries"},
		{"GCP time series, one", `{"timeSeries":[{"points":[]}]}`, "1 time series"},
		{"GCP node pools", `{"nodePools":[{"status":"RUNNING"},{"status":"RECONCILING"}]}`, "2 node pools · RUNNING, RECONCILING"},
		{"nothing to count or name", `{"etag":"x"}`, ""},
		{"not JSON", `plain text`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := digestJSON(c.answer); got != c.want {
				t.Errorf("digest = %q\n          want %q", got, c.want)
			}
		})
	}
}

func TestAzureActivityDigest(t *testing.T) {
	ans := azureActivityAnswer{Operations: []*azureActivityOp{
		{Title: "Create or Update Security Rule", Status: "Succeeded", Ended: "2026-10-12T21:10:00Z"},
		{Title: "Create or Update Security Rule", Status: "Succeeded", Ended: "2026-10-12T21:20:00Z"},
		{Title: "Delete Security Rule", Status: "Succeeded", Ended: "2026-10-12T22:05:00Z"},
		{Operation: "Microsoft.Compute/virtualMachineScaleSets/restart/action", Status: "Failed", Started: "2026-10-12T22:30:00Z"},
	}}
	want := "4 operations · 1 failed · rarest first: Microsoft.Compute/virtualMachineScaleSets/restart/action @2026-10-12T22:30:00Z, " +
		"Delete Security Rule @2026-10-12T22:05:00Z, Create or Update Security Rule ×2"
	if got := azureActivityDigest(ans); got != want {
		t.Errorf("digest = %q\n          want %q", got, want)
	}
	if got := azureActivityDigest(azureActivityAnswer{}); got != "no operations" {
		t.Errorf("empty = %q", got)
	}
}

func TestAzureMetricsDigest(t *testing.T) {
	items := []json.RawMessage{
		json.RawMessage(`{"name":"cpu_percent","timeSeries":[]}`),
		json.RawMessage(`{"name":{"value":"storage_percent"}}`),
	}
	if got := azureMetricsDigest(items, 1); got != "2 metrics: cpu_percent, storage_percent · 1 failed" {
		t.Errorf("digest = %q", got)
	}
}
