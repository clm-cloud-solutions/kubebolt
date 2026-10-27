package mcpgateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/mcp/wire"
	"github.com/kubebolt/kubebolt/apps/api/internal/mcpcatalog"
	"github.com/kubebolt/kubebolt/apps/api/internal/mcpclient"
	"github.com/kubebolt/kubebolt/apps/api/internal/settings"
)

// Connector statuses (CONTRACT §5).
const (
	StatusAvailable = "available"
	StatusEnabled   = "enabled"
	StatusPaused    = "paused"
	StatusPlan      = "plan"
	StatusError     = "error"
)

// ConnectorView is the admin API's view of one connector (CONTRACT §5). It
// says which secrets are set, never anything derived from their values.
type ConnectorView struct {
	ID          string              `json:"id"`
	Name        string              `json:"name"`
	Category    mcpcatalog.Category `json:"category"`
	Description string              `json:"description"`
	Tags        []string            `json:"tags"`
	DocsURL     string              `json:"docsURL"`
	MinPlan     string              `json:"minPlan"`
	Entitled    bool                `json:"entitled"`
	UpgradeTo   string              `json:"upgradeTo,omitempty"`
	// Status is the badge. Enabled and Paused are the stored flags of a
	// configured connector (false when it is not), so the drawer offers
	// Pause or Resume from them rather than from the badge.
	Status  string `json:"status"`
	Enabled bool   `json:"enabled"`
	Paused  bool   `json:"paused"`
	// Mode is the stored auth mode ("" when never configured).
	Mode string `json:"mode"`
	// KobiMinRole is the lowest role whose Kobi conversations use the
	// connector: the stored value, or the default when none was set.
	KobiMinRole string `json:"kobiMinRole"`
	// AuthModes omits the modes this install may not offer.
	AuthModes []mcpcatalog.AuthMode `json:"authModes"`
	Families  []mcpcatalog.Family   `json:"families"`
	Never     []string              `json:"never"`
	Tools     ToolNames             `json:"tools"`
	// Reach is how much of the provider each consumer reads; nil when the
	// binder cannot say.
	Reach         *ReachView `json:"reach,omitempty"`
	Config        ConfigView `json:"config"`
	VerifiedAt    *time.Time `json:"verifiedAt,omitempty"`
	VerifiedTools int        `json:"verifiedTools,omitempty"`
	// VerifiedIdentity is who the last passing test signed in as, and
	// VerifiedWarning what it found that still needs attention (the AWS
	// account's root user). Both go with the verification.
	VerifiedIdentity string                `json:"verifiedIdentity,omitempty"`
	VerifiedWarning  string                `json:"verifiedWarning,omitempty"`
	LastError        string                `json:"lastError,omitempty"`
	SetupGuide       mcpcatalog.SetupGuide `json:"setupGuide"`
}

// ToolNames are the LOCAL tool names each consumer gets.
type ToolNames struct {
	Kobi      []string `json:"kobi"`
	Autopilot []string `json:"autopilot"`
}

// ConfigView is the masked stored configuration.
type ConfigView struct {
	Fields            map[string]string `json:"fields"`
	SecretsConfigured map[string]bool   `json:"secretsConfigured"`
	SecretsUnreadable []string          `json:"secretsUnreadable"`
}

// PutRequest is a partial update. Fields and Secrets follow keep/clear/set:
// an absent key or null keeps, "" clears, any other value sets. KobiMinRole
// is "viewer", "editor" or "admin", or "" for the default.
type PutRequest struct {
	Enabled     *bool              `json:"enabled,omitempty"`
	Paused      *bool              `json:"paused,omitempty"`
	Mode        *string            `json:"mode,omitempty"`
	Fields      map[string]*string `json:"fields,omitempty"`
	Secrets     map[string]*string `json:"secrets,omitempty"`
	KobiMinRole *string            `json:"kobiMinRole,omitempty"`
}

// ChangesNothing reports whether the request, read with the keep/clear/set
// rules, leaves everything as it is: no flag, no floor, no mode ("" picks
// nothing) and only null — keep — for every field and secret. Put answers
// such a request with the current view and writes nothing.
func (r PutRequest) ChangesNothing() bool {
	return !r.touchesConfig() && r.Enabled == nil && r.Paused == nil && r.KobiMinRole == nil
}

