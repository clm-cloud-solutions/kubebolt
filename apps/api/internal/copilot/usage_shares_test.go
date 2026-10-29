package copilot

import (
	"math"
	"reflect"
	"testing"
)

// A lab session of 2026-10-28: five rounds on Haiku 5.5, then
// mcp__aws__call moved it to Sonnet 5.5 for two more. Priced whole at Sonnet it
// read $0.456; what it cost is each model's share at its own price, ~$0.218.
func escalatedLabSession() SessionRecord {
	return SessionRecord{
		Provider: "anthropic",
		Model:    "claude-sonnet-5-5",
		Rounds:   7,
		Usage: Usage{
			InputTokens: 16, OutputTokens: 6748, ThinkingTokens: 3190,
			CacheReadTokens: 256857, CacheCreationTokens: 122486, CacheCreation1hTokens: 37738,
		},
		Escalation: &EscalationRecord{
			Provider: "anthropic", Model: "claude-sonnet-5-5",
			FromProvider: "anthropic", FromModel: "claude-haiku-5-5",
			Reason: "mcp__aws__call", Round: 5,
			Usage: Usage{
				InputTokens: 4, OutputTokens: 3325, ThinkingTokens: 1341,
				CacheReadTokens: 61670, CacheCreationTokens: 65871,
			},
		},
	}
}

func TestModelShares_EscalatedSessionIsPricedAtEachModel(t *testing.T) {
	rec := escalatedLabSession()
	haiku := Usage{
		InputTokens: 12, OutputTokens: 3423, ThinkingTokens: 1849,
		CacheReadTokens: 195187, CacheCreationTokens: 56615, CacheCreation1hTokens: 37738,
	}
	want := []ModelShare{
		{Provider: "anthropic", Model: "claude-haiku-5-5", Usage: haiku},
		{Provider: "anthropic", Model: "claude-sonnet-5-5", Usage: rec.Escalation.Usage},
	}
	if got := rec.ModelShares(); !reflect.DeepEqual(got, want) {
		t.Fatalf("ModelShares() = %+v, want %+v", got, want)
	}

	price := func(s ModelShare) float64 {
		p, ok := PricingFor(s.Provider, s.Model)
		if !ok {
			t.Fatalf("no price for %s", s.Model)
		}
		return EstimateUSD(s.Usage, p)
	}
	usd, known := rec.EstimatedUSD()
	if !known || math.Abs(usd-(price(want[0])+price(want[1]))) > 1e-12 {
		t.Errorf("EstimatedUSD() = %v (known %v), want %v", usd, known, price(want[0])+price(want[1]))
	}
	whole := price(ModelShare{Provider: rec.Provider, Model: rec.Model, Usage: rec.Usage})
	t.Logf("each model at its price: $%.4f; whole at Sonnet: $%.4f", usd, whole)
	if usd >= whole/2 {
		t.Errorf("EstimatedUSD() = %v: Haiku's five rounds are still priced near Sonnet's (%v whole)", usd, whole)
	}
}

func TestModelShares_OneModelOnly(t *testing.T) {
	u := Usage{InputTokens: 100, OutputTokens: 50, CacheReadTokens: 1000}
	cases := map[string]struct {
		rec  SessionRecord
		want ModelShare
	}{
		"not escalated": {
			rec:  SessionRecord{Provider: "anthropic", Model: "claude-haiku-5-5", Usage: u},
			want: ModelShare{Provider: "anthropic", Model: "claude-haiku-5-5", Usage: u},
		},
		// A turn of a conversation that had already left the cluster runs on
		// the escalation model from its first round.
		"escalated from the start": {
			rec: SessionRecord{Provider: "anthropic", Model: "claude-sonnet-5-5", Usage: u,
				Escalation: &EscalationRecord{Provider: "anthropic", Model: "claude-sonnet-5-5",
					FromProvider: "anthropic", FromModel: "claude-haiku-5-5", Reason: "history", Usage: u}},
			want: ModelShare{Provider: "anthropic", Model: "claude-sonnet-5-5", Usage: u},
		},
		// The call that moved it came in the last round: the escalation model
		// never answered, and nothing is priced at it.
		"escalated after its last call": {
			rec: SessionRecord{Provider: "anthropic", Model: "claude-sonnet-5-5", Usage: u, Reason: "max_rounds",
				Escalation: &EscalationRecord{Provider: "anthropic", Model: "claude-sonnet-5-5",
					FromProvider: "anthropic", FromModel: "claude-haiku-5-5", Reason: "mcp__aws__call", Round: 12}},
			want: ModelShare{Provider: "anthropic", Model: "claude-haiku-5-5", Usage: u},
		},
		// Written before the record kept the model it moved from: it cannot be
		// split, and stays whole on the model it ended on.
		"older record": {
			rec: SessionRecord{Provider: "anthropic", Model: "claude-sonnet-5-5", Usage: u,
				Escalation: &EscalationRecord{Provider: "anthropic", Model: "claude-sonnet-5-5",
					Reason: "mcp__aws__call", Round: 3, Usage: Usage{InputTokens: 40, OutputTokens: 20}}},
			want: ModelShare{Provider: "anthropic", Model: "claude-sonnet-5-5", Usage: u},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := tc.rec.ModelShares()
			if !reflect.DeepEqual(got, []ModelShare{tc.want}) {
				t.Errorf("ModelShares() = %+v, want [%+v]", got, tc.want)
			}
		})
	}
}
