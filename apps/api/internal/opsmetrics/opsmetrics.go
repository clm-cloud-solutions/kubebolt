// Package opsmetrics holds the series that describe the platform itself (doc
// #67, O2): the API's HTTP surface, its calls to VictoriaMetrics, its WebSocket
// feed and its background jobs. None carries a tenant — they are the
// operator's, read in Administration › System › Health — and every label set is small and
// closed (route groups, call sites, job names), so they cost a few hundred
// series per replica at most.
//
// They live in the default registry and reach VictoriaMetrics through the
// API's self-push with the rest of it (instance, run_id and job stamped there),
// so a counter is read with increase_pure and a gauge through liveProcess.
package opsmetrics

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	httpRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "kubebolt_http_requests_total",
		Help: "HTTP requests the API answered, by route group and status class (2xx, 3xx, 4xx, 5xx), with 503 apart: it is how the API says a cluster or a feature is unavailable, a state rather than a failure.",
	}, []string{"group", "code"})
	httpSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "kubebolt_http_request_seconds",
		Help:    "Time to answer an HTTP request, by route group. Streams (WebSocket, terminal, port-forward, Kobi chat, MCP) are not timed.",
		Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
	}, []string{"group"})
	httpInFlight = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "kubebolt_http_requests_in_flight",
		Help: "HTTP requests being answered right now, streams excluded.",
	})

	vmRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "kubebolt_vm_requests_total",
		Help: "Requests the API made to VictoriaMetrics, by call site, operation (query, query_range, metadata, export, import, write, other) and result (ok, http_4xx, http_5xx, timeout, canceled, error).",
	}, []string{"caller", "op", "result"})
	vmSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "kubebolt_vm_request_seconds",
		Help:    "Time VictoriaMetrics took to answer the API (until the response headers), by operation.",
		Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 15},
	}, []string{"op"})

	wsDropped = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "kubebolt_ws_dropped_total",
		Help: "Live-update events the WebSocket hub could not deliver: queue_full (the broadcast queue was full, the event was dropped for everyone) or slow_client (a browser fell behind and was disconnected).",
	}, []string{"reason"})

	// The job's label is "name", never "job": the self-push stamps job= on every
	// series it writes (job="kubebolt-api"), and that would overwrite it.
	jobRuns = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "kubebolt_job_runs_total",
		Help: "Runs of the API's background jobs, by job name and result (ok, error).",
	}, []string{"name", "result"})
	jobLastSuccess = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "kubebolt_job_last_success_timestamp_seconds",
		Help: "When each background job last finished without error (unix seconds).",
	}, []string{"name"})
	jobDuration = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "kubebolt_job_last_duration_seconds",
		Help: "How long each background job's last run took.",
	}, []string{"name"})
	jobInterval = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "kubebolt_job_interval_seconds",
		Help: "How often each background job is meant to run; a job whose last success is several intervals old has stopped.",
	}, []string{"name"})
)

func init() {
	prometheus.MustRegister(httpRequests, httpSeconds, httpInFlight, vmRequests, vmSeconds,
		wsDropped, jobRuns, jobLastSuccess, jobDuration, jobInterval)
}

// ─── HTTP ───────────────────────────────────────────────────────────────

// HTTPStarted counts a request in flight; the returned func ends it. Streams
// are left out: a browser tab holding the WebSocket open is not load.
func HTTPStarted(stream bool) func() {
	if stream {
		return func() {}
	}
	httpInFlight.Inc()
	return httpInFlight.Dec
}

// ObserveHTTP records an answered request. status 0 (nothing written) is a 200,
// as net/http sends it.
func ObserveHTTP(group string, status int, d time.Duration, stream bool) {
	httpRequests.WithLabelValues(group, statusClass(status)).Inc()
	if !stream {
		httpSeconds.WithLabelValues(group).Observe(d.Seconds())
	}
}

// statusClass folds a status into its class, except 503: the API answers it
// when a cluster is unreachable (the UI's «Cluster unreachable») or a feature
// is not configured, and a customer's dead cluster polled every 30 s must not
// read as the API failing.
func statusClass(status int) string {
	switch {
	case status == http.StatusServiceUnavailable:
		return "503"
	case status == 0 || (status >= 200 && status < 300):
		return "2xx"
	case status < 200:
		return "1xx"
	case status < 400:
		return "3xx"
	case status < 500:
		return "4xx"
	default:
		return "5xx"
	}
}

// ─── VictoriaMetrics ────────────────────────────────────────────────────

// VMClient is an http.Client for VictoriaMetrics whose calls are measured
// under caller — one name per call site (kobi, alerts, dashboards…).
func VMClient(caller string, timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: VMTransport(caller, nil)}
}

