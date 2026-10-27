package mcpcatalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/kubebolt/kubebolt/apps/api/internal/mcpclient"
)

// Upstream tool names. The binding maps its own local names onto them and
// never exposes the upstream ones: run_script executes arbitrary Python,
// get_tasks is an implementation detail of run_script, and get_presigned_url
// and retrieve_skill are not offered at all.
const (
	awsUpRunScript    = "aws___run_script"
	awsUpGetTasks     = "aws___get_tasks"
	awsUpSearchDocs   = "aws___search_documentation"
	awsUpReadDocs     = "aws___read_documentation"
	awsUpListRegions  = "aws___list_regions"
	awsUpAvailability = "aws___get_regional_availability"
)

// Local tool names.
const (
	awsToolCall         = "call"
	awsToolSearchDocs   = "search_documentation"
	awsToolReadDocs     = "read_documentation"
	awsToolListRegions  = "list_regions"
	awsToolAvailability = "get_regional_availability"
)

const (
	// awsMaxParamsBytes bounds the params of one call: a request body, never
	// a payload.
	awsMaxParamsBytes = 16 << 10
	// awsMaxCallBytes is how much of a run_script reply is read. It is well
	// above what the gateway hands the model (32 KiB) because the reply must
	// be parsed whole to verify api_calls; a larger one is refused, not cut.
	awsMaxCallBytes = 1 << 20
	// awsPollBudget bounds get_tasks polling when the caller set no deadline.
	awsPollBudget = 60 * time.Second
	awsMaxPolls   = 30
)

var errAWSBindingClosed = errors.New("aws: connector binding is closed")

// awsBinder opens bindings. Its fields are test seams; the zero value talks
// to the real AWS MCP Server with the real credential chain.
type awsBinder struct {
	// endpoint overrides the MCP URL for a host region.
	endpoint func(hostRegion string) string
	// allowHostPorts are passed to the dial guard (a local fake server).
	allowHostPorts []string
	// loadConfig resolves KubeBolt's own AWS identity.
	loadConfig func(ctx context.Context, region, profile string) (aws.Config, error)
	// assumeRole builds the per-consumer assumed-role credentials.
	assumeRole func(base aws.Config, roleARN, externalID, sessionPolicy, sessionName string) (aws.CredentialsProvider, error)
	// pollWait fixes the wait between get_tasks polls.
	pollWait time.Duration
	// observe sees every run_script and get_tasks reply (live fixtures).
	observe func(upstream string, res *mcpclient.CallResult)
}

func (b *awsBinder) endpointFor(hostRegion string) string {
	if b.endpoint != nil {
		return b.endpoint(hostRegion)
	}
	return "https://aws-mcp." + hostRegion + ".api.aws/mcp"
}

// awsConfig is an org's validated configuration.
type awsConfig struct {
	mode       string
	roleARN    string
	externalID string
	region     string
	hostRegion string
	profile    string
}

func resolveAWSConfig(r Resolved, env Env) (awsConfig, error) {
	var (
		mode AuthMode
		ok   bool
	)
	for _, m := range awsAuthModes() {
		if m.ID == r.Mode {
			mode, ok = m, true
		}
	}
	if !ok {
		return awsConfig{}, fmt.Errorf("aws: unknown auth mode %q", r.Mode)
	}
	if mode.SelfHostedOnly && !env.AmbientAllowed {
		return awsConfig{}, errors.New("aws: this install does not allow using the machine's own credentials")
	}
	// Only the mode's own fields are considered: a value left over from the
	// other mode (a stored roleArn after switching to ambient) is ignored
	// rather than failing the whole connector.
	fields := map[string]string{}
	secretsSet := map[string]bool{}
	secretValues := map[string]string{}
	for _, f := range mode.Fields {
		if f.Secret {
			if v, ok := r.Secrets[f.Name]; ok && v != "" {
				secretsSet[f.Name] = true
				secretValues[f.Name] = v
			}
			continue
		}
		if v, ok := r.Fields[f.Name]; ok {
			fields[f.Name] = v
		}
	}
	if err := ValidateConfig(mode, fields, secretsSet, secretValues); err != nil {
		return awsConfig{}, err
	}
	// The external ID is one of the fields, not a secret: KubeBolt generated
	// it for this org, and the org's admins read it back.
	cfg := awsConfig{
		mode:       mode.ID,
		roleARN:    strings.TrimSpace(fields["roleArn"]),
		externalID: strings.TrimSpace(fields["externalId"]),
		region:     strings.TrimSpace(fields["region"]),
		hostRegion: strings.TrimSpace(fields["hostRegion"]),
		profile:    strings.TrimSpace(fields["profile"]),
	}
	if cfg.hostRegion == "" {
		cfg.hostRegion = awsDefaultHostRegion
	}
	if cfg.mode == awsModeAssumeRole {
		if err := awsCheckRoleAccount(cfg.roleARN, env); err != nil {
			return awsConfig{}, err
		}
	}
	return cfg, nil
}

// ValidateConfig checks what the field patterns cannot: that the role is not
// in KubeBolt's own AWS account. KubeBolt's principal may be allowed to assume
// roles there for reasons of its own, and STS ignores an external ID the
// trust policy does not ask for, so a role there that trusts the account
// would be open to every org. It never contacts the network. The gateway
// calls it on every save and draft test; Bind runs the same check, so a
// configuration saved without it still cannot be used.
func (*awsBinder) ValidateConfig(mode string, fields, secretValues map[string]string, env Env) error {
	if mode != awsModeAssumeRole {
		return nil
	}
	return awsCheckRoleAccount(fields["roleArn"], env)
}

// awsAccountRE reads the account out of an IAM or STS ARN: the role an org
// names, and KubeBolt's principal, which may be a role, a user, an account
// root or an assumed-role session.
var awsAccountRE = regexp.MustCompile(`^arn:aws(?:-[a-z]+)*:(?:iam|sts)::([0-9]{12}):`)

// awsCheckRoleAccount refuses a role in the account of KubeBolt's own
// principal. Without a configured principal there is nothing to compare.
func awsCheckRoleAccount(roleARN string, env Env) error {
	// A self-hosted install serves one organization, whose KubeBolt usually
	// runs in the same account as the role it reads through: nobody there to
	// be confused on behalf of.
	if !env.MultiTenant {
		return nil
	}
	own := awsAccountRE.FindStringSubmatch(strings.TrimSpace(env.AWSPrincipalARN))
	role := awsAccountRE.FindStringSubmatch(strings.TrimSpace(roleARN))
	if own == nil || role == nil || own[1] != role[1] {
		return nil
	}
	return &FieldError{Field: "roleArn", Message: "KubeBolt cannot assume a role in its own AWS account."}
}

