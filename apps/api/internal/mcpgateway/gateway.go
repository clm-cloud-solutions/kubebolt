// Package mcpgateway is the one door between the models (Kobi, Autopilot) and
// the external MCP connectors an org has turned on.
//
// It joins four things that must agree on every request: the curated catalog
// (which connectors and tools exist), the org's stored configuration (whether
// it is enabled and with which credentials), the plan gate, and the audit
// trail. ToolsFor lists only what the org has enabled and may use; Call checks
// the name against that same list again before a binding is touched, and the
// binding re-checks the command or operation inside it — three barriers, so a
// model that invents a tool name reaches nothing.
//
// Bindings (live, credentialed connections) are cached per (org, connector)
// and keyed by a fingerprint of the configuration they were built from: a
// saved change is picked up by the next request without anyone having to
// remember to invalidate, and one org's binding can never be looked up under
// another org's key. A background sweep closes the ones nobody uses, and
// Shutdown closes the rest — each holds provider sessions (an AWS sandbox
// per session) that are released, not abandoned.
package mcpgateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/time/rate"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/mcpcatalog"
	"github.com/kubebolt/kubebolt/apps/api/internal/mcpclient"
	"github.com/kubebolt/kubebolt/apps/api/internal/settings"
)

// Defaults for Options left at their zero value.
const (
	DefaultOrgRatePerMin         = 120
	DefaultPerBindingConcurrency = 4
	// DefaultBindTimeout bounds how long a request waits for a binding to be
	// built (credentials exchanged, tool lists fetched). Kobi's first turn
	// waits on it, so it is short; a slow provider is skipped for that turn,
	// not waited on.
	DefaultBindTimeout = 10 * time.Second
)

// Tuning that is not worth an option.
const (
	// bindRetryAfter is how long a failed bind is remembered before the next
	// request tries again. Without it a provider that is down would cost every
	// Kobi turn the full bind timeout.
	bindRetryAfter = time.Minute
	// lastErrorEvery throttles the lastError write a failed bind makes: the
	// admin should see why the connector is not answering, but a request path
	// must not rewrite the org's settings blob on every turn.
	lastErrorEvery = 10 * time.Minute
	// idleTTL closes bindings nobody has used for a while: each holds provider
	// sessions and cached credentials, and in SaaS there is one per org.
	idleTTL    = 30 * time.Minute
	sweepEvery = time.Minute
	// partialRebindAfter is how long a binding that came up with parts
	// missing (a GCP endpoint whose listing failed) serves as it is before
	// the gateway builds it again in the background. Long enough not to
	// hammer a provider that is down, short enough that a transient failure
	// at bind time does not cost the org those tools for as long as it keeps
	// using the connector. Each rebuild that brings nothing better doubles
	// the wait, up to partialRebindMax: a binding that stays partial (an
	// Azure sidecar started without some namespaces, a GCP catalog that is
	// always refused) would otherwise be rebuilt — a new provider session and
	// every tool list — every few minutes for as long as the org uses it.
	partialRebindAfter = 3 * time.Minute
	partialRebindMax   = 30 * time.Minute
	// testEvery is the pace of connectivity tests per (org, connector): each
	// one exchanges credentials and lists tools at the provider.
	testEvery    = 5 * time.Second
	closeTimeout = 5 * time.Second
	writeTimeout = 5 * time.Second
)

// Options configures a Gateway.
type Options struct {
	// MultiTenant turns on the plan gate. Single-tenant installs are never
	// gated (their default tenant carries a plan string the catalog does not
	// know, which would read as Free).
	MultiTenant bool
	// AmbientAllowed lets modes that borrow the machine's own identity be
	// offered: AWS ambient, GCP ADC and the Azure sidecar. True on
	// single-tenant installs; in multi-tenant ones only through the
	// development override, because those credentials would be KubeBolt's.
	AmbientAllowed bool
	// PlanFor resolves an org's plan id. Only called when MultiTenant; nil
	// there means no org is entitled (fail closed).
	PlanFor func(org string) string
	// AzmcpURL is the Azure MCP Server sidecar endpoint; its host:port is the
	// only private address the dial guard admits.
	AzmcpURL string
	// AWSPrincipalARN is KubeBolt's own AWS identity, printed into the AWS
	// setup guide's trust policy (env KUBEBOLT_MCP_AWS_PRINCIPAL_ARN).
	AWSPrincipalARN string
	// CallTimeout bounds one tool call (default KUBEBOLT_MCP_TOOL_TIMEOUT or
	// 30s). BindTimeout bounds building a binding (default 10s).
	CallTimeout time.Duration
	BindTimeout time.Duration
	// OrgRatePerMin is the per-org budget of tool calls per minute, shared by
	// Kobi and Autopilot and by every connector (default 120).
	OrgRatePerMin int
	// PerBindingConcurrency caps concurrent calls on one binding (default 4),
	// so one org cannot trip the provider's throttling on its own account.
	PerBindingConcurrency int
	Logger                *slog.Logger
	// Registerer receives the gateway's metrics; nil leaves them unregistered.
	Registerer prometheus.Registerer
	// Now is the clock (tests).
	Now func() time.Time
	// SweepInterval is how often the background sweep wakes up (default one
	// minute; tests shorten it). The sweep itself still runs at most once a
	// minute of Now.
	SweepInterval time.Duration
}

