package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
)

// CardinalityTracker monitors the number of active series each tenant
// owns in VictoriaMetrics and enforces the per-tenant
// MaxActiveSeries cap on the Prom remote_write receiver.
//
// Data model: in-memory `tenantID → count` map, refreshed
// periodically by a background goroutine that issues a single
// PromQL count-by query against VM. Lazy tenant discovery: as soon
// as the receiver sees a request from a tenant we haven't tracked
// before, we add the ID to the refresh roster — no admin pre-config
// required.
//
// Why one global query (count by tenant_id) instead of N per-tenant
// queries: VM evaluates the aggregation in one pass over its index;
// the alternative would loop over the receiver's known tenants and
// fire N HTTP round-trips. For SaaS with hundreds of tenants the
// global query is O(1) backend cost.
//
// Why in-memory state (not BoltDB): the count is derived state — VM
// is the authority. A backend restart drops the cache but the next
// refresh tick re-populates from VM. Persistence would add cost on
// the hot path without buying anything (we can't reject requests
// based on stale post-restart counts anyway, before the first refresh
// the cache is empty / "permissive boot" applies).
//
// Concurrency: sync.RWMutex on the count map; hot path reads under
// RLock, refresh writes under Lock.
type CardinalityTracker struct {
	vmURL    string
	defaults auth.EffectiveLimits
	client   *http.Client
	interval time.Duration

	mu       sync.RWMutex
	counts   map[string]int     // tenant_id → current series count
	known    map[string]struct{} // set of tenants observed via SeenTenant
	hasFresh bool                // true after first successful refresh
}

// NewCardinalityTracker constructs the tracker. vmURL is the base
// VictoriaMetrics endpoint (same URL the receiver forwards to);
// the tracker appends /api/v1/query for the count probe.
//
// interval is the refresh cadence. 30s is the recommended default —
// matches the agent's scrape_interval, keeping cache staleness within
// one scrape window. Shorter intervals add VM load without buying
// much precision; longer intervals let runaway tenants stretch past
// the cap longer.
func NewCardinalityTracker(vmURL string, defaults auth.EffectiveLimits, client *http.Client, interval time.Duration) *CardinalityTracker {
	if client == nil {
		client = http.DefaultClient
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &CardinalityTracker{
		vmURL:    vmURL,
		defaults: defaults,
		client:   client,
		interval: interval,
		counts:   make(map[string]int),
		known:    make(map[string]struct{}),
	}
}

// SeenTenant records that a tenant has shipped traffic through the
// receiver. The next refresh tick will fetch its current count. Safe
// to call repeatedly with the same ID; cheap on the steady-state
// hot path (single map lookup + write under RLock-promoted-to-Lock).
func (c *CardinalityTracker) SeenTenant(tenantID string) {
	if tenantID == "" {
		return
	}
	c.mu.RLock()
	_, exists := c.known[tenantID]
	c.mu.RUnlock()
	if exists {
		return
	}
	c.mu.Lock()
	c.known[tenantID] = struct{}{}
	c.mu.Unlock()
}

// Forget removes a tenant from the tracker. Use when a tenant is
// deleted via the admin API to avoid the map growing unbounded over
// process lifetime.
func (c *CardinalityTracker) Forget(tenantID string) {
	c.mu.Lock()
	delete(c.counts, tenantID)
	delete(c.known, tenantID)
	c.mu.Unlock()
}

// Allow returns true when the tenant's current cached cardinality is
// strictly less than its effective MaxActiveSeries cap. The bool's
// second return is the suggested Retry-After (always 1 hour for
// cardinality denial — series caps don't change quickly, retrying
// in <60min is wasted load).
//
// Conservative-boot semantics: when `hasFresh` is false (the
// goroutine hasn't completed its first refresh yet), we ALLOW every
// request. This is the right startup posture — blocking startup
// traffic on a not-yet-ready cache would manifest as "every request
// 413s for the first 30s after restart", which is much worse than
// "tenants may briefly exceed cap during startup window".
func (c *CardinalityTracker) Allow(tenantID string, tenantOverride *auth.TenantLimits) (bool, time.Duration) {
	c.SeenTenant(tenantID)

	c.mu.RLock()
	current, ok := c.counts[tenantID]
	fresh := c.hasFresh
	c.mu.RUnlock()

	// Boot-window permissiveness.
	if !fresh {
		return true, 0
	}
	// Unknown tenant (seen for the first time this refresh window) —
	// no count yet. Allow until next refresh learns its count.
	if !ok {
		return true, 0
	}

	effective := auth.ResolveLimits(tenantOverride, c.defaults)
	if effective.MaxActiveSeries <= 0 {
		// Explicit 0 cap = "block all". Mirrors the rate limiter's
		// zero-rate posture for disabled-but-not-deleted tenants.
		return false, time.Hour
	}
	if current >= effective.MaxActiveSeries {
		return false, time.Hour
	}
	return true, 0
}

// CurrentCount returns the cached count for the tenant + a fresh flag.
// Returned for testing / observability; the production path uses
// Allow() which incorporates the cap check.
func (c *CardinalityTracker) CurrentCount(tenantID string) (count int, fresh bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.hasFresh {
		return 0, false
	}
	return c.counts[tenantID], true
}

// RunRefreshLoop blocks until ctx is canceled. Should be started in
// a goroutine at server boot.
func (c *CardinalityTracker) RunRefreshLoop(ctx context.Context) {
	// Do an initial refresh immediately so the first tick doesn't
	// wait the full interval. Errors logged but not fatal —
	// permissive-boot semantics handle the empty-cache case.
	c.refresh(ctx)
	t := time.NewTicker(c.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.refresh(ctx)
		}
	}
}

