package copilot

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

// The fallback does not start a fresh conversation. It picks up the round loop
// mid-flight, with the transcript the PRIMARY produced — its tool calls, its
// tool-call ids, its partial reasoning — and is handed the same tool set. So
// "can I keep a fallback on a different provider?" is first a wire question:
// does a transcript authored by provider A survive serialization to provider
// B's format without losing a turn, orphaning a tool call, or silently
// dropping an instruction?
//
// These tests answer it deterministically, with no API key and no network, so
// the answer holds in CI. What they cannot answer is whether the fallback
// model REASONS as well — that is the live lab in fallback_lab_test.go.

// productionShapedTranscript mirrors the real grind captured in production on
// 2026-09-16 (conversation b52bbdcf): several rounds, parallel tool calls, a
// tool-calls-only assistant turn with no prose, and multi-result user turns.
// A synthetic two-message transcript would not exercise any of the shapes that
// actually differ between the two wire formats.
func productionShapedTranscript() []Message {
	return []Message{
		{Role: RoleUser, Content: "¿por qué se llenó el disco del nodo?"},
		{Role: RoleAssistant, Content: "Voy a mirar los eventos y los nodos.", ToolCalls: []ToolCall{
			{ID: "toolu_01AbcXYZ", Name: "get_events", Input: json.RawMessage(`{"namespace":"kube-system"}`)},
			{ID: "toolu_01DefUVW", Name: "list_resources", Input: json.RawMessage(`{"type":"nodes"}`)},
		}},
		{Role: RoleUser, ToolResults: []ToolResult{
			{ToolCallID: "toolu_01AbcXYZ", Content: "Evicted x15 — DiskPressure"},
			{ToolCallID: "toolu_01DefUVW", Content: "2 nodes"},
		}},
		// Tool-calls-only: no prose. This is the turn that forced the
		// no-omitempty rule on openaiMessage.Content.
		{Role: RoleAssistant, ToolCalls: []ToolCall{
			{ID: "toolu_01GhiRST", Name: "get_pod_logs", Input: json.RawMessage(`{"pod":"victoriametrics-0"}`)},
		}},
		{Role: RoleUser, ToolResults: []ToolResult{
			{ToolCallID: "toolu_01GhiRST", Content: "OOMKilled"},
		}},
		{Role: RoleAssistant, Content: "El nodo entró en DiskPressure a las 03:10 UTC."},
	}
}

// Both APIs hard-error on an orphan: a tool call with no matching result, or a
// result quoting an id nobody called. A handoff that breaks the pairing turns a
// recoverable primary error into a 400 from the fallback — the operator sees
// the retry fail and concludes the fallback is misconfigured.
func TestFallbackHandoff_ToolCallPairingSurvivesBothWireFormats(t *testing.T) {
	msgs := productionShapedTranscript()

	t.Run("openai", func(t *testing.T) {
		called, resolved := map[string]string{}, map[string]bool{}
		for _, m := range toOpenAIMessages("SYSTEM", msgs) {
			for _, tc := range m.ToolCalls {
				if tc.ID == "" || tc.Function.Name == "" {
					t.Errorf("tool call sin id o nombre: %+v", tc)
				}
				if !json.Valid([]byte(tc.Function.Arguments)) {
					t.Errorf("%s: arguments no es JSON válido: %q", tc.Function.Name, tc.Function.Arguments)
				}
				called[tc.ID] = tc.Function.Name
			}
			if m.Role == "tool" {
				if m.ToolCallID == "" {
					t.Error("mensaje role=tool sin tool_call_id")
				}
				resolved[m.ToolCallID] = true
			}
		}
		assertPaired(t, called, resolved)
	})

	t.Run("anthropic", func(t *testing.T) {
		called, resolved := map[string]string{}, map[string]bool{}
		for _, m := range toAnthropicMessages(msgs) {
			for _, c := range m.Content {
				switch c.Type {
				case "tool_use":
					if c.ID == "" || c.Name == "" {
						t.Errorf("tool_use sin id o nombre: %+v", c)
					}
					called[c.ID] = c.Name
				case "tool_result":
					if c.ToolUseID == "" {
						t.Error("tool_result sin tool_use_id")
					}
					resolved[c.ToolUseID] = true
				}
			}
		}
		assertPaired(t, called, resolved)
	})
}

func assertPaired(t *testing.T, called map[string]string, resolved map[string]bool) {
	t.Helper()
	if len(called) != 3 {
		t.Errorf("se serializaron %d tool calls, esperaba las 3 del transcript", len(called))
	}
	for id, name := range called {
		if !resolved[id] {
			t.Errorf("%s (%s) quedó huérfana: el fallback recibiría un transcript que la API rechaza", id, name)
		}
	}
	for id := range resolved {
		if _, ok := called[id]; !ok {
			t.Errorf("resultado para %q sin llamada previa", id)
		}
	}
}

