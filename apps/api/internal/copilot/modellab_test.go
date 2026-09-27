//go:build modellab

// Package-local lab, excluded from every normal build and from CI.
//
//	go test ./apps/api/internal/copilot/ -tags modellab -run TestModelLab -v -count=1
//
// Sibling of fallback_lab_test.go, and the OpenAI-side answer to
// apps/autopilot/src/eval/compare-models.ts. That harness drives Autopilot's
// stages through the Claude Agent SDK, so it can only ever compare Anthropic
// models. This one drives KOBI's loop instead — the surface these models
// actually serve — and Kobi's OpenAI adapter takes a base URL, so the same
// table compares OpenAI, xAI, DeepSeek, Qwen, Kimi, GLM, a vLLM deployment,
// or anything else that speaks /v1/chat/completions.
//
// It costs real money, which is why it is tagged out.
//
// ── Why the evidence is FROZEN ──────────────────────────────────────────
// The tools are answered from a fixture, not from a live cluster. Three
// reasons, all learned elsewhere in this repo:
//
//  1. A live cluster drifts between runs, so model B is graded on a slightly
//     different world than model A and the difference is unattributable.
//  2. Some candidates would repair the incident (Kobi's read-only catalogue
//     makes that unlikely, but the loop is the same one that grows actions).
//  3. Cost. A frozen incident is a handful of rounds; a live investigation is
//     unbounded and we are paying per candidate.
//
// What is NOT frozen is the reasoning: the model chooses which tools to call
// and in what order, and an unexpected call is recorded rather than hidden —
// asking for something the fixture never mentioned is itself a finding.
package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/config"
)

// Env:
//
//	ANTHROPIC_API_KEY / OPENAI_API_KEY  — whichever candidates you list
//	KUBEBOLT_MODEL_LAB_CANDIDATES       — comma list of provider:model[@baseURL]
//	                                      e.g. "openai:gpt-6-sol,openai:gpt-6-luna,
//	                                            anthropic:claude-opus-5-5,
//	                                            openai:deepseek-chat@https://api.deepseek.com/v1"
//	                                      A candidate with @baseURL reads its key
//	                                      from KUBEBOLT_MODEL_LAB_KEY_<N> (1-based)
//	                                      so a third-party key never has to be
//	                                      exported as OPENAI_API_KEY.
//	KUBEBOLT_MODEL_LAB_ROUNDS           — max tool rounds per run (default 8)

type labCandidate struct {
	cfg   config.ProviderConfig
	label string
}

func modelLabCandidates(t *testing.T) []labCandidate {
	t.Helper()
	spec := os.Getenv("KUBEBOLT_MODEL_LAB_CANDIDATES")
	if spec == "" {
		spec = "anthropic:claude-opus-5-5,anthropic:claude-opus-5,openai:gpt-6-sol,openai:gpt-6-luna"
	}
	var out []labCandidate
	for i, item := range strings.Split(spec, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		rest, baseURL, hasBase := strings.Cut(item, "@")
		provider, model, ok := strings.Cut(rest, ":")
		if !ok {
			t.Fatalf("candidato %q: se esperaba provider:model[@baseURL]", item)
		}
		key := os.Getenv("ANTHROPIC_API_KEY")
		if provider == "openai" {
			key = os.Getenv("OPENAI_API_KEY")
		}
		if hasBase {
			if k := os.Getenv(fmt.Sprintf("KUBEBOLT_MODEL_LAB_KEY_%d", i+1)); k != "" {
				key = k
			}
		}
		if key == "" {
			t.Logf("salto %s: no hay clave en el entorno", item)
			continue
		}
		label := model
		if hasBase {
			label = model + " @ " + baseURL
		}
		out = append(out, labCandidate{
			cfg:   config.ProviderConfig{Provider: provider, Model: model, APIKey: key, BaseURL: baseURL},
			label: label,
		})
	}
	return out
}

