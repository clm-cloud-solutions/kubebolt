package copilot

import (
	"strings"
	"testing"
)

func TestDetectLanguage(t *testing.T) {
	for text, want := range map[string]string{
		"oomy-killer en kobi-incident-lab se reinicia sin parar. ¿Qué está pasando?": "es",
		"Propón la solución": "es",
		"Borra el deployment scaled-down de kobi-incident-lab, ya no lo usamos.":             "es",
		"broken-image in kobi-incident-lab won't start since the last change. What's wrong?": "en",
		"Write a LinkedIn post announcing our new Kubernetes monitoring product.":            "en",
		"Propose the fix":                "en",
		"Puedes escalar a 0 a oomy-app?": "es",
		"ok":                             "",
		"kubectl get pods":               "",
	} {
		if got := DetectLanguage(text); got != want {
			t.Errorf("DetectLanguage(%q) = %q, want %q", text, got, want)
		}
	}
}

// A message that shows no language keeps the one the operator wrote in last.
func TestOperatorLanguageKeepsTheLastOneShown(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: "broken-image won't start since the last change. What's wrong?"},
		{Role: RoleAssistant, Content: "The image tag does not exist."},
		{Role: RoleUser, ToolResults: []ToolResult{{Content: "x"}}},
	}
	if got := OperatorLanguage(msgs); got != "en" {
		t.Errorf("got %q", got)
	}
}

// The notes name the language and say the zone is not it; no block without one.
func TestSessionNotesSayTheOperatorLanguage(t *testing.T) {
	in := SessionNotesInput{ClusterName: "c", ClientTimezone: "Europe/Madrid", OperatorLanguage: "en"}
	notes := SessionNotes(in)
	if !strings.Contains(notes, "The operator writes in English") || !strings.Contains(notes, "not their language") {
		t.Errorf("notes: %s", notes)
	}
	in.OperatorLanguage = ""
	if strings.Contains(SessionNotes(in), "# Operator language") {
		t.Error("a block without a language")
	}
}

// The UI's English follow-up of a stalled action does not turn a Spanish
// conversation English, nor does a short reply after it; a conversation the
// UI opened has only the UI's text, and a cluster switch carries the
// operator's own question.
func TestOperatorLanguageSkipsTheUIsOwnText(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: "Puedes escalar a 0 a oomy-app?"},
		{Role: RoleAssistant, Content: "He preparado la propuesta."},
		{Role: RoleUser, Trigger: "action_stalled", Content: "I ran the action `scale_workload` on autopilot-demo/oomy-app (deployments) and it had not converged after 30s."},
	}
	if got := OperatorLanguage(msgs); got != "es" {
		t.Errorf("after the stalled follow-up: got %q", got)
	}
	msgs = append(msgs, Message{Role: RoleAssistant, Content: "It converged."}, Message{Role: RoleUser, Content: "ok"})
	if got := OperatorLanguage(msgs); got != "es" {
		t.Errorf("after a short reply: got %q", got)
	}
	opened := []Message{{Role: RoleUser, Trigger: "insight", Content: "Diagnose this insight in detail and recommend a fix."}}
	if got := OperatorLanguage(opened); got != "en" {
		t.Errorf("conversation opened by a button: got %q", got)
	}
	switched := []Message{{Role: RoleUser, Trigger: "cluster_switch", Content: "¿Por qué shop-web devuelve 503?"}}
	if got := OperatorLanguage(switched); got != "es" {
		t.Errorf("cluster switch: got %q", got)
	}
	// Words that tell no language leave it to the model, not to the UI's text.
	unclear := []Message{
		{Role: RoleUser, Content: "oomy-app 0"},
		{Role: RoleUser, Trigger: "action_stalled", Content: "I ran the action and it did not converge."},
	}
	if got := OperatorLanguage(unclear); got != "" {
		t.Errorf("unclear operator text: got %q", got)
	}
}

// The handoff note a switch carries is Kobi's, not the operator's.
func TestOperatorLanguageSkipsTheHandoffNote(t *testing.T) {
	msg := "(From cluster kubeshop-back: In kubeshop-back, shop-web is not present. It is in kubeshop-front (Deployment, 2 pods, Service) and the insight history shows no 503 for it.)\n\nHace unos minutos, alrededor de las 04:43, shop-web devolvió errores 503. ¿Qué pasó?"
	if got := OperatorLanguage([]Message{{Role: RoleUser, Content: msg}}); got != "es" {
		t.Errorf("got %q", got)
	}
	fleet := "(From the fleet view: two criticals on prod.)\n\nwhat are the two criticals?"
	if got := OperatorLanguage([]Message{{Role: RoleUser, Content: fleet}}); got != "en" {
		t.Errorf("fleet handoff: got %q", got)
	}
}
