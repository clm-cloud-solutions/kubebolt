package websocket

// Event type constants used for WebSocket messages.
const (
	ResourceUpdated = "resource:updated"
	ResourceDeleted = "resource:deleted"
	EventNew        = "event:new"
	InsightNew      = "insight:new"
	InsightResolved = "insight:resolved"
	MetricsRefresh  = "metrics:refresh"
	// ClusterConnected fires when a previously-failed connector for an
	// agent-proxy context recovers (an agent registered in time for
	// auto-retry to bring informers up). The frontend invalidates
	// `['clusters']` and `['cluster-overview']` immediately so the user
	// doesn't wait for the 30s TanStack refetch tick.
	ClusterConnected = "cluster:connected"
)
