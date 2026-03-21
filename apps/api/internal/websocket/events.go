package websocket

// Event type constants used for WebSocket messages.
const (
	ResourceUpdated = "resource:updated"
	ResourceDeleted = "resource:deleted"
	EventNew        = "event:new"
	InsightNew      = "insight:new"
	InsightResolved = "insight:resolved"
	MetricsRefresh  = "metrics:refresh"
)