// refresh issues the count-by-tenant query and updates the cache.
// One round-trip to VM regardless of tenant count.
//
// Query: `count by (tenant_id) ({tenant_id!=""})`
// Returns one row per tenant with a non-empty tenant_id label,
// value = series count. We populate the local map directly from the
// response.
//
// Tenants present in `known` but missing from the response are
// pinned to count=0 in the cache — they have no live series in VM
// (the tenant exists but no agent / Prom has shipped data yet).
// Without this we'd serve stale counts from a previous refresh
// indefinitely after a tenant's data ages out.
func (c *CardinalityTracker) refresh(ctx context.Context) {
	const query = `count by (tenant_id) ({tenant_id!=""})`
	target, err := url.Parse(c.vmURL + "/api/v1/query")
	if err != nil {
		slog.Warn("cardinality refresh: invalid VM URL", slog.String("error", err.Error()))
		return
	}
	q := target.Query()
	q.Set("query", query)
	target.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		slog.Warn("cardinality refresh: build request", slog.String("error", err.Error()))
		return
	}
	resp, err := c.client.Do(req)
	if err != nil {
		// Network error / VM unreachable. Don't update — last good
		// counts remain in cache. Permissive boot still applies if
		// this was the first attempt.
		slog.Warn("cardinality refresh: VM query failed", slog.String("error", err.Error()))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		slog.Warn("cardinality refresh: VM returned non-200",
			slog.Int("status", resp.StatusCode),
			slog.String("body", string(body)))
		return
	}

	var parsed struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string `json:"metric"`
				Value  [2]interface{}    `json:"value"` // [unix_seconds, "string_count"]
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		slog.Warn("cardinality refresh: VM response parse", slog.String("error", err.Error()))
		return
	}
	if parsed.Status != "success" {
		slog.Warn("cardinality refresh: VM status not success", slog.String("status", parsed.Status))
		return
	}

	// Build the new count map.
	newCounts := make(map[string]int, len(parsed.Data.Result))
	for _, row := range parsed.Data.Result {
		tid := row.Metric["tenant_id"]
		if tid == "" {
			continue
		}
		// Value comes as [<float64 timestamp>, <string count>].
		strVal, ok := row.Value[1].(string)
		if !ok {
			continue
		}
		// Parse as float first (PromQL count returns float), truncate to int.
		var f float64
		if _, scanErr := fmt.Sscanf(strVal, "%g", &f); scanErr != nil {
			continue
		}
		newCounts[tid] = int(f)
	}

	c.mu.Lock()
	// Preserve known tenants with zero count when they're absent
	// from VM's response — they're tracked but currently empty.
	for tid := range c.known {
		if _, ok := newCounts[tid]; !ok {
			newCounts[tid] = 0
		}
	}
	c.counts = newCounts
	c.hasFresh = true
	c.mu.Unlock()
}
