package cluster

import (
	"sync"
	"time"
)

// RecentWritesOverlay bridges the gap between a Patch landing on the
// apiserver and the informer cache receiving the corresponding watch
// event. That gap is small (~hundreds of ms in healthy clusters) but
// non-zero, and a UI that fires a manual Refresh in the first ~second
// after a mutation will read stale data from the informer cache and
// see the pre-patch state — looking like the mutation didn't take.
//
// The overlay is a per-resource, per-field, TTL-bounded write log:
//
//   - Mutation handlers (e.g. handleRolloutPause) call Record(...)
//     after a successful Patch with the field they just changed.
//   - GET endpoints that return a resource map call Apply(...) on the
//     map; any in-window override is layered on top of the
//     informer-derived value.
//   - Entries auto-expire after `ttl` (default 5s — well past the
//     informer's typical catch-up window, short enough that a stuck
//     overlay never lives long enough to be confusing).
//
// The overlay does NOT replace the informer cache — it's strictly a
// last-mile correction for the read-after-write window. The informer
// remains the source of truth for everything else (list endpoints,
// metrics, topology, etc.) so list-page reads stay cheap.
//
// Scope today: deployments.paused. The same pattern applies to
// cronjobs.suspend and nodes.unschedulable; extend by recording on
// those mutation handlers and applying in the matching GetResourceDetail
// branches.

// RecentWritesOverlay is goroutine-safe — Record is called from the
// API mutation handlers (one per request goroutine); Apply is called
// from GetResourceDetail / list paths (also goroutine-per-request).
//
// Two related-but-distinct mechanisms live on the same struct:
//
//   1. Field overlays (entries) — for in-place mutations (cordon,
//      pause, suspend). The patched field's value is layered on top
//      of the informer-derived map until the watch event arrives.
//
//   2. Tombstones — for deletes. The deleted resource is masked from
//      list and detail reads until the informer's deletion event
//      propagates. Without this, the UI shows a deleted resource
//      lingering in the list for several seconds, looking like the
//      delete didn't take.
//
// Both share the same TTL-bounded model and goroutine-safety guarantees.
type RecentWritesOverlay struct {
	mu         sync.RWMutex
	entries    map[string]map[string]recentWriteEntry // resourceKey -> field -> entry
	tombstones map[string]time.Time                   // resourceKey -> expires
}

type recentWriteEntry struct {
	value   interface{}
	expires time.Time
}

// NewRecentWritesOverlay returns an empty overlay ready for use.
// The caller stashes it on the Connector (one per cluster).
func NewRecentWritesOverlay() *RecentWritesOverlay {
	return &RecentWritesOverlay{
		entries:    map[string]map[string]recentWriteEntry{},
		tombstones: map[string]time.Time{},
	}
}

// Record stores `value` for the given resource's field with the given
// TTL. Re-recording the same field replaces the prior entry (each
// mutation overrides the last). A zero-duration TTL is treated as
// 5 seconds — the project default — to avoid silent no-ops if a
// caller forgets to pass one.
func (o *RecentWritesOverlay) Record(resourceType, namespace, name, field string, value interface{}, ttl time.Duration) {
	if o == nil {
		return
	}
	if ttl <= 0 {
		ttl = 5 * time.Second
	}
	key := overlayKey(resourceType, namespace, name)
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.entries[key] == nil {
		o.entries[key] = map[string]recentWriteEntry{}
	}
	o.entries[key][field] = recentWriteEntry{
		value:   value,
		expires: time.Now().Add(ttl),
	}
}

// Apply layers any in-window overrides for the given resource onto
// the supplied map. Expired entries are GC'd opportunistically — no
// background sweeper needed because every Apply on the same key
// either consumes or evicts old entries, and unread keys naturally
// stay small.
//
// `m` may be nil (caller's GetResourceDetail returned an error); in
// that case Apply is a no-op.
func (o *RecentWritesOverlay) Apply(resourceType, namespace, name string, m map[string]interface{}) {
	if o == nil || m == nil {
		return
	}
	key := overlayKey(resourceType, namespace, name)
	o.mu.RLock()
	fields, ok := o.entries[key]
	if !ok {
		o.mu.RUnlock()
		return
	}
	now := time.Now()
	// Snapshot still-valid overrides under RLock so the read path is
	// concurrent. Defer eviction to a separate Lock pass below.
	apply := make(map[string]interface{}, len(fields))
	hasExpired := false
	for field, entry := range fields {
		if now.After(entry.expires) {
			hasExpired = true
			continue
		}
		apply[field] = entry.value
	}
	o.mu.RUnlock()

	for field, value := range apply {
		m[field] = value
	}

	if hasExpired {
		o.mu.Lock()
		bucket := o.entries[key]
		for field, entry := range bucket {
			if now.After(entry.expires) {
				delete(bucket, field)
			}
		}
		if len(bucket) == 0 {
			delete(o.entries, key)
		}
		o.mu.Unlock()
	}
}

// Clear drops every override for a specific resource (for example,
// after a Delete — keeping a stale "paused: true" overlay for a
// just-deleted Deployment would be confusing if a same-named
// resource is recreated within 5s). Optional helper, not used by
// the Patch handlers.
func (o *RecentWritesOverlay) Clear(resourceType, namespace, name string) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.entries, overlayKey(resourceType, namespace, name))
}

// RecordDeletion marks a resource as deleted for `ttl`. Used by the
// delete handler to mask the resource from subsequent list / detail
// reads until the informer's deletion watch event propagates —
// typically <1s but the UI's refetch can fire faster than that, so a
// brief tombstone closes the visual gap.
//
// Default TTL when ttl≤0 is 10s, longer than the field-overlay default
// because cascade deletes (Deployment → ReplicaSets → Pods) take a
// few seconds to ripple through the informer.
func (o *RecentWritesOverlay) RecordDeletion(resourceType, namespace, name string, ttl time.Duration) {
	if o == nil {
		return
	}
	if ttl <= 0 {
		ttl = 10 * time.Second
	}
	key := overlayKey(resourceType, namespace, name)
	o.mu.Lock()
	defer o.mu.Unlock()
	o.tombstones[key] = time.Now().Add(ttl)
}

// IsDeleted reports whether the resource is in its tombstone window.
// Expired tombstones are GC'd opportunistically on read so the map
// stays small without a background sweeper.
func (o *RecentWritesOverlay) IsDeleted(resourceType, namespace, name string) bool {
	if o == nil {
		return false
	}
	key := overlayKey(resourceType, namespace, name)
	o.mu.RLock()
	expires, ok := o.tombstones[key]
	o.mu.RUnlock()
	if !ok {
		return false
	}
	if time.Now().After(expires) {
		o.mu.Lock()
		// Re-check under write lock — another goroutine may have
		// re-recorded the deletion in the meantime.
		if t, still := o.tombstones[key]; still && time.Now().After(t) {
			delete(o.tombstones, key)
		}
		o.mu.Unlock()
		return false
	}
	return true
}

// ClearTombstone removes a deletion tombstone for a resource. Useful
// when a resource of the same name is re-created within the tombstone
// window — the new resource is real and shouldn't be masked.
func (o *RecentWritesOverlay) ClearTombstone(resourceType, namespace, name string) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.tombstones, overlayKey(resourceType, namespace, name))
}

func overlayKey(resourceType, namespace, name string) string {
	return resourceType + ":" + namespace + ":" + name
}
