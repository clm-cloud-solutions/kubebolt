// Package mcpcatalog is the curated marketplace of external MCP connectors
// Kobi and Autopilot may use to read the cloud resources a workload depends
// on. Setup and limits: docs/guides/kobi-mcp-connectors.md.
//
// A connector is embedded in the binary, never configured into existence:
// which servers exist, which of their tools are exposed and to whom, and what
// the customer must set up is KubeBolt's decision, reviewed in code. What an
// org chooses is only whether to turn one on and with which credentials.
//
// This file is the contract every binder and the gateway share. It imports
// only the standard library on purpose: the transport (mcpclient), the
// settings store and the gateway all depend on it, never the other way round.
package mcpcatalog

import (
	"context"
	"encoding/json"
	"time"
)

// Consumer names who is asking. The two see different subsets of the same
// connector: Autopilot only gets what changes a pipeline decision, Kobi also
// gets inventory, docs and cost. Credentials that depend on the
// consumer — AWS session policies — are resolved per consumer, never shared.
type Consumer string

const (
	ConsumerKobi      Consumer = "kobi"
	ConsumerAutopilot Consumer = "autopilot"
)

// Category groups connectors in the marketplace.
type Category string

const (
	CategoryCloud         Category = "cloud"
	CategoryObservability Category = "observability"
)

// FieldSchema describes one configuration field the admin fills in. The UI
// renders the form from it and the API validates against it, so there is one
// definition of what a valid AWS or GCP connector config looks like.
type FieldSchema struct {
	Name        string        `json:"name"`
	Label       string        `json:"label"`
	Help        string        `json:"help,omitempty"`
	Placeholder string        `json:"placeholder,omitempty"`
	Secret      bool          `json:"secret"`
	Required    bool          `json:"required"`
	Advanced    bool          `json:"advanced,omitempty"`
	Options     []FieldOption `json:"options,omitempty"`
	Default     string        `json:"default,omitempty"`
	// Multiline marks values that legitimately span lines, such as a pasted
	// credential JSON. The UI renders a textarea; validation admits CR/LF.
	Multiline bool `json:"multiline,omitempty"`
	// Pattern is an anchored regular expression the value must match. It is
	// the first barrier against a field being used to steer a request
	// somewhere else (a region that is a hostname, an ARN with a path).
	Pattern string `json:"-"`
	// MaxLen bounds the value; secrets such as a GCP credential JSON are
	// large but not unbounded.
	MaxLen int `json:"-"`
	// Generated marks a value KubeBolt creates once per org and the admin
	// only copies — the AWS external ID, which must be unique per customer
	// and never chosen by them (confused deputy). The gateway fills it in the
	// first time the connector is shown; a PUT or draft that carries it is
	// refused. It is not secret: the org's own admins read it back.
	Generated bool `json:"generated,omitempty"`
}

// FieldOption is one choice of a select field.
type FieldOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// AuthMode is one way to give KubeBolt access to the provider. Modes that
// borrow the credentials of the machine KubeBolt runs on are only offered on
// single-tenant installs: in SaaS those credentials would belong to KubeBolt,
// not to the org asking.
type AuthMode struct {
	ID             string        `json:"id"`
	Label          string        `json:"label"`
	Description    string        `json:"description"`
	SelfHostedOnly bool          `json:"selfHostedOnly"`
	Fields         []FieldSchema `json:"fields"`
}

// Family is a plain-language line of what a consumer can read through the
// connector ("Databases — RDS and Aurora status, events, Performance
// Insights"). It is what the admin approves, so it must match the allowlist.
type Family struct {
	Label     string `json:"label"`
	Detail    string `json:"detail"`
	Kobi      bool   `json:"kobi"`
	Autopilot bool   `json:"autopilot"`
}

// SetupStep is one step of the manual the drawer shows: the role or policy to
// create on the customer's side, generated from the same tables as the
// allowlist so the two cannot drift.
type SetupStep struct {
	Title    string `json:"title"`
	Body     string `json:"body"`
	Code     string `json:"code,omitempty"`
	Language string `json:"language,omitempty"` // json | shell | text
	// Alternative marks a step that replaces the ones before it instead of
	// following them ("or, on a self-hosted install, …"). The UI renders it
	// unnumbered. Titles never carry their own numbering.
	Alternative bool `json:"alternative,omitempty"`
}

// SetupGuide is the ordered manual for one connector.
type SetupGuide struct {
	Steps []SetupStep `json:"steps"`
}

// ConnectorDef is one curated connector.
type ConnectorDef struct {
	ID          string
	Name        string
	Category    Category
	Description string
	Tags        []string
	DocsURL     string
	// MinPlan is the lowest plan (auth plan id) that may enable it in
	// multi-tenant installs. Single-tenant installs are never gated.
	MinPlan   string
	AuthModes []AuthMode
	Families  []Family
	// Never lists, in plain language, what the connector will not do. It is
	// shown next to the families so the admin reads both halves.
	Never []string
	// Guide renders the setup manual for an org. It receives the org's
	// stored non-secret fields so it can print, e.g., the ARN it will assume.
	Guide func(env Env, fields map[string]string) SetupGuide
	// Binder opens a live binding for an org's configuration.
	Binder Binder
}

// ToolSpec is one tool a binding exposes, under its LOCAL name. The gateway
// adds the consumer-specific prefix (mcp__<id>__ for Kobi, cloud_<id>_ for
// Autopilot) and enforces the name limits; binders never do.
type ToolSpec struct {
	Name        string
	Description string
	InputSchema map[string]any
}

