package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
)

// mcpSettingsKey is the org-settings key holding every MCP connector an org has
// configured.
//
// ONE blob per org rather than one key per connector because OrgSettingStore
// can neither list nor delete keys: with a single blob, "which connectors does
// this org have" is one read and removing one is a rewrite. The price is that
// every write is a read-modify-write of the whole blob, which is why writes go
// through mcpWriteMu — within one process; what that leaves open across
// replicas is spelled out on saveMCP.
const mcpSettingsKey = "mcp:connectors"

// mcpCacheTTL bounds how long this process serves an org's connectors from
// memory. This process's own writes invalidate at once; a write made on
// another replica is only seen once the entry expires, so this is also the
// longest a connector disabled or deleted there keeps answering here. A read
// is one small row, so the bound can be short.
const mcpCacheTTL = 20 * time.Second

// Bounds on what an admin can store. They are generous (a GCP service-account
// key is ~2.3 KB, an ARN at most 2 KB) and exist so a bad client cannot grow
// the one blob every connector shares without limit. Field-level rules — ARN
// shape, project id, region — belong to the catalog's FieldSchema and are
// checked by the API before a patch gets here.
const (
	maxMCPFieldBytes  = 4 << 10
	maxMCPSecretBytes = 32 << 10
	maxMCPEntries     = 32
)

// mcpIDPattern bounds connector and auth-mode ids; mcpNamePattern bounds field
// and secret names (camelCase, as the catalog's FieldSchema names them).
// Connector ids become part of model-facing tool names (mcp__<id>__<tool>), so
// their charset stays inside the tool-name charset, and "__" is rejected
// separately because it is the separator the gateway splits on.
var (
	mcpIDPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	mcpNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
)

// ErrMCPConnectorNotFound is returned when a write targets a connector the org
// has not configured and the write must not create it (a verification result
// arriving after the connector was removed).
var ErrMCPConnectorNotFound = errors.New("settings: mcp connector not configured")

// ErrMCPConnectorChanged is returned when a verification describes a
// configuration that is no longer the stored one: it was saved again while
// the test or the bind ran, here or on another replica.
var ErrMCPConnectorChanged = errors.New("settings: mcp connector changed since it was tested")

// StoredMCPSettings is the on-disk shape under mcpSettingsKey.
type StoredMCPSettings struct {
	Connectors map[string]StoredConnector `json:"connectors,omitempty"`
}

// StoredConnector is one connector's configuration as persisted. Secrets are
// only ever held here as "v1:" envelopes; the plaintext exists in memory, in
// ResolvedConnector, and nowhere else.
type StoredConnector struct {
	Enabled bool   `json:"enabled"`
	Paused  bool   `json:"paused"`
	Mode    string `json:"mode,omitempty"`
	// Fields are the non-secret settings (region, projectId, subscriptionId…).
	Fields map[string]string `json:"fields,omitempty"`
	// SecretsEncoded maps a secret field name to its encrypted envelope.
	SecretsEncoded map[string]string `json:"secretsEncoded,omitempty"`
	// KobiMinRole is the lowest role whose Kobi conversations may use the
	// connector ("" is the gateway's default). It says who may use it, not
	// how it connects, so changing it is not a configuration change.
	KobiMinRole string `json:"kobiMinRole,omitempty"`
	// VerifiedAt / VerifiedTools / VerifiedIdentity / VerifiedWarning /
	// LastError are the outcome of the last test of THIS configuration. A
	// change to the mode, a field or a secret clears them: a "Verified" badge
	// earned by the previous credentials would be a claim about a
	// configuration that no longer exists.
	VerifiedAt       *time.Time `json:"verifiedAt,omitempty"`
	VerifiedTools    int        `json:"verifiedTools,omitempty"`
	VerifiedIdentity string     `json:"verifiedIdentity,omitempty"`
	VerifiedWarning  string     `json:"verifiedWarning,omitempty"`
	LastError        string     `json:"lastError,omitempty"`
	UpdatedBy        string     `json:"updatedBy,omitempty"`
	UpdatedAt        time.Time  `json:"updatedAt"`
}

