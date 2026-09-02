import { describe, it, expect, beforeEach, vi, afterEach } from 'vitest'

// Sockets falsos: el navegador de jsdom no abre conexiones, y lo que hay que
// comprobar es la máquina de estados, no la red.
class FakeSocket {
  static instances: FakeSocket[] = []
  static OPEN = 1
  readyState = 0 // CONNECTING
  onopen: (() => void) | null = null
  onclose: (() => void) | null = null
  onerror: (() => void) | null = null
  onmessage: ((e: { data: string }) => void) | null = null
  closed = false
  sent: string[] = []

  constructor(public url: string) {
    FakeSocket.instances.push(this)
  }
  send(data: string) {
    this.sent.push(data)
  }
  close() {
    this.closed = true
    this.readyState = 3 // CLOSED
    // Un socket real dispara onclose al cerrarse. Reproducirlo es el punto del
    // test: ese handler es el que programaba la reconexión.
    this.onclose?.()
  }
  open() {
    this.readyState = 1
    this.onopen?.()
  }
}

vi.stubGlobal('WebSocket', FakeSocket as unknown as typeof WebSocket)

// El módulo exporta un singleton, así que se reimporta en cada test para
// arrancar con estado limpio.
async function freshManager() {
  vi.resetModules()
  FakeSocket.instances = []
  const mod = await import('./websocket')
  return mod.wsManager
}

describe('wsManager.disconnect', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('no deja programada una reconexión', async () => {
    // El fallo que traía: `close()` con el `onclose` puesto llama a
    // scheduleReconnect, así que "desconectar" era una pausa de un segundo. Al
    // usarlo para soltar el feed en ámbito global, el gating habría compilado,
    // pasado los tests y no hecho nada.
    const ws = await freshManager()
    ws.connect()
    FakeSocket.instances[0].open()
    expect(FakeSocket.instances).toHaveLength(1)

    ws.disconnect()
    // Muy por encima del backoff inicial de 1s.
    vi.advanceTimersByTime(30_000)

    expect(FakeSocket.instances).toHaveLength(1)
  })

  it('permite volver a conectar después — entrar, salir a global, volver', async () => {
    // El ciclo real del usuario. Si `disconnect` dejara `isConnecting` en true,
    // `connect()` saldría temprano y el cluster se quedaría sin feed en vivo
    // el resto de la sesión, sin ningún error visible.
    const ws = await freshManager()
    ws.connect()
    FakeSocket.instances[0].open()

    ws.disconnect()
    ws.connect()

    expect(FakeSocket.instances).toHaveLength(2)
    // OSS: un solo tenant, el socket no lleva cluster en la URL — basta con que
    // se haya abierto un segundo socket contra el endpoint.
    expect(FakeSocket.instances[1].url).toContain('/api/v1/ws')
  })

  it('corta un intento que aún no había abierto', async () => {
    // Salir a global antes de que el socket llegue a OPEN: sin resetear
    // `isConnecting`, el manager se queda creyendo que hay un intento en vuelo
    // para siempre.
    const ws = await freshManager()
    ws.connect() // queda en CONNECTING, nunca se llama open()

    ws.disconnect()
    vi.advanceTimersByTime(30_000)
    expect(FakeSocket.instances).toHaveLength(1)

    ws.connect()
    expect(FakeSocket.instances).toHaveLength(2)
  })

  it('un cierre NO provocado por nosotros sí reconecta', async () => {
    // La otra mitad: al arreglar disconnect no se puede matar la reconexión
    // automática, que es lo que devuelve el live-update tras un despliegue del
    // backend o una caída de red.
    const ws = await freshManager()
    ws.connect()
    FakeSocket.instances[0].open()

    // El servidor se fue: un socket real pasa a CLOSED y dispara onclose.
    FakeSocket.instances[0].close()
    vi.advanceTimersByTime(30_000)

    expect(FakeSocket.instances.length).toBeGreaterThan(1)
  })
})
