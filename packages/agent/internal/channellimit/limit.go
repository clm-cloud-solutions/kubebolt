// Package channellimit centralizes the AgentChannel gRPC size limits so the
// proxy body cap always tracks the channel's max message size: set the channel
// limit in ONE place and the body cap follows automatically.
package channellimit

import (
	"os"
	"strconv"
)

// MaxMessageBytes resolves the gRPC max message size for the AgentChannel
// (the client and the backend server keep this in sync). On large clusters the
// apiserver responses pushed over the channel (agent-proxy) exceed gRPC's 4 MiB
// default, failing the recv with ResourceExhausted and tearing down the whole
// session — metrics included. 64 MiB default; KUBEBOLT_AGENT_MAX_MSG_BYTES overrides.
func MaxMessageBytes() int {
	const def = 64 << 20 // 64 MiB
	if v := os.Getenv("KUBEBOLT_AGENT_MAX_MSG_BYTES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// bodyHeadroom reserves room inside one channel message for everything in a
// KubeProxyResponse that is NOT the body — response headers, status, request_id,
// protobuf framing — so a full-size body never overflows MaxMessageBytes.
const bodyHeadroom = 1 << 20 // 1 MiB

// MaxBodyBytes is the largest proxied request/response body the agent will
// materialize and relay. Derived from MaxMessageBytes so it tracks the channel
// limit automatically: raise KUBEBOLT_AGENT_MAX_MSG_BYTES and the body cap moves
// with it, holding bodyHeadroom back for the rest of the message.
func MaxBodyBytes() int {
	if m := MaxMessageBytes() - bodyHeadroom; m > 0 {
		return m
	}
	return MaxMessageBytes()
}
