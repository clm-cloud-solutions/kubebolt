package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/kubectl/pkg/drain"
)

// Drain — `kubectl drain <node>` made ergonomic and observable.
// This file contains:
//
//   - handleDrain        — POST starts a drain. Body is the
//                          drainRequest config; response is an SSE
//                          stream of pod-evicted events terminating
//                          in drain-complete or drain-error.
//   - handleDrainSession — GET re-attaches to an in-flight drain.
//                          Replays buffered events, then continues
//                          live until the drain finishes. This is
//                          how the UI reconnects after a browser
//                          tab close or page refresh.
//   - handleDrainCancel  — DELETE aborts the drain. Pods already
//                          submitted for eviction continue (kubelet
//                          finishes their grace period); new
//                          evictions stop.
//
// Sessions live in handlers.drainManager (created in router.go,
// CancelAll() invoked on cluster switch in handlers.go).

type drainRequest struct {
	GracePeriodSeconds int   `json:"gracePeriodSeconds"`
	TimeoutSeconds     int   `json:"timeoutSeconds"`
	DeleteEmptyDirData *bool `json:"deleteEmptyDirData"`
	IgnoreDaemonsets   *bool `json:"ignoreDaemonsets"`
	Force              bool  `json:"force"`
	DisableEviction    bool  `json:"disableEviction"`
}

// drainPodOutcome is one row of the per-pod outcome list. Carried
// in the pod-evicted SSE event AND aggregated in the final
// drain-complete event.
type drainPodOutcome struct {
	Pod       string `json:"pod"`
	Namespace string `json:"namespace"`
	Status    string `json:"status"` // "evicted" | "deleted" | "error"
	Error     string `json:"error,omitempty"`
}

// drainCompletePayload is the data field of the final
// drain-complete event. Mirrors the synchronous response from
// Cut 2 so a UI built against the SSE shape gets the same totals
// it would have gotten from the synchronous endpoint.
type drainCompletePayload struct {
	Status     string `json:"status"` // "drained" | "drain-failed" | "drain-partial" | "cancelled"
	Evicted    int    `json:"evicted"`
	DurationMs int64  `json:"durationMs"`
	Error      string `json:"error,omitempty"`
}

// boolDefault returns *ptr if non-nil, else def. Used so that
// `deleteEmptyDirData=false` in the body is respected, but omitting
// the key falls back to the spec's default of true.
func boolDefault(ptr *bool, def bool) bool {
	if ptr == nil {
		return def
	}
	return *ptr
}

func (h *handlers) handleDrain(w http.ResponseWriter, r *http.Request) {
	resourceType := chi.URLParam(r, "type")
	name := chi.URLParam(r, "name")

	if resourceType != "nodes" {
		respondError(w, http.StatusBadRequest, fmt.Sprintf("cannot drain %s — only nodes can be drained", resourceType))
		return
	}

	conn := h.manager.Connector(r.Context())
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}

	var req drainRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
	}
	gracePeriod := req.GracePeriodSeconds
	if gracePeriod <= 0 {
		gracePeriod = 60
	}
	timeoutSec := req.TimeoutSeconds
	if timeoutSec <= 0 {
		timeoutSec = 300
	}
	deleteEmptyDir := boolDefault(req.DeleteEmptyDirData, true)
	ignoreDS := boolDefault(req.IgnoreDaemonsets, true)

	params := map[string]any{
		"gracePeriodSeconds": gracePeriod,
		"timeoutSeconds":     timeoutSec,
		"deleteEmptyDirData": deleteEmptyDir,
		"ignoreDaemonsets":   ignoreDS,
		"force":              req.Force,
		"disableEviction":    req.DisableEviction,
	}

	// Acquire a fresh session. If one is already in-flight for this
	// node, we don't want a second drain firing on the same target —
	// kick the operator to GET to re-attach to the running one.
	session, drainCtx, fresh := h.drainManager.Start(name, params)
	if !fresh {
		respondError(w, http.StatusConflict, fmt.Sprintf("a drain is already in progress on node %q; GET this URL to attach", name))
		return
	}

	clientset := conn.Clientset()
	node, err := clientset.CoreV1().Nodes().Get(drainCtx, name, metav1.GetOptions{})
	if err != nil {
		// Failed before drain even started. Emit one error event so
		// SSE consumers see a clean termination rather than an empty
		// stream, then finalize.
		session.emit(drainEvent{Name: "drain-error", Data: map[string]any{"error": err.Error()}})
		session.finalize()
		auditMutation(r, "drain", resourceType, "", name, params, err)
		respondMutationError(w, err)
		return
	}

	// Spawn the drain goroutine. It runs on the session's drainCtx
	// — NOT on r.Context() — so a browser disconnect / SSE consumer
	// going away does NOT kill the drain. Same goroutine emits all
	// events; the SSE writer below is just one subscriber.
	go runDrainGoroutine(session, drainCtx, clientset, node,
		gracePeriod, timeoutSec, deleteEmptyDir, ignoreDS, req.Force, req.DisableEviction)

	// Audit fires now (on the request, with the operator's identity)
	// even though the drain runs async.
	auditMutation(r, "drain", resourceType, "", name, params, nil)

	// Stream the session's events to this consumer. If they
	// disconnect mid-drain, the goroutine and session keep running.
	streamDrainSession(w, r, session)
}

