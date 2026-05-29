package api

import (
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	gorilla "github.com/gorilla/websocket"
)

// pfUpgrader upgrades the raw port-forward stream. Same permissive origin
// policy as the exec upgrader (auth is enforced before the handler).
var pfUpgrader = gorilla.Upgrader{
	CheckOrigin:     func(r *http.Request) bool { return true },
	ReadBufferSize:  32 * 1024,
	WriteBufferSize: 32 * 1024,
}

// handleWSPortForward (Sprint 2) bridges a browser WebSocket to an active
// port-forward's loopback port, carrying raw TCP both ways. This is the
// "WS tunnel" that makes port-forward consumable against a REMOTE backend:
// the existing TCP bridge binds a port on the backend host (only reachable
// when browser + backend are the same machine — see
// project_port_forward_remote_backend_limitation), and the reverse-proxy
// (/pf/{id}) only carries HTTP. This endpoint carries arbitrary TCP for
// in-browser / WS-speaking consumers, over the same network path the
// dashboard already uses (browser → backend), so it works remotely.
//
// For agent-proxy (remote) clusters the underlying port-forward already
// rides the agent's gRPC tunnel via cluster.SPDYTransportsFor — so this
// handler closes the last-mile gap (browser → backend) without any new
// agent-side work.
//
// IN-VIVO GATE: per the 1.14 roadmap this needs a POC against yagan-prod
// through caddy (HTTP/2 → WS upgrade) for a >10-min session before it's
// trusted. Build-green + this bridge are necessary but not sufficient.
func (h *handlers) handleWSPortForward(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	pf := h.pfManager.Get(id)
	if pf == nil || pf.Status != "active" {
		http.Error(w, "port-forward not found or not active", http.StatusNotFound)
		return
	}

	// Dial the loopback PF port BEFORE upgrading, so a dial failure returns a
	// clean HTTP error instead of a half-open WebSocket.
	backend, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", pf.LocalPort), 5*time.Second)
	if err != nil {
		http.Error(w, "failed to reach forwarded port: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer backend.Close()

	ws, err := pfUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade already wrote the error
	}
	defer ws.Close()

	errc := make(chan error, 2)

	// backend (pod) → WebSocket (browser)
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, rerr := backend.Read(buf)
			if n > 0 {
				if werr := ws.WriteMessage(gorilla.BinaryMessage, buf[:n]); werr != nil {
					errc <- werr
					return
				}
			}
			if rerr != nil {
				errc <- rerr
				return
			}
		}
	}()

	// WebSocket (browser) → backend (pod)
	go func() {
		for {
			mt, data, rerr := ws.ReadMessage()
			if rerr != nil {
				errc <- rerr
				return
			}
			// Both binary and text frames carry stream bytes.
			if mt == gorilla.BinaryMessage || mt == gorilla.TextMessage {
				if _, werr := backend.Write(data); werr != nil {
					errc <- werr
					return
				}
			}
		}
	}()

	<-errc // first side to error/close tears down both (deferred Close)
}
