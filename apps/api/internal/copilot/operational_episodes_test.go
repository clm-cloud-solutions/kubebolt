package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/insights"
)

// These cover get_operational_episodes, the tool that answers "several things
// broke at once — what did they have in common?". It exists because of a real
// production miss: on 2026-09-16 Kobi spent 17 rounds on a burst of evictions
// and concluded with a cause that had happened 90 minutes AFTER them, while
// the burst itself was already classified as a node rotation in the episode
// store it could not reach.

func burstExecutor(fn func(ctx context.Context, from, to time.Time) ([]insights.OperationalEpisode, error)) *Executor {
	// A nil manager on purpose: this tool must not need a cluster runtime.
	return NewExecutor(nil).WithOperationalEpisodes(fn)
}

func callBurst(t *testing.T, e *Executor, input string) (ToolResult, map[string]any) {
	t.Helper()
	if input == "" {
		input = "{}"
	}
	res := e.ExecuteCtx(context.Background(), ToolCall{
		ID: "c1", Name: "get_operational_episodes", Input: json.RawMessage(input),
	})
	var payload map[string]any
	if err := json.Unmarshal([]byte(res.Content), &payload); err != nil {
		t.Fatalf("el resultado no es JSON: %v\n%s", err, res.Content)
	}
	return res, payload
}

func sampleBurst() insights.OperationalEpisode {
	from := time.Date(2026, 9, 16, 3, 8, 0, 0, time.UTC)
	return insights.OperationalEpisode{
		ID:         "0b1f5a2c-0000-4000-8000-000000000001",
		Kind:       insights.OpKindNodeRotation,
		Clusters:   []string{"prod"},
		WindowFrom: from,
		OnsetTo:    from.Add(4 * time.Minute),
		WindowTo:   from.Add(11 * time.Minute),
		SeedIDs:    []string{"seed-1", "seed-2"},
		MemberIDs:  []string{"m-1", "m-2", "m-3"},
		Blast: insights.BlastStats{
			Affected: 15, AutoRecovered: 15, WorstSeconds: 640, WorstResource: "app/api",
		},
	}
}

// The regression that matters most. Every other tool is gated on a live
// connector, and this one was too until it was caught: burst history lives in
// the org's episode store, and the question it answers is asked ABOUT clusters
// that are down. GET /insights/operational-episodes sits outside
// requireConnector for the same reason.
func TestOperationalEpisodes_AnswersWithoutAConnector(t *testing.T) {
	e := burstExecutor(func(context.Context, time.Time, time.Time) ([]insights.OperationalEpisode, error) {
		return []insights.OperationalEpisode{sampleBurst()}, nil
	})
	res, payload := callBurst(t, e, "")
	if res.IsError {
		t.Fatalf("falló sin conector, que es justo cuando hace falta: %s", res.Content)
	}
	if strings.Contains(res.Content, "needsProxy") {
		t.Fatal("cayó en la puerta del conector; el historial de ráfagas no lo necesita")
	}
	if payload["total"] != float64(1) {
		t.Errorf("total=%v, esperaba 1", payload["total"])
	}
}

// "I cannot see bursts" and "there were no bursts" are different answers, and
// collapsing them is how a model concludes a workload broke on its own.
func TestOperationalEpisodes_UnavailableIsNotTheSameAsNoBurst(t *testing.T) {
	res := NewExecutor(nil).ExecuteCtx(context.Background(), ToolCall{
		ID: "c1", Name: "get_operational_episodes", Input: json.RawMessage(`{}`),
	})
	if !res.IsError {
		t.Error("una lectura imposible debe marcarse como error, no devolver una lista vacía")
	}
	low := strings.ToLower(res.Content)
	if strings.Contains(low, `"episodes"`) || strings.Contains(low, `"total":0`) {
		t.Error("devolvió forma de «cero ráfagas»; el modelo lo leería como «no pasó nada»")
	}
	if !strings.Contains(low, "not") || !strings.Contains(low, "evidence") {
		t.Errorf("el mensaje no dice que la ausencia de dato no es evidencia: %s", res.Content)
	}
}