// runDrainGoroutine performs the actual drain. Long-running; emits
// events to the session as it progresses; finalizes on exit (which
// closes all subscriber channels and prevents further emissions).
func runDrainGoroutine(
	session *drainSession,
	ctx context.Context,
	clientset kubernetes.Interface,
	node *corev1.Node,
	gracePeriod, timeoutSec int,
	deleteEmptyDir, ignoreDS, force, disableEviction bool,
) {
	defer session.finalize()

	startedAt := time.Now()
	var evicted int32

	helper := &drain.Helper{
		Ctx:                 ctx,
		Client:              clientset,
		Force:               force,
		GracePeriodSeconds:  gracePeriod,
		IgnoreAllDaemonSets: ignoreDS,
		DeleteEmptyDirData:  deleteEmptyDir,
		DisableEviction:     disableEviction,
		Timeout:             time.Duration(timeoutSec) * time.Second,
		Out:                 &drainLogWriter{node: node.Name, stream: "out"},
		ErrOut:              &drainLogWriter{node: node.Name, stream: "err"},
		OnPodDeletedOrEvicted: func(pod *corev1.Pod, usingEviction bool) {
			status := "evicted"
			if !usingEviction {
				status = "deleted"
			}
			atomic.AddInt32(&evicted, 1)
			session.emit(drainEvent{
				Name: "pod-evicted",
				Data: drainPodOutcome{
					Pod:       pod.Name,
					Namespace: pod.Namespace,
					Status:    status,
				},
			})
		},
	}

	// Phase 1 — cordon. Idempotent; calling with desired=true on
	// an already-cordoned node is a no-op. We run unconditionally
	// so a drain operation is self-contained.
	if err := drain.RunCordonOrUncordon(helper, node, true); err != nil {
		session.emit(drainEvent{Name: "drain-error", Data: map[string]any{"phase": "cordon", "error": err.Error()}})
		session.emit(drainEvent{
			Name: "drain-complete",
			Data: drainCompletePayload{
				Status:     "drain-failed",
				Evicted:    int(atomic.LoadInt32(&evicted)),
				DurationMs: time.Since(startedAt).Milliseconds(),
				Error:      err.Error(),
			},
		})
		return
	}

	// Phase 2 — drain. RunNodeDrain returns the first error (or nil
	// on success). Per-pod outcomes flow through the callback above
	// regardless. Cancellation through ctx fires while RunNodeDrain
	// is mid-loop and surfaces as a wrapped context.Canceled error.
	drainErr := drain.RunNodeDrain(helper, node.Name)
	durationMs := time.Since(startedAt).Milliseconds()

	status := "drained"
	errStr := ""
	if drainErr != nil {
		errStr = drainErr.Error()
		switch {
		case ctx.Err() != nil:
			// Cancelled by handleDrainCancel or by cluster switch.
			status = "cancelled"
		case atomic.LoadInt32(&evicted) > 0:
			status = "drain-partial"
		default:
			status = "drain-failed"
		}
	}

	session.emit(drainEvent{
		Name: "drain-complete",
		Data: drainCompletePayload{
			Status:     status,
			Evicted:    int(atomic.LoadInt32(&evicted)),
			DurationMs: durationMs,
			Error:      errStr,
		},
	})
}

