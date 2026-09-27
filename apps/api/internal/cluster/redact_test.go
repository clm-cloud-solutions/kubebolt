package cluster

import (
	"strings"
	"testing"

	sigsyaml "sigs.k8s.io/yaml"
)

// Every case here is text a tool used to hand, unfiltered, to the model
// provider, Autopilot's run_events and the incident timeline.

func TestRedactText_HidesCredentialsInLogs(t *testing.T) {
	for _, tc := range []struct{ in, secret, keep string }{
		{"dial postgres://app:hunter2@db.prod:5432/orders failed", "hunter2", "db.prod:5432"},
		{"GET /api Authorization: Bearer abcdEFGH1234ijklMNOP", "abcdEFGH1234ijklMNOP", "Authorization"},
		{`{"level":"info","password":"s3cr3t-value","user":"bob"}`, "s3cr3t-value", `"user":"bob"`},
		{"DB_PASSWORD=hunter2", "hunter2", "DB_PASSWORD"},
		{"      DB_PASSWORD:  hunter2", "hunter2", "DB_PASSWORD"}, // kubectl describe's Environment block
		{"calling https://api.example.com/v1?api_key=ZX81abc9&page=2", "ZX81abc9", "page=2"},
		{"using token ghp_16C7e42F292c6912E7710c838347Ae178B4a for clone", "ghp_16C7e42F292c6912E7710c838347Ae178B4a", "for clone"},
		{"aws key AKIAIOSFODNN7EXAMPLE loaded", "AKIAIOSFODNN7EXAMPLE", "loaded"},
		{"jwt=eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0In0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U", "dozjgNryP4J3jVmNHl0w5N", ""},
		{"posting to https://hooks.slack.com/services/T000/B000/XXXXYYYYZZZZ", "XXXXYYYYZZZZ", "hooks.slack.com"},
		{"signing with Kq8vN2xPz7RtYb4WmHs9LcDfGj3AeU6TiOp1 now", "Kq8vN2xPz7RtYb4WmHs9LcDfGj3AeU6TiOp1", "now"},
	} {
		got := RedactText(tc.in)
		if strings.Contains(got, tc.secret) {
			t.Errorf("leaked %q\n  in:  %s\n  out: %s", tc.secret, tc.in, got)
		}
		if tc.keep != "" && !strings.Contains(got, tc.keep) {
			t.Errorf("over-redacted, lost %q\n  in:  %s\n  out: %s", tc.keep, tc.in, got)
		}
	}
}

// What an investigation needs from a log must survive: ids, digests, uuids,
// ordinary key/values, and prose that merely mentions a credential.
func TestRedactText_KeepsWhatAnInvestigationNeeds(t *testing.T) {
	for _, line := range []string{
		"trace_id=4bf92f3577b34da6a3ce929d0e0e4736 span=00f067aa0ba902b7",
		"pulled image sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
		"request 550e8400-e29b-41d4-a716-446655440000 took 120ms",
		"      LOG_LEVEL:  debug",
		"replicas: 3",
		"authentication failed for user bob",
		"OOMKilled: container exceeded 128Mi",
	} {
		if got := RedactText(line); got != line {
			t.Errorf("changed a line with no credential\n  in:  %s\n  out: %s", line, got)
		}
	}
}

func TestRedactText_HidesAPEMBlockWhole(t *testing.T) {
	in := "loading cert\n-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASC\nabc\n-----END PRIVATE KEY-----\nlistening on :8443"
	got := RedactText(in)
	if strings.Contains(got, "MIIEvQ") || strings.Contains(got, "abc") {
		t.Errorf("PEM body leaked:\n%s", got)
	}
	if !strings.Contains(got, "loading cert") || !strings.Contains(got, "listening on :8443") {
		t.Errorf("lines around the PEM block were lost:\n%s", got)
	}
}

func TestRedactObject_WorkloadEnvArgsAndAnnotations(t *testing.T) {
	obj := map[string]interface{}{
		"metadata": map[string]interface{}{
			"annotations": map[string]interface{}{
				"kubernetes.io/change-cause": "kubectl set env deploy/api DATABASE_URL=postgres://u:pw123@db/app",
			},
		},
		"spec": map[string]interface{}{"template": map[string]interface{}{"spec": map[string]interface{}{
			"containers": []interface{}{map[string]interface{}{
				"name": "api",
				"env": []interface{}{
					map[string]interface{}{"name": "DB_PASSWORD", "value": "hunter2"},
					map[string]interface{}{"name": "LOG_LEVEL", "value": "debug"},
					map[string]interface{}{"name": "API_TOKEN", "valueFrom": map[string]interface{}{"secretKeyRef": map[string]interface{}{"name": "api", "key": "token"}}},
				},
				"args": []interface{}{"--password", "hunter3", "--port", "8080", "--db-url=postgres://u:pw456@db/app"},
			}},
		}}},
	}
	RedactObject(obj)
	out := toString(obj)
	for _, secret := range []string{"hunter2", "hunter3", "pw123", "pw456"} {
		if strings.Contains(out, secret) {
			t.Errorf("leaked %q: %s", secret, out)
		}
	}
	for _, keep := range []string{"debug", "8080", "secretKeyRef", "DB_PASSWORD", "--password"} {
		if !strings.Contains(out, keep) {
			t.Errorf("lost %q: %s", keep, out)
		}
	}
}

func TestRedactYAML_DeploymentAndFallback(t *testing.T) {
	doc := []byte("kind: Deployment\nspec:\n  template:\n    spec:\n      containers:\n      - name: app\n        env:\n        - name: STRIPE_KEY\n          value: sk_live_51Habc123def456ghi\n")
	got := string(RedactYAML(doc))
	if strings.Contains(got, "sk_live_51Habc123def456ghi") || !strings.Contains(got, "STRIPE_KEY") {
		t.Errorf("yaml:\n%s", got)
	}
	// Not decodable as YAML: filtered as text, never returned raw.
	bad := []byte("{{ not yaml: password=hunter2")
	if strings.Contains(string(RedactYAML(bad)), "hunter2") {
		t.Error("undecodable document came back unfiltered")
	}
}

func toString(v interface{}) string {
	b, _ := sigsyaml.Marshal(v)
	return string(b)
}
