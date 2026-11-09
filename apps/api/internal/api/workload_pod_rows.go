package api

// The pod list of a workload (GET /resources/{deployments,statefulsets,
// daemonsets,jobs}/{ns}/{name}/pods) carries only the fields its readers use.
// The connector's rows are podToMap plus metrics: every volume, condition,
// annotation, label and owner reference, and each container's image, ports,
// resources and mounts. That is more than half of each row, and a DaemonSet's
// list has one row per node. Readers:
//   - the workload's Pods tab: status, ready, restarts, IP, node, age, and CPU
//     and memory usage against requests and limits;
//   - the Logs and Terminal pickers: name and containers (name, ephemeral,
//     state);
//   - the rollout panel: name, status, ready, node, createdAt;
//   - the Monitor tab's node note: nodeName;
//   - Autopilot's verify_pods_ready (EE): name, status, ready, and the list as
//     the model's tool result.
//
// Kobi and the blast radius read the connector functions directly and keep the
// full rows. A field a new reader needs is added here, deliberately.
var podListRowKeys = []string{
	"name", "namespace", "status", "ready", "restarts", "nodeName", "ip", "createdAt", "hostNetwork",
	"cpuRequest", "cpuLimit", "memoryRequest", "memoryLimit",
	"cpuUsage", "memoryUsage", "cpuPercent", "memoryPercent",
}

var podListContainerKeys = []string{"name", "ephemeral", "state", "ready"}

// podListRows projects the connector's pod rows onto podListRowKeys. A nil
// list stays nil, so the response shape does not change.
func podListRows(pods []map[string]interface{}) []map[string]interface{} {
	if pods == nil {
		return nil
	}
	rows := make([]map[string]interface{}, 0, len(pods))
	for _, p := range pods {
		row := make(map[string]interface{}, len(podListRowKeys)+1)
		for _, k := range podListRowKeys {
			if v, ok := p[k]; ok {
				row[k] = v
			}
		}
		if cs, ok := p["containers"].([]map[string]interface{}); ok {
			containers := make([]map[string]interface{}, 0, len(cs))
			for _, c := range cs {
				kept := make(map[string]interface{}, len(podListContainerKeys))
				for _, k := range podListContainerKeys {
					if v, ok := c[k]; ok {
						kept[k] = v
					}
				}
				containers = append(containers, kept)
			}
			row["containers"] = containers
		}
		rows = append(rows, row)
	}
	return rows
}