// ResolvedMCP is an org's connector configuration with secrets decrypted, for
// the gateway. It holds credentials: never log it, return it from an API or
// hand it to a model.
type ResolvedMCP struct {
	Connectors map[string]ResolvedConnector
}

// ResolvedConnector is one connector with its secrets decrypted in memory.
type ResolvedConnector struct {
	Enabled bool
	Paused  bool
	Mode    string
	Fields  map[string]string
	// Secrets holds the readable secrets in plaintext. The json tag keeps a
	// careless encode of this struct from putting them on the wire.
	Secrets map[string]string `json:"-"`
	// SecretsUnreadable lists stored secrets that no longer decrypt — the JWT
	// secret the key derives from was rotated. They are absent from Secrets,
	// not fatal: the connector reports an error and the admin re-enters them.
	SecretsUnreadable []string
	KobiMinRole       string
	VerifiedAt        *time.Time
	VerifiedTools     int
	VerifiedIdentity  string
	VerifiedWarning   string
	LastError         string
	UpdatedBy         string
	UpdatedAt         time.Time
}

// String renders the connector with its secrets reduced to their names. With
// Format it covers what the json tag cannot: a stray %v in a log line or an
// error, slog's text handler, a test failure message — every fmt verb, also
// when the connector is nested inside ResolvedMCP.
func (c ResolvedConnector) String() string {
	names := make([]string, 0, len(c.Secrets))
	for name := range c.Secrets {
		names = append(names, name)
	}
	sort.Strings(names)
	verified := "never"
	if c.VerifiedAt != nil {
		verified = c.VerifiedAt.UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf("{Enabled:%t Paused:%t Mode:%s Fields:%v Secrets:%v(redacted) SecretsUnreadable:%v KobiMinRole:%s VerifiedAt:%s VerifiedTools:%d VerifiedIdentity:%q VerifiedWarning:%q LastError:%q UpdatedBy:%s UpdatedAt:%s}",
		c.Enabled, c.Paused, c.Mode, c.Fields, names, c.SecretsUnreadable, c.KobiMinRole, verified, c.VerifiedTools, c.VerifiedIdentity, c.VerifiedWarning, c.LastError, c.UpdatedBy, c.UpdatedAt.UTC().Format(time.RFC3339))
}

// Format implements fmt.Formatter so %#v, %d and friends go through String
// too instead of walking the struct and printing Secrets.
func (c ResolvedConnector) Format(f fmt.State, _ rune) {
	fmt.Fprint(f, c.String())
}

// MCPConnectorPatch is a partial update of one connector. A nil pointer keeps
// the stored value.
//
// Fields and Secrets are keyed by field name with keep/clear/set semantics: a
// missing key or a nil value keeps, "" removes, anything else sets. Secrets
// arrive in plaintext and are encrypted before the stored blob is touched.
type MCPConnectorPatch struct {
	Enabled *bool
	Paused  *bool
	Mode    *string
	Fields  map[string]*string
	Secrets map[string]*string
	// KobiMinRole sets the connector's role floor for Kobi; "" restores the
	// default. A patch that changes only this leaves UpdatedAt alone.
	KobiMinRole *string
	// UpdatedBy names who made the change (user id or username); stamped on
	// the connector together with the time.
	UpdatedBy string
}

// MaskedMCPConnector is the admin-facing view of one connector. It says WHICH
// secrets are set and never anything derived from their values — not even a
// masked preview: an external id or a credential JSON has no prefix worth
// recognising and every revealed character is one less to guess.
type MaskedMCPConnector struct {
	Enabled bool              `json:"enabled"`
	Paused  bool              `json:"paused"`
	Mode    string            `json:"mode"`
	Fields  map[string]string `json:"fields"`
	// SecretsConfigured is true for every stored secret that decrypts. A
	// stored secret that does not is reported in SecretsUnreadable instead, so
	// the form asks for it again rather than offering to keep it.
	SecretsConfigured map[string]bool `json:"secretsConfigured"`
	SecretsUnreadable []string        `json:"secretsUnreadable"`
	KobiMinRole       string          `json:"kobiMinRole,omitempty"`
	VerifiedAt        *time.Time      `json:"verifiedAt,omitempty"`
	VerifiedTools     int             `json:"verifiedTools,omitempty"`
	VerifiedIdentity  string          `json:"verifiedIdentity,omitempty"`
	VerifiedWarning   string          `json:"verifiedWarning,omitempty"`
	LastError         string          `json:"lastError,omitempty"`
	UpdatedAt         time.Time       `json:"updatedAt"`
}

// mcpCache holds each org's resolved connectors for up to mcpCacheTTL. It
// has its own lock, so a reload never holds up the other settings domains
// while it reads the store, and a generation per org, so a reload that raced
// a write never stores what it read before that write.
type mcpCache struct {
	mu      sync.RWMutex
	entries map[string]mcpCacheEntry
	gen     map[string]uint64
	// now is the clock (tests); nil means time.Now.
	now func() time.Time
}

type mcpCacheEntry struct {
	resolved ResolvedMCP
	loadedAt time.Time
}

func (c *mcpCache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// MCP returns the org's connectors with secrets decrypted. The org is the one
// in ctx — always pass the request ctx: a background ctx resolves to the
// default tenant and would hand one org's credentials to another's call.
//
// Cached per org for mcpCacheTTL, or until InvalidateMCP. Every call returns a
// deep copy, so a caller that merges a draft over the stored config (the
// connectivity test) cannot poison the cache. The cache is per process, which
// is why it expires: in a multi-replica install another replica's write —
// disabling a connector, deleting it — is otherwise never seen here.
func (r *Runtime) MCP(ctx context.Context) (ResolvedMCP, error) {
	org := orgKey(ctx)
	c := &r.mcp
	// Taken before the read, so the entry expires early rather than late.
	now := c.clock()
	c.mu.RLock()
	if e, ok := c.entries[org]; ok && now.Sub(e.loadedAt) < mcpCacheTTL {
		out := e.resolved.clone()
		c.mu.RUnlock()
		return out, nil
	}
	gen := c.gen[org]
	c.mu.RUnlock()

	stored, err := r.loadMCP(ctx)
	if err != nil {
		// Not cached: a transient store error must not stick.
		return ResolvedMCP{Connectors: map[string]ResolvedConnector{}}, err
	}
	resolved := r.resolveMCP(stored)
	c.mu.Lock()
	if c.gen[org] == gen {
		if c.entries == nil {
			c.entries = map[string]mcpCacheEntry{}
		}
		c.entries[org] = mcpCacheEntry{resolved: resolved, loadedAt: now}
	}
	c.mu.Unlock()
	return resolved.clone(), nil
}

// InvalidateMCP marks the org's cached connectors stale. Writes call it
// themselves; the gateway must still drop its own sessions for the org.
func (r *Runtime) InvalidateMCP(ctx context.Context) {
	org := orgKey(ctx)
	c := &r.mcp
	c.mu.Lock()
	delete(c.entries, org)
	if c.gen == nil {
		c.gen = map[string]uint64{}
	}
	c.gen[org]++
	c.mu.Unlock()
}

// PutMCPConnector applies a patch to one connector, creating it when the org
// has none. Returns *ValidationError for a malformed id, name or value.
func (r *Runtime) PutMCPConnector(ctx context.Context, id string, p MCPConnectorPatch) error {
	if err := validateMCPConnectorID(id); err != nil {
		return err
	}
	if err := validateMCPPatch(p); err != nil {
		return err
	}

	// Encrypt before the blob is read so a failure cannot leave a half-applied
	// patch, and never turn a failed encryption into "" — that is the clear
	// sentinel and would silently delete the admin's credential.
	encrypted := make(map[string]string, len(p.Secrets))
	for name, v := range p.Secrets {
		if v == nil {
			continue
		}
		enc, err := r.crypto.encrypt(*v)
		if err != nil {
			return fmt.Errorf("encrypt secret %q: %w", name, err)
		}
		encrypted[name] = enc
	}

	r.mcpWriteMu.Lock()
	defer r.mcpWriteMu.Unlock()
	stored, err := r.loadMCP(ctx)
	if err != nil {
		// Refuse rather than write: the blob holds every connector, and
		// writing over an unread one would drop the others.
		return err
	}
	cur, exists := stored.Connectors[id]
	if !exists && len(stored.Connectors) >= maxMCPEntries {
		return &ValidationError{Field: "id", Message: fmt.Sprintf("at most %d connectors per organization", maxMCPEntries)}
	}
	if applyMCPPatch(&cur, p, encrypted) {
		clearVerification(&cur)
	}
	// The patch bounds are per request; these hold across requests.
	if len(cur.Fields) > maxMCPEntries {
		return &ValidationError{Field: "fields", Message: fmt.Sprintf("at most %d fields", maxMCPEntries)}
	}
	if len(cur.SecretsEncoded) > maxMCPEntries {
		return &ValidationError{Field: "secrets", Message: fmt.Sprintf("at most %d secrets", maxMCPEntries)}
	}
	// UpdatedAt versions what the bindings and the verification describe
	// (the gateway keys bindings on it, a test is recorded only against it);
	// a new role floor changes neither.
	if !p.onlyKobiMinRole() {
		cur.UpdatedBy = p.UpdatedBy
		cur.UpdatedAt = time.Now().UTC()
	}
	if stored.Connectors == nil {
		stored.Connectors = map[string]StoredConnector{}
	}
	stored.Connectors[id] = cur
	return r.saveMCP(ctx, stored)
}

// MCPVerification is what testing — or binding — one stored configuration
// found.
type MCPVerification struct {
	// ConfigAt is the UpdatedAt of the configuration the outcome describes.
	// The write is refused with ErrMCPConnectorChanged when the stored
	// connector was saved since: the caller worked from a snapshot, possibly
	// one this process cached before another replica's save.
	ConfigAt time.Time
	// Verified replaces the verification (a passing test). Nil keeps the
	// stored one, so a failure never rolls back a verification recorded
	// meanwhile.
	Verified  *MCPVerified
	LastError string
	// IfLastError, when set, writes only while the stored lastError is still
	// this one: a connector that binds again clears the error its failed
	// bind wrote, never one a test recorded meanwhile.
	IfLastError *string
}

// MCPVerified is a passing test of the stored configuration.
type MCPVerified struct {
	At    time.Time
	Tools int
	// Identity is who the provider says KubeBolt signed in as. Warning is
	// what the test found that still needs attention (the AWS account's
	// root user); "" clears the previous one.
	Identity string
	Warning  string
}

// SetMCPConnectorVerification records the outcome of testing or binding the
// STORED configuration. The connector's UpdatedAt is left alone — a test is
// not a configuration change. Returns ErrMCPConnectorNotFound when the
// connector was removed meanwhile, so a late result cannot resurrect it, and
// ErrMCPConnectorChanged when it was saved again or its lastError moved on.
func (r *Runtime) SetMCPConnectorVerification(ctx context.Context, id string, v MCPVerification) error {
	if err := validateMCPConnectorID(id); err != nil {
		return err
	}
	r.mcpWriteMu.Lock()
	defer r.mcpWriteMu.Unlock()
	stored, err := r.loadMCP(ctx)
	if err != nil {
		return err
	}
	cur, ok := stored.Connectors[id]
	if !ok {
		return ErrMCPConnectorNotFound
	}
	if !cur.UpdatedAt.Equal(v.ConfigAt) || (v.IfLastError != nil && cur.LastError != *v.IfLastError) {
		return ErrMCPConnectorChanged
	}
	if v.Verified != nil {
		at := v.Verified.At.UTC()
		cur.VerifiedAt = &at
		cur.VerifiedTools = v.Verified.Tools
		cur.VerifiedIdentity = v.Verified.Identity
		cur.VerifiedWarning = v.Verified.Warning
	}
	cur.LastError = v.LastError
	stored.Connectors[id] = cur
	return r.saveMCP(ctx, stored)
}

// EnsureMCPConnectorFields stores the given non-secret values where the
// connector has none yet, creating an otherwise empty record when the org
// never configured it. A value already stored is never replaced: these are
// values KubeBolt generates once per org (the AWS external ID) and an admin
// may already have copied into a trust policy. It reports whether it wrote.
//
// It is not a configuration change: UpdatedAt and the verification stay as
// they are. The connector it fills in was either never configured, or was
// configured before the value existed — then its next bind fails and says so.
//
// A caller asks for a value because its snapshot has none. Finding one stored
// means that snapshot was stale — another replica generated it since this
// process cached the org — so the cache is dropped even though nothing is
// written: the caller's next MCP read returns the stored value, instead of
// showing no value and refusing a save that lacks one until the entry
// expires.
func (r *Runtime) EnsureMCPConnectorFields(ctx context.Context, id string, values map[string]string) (bool, error) {
	if err := validateMCPConnectorID(id); err != nil {
		return false, err
	}
	fields := make(map[string]*string, len(values))
	for name, v := range values {
		fields[name] = &v
	}
	if err := validateMCPPatch(MCPConnectorPatch{Fields: fields}); err != nil {
		return false, err
	}
	r.mcpWriteMu.Lock()
	defer r.mcpWriteMu.Unlock()
	stored, err := r.loadMCP(ctx)
	if err != nil {
		return false, err
	}
	cur, exists := stored.Connectors[id]
	if !exists && len(stored.Connectors) >= maxMCPEntries {
		return false, &ValidationError{Field: "id", Message: fmt.Sprintf("at most %d connectors per organization", maxMCPEntries)}
	}
	wrote, stale := false, false
	for name, v := range values {
		if strings.TrimSpace(cur.Fields[name]) != "" {
			stale = true
			continue
		}
		if strings.TrimSpace(v) == "" {
			continue
		}
		if cur.Fields == nil {
			cur.Fields = map[string]string{}
		}
		cur.Fields[name] = v
		wrote = true
	}
	if !wrote {
		if stale {
			r.InvalidateMCP(ctx)
		}
		return false, nil
	}
	if len(cur.Fields) > maxMCPEntries {
		return false, &ValidationError{Field: "fields", Message: fmt.Sprintf("at most %d fields", maxMCPEntries)}
	}
	if stored.Connectors == nil {
		stored.Connectors = map[string]StoredConnector{}
	}
	stored.Connectors[id] = cur
	return true, r.saveMCP(ctx, stored)
}

// DeleteMCPConnector removes a connector with its fields and secrets. Removing
// one that does not exist is not an error (DELETE is idempotent).
//
// keepFields names non-secret values that belong to the org rather than to
// the configuration — the ones KubeBolt generated, like the AWS external ID a
// customer's trust policy already names. When the connector holds any of
// them, its record is reduced to those values alone, as if they had just been
// generated: no mode, flags, secrets, role floor or verification, so it reads
// as never configured, and setting it up again finds the same values.
func (r *Runtime) DeleteMCPConnector(ctx context.Context, id string, keepFields ...string) error {
	if err := validateMCPConnectorID(id); err != nil {
		return err
	}
	r.mcpWriteMu.Lock()
	defer r.mcpWriteMu.Unlock()
	stored, err := r.loadMCP(ctx)
	if err != nil {
		return err
	}
	cur, ok := stored.Connectors[id]
	if !ok {
		return nil
	}
	kept := map[string]string{}
	for _, name := range keepFields {
		if v := cur.Fields[name]; strings.TrimSpace(v) != "" {
			kept[name] = v
		}
	}
	if len(kept) == 0 {
		delete(stored.Connectors, id)
	} else {
		stored.Connectors[id] = StoredConnector{Fields: kept}
	}
	return r.saveMCP(ctx, stored)
}

// MaskedMCPConnector returns the admin view of one connector and whether the
// org has configured it at all. An unconfigured connector comes back as a zero
// view with empty (non-nil) maps, so the caller can render it unchanged.
func (r *Runtime) MaskedMCPConnector(ctx context.Context, id string) (MaskedMCPConnector, bool, error) {
	empty := MaskedMCPConnector{
		Fields:            map[string]string{},
		SecretsConfigured: map[string]bool{},
		SecretsUnreadable: []string{},
	}
	all, err := r.MCP(ctx)
	if err != nil {
		return empty, false, err
	}
	rc, ok := all.Connectors[id]
	if !ok {
		return empty, false, nil
	}
	m := MaskedMCPConnector{
		Enabled:           rc.Enabled,
		Paused:            rc.Paused,
		Mode:              rc.Mode,
		Fields:            rc.Fields, // already a copy
		SecretsConfigured: make(map[string]bool, len(rc.Secrets)),
		SecretsUnreadable: append([]string{}, rc.SecretsUnreadable...),
		KobiMinRole:       rc.KobiMinRole,
		VerifiedAt:        rc.VerifiedAt,
		VerifiedTools:     rc.VerifiedTools,
		VerifiedIdentity:  rc.VerifiedIdentity,
		VerifiedWarning:   rc.VerifiedWarning,
		LastError:         rc.LastError,
		UpdatedAt:         rc.UpdatedAt,
	}
	for name := range rc.Secrets {
		m.SecretsConfigured[name] = true
	}
	return m, true, nil
}

// loadMCP reads the org's stored blob. A missing record is an empty config; any
// other store error is returned, because the write path must tell "nothing
// configured yet" from "could not read what is configured".
func (r *Runtime) loadMCP(ctx context.Context) (StoredMCPSettings, error) {
	raw, err := r.orgStore.GetOrgSetting(ctx, mcpSettingsKey)
	if err != nil {
		if isOrgSettingMiss(err, mcpSettingsKey) {
			return StoredMCPSettings{}, nil
		}
		return StoredMCPSettings{}, fmt.Errorf("read mcp connectors: %w", err)
	}
	var out StoredMCPSettings
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			return StoredMCPSettings{}, fmt.Errorf("decode mcp connectors: %w", err)
		}
	}
	return out, nil
}

