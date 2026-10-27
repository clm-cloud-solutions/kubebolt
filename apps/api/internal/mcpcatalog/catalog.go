package mcpcatalog

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Each provider file registers its definition from init(), so adding a
// connector is one file plus its tests and never an edit to a shared list.
var (
	registryMu sync.Mutex
	registry   []func() *ConnectorDef
	// preview holds connectors that are built and tested but not offered:
	// New leaves them out, so no org can see, configure or call them.
	preview []func() *ConnectorDef
)

func register(f func() *ConnectorDef) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry = append(registry, f)
}

// registerPreview is register for a connector that is not offered yet.
// Offering it is moving its init() to register.
func registerPreview(f func() *ConnectorDef) {
	registryMu.Lock()
	defer registryMu.Unlock()
	preview = append(preview, f)
}

// Preview returns the connectors registered for preview, sorted by id: the
// connectors page lists them as coming soon, with nothing to set up.
func Preview() []*ConnectorDef {
	registryMu.Lock()
	fns := append([]func() *ConnectorDef(nil), preview...)
	registryMu.Unlock()
	defs := make([]*ConnectorDef, 0, len(fns))
	for _, f := range fns {
		defs = append(defs, f())
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].ID < defs[j].ID })
	return defs
}

// IsPreview reports whether id names a connector registered for preview, so
// a setting that names it can be told from a typo.
func IsPreview(id string) bool {
	for _, d := range Preview() {
		if d.ID == id {
			return true
		}
	}
	return false
}

// Catalog is the immutable set of connectors this binary ships.
type Catalog struct {
	defs []*ConnectorDef
	byID map[string]*ConnectorDef
	// minPlanSet names the connectors whose MinPlan came from WithMinPlans
	// rather than from their definition.
	minPlanSet map[string]bool
}

// Where a connector's minimum plan comes from, as MinPlanSource reports it.
const (
	// MinPlanSourceDefault: the MinPlan written in the connector's definition.
	MinPlanSourceDefault = "default"
	// MinPlanSourceEnv: replaced through WithMinPlans, which exists for the
	// operator's override (KUBEBOLT_MCP_CONNECTOR_MIN_PLANS, read in
	// cmd/server).
	MinPlanSourceEnv = "env"
)

// New builds the catalog from every registered definition. It fails rather
// than serving a definition whose ids or tool names would break a consumer:
// a name over the provider limit is rejected by the model API on every
// request, which is far worse than refusing to boot.
func New() (*Catalog, error) {
	registryMu.Lock()
	fns := append([]func() *ConnectorDef(nil), registry...)
	registryMu.Unlock()
	return build(fns)
}

// NewWithPreview is New plus the connectors registered for preview. Tests
// use it, so a connector that is not offered keeps going through the same
// gateway, plan and tool-name checks as the ones that are.
func NewWithPreview() (*Catalog, error) {
	registryMu.Lock()
	fns := append(append([]func() *ConnectorDef(nil), registry...), preview...)
	registryMu.Unlock()
	return build(fns)
}

func build(fns []func() *ConnectorDef) (*Catalog, error) {
	defs := make([]*ConnectorDef, 0, len(fns))
	for _, f := range fns {
		defs = append(defs, f())
	}
	return newCatalog(defs)
}

// NewWith builds a catalog from explicit definitions. Tests use it to put a
// fake connector behind the real gateway.
func NewWith(defs ...*ConnectorDef) (*Catalog, error) {
	return newCatalog(defs)
}

func newCatalog(defs []*ConnectorDef) (*Catalog, error) {
	c := &Catalog{byID: make(map[string]*ConnectorDef, len(defs))}
	for _, d := range defs {
		if err := ValidateConnectorID(d.ID); err != nil {
			return nil, err
		}
		if _, dup := c.byID[d.ID]; dup {
			return nil, fmt.Errorf("mcpcatalog: duplicate connector id %q", d.ID)
		}
		if d.Binder == nil {
			return nil, fmt.Errorf("mcpcatalog: connector %q has no binder", d.ID)
		}
		c.byID[d.ID] = d
		c.defs = append(c.defs, d)
	}
	// Deterministic order: the marketplace and every tool list derived from
	// it must not reshuffle between requests.
	sort.Slice(c.defs, func(i, j int) bool { return c.defs[i].ID < c.defs[j].ID })
	return c, nil
}

// List returns the connectors sorted by id.
func (c *Catalog) List() []*ConnectorDef {
	out := make([]*ConnectorDef, len(c.defs))
	copy(out, c.defs)
	return out
}

// Get returns one connector.
func (c *Catalog) Get(id string) (*ConnectorDef, bool) {
	d, ok := c.byID[id]
	return d, ok
}

// WithMinPlans returns a new catalog in which each connector named in m
// carries that minimum plan instead of its definition's. The receiver is left
// as it is, and so are the definitions: an overridden connector is a copy
// whose MinPlan alone differs, so a catalog built earlier never changes its
// price under whoever holds it.
//
// An id the catalog does not have is an error, not a no-op: the override is a
// price gate, and one that silently gates nothing is the typo nobody sees.
// Plan names are NOT checked here — this package does not know the plan list
// (auth does, and this package imports only the standard library); the caller
// validates them.
func (c *Catalog) WithMinPlans(m map[string]string) (*Catalog, error) {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Strings(ids) // the same unknown id is reported on every boot
	for _, id := range ids {
		if _, ok := c.byID[id]; !ok {
			known := make([]string, 0, len(c.defs))
			for _, d := range c.defs {
				known = append(known, d.ID)
			}
			return nil, fmt.Errorf("mcpcatalog: unknown connector id %q (known: %s)", id, strings.Join(known, ", "))
		}
	}
	out := &Catalog{
		defs:       make([]*ConnectorDef, 0, len(c.defs)),
		byID:       make(map[string]*ConnectorDef, len(c.defs)),
		minPlanSet: make(map[string]bool, len(c.minPlanSet)+len(m)),
	}
	for id := range c.minPlanSet {
		out.minPlanSet[id] = true
	}
	for _, d := range c.defs {
		if plan, ok := m[d.ID]; ok {
			cp := *d
			cp.MinPlan = plan
			d = &cp
			out.minPlanSet[d.ID] = true
		}
		out.defs = append(out.defs, d)
		out.byID[d.ID] = d
	}
	return out, nil
}

// MinPlanSource says where a connector's minimum plan comes from:
// MinPlanSourceEnv when WithMinPlans replaced it, MinPlanSourceDefault
// otherwise (an unknown id included — it has no override to report).
func (c *Catalog) MinPlanSource(id string) string {
	if c.minPlanSet[id] {
		return MinPlanSourceEnv
	}
	return MinPlanSourceDefault
}

// Mode returns an auth mode of a connector.
func (d *ConnectorDef) Mode(id string) (AuthMode, bool) {
	for _, m := range d.AuthModes {
		if m.ID == id {
			return m, true
		}
	}
	return AuthMode{}, false
}

// ModesFor returns the auth modes this install may offer. Modes that borrow
// the machine's own identity are dropped unless ambient credentials are
// allowed (single-tenant, or the explicit dev override).
func (d *ConnectorDef) ModesFor(ambientAllowed bool) []AuthMode {
	out := make([]AuthMode, 0, len(d.AuthModes))
	for _, m := range d.AuthModes {
		if m.SelfHostedOnly && !ambientAllowed {
			continue
		}
		out = append(out, m)
	}
	return out
}
