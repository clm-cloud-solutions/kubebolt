package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
)

// Kobi could already tell the operator "switch to that cluster and I'll query
// it directly" — and then could not do it, and the question was lost, because
// switching clears the conversation (one conversation belongs to one cluster).
// offer_cluster_switch closes that loop.

func infos() []cluster.ClusterInfo {
	return []cluster.ClusterInfo{
		{Name: "kind-kubebolt-lab", Context: "kind-kubebolt-lab", Status: "connected", Active: true},
		{Name: "kind-kubebolt-dev", Context: "agent:uid-dev", DisplayName: "kind-kubebolt-dev (via agent)",
			ClusterID: "uid-dev", Status: "connected"},
		{Name: "prod-down", Context: "agent:uid-down", DisplayName: "prod", ClusterID: "uid-down",
			Status: "disconnected", Error: "no agent connected yet"},
	}
}

// Models name a cluster however the operator did — the display name, the
// context, or the uid from a fleet row. A near-miss on any of those would be a
// dead end for a question the tool exists to rescue.
func TestSwitchTarget_MatchesEveryNameTheModelMightUse(t *testing.T) {
	for _, want := range []string{
		"kind-kubebolt-dev (via agent)", "kind-kubebolt-dev", "agent:uid-dev", "uid-dev",
		"KIND-KUBEBOLT-DEV (VIA AGENT)", "  uid-dev  ",
	} {
		got, ok := findSwitchTarget(infos(), want, "")
		if !ok {
			t.Errorf("%q did not match any cluster", want)
			continue
		}
		if got.ClusterID != "uid-dev" {
			t.Errorf("%q matched %q", want, got.Context)
		}
	}
	if _, ok := findSwitchTarget(infos(), "does-not-exist", ""); ok {
		t.Error("matched a cluster that is not there")
	}
}

func TestSwitchLabel_PrefersWhatAHumanCallsIt(t *testing.T) {
	if got := switchLabel(cluster.ClusterInfo{Name: "n", Context: "c", DisplayName: "friendly"}); got != "friendly" {
		t.Errorf("label = %q", got)
	}
	if got := switchLabel(cluster.ClusterInfo{Context: "c"}); got != "c" {
		t.Errorf("label = %q, want the context as the last resort", got)
	}
}

func offerSwitch(t *testing.T, clusters []cluster.ClusterInfo, in string) (map[string]any, bool) {
	t.Helper()
	return offerSwitchFrom(t, clusters, "", in)
}

// offerSwitchFrom offers the switch from a conversation that runs on the
// context current.
func offerSwitchFrom(t *testing.T, clusters []cluster.ClusterInfo, current, in string) (map[string]any, bool) {
	t.Helper()
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(in), &args); err != nil {
		t.Fatal(err)
	}
	content, failed := buildSwitchOffer(clusters, current, args)
	var payload map[string]any
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, content)
	}
	return payload, failed
}

func TestOfferSwitch_ProposesWhenTheTargetIsConnected(t *testing.T) {
	prop, failed := offerSwitch(t, infos(), `{"cluster":"kind-kubebolt-dev (via agent)","question":"Cuales son los dos criticos?","established":"la vista de flota reporta 2 criticos"}`)
	if failed {
		t.Fatalf("refused a connected cluster: %v", prop)
	}
	if prop["kind"] != "action_proposal" || prop["action"] != "switch_cluster" {
		t.Fatalf("not a switch proposal: %v", prop)
	}
	params, _ := prop["params"].(map[string]any)
	// The CONTEXT is what the switch endpoint takes, not the display name.
	if params["cluster"] != "agent:uid-dev" {
		t.Errorf("cluster param = %v, want the context", params["cluster"])
	}
	// The question travels verbatim: it is what the new conversation opens with.
	if params["question"] != "Cuales son los dos criticos?" {
		t.Errorf("question = %v", params["question"])
	}
	if params["established"] != "la vista de flota reporta 2 criticos" {
		t.Errorf("established = %v", params["established"])
	}
	if prop["reversible"] != true || prop["risk"] != "low" {
		t.Errorf("a view change is low risk and reversible: %v / %v", prop["risk"], prop["reversible"])
	}
}