// saveMCP persists the blob and invalidates the org's cache — also when the
// write fails, since a failed commit may still have landed.
//
// This is where the cross-replica limit of the single blob lives. Every
// caller re-reads the blob under mcpWriteMu right before calling it, so within
// one process no write is lost. mcpWriteMu is per process, though, and
// OrgSettingStore has no compare-and-swap (Bolt and Postgres both overwrite
// the row): a write another replica makes between this replica's read and
// this write is overwritten with what this replica read. The window is one
// read-modify-write, and no kind of write is safe inside it:
//   - an admin's disable, pause or delete on one replica can be undone by a
//     verification or a bind error another replica records at that instant.
//     ConfigAt and IfLastError are checked against this replica's own read,
//     so they decide whether it writes, not what its write overwrites;
//   - two replicas generating the org's value at once (the first views of
//     the AWS connector, EnsureMCPConnectorFields) each store their own and
//     the last write stays. The other replica shows its own value until its
//     cache expires (mcpCacheTTL); a trust policy copied from it fails the
//     connector's test until it names the stored one.
//
// Closing the window needs a conditional write in the store — a version on the
// org's setting, written only WHERE it is still the version read, retried on
// conflict — which OrgSettingStore does not offer yet.
func (r *Runtime) saveMCP(ctx context.Context, s StoredMCPSettings) error {
	defer r.InvalidateMCP(ctx)
	for id, c := range s.Connectors {
		if len(c.Fields) == 0 {
			c.Fields = nil
		}
		if len(c.SecretsEncoded) == 0 {
			c.SecretsEncoded = nil
		}
		s.Connectors[id] = c
	}
	encoded, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("marshal mcp connectors: %w", err)
	}
	if err := r.orgStore.SetOrgSetting(ctx, mcpSettingsKey, encoded); err != nil {
		return fmt.Errorf("persist mcp connectors: %w", err)
	}
	return nil
}

