package api

import (
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
)

// The field bug: Kobi offered "switch cluster and I'll ask there", the browser
// switched and then sent the follow-up carrying the PREVIOUS cluster's
// conversation id. The handler honoured it, the persist step stamped the new
// cluster on the record, and one conversation ended up holding two clusters'
// tool results — visible in the history list as a conversation that had moved
// cluster. The client is fixed; this is the guard that makes it unreachable.

func TestConversationForCluster_MintsWhenNoneRequested(t *testing.T) {
	store := copilot.NewMemoryConversationStore()
	id, isNew, displaced := conversationForCluster(store, copilot.DefaultConversationTenant, localUser, "", "prod")
	if id == "" || !isNew {
		t.Fatalf("id=%q isNew=%v, want a fresh id", id, isNew)
	}
	if displaced != "" {
		t.Fatalf("displaced = %q, want empty — nothing was requested", displaced)
	}
}

func TestConversationForCluster_ContinuesOnItsOwnCluster(t *testing.T) {
	store := copilot.NewMemoryConversationStore()
	seedConv(t, store, "c1", "prod", "on prod")

	id, isNew, displaced := conversationForCluster(store, copilot.DefaultConversationTenant, localUser, "c1", "prod")
	if id != "c1" || isNew || displaced != "" {
		t.Fatalf("id=%q isNew=%v displaced=%q, want c1/false/\"\"", id, isNew, displaced)
	}
}

func TestConversationForCluster_DisplacesOneFromAnotherCluster(t *testing.T) {
	store := copilot.NewMemoryConversationStore()
	seedConv(t, store, "c1", "kind-kubebolt-lab", "asked on lab",
		copilot.Message{Role: copilot.RoleUser, Content: "how is my fleet?"})

	id, isNew, displaced := conversationForCluster(
		store, copilot.DefaultConversationTenant, localUser, "c1", "agent:5368e0d2")
	if id == "c1" {
		t.Fatal("continued a conversation belonging to another cluster")
	}
	if !isNew {
		t.Fatalf("isNew = false, want true — a displaced turn starts a conversation")
	}
	if displaced != "c1" {
		t.Fatalf("displaced = %q, want c1 so the warning can name it", displaced)
	}

	// And the one it left is untouched: same cluster, same transcript.
	prior, found, err := store.Get(copilot.DefaultConversationTenant, localUser, "c1")
	if err != nil || !found {
		t.Fatalf("prior conversation gone: found=%v err=%v", found, err)
	}
	if prior.ClusterID != "kind-kubebolt-lab" {
		t.Fatalf("prior cluster = %q, want kind-kubebolt-lab — it must not move", prior.ClusterID)
	}
	if len(prior.Messages) != 1 {
		t.Fatalf("prior messages = %d, want 1 — nothing may be appended to it", len(prior.Messages))
	}
}

func TestConversationForCluster_NoActiveClusterMakesNoClaim(t *testing.T) {
	// Metrics-only, or mid-reconnect: there is no cluster to compare against,
	// so displacing would be guessing. Kobi answers without a connector on
	// purpose; a history question must not lose its thread here.
	store := copilot.NewMemoryConversationStore()
	seedConv(t, store, "c1", "prod", "on prod")

	id, isNew, displaced := conversationForCluster(store, copilot.DefaultConversationTenant, localUser, "c1", "")
	if id != "c1" || isNew || displaced != "" {
		t.Fatalf("id=%q isNew=%v displaced=%q, want the thread kept", id, isNew, displaced)
	}
}

func TestConversationForCluster_UnknownIDIsKept(t *testing.T) {
	// The id may simply not be in the store yet (first turn of a conversation
	// the client already minted). That is not a cluster conflict.
	store := copilot.NewMemoryConversationStore()
	id, isNew, displaced := conversationForCluster(store, copilot.DefaultConversationTenant, localUser, "never-seen", "prod")
	if id != "never-seen" || isNew || displaced != "" {
		t.Fatalf("id=%q isNew=%v displaced=%q, want it kept as-is", id, isNew, displaced)
	}
}

func TestConversationForCluster_NoStoreIsPassThrough(t *testing.T) {
	id, isNew, displaced := conversationForCluster(nil, copilot.DefaultConversationTenant, localUser, "c1", "prod")
	if id != "c1" || isNew || displaced != "" {
		t.Fatalf("id=%q isNew=%v displaced=%q, want pass-through when persistence is off", id, isNew, displaced)
	}
}
