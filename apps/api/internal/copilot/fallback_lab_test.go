//go:build fallbacklab

// Package-local lab, excluded from every normal build and from CI.
//
//	go test ./apps/api/internal/copilot/ -tags fallbacklab -run TestFallbackLab -v -count=1
//
// fallback_handoff_test.go proves the handoff is WIRE-safe: a transcript the
// primary authored serializes cleanly for either provider. It cannot prove the
// handoff is BEHAVIOUR-safe, because that depends on the model. This lab does
// that half, against the real endpoints, and it costs real money — a few cents
// per run — which is why it is tagged out.
//
// The question it answers: when the fallback takes over mid-investigation,
// does it keep investigating the way the primary would, or does it change
// character? Two candidates are compared on the IDENTICAL inherited transcript:
// one on the primary's own provider, one on a different provider.
//
// What it deliberately does NOT answer: whether a cross-provider fallback is
// worth keeping. A same-provider fallback shares the primary's blast radius —
// when Anthropic is down as a whole, a second Claude model is down too. That is
// an availability argument this test cannot measure, and it does not expire
// because the behaviour scores came out one way or the other.
package copilot

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/config"
)

// Env:
//
//	ANTHROPIC_API_KEY / OPENAI_API_KEY   — whichever candidates you list
//	KUBEBOLT_FALLBACK_LAB_CANDIDATES     — comma list of provider:model
//	                                       (default: anthropic:claude-sonnet-5,
//	                                        anthropic:claude-haiku-4-5,
//	                                        openai:gpt-5.6-terra)
//	KUBEBOLT_FALLBACK_LAB_FIXTURE        — path to a JSON []Message to replay
//	                                       instead of the synthetic incident.
//	                                       Keep real captures OUT of the repo.
func labCandidates() []config.ProviderConfig {
	spec := os.Getenv("KUBEBOLT_FALLBACK_LAB_CANDIDATES")
	if spec == "" {
		spec = "anthropic:claude-sonnet-5,anthropic:claude-haiku-4-5,openai:gpt-5.6-terra"
	}
	var out []config.ProviderConfig
	for _, item := range strings.Split(spec, ",") {
		provider, model, ok := strings.Cut(strings.TrimSpace(item), ":")
		if !ok {
			continue
		}
		key := os.Getenv("ANTHROPIC_API_KEY")
		if provider == "openai" {
			key = os.Getenv("OPENAI_API_KEY")
		}
		if key == "" {
			continue
		}
		out = append(out, config.ProviderConfig{Provider: provider, Model: model, APIKey: key})
	}
	return out
}

// inheritedTranscript is the state the fallback wakes up in: the user has asked
// a causal question, the primary has already spent several rounds gathering
// evidence, and the next turn is the one that either keeps digging or concludes.
//
// The default fixture is SYNTHETIC — same shape as the incident that motivated
// this lab (node disk pressure with a maintenance window in the timeline and a
// loud-but-later memory spike as the decoy), with invented names. Real captures
// belong in a file outside the repo, passed via KUBEBOLT_FALLBACK_LAB_FIXTURE.
func inheritedTranscript(t *testing.T) []Message {
	t.Helper()
	if path := os.Getenv("KUBEBOLT_FALLBACK_LAB_FIXTURE"); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("no pude leer el fixture: %v", err)
		}
		var msgs []Message
		if err := json.Unmarshal(raw, &msgs); err != nil {
			t.Fatalf("el fixture no es []Message: %v", err)
		}
		t.Logf("fixture externo: %s (%d mensajes)", path, len(msgs))
		return msgs
	}
	return []Message{
		{Role: RoleUser, Content: "Se me cayeron un montón de pods anoche. ¿Cuál fue la causa raíz?"},
		{Role: RoleAssistant, Content: "Reviso eventos y estado de los nodos.", ToolCalls: []ToolCall{
			{ID: "toolu_lab01", Name: "get_events", Input: json.RawMessage(`{"namespace":"app-system"}`)},
			{ID: "toolu_lab02", Name: "list_resources", Input: json.RawMessage(`{"type":"nodes"}`)},
		}},
		{Role: RoleUser, ToolResults: []ToolResult{
			{ToolCallID: "toolu_lab01", Content: `02:58 Node node-b NodeNotReady
03:02 Node node-b NodeReady (kubelet v1.31.4 -> v1.31.6)
03:11 Pod api-7d9 Evicted: node was low on resource: ephemeral-storage. Available 2.4Gi, threshold 3Gi
03:12..03:19 Pod api-* Evicted x14 (DiskPressure)
04:46 Pod tsdb-0 memory usage 4.4Gi exceeded limit 3.2Gi`},
			{ToolCallID: "toolu_lab02", Content: `node-a  Ready  age 14d  osDisk 30Gi  DiskPressure=False
node-b  Ready  age 6h   osDisk 30Gi  DiskPressure=False  (imagen de nodo reemplazada 03:00)`},
		}},
		{Role: RoleAssistant, ToolCalls: []ToolCall{
			{ID: "toolu_lab03", Name: "get_resource_detail", Input: json.RawMessage(`{"type":"statefulsets","name":"tsdb"}`)},
		}},
		{Role: RoleUser, ToolResults: []ToolResult{
			{ToolCallID: "toolu_lab03", Content: `tsdb-0 monta un PVC de 40Gi (Bound). Retención 90d. No usa almacenamiento efímero del nodo.`},
		}},
	}
}