// Errors the admin API maps to HTTP answers.
var (
	// ErrNotFound: the connector id is not in the catalog.
	ErrNotFound = errors.New("mcpgateway: unknown connector")
	// ErrNoOrg: the context carries no organization. Refused rather than
	// defaulted: in a multi-tenant install "default" is someone else's org.
	ErrNoOrg = errors.New("mcpgateway: request carries no organization")
)

// PlanError is returned when the org's plan does not include the connector.
// RequiredPlan is the catalog minimum; UpgradeTo is the cheapest plan the org
// can adopt today that unlocks it ("" when none is for sale).
type PlanError struct {
	RequiredPlan string
	UpgradeTo    string
}

func (e *PlanError) Error() string {
	return "mcpgateway: connector requires the " + e.RequiredPlan + " plan"
}

// RateLimitError is returned by Test when the org tested too recently.
type RateLimitError struct {
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	return "mcpgateway: rate limited, retry after " + e.RetryAfter.String()
}

// errBindTimeout is what a waiter gets when the binding is still being built
// after BindTimeout; the build itself carries on and the next request uses it.
var errBindTimeout = errors.New("mcpgateway: the connector did not answer in time")

// errStopped is what a request gets once Shutdown has begun: no new binding
// is built, since nothing would close it.
var errStopped = errors.New("mcpgateway: shutting down")

// Gateway serves external MCP tools to Kobi and Autopilot. Safe for
// concurrent use.
type Gateway struct {
	cat *mcpcatalog.Catalog
	st  *settings.Runtime
	o   Options
	env mcpcatalog.Env
	log *slog.Logger
	now func() time.Time
	m   *metrics

	mu        sync.Mutex
	bindings  map[bindingKey]*entry
	failures  map[bindingKey]*bindFailure
	limiters  map[string]*rate.Limiter
	lastTest  map[string]time.Time
	lastSweep time.Time
	// stopped is set by Shutdown: from then on nothing new is built.
	stopped bool

	// open counts the entries whose binding is not closed yet — being
	// built, cached, retired but still in use, or being rebuilt — so
	// Shutdown can wait for the provider sessions to be released. It is
	// only incremented under mu while !stopped, which is what makes the
	// Wait in Shutdown safe.
	open      sync.WaitGroup
	stopSweep chan struct{}
	stopOnce  sync.Once
}

// bindingKey always carries the org: there is no lookup by connector alone.
type bindingKey struct{ org, id string }

// entry is one binding, built once and shared by every request of the org
// with the same configuration fingerprint.
type entry struct {
	key  bindingKey
	fp   string
	name string // connector display name, for log lines
	// base carries the values (tenant) of the request that created the entry
	// but not its cancellation: the binding outlives that request.
	base  context.Context
	ready chan struct{}
	sem   chan struct{}
	// started is when the build began (monotonic), so a waiter never waits
	// past the build's own deadline.
	started time.Time

	// b and err are written once, before ready is closed.
	b   mcpcatalog.Binding
	err error

	// Guarded by Gateway.mu.
	refs     int
	retired  bool
	closed   bool
	lastUsed time.Time
	// missing is how many parts of the binding failed to come up
	// (mcpcatalog.PartialBinding); a partial one is rebuilt in the
	// background from nextRebind on, one attempt at a time (rebinding).
	// rebindWait is the wait before the next attempt: partialRebindAfter
	// for a new binding, doubled by every attempt that is not better.
	missing    int
	nextRebind time.Time
	rebindWait time.Duration
	rebinding  bool
}

// bindFailure remembers why an org's connector could not be bound, so the
// next requests skip it for a while and the admin view can say why.
type bindFailure struct {
	fp  string
	err error
	msg string // the admin sentence
	at  time.Time
	// written is the last lastError this gateway stored for the connector,
	// and when; it throttles the write and lets a later success clear it.
	written   string
	writtenAt time.Time
}