// Bind validates the configuration and prepares the binding. It opens no
// connection: sessions and credentials are established on first use, so a
// connector that is enabled but never asked costs nothing.
func (b *awsBinder) Bind(ctx context.Context, r Resolved, env Env) (Binding, error) {
	cfg, err := resolveAWSConfig(r, env)
	if err != nil {
		return nil, err
	}
	loadConfig := b.loadConfig
	if loadConfig == nil {
		loadConfig = mcpclient.LoadAWSConfig
	}
	// The base identity's region is the host region: it only serves STS (the
	// assume-role hop) and the SDK's own lookups, never the target calls.
	base, err := loadConfig(ctx, cfg.hostRegion, cfg.profile)
	if err != nil {
		return nil, err
	}
	policies := map[Consumer]string{}
	for _, c := range []Consumer{ConsumerKobi, ConsumerAutopilot} {
		p, err := awsSessionPolicy(c)
		if err != nil {
			return nil, err
		}
		policies[c] = p
	}
	guard := mcpclient.NewDialGuard(b.allowHostPorts...)
	return &awsBinding{
		binder:   b,
		cfg:      cfg,
		env:      env,
		base:     base,
		policies: policies,
		endpoint: b.endpointFor(cfg.hostRegion),
		guard:    guard,
		httpc:    guard.HTTPClient(),
		tools: map[Consumer][]ToolSpec{
			ConsumerKobi:      awsToolSpecs(ConsumerKobi, cfg.region),
			ConsumerAutopilot: awsToolSpecs(ConsumerAutopilot, cfg.region),
		},
		creds:   map[Consumer]aws.CredentialsProvider{},
		clients: map[Consumer]*mcpclient.Client{},
	}, nil
}

// awsBinding is one org's live AWS connector. Each consumer has its own MCP
// session — its own sandbox on AWS's side, since run_script tasks and the
// session's cold start belong to one Mcp-Session-Id — and, in assume-role
// mode, its own credentials narrowed by its own session policy. The host
// region is fixed per binding, so a session is keyed by consumer alone.
type awsBinding struct {
	binder   *awsBinder
	cfg      awsConfig
	env      Env
	base     aws.Config
	policies map[Consumer]string
	endpoint string
	guard    *mcpclient.DialGuard
	httpc    *http.Client
	tools    map[Consumer][]ToolSpec

	mu      sync.Mutex
	closed  bool
	creds   map[Consumer]aws.CredentialsProvider
	clients map[Consumer]*mcpclient.Client
}

func (b *awsBinding) Tools(consumer Consumer) []ToolSpec {
	specs := b.tools[consumer]
	out := make([]ToolSpec, len(specs))
	copy(out, specs)
	return out
}

// credentialsLocked returns the consumer's signing identity. The caller
// holds b.mu.
func (b *awsBinding) credentialsLocked(c Consumer) (aws.CredentialsProvider, error) {
	if p := b.creds[c]; p != nil {
		return p, nil
	}
	var p aws.CredentialsProvider
	switch b.cfg.mode {
	case awsModeAssumeRole:
		assume := b.binder.assumeRole
		if assume == nil {
			assume = mcpclient.AssumeRoleCredentials
		}
		// The session name shows up in the customer's CloudTrail as
		// assumed-role/<role>/kubebolt-kobi, so each consumer's calls are
		// attributable.
		var err error
		p, err = assume(b.base, b.cfg.roleARN, b.cfg.externalID, b.policies[c], "kubebolt-"+string(c))
		if err != nil {
			return nil, b.scrub(err)
		}
	default:
		// Ambient: no session policy can be applied to the machine's own
		// identity; the typed allowlist is the only narrowing.
		p = b.base.Credentials
		if p == nil {
			return nil, errors.New("aws: no AWS credentials found on this machine")
		}
	}
	b.creds[c] = p
	return p, nil
}

func (b *awsBinding) credentials(c Consumer) (aws.CredentialsProvider, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, errAWSBindingClosed
	}
	return b.credentialsLocked(c)
}

// client returns the consumer's MCP session, creating it on first use.
func (b *awsBinding) client(c Consumer) (*mcpclient.Client, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, errAWSBindingClosed
	}
	if cl := b.clients[c]; cl != nil {
		return cl, nil
	}
	creds, err := b.credentialsLocked(c)
	if err != nil {
		return nil, err
	}
	region := b.cfg.region
	opts := []mcpclient.Option{
		// Signing region is the HOST region; any other is answered 401.
		mcpclient.WithSigner(mcpclient.NewSigV4Signer(creds, mcpclient.AWSMCPService, b.cfg.hostRegion)),
		// The target region travels in _meta on every request with params.
		mcpclient.WithMeta(func() map[string]any { return map[string]any{"AWS_REGION": region} }),
		mcpclient.WithGuard(b.guard),
		mcpclient.WithHTTPClient(b.httpc),
		mcpclient.WithMaxCallBytes(awsMaxCallBytes),
	}
	if b.env.CallTimeout > 0 {
		opts = append(opts, mcpclient.WithCallTimeout(b.env.CallTimeout))
	}
	if b.env.UserAgent != "" {
		opts = append(opts, mcpclient.WithHeaders(map[string]string{"User-Agent": b.env.UserAgent}))
	}
	cl, err := mcpclient.New(b.endpoint, opts...)
	if err != nil {
		return nil, err
	}
	b.clients[c] = cl
	return cl, nil
}

// Close ends every session. AWS keeps a sandbox per session and caps
// concurrent sessions per account, so they are released, not abandoned.
func (b *awsBinding) Close(ctx context.Context) error {
	b.mu.Lock()
	b.closed = true
	clients := make([]*mcpclient.Client, 0, len(b.clients))
	for _, c := range []Consumer{ConsumerKobi, ConsumerAutopilot} {
		if cl := b.clients[c]; cl != nil {
			clients = append(clients, cl)
		}
	}
	b.clients = map[Consumer]*mcpclient.Client{}
	b.mu.Unlock()
	var first error
	for _, cl := range clients {
		if err := cl.Close(ctx); err != nil && first == nil {
			first = b.scrub(err)
		}
	}
	return first
}

func (b *awsBinding) isClosed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closed
}

func (b *awsBinding) hasTool(c Consumer, name string) bool {
	for _, t := range b.tools[c] {
		if t.Name == name {
			return true
		}
	}
	return false
}

// Call runs one tool after re-checking it against the consumer's list: the
// gateway's name check is the first barrier, this is the second, and awsCheck
// inside `call` is the third.
func (b *awsBinding) Call(ctx context.Context, consumer Consumer, tool string, args json.RawMessage) (CallResult, error) {
	audit := map[string]any{"hostRegion": b.cfg.hostRegion}
	if !b.hasTool(consumer, tool) {
		audit["denied"] = "tool"
		return CallResult{IsError: true, Own: true, Text: fmt.Sprintf("The AWS tool %q is not available here.", tool), Audit: audit}, nil
	}
	switch tool {
	case awsToolCall:
		return b.callTyped(ctx, consumer, args)
	default:
		return b.callKnowledge(ctx, consumer, tool, args)
	}
}

// ----- the typed call -----

type awsCallArgs struct {
	Service   string          `json:"service"`
	Operation string          `json:"operation"`
	Params    json.RawMessage `json:"params"`
	Region    string          `json:"region"`
}

