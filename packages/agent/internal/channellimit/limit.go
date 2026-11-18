// Package channellimit centralizes the AgentChannel gRPC size limits so the
// proxy body cap always tracks the channel's max message size: set the channel
// limit in ONE place and the body cap follows automatically.
package channellimit

import (
	"fmt"
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

// MemoryLimitBytes is the agent container's memory limit, from the
// KUBEBOLT_AGENT_MEMORY_LIMIT_BYTES env the chart sets through the downward
// API (resourceFieldRef limits.memory). 0 when unset or unreadable: no cap
// derived from memory.
func MemoryLimitBytes() int64 {
	v := os.Getenv("KUBEBOLT_AGENT_MEMORY_LIMIT_BYTES")
	if v == "" {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// memoryShare is the part of the agent's memory one proxied body may take.
// A body is held whole (read, then framed into one gRPC message, then sent),
// so its peak cost is a few times its size: a quarter keeps a full-size body
// clear of the limit next to the agent's own working set (a 33.5 MiB
// ReplicaSet LIST OOMKilled agents at 128Mi while the cap allowed 63 MiB).
const memoryShare = 4

// MaxBodyBytes is the largest proxied request/response body the agent will
// materialize and relay: the channel's message size minus bodyHeadroom, and
// never more than a quarter of the agent's memory limit. Raising
// KUBEBOLT_AGENT_MAX_MSG_BYTES moves the first bound; raising the agent's
// resources.limits.memory moves the second. Past it the agent answers with an
// error naming the limit, instead of being OOMKilled mid-read.
func MaxBodyBytes() int {
	n := MaxMessageBytes() - bodyHeadroom
	if n <= 0 {
		n = MaxMessageBytes()
	}
	if lim := MemoryLimitBytes(); lim > 0 {
		if byMem := lim / memoryShare; byMem < int64(n) {
			return int(byMem)
		}
	}
	return n
}

// BoundByMemory reports whether MaxBodyBytes is the memory share rather than
// the channel limit, so an over-size error can say which knob to turn.
func BoundByMemory() bool {
	lim := MemoryLimitBytes()
	return lim > 0 && lim/memoryShare < int64(MaxMessageBytes()-bodyHeadroom)
}

// OverSizeError is the error text for a body over MaxBodyBytes.
func OverSizeError(what string, max int64) string {
	mib := float64(max) / (1 << 20)
	if BoundByMemory() {
		return fmt.Sprintf("%s exceeds %.0f MiB, a quarter of this agent's memory limit (%.0f MiB): raise the agent's resources.limits.memory", what, mib, float64(MemoryLimitBytes())/(1<<20))
	}
	return fmt.Sprintf("%s exceeds %.0f MiB, the agent channel's message size (KUBEBOLT_AGENT_MAX_MSG_BYTES)", what, mib)
}