// New builds a gateway. It never fails: a misconfigured sidecar URL only
// means the Azure connector cannot reach it, which its test reports.
func New(cat *mcpcatalog.Catalog, st *settings.Runtime, o Options) *Gateway {
	if o.CallTimeout <= 0 {
		o.CallTimeout = mcpclient.CallTimeoutFromEnv()
	}
	if o.BindTimeout <= 0 {
		o.BindTimeout = DefaultBindTimeout
	}
	if o.OrgRatePerMin <= 0 {
		o.OrgRatePerMin = DefaultOrgRatePerMin
	}
	if o.PerBindingConcurrency <= 0 {
		o.PerBindingConcurrency = DefaultPerBindingConcurrency
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.SweepInterval <= 0 {
		o.SweepInterval = sweepEvery
	}
	g := &Gateway{
		cat: cat,
		st:  st,
		o:   o,
		log: o.Logger.With(slog.String("component", "mcpgateway")),
		now: o.Now,
		env: mcpcatalog.Env{
			AmbientAllowed:  o.AmbientAllowed,
			MultiTenant:     o.MultiTenant,
			AzmcpURL:        o.AzmcpURL,
			CallTimeout:     o.CallTimeout,
			UserAgent:       "KubeBolt",
			AWSPrincipalARN: o.AWSPrincipalARN,
		},
		m:         newMetrics(o.Registerer, o.Logger),
		bindings:  map[bindingKey]*entry{},
		failures:  map[bindingKey]*bindFailure{},
		limiters:  map[string]*rate.Limiter{},
		lastTest:  map[string]time.Time{},
		stopSweep: make(chan struct{}),
	}
	if o.AzmcpURL != "" {
		// The exact host:port the transport dials: an allowlist entry of
		// "localhost:5001" would not cover "http://127.0.0.1:5001/".
		if hp, err := mcpclient.EndpointHostPort(o.AzmcpURL); err == nil {
			g.env.AllowHostPorts = []string{hp}
		} else {
			g.log.Warn("mcp gateway: ignoring an invalid Azure MCP sidecar URL", slog.String("error", err.Error()))
		}
	}
	if o.MultiTenant && o.AmbientAllowed {
		g.log.Warn("mcp gateway: ambient cloud credentials are allowed in a multi-tenant install (KUBEBOLT_MCP_ALLOW_AMBIENT); every org would read clouds with KubeBolt's own identity — development only")
	}
	go g.sweepLoop(o.SweepInterval)
	return g
}

// Shutdown retires every binding and waits until their provider sessions are
// closed, or until ctx ends. From the moment it starts nothing new is built:
// a request that arrives later finds its connector unavailable. A call still
// in flight keeps its binding until it finishes; that binding is closed then,
// and Shutdown waits for it too.
func (g *Gateway) Shutdown(ctx context.Context) {
	g.stopOnce.Do(func() { close(g.stopSweep) })
	g.mu.Lock()
	g.stopped = true
	for _, e := range g.bindings {
		g.retireLocked(e)
	}
	g.mu.Unlock()

	done := make(chan struct{})
	go func() {
		g.open.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		g.log.Warn("mcp gateway: shutdown ended before every provider session was closed")
	}
}

// Close is Shutdown for callers that have no context: it waits at most
// closeTimeout, the time one session close is given.
func (g *Gateway) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
	defer cancel()
	g.Shutdown(ctx)
}

// sweepLoop runs the idle sweep on a timer: without it a binding nobody uses
// would only be closed when some other request came along, and a quiet
// install would keep its provider sessions open until the next restart.
func (g *Gateway) sweepLoop(every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-g.stopSweep:
			return
		case <-t.C:
			g.mu.Lock()
			g.sweepLocked(g.now())
			g.mu.Unlock()
		}
	}
}

// Invalidate drops the org's binding for a connector; the next request binds
// again from the stored configuration. The settings layer invalidates only
// its own cache, so every write path calls this too.
func (g *Gateway) Invalidate(ctx context.Context, id string) {
	org := auth.TenantIDFromContext(ctx)
	if org == "" {
		return
	}
	key := bindingKey{org, id}
	g.mu.Lock()
	defer g.mu.Unlock()
	if e := g.bindings[key]; e != nil {
		g.retireLocked(e)
	}
	delete(g.failures, key)
}

// EnabledConnectors returns the ids of the connectors that would contribute
// tools for the org in ctx — enabled, not paused, entitled and with a usable
// configuration — without building any binding. Used for prompt text, where
// a network round trip would be out of place. The consumer is the one ctx
// names (Kobi unless mcpcatalog.WithConsumer said otherwise), so Kobi's
// prompt never mentions a connector below the caller's role floor.
func (g *Gateway) EnabledConnectors(ctx context.Context) []string {
	org, err := orgOf(ctx)
	if err != nil {
		return nil
	}
	targets, err := g.usableConnectors(ctx, org, mcpcatalog.ConsumerFrom(ctx))
	if err != nil {
		return nil
	}
	ids := make([]string, 0, len(targets))
	for _, t := range targets {
		ids = append(ids, t.def.ID)
	}
	return ids
}

