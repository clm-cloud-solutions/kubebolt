package websocket

import (
	"testing"
	"time"
)

func TestClient_MatchesScope(t *testing.T) {
	cases := []struct {
		name                   string
		clientTenant, clientCl string
		msgTenant, msgCl       string
		want                   bool
	}{
		{"global msg → any client", "default", "a", "", "", true},
		{"unscoped client → any msg", "", "", "default", "a", true},
		{"scoped match", "default", "a", "default", "a", true},
		{"scoped mismatch cluster", "default", "a", "default", "b", false},
		{"scoped mismatch tenant", "t1", "a", "t2", "a", false},
		{"both unscoped", "", "", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Client{}
			c.SetScope(tc.clientTenant, tc.clientCl)
			if got := c.matchesScope(tc.msgTenant, tc.msgCl); got != tc.want {
				t.Fatalf("matchesScope(%q,%q) with client(%q,%q) = %v, want %v",
					tc.msgTenant, tc.msgCl, tc.clientTenant, tc.clientCl, got, tc.want)
			}
		})
	}
}

// End-to-end through the running hub: a scoped broadcast reaches only the
// client viewing that (tenant,cluster) plus any unscoped client.
func TestHub_ScopedDeliveryFiltersByCluster(t *testing.T) {
	h := NewHub()
	go h.Run()

	register := func(tenant, cluster string) *Client {
		c := &Client{send: make(chan []byte, 8), subs: map[string]bool{}}
		c.SetScope(tenant, cluster)
		h.register <- c
		return c
	}
	a := register("default", "cluster-a")
	b := register("default", "cluster-b")
	all := register("", "") // unscoped → receives everything

	recv := func(c *Client) bool {
		select {
		case <-c.send:
			return true
		case <-time.After(250 * time.Millisecond):
			return false
		}
	}

	h.BroadcastScoped("default", "cluster-a", ResourceUpdated, map[string]string{"x": "1"})

	if !recv(a) {
		t.Fatalf("client viewing cluster-a must receive its scoped event")
	}
	if !recv(all) {
		t.Fatalf("unscoped client must receive every scoped event")
	}
	if recv(b) {
		t.Fatalf("client viewing cluster-b must NOT receive cluster-a's event")
	}

	// A global (unscoped) broadcast reaches all three.
	h.Broadcast(ClusterConnected, nil)
	if !recv(a) || !recv(b) || !recv(all) {
		t.Fatalf("a global broadcast must reach every client")
	}
}