// touchesConfig reports whether the request changes the configuration: the
// mode, a field or a secret.
func (r PutRequest) touchesConfig() bool {
	return (r.Mode != nil && *r.Mode != "") || anySet(r.Fields) || anySet(r.Secrets)
}

// Draft is a configuration to test without saving it, merged over the stored
// one with the same keep/clear/set rules as PutRequest.
type Draft struct {
	Mode    *string            `json:"mode,omitempty"`
	Fields  map[string]*string `json:"fields,omitempty"`
	Secrets map[string]*string `json:"secrets,omitempty"`
}

func (d *Draft) empty() bool {
	return d == nil || ((d.Mode == nil || *d.Mode == "") && len(d.Fields) == 0 && len(d.Secrets) == 0)
}

// ToolLister is optionally implemented by a connector's Binder to name the
// local tools it exposes per consumer without binding, so the admin view can
// list them before the connector is first used.
type ToolLister interface {
	ToolNames(consumer mcpcatalog.Consumer) []string
}

// ReachLister is implemented by binders that can say, without binding, how
// much of their provider a consumer reads, in the provider's own unit (AWS
// services, Azure commands, Google Cloud tools).
type ReachLister interface {
	Reach(consumer mcpcatalog.Consumer) (unit string, n int)
}

// ReachView is ReachLister's answer for both consumers.
type ReachView struct {
	Unit      string `json:"unit"`
	Kobi      int    `json:"kobi"`
	Autopilot int    `json:"autopilot"`
}

const (
	notOfferedDescription = "Not available on this installation: it signs in with the identity of the server KubeBolt runs on, which only self-hosted, single-tenant installations may use."
	hintFix               = "Fix this setting and test again."
	hintSettings          = "Check the connector settings against the setup guide."
)