// isOrgSettingMiss reports whether a GetOrgSetting error for key means "no
// record". Neither store exports a sentinel; the Bolt and the Postgres
// implementations both return `setting "<key>" not found` on a miss. The key
// is part of the match so an unrelated "… not found" (a role, a relation, a
// tenant) is never read as "nothing configured" — the write path would then
// overwrite every connector the org has.
func isOrgSettingMiss(err error, key string) bool {
	return err != nil && strings.HasSuffix(err.Error(), fmt.Sprintf("setting %q not found", key))
}

// resolveMCP decrypts a stored blob. A secret that fails to decrypt is listed
// as unreadable, never dropped silently and never fatal for the others.
func (r *Runtime) resolveMCP(s StoredMCPSettings) ResolvedMCP {
	out := ResolvedMCP{Connectors: make(map[string]ResolvedConnector, len(s.Connectors))}
	for id, sc := range s.Connectors {
		rc := ResolvedConnector{
			Enabled:           sc.Enabled,
			Paused:            sc.Paused,
			Mode:              sc.Mode,
			Fields:            copyStringMap(sc.Fields),
			Secrets:           make(map[string]string, len(sc.SecretsEncoded)),
			SecretsUnreadable: []string{},
			KobiMinRole:       sc.KobiMinRole,
			VerifiedAt:        copyTime(sc.VerifiedAt),
			VerifiedTools:     sc.VerifiedTools,
			VerifiedIdentity:  sc.VerifiedIdentity,
			VerifiedWarning:   sc.VerifiedWarning,
			LastError:         sc.LastError,
			UpdatedBy:         sc.UpdatedBy,
			UpdatedAt:         sc.UpdatedAt,
		}
		for name, enc := range sc.SecretsEncoded {
			if enc == "" {
				continue
			}
			pt, err := r.crypto.decrypt(enc)
			if err != nil {
				rc.SecretsUnreadable = append(rc.SecretsUnreadable, name)
				continue
			}
			rc.Secrets[name] = pt
		}
		sort.Strings(rc.SecretsUnreadable)
		out.Connectors[id] = rc
	}
	return out
}

