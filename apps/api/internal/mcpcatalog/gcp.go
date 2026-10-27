package mcpcatalog

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/kubebolt/kubebolt/apps/api/internal/mcpclient"
)

// The Google Cloud connector reads Google Cloud through Google's managed,
// per-service MCP endpoints (https://<service>.googleapis.com/mcp), signed
// with an OAuth token of the org's own read-only service account. This file
// is the reviewed half — the definition, its fields and the setup manual;
// gcp_tools.go is the allowlist and gcp_binding.go talks to Google.

const (
	gcpConnectorID        = "gcp"
	gcpModeServiceAccount = "service-account"
	gcpModeADC            = "adc"
	gcpFieldCredential    = "credentialJSON"

	gcpProjectIDPattern = `^[a-z][a-z0-9-]{4,28}[a-z0-9]$`
	// A region (us-central1, northamerica-northeast1), a zone
	// (us-central1-a) or global.
	gcpLocationPattern = `^(global|[a-z]+(-[a-z]+)+[0-9]{1,2}(-[a-z])?)$`

	// gcpMaxCredentialJSON bounds the pasted document; a key is ~2.3 KB.
	gcpMaxCredentialJSON = 32768

	gcpServiceAccountName = "kubebolt-reader"
)

var (
	gcpProjectIDRE = regexp.MustCompile(gcpProjectIDPattern)
	gcpLocationRE  = regexp.MustCompile(gcpLocationPattern)
	gcpSAEmailRE   = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,62}@[a-z0-9.-]+\.gserviceaccount\.com$`)
	gcpImpersonRE  = regexp.MustCompile(`/serviceAccounts/([^/:]+):generateAccessToken$`)
)

// gcpServiceRoles is the viewer role each allowlisted endpoint needs
// (checked against Google's role reference on 2026-09-24).
// roles/container.viewer rather than clusterViewer: list_operations needs
// container.operations.list, which only the former has; neither reads
// Secret values.
var gcpServiceRoles = map[string]string{
	"alloydb":             "roles/alloydb.viewer",
	"cloudasset":          "roles/cloudasset.viewer",
	"clouderrorreporting": "roles/errorreporting.viewer",
	"cloudquotas":         "roles/cloudquotas.viewer",
	"compute":             "roles/compute.viewer",
	"container":           "roles/container.viewer",
	"databaseinsights":    "roles/databaseinsights.viewer",
	"logging":             "roles/logging.viewer",
	"memorystore":         "roles/memorystore.viewer",
	"monitoring":          "roles/monitoring.viewer",
	"pubsub":              "roles/pubsub.viewer",
	"recommender":         "roles/recommender.viewer",
	"redis":               "roles/redis.viewer",
	"sqladmin":            "roles/cloudsql.viewer",
}

// gcpToolUserRole is required for any MCP tool call on Google Cloud.
const gcpToolUserRole = "roles/mcp.toolUser"

// Google Cloud is not offered yet: no authenticated call has been validated
// against a real project, and SaaS has no auth mode that works in
// orgs created since 2024-05 (a service-account key is blocked there).
func init() { registerPreview(newGCPDef) }

func newGCPDef() *ConnectorDef {
	project := FieldSchema{
		Name: "projectId", Label: "Project ID", Required: true,
		Help:        "The Google Cloud project KubeBolt reads. Every call is pinned to it and billed to it.",
		Placeholder: "my-project", Pattern: gcpProjectIDPattern, MaxLen: 30,
	}
	location := FieldSchema{
		Name: "location", Label: "Default location", Advanced: true,
		Help:        "The region (or zone) Kobi should assume when a question does not name one, e.g. us-central1.",
		Placeholder: "us-central1", Pattern: gcpLocationPattern, MaxLen: 40,
	}
	return &ConnectorDef{
		ID:       gcpConnectorID,
		Name:     "Google Cloud",
		Category: CategoryCloud,
		Description: "Lets Kobi read the Google Cloud resources your workloads depend on: GKE node pools and operations, " +
			"Cloud SQL, AlloyDB and Memorystore status, Database Insights, Cloud Monitoring metrics and alerts, logs, Pub/Sub, VMs and " +
			"the asset inventory. KubeBolt calls Google's managed MCP endpoints with a read-only service account of yours, offers only a " +
			"reviewed list of read-only tools and pins every call to the project you set here.",
		Tags:    []string{"GKE", "Cloud SQL", "Cloud Monitoring", "Cloud Logging", "Memorystore", "Pub/Sub"},
		DocsURL: "https://docs.cloud.google.com/mcp/overview",
		MinPlan: "team",
		AuthModes: []AuthMode{
			{
				ID:    gcpModeServiceAccount,
				Label: "Service account key",
				Description: "A key of a read-only service account in your project. KubeBolt stores it encrypted and uses it only " +
					"to call Google's MCP endpoints.",
				Fields: []FieldSchema{
					project,
					{
						Name: gcpFieldCredential, Label: "Credential JSON", Secret: true, Required: true, Multiline: true,
						Help: "Paste the service account key file (type service_account). Self-hosted installs may paste a " +
							"workload identity federation config instead.",
						Placeholder: `{"type": "service_account", ...}`,
						MaxLen:      gcpMaxCredentialJSON,
					},
					location,
				},
			},
			{
				ID:    gcpModeADC,
				Label: "Application Default Credentials",
				Description: "Use the identity KubeBolt already runs as: GKE Workload Identity, the metadata server or gcloud's " +
					"application default credentials. Grant that identity the same read-only roles.",
				SelfHostedOnly: true,
				Fields:         []FieldSchema{project, location},
			},
		},
		Families: gcpFamilyList(),
		Never: []string{
			"Create, change or delete anything: only tools Google marks read-only and KubeBolt has reviewed are offered",
			"Run SQL or read database rows, documents or query results (Cloud SQL, AlloyDB, Spanner, BigQuery, Firestore)",
			"Read the Redis AUTH string, secret values or Cloud Storage object contents",
			"Run gcloud or bq commands, or start Gemini Cloud Assist investigations",
			"Read Kubernetes objects or pod logs through GKE: KubeBolt reads the cluster itself, with its own redaction",
			"Read a project other than the one set here",
		},
		Guide:  gcpGuide,
		Binder: gcpBinder{},
	}
}

// ValidateConfig checks what the generic field validation cannot: that a new
// credential document is a kind this install accepts. It never contacts the
// network. The gateway reaches it through an optional interface on the
// binder, when it has one; Bind runs the same checks either way, so a
// configuration saved without them still cannot be used.
func (gcpBinder) ValidateConfig(mode string, fields, secretValues map[string]string, env Env) error {
	if mode == gcpModeADC && !env.AmbientAllowed {
		return &FieldError{Field: "mode", Message: "Application Default Credentials are only available on self-hosted installs"}
	}
	if raw, ok := secretValues[gcpFieldCredential]; ok && strings.TrimSpace(raw) != "" {
		if _, err := gcpCheckCredentialJSON(raw, env.AmbientAllowed); err != nil {
			return err
		}
		// mcpclient's own checks (token endpoints on googleapis.com, no
		// file/URL/executable credential sources). Building the token
		// source fetches nothing; the source is discarded.
		if _, err := mcpclient.GoogleTokenSourceFromJSON(context.Background(), []byte(raw)); err != nil {
			return gcpCredentialRefused(err)
		}
	}
	return nil
}

// gcpCheckCredentialJSON checks the shape of a pasted credential document
// and returns the identity to show an admin (a service account email is an
// identifier, not a secret). Errors never quote the document.
//
// A workload identity federation (external_account) config is refused unless
// ambient credentials are allowed: every credential_source it can use reads
// the KubeBolt host's own identity, so in a multi-tenant install one org
// could federate into another org's pool that trusts KubeBolt — the same
// reason ADC is self-hosted only.
func gcpCheckCredentialJSON(raw string, ambientAllowed bool) (string, error) {
	fe := func(msg string) error { return &FieldError{Field: gcpFieldCredential, Message: msg} }
	if len(raw) > gcpMaxCredentialJSON {
		return "", fe("Credential JSON is too large")
	}
	var doc struct {
		Type                           string `json:"type"`
		ClientEmail                    string `json:"client_email"`
		PrivateKey                     string `json:"private_key"`
		ServiceAccountImpersonationURL string `json:"service_account_impersonation_url"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &doc); err != nil {
		return "", fe("Credential JSON is not a valid JSON document")
	}
	switch doc.Type {
	case "service_account":
		if doc.ClientEmail == "" || doc.PrivateKey == "" {
			return "", fe("The service account key is missing client_email or private_key")
		}
		if gcpSAEmailRE.MatchString(doc.ClientEmail) {
			return doc.ClientEmail, nil
		}
		return "service account key", nil
	case "external_account":
		if !ambientAllowed {
			return "", fe("Workload identity federation configs are only accepted on self-hosted installs; paste a service account key")
		}
		if m := gcpImpersonRE.FindStringSubmatch(doc.ServiceAccountImpersonationURL); m != nil && gcpSAEmailRE.MatchString(m[1]) {
			return m[1] + " (workload identity federation)", nil
		}
		return "workload identity federation", nil
	case "":
		return "", fe("Credential JSON has no type; paste a service account key file")
	}
	if ambientAllowed {
		return "", fe("Credential JSON must be a service account key or a workload identity federation config")
	}
	return "", fe("Credential JSON must be a service account key (type service_account)")
}