func (b *awsBinding) callTyped(ctx context.Context, consumer Consumer, raw json.RawMessage) (CallResult, error) {
	audit := map[string]any{"hostRegion": b.cfg.hostRegion}
	var a awsCallArgs
	if err := awsDecodeStrict(raw, &a); err != nil {
		audit["denied"] = "arguments"
		return CallResult{IsError: true, Own: true, Text: "Invalid arguments: " + err.Error(), Audit: audit}, nil
	}
	service := strings.ToLower(strings.TrimSpace(a.Service))
	operation := strings.TrimSpace(a.Operation)
	region := strings.TrimSpace(a.Region)
	if region == "" {
		region = b.cfg.region
	}
	// The audit record names the call, never its arguments; a malformed
	// name is model text of any length, so it is recorded as invalid.
	audit["service"] = awsAuditName(service, awsServiceRE.MatchString(service))
	audit["operation"] = awsAuditName(operation, awsOperationRE.MatchString(operation))
	audit["region"] = awsAuditName(region, awsRegionRE.MatchString(region) && len(region) <= 32)

	rule, err := awsCheck(consumer, service, operation)
	if err != nil {
		audit["denied"] = "operation"
		return CallResult{IsError: true, Own: true, Text: "Refused: " + err.Error() + ".", Audit: audit}, nil
	}
	if !awsRegionRE.MatchString(region) || len(region) > 32 {
		audit["denied"] = "region"
		return CallResult{IsError: true, Own: true, Text: "Refused: region must be an AWS region id such as \"us-east-1\".", Audit: audit}, nil
	}
	params, err := awsParams(a.Params)
	if err != nil {
		audit["denied"] = "params"
		return CallResult{IsError: true, Own: true, Text: "Refused: " + err.Error() + ".", Audit: audit}, nil
	}

	// A simulation gets the region its resources are in (aws_hints.go).
	var simulated string
	if awsIsSimulate(service, operation) {
		params, simulated = awsWithRequestedRegion(params, region)
	}

	out, err := b.runTyped(ctx, consumer, rule, service, operation, region, params)
	audit["apiCalls"] = out.apiCalls
	if err != nil {
		return CallResult{Audit: audit}, b.scrub(err)
	}
	if !out.isError {
		if simulated != "" {
			out.notes = append(out.notes, fmt.Sprintf(awsSimulateRegionNote, simulated))
		}
		if awsIsTrailLookup(service, operation) && awsTrailByKeyID(params) {
			out.notes = append(out.notes, awsTrailKeyIDNote)
		}
		if awsQueueHasDLQ(service, operation, out.text) {
			out.notes = append(out.notes, awsDLQNote)
		}
	}
	if out.detail != "" {
		audit["detail"] = out.detail
	}
	return CallResult{Text: out.text, IsError: out.isError, Notes: out.notes, Audit: audit, Digest: out.digest}, nil
}

func awsAuditName(s string, valid bool) string {
	if !valid {
		return "(invalid)"
	}
	return s
}

// awsParams normalizes the operation's parameters to a compact JSON object.
func awsParams(raw json.RawMessage) ([]byte, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return []byte("{}"), nil
	}
	if raw[0] != '{' {
		return nil, errors.New("params must be a JSON object of the operation's request parameters")
	}
	if len(raw) > awsMaxParamsBytes {
		return nil, fmt.Errorf("params are larger than %d KiB", awsMaxParamsBytes>>10)
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return nil, errors.New("params are not valid JSON")
	}
	return buf.Bytes(), nil
}

// awsScript renders the only code this connector ever sends to run_script.
// Every value is a Python string literal produced by awsPyStr, and the params
// travel as one such literal parsed by json.loads, so no argument can end
// the literal and add code of its own. The last line is the script's result
// expression, per the run_script contract.
func awsScript(service, operation, region string, params []byte) string {
	return "result = await call_boto3(service_name=" + awsPyStr(service) +
		", operation_name=" + awsPyStr(operation) +
		", region_name=" + awsPyStr(region) +
		", params=json.loads(" + awsPyStr(string(params)) + "))\nresult"
}