// Member and seed ids are uuids no tool consumes. Shipping them would spend
// context saying nothing — the failure this whole line of work is about was
// paid for in context, 879k cache-read tokens over 17 rounds.
func TestOperationalEpisodes_CollapsesIDsAndKeepsTheBlast(t *testing.T) {
	e := burstExecutor(func(context.Context, time.Time, time.Time) ([]insights.OperationalEpisode, error) {
		return []insights.OperationalEpisode{sampleBurst()}, nil
	})
	res, payload := callBurst(t, e, "")

	for _, leaked := range []string{"m-1", "m-2", "m-3", "seed-1", "seed-2"} {
		if strings.Contains(res.Content, `"`+leaked+`"`) {
			t.Errorf("filtró el id %q; deben viajar como cuenta", leaked)
		}
	}
	eps, _ := payload["episodes"].([]any)
	if len(eps) != 1 {
		t.Fatalf("episodes=%d, esperaba 1", len(eps))
	}
	ep, _ := eps[0].(map[string]any)
	for field, want := range map[string]float64{
		"members": 3, "seeds": 2, "affected": 15, "autoRecovered": 15, "worstSeconds": 640,
	} {
		if ep[field] != want {
			t.Errorf("%s=%v, esperaba %v", field, ep[field], want)
		}
	}
	// The kind is the whole point: it is the shared cause, pre-classified.
	if ep["kind"] != insights.OpKindNodeRotation {
		t.Errorf("kind=%v, esperaba %q", ep["kind"], insights.OpKindNodeRotation)
	}
	// The burst's own id stays — it is what a human types into the UI.
	if ep["id"] != sampleBurst().ID {
		t.Errorf("id=%v, esperaba que se conservara", ep["id"])
	}
	// Timestamps must be comparable to the event timestamps the model already
	// has, or the precedence rule in the prompt has nothing to compare.
	for _, f := range []string{"onsetFrom", "onsetTo", "lastSeen"} {
		if _, err := time.Parse(time.RFC3339, fmt.Sprint(ep[f])); err != nil {
			t.Errorf("%s no es RFC3339: %v", f, ep[f])
		}
	}
	// El inicio y la última actividad deben viajar SEPARADOS: comparar relojes
	// con la cola de una ráfaga crónica es lo que la regla de precedencia del
	// prompt necesita evitar.
	if ep["onsetTo"] == ep["lastSeen"] {
		t.Error("el inicio y la última actividad llegaron colapsados en el mismo valor")
	}
	if _, ok := ep["windowTo"]; ok {
		t.Error("sigue enviando windowTo, que era el nombre ambiguo")
	}
}

func TestOperationalEpisodes_WindowIsClamped(t *testing.T) {
	cases := []struct {
		input     string
		wantHours float64
	}{
		{`{}`, defaultBurstWindowHours},
		{`{"sinceHours": 6}`, 6},
		{`{"sinceHours": 0}`, 1},
		{`{"sinceHours": -40}`, 1},
		{`{"sinceHours": 100000}`, maxBurstWindowHours},
	}
	for _, tc := range cases {
		var gotFrom, gotTo time.Time
		e := burstExecutor(func(_ context.Context, from, to time.Time) ([]insights.OperationalEpisode, error) {
			gotFrom, gotTo = from, to
			return nil, nil
		})
		if res, _ := callBurst(t, e, tc.input); res.IsError {
			t.Fatalf("%s: %s", tc.input, res.Content)
		}
		if got := gotTo.Sub(gotFrom).Hours(); got != tc.wantHours {
			t.Errorf("%s → ventana de %.0fh, esperaba %.0fh", tc.input, got, tc.wantHours)
		}
	}
}

func TestOperationalEpisodes_ReaderErrorSurfaces(t *testing.T) {
	e := burstExecutor(func(context.Context, time.Time, time.Time) ([]insights.OperationalEpisode, error) {
		return nil, fmt.Errorf("postgres se cayó")
	})
	res, _ := callBurst(t, e, "")
	if !res.IsError {
		t.Error("un fallo del store debe marcarse como error, no pasar por «no hubo ráfagas»")
	}
	if !strings.Contains(res.Content, "postgres se cayó") {
		t.Errorf("la causa se perdió: %s", res.Content)
	}
}