// frozenIncident is one graded case: a question, the evidence the tools will
// return, and what a correct answer has to contain.
//
// `decoys` is the part that makes the score mean something. Every incident
// here carries a loud-but-innocent signal alongside the real cause, because
// separating those two is the entire job. A model that names the decoy has
// not "partially" solved it — it has reached the wrong conclusion, and the
// score says so.
type frozenIncident struct {
	name     string
	question string
	// facts maps a tool name to what that tool returns. Answering by tool
	// name (not by arguments) keeps the fixture small and lets a model phrase
	// its query however it likes without being punished for wording.
	facts map[string]string
	// mustSay: every entry has to appear in the verdict (case-folded).
	// "a|b" means either form counts. Needed because the models answer in
	// the language of the question: Opus 5 diagnosed "agotamiento de su
	// almacenamiento efímero", a flawless answer that a literal "disk"
	// requirement scored as a miss. Grading surface wording instead of the
	// claim is a bug in the rubric, not a failure of the model.
	mustSay []string
	// mustNotSay: naming any of these as the cause is a wrong answer.
	mustNotSay []string
}

// The catalogue. Three incidents so a single lucky guess cannot decide a
// comparison, and all three drawn from shapes this product has actually seen.
var labIncidents = []frozenIncident{
	{
		name:     "disk-pressure-tras-upgrade-de-nodo",
		question: "Se me cayeron un montón de pods anoche en el namespace app-system. ¿Cuál fue la causa raíz?",
		facts: map[string]string{
			"get_events": `02:58 Node node-b NodeNotReady
03:02 Node node-b NodeReady (kubelet v1.31.4 -> v1.31.6)
03:11 Pod api-7d9 Evicted: node was low on resource: ephemeral-storage. Available 2.4Gi, threshold 3Gi
03:12..03:19 Pod api-* Evicted x14 (DiskPressure)
04:46 Pod tsdb-0 memory usage 4.4Gi exceeded limit 3.2Gi`,
			"list_resources": `node-a  Ready  age 14d  osDisk 30Gi  DiskPressure=False
node-b  Ready  age 6h   osDisk 30Gi  DiskPressure=False  (imagen de nodo reemplazada 03:00)`,
			"get_resource_detail": `tsdb-0 monta un PVC de 40Gi (Bound). Retención 90d.
No usa almacenamiento efímero del nodo.`,
			"get_resource_describe": `Name: node-b
Conditions: DiskPressure False (últimas 4h)
Taints: <none>
Allocatable: ephemeral-storage 27Gi`,
			"get_pod_logs": `api-7d9: sin errores antes de las 03:11; el proceso recibe SIGTERM y sale 0.`,
		},
		mustSay:    []string{"disk|almacenamiento efímero|ephemeral|efímero", "node-b"},
		mustNotSay: []string{"tsdb", "memory leak", "fuga de memoria"},
	},
	{
		name:     "crashloop-por-secret-ausente",
		question: "El deployment checkout lleva media hora en CrashLoopBackOff. ¿Qué pasa?",
		facts: map[string]string{
			"get_events": `10:02 Pod checkout-5f4 Created
10:02 Pod checkout-5f4 Failed: Error: secret "checkout-stripe" not found
10:03..10:31 Pod checkout-5f4 BackOff restarting failed container x18
10:14 HorizontalPodAutoscaler checkout unable to read metrics`,
			"get_pod_logs": `Error: environment variable STRIPE_API_KEY is required
exit status 1`,
			"get_resource_detail": `checkout: 0/3 ready. envFrom: secretRef checkout-stripe (optional: false)
Última actualización del manifiesto: hace 34 min.`,
			"list_resources": `secrets en el namespace: checkout-db, checkout-redis, observability-token
(no aparece checkout-stripe)`,
			"get_resource_describe": `Events: FailedMount? no. El fallo es en el arranque del contenedor.`,
		},
		mustSay:    []string{"secret", "checkout-stripe"},
		mustNotSay: []string{"hpa", "autoscaler", "métricas"},
	},
	{
		name:     "latencia-por-vecino-ruidoso-no-por-la-app",
		question: "El servicio payments está lento desde las 14:00. ¿Es culpa del último despliegue?",
		facts: map[string]string{
			"get_events": `13:58 Deployment payments ScalingReplicaSet payments-77c -> 4 (rollout)
14:00.. Pod batch-export-* Created x12 (Job batch-export nightly, arrancado a mano)
14:03 Node node-c CPU throttling observed on cgroup /kubepods/burstable`,
			"get_workload_metrics": `payments  p95 latency 120ms -> 940ms a las 14:02
payments  cpu usage 0.4 cores (limit 2)  — sin throttling propio
batch-export  cpu usage 7.6 cores en node-c (sin limits)`,
			"get_resource_detail": `payments-77c: misma imagen que la revisión anterior salvo el tag (v2.4.0 -> v2.4.1).
Diff del manifiesto: sólo la etiqueta de versión.`,
			"get_workload_history": `payments: rev 12 (v2.4.0) -> rev 13 (v2.4.1) a las 13:58. Sin cambios de recursos.`,
			"list_resources":       `node-c: payments-77c-x2, batch-export-* x12. Sin limits en batch-export.`,
		},
		mustSay:    []string{"batch-export"},
		mustNotSay: []string{"v2.4.1 es la causa", "rollback del despliegue"},
	},
}