// awsPyStr renders s as a double-quoted Python str literal made only of
// printable ASCII: quotes and backslashes are escaped, everything else
// outside 0x20-0x7e becomes \uXXXX or \UXXXXXXXX. There is no newline, no
// unescaped quote and no byte the Python tokenizer could read differently.
// Invalid UTF-8 becomes U+FFFD, as json.Marshal would make it.
func awsPyStr(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r >= 0x20 && r < 0x7f:
			b.WriteRune(r)
		case r <= 0xffff:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			fmt.Fprintf(&b, `\U%08x`, r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// awsTypedOutcome is what one typed call produced.
type awsTypedOutcome struct {
	text     string
	isError  bool
	apiCalls int
	// detail is an audit note: "api_calls mismatch", "truncated", ...
	detail string
	// notes are KubeBolt's own words about a result it reshaped
	// (CallResult.Notes).
	notes []string
	// digest is the chat's line for the answer (aws_digest.go).
	digest string
}

func (b *awsBinding) runTyped(ctx context.Context, consumer Consumer, rule *awsServiceRule, service, operation, region string, params []byte) (awsTypedOutcome, error) {
	cl, err := b.client(consumer)
	if err != nil {
		return awsTypedOutcome{}, err
	}
	code := awsScript(service, operation, region, params)
	args, err := json.Marshal(map[string]string{"code": code})
	if err != nil {
		return awsTypedOutcome{}, err
	}
	res, err := cl.CallTool(ctx, awsUpRunScript, args)
	if err != nil {
		return awsTypedOutcome{}, err
	}
	if b.binder.observe != nil {
		b.binder.observe(awsUpRunScript, res)
	}
	if res.Truncated {
		return awsTypedOutcome{isError: true, detail: "truncated",
			text: fmt.Sprintf("The result of %s.%s is larger than %d KiB and was discarded. Narrow the call with the operation's filters, an identifier or MaxRecords/MaxResults.", service, operation, awsMaxCallBytes>>10)}, nil
	}
	sr := parseAWSScript(res.Text(), res.IsError)
	if sr.pending() {
		sr, err = b.awaitTask(ctx, cl, sr)
		if err != nil {
			return awsTypedOutcome{}, err
		}
	}
	return awsFinish(rule, service, operation, sr), nil
}

// awsFinish turns a final script result into what the model sees, after
// verifying that the sandbox ran exactly the requested operation.
func awsFinish(rule *awsServiceRule, service, operation string, sr awsScriptResult) awsTypedOutcome {
	out := awsTypedOutcome{apiCalls: len(sr.apiCalls)}
	mismatch := func() awsTypedOutcome {
		out.isError = true
		out.detail = "api_calls mismatch"
		out.text = "The AWS response could not be verified against the requested operation and was discarded."
		return out
	}
	// Verify before anything else, errors included: a script that ran some
	// other operation is discarded whatever it returned.
	if sr.apiCallsBad {
		return mismatch()
	}
	for _, c := range sr.apiCalls {
		svc, op, ok := awsAPICallName(c)
		if !ok || !rule.names[awsNorm(svc)] || awsNorm(op) != awsNorm(operation) {
			return mismatch()
		}
	}
	if sr.errText != "" {
		out.isError = true
		out.text = fmt.Sprintf("AWS returned an error for %s.%s: %s", service, operation, sr.errText)
		if awsAccessDenied(sr.errText) {
			out.notes = append(out.notes, awsDeniedOperationNote(rule, operation))
		}
		return out
	}
	if !sr.hasReturn {
		out.isError = true
		out.detail = "unrecognized response"
		out.text = "The AWS MCP Server returned a response KubeBolt does not recognize."
		return out
	}
	if len(sr.apiCalls) == 0 {
		// call_boto3 always makes at least one call; a value without any is
		// not something the requested operation produced.
		out.isError = true
		out.detail = "api_calls missing"
		out.text = "The AWS response could not be verified against the requested operation and was discarded."
		return out
	}
	if awsIsTrailLookup(service, operation) {
		if ans, reads, ok := awsCompactTrail(sr.returnValue); ok {
			if b, err := json.Marshal(ans); err == nil {
				out.text = string(b)
				out.notes = awsTrailNotes(ans, reads)
				out.digest = awsTrailDigest(ans, reads)
				return out
			}
		}
	}
	if awsIsLambda(service) {
		v, hidden, err := awsHideEnvironment(sr.returnValue)
		if err != nil {
			out.isError = true
			out.detail = "unrecognized response"
			out.text = "The AWS response could not be checked for environment variable values and was discarded."
			return out
		}
		sr.returnValue = v
		if hidden > 0 {
			out.notes = append(out.notes, fmt.Sprintf(awsLambdaHiddenNote, hidden))
		}
	}
	out.text = awsCompactValue(sr.returnValue)
	out.digest = awsDigest(service, operation, sr.returnValue)
	return out
}

// The note that follows an AWS authorization error. A model that read one
// AccessDenied as "no access to the service" stopped with the operation that
// would have answered still enabled and unused.
const (
	awsDeniedNote       = "AWS refused this role one operation, not the %s service: a policy can deny single operations, regions or resources. "
	awsDeniedNoteOthers = "Other %s operations enabled here (%s), or another first tool get_external_connections suggested, may still answer: try them before telling the operator %s is out of reach."
	awsDeniedNoteAlone  = "Another first tool get_external_connections suggested may still answer: try it before telling the operator %s is out of reach."
)

// awsAccessDenied says whether an operation's error is AWS refusing the
// caller's identity, whatever policy did it (identity, boundary, SCP,
// resource policy): the codes IAM-backed services answer with.
func awsAccessDenied(errText string) bool {
	for _, s := range []string{"AccessDenied", "UnauthorizedOperation", "AuthorizationError", "not authorized to perform"} {
		if strings.Contains(errText, s) {
			return true
		}
	}
	return false
}

// awsDeniedOperationNote names only allowlist values: the rule's service and
// its other enabled operations, never the provider's text nor the name the
// model sent (the refused operation is left out by matching it, not quoted).
func awsDeniedOperationNote(rule *awsServiceRule, operation string) string {
	others := make([]string, 0, len(rule.ops))
	for _, p := range rule.ops {
		if !strings.Contains(p, "*") && awsOpMatch(p, operation) {
			continue
		}
		others = append(others, p)
	}
	note := fmt.Sprintf(awsDeniedNote, rule.service)
	if len(others) == 0 {
		return note + fmt.Sprintf(awsDeniedNoteAlone, rule.service)
	}
	return note + fmt.Sprintf(awsDeniedNoteOthers, rule.service, strings.Join(others, ", "), rule.service)
}

// awsCompactValue renders the operation's result compactly, without the
// ResponseMetadata block boto3 adds to every reply (request ids and HTTP
// headers the model has no use for).
func awsCompactValue(v json.RawMessage) string {
	var obj map[string]json.RawMessage
	if json.Unmarshal(v, &obj) == nil && obj != nil {
		if _, ok := obj["ResponseMetadata"]; ok {
			delete(obj, "ResponseMetadata")
			if b, err := json.Marshal(obj); err == nil {
				return string(b)
			}
		}
	}
	var buf bytes.Buffer
	if json.Compact(&buf, v) == nil {
		return buf.String()
	}
	return string(v)
}

// awsScriptResult is a parsed run_script or get_tasks answer.
type awsScriptResult struct {
	taskID      string
	status      string
	wait        time.Duration
	returnValue json.RawMessage
	hasReturn   bool
	apiCalls    []json.RawMessage
	// apiCallsBad marks an api_calls that is present but not a list: it
	// cannot be verified, so the result is discarded.
	apiCallsBad bool
	errText     string
}

func (r awsScriptResult) pending() bool {
	return r.taskID != "" && awsPendingStatus(r.status)
}

func awsPendingStatus(s string) bool {
	switch strings.ToLower(s) {
	case "working", "pending", "running", "queued", "submitted", "in_progress":
		return true
	}
	return false
}

// parseAWSScript reads a run_script (or finished task) result. Verified live
// on 2026-09-24 (testdata/aws_run_script_*.json), a finished script answers
//
//	{"status":"success","return_value":{…},"api_calls":[{"service":"rds",
//	 "operation":"DescribeDBInstances","status":"success","n_items":{…}}],
//	 "stdout":"","stderr":null,"warning":null}
//
// with the same object in structuredContent. A long run answers a task_id
// and a pending status instead (per the tool's own description; not observed
// on these fast reads), and a failure a non-success status with an error or
// stderr.
func parseAWSScript(text string, isError bool) awsScriptResult {
	var r awsScriptResult
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &m); err != nil || m == nil {
		// Not the script result shape, so nothing in it can be verified: only
		// a reply the server itself flagged as an error is passed on, short.
		r.errText = "the AWS MCP Server returned a response KubeBolt does not recognize"
		if s := awsShortText(text); isError && s != "" {
			r.errText = s
		}
		return r
	}
	return awsScriptFromObject(m, isError)
}

func awsScriptFromObject(m map[string]json.RawMessage, isError bool) awsScriptResult {
	var r awsScriptResult
	r.taskID = awsJSONString(m["task_id"])
	r.status = awsJSONString(m["status"])
	r.wait = awsWait(m)
	if v, ok := m["return_value"]; ok {
		r.returnValue = v
		r.hasReturn = true
	}
	if v, ok := m["api_calls"]; ok {
		var calls []json.RawMessage
		if json.Unmarshal(v, &calls) == nil {
			r.apiCalls = calls
		} else {
			r.apiCallsBad = true
		}
	}
	// A finished task may carry the script result nested.
	if res, ok := m["result"]; ok && !r.hasReturn {
		var inner map[string]json.RawMessage
		if s := awsJSONString(res); s != "" {
			_ = json.Unmarshal([]byte(s), &inner)
		} else {
			_ = json.Unmarshal(res, &inner)
		}
		if inner != nil {
			nested := awsScriptFromObject(inner, isError)
			nested.taskID, nested.status = r.taskID, r.status
			return nested
		}
	}
	failed := isError || (r.status != "" && !awsSuccessStatus(r.status) && !awsPendingStatus(r.status))
	for _, k := range []string{"error", "errorMessage", "error_message", "exception"} {
		if s := awsErrorText(m[k]); s != "" {
			r.errText = s
			break
		}
	}
	if r.errText == "" && failed {
		// The operation's own error (AccessDenied, a missing identifier) is
		// reported on its api_calls entry; the traceback lands in stderr.
		for _, c := range r.apiCalls {
			var entry map[string]json.RawMessage
			if json.Unmarshal(c, &entry) == nil {
				if s := awsErrorText(entry["error"]); s != "" {
					r.errText = s
					break
				}
			}
		}
		if r.errText == "" {
			r.errText = awsErrorText(m["stderr"])
		}
		if r.errText == "" {
			r.errText = "the script failed"
			if r.status != "" {
				r.errText = "the script ended with status " + r.status
			}
		}
	}
	return r
}

func awsSuccessStatus(s string) bool {
	switch strings.ToLower(s) {
	case "success", "succeeded", "completed", "complete", "done", "ok":
		return true
	}
	return false
}

func awsWait(m map[string]json.RawMessage) time.Duration {
	for _, k := range []string{"recommended_wait_seconds", "retry_after_seconds"} {
		var f float64
		if v, ok := m[k]; ok && json.Unmarshal(v, &f) == nil && f > 0 {
			return time.Duration(f * float64(time.Second))
		}
	}
	return 0
}

func awsJSONString(v json.RawMessage) string {
	var s string
	if len(v) > 0 && json.Unmarshal(v, &s) == nil {
		return s
	}
	return ""
}

// awsErrorText extracts a short error from a string or an object with a
// message.
func awsErrorText(v json.RawMessage) string {
	if len(v) == 0 || bytes.Equal(v, []byte("null")) {
		return ""
	}
	if s := awsJSONString(v); s != "" {
		return awsShortText(s)
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(v, &obj) == nil {
		for _, k := range []string{"message", "Message", "error", "errorMessage"} {
			if s := awsJSONString(obj[k]); s != "" {
				return awsShortText(s)
			}
		}
	}
	return awsShortText(string(v))
}

// awsShortText bounds an upstream error message: enough for the model or an
// admin to act on, never a whole traceback.
func awsShortText(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	const max = 600
	if len(s) > max {
		cut := max
		for cut > 0 && (s[cut]&0xc0) == 0x80 {
			cut--
		}
		s = s[:cut] + "…"
	}
	return s
}

// awsAPICallName reads one api_calls entry as (service, operation), whatever
// its shape: "rds.describe_db_instances", "rds:DescribeDBInstances", or an
// object naming both.
func awsAPICallName(raw json.RawMessage) (service, operation string, ok bool) {
	if s := awsJSONString(raw); s != "" {
		for _, sep := range []string{":", ".", "/", " "} {
			if svc, op, found := strings.Cut(s, sep); found && svc != "" && op != "" {
				return svc, op, true
			}
		}
		return "", "", false
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return "", "", false
	}
	pick := func(keys ...string) string {
		for _, k := range keys {
			if s := awsJSONString(obj[k]); s != "" {
				return s
			}
		}
		return ""
	}
	service = pick("service", "service_name", "serviceName", "client")
	operation = pick("operation", "operation_name", "operationName", "api", "method")
	if service == "" || operation == "" {
		return "", "", false
	}
	return service, operation, true
}

// awaitTask polls get_tasks on the same session — tasks belong to the
// session that started them — until the script finishes, the caller's
// deadline passes or the poll budget runs out.
func (b *awsBinding) awaitTask(ctx context.Context, cl *mcpclient.Client, sr awsScriptResult) (awsScriptResult, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, awsPollBudget)
		defer cancel()
	}
	taskID := sr.taskID
	wait := sr.wait
	for i := 1; i <= awsMaxPolls; i++ {
		if err := awsSleep(ctx, b.pollDelay(wait)); err != nil {
			return awsScriptResult{}, err
		}
		// Close has DELETEd this session. Polling on would make the client
		// open a fresh session that nothing closes (AWS caps sessions per
		// account) and keep reading after the connector was turned off.
		if b.isClosed() {
			return awsScriptResult{}, errAWSBindingClosed
		}
		args, err := json.Marshal(map[string]any{"task_ids": []string{taskID}, "poll_iteration": i})
		if err != nil {
			return awsScriptResult{}, err
		}
		res, err := cl.CallTool(ctx, awsUpGetTasks, args)
		if err != nil {
			return awsScriptResult{}, err
		}
		if b.binder.observe != nil {
			b.binder.observe(awsUpGetTasks, res)
		}
		if res.Truncated {
			return awsScriptResult{errText: fmt.Sprintf("the result is larger than %d KiB; narrow the call", awsMaxCallBytes>>10)}, nil
		}
		next, found := awsTaskFrom(res, taskID)
		if !found {
			return awsScriptResult{errText: "the AWS MCP Server lost track of the running call"}, nil
		}
		if !next.pending() {
			return next, nil
		}
		wait = next.wait
	}
	return awsScriptResult{errText: "the call did not finish in time"}, nil
}