// Round N+1 on the inherited transcript, tools still attached: the fallback's
// first decision. Keeping tools in hand is the behaviour we care about — the
// failure mode worth catching is a fallback that stops investigating and
// narrates what it already has.
func TestFallbackLab_ContinuationOnInheritedTranscript(t *testing.T) {
	candidates := labCandidates()
	if len(candidates) == 0 {
		t.Skip("sin API keys: exporta ANTHROPIC_API_KEY / OPENAI_API_KEY")
	}
	msgs := inheritedTranscript(t)
	tools := ToolDefinitions()

	for _, cand := range candidates {
		t.Run(cand.Provider+"/"+cand.Model, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()
			start := time.Now()
			p := GetProvider(cand.Provider)
			if p == nil {
				t.Fatalf("proveedor no registrado: %q", cand.Provider)
			}
			resp, err := p.Chat(ctx, ChatRequest{
				System:    BuildSystemPrompt(),
				Messages:  msgs,
				Tools:     tools,
				Provider:  cand,
				MaxTokens: 4096,
			})
			if err != nil {
				t.Fatalf("error del proveedor: %v", err)
			}
			names := make([]string, len(resp.ToolCalls))
			for i, tc := range resp.ToolCalls {
				names[i] = tc.Name
			}
			decision := "CONCLUYE en prosa"
			if len(resp.ToolCalls) > 0 {
				decision = "SIGUE investigando: " + strings.Join(names, ", ")
			}
			t.Logf("%-34s %-7s %s | in=%d cacheRead=%d out=%d | %s",
				cand.Provider+"/"+cand.Model,
				time.Since(start).Round(100*time.Millisecond),
				resp.StopReason,
				resp.Usage.InputTokens, resp.Usage.CacheReadTokens, resp.Usage.OutputTokens,
				decision)
			if resp.Text != "" {
				t.Logf("  texto: %s", truncateLab(resp.Text, 700))
			}
		})
	}
}

// The close turn: same evidence, tools removed, forced to commit to a cause.
// This is where a weaker model shows — it reaches for the loudest signal in the
// window (the memory spike) instead of the one whose timestamps actually line
// up with the evictions (the node replacement at 03:00, before them).
//
// The assertion is deliberately one-sided: naming the decoy as the cause is
// wrong and is reported as a failure; NOT naming it is not by itself proof of a
// right answer, so read the logged text before trusting a pass.
func TestFallbackLab_CloseTurnGroundsTheCause(t *testing.T) {
	candidates := labCandidates()
	if len(candidates) == 0 {
		t.Skip("sin API keys: exporta ANTHROPIC_API_KEY / OPENAI_API_KEY")
	}
	if os.Getenv("KUBEBOLT_FALLBACK_LAB_FIXTURE") != "" {
		t.Skip("el scoring está calibrado al fixture sintético; con un replay real, lee el texto")
	}
	msgs := append(inheritedTranscript(t), Message{
		Role:    RoleUser,
		Content: "Dame la causa raíz, sin más herramientas.",
	})

	for _, cand := range candidates {
		t.Run(cand.Provider+"/"+cand.Model, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()
			p := GetProvider(cand.Provider)
			if p == nil {
				t.Fatalf("proveedor no registrado: %q", cand.Provider)
			}
			resp, err := p.Chat(ctx, ChatRequest{
				System:    BuildSystemPrompt(),
				Messages:  msgs,
				Tools:     nil, // sin herramientas: obligado a concluir
				Provider:  cand,
				MaxTokens: 4096,
			})
			if err != nil {
				t.Fatalf("error del proveedor: %v", err)
			}
			text := strings.ToLower(resp.Text)
			t.Logf("%s →\n%s", cand.Provider+"/"+cand.Model, truncateLab(resp.Text, 1600))

			// El decoy: culpar al pico de memoria del tsdb, que ocurrió 90
			// minutos DESPUÉS de las evictions y sobre un PVC, no sobre el
			// disco del nodo.
			blamesDecoy := strings.Contains(text, "tsdb") &&
				(strings.Contains(text, "causa raíz") || strings.Contains(text, "root cause") ||
					strings.Contains(text, "causa raiz")) &&
				!strings.Contains(text, "después") && !strings.Contains(text, "posterior") &&
				!strings.Contains(text, "no es la causa")
			if blamesDecoy {
				t.Errorf("culpa al pico de memoria del tsdb — ocurrió después de las evictions y sobre un PVC")
			}
			// La señal correcta: el reemplazo de imagen del nodo a las 03:00,
			// minutos antes de las evictions, sobre un disco de 30Gi.
			if !strings.Contains(text, "03:00") && !strings.Contains(text, "imagen de nodo") &&
				!strings.Contains(text, "node-b") && !strings.Contains(text, "nodo nuevo") {
				t.Errorf("no menciona el reemplazo del nodo a las 03:00, que es lo único que precede a las evictions")
			}
		})
	}
}

func truncateLab(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
