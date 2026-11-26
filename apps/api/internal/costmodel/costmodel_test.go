package costmodel

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// The web's formula: 1.14 cores and 2.12 GiB at $0.03/core-h and $0.004/GiB-h.
func TestMonthlySavings_IsTheWebsFormula(t *testing.T) {
	got := MonthlySavings(1140, 2277507072, NodeRates{CPUCoreHourly: 0.03, RAMGiBHourly: 0.004})
	want := (1.14*0.03 + (2277507072.0/(1024*1024*1024))*0.004) * 730
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("got %v, want %v", got, want)
	}
	if Round2(got) != 31.16 {
		t.Fatalf("rounded %v, want 31.16", Round2(got))
	}
	if (NodeRates{}).Available() {
		t.Fatal("no rates must not count as available")
	}
}

const ecbDoc = `<?xml version="1.0" encoding="UTF-8"?>
<gesmes:Envelope xmlns:gesmes="http://www.gesmes.org/xml/2002-08-01" xmlns="http://www.ecb.int/vocabulary/2002-08-01/eurofxref">
  <gesmes:subject>Reference rates</gesmes:subject>
  <Cube><Cube time="2026-11-24"><Cube currency="USD" rate="1.0800"/><Cube currency="MXN" rate="21.6000"/></Cube></Cube>
</gesmes:Envelope>`

func ecbServer(t *testing.T, fail *atomic.Bool, hits *atomic.Int32) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if fail.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(ecbDoc))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestECB_ConvertsThroughTheEuro(t *testing.T) {
	var fail atomic.Bool
	var hits atomic.Int32
	srv := ecbServer(t, &fail, &hits)
	now := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	e := &ECB{URL: srv.URL, Client: srv.Client(), TTL: 24 * time.Hour, MaxStale: 7 * 24 * time.Hour, Now: func() time.Time { return now }}

	usdToEur, ok := e.Rate(context.Background(), "usd", "eur")
	if !ok || math.Abs(usdToEur-1/1.08) > 1e-9 {
		t.Fatalf("usd→eur = %v, %v; want 1/1.08", usdToEur, ok)
	}
	usdToMxn, ok := e.Rate(context.Background(), "USD", "MXN")
	if !ok || math.Abs(usdToMxn-21.6/1.08) > 1e-9 {
		t.Fatalf("usd→mxn = %v, %v; want 20", usdToMxn, ok)
	}
	if r, ok := e.Rate(context.Background(), "eur", "eur"); !ok || r != 1 {
		t.Fatalf("same currency = %v, %v", r, ok)
	}
	if _, ok := e.Rate(context.Background(), "usd", "xyz"); ok {
		t.Fatal("an unknown currency got a rate")
	}
	if hits.Load() != 1 {
		t.Fatalf("fetched %d times within the TTL, want once", hits.Load())
	}
}

// A failed refresh keeps the last rates for up to MaxStale, then gives up; a
// failure is not retried on every call.
func TestECB_KeepsTheLastRatesThroughAnOutage(t *testing.T) {
	var fail atomic.Bool
	var hits atomic.Int32
	srv := ecbServer(t, &fail, &hits)
	now := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	e := &ECB{URL: srv.URL, Client: srv.Client(), TTL: 24 * time.Hour, MaxStale: 7 * 24 * time.Hour, Now: func() time.Time { return now }}
	if _, ok := e.Rate(context.Background(), "usd", "eur"); !ok {
		t.Fatal("first fetch failed")
	}

	fail.Store(true)
	now = now.Add(25 * time.Hour)
	if _, ok := e.Rate(context.Background(), "usd", "eur"); !ok {
		t.Fatal("a day-old rate was dropped on a failed refresh")
	}
	before := hits.Load()
	_, _ = e.Rate(context.Background(), "usd", "eur")
	if hits.Load() != before {
		t.Fatal("a failing source was retried immediately")
	}

	now = now.Add(7 * 24 * time.Hour)
	if _, ok := e.Rate(context.Background(), "usd", "eur"); ok {
		t.Fatal("rates older than MaxStale were still used")
	}
}

func TestECB_NothingFetchedIsNoRate(t *testing.T) {
	var fail atomic.Bool
	var hits atomic.Int32
	fail.Store(true)
	srv := ecbServer(t, &fail, &hits)
	e := &ECB{URL: srv.URL, Client: srv.Client(), TTL: 24 * time.Hour, MaxStale: 7 * 24 * time.Hour, Now: time.Now}
	if _, ok := e.Rate(context.Background(), "usd", "eur"); ok {
		t.Fatal("a rate without any successful fetch")
	}
}