// awsTaskFrom finds the task in a get_tasks answer: structuredContent when
// present, else the text.
func awsTaskFrom(res *mcpclient.CallResult, taskID string) (awsScriptResult, bool) {
	var body map[string]json.RawMessage
	if len(res.StructuredContent) > 0 {
		_ = json.Unmarshal(res.StructuredContent, &body)
	}
	if body == nil {
		_ = json.Unmarshal([]byte(strings.TrimSpace(res.Text())), &body)
	}
	if body == nil {
		return awsScriptResult{}, false
	}
	var tasks []map[string]json.RawMessage
	_ = json.Unmarshal(body["tasks"], &tasks)
	for _, t := range tasks {
		if awsJSONString(t["task_id"]) != taskID {
			continue
		}
		r := awsScriptFromObject(t, false)
		if r.taskID == "" {
			r.taskID = taskID
		}
		if r.wait == 0 {
			r.wait = awsWait(body)
		}
		return r, true
	}
	return awsScriptResult{}, false
}

func (b *awsBinding) pollDelay(recommended time.Duration) time.Duration {
	if b.binder.pollWait > 0 {
		return b.binder.pollWait
	}
	switch {
	case recommended <= 0:
		return time.Second
	case recommended < 200*time.Millisecond:
		return 200 * time.Millisecond
	case recommended > 5*time.Second:
		return 5 * time.Second
	}
	return recommended
}

func awsSleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// ----- documentation and region tools (Kobi only) -----

type awsSearchArgs struct {
	SearchPhrase string   `json:"search_phrase"`
	Limit        int      `json:"limit,omitempty"`
	Topics       []string `json:"topics,omitempty"`
}

type awsReadDocsArgs struct {
	Requests []awsReadDocsRequest `json:"requests"`
}

type awsReadDocsRequest struct {
	URL        string `json:"url"`
	MaxLength  int    `json:"max_length,omitempty"`
	StartIndex int    `json:"start_index,omitempty"`
}

type awsAvailabilityArgs struct {
	ResourceType string   `json:"resource_type"`
	Regions      []string `json:"regions"`
	Filters      []string `json:"filters,omitempty"`
	NextToken    string   `json:"next_token,omitempty"`
}