type labResult struct {
	candidate string
	// provider/model are kept raw (not just the display label) because the
	// attribution in report() has to price ONE candidate's tokens at
	// ANOTHER's rates, and PricingFor needs the id, not the label.
	provider     string
	model        string
	incident     string
	rounds       int
	toolCalls    []string
	unknownCall  int
	usage        Usage
	costUSD      float64
	elapsed      time.Duration
	answer       string
	score        int
	misses       []string
	fellForDecoy bool
	err          error
}

// runOne drives the real multi-round loop by hand. The provider contract says
// Chat does NOT loop, so the loop lives here — same division of labour as the
// production handler, minus SSE.
func runOne(ctx context.Context, cand labCandidate, inc frozenIncident, maxRounds int) labResult {
	res := labResult{
		candidate: cand.label, incident: inc.name,
		provider: cand.cfg.Provider, model: cand.cfg.Model,
	}
	p := GetProvider(cand.cfg.Provider)
	if p == nil {
		res.err = fmt.Errorf("proveedor desconocido: %s", cand.cfg.Provider)
		return res
	}
	// The real read-only catalogue: 23 tools, no propose_*. Giving every
	// candidate the identical tool surface is the whole point.
	tools := GovernedToolDefinitions(false, false)

	msgs := []Message{{Role: RoleUser, Content: inc.question}}
	system := "Eres un SRE que diagnostica incidentes de Kubernetes. " +
		"Usa las herramientas de lectura para reunir evidencia y luego responde " +
		"con la causa raíz concreta, nombrando el recurso responsable. " +
		"No propongas cambios; sólo diagnostica."

	start := time.Now()
	for round := 0; round < maxRounds; round++ {
		resp, err := p.Chat(ctx, ChatRequest{
			System: system, Messages: msgs, Tools: tools,
			Provider: cand.cfg, MaxTokens: 2048,
		})
		if err != nil {
			res.err = err
			break
		}
		res.rounds++
		res.usage.InputTokens += resp.Usage.InputTokens
		res.usage.OutputTokens += resp.Usage.OutputTokens
		res.usage.CacheReadTokens += resp.Usage.CacheReadTokens
		res.usage.CacheCreationTokens += resp.Usage.CacheCreationTokens

		if len(resp.ToolCalls) == 0 {
			res.answer = resp.Text
			break
		}
		msgs = append(msgs, Message{Role: RoleAssistant, Content: resp.Text, ToolCalls: resp.ToolCalls})
		var results []ToolResult
		for _, tc := range resp.ToolCalls {
			res.toolCalls = append(res.toolCalls, tc.Name)
			content, ok := inc.facts[tc.Name]
			if !ok {
				// Not an error: the fixture simply has nothing to say. The
				// model is told so plainly, the way a real empty result reads.
				res.unknownCall++
				content = "Sin datos para esa consulta en este incidente."
			}
			results = append(results, ToolResult{ToolCallID: tc.ID, Content: content})
		}
		msgs = append(msgs, Message{Role: RoleUser, ToolResults: results})
	}
	res.elapsed = time.Since(start)

	if pricing, ok := PricingFor(cand.cfg.Provider, cand.cfg.Model); ok {
		res.costUSD = EstimateUSD(res.usage, pricing)
	}
	res.score, res.misses, res.fellForDecoy = grade(res.answer, inc)
	return res
}

