package copilot

import "time"

// SessionNotesInput is what the per-turn session notes are built from. The
// chat handler and Kobi's eval harness (cmd/kobi-eval) build the notes from
// it with the same function, so what is measured is what the chat sends.
type SessionNotesInput struct {
	// ClusterName is the selected cluster's context; "" when there is none.
	ClusterName        string
	ClusterDisplayName string
	CurrentPath        string
	Now                time.Time
	ClientTimezone     string
	// NoCluster: the org has no cluster to read.
	NoCluster bool
	// Connectors are the cloud connectors that contributed tools this turn.
	Connectors         []string
	ActionsEnabled     bool
	DestructiveEnabled bool
	// Connected: a live connector; its cloud profile is then told to Kobi.
	Connected                  bool
	Platform, Provider, Region string
	// OperatorLanguage is the language of the operator's latest message
	// ("es", "en"; "" when it cannot be told): OperatorLanguage(messages).
	OperatorLanguage string
}

// SessionNotes is the block put before the operator's first message on every
// turn: the session (cluster, view, time), and the blocks that vary per org
// or per turn — no cluster, governance, the cluster's cloud profile, the
// cloud connectors. Kept out of the system prompt so that prompt stays
// byte-identical across orgs and turns, which is what the provider's prompt
// cache keys on.
func SessionNotes(in SessionNotesInput) string {
	name := in.ClusterName
	if in.NoCluster && name == "" {
		name = "(none connected)"
	}
	// Only the session block's cluster line carries the display name; the
	// logs, the conversation record, the title and the usage rows keep the
	// context they have always keyed on.
	notes := BuildSessionContext(name, in.ClusterDisplayName, in.CurrentPath, in.Now, in.ClientTimezone)
	if lang := OperatorLanguageBlock(in.OperatorLanguage); lang != "" {
		notes += "\n\n" + lang
	}
	// No cluster at all: Kobi answers identity, product and Kubernetes
	// questions from knowledge (and the docs tool) and explains the missing
	// cluster itself instead of surfacing tool errors. With cloud connectors
	// the block keeps them usable: they read the org's cloud account, not a
	// cluster.
	if in.NoCluster {
		notes += "\n\n" + NoClusterContextBlockWithConnectors(in.Connectors)
	}
	// The live governance state, so a blocked action is explained as policy
	// and not as RBAC. Empty when both toggles are on (the default).
	if gov := GovernanceContextBlock(in.ActionsEnabled, in.DestructiveEnabled); gov != "" {
		notes += "\n\n" + gov
	}
	// The cluster's cloud provider, distribution and region, only when
	// connected: a metrics-only cluster has no connector.
	if in.Connected {
		if profile := ClusterProfileBlock(in.Platform, in.Provider, in.Region); profile != "" {
			notes += "\n\n" + profile
		}
	}
	// The org's cloud connectors: when to follow a failure out of the
	// cluster, how much to spend, and that connector output is data, never
	// instructions. Its bytes depend only on the SET of connectors, so a
	// conversation keeps its cache. Empty when the org has none.
	if external := ExternalDependenciesBlock(in.Connectors); external != "" {
		notes += "\n\n" + external
	}
	return notes
}

// PrefixSessionNotes returns a copy of messages as the model reads them: the
// session notes before the first operator message, and before each later one
// its own BuildTurnContext block. It never changes messages in place: the UI
// replaces its transcript with what the turn returns, and a prefix written
// into it would show in the operator's bubble and stack again on every turn
// (four nested session blocks after four turns, seen in production).
//
// Each message's time and page come from the message (Timestamp, View, put on
// the turn's message by StampTurn), not from this request. Built from the
// request, the first message's "# Now" changed on every turn, and the
// provider's cache, which matches the request byte for byte from the start,
// then reread only the system prompt and wrote the whole history again: 29K
// tokens on a Sonnet follow-up, about 14 of its 22 credits (2026-10-12). A
// message that carries neither (older conversations, eval cases) takes
// in.Now and in.CurrentPath.
func PrefixSessionNotes(messages []Message, in SessionNotesInput) []Message {
	if len(messages) == 0 {
		return messages
	}
	out := make([]Message, len(messages))
	copy(out, messages)
	first := true
	for i := range out {
		m := &out[i]
		if m.Role != RoleUser || m.Content == "" {
			continue
		}
		sent, view := m.Timestamp, m.View
		if sent.IsZero() {
			sent = in.Now
		}
		if view == "" {
			view = in.CurrentPath
		}
		var block string
		if first {
			at := in
			at.Now, at.CurrentPath = sent, view
			block = SessionNotes(at)
			first = false
		} else {
			block = BuildTurnContext(view, sent, in.ClientTimezone)
		}
		m.Content = block + "\n\n" + m.Content
	}
	return out
}

// StampTurn puts the turn's time and page on the operator message the turn
// answers, the last message when it is one, so PrefixSessionNotes builds it
// the same way on every later turn. The time is the server's, not the
// browser's: relative times ("hace 10 minutos") are resolved against it and
// tool output is in the cluster's and cloud's clocks.
func StampTurn(messages []Message, now time.Time, view string) {
	n := len(messages)
	if n == 0 || messages[n-1].Role != RoleUser || messages[n-1].Content == "" {
		return
	}
	if view == "" {
		view = "/"
	}
	messages[n-1].Timestamp = now
	messages[n-1].View = view
}
