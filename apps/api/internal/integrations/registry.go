package integrations

import (
	"context"
	"fmt"
	"sort"

	"k8s.io/client-go/kubernetes"
)

// Registry holds one Provider per known integration id and
// dispatches list / get / detect calls to them.
//
// Concurrency: the registry is populated at process startup
// (main.go) and read-only thereafter, so no locking needed. If we
// ever add dynamic registration we'll revisit.
type Registry struct {
	providers map[string]Provider
	// order preserves registration order so the UI's list card
	// layout is deterministic across restarts.
	order []string
}

// NewRegistry returns an empty registry. Callers register providers
// explicitly — the registry makes no assumptions about what's
// available.
func NewRegistry() *Registry {
	return &Registry{providers: map[string]Provider{}}
}

// Register adds a provider. Panics on duplicate id because that's
// always a programming error; duplicates can only be introduced at
// build time.
func (r *Registry) Register(p Provider) {
	id := p.Meta().ID
	if _, exists := r.providers[id]; exists {
		panic(fmt.Sprintf("integrations: duplicate provider id %q", id))
	}
	r.providers[id] = p
	r.order = append(r.order, id)
}

// Get returns the provider for a given id. False when not found.
func (r *Registry) Get(id string) (Provider, bool) {
	p, ok := r.providers[id]
	return p, ok
}

// IDs returns the registered integration ids in registration order.
// Useful for tests; the handlers use List.
func (r *Registry) IDs() []string {
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// List runs Detect on every registered provider and returns the
// snapshots. Errors from individual providers surface as
// StatusUnknown on that provider's entry — one failing adapter
// doesn't poison the whole list.
//
// Results are sorted by registration order so the UI doesn't jump
// around between polls.
func (r *Registry) List(ctx context.Context, cs kubernetes.Interface) []Integration {
	out := make([]Integration, 0, len(r.order))
	for _, id := range r.order {
		p := r.providers[id]
		snap, err := p.Detect(ctx, cs)
		if err != nil {
			// Preserve the metadata so the UI still has a card to
			// render; just flag the status.
			meta := p.Meta()
			meta.Status = StatusUnknown
			if snap.Health == nil {
				snap.Health = &Health{Message: err.Error()}
			}
			meta.Health = snap.Health
			out = append(out, meta)
			continue
		}
		out = append(out, snap)
	}
	return out
}

// SortByName reorders integrations alphabetically. Not used by the
// default List path (registration order is the default) but exposed
// for views that want a stable alphabetical sort.
func SortByName(integrations []Integration) {
	sort.Slice(integrations, func(i, j int) bool {
		return integrations[i].Name < integrations[j].Name
	})
}