// gcpRoles returns the roles to grant, tool user first, then one viewer per
// endpoint in table order.
func gcpRoles() []string {
	roles := []string{gcpToolUserRole}
	seen := map[string]bool{gcpToolUserRole: true}
	for _, s := range gcpServices {
		if r := gcpServiceRoles[s.id]; r != "" && !seen[r] {
			seen[r] = true
			roles = append(roles, r)
		}
	}
	return roles
}

// gcpDenyPolicy is the recommended IAM deny policy: Google refuses, for
// KubeBolt's service account, any MCP tool it does not annotate read-only.
func gcpDenyPolicy(saEmail string) string {
	policy := map[string]any{
		"displayName": "KubeBolt: read-only MCP tools only",
		"rules": []any{map[string]any{
			"denyRule": map[string]any{
				"deniedPrincipals":  []string{"principal://iam.googleapis.com/projects/-/serviceAccounts/" + saEmail},
				"deniedPermissions": []string{"mcp.googleapis.com/tools.call"},
				"denialCondition": map[string]any{
					"title":      "Deny MCP tools that are not read-only",
					"expression": "api.getAttribute('mcp.googleapis.com/tool.isReadOnly', false) == false",
				},
			},
		}},
	}
	b, _ := json.MarshalIndent(policy, "", "  ")
	return string(b)
}

