package shipper

import "github.com/kubebolt/kubebolt/packages/agent/internal/channellimit"

// maxMsgBytes resolves the gRPC max message size for the AgentChannel client.
// The single source of truth lives in channellimit — the proxy body cap derives
// from the same value, so the two never drift. KUBEBOLT_AGENT_MAX_MSG_BYTES
// overrides the 64 MiB default. Kept in sync with the backend-side limit
// (apps/api/internal/agent).
func maxMsgBytes() int { return channellimit.MaxMessageBytes() }
