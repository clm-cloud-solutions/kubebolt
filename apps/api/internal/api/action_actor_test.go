package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/audit"
	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
	"github.com/kubebolt/kubebolt/apps/api/internal/fieldmanager"
)

// Finding #69: who made a mutation is decided from the principal. A header can
// only claim it.

func actorRequest(principal *auth.APIPrincipal, headers map[string]string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/resources/deployments/shop/web/restart", nil)
	ctx := auth.WithTenantID(r.Context(), "org-a")
	if principal != nil {
		ctx = auth.WithAPIPrincipal(ctx, principal)
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r.WithContext(ctx)
}

func TestResolveActionActor(t *testing.T) {
	service := &auth.APIPrincipal{TokenID: "t1", Type: auth.TokenTypeService}
	apiKey := &auth.APIPrincipal{TokenID: "t2", Type: auth.TokenTypeAPIKey}
	owns := func(_ *http.Request, id string) bool { return id == "conv-mine" }

	cases := []struct {
		name      string
		principal *auth.APIPrincipal
		headers   map[string]string
		owns      func(*http.Request, string) bool
		want      actionActor
	}{
		{
			name: "a person in the UI",
			owns: owns,
			want: actionActor{Source: sourceUI},
		},
		{
			name:    "a Copilot proposal from the caller's own conversation",
			headers: map[string]string{actionSourceHeader: sourceCopilot, conversationIDHeader: "conv-mine"},
			owns:    owns,
			want:    actionActor{Actor: fieldmanager.ActorKobi, Source: sourceCopilot, ConversationID: "conv-mine"},
		},
		{
			name:    "a Copilot claim with someone else's conversation",
			headers: map[string]string{actionSourceHeader: sourceCopilot, conversationIDHeader: "conv-theirs"},
			owns:    owns,
			want:    actionActor{Source: sourceUI, ClaimedSource: sourceCopilot},
		},
		{
			name:    "a Copilot claim with no conversation",
			headers: map[string]string{actionSourceHeader: sourceCopilot},
			owns:    owns,
			want:    actionActor{Source: sourceUI, ClaimedSource: sourceCopilot},
		},
		{
			name:    "a Copilot claim on a route with nothing to check it against",
			headers: map[string]string{actionSourceHeader: sourceCopilot, conversationIDHeader: "conv-mine"},
			want:    actionActor{Source: sourceUI, ClaimedSource: sourceCopilot},
		},
		{
			name:    "a session that claims to be Autopilot",
			headers: map[string]string{actionSourceHeader: sourceAutopilot},
			owns:    owns,
			want:    actionActor{Source: sourceUI, ClaimedSource: sourceAutopilot},
		},
		{
			name:      "an API key that claims to be Kobi",
			principal: apiKey,
			headers:   map[string]string{actionSourceHeader: sourceCopilot, conversationIDHeader: "conv-mine"},
			owns:      owns,
			want:      actionActor{Source: sourceAPI, ClaimedSource: sourceCopilot},
		},
		{
			name:      "an API key that claims to be Autopilot",
			principal: apiKey,
			headers:   map[string]string{actionSourceHeader: sourceAutopilot},
			owns:      owns,
			want:      actionActor{Source: sourceAPI, ClaimedSource: sourceAutopilot},
		},
		{
			name:      "Autopilot, with its run and incident",
			principal: service,
			headers: map[string]string{
				actionSourceHeader:      sourceAutopilot,
				autopilotRunHeader:      "run_a1B2c3D4e5",
				autopilotIncidentHeader: "inc_7",
			},
			owns: owns,
			want: actionActor{
				Actor: fieldmanager.ActorAutopilot, Source: sourceAutopilot,
				AutopilotRun: "run_a1B2c3D4e5", AutopilotIncident: "inc_7",
			},
		},
		{
			name:      "Autopilot ids that are not ids stay out of the trail",
			principal: service,
			headers: map[string]string{
				actionSourceHeader:      sourceAutopilot,
				autopilotRunHeader:      "run 1; drop",
				autopilotIncidentHeader: "",
			},
			owns: owns,
			want: actionActor{Actor: fieldmanager.ActorAutopilot, Source: sourceAutopilot},
		},
		{
			name:      "a service token that says nothing",
			principal: service,
			owns:      owns,
			want:      actionActor{Source: sourceAPI},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := resolveActionActor(actorRequest(c.principal, c.headers), c.owns)
			if got != c.want {
				t.Errorf("got %+v\nwant %+v", got, c.want)
			}
		})
	}
}

