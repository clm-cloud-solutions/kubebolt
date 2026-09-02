package api

import (
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/kubebolt/kubebolt/apps/api/internal/audit"
	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
)

// Access auditing — "someone reached into a running workload".
//
// A shell, a tunnel or a file read changes nothing, so none of them fit the
// mutation vocabulary; but they are the events an incident review cares about
// most, because they are how data leaves. They get their own class (see
// audit.ClassAccess) in the same trail, so one query answers "what happened on
// this account" while the admin view can still separate them — access is far
// more numerous and would otherwise bury the mutations.
//
// What is recorded is the SESSION, never its content. No terminal I/O, no
// tunnelled bytes, no file contents. The terminal case is the sharp one: it is
// where operators type passwords, so capturing the stream would turn the audit
// trail into the longest-lived and least-guarded copy of every credential that
// ever passed through a shell. Paths and sizes are metadata and safe; bytes are
// not, at any volume.

// accessActor is who performed the access.
//
// It is passed explicitly rather than read from the request inside the helper
// because the terminal does not authenticate like everything else: handleExec
// validates a token from the query string (a browser cannot set an
// Authorization header when opening a WebSocket), so the request context has no
// claims and auth.ContextTenantID would resolve every terminal session to the
// default org. Attributing one tenant's shell to another is worse than not
// recording it.
type accessActor struct {
	UserID   string
	Username string
	Role     string
	TenantID string
}

// actorFromRequest reads the actor off a normally-authenticated request.
func actorFromRequest(r *http.Request) accessActor {
	a := accessActor{
		Role:     string(auth.ContextRole(r)),
		TenantID: auth.ContextTenantID(r),
	}
	if claims := auth.ContextClaims(r); claims != nil {
		a.UserID = claims.UserID
		a.Username = claims.Username
	}
	return a
}

// actorFromClaims builds the actor from WebSocket-token claims (the terminal).
func actorFromClaims(c *auth.Claims, fallbackTenant string) accessActor {
	if c == nil {
		return accessActor{TenantID: fallbackTenant}
	}
	tenant := c.TenantID
	if tenant == "" {
		tenant = fallbackTenant
	}
	return accessActor{UserID: c.UserID, Username: c.Username, Role: string(c.Role), TenantID: tenant}
}

func (a accessActor) record(action, targetType, namespace, name string, params map[string]any) *audit.Record {
	return &audit.Record{
		ID:              uuid.New().String(),
		Timestamp:       time.Now().UTC(),
		TenantID:        a.TenantID,
		Class:           audit.ClassAccess,
		Source:          "ui",
		UserID:          a.UserID,
		Username:        a.Username,
		Role:            a.Role,
		ClusterID:       audit.ClusterID(),
		Action:          action,
		TargetType:      targetType,
		TargetNamespace: namespace,
		TargetName:      name,
		Params:          params,
		Result:          "success",
	}
}

// auditAccess records a one-shot access: a file read, a download. `params` must
// describe the access (path, size) and never carry what was accessed.
func auditAccess(actor accessActor, action, targetType, namespace, name string, params map[string]any, err error) {
	if !audit.Enabled() {
		return
	}
	rec := actor.record(action, targetType, namespace, name, params)
	if err != nil {
		rec.Result = "error"
		rec.Error = err.Error()
	}
	audit.Emit(rec)
}

// auditAccessSession records the OPENING of a long-lived access — a terminal, a
// port-forward — and returns the function that records its close.
//
// Two records rather than one, deliberately. Writing only on close means a
// session that dies because the API crashed or the pod vanished leaves no trace
// at all, and that is precisely the session a reviewer wants to find. With a
// pair, an open with no close is itself the signal.
//
// The returned closer records exactly one close no matter how many times, or
// from how many goroutines, it is called. That is a requirement, not a
// convenience: a port-forward can be torn down by the HTTP delete handler and
// by its own forwarding goroutine at the same instant, and the terminal calls
// it both explicitly and from a defer.
func auditAccessSession(actor accessActor, action, targetType, namespace, name string, params map[string]any) func(reason string, extra map[string]any) {
	if !audit.Enabled() {
		return func(string, map[string]any) {}
	}
	sessionID := uuid.New().String()
	openedAt := time.Now()

	openParams := map[string]any{"sessionId": sessionID}
	for k, v := range params {
		openParams[k] = v
	}
	audit.Emit(actor.record(action+"_open", targetType, namespace, name, openParams))

	var once sync.Once
	return func(reason string, extra map[string]any) {
		once.Do(func() {
			emitClose(actor, action, targetType, namespace, name, params, extra, sessionID, openedAt, reason)
		})
	}
}

func emitClose(actor accessActor, action, targetType, namespace, name string,
	params, extra map[string]any, sessionID string, openedAt time.Time, reason string) {
	closeParams := map[string]any{
		"sessionId":  sessionID,
		"durationMs": time.Since(openedAt).Milliseconds(),
		"reason":     reason,
	}
	for k, v := range params {
		closeParams[k] = v
	}
	for k, v := range extra {
		closeParams[k] = v
	}
	audit.Emit(actor.record(action+"_close", targetType, namespace, name, closeParams))
}
