package audit

import "log/slog"

// The process-wide audit sink.
//
// It lives here rather than in the api package because the audit trail has to
// cover TWO planes and they are implemented in different packages: cluster
// mutations (package api) and the administrative plane — users, teams, tokens,
// clusters, settings, integrations — (package auth). A sink owned by api would
// force auth to import api, which it cannot.
//
// The direction that works is auth → audit and api → audit, so the sink lives
// at the bottom. audit deliberately imports NEITHER, which is also why Emit
// takes a fully-built Record: extracting the caller's identity and org needs
// auth's context helpers, and doing it here would close the cycle.
var (
	sink        Store
	clusterIDFn func() string
)

// SetSink wires the durable store + a resolver for the active cluster id.
// Called once at boot, after the router is built. A nil store leaves the trail
// slog-only, which is the OSS/auth-disabled posture.
func SetSink(s Store, clusterID func() string) {
	sink = s
	clusterIDFn = clusterID
}

// Enabled reports whether a durable sink is wired. Callers use it to skip
// building a Record they would only throw away.
func Enabled() bool { return sink != nil }

// ClusterID resolves the active cluster for stamping. Empty when no resolver is
// wired or no cluster is connected — an administrative action (creating a user,
// rotating a token) legitimately has no cluster, and that is not an error.
func ClusterID() string {
	if clusterIDFn == nil {
		return ""
	}
	return clusterIDFn()
}

// Emit persists one record. Failures are logged, never propagated: a mutation
// that already happened must not be reported as failed because its audit write
// did, and the slog line at the call site is the fallback record.
func Emit(rec *Record) {
	if sink == nil || rec == nil {
		return
	}
	if err := sink.Append(rec); err != nil {
		slog.Warn("audit persist failed",
			slog.String("action", rec.Action),
			slog.String("error", err.Error()))
	}
}