// CallResult is what a binding returns for one tool call. Text is the payload
// for the model before the gateway redacts, truncates and wraps it.
type CallResult struct {
	Text    string
	IsError bool
	// Notes are KubeBolt's own words about the call: what it did not
	// forward, where a resource group comes from. The gateway prints them
	// before the wrapper that marks Text as the provider's data, where the
	// model reads them as KubeBolt's, so a note is a constant or a template
	// KubeBolt fills with values of its own (a command from the allowlist, a
	// parameter it strips, a count), never provider text nor a name the
	// model made up.
	Notes []string
	// Own marks Text as KubeBolt's own words — a refusal of the call, written
	// by the binding before anything reached the provider — and the gateway
	// shows it as KubeBolt's, outside the wrapper that marks provider data.
	// Inside it, the words that tell the model how to correct the call would
	// be data the prompts tell it never to follow. Never set it on text that
	// carries anything a provider wrote: that stays wrapped.
	Own bool
	// Audit carries what the durable audit record may say about the call —
	// the service and operation, the Azure command, the GCP endpoint — and
	// never arguments or response content.
	Audit map[string]any
	// Digest is the line the chat shows under the call once it is back
	// (copilot.ToolResult.Digest): what came back, counted and listed, never
	// what it means. Written from the provider's whole answer; the model
	// never reads it.
	Digest string
}

// TestResult is the outcome of a connectivity test.
type TestResult struct {
	OK        bool    `json:"ok"`
	Tools     int     `json:"tools"`
	LatencyMs int64   `json:"latencyMs"`
	Identity  string  `json:"identity,omitempty"`
	Canary    *Canary `json:"canary,omitempty"`
	// Error is a sentence an admin can act on; Hint says what to check.
	Error string `json:"error,omitempty"`
	Hint  string `json:"hint,omitempty"`
	// Warning is set on a passing test whose setup still needs attention —
	// the credentials are the AWS account's root user, for instance. The
	// gateway stores it with the verification so the card keeps saying it
	// after the drawer closes.
	Warning string `json:"warning,omitempty"`
}

// Canary is the one harmless read a test performs to prove the credentials
// reach the provider's control plane, not only the MCP endpoint.
type Canary struct {
	Tool string `json:"tool"`
	OK   bool   `json:"ok"`
}

// Resolved is an org's decrypted configuration for one connector, handed to
// a binder. It lives in memory only.
type Resolved struct {
	Org     string
	Mode    string
	Fields  map[string]string
	Secrets map[string]string
}

// Env is what the process knows that an org config does not.
type Env struct {
	// AmbientAllowed is true on single-tenant installs, where borrowing the
	// machine's own cloud credentials (default chain, ADC, the local Azure
	// sidecar's identity) is the operator's own choice.
	AmbientAllowed bool
	// MultiTenant is true on the SaaS build, where one KubeBolt principal
	// serves every org. Checks that only guard one org from another (the
	// AWS confused-deputy refusal) apply there and nowhere else.
	MultiTenant bool
	// AzmcpURL is the Azure MCP Server sidecar endpoint (root path).
	AzmcpURL string
	// AllowHostPorts are the only loopback/private host:port pairs the
	// outbound dial guard admits — the local sidecar and nothing else.
	AllowHostPorts []string
	CallTimeout    time.Duration
	// UserAgent identifies KubeBolt to the provider.
	UserAgent string
	// AWSPrincipalARN is the AWS identity KubeBolt assumes customer roles
	// from (env KUBEBOLT_MCP_AWS_PRINCIPAL_ARN). The setup guide prints it in
	// the trust policy; empty leaves a placeholder the admin fills in.
	AWSPrincipalARN string
}

// ConfigValidator is implemented by binders that can check what the generic
// schema cannot — that a pasted credential document is a kind this install
// accepts, for instance. It never contacts the network. The gateway calls it
// on every save and every draft test, with only the secret values that write
// introduces: a stored secret is never handed back to be re-validated.
type ConfigValidator interface {
	ValidateConfig(mode string, fields, secretValues map[string]string, env Env) error
}

// Binder opens a Binding for one org's configuration of one connector.
type Binder interface {
	Bind(ctx context.Context, r Resolved, env Env) (Binding, error)
}

// Binding is a live, org-scoped connection to a connector. It is safe for
// concurrent use. Tools must be deterministic for a given configuration: the
// gateway feeds them into a prompt-cached tool list, and a reordering or a
// changing description rewrites that cache on every request.
type Binding interface {
	Tools(consumer Consumer) []ToolSpec
	// Call runs one tool. It MUST re-check that the tool — and, for router
	// tools, the command or operation inside it — is allowed for the
	// consumer before contacting the provider: the gateway's name check is
	// the first barrier, this is the second.
	Call(ctx context.Context, consumer Consumer, tool string, args json.RawMessage) (CallResult, error)
	Test(ctx context.Context) (TestResult, error)
	Close(ctx context.Context) error
}

// PartialBinding is implemented by bindings that can come up with part of
// their tools missing (a GCP endpoint or an Azure router whose listing failed
// at bind time). The gateway re-binds a partial binding in the background and
// swaps it in only when the new one is missing less.
type PartialBinding interface {
	// Missing names what failed to bind; empty when the binding is whole.
	Missing() []string
}

type consumerKey struct{}

// WithConsumer records who is calling in ctx, for code that serves both
// consumers through one executor (get_external_connections suggests only the
// tools the caller can run).
func WithConsumer(ctx context.Context, c Consumer) context.Context {
	return context.WithValue(ctx, consumerKey{}, c)
}

// ConsumerFrom returns the consumer WithConsumer recorded; Kobi when none was.
func ConsumerFrom(ctx context.Context) Consumer {
	if c, ok := ctx.Value(consumerKey{}).(Consumer); ok && c != "" {
		return c
	}
	return ConsumerKobi
}