// A noisy morning is exactly when the tool gets called, so its cost has to
// stay flat. 40 bursts of 60 members each would be ~4k of uuids alone.
func TestOperationalEpisodes_StaysSmallOnANoisyMorning(t *testing.T) {
	e := burstExecutor(func(context.Context, time.Time, time.Time) ([]insights.OperationalEpisode, error) {
		var out []insights.OperationalEpisode
		for i := 0; i < 40; i++ {
			ep := sampleBurst()
			ep.ID = fmt.Sprintf("burst-%02d", i)
			ep.MemberIDs = make([]string, 60)
			for j := range ep.MemberIDs {
				ep.MemberIDs[j] = fmt.Sprintf("00000000-0000-4000-8000-%012d", i*100+j)
			}
			out = append(out, ep)
		}
		return out, nil
	})
	res, payload := callBurst(t, e, "")
	if res.IsError {
		t.Fatalf("error inesperado: %s", res.Content)
	}
	// The real count is still reported: the model must know the morning was
	// noisier than what it is looking at.
	if payload["total"] != float64(40) {
		t.Errorf("total=%v, esperaba que reportara las 40 reales", payload["total"])
	}
	if payload["returned"] != float64(maxBurstsReturned) {
		t.Errorf("returned=%v, esperaba %d", payload["returned"], maxBurstsReturned)
	}
	if payload["truncated"] != true {
		t.Error("recortó sin decirlo — un historial truncado en silencio se lee como una mañana tranquila")
	}
	// The tail survives: the most recent bursts, as a contiguous timeline.
	eps, _ := payload["episodes"].([]any)
	if len(eps) != maxBurstsReturned {
		t.Fatalf("episodes=%d, esperaba %d", len(eps), maxBurstsReturned)
	}
	if first, _ := eps[0].(map[string]any); first["id"] != "burst-20" {
		t.Errorf("la primera es %v; el recorte debe conservar la cola, no la cabeza", first["id"])
	}
	if last, _ := eps[len(eps)-1].(map[string]any); last["id"] != "burst-39" {
		t.Errorf("la última es %v, esperaba burst-39", last["id"])
	}
	// ~4 chars per token: 8KB ≈ 2k tokens is a fair ceiling for one answer.
	if len(res.Content) > 8*1024 {
		t.Errorf("%d bytes — demasiado contexto por respuesta", len(res.Content))
	}
}

// Under the cap nothing is trimmed and no truncation noise is added.
func TestOperationalEpisodes_NoTruncationNoiseWhenItFits(t *testing.T) {
	e := burstExecutor(func(context.Context, time.Time, time.Time) ([]insights.OperationalEpisode, error) {
		return []insights.OperationalEpisode{sampleBurst(), sampleBurst()}, nil
	})
	res, payload := callBurst(t, e, "")
	if _, ok := payload["truncated"]; ok {
		t.Error("marcó truncado sin recortar nada")
	}
	if payload["total"] != float64(2) || payload["returned"] != float64(2) {
		t.Errorf("total=%v returned=%v, esperaba 2 y 2", payload["total"], payload["returned"])
	}
	if strings.Contains(res.Content, "narrow sinceHours") {
		t.Error("añadió la nota de recorte sin recortar")
	}
}

// The MCP server publishes GovernedToolDefinitions(false, false) as its
// read-only catalogue. A read tool missing from it is invisible to every MCP
// host, which is where a chunk of Kobi's usage now lives.
func TestOperationalEpisodes_IsPublishedAndReadOnly(t *testing.T) {
	const name = "get_operational_episodes"
	find := func(defs []ToolDefinition) *ToolDefinition {
		for i := range defs {
			if defs[i].Name == name {
				return &defs[i]
			}
		}
		return nil
	}
	def := find(ToolDefinitions())
	if def == nil {
		t.Fatal("la tool no está en el catálogo")
	}
	if find(GovernedToolDefinitions(false, false)) == nil {
		t.Error("se cae del catálogo de solo-lectura que consume MCP")
	}
	if strings.HasPrefix(name, "propose_") {
		t.Error("no debe parecer una mutación")
	}
	// The description must say WHEN, not only what: a tool the model reaches
	// for after it has already built a theory arrives too late to change it.
	low := strings.ToLower(def.Description)
	for _, cue := range []string{"first", "at once", "node_rotation"} {
		if !strings.Contains(low, cue) {
			t.Errorf("la descripción no orienta el cuándo: falta %q", cue)
		}
	}
}