// verdictWindow narrows an answer to the part that states a conclusion.
//
// This exists because the first version of this rubric graded the WHOLE
// answer and was wrong about it. A good diagnosis names the decoy in order
// to dismiss it — Opus 5.5 put the tsdb memory spike in a timeline table and
// then explained why it was not the cause — and scanning the full text
// scored that as "fell for the decoy". Mentioning is not concluding. So the
// decoy check (and the must-say check) run over the verdict sentence: from
// the first cause marker, or the opening of the answer when there is none.
func verdictWindow(a string) string {
	// The region is the UNION of the opening and every marker neighbourhood,
	// not the first marker alone. Second bug in this rubric, same family as
	// the first: a diagnosis either LEADS with its conclusion or LABELS it,
	// and both are verdicts. Taking only the earliest marker scored a perfect
	// answer at 0 — it named the missing Secret in its first sentence and the
	// only phrase matching a marker was a closing remark 1,700 chars later,
	// so the window landed past the answer and found nothing.
	const span = 500
	regions := []string{}
	if len(a) < span {
		regions = append(regions, a)
	} else {
		regions = append(regions, a[:span])
	}
	markers := []string{"causa raíz", "causa raiz", "root cause", "la causa es", "el problema es"}
	for _, m := range markers {
		for i := 0; ; {
			j := strings.Index(a[i:], m)
			if j < 0 {
				break
			}
			at := i + j
			end := at + span
			if end > len(a) {
				end = len(a)
			}
			regions = append(regions, a[at:end])
			i = at + len(m)
		}
	}
	return strings.Join(regions, "\n")
}

// grade is deterministic on purpose — no LLM judge. The rubric is coarse
// because the question it answers is coarse: did this model reach the right
// conclusion from the same evidence, or not.
func grade(answer string, inc frozenIncident) (int, []string, bool) {
	a := strings.ToLower(answer)
	verdict := verdictWindow(a)
	score := 100
	var misses []string
	if strings.TrimSpace(a) == "" {
		return 0, []string{"sin respuesta final"}, false
	}
	per := 60 / max(1, len(inc.mustSay))
	for _, want := range inc.mustSay {
		hit := false
		for _, alt := range strings.Split(strings.ToLower(want), "|") {
			if strings.Contains(verdict, strings.TrimSpace(alt)) {
				hit = true
				break
			}
		}
		if !hit {
			score -= per
			misses = append(misses, "no menciona: "+want)
		}
	}
	decoy := false
	for _, bad := range inc.mustNotSay {
		if blamesDecoy(verdict, strings.ToLower(bad)) {
			decoy = true
			misses = append(misses, "señala el señuelo: "+bad)
		}
	}
	if decoy {
		score -= 40
	}
	if score < 0 {
		score = 0
	}
	return score, misses, decoy
}

