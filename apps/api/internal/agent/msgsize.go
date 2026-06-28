package agent

import (
	"os"
	"strconv"
)

// maxMsgBytes resolves the gRPC max message size for the AgentChannel server. The
// channel proxies apiserver responses that exceed gRPC's 4 MiB default on large
// clusters; without this the server recv fails with ResourceExhausted and the
// session is torn down. KUBEBOLT_AGENT_MAX_MSG_BYTES overrides the 64 MiB default.
// Kept in sync with the agent-side limit (packages/agent/internal/shipper).
func maxMsgBytes() int {
	const def = 64 << 20 // 64 MiB
	if v := os.Getenv("KUBEBOLT_AGENT_MAX_MSG_BYTES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}
