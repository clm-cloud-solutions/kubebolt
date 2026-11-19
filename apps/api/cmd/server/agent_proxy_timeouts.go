package main

import "log/slog"

// warnRaisedIdleBudget says when an agent-proxy idle budget (the connect
// deadline, the stuck-agent watchdog) runs above what was configured because
// config.EffectiveIdleBudgetSeconds held it at the request timeout plus
// headroom. Without the line, a Settings value of 25 s and a log reading 35 s
// would contradict each other.
func warnRaisedIdleBudget(name string, configured, effective, request int) {
	if configured == effective {
		return
	}
	slog.Warn("agent-proxy idle budget raised to the request timeout plus headroom",
		slog.String("timeout", name),
		slog.Int("configured_seconds", configured),
		slog.Int("effective_seconds", effective),
		slog.Int("request_timeout_seconds", request))
}