// blamesDecoy reports whether the decoy is named AS THE CAUSE, rather than
// named in order to be dismissed.
//
// Third and final correction to this rubric, and the one that marks its
// ceiling. Opus 5.5 wrote "Aviso aparte, no es la causa: a las 10:14 el HPA
// registró unable to read metrics — es una consecuencia", which is the best
// single piece of reasoning in the run, and a plain substring check scored it
// as falling for the decoy. Explicitly ruling a signal out is the opposite of
// being fooled by it.
//
// The guard is a real linguistic rule, not a threshold tuned until the
// numbers looked good: if a negation or dismissal appears shortly before the
// mention, the model is discarding the decoy, not blaming it.
//
// HONEST LIMIT: this is still keyword matching. It cannot read an argument.
// Treat `score` as a coarse screen — it reliably separates "named the right
// resource" from "named the wrong one" — and treat cost, rounds, tool calls
// and latency as the measured numbers. A quality verdict needs a blind judge
// or a human reading the transcripts the lab writes out.
func blamesDecoy(verdict, decoy string) bool {
	dismissals := []string{
		"no es la causa", "no es el problema", "no fue la causa",
		"es una consecuencia", "es consecuencia", "descartad", "descarto",
		"aparte", "secundario", "síntoma", "sintoma", "not the cause",
		// "no explica los desalojos" — gpt-6-sol's way of ruling the decoy
		// out. Added after it was scored as blaming it, which it was not.
		"no explica", "no justifica", "posterior", "ocurrió después",
		"does not explain",
	}
	for i := 0; ; {
		j := strings.Index(verdict[i:], decoy)
		if j < 0 {
			return false
		}
		at := i + j
		from := at - 140
		if from < 0 {
			from = 0
		}
		to := at + len(decoy) + 160
		if to > len(verdict) {
			to = len(verdict)
		}
		// Look BOTH ways. The guard used to scan only backwards and flagged
		// "Aviso del HPA (10:14): es una consecuencia, no la causa" — where
		// the dismissal trails the mention. Spanish puts it either side.
		lead := verdict[from:at] + " " + verdict[at+len(decoy):to]
		dismissed := false
		for _, d := range dismissals {
			if strings.Contains(lead, d) {
				dismissed = true
				break
			}
		}
		if !dismissed {
			return true // named with no hedge in front of it: it is the verdict
		}
		i = at + len(decoy)
	}
}

