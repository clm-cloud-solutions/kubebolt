package shipper

import (
	"os"
	"strconv"
)

// maxMsgBytes resolves the gRPC max message size for the AgentChannel client.
// On large clusters the apiserver responses the backend pushes over the channel
// (agent-proxy) exceed gRPC's 4 MiB default, failing the client recv with
// ResourceExhausted and tearing down the whole session — metrics included.
// KUBEBOLT_AGENT_MAX_MSG_BYTES overrides the 64 MiB default. Kept in sync with the
// backend-side limit (apps/api/internal/agent).
func maxMsgBytes() int {
	const def = 64 << 20 // 64 MiB
	if v := os.Getenv("KUBEBOLT_AGENT_MAX_MSG_BYTES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}
