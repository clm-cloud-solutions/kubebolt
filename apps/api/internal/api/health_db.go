package api

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// GET /health/db answers whether the API can reach its database, for an
// external monitor. /health answers ok without touching Postgres, and that is
// what the Kubernetes probes must keep doing: a probe that failed with the
// database would take the only replica out of the Service on a Postgres blip.
// This route is for watching the database from outside, not for routing.
//
// It is public, says nothing beyond ok / unavailable, and caches its answer for
// dbHealthCacheFor: however often it is called, it pings Postgres at most once
// per window per replica.

var (
	dbHealthCacheFor = 5 * time.Second
	dbHealthTimeout  = 2 * time.Second

	dbCheckMu sync.RWMutex
	dbCheck   func(context.Context) error
)

// SetDatabaseCheck installs the edition's database ping. nil (OSS, an EE
// install without Postgres) makes /health/db answer ok: there is no database
// out of process to lose.
func SetDatabaseCheck(fn func(context.Context) error) {
	dbCheckMu.Lock()
	dbCheck = fn
	dbCheckMu.Unlock()
}

type dbHealthState struct {
	mu sync.Mutex
	at time.Time
	ok bool
}

var dbHealth dbHealthState

func handleHealthDB(w http.ResponseWriter, r *http.Request) {
	dbCheckMu.RLock()
	check := dbCheck
	dbCheckMu.RUnlock()

	ok := true
	if check != nil {
		ok = dbHealth.read(check)
	}
	if !ok {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("database unavailable"))
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

// read returns the cached answer, or pings once when it is older than the
// window. The ping runs on its own deadline, not the request's: a caller that
// hangs up must not cache a failure the database never had.
func (s *dbHealthState) read(check func(context.Context) error) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.at.IsZero() && time.Since(s.at) < dbHealthCacheFor {
		return s.ok
	}
	ctx, cancel := context.WithTimeout(context.Background(), dbHealthTimeout)
	err := check(ctx)
	cancel()
	ok := err == nil
	if !ok && (s.ok || s.at.IsZero()) {
		slog.Warn("health/db: database unreachable", slog.String("error", err.Error()))
	} else if ok && !s.ok && !s.at.IsZero() {
		slog.Info("health/db: database reachable again")
	}
	s.ok, s.at = ok, time.Now()
	return ok
}