// target is a connector the org can use right now, with the mode its stored
// configuration selects.
type target struct {
	def  *mcpcatalog.ConnectorDef
	cfg  settings.ResolvedConnector
	mode mcpcatalog.AuthMode
}

// usableConnectors lists, in catalog order, the org's connectors that are
// enabled, not paused, open to the consumer and the caller in ctx (the Kobi
// role floor), entitled, in a mode this install offers and whose stored
// configuration is complete.
func (g *Gateway) usableConnectors(ctx context.Context, org string, consumer mcpcatalog.Consumer) ([]target, error) {
	all, err := g.st.MCP(ctx)
	if err != nil {
		return nil, err
	}
	plan := g.planOf(org)
	var out []target
	for _, def := range g.cat.List() {
		cfg, ok := all.Connectors[def.ID]
		if !ok || !cfg.Enabled || cfg.Paused || !reachable(ctx, consumer, cfg) {
			continue
		}
		if entitled, _ := g.entitlement(plan, def); !entitled {
			continue
		}
		mode, problem := g.readiness(def, cfg)
		if problem != nil {
			continue
		}
		out = append(out, target{def: def, cfg: cfg, mode: mode})
	}
	return out, nil
}

// planOf resolves the org's plan, and only in multi-tenant installs: the
// single-tenant short-circuit must come BEFORE the plan helper, which reads
// the default tenant's plan string as Free.
func (g *Gateway) planOf(org string) string {
	if !g.o.MultiTenant || g.o.PlanFor == nil {
		return ""
	}
	return g.o.PlanFor(org)
}

// entitlement applies the plan gate for a plan resolved with planOf.
func (g *Gateway) entitlement(plan string, def *mcpcatalog.ConnectorDef) (entitled bool, upgradeTo string) {
	if !g.o.MultiTenant {
		return true, ""
	}
	return auth.MCPConnectorEntitled(plan, def.MinPlan)
}

// Offers reports whether this install offers at least one of def's auth
// modes, so an org here could set it up at all. The price lists use it (the
// platform matrix's offeredHere, the Billing ladder's connectors line): on a
// multi-tenant install Azure's only mode borrows the machine's identity and
// is offered only through the KUBEBOLT_MCP_ALLOW_AMBIENT dev override.
func (g *Gateway) Offers(def *mcpcatalog.ConnectorDef) bool {
	return len(def.ModesFor(g.o.AmbientAllowed)) > 0
}

// offeredMode finds a mode among those this install may offer.
func (g *Gateway) offeredMode(def *mcpcatalog.ConnectorDef, id string) (mcpcatalog.AuthMode, bool) {
	for _, m := range def.ModesFor(g.o.AmbientAllowed) {
		if m.ID == id {
			return m, true
		}
	}
	return mcpcatalog.AuthMode{}, false
}

// Admin-facing sentences for a stored configuration that cannot be used.
const (
	msgModeNotOffered = "The selected sign-in method is not available on this installation. Choose another one and save again."
	msgNoModes        = "This connector is not available on this installation."
	msgNotConfigured  = "This connector has not been configured yet."
)

// problem is a reason a stored configuration cannot be bound, phrased for the
// admin.
type problem struct{ msg string }

func (p *problem) Error() string { return p.msg }

// readiness reports whether a stored configuration can be bound and in which
// mode. A nil problem means ready.
func (g *Gateway) readiness(def *mcpcatalog.ConnectorDef, cfg settings.ResolvedConnector) (mcpcatalog.AuthMode, *problem) {
	if cfg.Mode == "" {
		return mcpcatalog.AuthMode{}, &problem{msgNotConfigured}
	}
	mode, ok := g.offeredMode(def, cfg.Mode)
	if !ok {
		if !g.Offers(def) {
			return mode, &problem{msgNoModes}
		}
		return mode, &problem{msgModeNotOffered}
	}
	eff := effectiveOf(def, mode, cfg, nil, nil)
	if err := eff.validateWith(def, g.env, true); err != nil {
		return mode, &problem{err.Message}
	}
	return mode, nil
}

// DefaultKobiMinRole is the role floor of a connector whose admin never set
// one: cloud reads (log contents, CloudTrail, IAM policies) are org-wide and
// not team-scoped, so a viewer does not get them unless an admin says so.
const DefaultKobiMinRole = auth.RoleEditor

// kobiMinRole is a connector's effective role floor for Kobi. A stored value
// this build does not know reads as admin, the strictest (settings refuses
// one on write).
func kobiMinRole(cfg settings.ResolvedConnector) auth.Role {
	r := auth.Role(cfg.KobiMinRole)
	switch {
	case r == "":
		return DefaultKobiMinRole
	case auth.ValidRole(r):
		return r
	}
	return auth.RoleAdmin
}

