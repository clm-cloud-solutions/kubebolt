package copilot

import (
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The prompt half of the 2026-09-16 miss. Kobi had the decisive evidence in
// context — a node whose reported age equalled the incident's age — and still
// named a cause that its own answer placed 90 minutes AFTER the failures. No
// extra tool fixes that; the instruction has to say it.
func TestPromptTeachesTemporalPrecedence(t *testing.T) {
	p := strings.ToLower(BuildSystemPrompt())
	required := map[string]string{
		"precede":                          "que la causa debe preceder al efecto",
		"contradicts your own conclusion":  "la prohibición de escribir una línea temporal que contradice la conclusión",
		"loudest signal is not the causal": "que la señal más ruidosa no es la causal",
		"creationtimestamp":                "leer la edad del nodo al explicar evictions",
		"correlates with":                  "el escape honesto cuando sólo hay correlación",
	}
	for needle, why := range required {
		if !strings.Contains(p, needle) {
			t.Errorf("el prompt ya no enseña %s (falta %q)", why, needle)
		}
	}
	// The mechanism filter is what rejects "a memory spike filled the disk".
	if !strings.Contains(p, "memory exhaustion does not consume node disk") {
		t.Error("falta el ejemplo de mecanismo que no cuadra (memoria vs disco)")
	}
}

// A burst is the shared cause. Reaching for it after building a per-workload
// theory is reaching for it too late, so the prompt must order it first.
func TestPromptOrdersTheBurstToolFirst(t *testing.T) {
	p := BuildSystemPrompt()
	if !strings.Contains(p, "get_operational_episodes") {
		t.Fatal("el prompt no nombra get_operational_episodes: la tool existiría sin que el modelo sepa cuándo usarla")
	}
	low := strings.ToLower(p)
	if !strings.Contains(low, "before diagnosing any of them individually") {
		t.Error("no ordena consultarla ANTES del diagnóstico por workload")
	}
	// Absence of the capability must not read as absence of a burst — the same
	// distinction the tool's own error message makes.
	idx := strings.Index(p, "get_operational_episodes")
	tail := strings.ToLower(p[idx:])
	if !strings.Contains(tail, "not evidence that no burst happened") {
		t.Error("no distingue «no puedo verlas» de «no hubo ninguna»")
	}
}

// Prompt/tool drift: the prompt tells the model to call things by name, and a
// name that no longer exists is an instruction to hallucinate a tool call.
// Renaming a tool without touching the prompt is the cheap way to break Kobi.
func TestPromptOnlyNamesToolsThatExist(t *testing.T) {
	known := make(map[string]bool, len(ToolDefinitions()))
	for _, d := range ToolDefinitions() {
		known[d.Name] = true
	}
	// Globs like get_resource_* are deliberate prose, not a tool reference.
	ref := regexp.MustCompile(`\b(?:get|list|search|propose)_[a-z]+(?:_[a-z]+)*\b`)
	prompt := BuildSystemPrompt()

	var missing []string
	for _, m := range ref.FindAllString(prompt, -1) {
		if known[m] || strings.HasSuffix(m, "_") {
			continue
		}
		if i := strings.Index(prompt, m+"*"); i >= 0 {
			continue // the glob form
		}
		missing = append(missing, m)
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("el prompt nombra tools inexistentes: %v", uniqueStrings(missing))
	}
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	out := in[:0]
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// get_insights is the present and get_insight_episodes is the past. A model
// that does not know the difference answers "I can't tell you, the cluster is
// down" to the one question history exists to answer.
func TestPromptSeparatesPresentFromHistory(t *testing.T) {
	p := BuildSystemPrompt()
	for _, name := range []string{"get_insight_episodes", "get_insight_episode"} {
		if !strings.Contains(p, name) {
			t.Fatalf("the prompt never names %s: the tool would exist without the model knowing when to reach for it", name)
		}
	}
	// The prompt is hard-wrapped, so a phrase can straddle a newline. Compare
	// against collapsed whitespace or the test breaks on a reflow that changed
	// nothing.
	flat := strings.ToLower(strings.Join(strings.Fields(p), " "))

	if !strings.Contains(flat, "reads stored history, so it answers when the cluster does not") {
		t.Error("does not say history answers for a dead cluster — the case it exists for")
	}
	if !strings.Contains(flat, "never a reason to say you cannot know") {
		t.Error("does not forbid 'the cluster is unreachable so I cannot know'")
	}
	if !strings.Contains(flat, "check resolutionkind and flapcount before proposing") {
		t.Error("does not tell the model to read how it ended last time before proposing")
	}

	// The honest limit. resolutionKind never carries a human-remediated value
	// in the current code, so its absence must not be read as "nobody acted".
	idx := strings.Index(flat, "one honest limit on resolutionkind")
	if idx < 0 {
		t.Fatal("the caveat about resolutionKind is gone")
	}
	tail := flat[idx:]
	if !strings.Contains(tail, "never written by the current code") {
		t.Error("does not warn that the remediated value is never written")
	}
	if !strings.Contains(tail, "tells you nothing about whether a person acted") {
		t.Error("does not forbid inferring 'nobody intervened' from the field")
	}
}

// The causality rules say memory does not consume node disk. Without naming
// the tool that DOES answer, that is a prohibition with no alternative — and
// the 16-Sep answer was produced by a model that had no disk tool at all.
func TestPromptPointsDiskQuestionsAtTheTool(t *testing.T) {
	flat := strings.ToLower(strings.Join(strings.Fields(BuildSystemPrompt()), " "))
	if !strings.Contains(flat, "kind=node and metric=filesystem") {
		t.Error("does not name the call that answers a disk question")
	}
	for _, cue := range []string{"diskpressure", "ephemeral-storage"} {
		if !strings.Contains(flat, cue) {
			t.Errorf("does not connect the tool to %q, the words the symptom arrives in", cue)
		}
	}
	// The mechanism that must not be invented: a PVC is not node storage.
	if !strings.Contains(flat, "a pvc does not consume the node's ephemeral storage") {
		t.Error("does not rule out the PVC-fills-the-node mechanism")
	}
}

// A clean insight list is not a secure cluster: the scanners are a separate
// pillar, and Kobi could not see it at all until get_findings landed.
func TestPromptCoversTheSecurityPillar(t *testing.T) {
	flat := strings.ToLower(strings.Join(strings.Fields(BuildSystemPrompt()), " "))
	if !strings.Contains(flat, "get_findings") {
		t.Fatal("the prompt never names get_findings")
	}
	// Posture before rows — the reason the tool defaults to a summary.
	if !strings.Contains(flat, "call it with no filter first") {
		t.Error("does not tell the model to ask for the posture before the rows")
	}
	// The image is the unit of repair.
	if !strings.Contains(flat, "two workloads running the same image are one fix") {
		t.Error("does not say the image is what gets rebuilt")
	}
	// The failure mode that matters most in a security answer.
	if !strings.Contains(flat, "reporting a cluster as secure because you could not look") {
		t.Error("does not forbid reading an unavailable scanner as a clean cluster")
	}
}

// Kobi answers about one cluster everywhere else; from Home or Fleet that is
// the wrong altitude and it needs to know which tool changes it.
func TestPromptKnowsTheFleetAltitude(t *testing.T) {
	flat := strings.ToLower(strings.Join(strings.Fields(BuildSystemPrompt()), " "))
	if !strings.Contains(flat, "get_fleet_summary") {
		t.Fatal("the prompt never names get_fleet_summary")
	}
	if !strings.Contains(flat, "list_clusters gives names and connectivity, not health") {
		t.Error("does not say why list_clusters is not the answer to 'which cluster is worst'")
	}
	if !strings.Contains(flat, "clusters that are unreachable are still counted") {
		t.Error("does not say the fleet view survives a dead cluster, which is when it matters")
	}
}

// The gap this closes was observed verbatim: Kobi answered "Cambia a ese
// cluster y los consulto directamente" — asking the operator to do the work
// and then retype the question, which switching had already thrown away.
func TestPromptOffersTheSwitchInsteadOfAskingForIt(t *testing.T) {
	flat := strings.ToLower(strings.Join(strings.Fields(BuildSystemPrompt()), " "))
	if !strings.Contains(flat, "offer_cluster_switch") {
		t.Fatal("the prompt never names offer_cluster_switch")
	}
	if !strings.Contains(flat, "do not ask the operator to do it") {
		t.Error("does not forbid handing the switch back to the operator")
	}
	// Why the question cannot simply be carried.
	if !strings.Contains(flat, "switching clears the transcript") {
		t.Error("does not explain why the question has to be re-asked rather than kept")
	}
	// The refusal path is an answer, not a dead end.
	if !strings.Contains(flat, "do not offer the switch") {
		t.Error("does not say what to do when the target is unreachable")
	}
}

// Field report: asked "how is my fleet?" from the lab cluster, Kobi answered the
// fleet question correctly AND put a switch card above it. Nothing had been
// asked about the other cluster. The earlier wording keyed the offer off the
// fleet being interesting elsewhere, which is true of almost every fleet
// question; the trigger has to be that THEIR question needs a tool that only
// reads the selected cluster.
func TestPromptOffersTheSwitchOnlyWhenTheQuestionNeedsIt(t *testing.T) {
	flat := strings.ToLower(strings.Join(strings.Fields(BuildSystemPrompt()), " "))
	if !strings.Contains(flat, "what triggers the offer is the question, not the state of the fleet") {
		t.Error("does not say the question is the trigger, not the fleet's state")
	}
	if !strings.Contains(flat, "another cluster being worse is not a reason to offer anything") {
		t.Error("does not rule out the case that shipped wrong")
	}
	// A concrete test the model can apply rather than a principle to weigh.
	if !strings.Contains(flat, "name the tool you would have to call over there") {
		t.Error("gives no operational test for whether to offer")
	}
	// And the counter-example, so the rule does not read as "never offer".
	if !strings.Contains(flat, "get_fleet_summary is the answer") {
		t.Error("does not contrast the fleet question that needs no switch")
	}
	// The card is self-describing; narrating it is noise.
	if !strings.Contains(flat, "do not narrate the button") {
		t.Error("does not stop the model from explaining the card it just drew")
	}
}
