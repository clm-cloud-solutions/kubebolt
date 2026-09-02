package main

import (
	"log/slog"
	"os"
)

// WatchListClient — why KubeBolt turns off a client-go optimisation by default.
//
// From client-go 1.35 the `WatchListClient` feature gate is Beta and ON. With it,
// an informer no longer does LIST + WATCH: it opens a watch with
// `sendInitialEvents=true` and receives the entire initial state as a stream of
// ADDED events, closed by a bookmark carrying `k8s.io/initial-events-end`.
//
// Against a real apiserver that is strictly better. Over KubeBolt's agent tunnel
// it silently truncates caches. The tunnel is known to lose watch events
// (finding #12), and the two list shapes fail very differently:
//
//	LIST + WATCH   a lost event → an object goes stale, and the next relist fixes it
//	streaming list a lost event → the object NEVER ENTERS the cache, and nothing retries
//
// And `WaitForCacheSync` cannot tell the difference: it waits for the
// end-of-initial-events bookmark, which arrives however many ADDED events were
// dropped on the way. The informer then reports itself synced while serving a
// short list — the worst failure shape there is, because every count, list,
// topology edge and ownership lookup downstream reads from it and none of them
// can know.
//
// Measured in-vivo 2026-08-11 (finding #21): the ReplicaSet cache of an
// agent-proxy cluster held 101 of 117 objects, stable across three sweeps ten
// minutes apart, `WaitForCacheSync` green throughout. Flipping this one gate —
// same process, same cluster, same agent — took the divergence to zero on all
// three audited types.
//
// # Why it is off for everyone, not just for agent installs
//
// The gate is read when each reflector is built, from a process-global, so it
// cannot be decided per connector. And it cannot honestly be decided at boot
// either: an agent may register at any moment, in any distribution channel,
// without restarting the API — including OSS and EE self-hosted, which mostly
// connect by kubeconfig. A process that started with the gate on would then serve
// short caches for that new cluster and say nothing.
//
// The trade is asymmetric. On: silently short lists, invisible, corrupting
// everything computed from them. Off: one unary response instead of a stream on
// each informer's first sync — which is exactly what every KubeBolt release did
// until client-go moved to 1.35. Turning it off is not a regression, it is the
// status quo ante, and it is the shape the tunnel's body cap was sized for in
// finding #08.
//
// # The escape hatch
//
// KUBEBOLT_WATCHLIST_CLIENT=true restores client-go's default. It is meant for an
// operator who connects ONLY by kubeconfig or in-cluster, never through an agent,
// and wants the cheaper initial sync back. If an agent is ever added to such an
// install, this must go back off.
//
// A raw KUBE_FEATURE_WatchListClient in the environment always wins: it is
// client-go's own knob, and an operator who reaches for it means it.
const watchListEnvVar = "KUBEBOLT_WATCHLIST_CLIENT"

// clientGoWatchListEnvVar is client-go's own gate. It reads its features from
// the environment lazily, on the first Enabled() call, so setting it before any
// client is built is enough — no client-go internals, no ReplaceFeatureGates.
const clientGoWatchListEnvVar = "KUBE_FEATURE_WatchListClient"

// configureWatchListClient must run before ANY Kubernetes client or informer is
// created. client-go caches the parsed gates on first read, so a later call
// would be silently ignored.
func configureWatchListClient() {
	if raw, ok := os.LookupEnv(clientGoWatchListEnvVar); ok {
		slog.Info("watch-list: leaving client-go's own gate untouched",
			slog.String("env", clientGoWatchListEnvVar),
			slog.String("value", raw))
		return
	}
	if os.Getenv(watchListEnvVar) == "true" {
		slog.Info("watch-list: streaming initial list ENABLED by configuration — " +
			"only safe when no cluster is reached through an agent (see finding #21)")
		return
	}
	if err := os.Setenv(clientGoWatchListEnvVar, "false"); err != nil {
		// Not fatal: the gate stays at client-go's default, which is the risky
		// one, so this has to be loud rather than swallowed.
		slog.Error("watch-list: could not disable the streaming initial list — "+
			"informer caches served over an agent tunnel may be silently short",
			slog.String("error", err.Error()))
		return
	}
	// Logged at INFO on every boot, deliberately. A feature gate turned off in
	// silence is the next person's unexplained behaviour.
	slog.Info("watch-list: streaming initial list disabled (informers use LIST+WATCH) — " +
		"set " + watchListEnvVar + "=true to restore it on installs that never use an agent")
}