// The decision that matters: validate BEFORE offering. The card is one click —
// it switches and asks immediately — so offering a switch to a cluster that
// cannot answer lands the operator on the waiting-for-agent screen with their
// question in mid-air.
func TestOfferSwitch_RefusesAnUnreachableTargetAndSaysWhy(t *testing.T) {
	payload, failed := offerSwitch(t, infos(), `{"cluster":"prod","question":"que pasa ahi?"}`)
	if !failed {
		t.Fatal("offered a switch to a disconnected cluster")
	}
	if payload["kind"] == "action_proposal" {
		t.Error("emitted a proposal card for an unreachable cluster")
	}
	if payload["offerable"] != false {
		t.Errorf("offerable = %v, want an explicit false", payload["offerable"])
	}
	// The operator needs the reason, not just a refusal.
	for _, cue := range []string{"disconnected", "no agent connected yet"} {
		if !strings.Contains(fmt.Sprint(payload), cue) {
			t.Errorf("the refusal does not carry %q: %v", cue, payload)
		}
	}
	if !strings.Contains(fmt.Sprint(payload), "do not offer the switch") {
		t.Error("does not tell the model what to do instead")
	}
}

func TestOfferSwitch_UnknownClusterListsWhatThereIs(t *testing.T) {
	payload, failed := offerSwitch(t, infos(), `{"cluster":"typo-cluster","question":"q"}`)
	if !failed {
		t.Fatal("accepted a cluster that does not exist")
	}
	// A near-miss should be one correction away, not a dead end.
	for _, cue := range []string{"kind-kubebolt-lab", "prod"} {
		if !strings.Contains(fmt.Sprint(payload), cue) {
			t.Errorf("the error does not list %q as an available cluster", cue)
		}
	}
}

func TestOfferSwitch_RequiresBothClusterAndQuestion(t *testing.T) {
	for _, in := range []string{`{}`, `{"cluster":"kind-kubebolt-lab"}`, `{"question":"q"}`} {
		_, failed := offerSwitch(t, infos(), in)
		if !failed {
			t.Errorf("%s was accepted; without the question there is nothing to re-ask", in)
		}
	}
}

// It must NOT be withheld when actions are disabled: it mutates nothing, and
// the read-only operator is the one who most needs it — they cannot do
// anything else from the chat.
func TestOfferSwitch_SurvivesActionsBeingDisabled(t *testing.T) {
	var found bool
	for _, d := range GovernedToolDefinitions(false, false) {
		if d.Name == "offer_cluster_switch" {
			found = true
		}
	}
	if !found {
		t.Fatal("withheld with the mutation tools; it changes the view, not the cluster")
	}
	if strings.HasPrefix("offer_cluster_switch", "propose_") {
		t.Error("the propose_ prefix would have it withheld")
	}
}

// Two ways into another cluster, two questions. From the fleet view the
// operator's question is the whole question. Following a dependency, the new
// conversation does not run the caller: on 2026-09-25 it carried only "the
// store is slow", looked for shop-web in kubeshop-back and stopped. There the
// question names the workloads to investigate, by namespace/name.
func TestOfferSwitch_QuestionNamesWhatToInvestigate(t *testing.T) {
	var def ToolDefinition
	for _, d := range ToolDefinitions() {
		if d.Name == "offer_cluster_switch" {
			def = d
		}
	}
	for _, want := range []string{
		"(get_fleet_summary, or an inFleet row of get_external_connections)",
		"load_skill fleet-and-switch for what the question carries",
	} {
		if !strings.Contains(def.Description, want) {
			t.Errorf("offer_cluster_switch description lacks %q:\n%s", want, def.Description)
		}
	}
	// What the question carries lives in the skill the description points to.
	skill := strings.Join(strings.Fields(skillBody(t, "fleet-and-switch")), " ")
	for _, want := range []string{
		"From the fleet view, pass the question the operator actually asked, not a paraphrase",
		"Following a dependency there, the question names what to investigate, since that cluster does not run the caller",
		"the operator's words first, then the symptom, the caller and the cluster it runs in",
		"every workload there the symptom may come from, as namespace/name from inFleet",
	} {
		if !strings.Contains(skill, want) {
			t.Errorf("fleet-and-switch skill lacks %q", want)
		}
	}
	props, _ := def.InputSchema["properties"].(map[string]interface{})
	q, _ := props["question"].(map[string]interface{})
	desc, _ := q["description"].(string)
	for _, want := range []string{
		"in the operator's own words",
		"Following a dependency, add after them the symptom, the caller with its cluster, and the workloads there to investigate, as namespace/name",
	} {
		if !strings.Contains(desc, want) {
			t.Errorf("question property lacks %q: %q", want, desc)
		}
	}
}