// awsDocHosts are the hosts read_documentation may ask AWS to fetch: the
// documentation sites from the server's own allowlist, minus the wildcard
// readthedocs.io subdomains anyone can register. The server checks its
// prefixes too, but a URL is the one argument a prompt-injected model could
// use to carry data out, so it is matched here on the parsed host, before a
// look-alike ("docs.aws.amazon.com.example.net"), a userinfo trick or a dot
// segment can depend on how the server compares strings. A host that also
// serves anyone's content (github.com) is narrowed to AWS's own
// organizations by its first path segment; nil admits any path.
var awsDocHosts = map[string][]string{
	"docs.aws.amazon.com": nil,
	"aws.amazon.com":      nil,
	"repost.aws":          nil,
	"docs.amplify.aws":    nil,
	"ui.docs.amplify.aws": nil,
	"constructs.dev":      nil,
	"strandsagents.com":   nil,
	"karpenter.sh":        nil,
	"github.com":          {"aws-cloudformation", "aws-samples", "awsdocs", "awslabs", "cdklabs"},
}

// awsDocURL reports whether raw is a plain https URL on a documentation host.
func awsDocURL(raw string) bool {
	if len(raw) > 2048 || strings.ContainsAny(raw, " \\\x00\r\n\t") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Opaque != "" || u.User != nil || u.Port() != "" {
		return false
	}
	// The host must be written exactly as listed: no port, no upper case
	// or trailing dot that one comparison would fold and another would not.
	owners, ok := awsDocHosts[u.Host]
	if !ok {
		return false
	}
	// u.Path is decoded, so %2e%2e and %2f are caught as ".." and "/".
	p := "/" + u.Path + "/"
	if strings.Contains(p, "/../") || strings.Contains(p, "/./") {
		return false
	}
	if owners != nil {
		first, _, _ := strings.Cut(strings.TrimPrefix(u.Path, "/"), "/")
		return awsContains(owners, first)
	}
	return true
}

var awsDocTopics = []string{
	"reference_documentation", "current_awareness", "troubleshooting", "general",
	"cloudformation", "cdk_docs", "cdk_constructs", "amplify_docs", "strands_docs",
}

// awsKnowledgeArgs validates a documentation or region tool's arguments and
// rebuilds them from the typed struct, so nothing the schema does not name
// reaches the server.
func awsKnowledgeArgs(tool string, raw json.RawMessage) (upstream string, args []byte, err error) {
	switch tool {
	case awsToolSearchDocs:
		var a awsSearchArgs
		if err := awsDecodeStrict(raw, &a); err != nil {
			return "", nil, err
		}
		a.SearchPhrase = strings.TrimSpace(a.SearchPhrase)
		if a.SearchPhrase == "" || len(a.SearchPhrase) > 500 || strings.ContainsAny(a.SearchPhrase, "\x00") {
			return "", nil, errors.New("search_phrase must be 1-500 characters")
		}
		if a.Limit < 0 || a.Limit > 10 {
			return "", nil, errors.New("limit must be between 1 and 10")
		}
		if len(a.Topics) > 3 {
			return "", nil, errors.New("at most 3 topics")
		}
		for _, t := range a.Topics {
			if !awsContains(awsDocTopics, t) {
				return "", nil, fmt.Errorf("unknown topic %q (use one of %s)", t, strings.Join(awsDocTopics, ", "))
			}
		}
		args, err = json.Marshal(a)
		return awsUpSearchDocs, args, err
	case awsToolReadDocs:
		var a awsReadDocsArgs
		if err := awsDecodeStrict(raw, &a); err != nil {
			return "", nil, err
		}
		if len(a.Requests) == 0 || len(a.Requests) > 5 {
			return "", nil, errors.New("requests must hold 1 to 5 pages")
		}
		for i := range a.Requests {
			r := &a.Requests[i]
			r.URL = strings.TrimSpace(r.URL)
			if !awsDocURL(r.URL) {
				return "", nil, errors.New("each url must be an AWS documentation URL from search_documentation (docs.aws.amazon.com, aws.amazon.com, repost.aws)")
			}
			if r.MaxLength < 0 || r.MaxLength > 20000 {
				return "", nil, errors.New("max_length must be at most 20000")
			}
			if r.StartIndex < 0 {
				return "", nil, errors.New("start_index cannot be negative")
			}
		}
		args, err = json.Marshal(a)
		return awsUpReadDocs, args, err
	case awsToolListRegions:
		var a struct{}
		if err := awsDecodeStrict(raw, &a); err != nil {
			return "", nil, err
		}
		return awsUpListRegions, []byte("{}"), nil
	case awsToolAvailability:
		var a awsAvailabilityArgs
		if err := awsDecodeStrict(raw, &a); err != nil {
			return "", nil, err
		}
		switch a.ResourceType {
		case "product", "api", "cfn":
		default:
			return "", nil, errors.New("resource_type must be product, api or cfn")
		}
		if len(a.Regions) == 0 || len(a.Regions) > 10 {
			return "", nil, errors.New("regions must hold 1 to 10 region ids")
		}
		for _, r := range a.Regions {
			if !awsRegionRE.MatchString(r) || len(r) > 32 {
				return "", nil, fmt.Errorf("%q is not an AWS region id", r)
			}
		}
		if len(a.Filters) > 20 {
			return "", nil, errors.New("at most 20 filters")
		}
		for _, f := range a.Filters {
			if f == "" || len(f) > 200 || strings.ContainsAny(f, "\x00\r\n") {
				return "", nil, errors.New("each filter must be 1-200 characters on one line")
			}
		}
		if len(a.NextToken) > 4096 || strings.ContainsAny(a.NextToken, "\x00\r\n") {
			return "", nil, errors.New("next_token is not valid")
		}
		args, err = json.Marshal(a)
		return awsUpAvailability, args, err
	}
	return "", nil, fmt.Errorf("unknown tool %q", tool)
}

func (b *awsBinding) callKnowledge(ctx context.Context, consumer Consumer, tool string, raw json.RawMessage) (CallResult, error) {
	audit := map[string]any{"hostRegion": b.cfg.hostRegion, "operation": tool}
	upstream, args, err := awsKnowledgeArgs(tool, raw)
	if err != nil {
		audit["denied"] = "arguments"
		return CallResult{IsError: true, Own: true, Text: "Invalid arguments: " + err.Error() + ".", Audit: audit}, nil
	}
	cl, err := b.client(consumer)
	if err != nil {
		return CallResult{Audit: audit}, err
	}
	res, err := cl.CallTool(ctx, upstream, args)
	if err != nil {
		return CallResult{Audit: audit}, b.scrub(err)
	}
	text := res.Text()
	isError := res.IsError
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(strings.TrimSpace(text)), &m) == nil && m != nil {
		// A failure can arrive with isError:false and an errorMessage.
		if s := awsErrorText(m["errorMessage"]); s != "" {
			return CallResult{IsError: true, Text: s, Audit: audit}, nil
		}
		// The knowledge tools wrap their answer as {"content":{"result":…}}.
		var content map[string]json.RawMessage
		if json.Unmarshal(m["content"], &content) == nil {
			if r, ok := content["result"]; ok {
				var buf bytes.Buffer
				if json.Compact(&buf, r) == nil {
					text = buf.String()
				}
			}
		}
	}
	var notes []string
	if res.Truncated {
		// A note, outside the wrapper: appended to the answer it was data.
		notes = append(notes, "the answer was larger than KubeBolt reads and is cut")
	}
	return CallResult{Text: text, IsError: isError, Notes: notes, Audit: audit}, nil
}

