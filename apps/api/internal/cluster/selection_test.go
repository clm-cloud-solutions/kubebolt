package cluster

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/agent/channel"
)

// memSelectionStore is an in-memory SelectionStore, one value per key (the
// tests run single-tenant).
type memSelectionStore struct {
	mu sync.Mutex
	v  map[string][]byte
}

func (s *memSelectionStore) GetOrgSetting(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.v[key]; ok {
		return v, nil
	}
	return nil, errors.New("not found")
}

func (s *memSelectionStore) SetOrgSetting(_ context.Context, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.v == nil {
		s.v = map[string][]byte{}
	}
	s.v[key] = value
	return nil
}

func (s *memSelectionStore) get(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.v[key])
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// A switch is remembered, even one whose agent has not connected yet: the
// operator chose that cluster.
func TestSelection_SwitchIsRemembered(t *testing.T) {
	m := newBareManager()
	m.SetAgentRegistry(channel.NewAgentRegistry())
	store := &memSelectionStore{}
	m.SetSelectionStore(store)
	if _, err := m.AddAgentProxyCluster("c1", "Lab"); err != nil {
		t.Fatal(err)
	}

	_ = m.SwitchCluster("agent:c1") // fails: no agent yet

	eventually(t, "the selection to be stored", func() bool { return store.get(selectionSettingKey) == "agent:c1" })
}

// At boot the remembered cluster is selected again; one deleted meanwhile is
// ignored and leaves the slot to the default pick.
func TestSelection_RestoreSelection(t *testing.T) {
	t.Run("remembered and registered", func(t *testing.T) {
		m := newBareManager()
		m.SetAgentRegistry(channel.NewAgentRegistry())
		store := &memSelectionStore{v: map[string][]byte{selectionSettingKey: []byte("agent:c2")}}
		m.SetSelectionStore(store)
		for _, cid := range []string{"c1", "c2"} {
			if _, err := m.AddAgentProxyCluster(cid, ""); err != nil {
				t.Fatal(err)
			}
		}

		m.RestoreSelection()

		eventually(t, "agent:c2 to be selected", func() bool { return m.ActiveContext() == "agent:c2" })
	})
	t.Run("remembered but deleted", func(t *testing.T) {
		m := newBareManager()
		m.SetAgentRegistry(channel.NewAgentRegistry())
		m.SetSelectionStore(&memSelectionStore{v: map[string][]byte{selectionSettingKey: []byte("agent:gone")}})
		if _, err := m.AddAgentProxyCluster("c1", ""); err != nil {
			t.Fatal(err)
		}

		m.RestoreSelection()
		time.Sleep(100 * time.Millisecond)

		if got := m.ActiveContext(); got != "" {
			t.Errorf("selected %q, want nothing (the remembered cluster is gone)", got)
		}
	})
}

// With nothing selected, the default is the first cluster by display name
// among those with a live agent that can serve the resource views.
func TestSelection_DefaultSelection(t *testing.T) {
	m := newBareManager()
	reg := channel.NewAgentRegistry()
	m.SetAgentRegistry(reg)
	m.SetStorage(newBoltStorage(t))
	// Registered before their agents connect, so no default pick is scheduled.
	for cid, name := range map[string]string{
		"c-zeta":  "zeta (via agent)",
		"c-alpha": "alpha (via agent)",
		"c-aaa":   "aaa (via agent)", // metrics-only agent
		"c-off":   "000 (via agent)", // no agent
	} {
		if _, err := m.AddAgentProxyCluster(cid, name); err != nil {
			t.Fatal(err)
		}
	}
	reg.Register(channel.NewAgent("c-zeta", "a1", "n1", nil, []string{"metrics", "kube-proxy"}, nil))
	reg.Register(channel.NewAgent("c-alpha", "a1", "n1", nil, []string{"metrics", "kube-proxy"}, nil))
	reg.Register(channel.NewAgent("c-aaa", "a1", "n1", nil, []string{"metrics"}, nil))

	if got, name := m.defaultSelection(); got != "agent:c-alpha" || name != "alpha (via agent)" {
		t.Errorf("defaultSelection = %q (%q), want agent:c-alpha", got, name)
	}

	// Never over a selection someone already made.
	m.mu.Lock()
	m.activeContext = "agent:c-zeta"
	m.mu.Unlock()
	if got, _ := m.defaultSelection(); got != "" {
		t.Errorf("defaultSelection with a cluster selected = %q, want none", got)
	}
	if err := m.switchCluster("agent:c-alpha", true); !errors.Is(err, errAlreadySelected) {
		t.Errorf("switchCluster(onlyIfUnselected) over a selection = %v, want errAlreadySelected", err)
	}
	if got := m.ActiveContext(); got != "agent:c-zeta" {
		t.Errorf("selection changed to %q", got)
	}
}
