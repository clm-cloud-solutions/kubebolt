package api

import (
	"context"
	"net/http"
	"regexp"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/fieldmanager"
)

// Who made a mutation (finding #69). A request can say it comes from Kobi
// (X-KubeBolt-Action-Source), but the actor is decided here, from the
// principal, never from the header alone:
//
//   - Autopilot only with a service token (kbs_) that says so. Only platform
//     admins mint those, and Autopilot calls with one.
//   - Kobi Copilot only from a person's session, and only when the proposal's
//     conversation is the caller's own. Copilot's proposals execute in the
//     browser, with the session of the person who approved them; no API token
//     speaks for Kobi.
//   - Everyone else is a person: "ui" in a session, "api" with a token.
//
// The actor names the field manager of every write the request makes
// (fieldmanager.For) and the source of its audit record. A claim that was not
// believed stays in the record as ClaimedSource.

const (
	actionSourceHeader      = "X-KubeBolt-Action-Source"
	conversationIDHeader    = "X-KubeBolt-Conversation-Id"
	autopilotRunHeader      = "X-KubeBolt-Autopilot-Run"
	autopilotIncidentHeader = "X-KubeBolt-Autopilot-Incident"

	sourceUI        = "ui"
	sourceAPI       = "api"
	sourceCopilot   = "copilot_proposal"
	sourceAutopilot = "autopilot"
)

type actionActor struct {
	Actor  fieldmanager.Actor
	Source string
	// ClaimedSource is what the request said it was, when that was not
	// believed: a token claiming Kobi, a Copilot claim whose conversation is
	// not the caller's (or not saved yet).
	ClaimedSource     string
	ConversationID    string
	AutopilotRun      string
	AutopilotIncident string
}

type actionActorKey struct{}

// withActionActor resolves the request's actor once and stamps it on the
// context, where fieldmanager.For and auditMutation read it. Mounted after
// authentication and tenant resolution.
func (h *handlers) withActionActor(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := resolveActionActor(r, h.ownsConversation)
		ctx := fieldmanager.WithActor(r.Context(), a.Actor)
		ctx = context.WithValue(ctx, actionActorKey{}, a)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// actionActorOf is the actor withActionActor resolved. A route mounted
// outside that group (the EE Autopilot proxy) resolves it here, with no
// conversation to check: a Copilot claim is not believed there.
func actionActorOf(r *http.Request) actionActor {
	if a, ok := r.Context().Value(actionActorKey{}).(actionActor); ok {
		return a
	}
	return resolveActionActor(r, nil)
}

func resolveActionActor(r *http.Request, owns func(r *http.Request, conversationID string) bool) actionActor {
	claimed := r.Header.Get(actionSourceHeader)
	var a actionActor
	if p := auth.ContextAPIPrincipal(r); p != nil {
		if p.Type == auth.TokenTypeService && claimed == sourceAutopilot {
			return actionActor{
				Actor:             fieldmanager.ActorAutopilot,
				Source:            sourceAutopilot,
				AutopilotRun:      autopilotRef(r.Header.Get(autopilotRunHeader)),
				AutopilotIncident: autopilotRef(r.Header.Get(autopilotIncidentHeader)),
			}
		}
		a = actionActor{Source: sourceAPI}
	} else {
		a = actionActor{Source: sourceUI}
		if claimed == sourceCopilot && owns != nil {
			if id := r.Header.Get(conversationIDHeader); id != "" && owns(r, id) {
				return actionActor{Actor: fieldmanager.ActorKobi, Source: sourceCopilot, ConversationID: id}
			}
		}
	}
	if claimed != "" && claimed != a.Source {
		a.ClaimedSource = claimed
	}
	return a
}

// ownsConversation reports whether conversationID is one of the caller's own
// Kobi conversations, read the way GET /copilot/conversations/{id} reads it.
// Without conversation persistence there is nothing to check against, and a
// claim from a person's session is believed, as it was before #69.
func (h *handlers) ownsConversation(r *http.Request, conversationID string) bool {
	if h.copilotConversations == nil {
		return true
	}
	_, ok, err := h.copilotConversations.Get(auth.ContextTenantID(r), conversationUserID(r), conversationID)
	return err == nil && ok
}

// autopilotRef keeps an Autopilot run or incident id only if it looks like
// one: it is copied into the audit trail.
var autopilotRefPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func autopilotRef(v string) string {
	if autopilotRefPattern.MatchString(v) {
		return v
	}
	return ""
}