// awsDecodeStrict decodes a tool's arguments, refusing unknown keys: a key the
// schema does not name is either a mistake the model should hear about or
// an attempt to pass something through.
func awsDecodeStrict(raw json.RawMessage, v any) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		raw = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%s", strings.TrimPrefix(err.Error(), "json: "))
	}
	if dec.More() {
		return errors.New("trailing data after the arguments object")
	}
	return nil
}

// ----- connectivity test -----

// Test proves the whole chain an admin configured: the credentials resolve
// (the role can be assumed with its external ID and Kobi's session policy),
// the MCP session opens and lists its tools, and one harmless typed read —
// sts GetCallerIdentity, which needs no IAM permission — comes back
// verified.
func (b *awsBinding) Test(ctx context.Context) (TestResult, error) {
	start := time.Now()
	res := TestResult{}
	done := func() (TestResult, error) {
		res.LatencyMs = time.Since(start).Milliseconds()
		return res, nil
	}

	creds, err := b.credentials(ConsumerKobi)
	if err == nil {
		_, err = creds.Retrieve(ctx)
	}
	if err != nil {
		res.Error, res.Hint = b.credentialsFailure(err)
		return done()
	}

	cl, err := b.client(ConsumerKobi)
	if err != nil {
		res.Error, res.Hint = b.transportFailure(err)
		return done()
	}
	upstream, err := cl.ListTools(ctx)
	if err != nil {
		res.Error, res.Hint = b.transportFailure(err)
		return done()
	}
	have := map[string]bool{}
	for _, t := range upstream {
		have[t.Name] = true
	}
	if !have[awsUpRunScript] || !have[awsUpGetTasks] {
		res.Error = "The AWS MCP Server no longer offers the tools KubeBolt reads through."
		res.Hint = "AWS changed its MCP server. Update KubeBolt to a version that supports it."
		return done()
	}
	for _, t := range b.tools[ConsumerKobi] {
		if have[awsUpstreamOf(t.Name)] {
			res.Tools++
		}
	}

	rule, err := awsCheck(ConsumerKobi, "sts", "GetCallerIdentity")
	if err != nil {
		return TestResult{}, err // the table lost its canary: a build bug
	}
	res.Canary = &Canary{Tool: awsToolCall}
	out, err := b.runTyped(ctx, ConsumerKobi, rule, "sts", "GetCallerIdentity", b.cfg.region, []byte("{}"))
	if err != nil {
		res.Error, res.Hint = b.transportFailure(err)
		return done()
	}
	if out.isError {
		res.Error = "KubeBolt reached AWS, but the test read failed: " + b.scrubText(out.text)
		res.Hint = "Check that the region is enabled in your account."
		return done()
	}
	var id struct {
		Arn string `json:"Arn"`
	}
	_ = json.Unmarshal([]byte(out.text), &id)
	res.Identity = id.Arn
	res.Canary.OK = true
	res.OK = true
	// A warning, not a hint: the test passes, but the setup needs fixing,
	// and the gateway keeps a warning with the verification so the card
	// still says it after the drawer closes.
	if strings.HasSuffix(id.Arn, ":root") {
		res.Warning = "These are the account's root user credentials. Use a dedicated read-only role or profile instead."
	}
	return done()
}

// awsUpstreamOf maps a local tool to the upstream tool it needs.
func awsUpstreamOf(local string) string {
	switch local {
	case awsToolCall:
		return awsUpRunScript
	case awsToolSearchDocs:
		return awsUpSearchDocs
	case awsToolReadDocs:
		return awsUpReadDocs
	case awsToolListRegions:
		return awsUpListRegions
	case awsToolAvailability:
		return awsUpAvailability
	}
	return ""
}

// awsErrorCoder is the method set of the SDK's API errors (smithy.APIError),
// matched structurally so the connector does not import smithy directly.
type awsErrorCoder interface{ ErrorCode() string }

func (b *awsBinding) credentialsFailure(err error) (msg, hint string) {
	code := ""
	var coder awsErrorCoder
	if errors.As(err, &coder) {
		code = coder.ErrorCode()
	}
	if b.cfg.mode == awsModeAssumeRole {
		msg = "KubeBolt could not assume the role."
		if code != "" {
			msg = "KubeBolt could not assume the role (" + code + ")."
		}
		hint = "Check the role ARN, that the role's trust policy names KubeBolt's AWS principal, and that its sts:ExternalId matches the external ID saved here."
		if code == "PackedPolicyTooLarge" {
			hint = "AWS rejected KubeBolt's session policy as too large. Update KubeBolt."
		}
		return msg, hint
	}
	msg = "KubeBolt found no usable AWS credentials on this machine."
	if code != "" {
		msg = "KubeBolt could not load this machine's AWS credentials (" + code + ")."
	}
	hint = "Run KubeBolt with an AWS identity (environment, config profile, IRSA, Pod Identity or instance role), or name a profile that exists."
	return msg, hint
}

func (b *awsBinding) transportFailure(err error) (msg, hint string) {
	var httpErr *mcpclient.HTTPError
	switch {
	case errors.Is(err, mcpclient.ErrUnauthorized):
		return "AWS rejected KubeBolt's signature.", "The credentials were refused by the AWS MCP Server. If you just rotated them, test again; otherwise check the role or profile."
	case errors.Is(err, mcpclient.ErrRateLimited):
		return "AWS is rate limiting requests to its MCP server.", "Wait a minute and test again. The limit is shared with your account's other use of the AWS MCP Server."
	case errors.Is(err, context.DeadlineExceeded):
		return "The AWS MCP Server did not answer in time.", "The first call of a session can take up to 10 seconds. Test again."
	case errors.As(err, &httpErr):
		return fmt.Sprintf("The AWS MCP Server answered HTTP %d.", httpErr.Status), "Check that KubeBolt can reach aws-mcp." + b.cfg.hostRegion + ".api.aws over HTTPS."
	}
	return "KubeBolt could not reach the AWS MCP Server.", "Check that KubeBolt can reach aws-mcp." + b.cfg.hostRegion + ".api.aws over HTTPS."
}

// scrub removes the external ID from anything that may be logged or shown.
// The client and STS never put it in an error, but an admin-facing message
// is the one place a leak would be persisted, so it is checked again here.
func (b *awsBinding) scrub(err error) error {
	if err == nil || len(b.cfg.externalID) < 2 || !strings.Contains(err.Error(), b.cfg.externalID) {
		return err
	}
	return &awsScrubbedError{err: err, msg: strings.ReplaceAll(err.Error(), b.cfg.externalID, "[external ID]")}
}

func (b *awsBinding) scrubText(s string) string {
	if len(b.cfg.externalID) < 2 {
		return s
	}
	return strings.ReplaceAll(s, b.cfg.externalID, "[external ID]")
}

type awsScrubbedError struct {
	err error
	msg string
}

func (e *awsScrubbedError) Error() string { return e.msg }
func (e *awsScrubbedError) Unwrap() error { return e.err }

// ----- tool specs -----