// coredns-override, 2026-10-20: the frontend quoted the coredns-custom block
// with its fallthrough; the one line it carried dropped it, and the backend,
// which cannot read that ConfigMap, proposed the block without it — applied
// as written, every other name in the frontend cluster stops resolving. The
// note carries verbatim the config a fix may edit. In both re-runs that
// evening the word arrived, on one line, where CoreDNS reads it as one more
// hostname; asking for the line breaks too changed nothing, so the edit rule
// in the prompt is what keeps the fix safe.
func TestOfferSwitch_EstablishedCarriesTheConfigVerbatim(t *testing.T) {
	var def ToolDefinition
	for _, d := range ToolDefinitions() {
		if d.Name == "offer_cluster_switch" {
			def = d
		}
	}
	props, _ := def.InputSchema["properties"].(map[string]interface{})
	e, _ := props["established"].(map[string]interface{})
	desc, _ := e["description"].(string)
	if strings.Contains(desc, "One line") || !strings.Contains(desc, "verbatim any config a fix may edit") {
		t.Errorf("established property does not ask for the config verbatim: %q", desc)
	}
	skill := strings.Join(strings.Fields(skillBody(t, "fleet-and-switch")), " ")
	if strings.Contains(skill, "`established` is one line") || !strings.Contains(skill, "Copy verbatim any configuration a fix may have to edit") {
		t.Errorf("fleet-and-switch does not ask for the config verbatim")
	}
}

// A conversation opened by a switch to kubeshop-back once offered to switch to
// kubeshop-back — its own cluster — without a single read (2026-09-25). A
// switch to the cluster the turn runs on is refused, however the model names
// it, and the refusal says to investigate here.
func TestOfferSwitch_RefusesTheClusterThisTurnRunsOn(t *testing.T) {
	for _, target := range []string{"kind-kubebolt-dev (via agent)", "agent:uid-dev", "uid-dev"} {
		payload, failed := offerSwitchFrom(t, infos(), "agent:uid-dev", fmt.Sprintf(`{"cluster":%q,"question":"q"}`, target))
		if !failed {
			t.Fatalf("%q: offered a switch to the cluster this turn runs on: %v", target, payload)
		}
		if payload["current"] != true || payload["offerable"] != false {
			t.Errorf("%q: refusal does not say it is the current cluster: %v", target, payload)
		}
		if msg, _ := payload["error"].(string); !strings.Contains(msg, "already on kind-kubebolt-dev (via agent)") || !strings.Contains(msg, "investigate it here") {
			t.Errorf("%q: refusal = %q", target, msg)
		}
	}
	// From another cluster the same target is still offered.
	if prop, failed := offerSwitchFrom(t, infos(), "kind-kubebolt-lab", `{"cluster":"agent:uid-dev","question":"q","established":"e"}`); failed {
		t.Fatalf("refused a switch from another cluster: %v", prop)
	}
}

// A cluster registered again keeps its old display-name row, so two clusters
// can answer to one name (the lab had two "kubeshop-back"). The one this turn
// runs on wins — and is refused — then a connected one; never a stale one
// that happens to come first.
func TestSwitchTarget_ARepeatedNamePrefersCurrentThenConnected(t *testing.T) {
	clusters := []cluster.ClusterInfo{
		{Context: "agent:old", ClusterID: "old", DisplayName: "kubeshop-back", Status: "disconnected"},
		{Context: "agent:live", ClusterID: "live", DisplayName: "kubeshop-back", Status: "connected"},
		{Context: "agent:front", ClusterID: "front", DisplayName: "kubeshop-front", Status: "connected"},
	}
	if got, _ := findSwitchTarget(clusters, "kubeshop-back", "agent:front"); got.Context != "agent:live" {
		t.Errorf("from another cluster matched %q, want the connected agent:live", got.Context)
	}
	if got, _ := findSwitchTarget(clusters, "kubeshop-back", "agent:live"); got.Context != "agent:live" {
		t.Errorf("from agent:live matched %q, want itself", got.Context)
	}
	if payload, failed := offerSwitchFrom(t, clusters, "agent:live", `{"cluster":"kubeshop-back","question":"q"}`); !failed || payload["current"] != true {
		t.Errorf("a repeated name let the current cluster be offered: %v", payload)
	}
	if prop, failed := offerSwitchFrom(t, clusters, "agent:front", `{"cluster":"kubeshop-back","question":"q","established":"e"}`); failed {
		t.Errorf("from the front, the connected kubeshop-back was not offered: %v", prop)
	} else if params, _ := prop["params"].(map[string]any); params["cluster"] != "agent:live" {
		t.Errorf("offered %v, want agent:live", params["cluster"])
	}
}

// From the backend cluster Kobi had the whole cause and offered the switch to
// the frontend without it; the frontend traced the 503s back and offered the
// way back, and the backend redid the investigation (three-tier-shop lab,
// 2026-10-10). What was found travels with the question, or there is no card.
func TestOfferSwitch_RequiresWhatWasEstablished(t *testing.T) {
	for _, in := range []string{
		`{"cluster":"kind-kubebolt-dev","question":"que paso con shop-web?"}`,
		`{"cluster":"kind-kubebolt-dev","question":"que paso con shop-web?","established":"   "}`,
	} {
		payload, failed := offerSwitch(t, infos(), in)
		if !failed || payload["kind"] == "action_proposal" {
			t.Errorf("%s offered a card with nothing established", in)
		}
		if !strings.Contains(fmt.Sprint(payload["error"]), "established is required") {
			t.Errorf("the refusal must say what is missing: %v", payload)
		}
	}
}

