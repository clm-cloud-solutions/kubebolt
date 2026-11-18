package channellimit

import (
	"strings"
	"testing"
)

const mib = 1 << 20

// Without a memory limit in the environment the cap is the channel's message
// size minus headroom, as before.
func TestMaxBodyBytes_ChannelBoundWithoutAMemoryLimit(t *testing.T) {
	t.Setenv("KUBEBOLT_AGENT_MEMORY_LIMIT_BYTES", "")
	t.Setenv("KUBEBOLT_AGENT_MAX_MSG_BYTES", "")
	if got := MaxBodyBytes(); got != 63*mib {
		t.Fatalf("MaxBodyBytes = %d MiB, want 63", got/mib)
	}
	if BoundByMemory() {
		t.Fatal("BoundByMemory without a memory limit")
	}
}

// At 128Mi a body is capped at 32 MiB: the 33.5 MiB ReplicaSet LIST that
// OOMKilled agents at 128Mi is refused with an error that names the
// knob, instead of being read whole.
func TestMaxBodyBytes_AQuarterOfASmallMemoryLimit(t *testing.T) {
	t.Setenv("KUBEBOLT_AGENT_MEMORY_LIMIT_BYTES", "134217728") // 128Mi
	if got := MaxBodyBytes(); got != 32*mib {
		t.Fatalf("MaxBodyBytes = %d MiB, want 32", got/mib)
	}
	if !BoundByMemory() {
		t.Fatal("BoundByMemory = false at 128Mi")
	}
	msg := OverSizeError("response body", int64(MaxBodyBytes()))
	if !strings.Contains(msg, "32 MiB") || !strings.Contains(msg, "128 MiB") || !strings.Contains(msg, "resources.limits.memory") {
		t.Fatalf("error = %q, want the cap, the limit and the knob", msg)
	}
}

// The chart's 256Mi default keeps the channel's cap; so does anything larger.
func TestMaxBodyBytes_ALargeMemoryLimitKeepsTheChannelCap(t *testing.T) {
	for _, v := range []string{"268435456", "536870912"} { // 256Mi, 512Mi
		t.Setenv("KUBEBOLT_AGENT_MEMORY_LIMIT_BYTES", v)
		if got := MaxBodyBytes(); got != 63*mib {
			t.Fatalf("limit %s: MaxBodyBytes = %d MiB, want 63", v, got/mib)
		}
		if BoundByMemory() {
			t.Fatalf("limit %s: BoundByMemory", v)
		}
		if msg := OverSizeError("response body", int64(MaxBodyBytes())); !strings.Contains(msg, "KUBEBOLT_AGENT_MAX_MSG_BYTES") {
			t.Fatalf("limit %s: error = %q, want the channel knob", v, msg)
		}
	}
}

func TestMemoryLimitBytes_IgnoresGarbage(t *testing.T) {
	for _, v := range []string{"", "abc", "-5", "0"} {
		t.Setenv("KUBEBOLT_AGENT_MEMORY_LIMIT_BYTES", v)
		if got := MemoryLimitBytes(); got != 0 {
			t.Fatalf("%q → %d, want 0", v, got)
		}
	}
}