// gcpGuide renders the setup manual. It prints the org's project when one is
// stored and valid — the value lands in shell commands, so anything else is
// replaced by a placeholder.
func gcpGuide(env Env, fields map[string]string) SetupGuide {
	project := strings.TrimSpace(fields["projectId"])
	if !gcpProjectIDRE.MatchString(project) {
		project = "PROJECT_ID"
	}
	sa := gcpServiceAccountName + "@" + project + ".iam.gserviceaccount.com"

	var apis strings.Builder
	for _, s := range gcpServices {
		apis.WriteString(" \\\n  " + s.id + ".googleapis.com")
	}
	var roles strings.Builder
	for _, r := range gcpRoles() {
		roles.WriteString(" \\\n  " + r)
	}

	keyBody := "Create a key for the service account and paste the contents of the file into Credential JSON, then delete the file. " +
		"KubeBolt stores it encrypted and never shows it again."
	if env.AmbientAllowed {
		keyBody += " If your organization blocks service account keys, paste a workload identity federation config instead, " +
			"or choose Application Default Credentials to use the identity KubeBolt already runs as: grant that identity the roles " +
			"above and use it as the principal of the deny policy."
	}

	return SetupGuide{Steps: []SetupStep{
		{
			Title: "Enable the product APIs",
			Body: "Google's MCP endpoints answer for a product only when its API is enabled in the project. The command lists every " +
				"product KubeBolt reads; leave out the ones you do not use and their tools simply return nothing.",
			Code:     "gcloud services enable --project=" + project + apis.String(),
			Language: "shell",
		},
		{
			Title:    "Create a read-only service account",
			Body:     "KubeBolt reads Google Cloud as this account, so it can only ever see what the roles below allow.",
			Code:     "gcloud iam service-accounts create " + gcpServiceAccountName + " \\\n  --project=" + project + " \\\n  --display-name=\"KubeBolt read-only\"",
			Language: "shell",
		},
		{
			Title: "Grant it read-only roles",
			Body: gcpToolUserRole + " lets the account call Google's MCP tools at all; each viewer role lets one product answer. " +
				"None of them can change anything, and none reads secret values, database rows or object contents. " +
				"roles/container.viewer is used instead of container.clusterViewer because it can list the operations " +
				"(upgrades, repairs) running on a cluster.",
			Code: "for role in" + roles.String() + "; do\n  gcloud projects add-iam-policy-binding " + project + " \\\n" +
				"    --member=\"serviceAccount:" + sa + "\" --role=\"$role\" --condition=None\ndone",
			Language: "shell",
		},
		{
			Title: "Recommended: let Google refuse every tool that is not read-only",
			Body: "Save this IAM deny policy as kubebolt-mcp-deny.json. For KubeBolt's service account, Google then refuses any MCP " +
				"tool it does not mark read-only, even if one were ever requested by mistake. It does NOT cover data-plane tools " +
				"that Google marks read-only: execute_sql_readonly and execute_sql_read_only (SQL on Cloud SQL and AlloyDB), " +
				"get_instance_auth_string (the Redis AUTH string), read_object and read_text (Cloud Storage contents) and the " +
				"Spanner, BigQuery and Firestore data reads. KubeBolt blocks those by name and never offers them to Kobi.",
			Code:     gcpDenyPolicy(sa),
			Language: "json",
		},
		{
			Title:    "Attach the deny policy to the project",
			Body:     "Creating a deny policy needs the Deny Admin role (roles/iam.denyAdmin) on the project.",
			Code:     "gcloud iam policies create kubebolt-mcp-read-only \\\n  --attachment-point=cloudresourcemanager.googleapis.com/projects/" + project + " \\\n  --kind=denypolicies \\\n  --policy-file=kubebolt-mcp-deny.json",
			Language: "shell",
		},
		{
			Title:    "Create a key and paste it here",
			Body:     keyBody,
			Code:     "gcloud iam service-accounts keys create kubebolt-key.json \\\n  --iam-account=" + sa,
			Language: "shell",
		},
	}}
}
