package websocket

import (
	"log"
	"net/http"
	"sync"
	"time"

	gorilla "github.com/gorilla/websocket"
)

const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = (pongWait * 9) / 10
	maxMessageSize = 4096
)

var upgrader = gorilla.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // CORS handled at HTTP layer
	},
}

// Client represents a single WebSocket connection.
type Client struct {
	hub  *Hub
	conn *gorilla.Conn
	send chan []byte
	// tenant/cluster scope the events this client wants (A.4). Empty = receive
	// every cluster's events (the OSS-degenerate default). EE sets these so a
	// tenant's client never sees another tenant's resource/insight events.
	tenant    string
	cluster   string
	mu        sync.RWMutex
	closeOnce sync.Once
}

// SetScope pins the (tenant, cluster) this client is viewing. Empty values
// clear the scope (receive all). Safe for concurrent use.
func (c *Client) SetScope(tenant, cluster string) {
	c.mu.Lock()
	c.tenant = tenant
	c.cluster = cluster
	c.mu.Unlock()
}

// matchesScope reports whether a message tagged (tenant, cluster) should reach
// this client. An unscoped message (empty) is global → always delivered. A
// client with no scope set receives everything (OSS-degenerate). Otherwise both
// dimensions must match.
func (c *Client) matchesScope(tenant, cluster string) bool {
	if tenant == "" && cluster == "" {
		return true // global event
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.tenant == "" && c.cluster == "" {
		return true // unscoped client — receive all
	}
	return c.tenant == tenant && c.cluster == cluster
}

// NOTE — there is deliberately no per-message-type subscription here.
//
// One existed until 2026-08-19 and had never worked: the client sent
// `{"type":"subscribe","resources":["pods",…]}` while this file decoded
// `{"action":…,"types":…}`, so neither field ever matched and the subscription
// set stayed empty. An empty set meant "deliver everything", so the mechanism
// failed OPEN and looked correct for as long as it existed.
//
// It was removed rather than repaired, for two reasons:
//
//  1. The vocabularies did not match either. The client listed RESOURCE KINDS
//     (`pods`, `nodes`) while the gate was consulted with MESSAGE TYPES
//     (`resource:updated`). Aligning only the field names would have made every
//     lookup miss and silenced every browser at once — a latent outage sitting
//     one well-intentioned commit away.
//  2. After finding #43 reduced broadcasts to small notifications, filtering
//     buys very little: the highest-volume kinds (Pods, Events) are precisely
//     the ones the UI needs, so a correct filter would cut a minority of an
//     already-small payload while risking that some detail page silently stops
//     updating.
//
// What scopes a client today is (tenant, cluster) — see matchesScope — and that
// is enforced, tested, and enough. If per-kind filtering is ever wanted, design
// it against the `kind` field that #43 put on the wire, and change both sides in
// the same release.

// ServeWS upgrades an HTTP connection to WebSocket and registers the client.
func ServeWS(hub *Hub, w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}
	client := &Client{
		hub:  hub,
		conn: conn,
		send: make(chan []byte, 256),
	}
	hub.register <- client
	go client.writePump()
	go client.readPump()
}

func (c *Client) close() {
	c.closeOnce.Do(func() {
		c.conn.Close()
	})
}

func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.close()
	}()
	c.conn.SetReadLimit(maxMessageSize)
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})
	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if gorilla.IsUnexpectedCloseError(err, gorilla.CloseGoingAway, gorilla.CloseNormalClosure) {
				log.Printf("WebSocket read error: %v", err)
			}
			break
		}
		// Frames from the client are drained and ignored: the read loop must keep
		// running for gorilla to process pongs and close frames, but the client
		// has nothing to tell us — see the note above.
		_ = message
	}
}

func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.close()
	}()
	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				c.conn.WriteMessage(gorilla.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(gorilla.TextMessage, message); err != nil {
				return
			}
		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(gorilla.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