// applyMCPPatch merges p onto c and reports whether the configuration itself —
// mode, a field or a secret — changed. Enabled, Paused and the role floor are
// not configuration: pausing a verified connector keeps it verified.
func applyMCPPatch(c *StoredConnector, p MCPConnectorPatch, encrypted map[string]string) (configChanged bool) {
	if p.Enabled != nil {
		c.Enabled = *p.Enabled
	}
	if p.Paused != nil {
		c.Paused = *p.Paused
	}
	if p.KobiMinRole != nil {
		c.KobiMinRole = *p.KobiMinRole
	}
	if p.Mode != nil && *p.Mode != c.Mode {
		c.Mode = *p.Mode
		configChanged = true
	}
	for name, v := range p.Fields {
		if v == nil {
			continue
		}
		old, had := c.Fields[name]
		if *v == "" {
			if had {
				delete(c.Fields, name)
				configChanged = true
			}
			continue
		}
		if !had || old != *v {
			if c.Fields == nil {
				c.Fields = map[string]string{}
			}
			c.Fields[name] = *v
			configChanged = true
		}
	}
	for name, enc := range encrypted {
		if enc == "" {
			if _, had := c.SecretsEncoded[name]; had {
				delete(c.SecretsEncoded, name)
				configChanged = true
			}
			continue
		}
		if c.SecretsEncoded == nil {
			c.SecretsEncoded = map[string]string{}
		}
		c.SecretsEncoded[name] = enc
		configChanged = true
	}
	return configChanged
}