// The ids in a handed-over transcript were minted by the PRIMARY: Anthropic
// emits `toolu_…`, OpenAI emits `call_…`. Neither adapter may rewrite or drop
// them — the pairing is the only thing tying a result to its call — and both
// shapes must pass the receiving provider's identifier rules.
func TestFallbackHandoff_ForeignToolCallIDsArePreservedVerbatim(t *testing.T) {
	idOK := regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
	for _, id := range []string{"toolu_01AbcXYZ", "call_9xKpQ2"} {
		if !idOK.MatchString(id) {
			t.Fatalf("id de prueba %q no es representativo", id)
		}
		msgs := []Message{
			{Role: RoleUser, Content: "q"},
			{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: id, Name: "get_events", Input: json.RawMessage(`{}`)}}},
			{Role: RoleUser, ToolResults: []ToolResult{{ToolCallID: id, Content: "ok"}}},
		}
		ob, _ := json.Marshal(toOpenAIMessages("", msgs))
		ab, _ := json.Marshal(toAnthropicMessages(msgs))
		for name, blob := range map[string][]byte{"openai": ob, "anthropic": ab} {
			if n := strings.Count(string(blob), id); n != 2 {
				t.Errorf("%s: el id %q aparece %d veces, esperaba 2 (llamada + resultado)", name, id, n)
			}
		}
	}
}

// The fallback inherits the primary's tool set verbatim — the round loop does
// not re-derive it. A tool whose schema only satisfies one provider would make
// every fallback attempt 400 while the primary works fine, which reads as "the
// fallback is broken" long after the offending tool shipped.
func TestFallbackHandoff_ToolSetIsAcceptedByBothProviders(t *testing.T) {
	defs := ToolDefinitions()
	if len(defs) == 0 {
		t.Fatal("sin tools que verificar")
	}
	oa, an := toOpenAITools(defs), toAnthropicTools(defs)
	if len(oa) != len(defs) || len(an) != len(defs) {
		t.Fatalf("conversión con pérdida: %d defs → %d openai, %d anthropic", len(defs), len(oa), len(an))
	}
	// OpenAI rejects a function name outside this class; Anthropic is laxer, so
	// the strictest of the two is the one worth enforcing on both.
	nameOK := regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
	for _, d := range defs {
		if !nameOK.MatchString(d.Name) {
			t.Errorf("%q: OpenAI rechaza este nombre de función", d.Name)
		}
		if d.Description == "" {
			t.Errorf("%q: sin descripción — el fallback no sabría cuándo usarla", d.Name)
		}
		raw, err := json.Marshal(d.InputSchema)
		if err != nil {
			t.Errorf("%q: el schema no serializa: %v", d.Name, err)
			continue
		}
		var schema map[string]any
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Errorf("%q: el schema no es un objeto JSON: %v", d.Name, err)
			continue
		}
		// Both providers require the top-level schema to be an object with
		// properties; OpenAI additionally refuses a bare/typeless schema.
		if schema["type"] != "object" {
			t.Errorf("%q: schema type=%v, ambos proveedores exigen \"object\"", d.Name, schema["type"])
		}
		if _, ok := schema["properties"]; !ok {
			t.Errorf("%q: schema sin \"properties\"", d.Name)
		}
	}
}

// Two shapes DO diverge between the formats. Neither is reachable today, which
// is exactly why they need pinning: the cost of each only shows up on the retry
// path, in production, on the day the primary is down — the worst moment to
// discover that the two providers were reading different conversations.
func TestFallbackHandoff_KnownDivergencesArePinned(t *testing.T) {
	// (1) A system message sitting INSIDE the transcript. Anthropic keeps the
	// system prompt in a separate top-level field, so the adapter skips it;
	// OpenAI emits it as a real turn. Same transcript, different instructions.
	// The backend never injects one today — but CopilotRole in the web types
	// still admits 'system', so a client could.
	mid := []Message{
		{Role: RoleUser, Content: "q"},
		{Role: RoleSystem, Content: "REGLA EXTRA"},
		{Role: RoleAssistant, Content: "a"},
	}
	oa := toOpenAIMessages("", mid)
	an := toAnthropicMessages(mid)
	if len(oa) != 3 {
		t.Errorf("openai conservó %d mensajes, esperaba 3 (honra el system intermedio)", len(oa))
	}
	if len(an) != 2 {
		t.Errorf("anthropic conservó %d mensajes, esperaba 2 (descarta el system intermedio)", len(an))
	}
	if len(oa) == len(an) {
		t.Log("la divergencia del system intermedio desapareció — si fue deliberado, borra este caso")
	}

	// (2) An assistant turn with no text at all. OpenAI keeps the empty turn,
	// Anthropic drops the message entirely. Harmless while it is the last turn;
	// it would matter if an empty turn ever sat mid-transcript.
	empty := []Message{
		{Role: RoleUser, Content: "q"},
		{Role: RoleAssistant, Content: ""},
	}
	if got := len(toOpenAIMessages("", empty)); got != 2 {
		t.Errorf("openai conservó %d mensajes para el turno vacío, esperaba 2", got)
	}
	if got := len(toAnthropicMessages(empty)); got != 1 {
		t.Errorf("anthropic conservó %d mensajes para el turno vacío, esperaba 1", got)
	}

	// (3) A tool result with no output. OpenAI must receive "" explicitly —
	// this is the rule openaiMessage.Content documents; Anthropic omits the
	// field. Both are accepted, but only because the adapters differ on purpose.
	none := []Message{
		{Role: RoleUser, Content: "q"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "t1", Name: "get_events", Input: json.RawMessage(`{}`)}}},
		{Role: RoleUser, ToolResults: []ToolResult{{ToolCallID: "t1", Content: ""}}},
	}
	blob, _ := json.Marshal(toOpenAIMessages("", none))
	if strings.Count(string(blob), `"content":""`) == 0 {
		t.Error(`openai debe enviar "content":"" explícito en el resultado vacío; lo rechaza si falta`)
	}
}
