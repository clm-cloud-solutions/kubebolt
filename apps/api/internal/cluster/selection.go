package cluster

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/websocket"
)

// SelectionStore persists the cluster last selected, so a restart lands where
// the operator left off instead of on no cluster at all. The per-org settings
// store (auth.OrgSettingStore) satisfies it.
type SelectionStore interface {
	GetOrgSetting(ctx context.Context, key string) ([]byte, error)
	SetOrgSetting(ctx context.Context, key string, value []byte) error
}

// selectionSettingKey is the org setting holding the selected context name.
const selectionSettingKey = "cluster.selection"

// adoptDelay is how long the first agent's hello waits before a default is
// picked, so the agents that dial in together after a restart are all
// candidates and the pick follows their names, not who connected first.
var adoptDelay = 3 * time.Second

var errAlreadySelected = errors.New("a cluster is already selected")

// SetSelectionStore enables remembering the selected cluster. nil disables it.
func (m *Manager) SetSelectionStore(s SelectionStore) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.selection = s
}

// rememberSelectionLocked persists the selection, off the lock. Caller holds
// m.mu.
func (m *Manager) rememberSelectionLocked(contextName string) {
	s := m.selection
	if s == nil {
		return
	}
	ctx := m.storeCtx()
	go func() {
		if err := s.SetOrgSetting(ctx, selectionSettingKey, []byte(contextName)); err != nil {
			slog.Warn("could not remember the selected cluster",
				slog.String("context", contextName), slog.String("error", err.Error()))
		}
	}()
}

// RestoreSelection selects, at boot, the cluster selected before the restart. Call it once every persisted cluster is registered
// again. A remembered cluster that is no longer registered (deleted meanwhile)
// is ignored and the default pick covers it. An agent-proxy cluster whose agent
// has not connected yet stays selected and connects on that agent's hello
// (AddAgentProxyCluster's retry), like a switch made while the agent was away.
func (m *Manager) RestoreSelection() {
	m.mu.RLock()
	s := m.selection
	ctx := m.storeCtx()
	m.mu.RUnlock()
	if s == nil {
		return
	}
	raw, err := s.GetOrgSetting(ctx, selectionSettingKey)
	if err != nil || len(raw) == 0 {
		return
	}
	contextName := string(raw)
	m.mu.RLock()
	_, registered := m.kubeConfig.Contexts[contextName]
	m.mu.RUnlock()
	if !registered {
		slog.Info("the cluster selected before the restart is no longer registered; a default will be picked",
			slog.String("context", contextName))
		return
	}
	slog.Info("selecting the cluster selected before the restart", slog.String("context", contextName))
	go func() {
		// A failure here is a cluster whose agent has not connected yet: it
		// stays selected and connects on the agent's hello.
		_ = m.SwitchCluster(contextName)
	}()
}

// scheduleDefaultSelectionLocked arranges one default pick (adoptDefaultSelection)
// after adoptDelay. Caller holds m.mu. Single-tenant only (kept for parity with
// the multi-org build, where each org resolves its own first reachable cluster
// per request).
func (m *Manager) scheduleDefaultSelectionLocked() {
	if auth.MultiTenantEnabled {
		return
	}
	if !m.adoptScheduled.CompareAndSwap(false, true) {
		return
	}
	time.AfterFunc(adoptDelay, m.adoptDefaultSelection)
}

// adoptDefaultSelection selects, when nothing is selected, the first cluster
// with a live agent in display-name order (defaultSelection). A backend whose
// kubeconfig names no current context (agent-only installs, `make dev-clean`)
// otherwise starts with nothing selected, and every cluster view answers 503
// "no cluster selected" until someone opens the switcher. The pick is
// remembered like any switch.
func (m *Manager) adoptDefaultSelection() {
	defer m.adoptScheduled.Store(false)
	pick, label := m.defaultSelection()
	if pick == "" {
		return
	}
	if err := m.switchCluster(pick, true); err != nil {
		if !errors.Is(err, errAlreadySelected) {
			slog.Warn("could not select a default cluster", slog.String("context", pick), slog.String("error", err.Error()))
		}
		return
	}
	slog.Info("no cluster was selected: selected the first one with a live agent",
		slog.String("context", pick), slog.String("display_name", label))
	if m.wsHub != nil {
		m.wsHub.Broadcast(websocket.ClusterConnected, map[string]string{"context": pick})
	}
}

// defaultSelection returns the cluster adoptDefaultSelection would select and
// its display name: the first, by display name, among the clusters with a live
// agent that can serve the resource views. "" when something is already
// selected or no cluster qualifies.
func (m *Manager) defaultSelection() (contextName, displayName string) {
	m.mu.RLock()
	if m.activeContext != "" || m.agentRegistry == nil {
		m.mu.RUnlock()
		return "", ""
	}
	var live []string
	for ctxName, cid := range m.agentProxyContexts {
		// A metrics-only agent alone would select a cluster with nothing to open.
		if m.agentRegistry.HasProxyCapableAgent("", cid) {
			live = append(live, ctxName)
		}
	}
	storage := m.storage
	ctx := m.storeCtx()
	m.mu.RUnlock()
	if len(live) == 0 {
		return "", ""
	}
	names := map[string]string{}
	if storage != nil {
		if all, err := storage.AllDisplayNames(ctx); err == nil {
			names = all
		}
	}
	label := func(ctxName string) string {
		if n := names[ctxName]; n != "" {
			return n
		}
		return ctxName
	}
	sort.Slice(live, func(i, j int) bool {
		if a, b := label(live[i]), label(live[j]); a != b {
			return a < b
		}
		return live[i] < live[j]
	})
	return live[0], label(live[0])
}
