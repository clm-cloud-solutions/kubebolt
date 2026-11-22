package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func withDBCheck(t *testing.T, fn func(context.Context) error) {
	t.Helper()
	prevCache, prevTimeout := dbHealthCacheFor, dbHealthTimeout
	SetDatabaseCheck(fn)
	dbHealth = dbHealthState{}
	t.Cleanup(func() {
		SetDatabaseCheck(nil)
		dbHealth = dbHealthState{}
		dbHealthCacheFor, dbHealthTimeout = prevCache, prevTimeout
	})
}

func getHealthDB() *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	handleHealthDB(rr, httptest.NewRequest(http.MethodGet, "/health/db", nil))
	return rr
}

func TestHealthDB_AnswersWhatTheDatabaseSays(t *testing.T) {
	var down atomic.Bool
	withDBCheck(t, func(context.Context) error {
		if down.Load() {
			return errors.New("dial tcp 10.0.0.5:5432: connect: connection refused")
		}
		return nil
	})
	dbHealthCacheFor = 0

	if rr := getHealthDB(); rr.Code != http.StatusOK || rr.Body.String() != "ok" {
		t.Fatalf("database up: %d %q", rr.Code, rr.Body.String())
	}
	down.Store(true)
	rr := getHealthDB()
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("database down: status %d, want 503", rr.Code)
	}
	if body := rr.Body.String(); body != "database unavailable" {
		t.Errorf("the public body must not carry the error: %q", body)
	}
}

func TestHealthDB_PingsAtMostOncePerWindow(t *testing.T) {
	var pings atomic.Int32
	withDBCheck(t, func(context.Context) error { pings.Add(1); return nil })
	dbHealthCacheFor = time.Hour

	for i := 0; i < 50; i++ {
		getHealthDB()
	}
	if n := pings.Load(); n != 1 {
		t.Errorf("50 requests inside one window pinged %d times, want 1", n)
	}
}

func TestHealthDB_ASlowDatabaseIsUnavailable(t *testing.T) {
	withDBCheck(t, func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	dbHealthTimeout = 20 * time.Millisecond

	start := time.Now()
	rr := getHealthDB()
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("a ping past its deadline: status %d, want 503", rr.Code)
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("the check waited %v; the timeout must bound it", took)
	}
}

func TestHealthDB_NoDatabaseIsOK(t *testing.T) {
	withDBCheck(t, nil)
	if rr := getHealthDB(); rr.Code != http.StatusOK {
		t.Errorf("no database configured: status %d, want 200", rr.Code)
	}
}