// awsToolSpecs builds the consumer's tool list. The descriptions are
// KubeBolt's own: the upstream ones push the model to sweep every region and
// return every resource, and its instructions tell it to prefer run_script —
// none of that is forwarded. They depend only on the consumer and the
// configured region, so the prompt-cached tool list stays stable.
func awsToolSpecs(c Consumer, region string) []ToolSpec {
	specs := []ToolSpec{awsCallSpec(c, region)}
	if c != ConsumerKobi {
		return specs
	}
	return append(specs,
		ToolSpec{
			Name: awsToolSearchDocs,
			Description: "Search the official AWS documentation. Each result carries a verbatim excerpt of the page that usually " +
				"answers the question; read the full page with read_documentation only when the excerpt is not enough. " +
				"Use it to explain an AWS error, a service limit or a configuration option. " +
				"Example: {\"search_phrase\":\"RDS DB instance status storage-full\",\"limit\":3}.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"search_phrase": map[string]any{"type": "string", "description": "Keywords. Keep exact error messages verbatim."},
					"limit":         map[string]any{"type": "integer", "minimum": 1, "maximum": 10, "description": "How many results (default 4)."},
					"topics": map[string]any{
						"type": "array", "maxItems": 3,
						"items":       map[string]any{"type": "string", "enum": awsDocTopics},
						"description": "Usually one: troubleshooting for errors, reference_documentation for API parameters, general otherwise.",
					},
				},
				"required":             []string{"search_phrase"},
				"additionalProperties": false,
			},
		},
		ToolSpec{
			Name: awsToolReadDocs,
			Description: "Read AWS documentation pages as markdown, by the exact URL a search_documentation result gave. " +
				"Long pages come back in parts: pass the previous end_index as start_index to continue. " +
				"Example: {\"requests\":[{\"url\":\"https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/accessing-monitoring.html\"}]}.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"requests": map[string]any{
						"type": "array", "minItems": 1, "maxItems": 5,
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"url":         map[string]any{"type": "string", "description": "A documentation URL from search_documentation."},
								"max_length":  map[string]any{"type": "integer", "minimum": 1, "maximum": 20000, "description": "Characters to return (default 10000)."},
								"start_index": map[string]any{"type": "integer", "minimum": 0, "description": "Character offset to continue from."},
							},
							"required":             []string{"url"},
							"additionalProperties": false,
						},
					},
				},
				"required":             []string{"requests"},
				"additionalProperties": false,
			},
		},
		ToolSpec{
			Name:        awsToolListRegions,
			Description: "List the AWS regions with their ids and names (for example us-east-2, US East (Ohio)).",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
		},
		ToolSpec{
			Name: awsToolAvailability,
			Description: "Check whether an AWS service, API operation or CloudFormation resource type is available in up to 10 regions. " +
				"Filter names must match AWS's catalog exactly: api as \"SdkServiceId+Operation\" (\"RDS+DescribeDBInstances\"), " +
				"cfn as a resource type (\"AWS::RDS::DBInstance\"), product as the service's full name (\"Amazon Aurora\"). " +
				"Several regions need filters. Example: {\"resource_type\":\"api\",\"regions\":[\"eu-south-2\"],\"filters\":[\"EKS+DescribeCluster\"]}.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"resource_type": map[string]any{"type": "string", "enum": []string{"product", "api", "cfn"}},
					"regions":       map[string]any{"type": "array", "minItems": 1, "maxItems": 10, "items": map[string]any{"type": "string"}},
					"filters":       map[string]any{"type": "array", "maxItems": 20, "items": map[string]any{"type": "string"}},
					"next_token":    map[string]any{"type": "string", "description": "Continuation token from a previous single-region answer."},
				},
				"required":             []string{"resource_type", "regions"},
				"additionalProperties": false,
			},
		},
	)
}

func awsCallSpec(c Consumer, region string) ToolSpec {
	var lines []string
	for _, f := range awsFamilyTable() {
		var svcs []string
		for _, g := range f.grants {
			if awsGrantFor(f, g, c) && !awsContains(svcs, g.service) {
				svcs = append(svcs, g.service)
			}
		}
		if len(svcs) > 0 {
			lines = append(lines, "- "+f.Label+": "+strings.Join(svcs, ", "))
		}
	}
	globalNote := "AWS Health and Service Quotas defaults"
	if c == ConsumerKobi {
		globalNote = "AWS Health, Cost Explorer and pricing"
	}
	desc := "Run one read-only AWS API operation in the connected AWS account and get its JSON result. " +
		"Pass the boto3 client name as service, the API operation in PascalCase as operation, and the request " +
		"parameters exactly as the AWS API reference names them (PascalCase keys) as params. " +
		"region defaults to " + region + "; set it only for resources in another region (" + globalNote + " answer from us-east-1).\n" +
		"Only read operations (Describe, List, Get, BatchGet, Lookup, Search, Filter, and Logs Insights queries) of these services are enabled:\n" +
		strings.Join(lines, "\n") + "\n" +
		"Keep every result small: pass an identifier (DBInstanceIdentifier, ClusterName, LoadBalancerArns), Filters, " +
		"MaxRecords or MaxResults, and a StartTime/EndTime window for metrics, logs and CloudTrail events. Results are " +
		"paginated automatically, so an unfiltered call on a large account is slow and gets cut.\n" +
		"Examples:\n" +
		`{"service":"rds","operation":"DescribeDBInstances","params":{"DBInstanceIdentifier":"orders-db"}}` + "\n" +
		`{"service":"rds","operation":"DescribeEvents","params":{"SourceType":"db-instance","SourceIdentifier":"orders-db","Duration":120}}` + "\n" +
		`{"service":"cloudwatch","operation":"GetMetricData","params":{"MetricDataQueries":[{"Id":"cpu","MetricStat":{"Metric":{"Namespace":"AWS/RDS","MetricName":"CPUUtilization","Dimensions":[{"Name":"DBInstanceIdentifier","Value":"orders-db"}]},"Period":300,"Stat":"Average"}}],"StartTime":"2026-01-01T10:00:00Z","EndTime":"2026-01-01T11:00:00Z"}}` + "\n" +
		`{"service":"elbv2","operation":"DescribeTargetHealth","params":{"TargetGroupArn":"arn:aws:elasticloadbalancing:..."}}` + "\n" +
		`{"service":"cloudtrail","operation":"LookupEvents","params":{"LookupAttributes":[{"AttributeKey":"ResourceName","AttributeValue":"sg-0123456789abcdef0"}],"MaxResults":20}}` + "\n" +
		"Operations that write, return secret values, query tables or read object contents are refused. Never ask for secrets, passwords or tokens."

	services := awsServices(c)
	sort.Strings(services)
	return ToolSpec{
		Name:        awsToolCall,
		Description: desc,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"service":   map[string]any{"type": "string", "enum": services, "description": "The boto3 client name, e.g. rds, cloudwatch, logs, ec2, elbv2."},
				"operation": map[string]any{"type": "string", "description": "The API operation in PascalCase, e.g. DescribeDBInstances, GetMetricData, LookupEvents."},
				"params":    map[string]any{"type": "object", "description": "The operation's request parameters, as in the AWS API reference."},
				"region":    map[string]any{"type": "string", "description": "AWS region id; defaults to " + region + "."},
			},
			"required":             []string{"service", "operation"},
			"additionalProperties": false,
		},
	}
}