// After the card, Kobi wrote its answer a second time and told the operator to
// press the button. The result says what to do instead.
func TestOfferSwitch_TellsTheModelHowToEndTheTurn(t *testing.T) {
	prop, failed := offerSwitch(t, infos(), `{"cluster":"kind-kubebolt-dev","question":"que paso con shop-web?","established":"orders-api 503 01:06:01-09Z: Azure PG restart"}`)
	if failed {
		t.Fatalf("refused: %v", prop)
	}
	next, _ := prop["next"].(string)
	for _, cue := range []string{"end the turn", "Do not mention the card or the button", "do not ask whether to switch"} {
		if !strings.Contains(next, cue) {
			t.Errorf("next lacks %q: %q", cue, next)
		}
	}
}

func TestOfferSwitch_EstablishedIsRequiredInTheSchema(t *testing.T) {
	for _, d := range ToolDefinitions() {
		if d.Name != "offer_cluster_switch" {
			continue
		}
		req := fmt.Sprint(d.InputSchema["required"])
		if !strings.Contains(req, "established") {
			t.Errorf("required = %s", req)
		}
		return
	}
	t.Fatal("offer_cluster_switch is not defined")
}

// After the card, Haiku 5.5 wrote its whole answer again and narrated the card
// even with switchOfferNext telling it not to; the loop ends there instead.
func TestSwitchEndsTurn(t *testing.T) {
	answer := strings.Repeat("Los 503 de shop-web vienen de orders-api. ", 15) // > 500 runes
	card := []ToolCall{{ID: "1", Name: "offer_cluster_switch"}}
	ok := []ToolResult{{ToolCallID: "1", Content: `{"kind":"action_proposal"}`}}
	if !SwitchEndsTurn(answer, card, ok) {
		t.Error("an answer written next to the card must end the turn")
	}
	if SwitchEndsTurn("Puedo ofrecer el cambio de cluster.", card, ok) {
		t.Error("a one-line lead-in must get its round, to give the answer")
	}
	if SwitchEndsTurn(answer, card, []ToolResult{{ToolCallID: "1", Content: `{"error":"established is required"}`, IsError: true}}) {
		t.Error("a refused card must get its round, to fix the call")
	}
	both := []ToolCall{{ID: "1", Name: "offer_cluster_switch"}, {ID: "2", Name: "get_events"}}
	if SwitchEndsTurn(answer, both, append(ok, ToolResult{ToolCallID: "2"})) {
		t.Error("a round with another call must go on, to read its result")
	}
}

// The turn that ended on the card leaves its tool result last; the next
// question follows it as a second user message, which Anthropic gets as one.
func TestToAnthropicMessages_JoinsConsecutiveUserMessages(t *testing.T) {
	out := toAnthropicMessages([]Message{
		{Role: RoleUser, Content: "que paso con shop-web?"},
		{Role: RoleAssistant, Content: "respuesta", ToolCalls: []ToolCall{{ID: "1", Name: "offer_cluster_switch"}}},
		{Role: RoleUser, ToolResults: []ToolResult{{ToolCallID: "1", Content: "{}"}}},
		{Role: RoleUser, Content: "y el catalogo?"},
	})
	if len(out) != 3 {
		t.Fatalf("messages = %d, want 3 (user, assistant, user)", len(out))
	}
	last := out[2]
	if last.Role != "user" || len(last.Content) != 2 || last.Content[0].Type != "tool_result" || last.Content[1].Type != "text" {
		t.Errorf("the joined user message must carry the tool_result first, then the question: %+v", last)
	}
}

type stubClusterList struct{ list []cluster.ClusterInfo }

func (s stubClusterList) CallerClusters(context.Context) ([]cluster.ClusterInfo, bool) {
	return s.list, true
}

// list_clusters is how an MCP client with no cluster selected finds out which
// ones exist. It answered "enable the agent-proxy" to that question, because
// the connector gate sent it away before it could read the list.
func TestListClusters_AnswersWithoutAConnector(t *testing.T) {
	res := NewExecutor(nil).WithClusterList(stubClusterList{list: infos()}).ExecuteCtx(context.Background(),
		ToolCall{ID: "c1", Name: "list_clusters", Input: json.RawMessage(`{}`)})
	if res.IsError || strings.Contains(res.Content, "needsProxy") {
		t.Fatalf("hit the connector gate: %s", res.Content)
	}
	if !strings.Contains(res.Content, "kind-kubebolt-dev") {
		t.Errorf("did not list the clusters: %s", res.Content)
	}
}
