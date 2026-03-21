package websocket

import (
	"encoding/json"
	"log"
	"sync"

	"github.com/kubebolt/kubebolt/apps/api/internal/models"
)

// Hub manages WebSocket clients and broadcasts messages.
type Hub struct {
	clients    map[*Client]bool
	broadcast  chan *models.WSMessage
	register   chan *Client
	unregister chan *Client
	mu         sync.RWMutex
}

// NewHub creates a new Hub.
func NewHub() *Hub {
	return &Hub{
		clients:    make(map[*Client]bool),
		broadcast:  make(chan *models.WSMessage, 4096),
		register:   make(chan *Client),
		unregister: make(chan *Client),
	}
}

// Run processes hub events. Should be called as a goroutine.
func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			h.mu.Unlock()
			log.Printf("WebSocket client connected (%d total)", len(h.clients))

		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
			}
			h.mu.Unlock()
			log.Printf("WebSocket client disconnected (%d total)", len(h.clients))

		case msg := <-h.broadcast:
			data, err := json.Marshal(msg)
			if err != nil {
				log.Printf("Error marshaling broadcast: %v", err)
				continue
			}
			h.mu.RLock()
			var slowClients []*Client
			for client := range h.clients {
				// Check if the client is subscribed to this message type
				if !client.IsSubscribed(msg.Type) {
					continue
				}
				select {
				case client.send <- data:
				default:
					slowClients = append(slowClients, client)
				}
			}
			h.mu.RUnlock()
			// Unregister slow clients synchronously to avoid goroutine leak
			for _, c := range slowClients {
				h.unregister <- c
			}
		}
	}
}

// Broadcast sends a message to all subscribed clients.
func (h *Hub) Broadcast(msgType string, data interface{}) {
	h.mu.RLock()
	clientCount := len(h.clients)
	h.mu.RUnlock()

	// Skip if no clients are connected
	if clientCount == 0 {
		return
	}

	msg := &models.WSMessage{
		Type: msgType,
		Data: data,
	}
	select {
	case h.broadcast <- msg:
	default:
		// Channel full — drop silently to avoid log spam during cluster switches
	}
}
