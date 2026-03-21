import type { WSEvent, WSMessage } from '@/types/kubernetes'

type MessageHandler = (event: WSEvent) => void

class WebSocketManager {
  private ws: WebSocket | null = null
  private handlers: Set<MessageHandler> = new Set()
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null
  private reconnectDelay = 1000
  private maxReconnectDelay = 30000
  private subscribedResources: string[] = []
  private isConnecting = false

  connect() {
    if (this.ws?.readyState === WebSocket.OPEN || this.isConnecting) return
    this.isConnecting = true

    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const url = `${protocol}//${window.location.host}/api/v1/ws`

    try {
      this.ws = new WebSocket(url)

      this.ws.onopen = () => {
        this.isConnecting = false
        this.reconnectDelay = 1000
        if (this.subscribedResources.length > 0) {
          this.send({ type: 'subscribe', resources: this.subscribedResources })
        }
      }

      this.ws.onmessage = (event) => {
        try {
          const data = JSON.parse(event.data) as WSEvent
          this.handlers.forEach((handler) => handler(data))
        } catch {
          // ignore malformed messages
        }
      }

      this.ws.onclose = () => {
        this.isConnecting = false
        this.scheduleReconnect()
      }

      this.ws.onerror = () => {
        this.isConnecting = false
        this.ws?.close()
      }
    } catch {
      this.isConnecting = false
      this.scheduleReconnect()
    }
  }

  disconnect() {
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer)
      this.reconnectTimer = null
    }
    this.ws?.close()
    this.ws = null
  }

  subscribe(resources: string[]) {
    this.subscribedResources = resources
    if (this.ws?.readyState === WebSocket.OPEN) {
      this.send({ type: 'subscribe', resources })
    }
  }

  onMessage(handler: MessageHandler) {
    this.handlers.add(handler)
    return () => {
      this.handlers.delete(handler)
    }
  }

  private send(message: WSMessage) {
    if (this.ws?.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify(message))
    }
  }

  private scheduleReconnect() {
    if (this.reconnectTimer) return
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null
      this.reconnectDelay = Math.min(this.reconnectDelay * 2, this.maxReconnectDelay)
      this.connect()
    }, this.reconnectDelay)
  }
}

export const wsManager = new WebSocketManager()