// callerRole is the role of the person the request acts for. A request with
// no claims is admin, as everywhere else in KubeBolt: that is a request made
// with authentication disabled.
func callerRole(ctx context.Context) auth.Role {
	if c := claimsFrom(ctx); c != nil {
		return c.Role
	}
	return auth.RoleAdmin
}

// reachable reports whether consumer may use the connector for the caller in
// ctx. Only Kobi has a role floor: Autopilot comes in through a service door
// whose profiles already decide what it may read.
func reachable(ctx context.Context, consumer mcpcatalog.Consumer, cfg settings.ResolvedConnector) bool {
	if consumer != mcpcatalog.ConsumerKobi {
		return true
	}
	return auth.RoleLevel(callerRole(ctx)) >= auth.RoleLevel(kobiMinRole(cfg))
}

// orgOf returns the org of the request, refusing a context without one.
func orgOf(ctx context.Context) (string, error) {
	org := auth.TenantIDFromContext(ctx)
	if org == "" {
		return "", ErrNoOrg
	}
	return org, nil
}

// resolvedFor hands a binder only what the mode's schema names: a stale field
// left over from another mode never reaches a request.
func resolvedFor(org string, eff effective) mcpcatalog.Resolved {
	return mcpcatalog.Resolved{
		Org:     org,
		Mode:    eff.mode.ID,
		Fields:  eff.bindFields(),
		Secrets: eff.bindSecrets(),
	}
}

// fingerprint identifies a stored configuration. It includes the secrets, so
// it never leaves the process: it is a map key, never logged or returned.
// UpdatedAt is part of it, so any saved change — also one made on another
// replica, which this process sees once its settings cache expires (seconds)
// — builds a fresh binding. A verification write and a new role floor leave
// UpdatedAt alone and so keep the binding.
func fingerprint(cfg settings.ResolvedConnector) string {
	h := sha256.New()
	write := func(s string) {
		h.Write([]byte(strconv.Itoa(len(s))))
		h.Write([]byte{':'})
		h.Write([]byte(s))
	}
	write(cfg.Mode)
	for _, m := range []map[string]string{cfg.Fields, cfg.Secrets} {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		write(strconv.Itoa(len(keys)))
		for _, k := range keys {
			write(k)
			write(m[k])
		}
	}
	write(strings.Join(cfg.SecretsUnreadable, ","))
	write(cfg.UpdatedAt.UTC().Format(time.RFC3339Nano))
	return hex.EncodeToString(h.Sum(nil))
}

// acquire returns the org's binding for a connector, building it when there
// is none for the current configuration. The caller must release it. It waits
// at most BindTimeout; concurrent callers share one build.
func (g *Gateway) acquire(ctx context.Context, org string, t target) (*entry, error) {
	key := bindingKey{org, t.def.ID}
	fp := fingerprint(t.cfg)
	now := g.now()

	g.mu.Lock()
	if g.stopped {
		g.mu.Unlock()
		return nil, errStopped
	}
	g.sweepLocked(now)
	e := g.bindings[key]
	if e != nil && e.fp != fp {
		// The configuration changed since this binding was built.
		g.retireLocked(e)
		e = nil
	}
	if e != nil {
		g.maybeRebindLocked(e, t, now)
	}
	if e == nil {
		if f := g.failures[key]; f != nil && f.fp == fp && now.Sub(f.at) < bindRetryAfter {
			g.mu.Unlock()
			return nil, f.err
		}
		e = &entry{
			key:     key,
			fp:      fp,
			name:    t.def.Name,
			base:    context.WithoutCancel(ctx),
			ready:   make(chan struct{}),
			sem:     make(chan struct{}, g.o.PerBindingConcurrency),
			started: time.Now(),
		}
		g.bindings[key] = e
		g.open.Add(1)
		go g.bind(e, t)
	}
	e.refs++
	e.lastUsed = now
	g.mu.Unlock()

	select {
	case <-e.ready:
	default:
		// Wait for what is left of the build's BindTimeout, not a fresh one:
		// a binder that overruns its context (a token fetch that ignores
		// cancellation) would otherwise cost every request that arrives
		// while it is stuck the full timeout again.
		wait := g.o.BindTimeout - time.Since(e.started)
		if wait <= 0 {
			g.release(e)
			return nil, errBindTimeout
		}
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-e.ready:
		case <-ctx.Done():
			g.release(e)
			return nil, ctx.Err()
		case <-timer.C:
			g.release(e)
			return nil, errBindTimeout
		}
	}
	if e.err != nil {
		g.release(e)
		return nil, e.err
	}
	return e, nil
}