// The middleware stamps the actor where both readers find it: the field
// manager of the writes, and the audit record. A forged header changes
// neither.
func TestWithActionActor_NamesTheWriteAndTheRecord(t *testing.T) {
	sink := audit.NewMemoryStore()
	audit.SetSink(sink, func() string { return "cluster-xyz" })
	t.Cleanup(func() { audit.SetSink(nil, nil) })

	convs := copilot.NewMemoryConversationStore()
	if err := convs.Upsert(&copilot.ConversationRecord{ID: "conv-mine", TenantID: "org-a", UserID: copilot.FallbackConversationUser}); err != nil {
		t.Fatal(err)
	}
	h := &handlers{copilotConversations: convs}

	run := func(r *http.Request) (fieldManager string) {
		h.withActionActor(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			fieldManager = fieldmanager.For(r.Context())
			auditMutation(r, "restart", "deployments", "shop", "web", nil, nil)
		})).ServeHTTP(httptest.NewRecorder(), r)
		return fieldManager
	}

	if fm := run(actorRequest(nil, map[string]string{actionSourceHeader: sourceCopilot, conversationIDHeader: "conv-mine"})); fm != "kubebolt-kobi" {
		t.Errorf("Copilot proposal: field manager %q, want kubebolt-kobi", fm)
	}
	if fm := run(actorRequest(nil, map[string]string{actionSourceHeader: sourceAutopilot})); fm != "kubebolt" {
		t.Errorf("forged Autopilot header: field manager %q, want kubebolt", fm)
	}
	service := &auth.APIPrincipal{TokenID: "t1", Type: auth.TokenTypeService}
	if fm := run(actorRequest(service, map[string]string{actionSourceHeader: sourceAutopilot, autopilotRunHeader: "run_1"})); fm != "kubebolt-autopilot" {
		t.Errorf("Autopilot: field manager %q, want kubebolt-autopilot", fm)
	}

	recs, err := sink.ListOrg("org-a", 0)
	if err != nil || len(recs) != 3 {
		t.Fatalf("records: %d, err %v", len(recs), err)
	}
	bySource := map[string]audit.Record{}
	for _, rec := range recs {
		bySource[rec.Source] = rec
	}
	if rec := bySource[sourceCopilot]; rec.ConversationID != "conv-mine" {
		t.Errorf("Copilot record: conversation %q, want conv-mine", rec.ConversationID)
	}
	if rec := bySource[sourceUI]; rec.ClaimedSource != sourceAutopilot {
		t.Errorf("forged record: source ui, claimed %q, want %q", rec.ClaimedSource, sourceAutopilot)
	}
	if rec := bySource[sourceAutopilot]; rec.AutopilotRunID != "run_1" {
		t.Errorf("Autopilot record: run %q, want run_1", rec.AutopilotRunID)
	}
}

// Without conversation persistence there is nothing to check a Copilot claim
// against; a person's session keeps being believed, as before #69.
func TestOwnsConversation_WithoutPersistenceBelievesTheSession(t *testing.T) {
	h := &handlers{}
	if !h.ownsConversation(actorRequest(nil, nil), "any") {
		t.Error("no conversation store: a session's claim should be believed")
	}
}

// A write made with the request's context names its actor (fieldmanager.For),
// down to the options Kubernetes records the manager from.
func TestActionWrite_CarriesTheActorsFieldManager(t *testing.T) {
	cs := fake.NewSimpleClientset()
	ctx := fieldmanager.WithActor(context.Background(), fieldmanager.ActorKobi)
	tmpl := corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "web", Image: "web:2"}}}}
	if err := dryRunCreatePod(ctx, cs, "shop", "web", tmpl); err != nil {
		t.Fatalf("dryRunCreatePod: %v", err)
	}
	for _, a := range cs.Actions() {
		if c, ok := a.(k8stesting.CreateActionImpl); ok {
			if got := c.CreateOptions.FieldManager; got != "kubebolt-kobi" {
				t.Errorf("field manager %q, want kubebolt-kobi", got)
			}
			return
		}
	}
	t.Fatal("no create recorded")
}
