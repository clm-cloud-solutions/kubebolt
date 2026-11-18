package cluster

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	clientfeatures "k8s.io/client-go/features"
	clientfeaturestesting "k8s.io/client-go/features/testing"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// fakeReplicaSetServer answers the ReplicaSet LIST in `pages` pages of one
// object, sleeping `delay` before each. A page listed in stallFrom and after
// never answers (until the client gives up). Watches hang until canceled.
func fakeReplicaSetServer(t *testing.T, pages int, delay time.Duration, stallFrom int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/apis/apps/v1/replicasets" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		if q.Get("watch") == "true" || q.Get("watch") == "1" {
			<-r.Context().Done()
			return
		}
		page := 1
		if c := q.Get("continue"); c != "" {
			page, _ = strconv.Atoi(strings.TrimPrefix(c, "p"))
		}
		if stallFrom > 0 && page >= stallFrom {
			<-r.Context().Done()
			return
		}
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
		next := ""
		if pages <= 0 || page < pages {
			next = fmt.Sprintf("p%d", page+1)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"kind":"ReplicaSetList","apiVersion":"apps/v1","metadata":{"resourceVersion":"100","continue":%q},"items":[{"metadata":{"name":"rs-%d","namespace":"default","resourceVersion":"100"}}]}`, next, page)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// connectorFor builds the part of a Connector the cache sync uses, with the
// same transport wrapper newConnectorFromConfig installs.
func connectorFor(t *testing.T, srv *httptest.Server) *Connector {
	t.Helper()
	// The API server runs informers with LIST+WATCH (cmd/server/watchlist_gate.go
	// turns client-go's streaming watch-list off before any client exists); a
	// test binary keeps client-go's default, so set it the same way here.
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	cfg := &rest.Config{Host: srv.URL}
	progress := newConnectProgress()
	cfg.Wrap(func(rt http.RoundTripper) http.RoundTripper { return &progressRoundTripper{next: rt, p: progress} })
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	f := newInformerFactory(cs)
	f.Apps().V1().ReplicaSets().Informer()
	c := &Connector{factory: f, progress: progress, stopCh: make(chan struct{})}
	f.Start(c.stopCh)
	t.Cleanup(func() { close(c.stopCh); f.Shutdown() })
	return c
}

// A slow tunnel that keeps delivering: 8 pages at 150 ms each take ~1.2 s,
// three times the 400 ms budget, and the sync still completes, because the
// budget measures silence. With the old fixed deadline this failed at 400 ms
// on every attempt.
func TestWaitForCacheSync_SlowPagesThatKeepArrivingSync(t *testing.T) {
	c := connectorFor(t, fakeReplicaSetServer(t, 8, 150*time.Millisecond, 0))
	start := time.Now()
	if err := c.waitForCacheSync(400 * time.Millisecond); err != nil {
		t.Fatalf("sync failed after %s: %v", time.Since(start), err)
	}
	if took := time.Since(start); took < 800*time.Millisecond {
		t.Fatalf("synced in %s: the test needs the sync to outlast the budget to mean anything", took)
	}
}

// A cluster that stops answering still fails, after the budget of silence.
func TestWaitForCacheSync_FailsWhenPagesStopArriving(t *testing.T) {
	c := connectorFor(t, fakeReplicaSetServer(t, 8, 50*time.Millisecond, 3))
	start := time.Now()
	err := c.waitForCacheSync(400 * time.Millisecond)
	if err == nil {
		t.Fatal("sync succeeded against a server that stopped answering")
	}
	if !strings.Contains(err.Error(), "no response from the cluster for 400ms") {
		t.Fatalf("error = %v, want it to say the cluster went silent", err)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("took %s to notice a stall of 400ms", took)
	}
}

// A connect that trickles forever is still bounded by maxConnectDuration.
func TestWaitForCacheSync_CapsAConnectThatNeverEnds(t *testing.T) {
	prev := maxConnectDuration
	maxConnectDuration = 600 * time.Millisecond
	t.Cleanup(func() { maxConnectDuration = prev })
	c := connectorFor(t, fakeReplicaSetServer(t, 0, 50*time.Millisecond, 0)) // endless pages
	done := make(chan error, 1)
	go func() { done <- c.waitForCacheSync(400 * time.Millisecond) }()
	var err error
	select {
	case err = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the sync was still running after 5s: nothing caps a connect that keeps receiving pages")
	}
	if err == nil || !strings.Contains(err.Error(), "still connecting after 600ms") {
		t.Fatalf("error = %v, want the overall cap", err)
	}
}

type staticRT struct{ code int }

func (s staticRT) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: s.code, Body: http.NoBody}, nil
}

// Only a successful response to a request that is not a watch is progress:
// the watches of informers that already synced deliver events all the time on
// a busy cluster and would hide a stuck LIST, and a LIST failing in a retry
// loop is not progress either.
func TestProgressRoundTripper_CountsOnlySuccessfulNonWatchResponses(t *testing.T) {
	cases := []struct {
		name string
		url  string
		code int
		want bool
	}{
		{"list page", "https://k8s/apis/apps/v1/replicasets?limit=500", 200, true},
		{"created (SSAR)", "https://k8s/apis/authorization.k8s.io/v1/selfsubjectaccessreviews", 201, true},
		{"watch", "https://k8s/apis/apps/v1/replicasets?watch=true", 200, false},
		{"watch=1", "https://k8s/api/v1/pods?watch=1", 200, false},
		{"server error", "https://k8s/apis/apps/v1/replicasets?limit=500", 500, false},
		{"throttled", "https://k8s/api/v1/pods", 429, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &connectProgress{} // last = 0: idle since 1970
			rt := &progressRoundTripper{next: staticRT{tc.code}, p: p}
			req, _ := http.NewRequest(http.MethodGet, tc.url, nil)
			if _, err := rt.RoundTrip(req); err != nil {
				t.Fatal(err)
			}
			if got := p.idleFor() < time.Minute; got != tc.want {
				t.Fatalf("counted as progress = %v, want %v", got, tc.want)
			}
		})
	}
}

// progressContext reports why it ended, and touching keeps it alive.
func TestProgressContext_TouchKeepsItAliveAndTheCauseSaysWhy(t *testing.T) {
	p := newConnectProgress()
	ctx, cancel := progressContext(context.Background(), p, 200*time.Millisecond, 0)
	defer cancel()
	deadline := time.Now().Add(600 * time.Millisecond)
	for time.Now().Before(deadline) {
		p.touch()
		select {
		case <-ctx.Done():
			t.Fatalf("ended while progress kept arriving: %v", context.Cause(ctx))
		case <-time.After(50 * time.Millisecond):
		}
	}
	<-ctx.Done() // stop touching: it must end on silence
	var be *connectBudgetError
	if !errors.As(context.Cause(ctx), &be) || !be.stalled {
		t.Fatalf("cause = %v, want a stall", context.Cause(ctx))
	}
}
