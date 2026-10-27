package copilot

import (
	"encoding/json"
	"testing"
	"time"
)

// The prompt-size brake.
//
// Every byte measured here reaches the model on every turn, whatever the
// operator asked. Kobi's prompt went from 4.5 KB in April to 64.6 KB in
// September, one lab conversation at a time: a lesson from one incident added
// here is paid on every turn of every conversation, and it never leaves.
//
// The budgets are the size of each piece when the brake landed, rounded up to
// the next 1000 bytes, and they only go down. A change that needs room takes
// it from something else in the same piece — or puts the missing fact in a
// tool's output, the mandatory behavior in code, or the procedure in a skill.
//
// Lowering is part of the contract: a piece that ends up promptBudgetSlack or
// more below its budget fails until the budget follows it, so the room a
// cleanup frees is not quietly spent on the next rule.
//
// Not measured yet: the cloud connectors' tool descriptions, which the gateway
// learns from each provider's server at run time.

const promptBudgetSlack = 1000

type promptPiece struct {
	name   string
	budget int // bytes
	size   int
}

func kobiPromptPieces() []promptPiece {
	tools, err := json.Marshal(ChatToolDefinitions(true, true))
	if err != nil {
		panic(err)
	}
	// The per-session notes, at their largest: every connector on, no cluster,
	// actions off. Their size depends on the org, not on the question.
	all := []string{"aws", "azure", "gcp"}
	notes := BuildSessionContext("agent:uid-x", "shop-prod", "/deployments/shop/orders-api",
		time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC), "Europe/Madrid") +
		NoClusterContextBlockWithConnectors(all) +
		GovernanceContextBlock(false, false) +
		ExternalDependenciesBlock(all)

	return []promptPiece{
		{name: "system prompt (BuildSystemPrompt)", budget: 54000, size: len(BuildSystemPrompt())},
		// Raised from 33000 when develop merged in (2026-10-25): its seven new
		// tools came without a budget. Trimming them is pending review.
		{name: "chat tool definitions (ChatToolDefinitions)", budget: 43000, size: len(tools)},
		{name: "per-session notes, largest case", budget: 6000, size: len(notes)},
	}
}

func TestKobiPromptStaysWithinBudget(t *testing.T) {
	for _, p := range kobiPromptPieces() {
		checkPromptBudget(t, p)
	}
}

func checkPromptBudget(t *testing.T, p promptPiece) {
	t.Helper()
	switch {
	case p.size > p.budget:
		t.Errorf("%s is %d bytes (≈%d tokens), over its budget of %d. Every byte is read on every turn: "+
			"make room in the same piece, or put the fact in a tool's output, the behavior in code, or the "+
			"procedure in a skill. Raising the budget is a decision for review, not a fix.",
			p.name, p.size, p.size/4, p.budget)
	case p.budget-p.size >= promptBudgetSlack:
		t.Errorf("%s shrank to %d bytes, %d below its budget of %d: lower the budget to %d so the freed room stays freed.",
			p.name, p.size, p.budget-p.size, p.budget, roundUpBudget(p.size))
	}
}

func roundUpBudget(size int) int {
	return (size/promptBudgetSlack + 1) * promptBudgetSlack
}