// Connectors returns every catalog connector as the org in ctx sees it.
func (g *Gateway) Connectors(ctx context.Context) ([]ConnectorView, error) {
	org, err := orgOf(ctx)
	if err != nil {
		return nil, err
	}
	all, err := g.st.MCP(ctx)
	if err != nil {
		return nil, err
	}
	plan := g.planOf(org)
	defs := g.cat.List()
	out := make([]ConnectorView, 0, len(defs))
	for _, def := range defs {
		all = g.ensureGeneratedForView(ctx, def, all)
		v, err := g.view(ctx, org, plan, def, all)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// Connector returns one connector as the org in ctx sees it.
func (g *Gateway) Connector(ctx context.Context, id string) (ConnectorView, error) {
	org, err := orgOf(ctx)
	if err != nil {
		return ConnectorView{}, err
	}
	def, ok := g.cat.Get(id)
	if !ok {
		return ConnectorView{}, ErrNotFound
	}
	all, err := g.st.MCP(ctx)
	if err != nil {
		return ConnectorView{}, err
	}
	all = g.ensureGeneratedForView(ctx, def, all)
	return g.view(ctx, org, g.planOf(org), def, all)
}

// ensureGenerated creates the values KubeBolt generates for def — the AWS
// external ID — the first time the org's admin sees or saves the connector,
// and returns the configuration as it now stands. A value, once stored, is
// never replaced — not by a later view, not by removing the connector: the
// admin may already have put it in a trust policy. It does not make the
// connector configured; only a saved mode does.
//
// all may be this replica's cached snapshot, older than a value another
// replica generated; settings then keeps the stored value and drops the
// cache, so the re-read below returns it. Two replicas generating at the
// same instant is the blob's cross-replica limit (settings, saveMCP).
//
// Only the admin paths call it. A read a model makes goes through
// ConnectorToolNames, which never writes.
func (g *Gateway) ensureGenerated(ctx context.Context, def *mcpcatalog.ConnectorDef, all settings.ResolvedMCP) (settings.ResolvedMCP, error) {
	generated := generatedFields(def)
	if len(generated) == 0 {
		return all, nil
	}
	cfg := all.Connectors[def.ID]
	values := map[string]string{}
	for name := range generated {
		if strings.TrimSpace(cfg.Fields[name]) != "" {
			continue
		}
		v, err := newGeneratedValue()
		if err != nil {
			return all, fmt.Errorf("generate %s: %w", name, err)
		}
		values[name] = v
	}
	if len(values) == 0 {
		return all, nil
	}
	if _, err := g.st.EnsureMCPConnectorFields(ctx, def.ID, values); err != nil {
		return all, err
	}
	return g.st.MCP(ctx)
}

// ensureGeneratedForView is ensureGenerated for a read: a failed write costs
// the setup guide its value this time, not the page.
func (g *Gateway) ensureGeneratedForView(ctx context.Context, def *mcpcatalog.ConnectorDef, all settings.ResolvedMCP) settings.ResolvedMCP {
	fresh, err := g.ensureGenerated(ctx, def, all)
	if err != nil {
		g.log.Warn("mcp gateway: could not store a generated connector value",
			slog.String("org", auth.TenantIDFromContext(ctx)), slog.String("connector", def.ID), slog.String("error", err.Error()))
		return all
	}
	return fresh
}

// configuredConnector reports whether the org has set the connector up: a
// stored record with a mode. A record that holds only generated values, or
// a flag a stale drawer sent before anything was saved, is not.
func configuredConnector(all settings.ResolvedMCP, id string) (settings.ResolvedConnector, bool) {
	cfg, ok := all.Connectors[id]
	return cfg, ok && cfg.Mode != ""
}

func (g *Gateway) view(ctx context.Context, org, plan string, def *mcpcatalog.ConnectorDef, all settings.ResolvedMCP) (ConnectorView, error) {
	masked, _, err := g.st.MaskedMCPConnector(ctx, def.ID)
	if err != nil {
		return ConnectorView{}, err
	}
	cfg, configured := configuredConnector(all, def.ID)
	entitled, upgradeTo := g.entitlement(plan, def)
	offered := def.ModesFor(g.o.AmbientAllowed)

	v := ConnectorView{
		ID:          def.ID,
		Name:        def.Name,
		Category:    def.Category,
		Description: def.Description,
		Tags:        nonNil(def.Tags),
		DocsURL:     def.DocsURL,
		MinPlan:     def.MinPlan,
		Entitled:    entitled,
		UpgradeTo:   upgradeTo,
		Enabled:     configured && cfg.Enabled,
		Paused:      configured && cfg.Paused,
		Mode:        masked.Mode,
		KobiMinRole: string(kobiMinRole(cfg)),
		AuthModes:   copyModes(offered),
		Families:    nonNil(def.Families),
		Never:       nonNil(def.Never),
		Tools:       g.toolNamesView(org, def, cfg, configured),
		Reach:       reachView(def),
		Config: ConfigView{
			Fields:            nonNilMap(masked.Fields),
			SecretsConfigured: nonNilMap(masked.SecretsConfigured),
			SecretsUnreadable: nonNil(masked.SecretsUnreadable),
		},
		VerifiedAt:       masked.VerifiedAt,
		VerifiedTools:    masked.VerifiedTools,
		VerifiedIdentity: masked.VerifiedIdentity,
		VerifiedWarning:  masked.VerifiedWarning,
		LastError:        masked.LastError,
		SetupGuide:       guideOf(def, g.env, masked.Fields),
	}
	if len(def.AuthModes) > 0 && len(offered) == 0 {
		// Not an error and not a plan matter: the whole connector is off
		// the table on this install, and the card says why.
		v.Description = strings.TrimSpace(v.Description + " " + notOfferedDescription)
	}

	// Paused comes before error: an admin who pauses a failing connector
	// must see it paused, with Resume, and the stored lastError still says
	// why it was failing.
	switch {
	case g.o.MultiTenant && !entitled:
		v.Status = StatusPlan
	case !configured:
		v.Status = StatusAvailable
	case cfg.Paused:
		v.Status = StatusPaused
	case cfg.Enabled && g.problemOf(org, def, cfg, &v):
		v.Status = StatusError
	case cfg.Enabled:
		v.Status = StatusEnabled
	default:
		v.Status = StatusAvailable
	}
	if !autopilotServed {
		v = withoutAutopilot(v)
	}
	return v, nil
}

// problemOf reports whether an enabled connector is in error, filling
// v.LastError with the reason when the stored one is empty: a stored error
// (the last test, or a bind failure this gateway wrote), a configuration that
// cannot be used as saved, or a bind failure too recent to have been written.
func (g *Gateway) problemOf(org string, def *mcpcatalog.ConnectorDef, cfg settings.ResolvedConnector, v *ConnectorView) bool {
	if cfg.LastError != "" {
		return true
	}
	if len(cfg.SecretsUnreadable) > 0 {
		v.LastError = "Some saved credentials can no longer be decrypted (the server's encryption key changed). Enter them again."
		return true
	}
	if _, p := g.readiness(def, cfg); p != nil {
		v.LastError = p.msg
		return true
	}
	if msg := g.failureFor(org, def.ID, cfg); msg != "" {
		v.LastError = msg
		return true
	}
	return false
}

// ConnectorToolNames lists the local tools a connector serves each consumer,
// for the org in ctx, as the admin view names them. It is the read-only door
// for a model's read paths (get_external_connections): it never binds,
// generates a value, renders the guide or writes the org's settings — a
// generated value written there would change the connector's fingerprint and
// rebuild its live binding from a read.
func (g *Gateway) ConnectorToolNames(ctx context.Context, id string) (ToolNames, error) {
	org, err := orgOf(ctx)
	if err != nil {
		return ToolNames{}, err
	}
	def, ok := g.cat.Get(id)
	if !ok {
		return ToolNames{}, ErrNotFound
	}
	all, err := g.st.MCP(ctx)
	if err != nil {
		return ToolNames{}, err
	}
	cfg, configured := configuredConnector(all, id)
	return g.toolNamesView(org, def, cfg, configured), nil
}

// toolNamesView lists local tool names from the live binding when there is
// one for the current configuration, else from the binder's static list when
// it has one. It never binds: the admin page must not wait on a provider.
func (g *Gateway) toolNamesView(org string, def *mcpcatalog.ConnectorDef, cfg settings.ResolvedConnector, configured bool) ToolNames {
	out := ToolNames{Kobi: []string{}, Autopilot: []string{}}
	if configured {
		if e := g.liveBinding(org, def.ID, cfg); e != nil {
			defer g.release(e)
			out.Kobi = localNames(e.b, mcpcatalog.ConsumerKobi, def.ID)
			out.Autopilot = localNames(e.b, mcpcatalog.ConsumerAutopilot, def.ID)
			return out
		}
	}
	if l, ok := def.Binder.(ToolLister); ok {
		out.Kobi = validNames(def.ID, l.ToolNames(mcpcatalog.ConsumerKobi))
		out.Autopilot = validNames(def.ID, l.ToolNames(mcpcatalog.ConsumerAutopilot))
	}
	return out
}

func reachView(def *mcpcatalog.ConnectorDef) *ReachView {
	l, ok := def.Binder.(ReachLister)
	if !ok {
		return nil
	}
	unit, kobi := l.Reach(mcpcatalog.ConsumerKobi)
	_, autopilot := l.Reach(mcpcatalog.ConsumerAutopilot)
	return &ReachView{Unit: unit, Kobi: kobi, Autopilot: autopilot}
}

// withoutAutopilot is the view an edition without Autopilot serves: no tools,
// reach or family marked for a consumer that never calls.
func withoutAutopilot(v ConnectorView) ConnectorView {
	v.Tools.Autopilot = []string{}
	if v.Reach != nil {
		r := *v.Reach
		r.Autopilot = 0
		v.Reach = &r
	}
	families := make([]mcpcatalog.Family, len(v.Families))
	for i, f := range v.Families {
		f.Autopilot = false
		families[i] = f
	}
	v.Families = families
	return v
}

func localNames(b mcpcatalog.Binding, c mcpcatalog.Consumer, id string) []string {
	specs, ok := safeTools(b, c)
	if !ok {
		return []string{}
	}
	names := make([]string, 0, len(specs))
	for _, s := range specs {
		names = append(names, s.Name)
	}
	return validNames(id, names)
}

func validNames(id string, names []string) []string {
	out := make([]string, 0, len(names))
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] || mcpcatalog.ValidateLocalName(id, n) != nil {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Put applies a partial update for the org in ctx and returns the fresh view.
// Validation failures are *settings.ValidationError (400 with the field), a
// plan the connector is not in is *PlanError (409), an unknown id ErrNotFound.
func (g *Gateway) Put(ctx context.Context, id string, req PutRequest, actor string) (ConnectorView, error) {
	org, err := orgOf(ctx)
	if err != nil {
		return ConnectorView{}, err
	}
	def, ok := g.cat.Get(id)
	if !ok {
		return ConnectorView{}, ErrNotFound
	}
	if verr := refuseGenerated(def, req.Fields, req.Secrets); verr != nil {
		return ConnectorView{}, verr
	}
	if req.KobiMinRole != nil && *req.KobiMinRole != "" && !auth.ValidRole(auth.Role(*req.KobiMinRole)) {
		return ConnectorView{}, &settings.ValidationError{Field: "kobiMinRole", Message: "Choose viewer, editor or admin."}
	}
	// Only a non-nil value changes anything: nil means keep, so a patch of
	// nothing but nulls must not pass through the plan gate or write a record.
	touched := req.touchesConfig()
	turningOn := req.Enabled != nil && *req.Enabled
	if req.ChangesNothing() {
		// Nothing to change: answer with the current view and write nothing,
		// so a no-op never creates a record for an unconfigured connector.
		return g.Connector(ctx, id)
	}
	// Disabling, pausing, resuming and the role floor stay possible after a
	// downgrade: an org must always be able to switch off, or narrow, what
	// it can no longer use.
	if touched || turningOn {
		if ok, up := g.entitlement(g.planOf(org), def); !ok {
			return ConnectorView{}, &PlanError{RequiredPlan: def.MinPlan, UpgradeTo: up}
		}
	}
	all, err := g.st.MCP(ctx)
	if err != nil {
		return ConnectorView{}, err
	}
	if all, err = g.ensureGenerated(ctx, def, all); err != nil {
		return ConnectorView{}, err
	}
	cfg, configured := configuredConnector(all, id)

	patch := settings.MCPConnectorPatch{Enabled: req.Enabled, Paused: req.Paused, KobiMinRole: req.KobiMinRole, UpdatedBy: actor}
	if !configured && !touched && !turningOn {
		// Pausing or switching off a connector nobody set up changes
		// nothing. Writing the flag would create a record for it, and a
		// stored pause would land the first real setup paused.
		patch.Enabled, patch.Paused = nil, nil
		if patch.KobiMinRole == nil {
			return g.Connector(ctx, id)
		}
	}
	willBeEnabled := configured && cfg.Enabled
	if req.Enabled != nil {
		willBeEnabled = *req.Enabled
	}
	if touched || turningOn {
		mode, verr := g.pickMode(def, cfg.Mode, req.Mode)
		if verr != nil {
			return ConnectorView{}, verr
		}
		eff := effectiveOf(def, mode, cfg, req.Fields, req.Secrets)
		// Enabling needs a complete configuration; a disabled connector may
		// be saved half filled in, but every value present must be valid.
		if verr := eff.validateWith(def, g.env, willBeEnabled); verr != nil {
			return ConnectorView{}, verr
		}
		if mode.ID != cfg.Mode {
			m := mode.ID
			patch.Mode = &m
		}
		patch.Fields = fieldsPatch(mode, req.Fields, eff.staleFields)
		patch.Secrets = secretsPatch(req.Secrets, eff.staleSecrets)
		if !configured && cfg.Paused && req.Paused == nil {
			// A pause stored before the connector was ever set up (an
			// earlier build wrote one) must not land this setup paused.
			resumed := false
			patch.Paused = &resumed
		}
	}
	err = g.st.PutMCPConnector(ctx, id, patch)
	// A new role floor changes who may use the connector, not how it
	// connects: the binding stays. Anything else drops it — also on error,
	// since a failed commit may still have landed.
	if floorOnly := patch.Enabled == nil && patch.Paused == nil && !touched && !turningOn; !floorOnly {
		g.Invalidate(ctx, id)
	}
	if err != nil {
		return ConnectorView{}, err
	}
	return g.Connector(ctx, id)
}

// pickMode resolves the auth mode a write or a test uses: the requested one,
// else the stored one, else the only one offered.
func (g *Gateway) pickMode(def *mcpcatalog.ConnectorDef, stored string, requested *string) (mcpcatalog.AuthMode, *settings.ValidationError) {
	id := stored
	if requested != nil && *requested != "" {
		id = *requested
	}
	offered := def.ModesFor(g.o.AmbientAllowed)
	if len(offered) == 0 {
		return mcpcatalog.AuthMode{}, &settings.ValidationError{Field: "mode", Message: msgNoModes}
	}
	if id == "" {
		if len(offered) == 1 {
			return offered[0], nil
		}
		return mcpcatalog.AuthMode{}, &settings.ValidationError{Field: "mode", Message: "Choose how KubeBolt signs in to " + def.Name + "."}
	}
	for _, m := range offered {
		if m.ID == id {
			return m, nil
		}
	}
	if _, known := def.Mode(id); known {
		return mcpcatalog.AuthMode{}, &settings.ValidationError{Field: "mode", Message: "This sign-in method is not available on this installation."}
	}
	return mcpcatalog.AuthMode{}, &settings.ValidationError{Field: "mode", Message: "Unknown sign-in method."}
}

// fieldsPatch is the request's field changes, trimmed, plus the clearing of
// stored fields the mode does not know (another mode's, after a switch).
func fieldsPatch(mode mcpcatalog.AuthMode, req map[string]*string, stale []string) map[string]*string {
	multiline := map[string]bool{}
	for _, f := range mode.Fields {
		multiline[f.Name] = f.Multiline
	}
	out := make(map[string]*string, len(req)+len(stale))
	for name, v := range req {
		if v == nil {
			continue
		}
		val := *v
		if !multiline[name] {
			val = strings.TrimSpace(val)
		}
		out[name] = &val
	}
	for _, name := range stale {
		if _, set := out[name]; !set {
			empty := ""
			out[name] = &empty
		}
	}
	return out
}

func secretsPatch(req map[string]*string, stale []string) map[string]*string {
	out := make(map[string]*string, len(req)+len(stale))
	for name, v := range req {
		if v != nil {
			out[name] = v
		}
	}
	for _, name := range stale {
		if _, set := out[name]; !set {
			empty := ""
			out[name] = &empty
		}
	}
	return out
}

// Delete removes the org's configuration of a connector, secrets included,
// and drops its binding. The values KubeBolt generated for the org stay: the
// AWS external ID is already in the customer's trust policy, and re-adding
// the connector must print the same one, or the role it names stops
// trusting KubeBolt. What is left reads as never configured.
func (g *Gateway) Delete(ctx context.Context, id string) error {
	if _, err := orgOf(ctx); err != nil {
		return err
	}
	def, ok := g.cat.Get(id)
	if !ok {
		return ErrNotFound
	}
	generated := make([]string, 0, len(generatedFields(def)))
	for name := range generatedFields(def) {
		generated = append(generated, name)
	}
	sort.Strings(generated)
	err := g.st.DeleteMCPConnector(ctx, id, generated...)
	g.Invalidate(ctx, id)
	return err
}

// Test checks a connector's configuration against the provider: the stored
// one (draft nil), or a draft merged over it and never saved. Only a test of
// the stored configuration records its outcome. Tests are paced per org
// (*RateLimitError); a configuration that is not valid fails without
// reaching the provider and without spending the pace.
func (g *Gateway) Test(ctx context.Context, id string, draft *Draft) (mcpcatalog.TestResult, error) {
	org, err := orgOf(ctx)
	if err != nil {
		return mcpcatalog.TestResult{}, err
	}
	def, ok := g.cat.Get(id)
	if !ok {
		return mcpcatalog.TestResult{}, ErrNotFound
	}
	if ok, up := g.entitlement(g.planOf(org), def); !ok {
		return mcpcatalog.TestResult{}, &PlanError{RequiredPlan: def.MinPlan, UpgradeTo: up}
	}
	if draft.empty() {
		draft = nil
	}
	var (
		modeReq        *string
		fields, secret map[string]*string
	)
	if draft != nil {
		if verr := refuseGenerated(def, draft.Fields, draft.Secrets); verr != nil {
			return mcpcatalog.TestResult{}, verr
		}
		modeReq, fields, secret = draft.Mode, draft.Fields, draft.Secrets
	}
	all, err := g.st.MCP(ctx)
	if err != nil {
		return mcpcatalog.TestResult{}, err
	}
	if all, err = g.ensureGenerated(ctx, def, all); err != nil {
		return mcpcatalog.TestResult{}, err
	}
	cfg, configured := configuredConnector(all, id)
	if draft == nil && !configured {
		return failedTest(msgNotConfigured, "Fill in the settings and test them before saving."), nil
	}

	var res mcpcatalog.TestResult
	mode, verr := g.pickMode(def, cfg.Mode, modeReq)
	if verr == nil {
		eff := effectiveOf(def, mode, cfg, fields, secret)
		if verr = eff.validateWith(def, g.env, true); verr == nil {
			if wait, ok := g.allowTest(org, id); !ok {
				return mcpcatalog.TestResult{}, &RateLimitError{RetryAfter: wait}
			}
			res = g.runTest(ctx, def, resolvedFor(org, eff))
		}
	}
	if verr != nil {
		res = failedTest(verr.Message, hintFix)
	}
	if draft == nil {
		g.recordTest(ctx, org, id, cfg, res)
	}
	return res, nil
}

// runTest binds a temporary binding for r, runs its test and closes it. It
// never touches the cached bindings: a draft must not serve a single request.
func (g *Gateway) runTest(ctx context.Context, def *mcpcatalog.ConnectorDef, r mcpcatalog.Resolved) mcpcatalog.TestResult {
	start := time.Now()
	tctx, cancel := context.WithTimeout(ctx, g.o.BindTimeout+g.o.CallTimeout)
	defer cancel()
	b, err := safeBind(tctx, def.Binder, r, g.env)
	if err == nil && b == nil {
		err = errors.New("the connector returned no binding")
	}
	var res mcpcatalog.TestResult
	switch {
	case err == nil:
		res, err = safeTest(tctx, b)
		closeBinding(context.WithoutCancel(ctx), b, g.log)
	case b != nil:
		// A half-built binding may already hold a provider session.
		closeBinding(context.WithoutCancel(ctx), b, g.log)
	}
	if err != nil {
		msg, hint := adminError(def, err)
		res = mcpcatalog.TestResult{Error: msg, Hint: hint}
	}
	if res.LatencyMs == 0 {
		res.LatencyMs = time.Since(start).Milliseconds()
	}
	if !res.OK && res.Error == "" {
		res.Error = "The connection test failed."
	}
	// A binder may quote the provider; the admin page is no place for a
	// credential either.
	res.Error = shortRedacted(res.Error, 500)
	res.Hint = shortRedacted(res.Hint, 500)
	res.Warning = shortRedacted(res.Warning, 500)
	res.Identity = shortRedacted(res.Identity, 300)
	if !res.OK {
		// A warning qualifies a pass; on a failure the error says it all.
		res.Warning = ""
	}
	return res
}

func failedTest(msg, hint string) mcpcatalog.TestResult {
	return mcpcatalog.TestResult{Error: msg, Hint: hint}
}

// recordTest stores the outcome of a test of the stored configuration: the
// verification on success — with who it signed in as and any warning — and
// the error on failure, keeping the last good verification. A configuration
// saved while the test ran is left alone — the result describes one that no
// longer exists; settings checks that again against what is stored, since
// this process's snapshot may predate a save on another replica.
//
// A passing test also retires a cached binding that serves less than the
// provider just offered — one with parts missing, or fewer tools than the
// test counted — so the next request rebuilds it instead of it serving as it
// is. A whole binding stays: rebuilding it gains nothing and costs the next
// call a new provider session (an AWS cold start is ~11 s).
func (g *Gateway) recordTest(ctx context.Context, org, id string, tested settings.ResolvedConnector, res mcpcatalog.TestResult) {
	all, err := g.st.MCP(ctx)
	if err != nil {
		return
	}
	cur, ok := all.Connectors[id]
	if !ok || fingerprint(cur) != fingerprint(tested) {
		return
	}
	if res.OK {
		err = g.st.SetMCPConnectorVerification(ctx, id, settings.MCPVerification{
			ConfigAt: cur.UpdatedAt,
			Verified: &settings.MCPVerified{At: g.now().UTC(), Tools: res.Tools, Identity: res.Identity, Warning: res.Warning},
		})
		g.mu.Lock()
		delete(g.failures, bindingKey{org, id})
		g.mu.Unlock()
		g.retireIfBehind(org, id, cur, res.Tools)
	} else {
		err = g.st.SetMCPConnectorVerification(ctx, id, settings.MCPVerification{ConfigAt: cur.UpdatedAt, LastError: res.Error})
	}
	if err != nil && !errors.Is(err, settings.ErrMCPConnectorNotFound) && !errors.Is(err, settings.ErrMCPConnectorChanged) {
		g.log.Warn("mcp gateway: could not record the test result",
			slog.String("org", org), slog.String("connector", id), slog.String("error", err.Error()))
	}
}

// retireIfBehind retires the org's live binding of the stored configuration
// cfg when it is partial or serves Kobi fewer tools than a passing test just
// counted (TestResult.Tools counts Kobi's). The tool list is read outside the
// gateway's lock, holding a reference, like every other read of a binding.
func (g *Gateway) retireIfBehind(org, id string, cfg settings.ResolvedConnector, tested int) {
	e := g.liveBinding(org, id, cfg)
	if e == nil {
		return
	}
	defer g.release(e)
	specs, ok := safeTools(e.b, mcpcatalog.ConsumerKobi)
	g.mu.Lock()
	defer g.mu.Unlock()
	if e.missing > 0 || !ok || len(specs) < tested {
		g.retireLocked(e)
	}
}

// safeTest runs a binding's test, turning a panic into an error.
func safeTest(ctx context.Context, b mcpcatalog.Binding) (res mcpcatalog.TestResult, err error) {
	defer func() {
		if p := recover(); p != nil {
			res, err = mcpcatalog.TestResult{}, errBindingPanic
		}
	}()
	return b.Test(ctx)
}

// adminError phrases a bind or test failure for the admin who has to fix it:
// what happened, and what to check.
func adminError(def *mcpcatalog.ConnectorDef, err error) (msg, hint string) {
	var (
		fe      *mcpcatalog.FieldError
		ve      *settings.ValidationError
		httpErr *mcpclient.HTTPError
		rpcErr  *wire.Error
		blocked *mcpclient.BlockedError
		dnsErr  *net.DNSError
		opErr   *net.OpError
	)
	switch {
	case errors.As(err, &fe):
		return fe.Message, hintFix
	case errors.As(err, &ve):
		return ve.Message, hintFix
	case errors.Is(err, errBindingPanic):
		return "The connector failed inside KubeBolt.", "Try again; if it persists, report it to KubeBolt support."
	case errors.Is(err, errBindTimeout), errors.Is(err, context.DeadlineExceeded):
		return def.Name + " did not answer in time.", "Check that KubeBolt can reach the provider (outbound network, proxy, firewall) and try again."
	case errors.Is(err, mcpclient.ErrUnauthorized):
		return def.Name + " rejected the credentials.", "Check that the credentials are current and that the role or service account still exists and trusts KubeBolt (see the setup guide)."
	case errors.Is(err, mcpclient.ErrRateLimited):
		return def.Name + " is rate limiting requests.", "Wait a minute and test again."
	case errors.As(err, &blocked):
		return "KubeBolt refused to connect to this endpoint because its address is private or reserved.", "Only the provider's public endpoints and the local Azure MCP sidecar are allowed."
	case errors.As(err, &httpErr):
		switch {
		case httpErr.Status == 403:
			return fmt.Sprintf("%s answered 403 Forbidden: the credentials were accepted but lack permission.", def.Name), "Check the permissions granted to the role or service account (see the setup guide)."
		case httpErr.Status == 404:
			return fmt.Sprintf("%s answered 404 Not Found.", def.Name), "Check the region, project or endpoint settings."
		case httpErr.Status >= 500:
			return fmt.Sprintf("%s answered HTTP %d, an error on the provider's side.", def.Name, httpErr.Status), "Try again in a few minutes."
		}
		return fmt.Sprintf("%s answered HTTP %d.", def.Name, httpErr.Status), hintSettings
	case errors.As(err, &rpcErr):
		return fmt.Sprintf("%s returned an error: %s", def.Name, shortRedacted(rpcErr.Message, 300)), hintSettings
	case errors.As(err, &dnsErr), errors.As(err, &opErr), errors.Is(err, syscall.ECONNREFUSED):
		return "KubeBolt could not reach " + def.Name + ".", "Check outbound network access from KubeBolt to the provider (egress rules, proxy, DNS)."
	}
	return "The connection failed: " + shortRedacted(err.Error(), 300), hintSettings
}

// guideOf renders the setup guide with a non-nil step list.
func guideOf(def *mcpcatalog.ConnectorDef, env mcpcatalog.Env, fields map[string]string) mcpcatalog.SetupGuide {
	var guide mcpcatalog.SetupGuide
	if def.Guide != nil {
		guide = def.Guide(env, nonNilMap(fields))
	}
	if guide.Steps == nil {
		guide.Steps = []mcpcatalog.SetupStep{}
	}
	return guide
}

// copyModes copies the offered modes with non-nil field lists, so the JSON
// carries [] rather than null.
func copyModes(modes []mcpcatalog.AuthMode) []mcpcatalog.AuthMode {
	out := make([]mcpcatalog.AuthMode, len(modes))
	for i, m := range modes {
		m.Fields = nonNil(append([]mcpcatalog.FieldSchema(nil), m.Fields...))
		out[i] = m
	}
	return out
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func nonNilMap[K comparable, V any](m map[K]V) map[K]V {
	if m == nil {
		return map[K]V{}
	}
	return m
}

// anySet reports whether a keep/clear/set patch map changes anything.
func anySet(m map[string]*string) bool {
	for _, v := range m {
		if v != nil {
			return true
		}
	}
	return false
}