// bind builds e's binding. It runs detached from the request that asked for
// it, bounded by BindTimeout, so a waiter that gives up does not waste the
// work for the next one.
func (g *Gateway) bind(e *entry, t target) {
	ctx, cancel := context.WithTimeout(e.base, g.o.BindTimeout)
	b, err := safeBind(ctx, t.def.Binder, resolvedFor(e.key.org, effectiveOf(t.def, t.mode, t.cfg, nil, nil)), g.env)
	cancel()
	if err == nil && b == nil {
		err = errors.New("the connector returned no binding")
	}
	if err != nil {
		// A failed entry never holds a binding, so this is where it ends —
		// after a half-built binding, which may already hold a provider
		// session, is closed.
		if half := b; half != nil {
			go func() {
				defer g.open.Done()
				closeBinding(e.base, half, g.log)
			}()
		} else {
			defer g.open.Done()
		}
		b = nil
	}
	var missing []string
	if b != nil {
		missing = missingOf(b)
	}

	var (
		write    *string
		clearing string // when write clears, the error it clears
	)
	now := g.now()
	g.mu.Lock()
	prev := g.failures[e.key]
	if err != nil {
		e.err = err
		if g.bindings[e.key] == e {
			delete(g.bindings, e.key)
		}
		// A binding retired meanwhile belonged to a configuration that no
		// longer exists: recording its failure would put an old error on
		// the new configuration.
		if !e.retired {
			msg, _ := adminError(t.def, err)
			f := &bindFailure{fp: e.fp, err: err, msg: msg, at: now}
			// The throttle belongs to one configuration: a changed one (saved
			// on another replica) starts with its error cleared, and the same
			// sentence must be written for it again.
			if prev != nil && prev.fp == e.fp {
				f.written, f.writtenAt = prev.written, prev.writtenAt
			}
			switch {
			case t.cfg.LastError == msg:
				// Already on record (this process or a previous one wrote
				// it): nothing to write, but a later success may clear it.
				if f.written != msg {
					f.written, f.writtenAt = msg, now
				}
			case f.written != msg || now.Sub(f.writtenAt) >= lastErrorEvery:
				f.written, f.writtenAt = msg, now
				write = &f.msg
			}
			g.failures[e.key] = f
		}
	} else {
		e.b = b
		e.missing = len(missing)
		e.rebindWait = partialRebindAfter
		e.nextRebind = now.Add(e.rebindWait)
		if prev != nil && !e.retired {
			// The connector works again: clear the error this gateway
			// wrote, and only that one — a failed canary recorded by an
			// admin's test says something a successful bind does not.
			if prev.written != "" && t.cfg.LastError == prev.written {
				empty := ""
				write, clearing = &empty, prev.written
			}
			delete(g.failures, e.key)
		}
		if e.retired && e.refs == 0 {
			g.closeLocked(e)
		}
	}
	g.mu.Unlock()

	if err != nil {
		g.log.Warn("mcp gateway: connector bind failed",
			slog.String("org", e.key.org),
			slog.String("connector", e.key.id),
			slog.String("error", shortRedacted(err.Error(), 300)))
	}
	if len(missing) > 0 {
		g.log.Info("mcp gateway: connector bound with parts missing; it is rebuilt in the background",
			slog.String("org", e.key.org),
			slog.String("connector", e.key.id),
			slog.String("missing", shortRedacted(strings.Join(missing, ", "), 300)))
	}
	if write != nil {
		g.recordBindOutcome(e, *write, clearing)
	}
	close(e.ready)
}

// recordBindOutcome stores the lastError of a failed bind, or clears the one
// this gateway stored once the connector binds again. The bind worked from a
// snapshot taken when it started: a save that landed meanwhile (here or on
// another replica) must not inherit this configuration's error, and a test
// that failed meanwhile must not see its error wiped. The re-read below skips
// the write cheaply when this process already knows; the write itself is
// conditional on the stored configuration and error, which settings checks
// under its lock. The verification is kept as it is: a bind failure says the
// connector is not answering now, not that its last test did not pass.
func (g *Gateway) recordBindOutcome(e *entry, lastError, clearing string) {
	ctx, cancel := context.WithTimeout(e.base, writeTimeout)
	defer cancel()
	all, err := g.st.MCP(ctx)
	if err != nil {
		g.log.Warn("mcp gateway: could not read the connector before recording its error",
			slog.String("org", e.key.org), slog.String("connector", e.key.id), slog.String("error", err.Error()))
		return
	}
	cur, ok := all.Connectors[e.key.id]
	switch {
	case !ok || fingerprint(cur) != e.fp:
		return
	case lastError == "" && cur.LastError != clearing:
		return // someone else's error now (a failed test): not ours to clear
	case lastError != "" && cur.LastError == lastError:
		return
	}
	// The snapshot above may be up to the settings cache's age; settings
	// checks again against what is stored, under its write lock.
	v := settings.MCPVerification{ConfigAt: cur.UpdatedAt, LastError: lastError}
	if lastError == "" {
		v.IfLastError = &clearing
	}
	err = g.st.SetMCPConnectorVerification(ctx, e.key.id, v)
	if err != nil && !errors.Is(err, settings.ErrMCPConnectorNotFound) && !errors.Is(err, settings.ErrMCPConnectorChanged) {
		g.log.Warn("mcp gateway: could not record the connector error",
			slog.String("org", e.key.org), slog.String("connector", e.key.id), slog.String("error", err.Error()))
	}
}

