package copilot

import (
	"encoding/json"
	"strings"
	"testing"
)

// oom-limit-lowered: the Deployment carries DB_PASSWORD as a literal, and
// both reads handed Kobi its value. It must learn that a credential sits in
// the spec as a literal, never what it is.
func TestHideCredentialEnv_DetailAndDescribe(t *testing.T) {
	detail := `{"containers":[{"env":[{"kind":"literal","name":"DB_PASSWORD","value":"tunnel"},` +
		`{"kind":"literal","name":"LOG_LEVEL","value":"debug"},{"kind":"secret","name":"API_TOKEN","value":"secret db-creds key token"}]}]}`
	got := HideCredentialEnv("get_resource_detail", detail)
	if strings.Contains(got, "tunnel") {
		t.Errorf("detail still carries the password: %s", got)
	}
	for _, want := range []string{`"name":"DB_PASSWORD"`, `"value":"(value hidden)"`, `"value":"debug"`, `"value":"secret db-creds key token"`} {
		if !strings.Contains(got, want) {
			t.Errorf("detail lacks %s: %s", want, got)
		}
	}

	describe := jsonString(map[string]string{"describe": "Containers:\n  app:\n    Limits:\n      memory:  128Mi\n" +
		"    Environment:\n      DB_PASSWORD:  tunnel\n      LOG_LEVEL:    debug\n" +
		"      API_TOKEN:    <set to the key 'token' in secret 'db-creds'>  Optional: false\n" +
		"    Mounts:         <none>\n"})
	var d map[string]string
	if err := json.Unmarshal([]byte(HideCredentialEnv("get_resource_describe", describe)), &d); err != nil {
		t.Fatal(err)
	}
	got = d["describe"]
	if strings.Contains(got, "tunnel") {
		t.Errorf("describe still carries the password: %s", got)
	}
	for _, want := range []string{"DB_PASSWORD:  (value hidden)", "LOG_LEVEL:    debug", "<set to the key 'token' in secret 'db-creds'>", "memory:  128Mi"} {
		if !strings.Contains(got, want) {
			t.Errorf("describe lacks %q: %s", want, got)
		}
	}

	clean := `{"containers":[{"env":[{"kind":"literal","name":"LOG_LEVEL","value":"debug"}]}]}`
	if HideCredentialEnv("get_resource_detail", clean) != clean {
		t.Error("a detail with nothing to hide was rewritten")
	}
	if HideCredentialEnv("get_pod_logs", detail) != detail {
		t.Error("only detail and describe are rewritten")
	}
}

// A DSN keeps its host, port and database: they are what a dependency
// investigation follows. Only the user:password goes.
func TestHideCredentialEnv_DSNKeepsItsHost(t *testing.T) {
	detail := `{"env":[{"kind":"literal","name":"DATABASE_URL","value":"postgres://shop:hunter2@shop-db.shop-data:5432/shop"}]}`
	got := HideCredentialEnv("get_resource_detail", detail)
	if strings.Contains(got, "hunter2") || strings.Contains(got, "shop:") {
		t.Errorf("credentials still there: %s", got)
	}
	if !strings.Contains(got, "postgres://(credentials hidden)@shop-db.shop-data:5432/shop") {
		t.Errorf("host lost: %s", got)
	}
	// A name that is itself a credential hides the whole value.
	named := `{"env":[{"kind":"literal","name":"DB_PASSWORD","value":"postgres://a:b@h/d"}]}`
	if got := HideCredentialEnv("get_resource_detail", named); !strings.Contains(got, `"value":"(value hidden)"`) {
		t.Errorf("credential-named value not hidden whole: %s", got)
	}
}
