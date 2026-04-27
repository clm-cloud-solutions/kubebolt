package copilot

// ActionProposal is the structured payload returned by `propose_*` tools.
// The LLM never executes mutations — it builds a proposal that the frontend
// renders as a card with an explicit Execute button. Only the user's click
// drives the actual mutation, which goes through the existing mutation
// endpoints under the user's RBAC role.
//
// The shape is consumed by the frontend by parsing ToolResult.Content as
// JSON and looking for `kind == "action_proposal"`. Schema is versioned so
// it can evolve without breaking older clients.
type ActionProposal struct {
	Kind       string                 `json:"kind"`       // always "action_proposal"
	Version    int                    `json:"version"`    // schema version (PoC: 1)
	Action     string                 `json:"action"`     // e.g. "restart_workload", "scale_workload"
	Target     ProposalTarget         `json:"target"`     // resource the action applies to
	Params     map[string]interface{} `json:"params"`     // action-specific args (e.g. {"replicas": 3})
	Summary    string                 `json:"summary"`    // short label, used as card title and button text
	Rationale  string                 `json:"rationale"`  // why the LLM is proposing this
	Risk       string                 `json:"risk"`       // "low" | "medium" | "high"
	Reversible bool                   `json:"reversible"` // true if trivially reversible (restart) vs not (delete)
}

// ProposalTarget identifies the Kubernetes resource the proposal acts on.
type ProposalTarget struct {
	Type      string `json:"type"`      // e.g. "deployments"
	Namespace string `json:"namespace"` // empty for cluster-scoped
	Name      string `json:"name"`
}

// newProposal returns a proposal pre-populated with kind/version so callers
// in the executor only fill in the action-specific fields.
func newProposal(action string) ActionProposal {
	return ActionProposal{
		Kind:    "action_proposal",
		Version: 1,
		Action:  action,
		Params:  map[string]interface{}{},
	}
}

// resolveRisk normalizes the risk value coming from the LLM-provided arg.
// If the arg is empty or not one of the allowed values, the per-action
// default is used. This keeps the badge and the LLM's prose in sync (the
// LLM picks the level when its situational analysis warrants it) while
// guaranteeing a sensible value when the LLM omits it.
func resolveRisk(provided, fallback string) string {
	switch provided {
	case "low", "medium", "high":
		return provided
	default:
		return fallback
	}
}