// VMTransport wraps base (http.DefaultTransport when nil) so every request to
// VictoriaMetrics is counted and timed.
func VMTransport(caller string, base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &vmTransport{caller: caller, base: base}
}

type vmTransport struct {
	caller string
	base   http.RoundTripper
}

func (t *vmTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	op := vmOp(req.URL.Path)
	resp, err := t.base.RoundTrip(req)
	end := time.Now()
	vmSeconds.WithLabelValues(op).Observe(end.Sub(start).Seconds())
	vmRequests.WithLabelValues(t.caller, op, vmResult(resp, err, ctxErrAt(req.Context(), end))).Inc()
	return resp, err
}

// ctxErrAt is the request context's error as of now — DeadlineExceeded once
// its deadline has passed, even when the context's own timer has not fired
// yet. http.Client enforces Timeout twice, with that deadline and with a timer
// of its own that cancels the request at the same instant; when the client's
// timer wins, the transport returns "request canceled" before the context
// knows it expired (11 runs in 300 under -race). A call that ended at its
// deadline timed out, whichever timer got there first.
func ctxErrAt(ctx context.Context, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d, ok := ctx.Deadline(); ok && !now.Before(d) {
		return context.DeadlineExceeded
	}
	return nil
}

func vmOp(path string) string {
	switch {
	case strings.HasSuffix(path, "/api/v1/query"):
		return "query"
	case strings.HasSuffix(path, "/api/v1/query_range"):
		return "query_range"
	case strings.HasSuffix(path, "/api/v1/series"), strings.HasSuffix(path, "/api/v1/labels"),
		strings.Contains(path, "/api/v1/label/"):
		return "metadata"
	case strings.Contains(path, "/api/v1/export"):
		return "export"
	case strings.Contains(path, "/api/v1/import"):
		return "import"
	case strings.HasSuffix(path, "/api/v1/write"):
		return "write"
	default:
		return "other"
	}
}

// vmResult classifies a call. ctxErr is the request context's error as of the
// call's end (ctxErrAt): when the client's Timeout fires, the transport reports
// "request canceled" about as often as "deadline exceeded", and only the
// context says which it was.
func vmResult(resp *http.Response, err, ctxErr error) string {
	if err != nil {
		var ne net.Error
		switch {
		case errors.Is(ctxErr, context.DeadlineExceeded):
			return "timeout"
		case errors.Is(ctxErr, context.Canceled), errors.Is(err, context.Canceled):
			return "canceled"
		case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
			return "timeout"
		default:
			return "error"
		}
	}
	switch {
	case resp.StatusCode >= 500:
		return "http_5xx"
	case resp.StatusCode >= 400:
		return "http_4xx"
	default:
		return "ok"
	}
}

// ─── WebSocket ──────────────────────────────────────────────────────────

var wsClientsOnce sync.Once

// WSClients publishes the number of connected browsers, read from the hub on
// every push. The process has one hub; a second call is ignored.
func WSClients(count func() int) {
	wsClientsOnce.Do(func() {
		prometheus.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "kubebolt_ws_clients",
			Help: "Browsers connected to the live-update WebSocket.",
		}, func() float64 { return float64(count()) }))
	})
}

// WSDropped counts an event the hub could not deliver.
func WSDropped(reason string) { wsDropped.WithLabelValues(reason).Inc() }

// ─── Background jobs ────────────────────────────────────────────────────

// Job is one of the API's periodic jobs. Each run is bracketed:
//
//	run := job.Start()
//	defer run.End()
//	…
//	run.OK() // where the job has done its work
//
// A run that never reaches OK counts as an error, so a job's own early returns
// on failure need no change.
type Job struct{ name string }

// NewJob declares a job and how often it is meant to run.
func NewJob(name string, interval time.Duration) *Job {
	jobInterval.WithLabelValues(name).Set(interval.Seconds())
	return &Job{name: name}
}

// Start begins a run. Nil-safe: a nil job measures nothing.
func (j *Job) Start() *Run {
	if j == nil {
		return nil
	}
	return &Run{job: j, start: time.Now()}
}

// Run is one execution of a Job.
type Run struct {
	job   *Job
	start time.Time
	ok    bool
}

// OK marks the run as successful.
func (r *Run) OK() {
	if r != nil {
		r.ok = true
	}
}

// End records the run: its result, its duration and, when it succeeded, the
// time of the success.
func (r *Run) End() {
	if r == nil {
		return
	}
	name := r.job.name
	jobDuration.WithLabelValues(name).Set(time.Since(r.start).Seconds())
	if r.ok {
		jobRuns.WithLabelValues(name, "ok").Inc()
		jobLastSuccess.WithLabelValues(name).SetToCurrentTime()
		return
	}
	jobRuns.WithLabelValues(name, "error").Inc()
}