// clearVerification drops everything a test said about the previous
// configuration.
func clearVerification(c *StoredConnector) {
	c.VerifiedAt = nil
	c.VerifiedTools = 0
	c.VerifiedIdentity = ""
	c.VerifiedWarning = ""
	c.LastError = ""
}

// onlyKobiMinRole reports whether the patch changes nothing but the role
// floor.
func (p MCPConnectorPatch) onlyKobiMinRole() bool {
	if p.KobiMinRole == nil || p.Enabled != nil || p.Paused != nil || p.Mode != nil {
		return false
	}
	for _, m := range []map[string]*string{p.Fields, p.Secrets} {
		for _, v := range m {
			if v != nil {
				return false
			}
		}
	}
	return true
}

func validateMCPConnectorID(id string) error {
	if !mcpIDPattern.MatchString(id) || strings.Contains(id, "__") {
		return &ValidationError{Field: "id", Message: "connector id must be 1-64 lowercase letters, digits, '-' or '_' and must not contain '__'"}
	}
	return nil
}

func validateMCPPatch(p MCPConnectorPatch) error {
	if p.Mode != nil && *p.Mode != "" && (!mcpIDPattern.MatchString(*p.Mode) || strings.Contains(*p.Mode, "__")) {
		return &ValidationError{Field: "mode", Message: "auth mode must be 1-64 lowercase letters, digits, '-' or '_'"}
	}
	if p.KobiMinRole != nil && *p.KobiMinRole != "" && !auth.ValidRole(auth.Role(*p.KobiMinRole)) {
		return &ValidationError{Field: "kobiMinRole", Message: "must be viewer, editor or admin"}
	}
	if len(p.Fields) > maxMCPEntries {
		return &ValidationError{Field: "fields", Message: fmt.Sprintf("at most %d fields", maxMCPEntries)}
	}
	if len(p.Secrets) > maxMCPEntries {
		return &ValidationError{Field: "secrets", Message: fmt.Sprintf("at most %d secrets", maxMCPEntries)}
	}
	for name, v := range p.Fields {
		if !mcpNamePattern.MatchString(name) {
			return &ValidationError{Field: "fields", Message: fmt.Sprintf("invalid field name %q", name)}
		}
		if v != nil && len(*v) > maxMCPFieldBytes {
			return &ValidationError{Field: "fields." + name, Message: fmt.Sprintf("value longer than %d bytes", maxMCPFieldBytes)}
		}
	}
	for name, v := range p.Secrets {
		// The name is echoed back in errors; the value never is.
		if !mcpNamePattern.MatchString(name) {
			return &ValidationError{Field: "secrets", Message: fmt.Sprintf("invalid secret name %q", name)}
		}
		if v != nil && len(*v) > maxMCPSecretBytes {
			return &ValidationError{Field: "secrets." + name, Message: fmt.Sprintf("value longer than %d bytes", maxMCPSecretBytes)}
		}
	}
	return nil
}

// clone deep-copies the resolved config so callers own what they get.
func (m ResolvedMCP) clone() ResolvedMCP {
	out := ResolvedMCP{Connectors: make(map[string]ResolvedConnector, len(m.Connectors))}
	for id, c := range m.Connectors {
		c.Fields = copyStringMap(c.Fields)
		c.Secrets = copyStringMap(c.Secrets)
		c.SecretsUnreadable = append([]string{}, c.SecretsUnreadable...)
		c.VerifiedAt = copyTime(c.VerifiedAt)
		out.Connectors[id] = c
	}
	return out
}

// copyStringMap returns a non-nil copy, so callers can write into the result.
func copyStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func copyTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	c := *t
	return &c
}