// streamDrainSession writes the session's event log + live tail to
// the HTTP response as Server-Sent Events. Returns when the session
// finishes, when r.Context() cancels (client disconnect), or when
// the writer errors.
func streamDrainSession(w http.ResponseWriter, r *http.Request, session *drainSession) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		respondError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	ch, replay, unsub := session.subscribe()
	defer unsub()

	// Replay buffered events first so a reconnecting client sees
	// the full history before live events.
	for _, ev := range replay {
		if err := writeDrainSSEEvent(w, ev); err != nil {
			return
		}
	}
	flusher.Flush()

	// Heartbeat ticker keeps proxies / load balancers from idling
	// the connection out during long stretches between pod
	// evictions. SSE comments (lines starting with `:`) are valid
	// keep-alives and ignored by EventSource clients.
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case ev, ok := <-ch:
			if !ok {
				// Channel closed → session finalized. Final flush
				// to make sure the last event lands, then return.
				flusher.Flush()
				return
			}
			if err := writeDrainSSEEvent(w, ev); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func writeDrainSSEEvent(w http.ResponseWriter, ev drainEvent) error {
	data, err := json.Marshal(ev.Data)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Name, data)
	return err
}

// handleDrainSession (GET) re-attaches to an IN-FLIGHT drain.
// Finished sessions return 404 even though the manager may still
// hold them in memory for a bit — they're not "active drains the
// operator can attach to and watch". Without this filter the
// modal's open-time probe re-hops into the completed view of the
// PREVIOUS drain instead of going to the configure form, which
// looked like the modal "remembered" the old run after a
// successful drain + uncordon + drain-again sequence.
//
// The promise the spec makes is "drain survives browser
// disconnect; UI re-attaches on return". That's about RUNNING
// drains, not historic ones.
func (h *handlers) handleDrainSession(w http.ResponseWriter, r *http.Request) {
	resourceType := chi.URLParam(r, "type")
	name := chi.URLParam(r, "name")
	if resourceType != "nodes" {
		respondError(w, http.StatusBadRequest, fmt.Sprintf("cannot get drain status for %s", resourceType))
		return
	}
	session, ok := h.drainManager.Get(name)
	if !ok || session.isFinished() {
		respondError(w, http.StatusNotFound, "no active drain session for this node")
		return
	}
	streamDrainSession(w, r, session)
}

// handleDrainCancel (DELETE) aborts an in-flight drain. The cancel
// signal flows through the drain context to drain.Helper, which
// stops queueing new evictions. Pods that the kubelet has already
// accepted for eviction continue terminating per their grace period
// — those evictions are NOT undone. The runDrainGoroutine emits a
// drain-complete event with status="cancelled" and finalizes.
func (h *handlers) handleDrainCancel(w http.ResponseWriter, r *http.Request) {
	resourceType := chi.URLParam(r, "type")
	name := chi.URLParam(r, "name")
	if resourceType != "nodes" {
		respondError(w, http.StatusBadRequest, fmt.Sprintf("cannot cancel drain for %s", resourceType))
		return
	}
	if !h.drainManager.Cancel(name) {
		respondError(w, http.StatusNotFound, "no active drain session for this node")
		return
	}
	auditMutation(r, "drain_cancel", resourceType, "", name, nil, nil)
	respondJSON(w, http.StatusOK, map[string]string{"status": "cancelling", "node": name})
}

// drainLogWriter pipes drain.Helper's free-form Out/ErrOut messages
// into structured slog. Without this they go to /dev/null when
// passed nil. Tagged with the node name so concurrent drains don't
// bleed into each other.
type drainLogWriter struct {
	node   string
	stream string // "out" or "err"
}

func (w *drainLogWriter) Write(p []byte) (int, error) {
	msg := string(bytes.TrimRight(p, "\n"))
	if msg == "" {
		return len(p), nil
	}
	level := slog.LevelInfo
	if w.stream == "err" {
		level = slog.LevelWarn
	}
	slog.Log(context.Background(), level, "drain progress",
		slog.String("node", w.node),
		slog.String("stream", w.stream),
		slog.String("msg", msg),
	)
	return len(p), nil
}
