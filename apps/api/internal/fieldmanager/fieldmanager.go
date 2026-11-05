// Package fieldmanager names KubeBolt in the clusters it writes to.
//
// Kubernetes records who wrote each field of an object (metadata.managedFields)
// under the field manager the write carries: kubectl writes as kubectl-rollout
// or kubectl-client-side-apply, Argo CD and Helm under their own names. A write
// that names none is recorded under the client's binary name, and KubeBolt's
// came out as "server" (db-role-password-rotated, 2026-10-12): Kobi, reading
// who restarted a Deployment, could not tell it was KubeBolt. Every create,
// update and patch KubeBolt sends names a field manager; fieldmanager_test
// fails the build on one that does not.
//
// The name also says which of KubeBolt's actors made the write (finding #69):
// a person, Kobi Copilot (a proposal a person approved) or Kobi Autopilot. The
// API resolves the actor once per request, from the principal and never from a
// header alone, and stamps it on the request's context; a write passes that
// context to For. Who the person was, and which conversation or incident, is in
// the audit trail.
package fieldmanager

import "context"

// Name is the field manager of a write a person made through KubeBolt, and of
// KubeBolt's own writes (boot, integrations, access reviews).
const Name = "kubebolt"

// The field managers of Kobi's writes.
const (
	// NameKobi is a Kobi Copilot proposal a person approved and executed.
	NameKobi = "kubebolt-kobi"
	// NameAutopilot is Kobi Autopilot acting on an incident.
	NameAutopilot = "kubebolt-autopilot"
)

// Actor is who, inside KubeBolt, made a write.
type Actor string

const (
	// ActorPerson is a person, in the UI or through an API token. The zero
	// value reads as a person: a context nobody stamped is not Kobi's.
	ActorPerson Actor = ""
	// ActorKobi is Kobi Copilot: a proposal from the caller's own
	// conversation, approved and executed by them.
	ActorKobi Actor = "kobi"
	// ActorAutopilot is Kobi Autopilot, calling with its service token.
	ActorAutopilot Actor = "autopilot"
)

type actorKey struct{}

// WithActor returns ctx carrying the actor of the writes made with it.
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, a)
}

// ActorFrom returns the actor ctx carries; ActorPerson when none.
func ActorFrom(ctx context.Context) Actor {
	if ctx == nil {
		return ActorPerson
	}
	a, _ := ctx.Value(actorKey{}).(Actor)
	return a
}

// For is the field manager of a write made with ctx.
func For(ctx context.Context) string {
	switch ActorFrom(ctx) {
	case ActorKobi:
		return NameKobi
	case ActorAutopilot:
		return NameAutopilot
	default:
		return Name
	}
}
