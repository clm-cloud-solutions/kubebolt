package config

import "testing"

// An idle budget below the request timeout ended a connect that was still
// receiving: one proxied page may take the whole request timeout, and the
// connect deadline counts from the last response (sb-dev, 2026-11-18: pages of
// 10–30 s against a 25 s default). The connect deadline and the stuck watchdog
// are held at the request timeout plus headroom.
func TestEffectiveIdleBudgetSeconds(t *testing.T) {
	cases := []struct {
		name                string
		configured, request int
		want                int
	}{
		{"the old 25s default under a 30s request", 25, 30, 35},
		{"equal to the request timeout", 30, 30, 35},
		{"at the floor", 35, 30, 35},
		{"above the floor", 60, 30, 60},
		{"request timeout widened past the deadline", 60, 90, 95},
		{"watchdog disabled stays disabled", 0, 30, 0},
		{"no request timeout leaves the value", 25, 0, 25},
	}
	for _, c := range cases {
		if got := EffectiveIdleBudgetSeconds(c.configured, c.request); got != c.want {
			t.Errorf("%s: EffectiveIdleBudgetSeconds(%d, %d) = %d, want %d", c.name, c.configured, c.request, got, c.want)
		}
	}
}

// The shipped defaults need no raising: an install that never touches the
// settings runs what it was told.
func TestDefaultIdleBudgetsClearTheRequestTimeout(t *testing.T) {
	for name, v := range map[string]int{"connect": DefaultConnectTimeoutSeconds, "stuck": DefaultStuckTimeoutSeconds} {
		if got := EffectiveIdleBudgetSeconds(v, DefaultRequestTimeoutSeconds); got != v {
			t.Errorf("default %s budget %ds is raised to %ds by the %ds request timeout", name, v, got, DefaultRequestTimeoutSeconds)
		}
	}
}

// Env baselines go through the same parse as before; the new connect default
// applies when the variable is unset.
func TestLoadIngestChannelConfig_ConnectDefault(t *testing.T) {
	t.Setenv("KUBEBOLT_AGENT_PROXY_CONNECT_TIMEOUT", "")
	if got := LoadIngestChannelConfig().ConnectTimeoutSeconds; got != 60 {
		t.Errorf("connect default = %ds, want 60s", got)
	}
	t.Setenv("KUBEBOLT_AGENT_PROXY_CONNECT_TIMEOUT", "25s")
	if got := LoadIngestChannelConfig().ConnectTimeoutSeconds; got != 25 {
		t.Errorf("connect from env = %ds, want the configured 25s (the floor applies at use)", got)
	}
}
