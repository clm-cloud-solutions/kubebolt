package copilot

import (
	"strings"
	"testing"
)

// The digests count and list what came back, in the shapes the tools write
// (sampled from stored conversations); they never say what it means.
func TestDigestToolResult(t *testing.T) {
	cases := []struct {
		name, tool string
		args       map[string]any
		content    string
		want       string
	}{
		{"events with warnings", "get_events", nil,
			`{"kind":"events","items":[{"type":"Warning","reason":"Unhealthy","count":25},{"type":"Warning","reason":"BackOff","count":1},{"type":"Normal","reason":"Pulled","count":3}],"total":3}`,
			"3 events · 2 warnings: Unhealthy ×25, BackOff"},
		{"no events", "get_events", nil, `{"kind":"events","items":[],"total":0}`, "no events"},
		{"pods", "get_workload_pods", nil,
			`{"pods":[{"status":"Running","ready":"1/1","restarts":0},{"status":"CrashLoopBackOff","ready":"0/1","restarts":12},{"status":"Running","ready":"1/1","restarts":0}]}`,
			"3 pods · 1 not ready · 12 restarts · CrashLoopBackOff"},
		{"list with statuses", "list_resources", map[string]any{"type": "pods"},
			`{"kind":"pods","items":[{"status":"Running"},{"status":"Running"},{"status":"Pending"}],"total":3}`,
			"3 pods · Running ×2, Pending"},
		{"list one page of more", "list_resources", map[string]any{"type": "pods"},
			`{"kind":"pods","items":[{"status":"Running"}],"total":40}`,
			"1 of 40 pods"},
		{"list empty", "list_resources", map[string]any{"type": "ingresses"}, `{"kind":"ingresses","items":[],"total":0}`, "none"},
		{"crd not installed", "list_resources", map[string]any{"type": "certificates"},
			`{"kind":"certificates","items":[],"total":0,"notInstalled":true}`, "not installed"},
		{"logs with grep and a burst", "get_pod_logs", nil,
			`{"returnedLines":442,"originalLines":2000,"grep":"ERROR","bursts":{"items":[{"from":"2026-10-12T16:35:25Z","to":"2026-10-12T16:51:37Z","lines":442}]}}`,
			`442 of 2000 lines match "ERROR" · burst @2026-10-12T16:35:25Z–@2026-10-12T16:51:37Z`},
		{"logs with a long filter", "get_pod_logs", nil,
			`{"returnedLines":2,"originalLines":1223,"grep":"error|timeout|refused|db|database|dial|deadline|fatal|panic","bursts":{"items":[{"from":"2026-10-26T13:05:10Z","to":"2026-10-26T13:06:25Z"}]}}`,
			"2 of 1223 lines match the filter · burst @2026-10-26T13:05:10Z–@2026-10-26T13:06:25Z"},
		{"logs with AWS denials", "get_pod_logs", nil,
			`{"returnedLines":200,"originalLines":200,"awsDenials":[{"action":"s3:GetObject"},{"action":"kms:Decrypt"}]}`,
			"200 lines · 2 AWS denials"},
		{"insights", "get_insights", nil,
			`{"items":[{"severity":"critical"},{"severity":"warning"},{"severity":"warning"}]}`,
			"3 insights · 1 critical · 2 warning"},
		{"metrics", "query_metrics", nil, `{"range":"24h","series":[{"labels":{}},{"labels":{}}]}`, "2 series · 24h"},
		{"overview", "get_cluster_overview", nil,
			`{"nodes":{"total":2,"ready":2},"pods":{"total":81,"ready":68,"succeeded":13}}`,
			"nodes 2/2 ready · pods 68/68 ready"},
		{"deploys", "get_recent_deploys", nil,
			`{"deploys":[{"deployedAt":"2026-10-26T12:05:49Z","resource":"shop-backend/catalog-api"},{"deployedAt":"2026-10-26T12:05:50Z","resource":"shop-backend/orders-api"}]}`,
			"2 deploys · latest shop-backend/orders-api @2026-10-26T12:05:50Z"},
		{"yaml of a deployment", "get_resource_yaml", nil,
			`{"yaml":"apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: orders-api\nspec:\n  replicas: 3\nstatus:\n  readyReplicas: 2\n","lastWrites":[{"manager":"kubectl-rollout","operation":"Update","time":"2026-10-26T12:05:49Z"},{"manager":"kube-controller-manager","subresource":"status","time":"2026-10-26T11:00:00Z"}]}`,
			"Deployment · replicas 2/3 ready · last write kubectl-rollout @2026-10-26T12:05:49Z"},
		{"yaml of a pod written last by the kubelet", "get_resource_yaml", nil,
			`{"yaml":"kind: Pod\nstatus:\n  phase: Running\n","lastWrites":[{"manager":"kubelet","subresource":"status","time":"2026-10-26T12:06:00Z"}]}`,
			"Pod · Running · last write kubelet (status) @2026-10-26T12:06:00Z"},
		{"yaml of a daemonset", "get_resource_yaml", nil,
			`{"yaml":"kind: DaemonSet\nstatus:\n  desiredNumberScheduled: 2\n  numberReady: 1\n"}`,
			"DaemonSet · pods 1/2 ready"},
		{"yaml it cannot read", "get_resource_yaml", nil, `{"yaml":"..."}`, ""},
		{"describe of a crashing pod", "get_resource_describe", nil,
			`{"describe":"Name:             orders-api-7c9d8-x2k4p\nNamespace:        shop-backend\nStatus:           Running\nContainers:\n  api:\n    State:          Waiting\n      Reason:       CrashLoopBackOff\nEvents:\n  Type     Reason     Age                  From               Message\n  ----     ------     ----                 ----               -------\n  Normal   Scheduled  28m                  default-scheduler  Successfully assigned shop-backend/orders-api-7c9d8-x2k4p\n  Warning  BackOff    3m2s (x120 over 28m)  kubelet            Back-off restarting failed container api\n  Warning  Unhealthy  27m                  kubelet            Readiness probe failed: connection refused\n"}`,
			"status Running · 3 events · 2 warnings: BackOff ×120, Unhealthy"},
		{"describe of a deployment without events", "get_resource_describe", nil,
			`{"describe":"Name:                   catalog-api\nReplicas:               3 desired | 3 updated | 3 total | 2 available | 1 unavailable\nEvents:          <none>\n"}`,
			"replicas 2/3 available · no events"},
		{"topology", "get_topology", nil,
			`{"nodes":[{"type":"Pod"},{"type":"Pod"},{"type":"Pod"},{"type":"Service"},{"type":"Deployment"},{"type":"ReplicaSet"}],"edges":[{"id":"e1"},{"id":"e2"}]}`,
			"6 resources · 2 links · Pod ×3, Deployment, ReplicaSet, +1 more"},
		{"external connections", "get_external_connections", nil,
			`{"sourcePresent":true,"clusterUIDKnown":true,"windowMinutes":15,"destinations":[{"fqdn":"shop-db.example.us-east-2.rds.amazonaws.com","verdict":"dropped"},{"ip":"203.0.113.7","verdict":"forwarded"},{"fqdn":"sts.us-east-2.amazonaws.com","infrastructure":true}],"dependencyHosts":[{"source":"env DB_HOST","host":"shop-db.example.us-east-2.rds.amazonaws.com"},{"source":"secret db-credentials"}],"entry":[{"via":"Ingress shop/web"}]}`,
			"3 destinations: shop-db.example.us-east-2.rds.amazonaws.com, 203.0.113.7 · 1 cluster infrastructure · 1 dropped · 1 host in its configuration · 1 entry point · 15m"},
		{"external connections past the budget", "get_external_connections", nil,
			`{"sourcePresent":true,"clusterUIDKnown":true,"windowMinutes":60,"destinations":[{"fqdn":"a.example.com"},{"fqdn":"b.example.com"},{"fqdn":"c.example.com"},{"fqdn":"d.example.com"}],"omittedDestinations":3}`,
			"7 destinations: a.example.com, b.example.com, c.example.com, +4 more · 1h"},
		{"external connections without flow data", "get_external_connections", nil,
			`{"sourcePresent":false,"clusterUIDKnown":true,"windowMinutes":15,"destinations":[],"dependencyHosts":[{"source":"env DB_HOST","host":"10.0.3.12"},{"source":"env API","host":"api.example.com"}]}`,
			"no flow data · 2 hosts in its configuration"},
		{"a tool without one", "get_resource_detail", nil, `{"name":"x"}`, ""},
		{"not json", "get_events", nil, "plain text", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := digestToolResult(c.tool, c.args, c.content); got != c.want {
				t.Errorf("digest = %q\n          want %q", got, c.want)
			}
		})
	}
}

func TestDigestListsAtMostThreeNames(t *testing.T) {
	c := counter{}
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		c.add(n, 1)
	}
	if got := c.mostFirst(3); got != "a, b, c, +2 more" {
		t.Errorf("mostFirst = %q", got)
	}
}

// The digest is taken from the whole result: a result the executor caps at
// 32 KB still gets the count of everything that came back.
func TestDigestReadsTheResultBeforeTheCap(t *testing.T) {
	items := make([]string, 0, 400)
	for i := 0; i < 400; i++ {
		items = append(items, `{"type":"Warning","reason":"Unhealthy","count":1,"message":"`+strings.Repeat("x", 120)+`"}`)
	}
	content := `{"kind":"events","items":[` + strings.Join(items, ",") + `],"total":400}`
	if capped := truncateToolResult(content, "get_events"); !strings.Contains(capped, "TRUNCATED") {
		t.Fatal("the fixture is not over the cap")
	}
	if got := digestToolResult("get_events", nil, content); got != "400 events · 400 warnings: Unhealthy ×400" {
		t.Errorf("digest = %q", got)
	}
}
