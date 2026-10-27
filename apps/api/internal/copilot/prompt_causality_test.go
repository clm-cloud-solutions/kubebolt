package copilot

import (
	"github.com/kubebolt/kubebolt/apps/api/internal/skills"
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
	// How to read the episodes lives in the incident-history skill, which
	// the prompt sends Kobi to; the anchors hold in either.
	flat := strings.ToLower(strings.Join(strings.Fields(p+" "+skillBody(t, "incident-history")), " "))

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
	flat := strings.ToLower(strings.Join(strings.Fields(BuildSystemPrompt()+" "+skillBody(t, "security-findings")), " "))
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
	// The fleet procedure lives in the fleet-and-switch skill, which the
	// prompt sends Kobi to; the anchors hold in either.
	flat := strings.ToLower(strings.Join(strings.Fields(BuildSystemPrompt()+" "+fleetSkillBody(t)), " "))
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
	flat := strings.ToLower(strings.Join(strings.Fields(BuildSystemPrompt()+" "+fleetSkillBody(t)), " "))
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

// The 2026-09-24 miss: Kobi answered that it could not find the external host
// behind orders-api while the Service its DB_HOST named — an ExternalName to
// the managed database — was one read away with a tool it had. A next step
// the model can take itself is a call to make before answering, not advice.
func TestPromptTakesTheNextReadItself(t *testing.T) {
	flat := strings.ToLower(strings.Join(strings.Fields(BuildSystemPrompt()), " "))
	if !strings.Contains(flat, `would end with "the next step is to read x"`) {
		t.Error("does not name the ending it is meant to catch")
	}
	if !strings.Contains(flat, "make that call now, within the cap above, and answer with what it shows") {
		t.Error("does not tell the model to do the read before answering, within the tool-call cap")
	}
	if !strings.Contains(flat, "leave the operator only the steps you cannot take yourself") {
		t.Error("does not keep the operator's next steps to what the model cannot do")
	}
}

// Two slips from a real incident (2026-09-24): Kobi said the timeouts ran
// "since 16:06 UTC" — the oldest line of the log tail it read, not the start
// (the database had stopped two hours earlier) — and the prompt claimed every
// Kubernetes timestamp is UTC while the tool output mixed "Z" with "+02:00".
func TestPromptReadsTimesAndLogTailsCarefully(t *testing.T) {
	p := BuildSystemPrompt()
	for _, want := range []string{
		"Put two times in the same zone before you order them or compute a gap",
		"its oldest line is where your window starts, not when the problem started",
		`Say "at least since <time>"`,
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	if strings.Contains(p, "Kubernetes timestamps are UTC") {
		t.Error("prompt still claims every Kubernetes timestamp is UTC")
	}
}

// The 2026-09-25 case (kubeshop-back): the Azure activity log answered in UTC
// (the server started between 18:28:22Z and 18:43:50Z), KubeBolt's own times
// came with the API host's offset (+02:00), and the Now block said
// Europe/Madrid. Kobi wrote "01:39 CEST" (right) and "20:33 UTC on 24
// September": it converted 18:33Z to the operator's zone and kept the "UTC"
// label, a two-hour error. "Label every time you write with its zone" let each
// time keep its own zone and said nothing about the label after a conversion.
func TestPromptWritesTimesInTheOperatorsZone(t *testing.T) {
	p := BuildSystemPrompt()
	for _, want := range []string{
		"Write every time in one zone: the operator's, from the Now block, named for the offset shown there (CEST when Europe/Madrid is at +02:00, CET at +01:00; the offset itself, e.g. UTC-03:00, when the zone has no common abbreviation)",
		`or as relative time ("3 minutes ago")`,
		"When the Now block shows UTC only, write UTC.",
		`Convert by the offset the value carries ("Z" is UTC, "+02:00" is two hours ahead of UTC), date included — name the day when it is not today.`,
		"A converted time takes the label of the zone it is now in, never the label of the zone it came from: 18:33Z is 20:33 CEST, not 20:33 UTC.",
		// The voice layers ahead of this bullet (kobi-copilot.md, kobi-few-shots.md)
		// show times in UTC; this sentence keeps a worked example from outvoting the rule.
		`Example times elsewhere in this prompt ("14:22 UTC") only show the form; the times in your answer follow this rule.`,
		"Put two times in the same zone before you order them or compute a gap.",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	// 2026-09-25 is past the equinox and Madrid is still at +02:00: the label
	// follows the offset the Now block shows, not the season.
	if strings.Contains(p, "in summer") {
		t.Error("the zone label is tied to a season instead of the offset")
	}
	// The permissive rule is replaced, not stacked under the new one.
	if strings.Contains(strings.ToLower(p), "label every time you write with its zone") {
		t.Error("prompt still lets every time keep its own zone")
	}
	if n := strings.Count(p, "Times come in more than one zone"); n != 1 {
		t.Errorf("the time bullet appears %d times, want once", n)
	}
	// The log-tail bullet is a separate rule and stays word for word.
	const tail = `- A log tail holds only its last lines: its oldest line is where your window starts, not when the problem started. Say "at least since <time>", and take the real start from events, conditions, insights (first seen), metrics, the createdAt of the revision that introduced the cause, or the cloud activity log.`
	if !strings.Contains(p, tail) {
		t.Error("the log-tail bullet changed")
	}
}

// The 2026-09-24 lab: "¿Qué está pasando con shop-web?" carried no namespace.
// Kobi fetched deployments/default/shop-web, got NotFound and answered that it
// did not exist. The rule it followed said to call get_resource_* FIRST and to
// stop on NotFound: with no namespace it had to guess one, and the stop
// forbade the search that finds it.
func TestPromptFindsAResourceNamedWithoutANamespace(t *testing.T) {
	flat := flatPrompt()
	for _, want := range []string{
		"when the operator names a specific resource, fetch it before you describe it",
		"never describe, diagnose, or quote logs / status / spec for a resource you have not fetched",
		"find it with search_resources first — never assume `default`",
		`a notfound in a namespace you assumed means search, not "it does not exist"`,
		"current_view's namespace is where to look first, not proof it lives there",
		"if you assumed the namespace, search_resources before concluding anything",
		"say plainly that it does not exist (or was deleted) only when the operator named the namespace or the search finds nothing",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	for _, gone := range []string{
		"call the matching get_resource_* tool first",
		"if the tool returns notfound, say plainly that it does not exist and stop",
		"404 (not found): the resource does not exist",
	} {
		if strings.Contains(flat, gone) {
			t.Errorf("prompt still says %q", gone)
		}
	}
	// The stop survives only as the end of a search that found nothing — and
	// search_resources reads the selected cluster only (shop-web ran in
	// another cluster of the org).
	const stop = "say plainly that it does not exist in this cluster and stop"
	if n := strings.Count(flat, stop); n != 1 {
		t.Fatalf("%q appears %d times, want once", stop, n)
	}
	if i := strings.Index(flat, stop); !strings.HasSuffix(flat[:i], "only when the search finds nothing ") {
		t.Error("the stop on a missing resource is no longer conditioned on an empty search")
	}
}

// Same lab, "¿Qué pasa con orders-api?": four calls in (search, detail,
// insights, events) Kobi had seen FAULT_MODE=error in the spec and left the
// logs "para el siguiente paso" — the prompt capped a message at 3–4 calls and
// the next-read rule said "within the cap above". A diagnosis needs room for
// the read that decides it.
func TestPromptBudgetsADiagnosisBeyondALookup(t *testing.T) {
	flat := flatPrompt()
	for _, want := range []string{
		"a lookup (what, where, how many) takes one to three tool calls",
		"a diagnosis (why something fails, what is happening to a workload) up to about eight",
		"past that, answer with what you have and name what is left",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	for _, gone := range []string{
		"cap yourself at 3–4 tool calls",
		"3–4 tool calls",
		"ask the operator to narrow the question",
	} {
		if strings.Contains(flat, gone) {
			t.Errorf("prompt still says %q", gone)
		}
	}
	// "The cap above" in the next-read rule must be this budget.
	budget := strings.Index(flat, "up to about eight")
	nudge := strings.Index(flat, "make that call now, within the cap above")
	if budget < 0 || nudge < 0 || budget > nudge {
		t.Error("the next-read rule does not follow the diagnosis budget it refers to")
	}
}

// And the diagnosis itself. Revision 2 only added FAULT_MODE=error: its pods
// passed /healthz and every GET / answered 500. Kobi called the workload
// healthy at 2/2 Ready, then claimed revision 2 "kept" FAULT_MODE and advised
// against rolling back to revision 1 because it showed 0 ready — the count of a
// revision already replaced, not how it ran.
func TestPromptReadsServingAndRevisionsFromEvidence(t *testing.T) {
	flat := flatPrompt()
	for _, want := range []string{
		"ready means the readiness probe passes, not that requests succeed",
		"before calling a workload the operator asks about healthy, read its recent logs",
		"rollouts, recoveries and first errors go on that one timeline, in one zone, before you say which caused which",
		"a change explains only what began after it",
		"what a previous revision had comes from get_workload_history (each revision's changes), never from assumption",
		"a replaced revision's 0 ready replicas is not evidence it was unhealthy",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	// Both belong to the diagnosis guidance, not to a tool's footnote.
	in := func(needle, from, to string) bool {
		i, a, b := strings.Index(flat, needle), strings.Index(flat, from), strings.Index(flat, to)
		return a >= 0 && a < i && i < b
	}
	if !in("ready means the readiness probe passes", "## troubleshooting methodology", "### external dependencies") {
		t.Error("the Ready rule is not in the troubleshooting methodology")
	}
	if !in("a change explains only what began after it", "### root cause must precede its effect", "two more filters") {
		t.Error("the timeline rule is not with the cause-precedes-effect rule")
	}
}

// The 2026-09-25 case (kubeshop-back): Kobi found FAULT_MODE=error in
// orders-api's env in one turn but never called get_workload_history, so it
// did not say revision 2 (created 18:50:33Z) introduced it — revision 1 did
// not have it — dated the start "at least since 22:25 UTC" from its log
// window, and did not offer the rollback. The rollout events had already
// expired; the revision's createdAt was the only durable start.
func TestPromptDatesASpecCauseByItsRelease(t *testing.T) {
	flat := flatPrompt()
	const rule = "**a cause in the workload's own spec has a release.**"
	for _, want := range []string{
		rule,
		"when the cause is a value in the workload's spec (an env var, image, command, probe, resources), call get_workload_history",
		"the revision whose changes introduced it dates the start (its createdat — rollout events expire within about an hour)",
		"when an earlier revision did not have it, it is a bad release: say so, and offer rolling back to that revision first",
		// Not onto a revision that was failing too (the healthy-prior-revision
		// condition the investigator inherits from ROLLBACK IS IN-SCOPE)...
		"unless the events and logs of its time show it failing too",
		// ...and by number: a rollout restart on top of the bad release leaves
		// the value in the previous revision, so the default target fixes nothing.
		"passing it as torevision, since the previous revision can still carry the value",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	if n := strings.Count(flat, rule); n != 1 {
		t.Errorf("the rule appears %d times, want once", n)
	}
	// Diagnosis guidance, right after the Ready rule it sits beside.
	i, ready, ext := strings.Index(flat, rule), strings.Index(flat, "**ready is not serving.**"), strings.Index(flat, "### external dependencies")
	if !(ready >= 0 && ready < i && i < ext) {
		t.Error("the rule is not in the troubleshooting methodology next to the Ready rule")
	}

	// The log tail's list of where the real start comes from names it too.
	tailAt, truncAt := strings.Index(flat, "a log tail holds only its last lines"), strings.Index(flat, "if a tool result is truncated")
	if tailAt < 0 || truncAt < tailAt {
		t.Fatal("the log-tail bullet moved")
	}
	if !strings.Contains(flat[tailAt:truncAt], "take the real start from events, conditions, insights (first seen), metrics, the createdat of the revision that introduced the cause, or the cloud activity log") {
		t.Error("the log-tail bullet does not name the revision's createdAt as a source of the real start")
	}
}

// search_resources' own description must not contradict the rule: the type
// can be known and the namespace still missing.
func TestSearchResourcesToolCoversAMissingNamespace(t *testing.T) {
	for _, td := range ToolDefinitions() {
		if td.Name == "search_resources" {
			if !strings.Contains(td.Description, "Use when the user names a resource without its type or namespace") {
				t.Errorf("search_resources description = %q", td.Description)
			}
			return
		}
	}
	t.Fatal("search_resources is not a tool")
}

func fleetSkillBody(t *testing.T) string {
	t.Helper()
	return skillBody(t, "fleet-and-switch")
}

func skillBody(t *testing.T, name string) string {
	t.Helper()
	sk, ok := skills.Get(name)
	if !ok {
		t.Fatalf("no %s skill", name)
	}
	return sk.Body
}