func TestModelLab(t *testing.T) {
	cands := modelLabCandidates(t)
	if len(cands) == 0 {
		t.Skip("sin candidatos con clave disponible")
	}
	maxRounds := 8
	if v := os.Getenv("KUBEBOLT_MODEL_LAB_ROUNDS"); v != "" {
		fmt.Sscanf(v, "%d", &maxRounds)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	var all []labResult
	for _, cand := range cands {
		for _, inc := range labIncidents {
			r := runOne(ctx, cand, inc, maxRounds)
			all = append(all, r)
			if r.err != nil {
				t.Logf("✗ %-28s %-34s ERROR: %v", cand.label, inc.name, r.err)
				continue
			}
			verdict := "✓"
			if r.score < 60 {
				verdict = "✗"
			}
			t.Logf("%s %-28s %-34s score=%3d rounds=%d tools=%d cost=$%.4f %s",
				verdict, cand.label, inc.name, r.score, r.rounds,
				len(r.toolCalls), r.costUSD, strings.Join(r.misses, "; "))
		}
	}
	baseline := ""
	if len(cands) > 0 {
		baseline = cands[0].label
	}
	report(t, all, baseline)
}

// report prints the table you paste into a comparison document. Cost is per
// incident and averaged, because a per-run number is what decides whether a
// model is affordable at the volume the product runs it.
func report(t *testing.T, all []labResult, baselineName string) {
	t.Helper()
	type agg struct {
		n, score, rounds, tools, unknown int
		cost                             float64
		elapsed                          time.Duration
		decoys                           int
		errs                             int
		usage                            Usage
		provider, model                  string
	}
	byCand := map[string]*agg{}
	var order []string
	for _, r := range all {
		a, ok := byCand[r.candidate]
		if !ok {
			a = &agg{}
			byCand[r.candidate] = a
			order = append(order, r.candidate)
		}
		if r.err != nil {
			a.errs++
			continue
		}
		a.n++
		a.score += r.score
		a.rounds += r.rounds
		a.tools += len(r.toolCalls)
		a.unknown += r.unknownCall
		a.cost += r.costUSD
		a.elapsed += r.elapsed
		a.usage.InputTokens += r.usage.InputTokens
		a.usage.OutputTokens += r.usage.OutputTokens
		a.usage.CacheReadTokens += r.usage.CacheReadTokens
		a.usage.CacheCreationTokens += r.usage.CacheCreationTokens
		a.provider, a.model = r.provider, r.model
		if r.fellForDecoy {
			a.decoys++
		}
	}
	sort.Slice(order, func(i, j int) bool {
		ai, aj := byCand[order[i]], byCand[order[j]]
		si, sj := 0, 0
		if ai.n > 0 {
			si = ai.score / ai.n
		}
		if aj.n > 0 {
			sj = aj.score / aj.n
		}
		return si > sj
	})

	t.Log("")
	t.Logf("%-30s %6s %8s %8s %10s %8s %7s", "modelo", "score", "rondas", "tools", "$/inc", "seg/inc", "señuelo")
	for _, name := range order {
		a := byCand[name]
		if a.n == 0 {
			t.Logf("%-30s   —  (%d errores)", name, a.errs)
			continue
		}
		t.Logf("%-30s %6d %8.1f %8.1f %10.4f %8.1f %7d",
			name, a.score/a.n,
			float64(a.rounds)/float64(a.n),
			float64(a.tools)/float64(a.n),
			a.cost/float64(a.n),
			a.elapsed.Seconds()/float64(a.n),
			a.decoys)
	}
	t.Log("")
	t.Logf("%-30s %10s %10s %12s %12s", "modelo", "in", "out", "cacheRead", "cacheWrite")
	for _, name := range order {
		a := byCand[name]
		if a.n == 0 {
			continue
		}
		t.Logf("%-30s %10d %10d %12d %12d", name,
			a.usage.InputTokens/a.n, a.usage.OutputTokens/a.n,
			a.usage.CacheReadTokens/a.n, a.usage.CacheCreationTokens/a.n)
	}

	// ── Atribución del coste ────────────────────────────────────────────
	//
	// A model can cost less for two unrelated reasons — a lower rate, or
	// fewer tokens — and the total does not say which. Reporting only the
	// total invites crediting whichever cause the release notes advertised.
	// That is not hypothetical: this harness reported Opus 5.5 at 48% below
	// Opus 5, the saving was written up as the cache-read discount, and the
	// rate cut only accounts for about 20 points of it. The rest was volume.
	//
	// The split is a counterfactual: price the CANDIDATE's tokens at the
	// BASELINE's rates. What moves from the baseline's own cost is volume;
	// what is left is rate. They sum to the total by construction.
	//
	// The baseline is the FIRST candidate on the command line, so the
	// incumbent goes first and everything else is read against it.
	if len(order) > 1 && len(baselineName) > 0 {
		base := byCand[baselineName]
		if base != nil && base.n > 0 {
			basePricing, okBase := PricingFor(base.provider, base.model)
			if okBase {
				t.Log("")
				t.Logf("por qué cambia el coste frente a %s (por incidente)", baselineName)
				t.Logf("  %-28s %12s %12s %12s", "modelo", "por tokens", "por tarifa", "total")
				baseCost := base.cost / float64(base.n)
				for _, name := range order {
					if name == baselineName {
						continue
					}
					a := byCand[name]
					if a.n == 0 {
						continue
					}
					perInc := Usage{
						InputTokens:         a.usage.InputTokens / a.n,
						OutputTokens:        a.usage.OutputTokens / a.n,
						CacheReadTokens:     a.usage.CacheReadTokens / a.n,
						CacheCreationTokens: a.usage.CacheCreationTokens / a.n,
					}
					atBaseRates := EstimateUSD(perInc, basePricing)
					own := a.cost / float64(a.n)
					volume := atBaseRates - baseCost
					rate := own - atBaseRates
					t.Logf("  %-28s %12s %12s %12s", name,
						fmt.Sprintf("%+.4f", volume),
						fmt.Sprintf("%+.4f", rate),
						fmt.Sprintf("%+.4f", volume+rate))
				}
				t.Log("  (negativo = más barato que la base; las dos columnas suman el total)")
			}
		}
	}

	t.Log("")
	t.Log("score: 100 = nombra la causa y no cae en el señuelo. <60 = conclusión equivocada.")

	// The transcript is the part a comparison document actually quotes.
	if out := os.Getenv("KUBEBOLT_MODEL_LAB_OUT"); out != "" {
		type dump struct {
			Candidate string   `json:"candidate"`
			Incident  string   `json:"incident"`
			Score     int      `json:"score"`
			Rounds    int      `json:"rounds"`
			Tools     []string `json:"tools"`
			CostUSD   float64  `json:"costUsd"`
			ElapsedMs int64    `json:"elapsedMs"`
			Provider  string   `json:"provider"`
			Model     string   `json:"model"`
			Usage     Usage    `json:"usage"`
			Answer    string   `json:"answer"`
			Misses    []string `json:"misses"`
		}
		var ds []dump
		for _, r := range all {
			ds = append(ds, dump{r.candidate, r.incident, r.score, r.rounds,
				r.toolCalls, r.costUSD, r.elapsed.Milliseconds(),
				r.provider, r.model, r.usage, r.answer, r.misses})
		}
		b, _ := json.MarshalIndent(ds, "", "  ")
		if err := os.WriteFile(out, b, 0o644); err != nil {
			t.Logf("no pude escribir %s: %v", out, err)
		} else {
			t.Logf("transcripciones en %s", out)
		}
	}
}

// TestModelLabRegrade re-scores a saved transcript with the CURRENT rubric.
// Free, offline, and the reason the rubric can be fixed without re-running
// every candidate — which is how the "mentioning is not concluding" bug above
// was caught and corrected against the same answers that exposed it.
//
//	KUBEBOLT_MODEL_LAB_REGRADE=/tmp/modellab.json \
//	  go test ./apps/api/internal/copilot/ -tags modellab -run TestModelLabRegrade -v
func TestModelLabRegrade(t *testing.T) {
	path := os.Getenv("KUBEBOLT_MODEL_LAB_REGRADE")
	if path == "" {
		t.Skip("KUBEBOLT_MODEL_LAB_REGRADE sin definir")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no pude leer %s: %v", path, err)
	}
	var saved []struct {
		Candidate string   `json:"candidate"`
		Incident  string   `json:"incident"`
		Score     int      `json:"score"`
		Rounds    int      `json:"rounds"`
		Tools     []string `json:"tools"`
		CostUSD   float64  `json:"costUsd"`
		ElapsedMs int64    `json:"elapsedMs"`
		Provider  string   `json:"provider"`
		Model     string   `json:"model"`
		Usage     Usage    `json:"usage"`
		Answer    string   `json:"answer"`
	}
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatalf("JSON inesperado: %v", err)
	}
	byName := map[string]frozenIncident{}
	for _, inc := range labIncidents {
		byName[inc.name] = inc
	}
	var all []labResult
	for _, s := range saved {
		inc, ok := byName[s.Incident]
		if !ok {
			t.Logf("incidente %q ya no está en el catálogo — lo salto", s.Incident)
			continue
		}
		score, misses, decoy := grade(s.Answer, inc)
		if score != s.Score {
			t.Logf("re-puntuado %-28s %-34s %d -> %d", s.Candidate, s.Incident, s.Score, score)
		}
		all = append(all, labResult{
			candidate: s.Candidate, incident: s.Incident, score: score,
			rounds: s.Rounds, toolCalls: s.Tools, costUSD: s.CostUSD,
			elapsed:  time.Duration(s.ElapsedMs) * time.Millisecond,
			provider: s.Provider, model: s.Model, usage: s.Usage,
			misses: misses, fellForDecoy: decoy,
		})
	}
	baseline := ""
	if len(all) > 0 {
		baseline = all[0].candidate
	}
	report(t, all, baseline)
}