// release drops a reference taken by acquire.
func (g *Gateway) release(e *entry) {
	g.mu.Lock()
	defer g.mu.Unlock()
	e.refs--
	if e.retired && e.refs == 0 {
		g.closeLocked(e)
	}
}

// retire removes e from the cache (unless a newer entry replaced it) and closes
// it once the last in-flight call is done.
func (g *Gateway) retire(e *entry) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.retireLocked(e)
}

func (g *Gateway) retireLocked(e *entry) {
	e.retired = true
	if g.bindings[e.key] == e {
		delete(g.bindings, e.key)
	}
	if e.refs == 0 {
		g.closeLocked(e)
	}
}

// closeLocked closes e's binding in the background; Shutdown waits for it. A
// binding still being built is closed by bind when it finishes.
func (g *Gateway) closeLocked(e *entry) {
	if e.closed || e.b == nil {
		return
	}
	e.closed = true
	b, base := e.b, e.base
	go func() {
		defer g.open.Done()
		closeBinding(base, b, g.log)
	}()
}

// maybeRebindLocked starts rebuilding a partial binding once it has served
// for its rebindWait, one attempt at a time and in the background: the
// request at hand keeps the binding it found.
func (g *Gateway) maybeRebindLocked(e *entry, t target, now time.Time) {
	if e.b == nil || e.missing == 0 || e.rebinding || e.retired || g.stopped || now.Before(e.nextRebind) {
		return
	}
	e.rebinding = true
	g.open.Add(1)
	go g.rebind(e, t)
}

// rebind builds a fresh binding for the partial entry old and swaps it in
// only when it is missing less; the new entry waits partialRebindAfter again.
// A failed or no better attempt — the same parts missing, or more — keeps
// old and doubles its wait, up to partialRebindMax.
func (g *Gateway) rebind(old *entry, t target) {
	ctx, cancel := context.WithTimeout(old.base, g.o.BindTimeout)
	b, err := safeBind(ctx, t.def.Binder, resolvedFor(old.key.org, effectiveOf(t.def, t.mode, t.cfg, nil, nil)), g.env)
	cancel()
	if err == nil && b == nil {
		err = errors.New("the connector returned no binding")
	}
	var missing []string
	if err == nil {
		missing = missingOf(b)
	}

	now := g.now()
	g.mu.Lock()
	old.rebinding = false
	swap := err == nil && len(missing) < old.missing && !old.retired && !g.stopped && g.bindings[old.key] == old
	if !swap {
		old.rebindWait = min(2*old.rebindWait, partialRebindMax)
	}
	old.nextRebind = now.Add(old.rebindWait)
	if swap {
		ready := make(chan struct{})
		close(ready)
		// The count this rebuild took in open moves to the new entry:
		// closing that entry is what ends it.
		g.bindings[old.key] = &entry{
			key:        old.key,
			fp:         old.fp,
			name:       old.name,
			base:       old.base,
			ready:      ready,
			sem:        make(chan struct{}, g.o.PerBindingConcurrency),
			started:    time.Now(),
			b:          b,
			lastUsed:   old.lastUsed,
			missing:    len(missing),
			rebindWait: partialRebindAfter,
			nextRebind: now.Add(partialRebindAfter),
		}
		g.retireLocked(old)
	}
	g.mu.Unlock()

	if swap {
		g.log.Info("mcp gateway: rebuilt a connector that had parts missing",
			slog.String("org", old.key.org), slog.String("connector", old.key.id),
			slog.Int("missingBefore", old.missing), slog.Int("missingNow", len(missing)))
		return
	}
	defer g.open.Done()
	if err != nil {
		g.log.Debug("mcp gateway: rebuilding a partial connector failed; keeping the one in use",
			slog.String("org", old.key.org), slog.String("connector", old.key.id),
			slog.String("error", shortRedacted(err.Error(), 300)))
	}
	if b != nil {
		closeBinding(old.base, b, g.log)
	}
}

