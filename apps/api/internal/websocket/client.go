package websocket

import (
	"encoding/json"
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
	subs map[string]bool
	mu   sync.RWMutex
}

// subscribeMessage is the incoming subscribe/unsubscribe request.
type subscribeMessage struct {
	Action string   `json:"action"` // "subscribe" or "unsubscribe"
	Types  []string `json:"types"`
}

// IsSubscribed checks whether the client subscribes to a given message type.
func (c *Client) IsSubscribed(msgType string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	// If no subscriptions set, receive everything
	if len(c.subs) == 0 {
		return true
	}
	return c.subs[msgType]
}

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
		subs: make(map[string]bool),
	}
	hub.register <- client
	go client.writePump()
	go client.readPump()
}

func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
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
		var sub subscribeMessage
		if err := json.Unmarshal(message, &sub); err != nil {
			continue
		}
		c.mu.Lock()
		switch sub.Action {
		case "subscribe":
			for _, t := range sub.Types {
				c.subs[t] = true
			}
		case "unsubscribe":
			for _, t := range sub.Types {
				delete(c.subs, t)
			}
		}
		c.mu.Unlock()
	}
}

func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
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
