package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/findings"
	"github.com/kubebolt/kubebolt/apps/api/internal/integrations"
)

// Finding #93: paths below Editor that reached secret material, and records
// that kept it for months.

func withRole(r *http.Request, role auth.Role) *http.Request {
	return r.WithContext(auth.WithClaims(r.Context(), &auth.Claims{UserID: "u1", Username: "alice", Role: role}))
}

// The pod file browser is exec: `cat` of whatever the container mounts — its
// Secrets, its ServiceAccount token. It answered any Viewer; the terminal,
// the same exec, required Editor.
func TestFileHandlers_RefuseBelowEditor(t *testing.T) {
	h := &handlers{}
	for name, handler := range map[string]http.HandlerFunc{
		"list":     h.handleListFiles,
		"content":  h.handleFileContent,
		"download": h.handleFileDownload,
	} {
		r := withRole(httptest.NewRequest(http.MethodGet,
			"/api/v1/resources/pods/shop/web-1/files/content?path=/var/run/secrets/kubernetes.io/serviceaccount/token", nil), auth.RoleViewer)
		rr := httptest.NewRecorder()
		handler(rr, r)
		if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "Editor") {
			t.Errorf("%s as viewer: %d %s, want 403 naming Editor", name, rr.Code, rr.Body)
		}
	}
	for _, role := range []auth.Role{auth.RoleEditor, auth.RoleAdmin} {
		rr := httptest.NewRecorder()
		if !filesRoleAllowed(rr, withRole(httptest.NewRequest(http.MethodGet, "/", nil), role)) {
			t.Errorf("%s was refused the file browser", role)
		}
	}
}

// set_env recorded each touched container's whole env, literal values
// included, in the durable trail and in the process log.
func TestAuditMutation_KeepsNoEnvValue(t *testing.T) {
	sink := newAccessSink(t)
	var logged bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logged, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	from := []envContainerSnapshot{{Container: "app", Env: []envEntryPair{
		{Name: "DB_PASSWORD", Kind: "literal", Value: "env-from-s3cret"},
		{Name: "API_TOKEN", Kind: "secret", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "orders-api"}, Key: "token"}}},
	}}}
	to := []envContainerSnapshot{{Container: "app", Env: []envEntryPair{
		{Name: "DB_PASSWORD", Kind: "literal", Value: "env-to-s3cret"},
	}}}
	r := withRole(httptest.NewRequest(http.MethodPost, "/api/v1/resources/deployments/shop/orders/set-env", nil), auth.RoleEditor)
	auditMutation(r, "set_env", "deployments", "shop", "orders",
		map[string]any{"fromEnv": from, "toEnv": to, "triggerRollout": true}, nil)

	recs, _ := sink.ListOrg(auth.ContextTenantID(r), 0)
	if len(recs) != 1 {
		t.Fatalf("records = %d, want 1", len(recs))
	}
	stored, _ := json.Marshal(recs[0].Params)
	for where, text := range map[string]string{"trail": string(stored), "log": logged.String()} {
		for _, leaked := range []string{"env-from-s3cret", "env-to-s3cret"} {
			if strings.Contains(text, leaked) {
				t.Errorf("the %s kept %s:\n%s", where, leaked, text)
			}
		}
	}
	for _, kept := range []string{"DB_PASSWORD", "literal", "orders-api", "triggerRollout"} {
		if !strings.Contains(string(stored), kept) {
			t.Errorf("the trail lost %s:\n%s", kept, stored)
		}
	}
	if from[0].Env[0].Value != "env-from-s3cret" {
		t.Error("auditing changed the caller's snapshot (the response carries it)")
	}
}

func TestAuditParams_HidesAnnotationCredentials(t *testing.T) {
	got := auditParams(map[string]any{"annotations": map[string]any{"webhook": "https://hooks.slack.com/services/T0/B0/annot-s3cret"}})
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "annot-s3cret") {
		t.Errorf("annotation credential kept: %s", raw)
	}
	if auditParams(nil) != nil {
		t.Error("nil params became something")
	}
}

const falcoCredentialBody = `{"rule":"Outbound connection with credentials","priority":"Warning",` +
	`"output":"curl -H 'Authorization: Bearer falco-bearer-s3cret' https://api.example.com",` +
	`"output_fields":{"k8s.ns.name":"prod","k8s.pod.name":"web-1","proc.cmdline":"mysql -u root --password=falco-cmd-s3cret"}}`

// A runtime event quotes the command line that ran. Kobi filtered it; the feed
// served it raw to every role, and the store kept it for a month.
func TestFalcoIngest_StoresTheCommandLineFiltered(t *testing.T) {
	rr, events := falcoPost(t, "cluster-a", falcoCredentialBody)
	if rr.Code != http.StatusAccepted || len(events.appended) != 1 {
		t.Fatalf("status = %d, stored %d", rr.Code, len(events.appended))
	}
	stored, _ := json.Marshal(events.appended[0])
	for _, leaked := range []string{"falco-bearer-s3cret", "falco-cmd-s3cret"} {
		if strings.Contains(string(stored), leaked) {
			t.Errorf("stored %s:\n%s", leaked, stored)
		}
	}
	for _, kept := range []string{"curl", "mysql", "web-1"} {
		if !strings.Contains(string(stored), kept) {
			t.Errorf("lost %s:\n%s", kept, stored)
		}
	}
}

// Events stored before ingest filtered them are filtered on the way out.
func TestRuntimeEvents_FilteredOnRead(t *testing.T) {
	events := &memEvents{appended: []findings.EventRecord{{
		ID: "e1", ClusterID: "cluster-a",
		RuntimeEvent: integrations.RuntimeEvent{
			RuleName:         "Outbound connection with credentials",
			DetectedBehavior: "curl -H 'Authorization: Bearer stored-bearer-s3cret' https://api.example.com",
			Fields:           map[string]string{"proc.cmdline": "mysql --password=stored-cmd-s3cret"},
		},
	}}}
	h := &handlers{eventStore: events}
	rr := httptest.NewRecorder()
	h.handleListRuntimeEvents(rr, withRole(httptest.NewRequest(http.MethodGet, "/api/v1/runtime-events", nil), auth.RoleViewer))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body)
	}
	for _, leaked := range []string{"stored-bearer-s3cret", "stored-cmd-s3cret"} {
		if strings.Contains(rr.Body.String(), leaked) {
			t.Errorf("the feed served %s:\n%s", leaked, rr.Body)
		}
	}
	if !strings.Contains(rr.Body.String(), "mysql") {
		t.Errorf("the feed lost the event:\n%s", rr.Body)
	}
}