// sweepLocked retires bindings nobody has used for idleTTL, at most once per
// sweepEvery, and forgets per-org state that no longer says anything: old
// failures, spent test paces and call budgets that are full again. In SaaS
// these maps have an entry per org that ever used a connector.
func (g *Gateway) sweepLocked(now time.Time) {
	if now.Sub(g.lastSweep) < sweepEvery {
		return
	}
	g.lastSweep = now
	for _, e := range g.bindings {
		if e.refs == 0 && e.b != nil && now.Sub(e.lastUsed) > idleTTL {
			g.retireLocked(e)
		}
	}
	for k, f := range g.failures {
		if now.Sub(f.at) > 2*lastErrorEvery {
			delete(g.failures, k)
		}
	}
	for org, at := range g.lastTest {
		if now.Sub(at) >= testEvery {
			delete(g.lastTest, org)
		}
	}
	for org, lim := range g.limiters {
		if lim.TokensAt(now) >= float64(lim.Burst()) {
			delete(g.limiters, org)
		}
	}
}

// failureFor returns the remembered bind failure for the org's current
// configuration of a connector, if any.
func (g *Gateway) failureFor(org, id string, cfg settings.ResolvedConnector) string {
	fp := fingerprint(cfg)
	g.mu.Lock()
	defer g.mu.Unlock()
	if f := g.failures[bindingKey{org, id}]; f != nil && f.fp == fp {
		return f.msg
	}
	return ""
}

// liveBinding returns the org's ready binding for the current configuration
// without building one; the caller must release it.
func (g *Gateway) liveBinding(org, id string, cfg settings.ResolvedConnector) *entry {
	fp := fingerprint(cfg)
	g.mu.Lock()
	defer g.mu.Unlock()
	e := g.bindings[bindingKey{org, id}]
	if e == nil || e.fp != fp || e.b == nil || e.retired {
		return nil
	}
	e.refs++
	return e
}

// allowCall spends one token of the org's call budget.
func (g *Gateway) allowCall(org string) bool {
	g.mu.Lock()
	lim := g.limiters[org]
	if lim == nil {
		// A quarter of the minute's budget may be spent at once: enough for
		// a burst of parallel reads, not for draining the minute in a
		// second against a provider that throttles per account.
		burst := g.o.OrgRatePerMin / 4
		if burst < 1 {
			burst = 1
		}
		lim = rate.NewLimiter(rate.Limit(float64(g.o.OrgRatePerMin)/60), burst)
		g.limiters[org] = lim
	}
	g.mu.Unlock()
	return lim.AllowN(g.now(), 1)
}

// allowTest paces connectivity tests per (org, connector): the point is not
// to hammer one provider, and an admin setting up AWS right after Azure is
// not doing that.
func (g *Gateway) allowTest(org, id string) (time.Duration, bool) {
	key := org + "\x00" + id
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	if last, ok := g.lastTest[key]; ok {
		if wait := testEvery - now.Sub(last); wait > 0 {
			return wait, false
		}
	}
	g.lastTest[key] = now
	return 0, true
}

// safeBind turns a panicking binder into an error: it runs on a goroutine of
// its own, where a panic would take the whole server down.
func safeBind(ctx context.Context, b mcpcatalog.Binder, r mcpcatalog.Resolved, env mcpcatalog.Env) (bd mcpcatalog.Binding, err error) {
	defer func() {
		if p := recover(); p != nil {
			bd, err = nil, fmt.Errorf("the connector failed while connecting (%T)", p)
		}
	}()
	return b.Bind(ctx, r, env)
}

// missingOf asks a binding what failed to come up; nil for one that cannot be
// partial, and for one that panics answering.
func missingOf(b mcpcatalog.Binding) (missing []string) {
	p, ok := b.(mcpcatalog.PartialBinding)
	if !ok {
		return nil
	}
	defer func() {
		if recover() != nil {
			missing = nil
		}
	}()
	return p.Missing()
}

// safeTools reads a binding's tool list, treating a panic as no tools.
func safeTools(b mcpcatalog.Binding, c mcpcatalog.Consumer) (specs []mcpcatalog.ToolSpec, ok bool) {
	defer func() {
		if p := recover(); p != nil {
			specs, ok = nil, false
		}
	}()
	return b.Tools(c), true
}

func closeBinding(base context.Context, b mcpcatalog.Binding, log *slog.Logger) {
	defer func() {
		if p := recover(); p != nil {
			log.Warn("mcp gateway: binding panicked on close", slog.String("panic", fmt.Sprintf("%T", p)))
		}
	}()
	ctx, cancel := context.WithTimeout(base, closeTimeout)
	defer cancel()
	if err := b.Close(ctx); err != nil {
		log.Debug("mcp gateway: closing a binding failed", slog.String("error", shortRedacted(err.Error(), 200)))
	}
}
