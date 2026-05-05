# Sprint A.5 — Design Doc: agent-as-K8s-API-proxy

**Estado**: Funcional core ✅. Commits 1-8e + fixes de smoke test ✅. Pendientes: 8f-8h (hardening + tests), 9-10 (helm), 11-12 (integration + e2e).
**Pre-requisito**: Sprint A ✅ (17 commits en `feat/agent-auth`).
**Branch**: `feat/agent-kube-proxy` (25 commits ahead de `develop` al cerrar el core).
**Estimación (actualizada con SPDY scope-in)**: 4-5 semanas full-time / 6-8 semanas calendar.

**Cambio de scope** (mid-sprint): SPDY/WebSocket tunneling para exec/portforward/attach
entra al sprint. Originalmente marcado como out-of-scope §8 con etiqueta "A.5.5 si surge
necesidad". Razón del scope-in: KubeBolt SaaS productivo no es viable sin pod terminal
+ portforward via agent-proxy. Sin ellos, agent-proxy es read-only-ish para los flujos
interactivos críticos. Decisiones técnicas en §0.7-§0.9.

**Verificado en vivo** (smoke test contra kind-kubebolt-dev): pod terminal, file browser,
port-forward, restart deployment, scale, delete, YAML apply, Copilot tool calls — todos
funcionando vía agent-proxy con sesión interactiva real (`v4.channel.k8s.io` negotiated,
6 KiB+ exec output flowing through tunnel).

---

## 0. Decisiones tomadas (confirmadas — no abrir de nuevo sin razón)

### 0.1 — Migración del wire protocol: **flip duro**

> **Decisión**: opción B (flip duro) — el agente todavía no está publicado fuera de
> dev local, no hay fleets desplegados que romper.

Sprint A tiene 3 RPCs (`Register`, `StreamMetrics`, `Heartbeat`) en `AgentIngest v1`.
Sprint A.5 los reemplaza completos con un único bidi stream `AgentChannel v2` que
multiplexa heartbeat + metrics + kube_request + kube_response + watch_events.

**Implicancias**:
- **No coexistencia**: el backend solo expone `AgentChannel v2`; `AgentIngest v1`
  se elimina del código.
- **No flag**: ningún `KUBEBOLT_AGENT_PROTOCOL` env var, ningún Helm value v1/v2.
- **Mensajes core (`Sample`, `MetricBatch`, `AgentConfig`, `AgentHealthStats`)** se
  mueven a `proto/kubebolt/agent/v2/` — el package `v1` se elimina entero.
- **Borramos**: `apps/api/internal/agent/server.go` (Server.Register/StreamMetrics/Heartbeat),
  `packages/agent/internal/shipper/shipper.go` se reescribe contra el canal nuevo
  (la auth + TLS de Sprint A se preserva, lo que cambia es el RPC subyacente).
- **Conservamos**: el interceptor de auth, TLS config, tenants store, admin REST,
  rate limiter, etc. de Sprint A — todo se aplica al server v2 sin cambios.

### 0.2 — Backpressure en watch streams: **close stream**

> **Decisión**: opción B — cuando el buffer al backend se llena, el agente cierra
> el watch y deja que el reflector de client-go haga re-list completo.

Es el comportamiento más simple, y los reflectores de client-go ya lo manejan
nativamente (re-list es parte de su contrato). La opción A (drop oldest +
bookmark) queda como mejora futura para Sprint B+ si las métricas de "stream
restarts" muestran que la frecuencia justifica la complejidad extra.

### 0.3 — Identity model: **híbrido opt-in (default false)**

> **Decisión**: opción C — Helm value `agentIngest.autoRegisterClusters: false`
> por default. El operador opta explícitamente en self-hosted.

Cuando un agente hace handshake `Hello → Welcome` y el flag está activo, el
manager crea un `ClusterAccess{Mode: agent-proxy, ID: cluster_id}` en el
inventario. La UI lo lista como cualquier otro cluster. Sin el flag, el
operador tiene que llamar a `POST /api/v1/admin/clusters` (futuro) para
registrarlo manualmente — y los handshakes de agentes para clusters no
registrados se rechazan con un mensaje claro.

### 0.4 — Capacidad: target inicial **100 agentes / 5k watches**

> **Decisión**: confirmado — Sprint A.5 target es 100 agentes simultáneos con
> ~5000 watches concurrentes (suficiente para SaaS small + headroom).

Escala mayor (1000+ agentes) cae en Sprint C+ con sharding del registry,
goroutine pooling y un análisis serio del consumo de memoria por stream.

### 0.5 — Arquitectura: **monolito**

> **Decisión**: el proxy vive dentro del binario actual del backend, junto con
> el HTTP API + ingest gRPC + websocket hub. Sin microservicio separado.

Si la carga eventualmente lo justifica (probablemente Sprint D+ con escala
SaaS real), se extrae a un binario `kubebolt-tunnel` separado. Sprint A.5
mantiene un único proceso para no sumar operaciones.

### 0.6 — Pseudoendpoint: **`https://<cluster_id>.agent.local`**

> **Decisión**: confirmado — el `rest.Config.Host` para clusters en
> `Mode: agent-proxy` es `https://<cluster_id>.agent.local`.

Sirve para logs (en grep se ve qué cluster vía qué proxy), para evitar
ambigüedades en SNI (client-go puede validar host name aunque la conexión
vaya por el Transport custom), y deja claro a cualquier dev que se topa con
el log que NO es un host real DNS-resolvible.

### 0.7 — SPDY/WebSocket tunneling: **opaque byte-tunnel**

> **Decisión**: opción D (byte-tunnel) — el `AgentProxyTransport` detecta
> el header `Connection: Upgrade` en la request y, a partir del 101
> Switching Protocols, ambas direcciones intercambian bytes crudos vía
> mensajes `KubeStreamData{data, eof}` correlacionados por `request_id`.

K8s usa SPDY (y WebSocket desde 1.30+ vía KEP-4006) para `pods/exec`,
`pods/attach` y `pods/portforward`. Estos protocolos son bidireccionales
multi-stream (stdin/stdout/stderr/error/resize) que NO caben en el modelo
unary-or-watch del proto actual.

**Alternativas consideradas y descartadas**:

| Approach | Por qué se descarta |
|---|---|
| Decodificar SPDY en el agent + re-encode per-stream | 500+ LOC framing; SPDY es protocolo en sunset (K8s migra a WebSocket); doble código path para WS futuro |
| Endpoint WebSocket separado en el agent | Rompe el modelo outbound-only — agents detrás de NAT no aceptan inbound; mata la propiedad SaaS-friendly |
| Stream gRPC dedicado por tunnel | El agent es quien dial-ea; backend no puede crear streams nuevos hacia agents |

**El byte-tunnel funciona idéntico para SPDY y WebSocket** — no parseamos
nada, sólo movemos bytes. client-go en el backend hace su framing SPDY
normal **encima** del tunnel. Cuando K8s deprecate SPDY completo y todos
los clientes hablen WS, no tocamos nada.

**Wire format** (proto v2, oneof addition):

```proto
// Either direction. Adds to AgentMessage.kind and BackendMessage.kind.
message KubeStreamData {
  bytes data = 1;
  bool eof = 2;     // signals end-of-stream from sender's side
}
```

**Flow para un exec request**:

```
1. backend → agent : KubeProxyRequest{method=POST, path=/.../exec?...,
                                      headers={Connection: Upgrade,
                                               Upgrade: SPDY/3.1, ...}}
2. agent: detecta Upgrade, dialea apiserver con SPDY upgrade
3. agent → backend : KubeProxyResponse{status_code=101, headers=...}
4. (a partir de aquí ambas direcciones envían KubeStreamData)
5. backend → agent : KubeStreamData{data=<stdin bytes>}      (loop)
6. agent  → backend: KubeStreamData{data=<stdout bytes>}     (loop)
7. cualquier lado: KubeStreamData{eof=true}  o  StreamClosed
```

### 0.8 — Backpressure en tunnels: **credit-based flow control**

> **Decisión**: opción A — flow control explícito con ACKs, NO blocking.
> Cada tunnel mantiene una ventana de bytes outstanding (default 256 KiB).
> El receiver envía `KubeStreamAck{request_id, bytes_consumed}` cuando
> consume datos; el sender pausa cuando outstanding ≥ window.

Diferencia crítica vs watch streams: en watch podemos drop-oldest porque
el reflector de client-go re-lista (§0.2). En tunnels **cualquier byte
perdido corrompe la sesión** — un solo byte de stdout faltante en un
exec significa caracteres fantasma en la terminal.

**Por qué credit-based y no "blocking on saturation"**:
- 50 watches + 5 exec sessions comparten el mismo bidi gRPC channel
- "Blocking" en un buffer compartido hace que un exec slow-consumer **ahoga** los 50 watches
- Credit-based aísla: cada tunnel tiene su propia ventana, lentitud en uno NO afecta otros
- Es exactamente lo que hace HTTP/2 a nivel de stream — pero como estamos multiplexando MUCHOS tunnels logical en UN stream HTTP/2, necesitamos hacerlo a nivel de aplicación

**Wire format** (segunda adición a oneof):

```proto
message KubeStreamAck {
  uint64 bytes_consumed = 1;  // delta since last ACK, monotonic
}
```

**Tamaño de ventana** (256 KiB inicial — heurística):
- Demasiado pequeño: round-trip cada N kilobytes mata throughput de portforward de DB streams
- Demasiado grande: una sesión exec puede acaparar 16 MiB con un stdout flood
- 256 KiB cubre típica latencia de 50ms con throughput de 5 MB/s — sweet spot
- Configurable vía `KUBEBOLT_AGENT_TUNNEL_WINDOW_BYTES`

### 0.9 — Hardening de tunnels para SaaS productivo

> **Decisión**: defaults conservadores en OSS, override por env vars.
> Defaults asumen un tenant de buena fe; SaaS multi-tenant los baja
> (per-tenant rate limiting es ENTERPRISE-CANDIDATE).

| Limit | Default | Env var | Justificación |
|---|---|---|---|
| Idle timeout | 5 min | `KUBEBOLT_TUNNEL_IDLE_TIMEOUT` | Sesión exec olvidada por usuario que cerró laptop sin Ctrl+D |
| Max duration | 24 h | `KUBEBOLT_TUNNEL_MAX_DURATION` | Hard cap; security defense in depth |
| Max tunnels per agent | 50 | `KUBEBOLT_TUNNEL_MAX_PER_AGENT` | Evita un cliente abriendo 10k tunnels |
| Max bytes/sec per tunnel | 50 MiB/s | `KUBEBOLT_TUNNEL_MAX_BPS` | Cap a portforward abusivo (kubectl cp de 100 GB) |
| Window bytes | 256 KiB | `KUBEBOLT_AGENT_TUNNEL_WINDOW_BYTES` | Ver §0.8 |

**Audit logging** (siempre on, no configurable): cada tunnel open/close
emite una línea `slog.Info` con `event=tunnel_open|tunnel_close,
tenant_id, cluster_id, user_id, target_pod, namespace, container, bytes_in,
bytes_out, duration_ms`. Sin esto, compliance es imposible (SOC2 requiere
audit trail de quién accedió a qué pod cuándo).

**Métricas Prometheus** (Sprint C polish — backend ya tiene scaffolding
para métricas en commit ENTERPRISE-CANDIDATE):
- `kubebolt_tunnel_active_total{cluster_id, kind}` — gauge
- `kubebolt_tunnel_bytes_total{cluster_id, direction}` — counter
- `kubebolt_tunnel_window_saturated_total{cluster_id}` — counter (cuántas veces el sender pausó por falta de credits)
- `kubebolt_tunnel_idle_closes_total` — counter

**ENTERPRISE-CANDIDATE**: per-tenant rate limiting (max-tunnels y max-bps
diferentes por plan free/team/enterprise) — el algoritmo OSS, las
políticas SaaS.

---

### 0.10 — Transport del AgentChannel: **gRPC bidi** (revisitable)

> **Decisión** (2026-05-04): seguimos con gRPC bidi como único transport.
> WebSocket queda como migración futura, NO se construye dual-transport
> ahora. La decisión apuesta a un mercado target dominado por clusters
> cloud-native (EKS/GKE/AKS) con egress directo, no enterprise/regulated
> con proxies de inspección HTTP/2.

**Contexto del bloqueante** (verificado in-vivo el 2026-04-30 con EKS prod
`yagan-eks-prod-v2` → Cloudflare Tunnel → backend local):

- Channel registra ✅, ingest de métricas funciona ✅, probes unarios
  (SelfSubjectAccessReview) funcionan ✅.
- Cache sync con 22 watches concurrentes **rompe a los ~2s** con
  `channel: agent closed` cuando hay un proxy HTTP/2 en el medio.
- Causas: HTTP/2 idle timeouts en CDN edges (~100s), reset cycles
  (~10 min), flow control saturado en cache sync, y inspección DPI en
  proxies enterprise (Zscaler / Netskope / Forcepoint / PaloAlto)
  matando streams long-lived sistemáticamente.

**Por qué gRPC bidi solo, ahora:**

| Razón | Detalle |
|---|---|
| Mercado target del MVP | Clusters cloud-native con egress directo — gRPC bidi funciona limpio acá. |
| Premature optimization evitable | Construir dual-transport para enterprise sin cliente enterprise paying es trabajo sin payoff. |
| Costo real de coexistencia | 2x test matrix, 2x debug surface, drift de bugs en la rama under-tested, auto-fallback es difícil de hacer correctamente. |
| Ningún competidor ship dual | Robusta + Komodor + Devtron son WS-only; Pixie + NewRelic + ArgoCD-agent son gRPC-only. Cada uno picó uno. |

**Por qué NO WebSocket todavía:**

- ~3-4 días de refactor del transport en server + agent. El wire protocol
  queda igual (los mensajes no cambian); cambia el carrier.
- Sin demanda concreta de cliente — el primer trigger sería un cliente
  paying bloqueado por su Zscaler.

**Triggers para revisitar (= migrar a WS-only, no dual):**

1. ≥1 cliente paying bloqueado por proxy corporativo de inspección.
2. Pivot estratégico hacia regulated industries (banca, salud, gobierno).
3. Feedback consistente de evaluación perdida por incompatibilidad de red.

**Lo que documentamos al usuario:**

- Compatible con clusters en cloud (AWS/GCP/Azure) con egress directo.
- Para clusters detrás de proxy corporativo con DPI o on-prem con
  inspección HTTP/2 → contactar / use OSS self-hosted (backend en la
  misma red que los clusters).
- Tailscale (P2P WireGuard) sirve para tests puntuales, sidesteps el
  proxy entero.

**Futureproofing barato (lo hacemos ahora):**

- En el `Hello` del proto agregar campo `transport_version` (u opcional
  `negotiated_features` map) así cuando WS llegue, no es breaking change
  agregar handshake nuevo. ~5 min de trabajo, paga si en algún momento
  se necesita.

**Lo que NO hacemos:**

- Auto-fallback gRPC→WS por detección de patrones de falla. Difícil de
  hacer correctamente, oscurece la causa real cuando falla, y cuando lo
  vayamos a necesitar de verdad va a ser un switch consciente de toda la
  flota, no per-conexión.

> *Memoria de contexto*: ver `~/.claude/.../memory/project_cf_tunnel_ruled_out.md`
> y `project_agent_transport_market_pattern.md` para los detalles del análisis
> y los patrones de mercado.

---

## 1. Wire protocol — `AgentChannel` proto

### 1.1 Service definition

```protobuf
// proto/kubebolt/agent/v2/channel.proto
syntax = "proto3";

package kubebolt.agent.v2;

import "google/protobuf/timestamp.proto";
import "kubebolt/agent/v1/agent.proto";  // re-uses Sample, MetricBatch, etc.

option go_package = "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2;agentv2";

// AgentChannel is the single bidi stream the agent maintains with the
// backend for the lifetime of its session. All traffic — metrics push,
// heartbeat, K8s API proxy, control commands — multiplexes here.
service AgentChannel {
  rpc Channel(stream AgentMessage) returns (stream BackendMessage);
}
```

### 1.2 Wire messages

```protobuf
message AgentMessage {
  // Optional. Set on responses to correlate with the BackendMessage that
  // triggered them (kube_response, watch_event). Empty for unsolicited
  // messages (heartbeat, metrics, watch events for an open stream).
  string request_id = 1;

  oneof kind {
    Hello hello = 2;
    Heartbeat heartbeat = 3;
    kubebolt.agent.v1.MetricBatch metrics = 4;
    KubeProxyResponse kube_response = 5;
    KubeProxyWatchEvent kube_event = 6;
    StreamClosed stream_closed = 7;
  }
}

message BackendMessage {
  string request_id = 1;

  oneof kind {
    Welcome welcome = 2;
    HeartbeatAck heartbeat_ack = 3;
    KubeProxyRequest kube_request = 4;
    ConfigUpdate config_update = 5;
    Disconnect disconnect = 6;
  }
}

// Hello / Welcome — handshake (replaces v1 Register).
message Hello {
  string node_name = 1;
  string agent_version = 2;
  string container_runtime = 3;
  string kernel_version = 4;
  string cluster_hint = 5;            // best-effort cluster_id from agent
  repeated string capabilities = 6;   // ["metrics", "kube-proxy"]
  map<string, string> labels = 7;     // curated node labels
}

message Welcome {
  string agent_id = 1;        // sha256(tenant|cluster|node)[:16] in Sprint A
  string cluster_id = 2;      // canonical
  kubebolt.agent.v1.AgentConfig config = 3;
}

// KubeProxyRequest — backend asks the agent to perform an HTTP call
// against its local apiserver.
message KubeProxyRequest {
  string method = 1;          // GET / POST / PATCH / PUT / DELETE
  string path = 2;            // /api/v1/namespaces/foo/pods/bar
  map<string, string> headers = 3;  // Accept, Content-Type, etc.
  bytes body = 4;             // JSON body for non-GET
  bool watch = 5;             // if true, opens a long-lived stream
  uint32 timeout_seconds = 6; // bound non-watch calls
}

message KubeProxyResponse {
  uint32 status_code = 1;
  map<string, string> headers = 2;
  bytes body = 3;
  string error = 4;           // network / serialization errors (not HTTP errors)
}

// KubeProxyWatchEvent is one event in a watch stream. Stream closes
// when KubeProxyResponse arrives with the same request_id (terminal),
// or StreamClosed arrives.
message KubeProxyWatchEvent {
  string event_type = 1;      // ADDED / MODIFIED / DELETED / BOOKMARK / ERROR
  bytes object = 2;           // raw apiserver event JSON
}

message StreamClosed {
  string reason = 1;          // "client_done", "server_disconnect", "buffer_overflow"
}

// Disconnect — backend asks agent to drop the channel and reconnect.
// Used when the backend redeploys, agent is being moved to a different
// shard, or token rotation requires re-authentication.
message Disconnect {
  string reason = 1;
  uint32 reconnect_after_seconds = 2;  // 0 = immediate
}

message Heartbeat {
  google.protobuf.Timestamp sent_at = 1;
  kubebolt.agent.v1.AgentHealthStats stats = 2;
}

message HeartbeatAck {
  google.protobuf.Timestamp received_at = 1;
}

message ConfigUpdate {
  kubebolt.agent.v1.AgentConfig config = 1;
}
```

### 1.3 Lifecycle

```
agent boot
  ↓
gRPC dial + Sprint A auth (bearer / tokenreview / mTLS)
  ↓
agent → backend: AgentMessage{Hello}
  ↓
backend authenticates → backend → agent: BackendMessage{Welcome, agent_id, cluster_id}
  ↓
loop {
  agent reads BackendMessage
  switch kind:
    kube_request:
      execute via local apiserver, send KubeProxyResponse
      (or open watch + stream KubeProxyWatchEvent + final Response/StreamClosed)
    config_update: apply
    disconnect: drain in-flight, close stream, reconnect after delay
  agent writes AgentMessage:
    metrics (push)
    heartbeat (every 30s)
    kube_response / kube_event (correlated to backend's request)
}
```

### 1.4 Correlation rules

- `request_id`: UUID v4 hex (32 chars) generated by the **initiator** (backend for kube_request, agent for own messages).
- Server-streaming RPCs (watch): all `kube_event` messages share the request_id of the originating `kube_request`. Stream ends with either a final `kube_response` (errored or completed) or a `stream_closed`.
- Heartbeat / metrics: no request_id.
- Bidi gRPC handles ordering per stream — no need for sequence numbers within a single watch.

---

## 2. Backend-side — multiplexor + transport + registry

### 2.1 New package layout

```
apps/api/internal/agent/
├── server.go                    # existing — AgentIngest v1, kept for migration
├── auth_interceptor.go          # existing
├── tls_config.go                # existing
├── authenticator_factory.go     # existing
├── channel/                     # NEW (Sprint A.5)
│   ├── server.go                # AgentChannel impl, dispatch
│   ├── multiplexor.go           # in-memory map of pending request_ids → reply chans
│   ├── registry.go              # AgentRegistry: cluster_id → live channel
│   └── transport.go             # AgentProxyTransport (http.RoundTripper)
└── proxy/                       # NEW
    └── watcher.go               # backend-side watch.Interface adapter
```

### 2.2 `AgentRegistry`

Owns the lifecycle of every connected agent.

```go
package channel

type AgentRegistry struct {
    mu      sync.RWMutex
    agents  map[string]*Agent  // keyed by cluster_id
}

type Agent struct {
    ClusterID  string
    AgentID    string
    Identity   *auth.AgentIdentity   // from interceptor
    Connected  time.Time

    // Channel inbox / outbox tied to the gRPC stream.
    incoming   chan *agentv2.AgentMessage   // server reads from here
    outgoing   chan *agentv2.BackendMessage // server writes from here
    pending    *Multiplexor                  // request_id → reply chan

    closeFn    func()                        // signal stream goroutines to exit
}

// Register inserts or replaces (on reconnect) the agent for cluster_id.
// Returns the Agent reference and an "evicted" channel of any pre-existing
// agent for that cluster_id — caller should call evicted.Close() to clean
// up its goroutines.
func (r *AgentRegistry) Register(cluster_id string, a *Agent) (*Agent, *Agent)

// Get returns the live agent for cluster_id, or nil if not connected.
func (r *AgentRegistry) Get(cluster_id string) *Agent

// Unregister removes the agent. Idempotent.
func (r *AgentRegistry) Unregister(cluster_id string)

// List returns a snapshot of currently connected agents.
func (r *AgentRegistry) List() []AgentSummary
```

### 2.3 `Multiplexor`

Per-agent map of in-flight request_ids → reply channels. Owns request_id correlation.

```go
package channel

type Multiplexor struct {
    mu      sync.Mutex
    pending map[string]chan *agentv2.AgentMessage
    // For watch streams: each event lands on the same chan; the consumer
    // closes the chan via Cancel() when done.
}

// Send writes a BackendMessage and returns a chan that receives correlated
// AgentMessages. For unary (non-watch), the chan delivers exactly one
// message and is closed. For watch (request.watch=true), the chan delivers
// every kube_event until the stream terminates.
func (m *Multiplexor) Send(req *agentv2.BackendMessage, watch bool) (<-chan *agentv2.AgentMessage, error)

// Deliver routes an incoming AgentMessage to the corresponding pending chan.
// Called from the gRPC server goroutine that drains agent → backend traffic.
func (m *Multiplexor) Deliver(msg *agentv2.AgentMessage)

// Cancel closes the chan for request_id and cleans up. Called by the
// transport on context cancellation.
func (m *Multiplexor) Cancel(request_id string)
```

### 2.4 `AgentProxyTransport`

The bridge between client-go and the agent channel. **Implements `http.RoundTripper`** — that's all client-go needs.

```go
package channel

type AgentProxyTransport struct {
    clusterID string
    registry  *AgentRegistry
}

func (t *AgentProxyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
    agent := t.registry.Get(t.clusterID)
    if agent == nil {
        return nil, fmt.Errorf("agent for cluster %s is not connected", t.clusterID)
    }

    isWatch := req.URL.Query().Get("watch") == "true"
    backendMsg := &agentv2.BackendMessage{
        RequestId: uuid.NewString(),
        Kind: &agentv2.BackendMessage_KubeRequest{
            KubeRequest: serializeRequest(req),
        },
    }

    replies, err := agent.pending.Send(backendMsg, isWatch)
    if err != nil {
        return nil, err
    }

    if isWatch {
        return buildWatchResponse(replies, req.Context()), nil
    }

    select {
    case msg := <-replies:
        return deserializeResponse(msg.GetKubeResponse()), nil
    case <-req.Context().Done():
        agent.pending.Cancel(backendMsg.RequestId)
        return nil, req.Context().Err()
    case <-time.After(timeoutFromRequest(req)):
        agent.pending.Cancel(backendMsg.RequestId)
        return nil, fmt.Errorf("agent proxy timeout")
    }
}
```

### 2.5 Watch adapter

When client-go calls `Watch()` it returns a `watch.Interface`. Our transport returns an `*http.Response` with a `Body` whose `Read()` produces newline-delimited JSON. client-go's `restclient.Watch` wraps that into a `watch.Interface`. **No changes to client-go side** — the transport just has to produce the right wire format.

```go
package channel

// buildWatchResponse turns a chan of agentv2.AgentMessage (kube_event)
// into an http.Response whose Body streams newline-delimited JSON in
// the format client-go expects.
func buildWatchResponse(replies <-chan *agentv2.AgentMessage, ctx context.Context) *http.Response {
    pr, pw := io.Pipe()
    go func() {
        defer pw.Close()
        for {
            select {
            case msg, ok := <-replies:
                if !ok {
                    return
                }
                ev := msg.GetKubeEvent()
                if ev == nil {
                    return  // got a final kube_response or stream_closed
                }
                // client-go expects {"type":"...","object":<raw>}
                fmt.Fprintf(pw, `{"type":%q,"object":`, ev.EventType)
                pw.Write(ev.Object)
                pw.Write([]byte("}\n"))
            case <-ctx.Done():
                return
            }
        }
    }()
    return &http.Response{
        StatusCode: 200,
        Header:     http.Header{"Content-Type": []string{"application/json"}},
        Body:       pr,
    }
}
```

### 2.6 `ClusterAccess` factory

Modify `cluster.Manager` to support the dual mode.

```go
package cluster

type ClusterAccess struct {
    ID    string
    Name  string
    Mode  string  // "local" | "agent-proxy"

    // mode=local
    KubeconfigContext string

    // mode=agent-proxy
    AgentRegistry *channel.AgentRegistry
}

// RestConfig returns a *rest.Config with the right Transport. client-go
// downstream sees no difference.
func (a *ClusterAccess) RestConfig() (*rest.Config, error) {
    switch a.Mode {
    case "local":
        return cluster.NewConnectorForContext(...).RestConfig(), nil
    case "agent-proxy":
        return &rest.Config{
            Host: fmt.Sprintf("https://%s.agent.local", a.ID),
            Transport: &channel.AgentProxyTransport{
                clusterID: a.ID,
                registry:  a.AgentRegistry,
            },
            Timeout: 30 * time.Second,
        }, nil
    }
    return nil, fmt.Errorf("unknown mode %q", a.Mode)
}
```

The `cluster.Manager` gains:
- A new constructor variant for agent-proxy clusters.
- `AddAgentProxyCluster(clusterID, name)` called when an agent registers (if `agentIngest.autoRegisterClusters=true`).
- `RemoveAgentProxyCluster(clusterID)` for explicit removal.
- The existing `Connector` works **unchanged** — `newConnectorFromConfig(restConfig, ...)` doesn't care if the Transport is real or proxied.

---

## 3. Agent-side — multiplexor + kube proxy

### 3.1 New package layout

```
packages/agent/internal/
├── shipper/                # existing — gets v2 protocol upgrade
├── channel/                # NEW
│   ├── client.go           # AgentChannel client, run loop
│   └── multiplexor.go      # routes BackendMessage → handlers
└── proxy/                  # NEW
    └── kube_proxy.go       # HandleRequest + HandleWatch
```

### 3.2 `KubeAPIProxy`

Tiny wrapper around the agent's local in-cluster kube client.

```go
package proxy

type KubeAPIProxy struct {
    client    kubernetes.Interface
    transport http.RoundTripper  // == client.RESTClient().Transport
    baseURL   string             // == rest.Config.Host
}

func New(cfg *rest.Config) (*KubeAPIProxy, error) {
    transport, err := rest.TransportFor(cfg)
    if err != nil { return nil, err }
    return &KubeAPIProxy{
        client:    must(kubernetes.NewForConfig(cfg)),
        transport: transport,
        baseURL:   cfg.Host,
    }, nil
}

// HandleRequest executes a unary kube call and returns the response.
func (p *KubeAPIProxy) HandleRequest(ctx context.Context, req *agentv2.KubeProxyRequest) *agentv2.KubeProxyResponse {
    httpReq, _ := http.NewRequestWithContext(ctx, req.Method, p.baseURL+req.Path, bytes.NewReader(req.Body))
    for k, v := range req.Headers { httpReq.Header.Set(k, v) }
    resp, err := p.transport.RoundTrip(httpReq)
    if err != nil {
        return &agentv2.KubeProxyResponse{Error: err.Error()}
    }
    body, _ := io.ReadAll(resp.Body)
    resp.Body.Close()
    return &agentv2.KubeProxyResponse{
        StatusCode: uint32(resp.StatusCode),
        Headers:    flattenHeaders(resp.Header),
        Body:       body,
    }
}

// HandleWatch opens a watch stream and emits events on the returned chan
// until the context cancels or the apiserver closes the connection.
func (p *KubeAPIProxy) HandleWatch(ctx context.Context, req *agentv2.KubeProxyRequest) (<-chan *agentv2.KubeProxyWatchEvent, error) {
    httpReq, _ := http.NewRequestWithContext(ctx, "GET", p.baseURL+req.Path, nil)
    resp, err := p.transport.RoundTrip(httpReq)
    if err != nil { return nil, err }
    if resp.StatusCode >= 400 {
        return nil, fmt.Errorf("watch failed: status %d", resp.StatusCode)
    }
    out := make(chan *agentv2.KubeProxyWatchEvent, 64)
    go func() {
        defer close(out)
        defer resp.Body.Close()
        dec := json.NewDecoder(resp.Body)
        for dec.More() {
            var raw struct {
                Type   string          `json:"type"`
                Object json.RawMessage `json:"object"`
            }
            if err := dec.Decode(&raw); err != nil { return }
            select {
            case out <- &agentv2.KubeProxyWatchEvent{EventType: raw.Type, Object: raw.Object}:
            case <-ctx.Done():
                return
            }
        }
    }()
    return out, nil
}
```

### 3.3 Channel client

Single goroutine reads `BackendMessage`, dispatches via multiplexor. Single goroutine writes `AgentMessage` from the outgoing chan.

```go
package channel

type Client struct {
    stream  agentv2.AgentChannel_ChannelClient
    proxy   *proxy.KubeAPIProxy
    pending sync.Map              // request_id → cancelFn (for inbound watches)
    out     chan *agentv2.AgentMessage
}

func (c *Client) Run(ctx context.Context) error {
    go c.writeLoop(ctx)
    return c.readLoop(ctx)
}

func (c *Client) readLoop(ctx context.Context) error {
    for {
        msg, err := c.stream.Recv()
        if err != nil { return err }
        switch k := msg.Kind.(type) {
        case *agentv2.BackendMessage_KubeRequest:
            go c.handleKubeRequest(ctx, msg.RequestId, k.KubeRequest)
        case *agentv2.BackendMessage_HeartbeatAck:
            // track liveness
        case *agentv2.BackendMessage_Disconnect:
            return fmt.Errorf("backend asked to disconnect: %s", k.Disconnect.Reason)
        case *agentv2.BackendMessage_ConfigUpdate:
            c.applyConfig(k.ConfigUpdate)
        }
    }
}
```

---

## 4. Migration v1 → v2: flip duro

Decisión §0.1 — el agente todavía no está publicado fuera de dev local, así
que no hay fleets externos que romper. El plan de migración es trivial:

| Componente | Acción |
|---|---|
| `proto/kubebolt/agent/v1/agent.proto` | **eliminado** |
| `proto/kubebolt/agent/v2/channel.proto` | **nuevo**, contiene service `AgentChannel` + todos los messages que antes vivían en v1 (Sample, MetricBatch, AgentConfig, AgentHealthStats) |
| `apps/api/internal/agent/server.go` | reemplazado por `apps/api/internal/agent/channel/server.go` |
| `packages/agent/internal/shipper/shipper.go` | reescrito contra el canal v2; preserva la auth + TLS de Sprint A |
| Generated bindings v1 | borrados |

**Riesgo residual**: cualquiera con un agente local corriendo en su laptop al
momento del upgrade (vos) tiene que rebuild + redeploy del agente. El backend
viejo no acepta agentes v2 y viceversa. El primer commit del sprint lo deja
explícito en el message + README.

---

## 5. Plan de tests

### 5.1 Unit tests

| File | What it tests |
|---|---|
| `apps/api/internal/agent/channel/multiplexor_test.go` | Send/Deliver correlation, watch delivers multiple events, Cancel cleans up, concurrent Send |
| `apps/api/internal/agent/channel/registry_test.go` | Register/Get/Unregister, eviction on reconnect, List snapshot consistency |
| `apps/api/internal/agent/channel/transport_test.go` | RoundTrip happy path with stub registry, agent-not-connected returns clear error, watch stream emits events, ctx cancel propagates Cancel |
| `apps/api/internal/agent/proxy/watcher_test.go` | client-go's restclient.Watch can decode our response body |
| `packages/agent/internal/proxy/kube_proxy_test.go` | HandleRequest with mock RoundTripper, HandleWatch decodes JSON stream, ctx cancel stops the goroutine |
| `packages/agent/internal/channel/client_test.go` | readLoop dispatches by kind, handleKubeRequest writes to outgoing, Disconnect terminates Run |

### 5.2 Integration tests (bufconn)

`apps/api/internal/agent/channel/e2e_test.go`:

```
spawn AgentChannel server with stub kube backend
spawn agent channel client
agent sends Hello → backend replies Welcome
backend issues KubeProxyRequest "GET /api/v1/pods" → agent replies KubeProxyResponse with 200 + body
backend issues KubeProxyRequest watch=true → agent emits 3 events → backend cancels → agent stops
agent sends MetricsBatch → backend acks
agent sends Heartbeat → backend acks
backend sends Disconnect → agent closes cleanly
```

### 5.3 client-go integration

The smoking gun. Connect a real `kubernetes.NewForConfig(cfg)` where `cfg.Transport == AgentProxyTransport` and confirm:

```
clientset.CoreV1().Pods("default").Get(ctx, "x", GetOptions{})
clientset.CoreV1().Pods("default").List(ctx, ListOptions{})
clientset.AppsV1().Deployments("default").Patch(ctx, "x", strategic-merge-patch, ...)
clientset.CoreV1().Pods("default").Watch(ctx, ListOptions{Watch: true})  // emits events
```

Test fixture: `tests/e2e/sprint-a5-proxy/`. Uses kind cluster + fake agent that wraps the cluster's apiserver.

### 5.4 Multi-cluster e2e

`tests/e2e/sprint-a5-multi-cluster.sh`:

```
Spin 2 kind clusters: kbdev-a, kbdev-b
helm install kubebolt in kbdev-a (control plane)
helm install kubebolt-agent in kbdev-a with proxy.enabled=false  → mode=local
helm install kubebolt-agent in kbdev-b with proxy.enabled=true   → mode=agent-proxy
   (backendUrl points at kbdev-a's exposed Service)
Verify:
  - Backend lists 2 clusters (kbdev-a as local, kbdev-b as agent-proxy)
  - kubectl-proxy handler for kbdev-b returns same data as kbdev-b's apiserver
  - Restart action in UI on kbdev-b actually restarts a pod on kbdev-b
  - Watch events from kbdev-b appear in real-time topology
```

### 5.5 Performance + fault injection

| Scenario | Target |
|---|---|
| 1000 watch events/s sustained | <200ms p99 latency vs direct apiserver |
| 100 agents simultaneously connected | <50MB RSS overhead per agent in backend |
| Agent disconnect mid-watch | client-go reflector re-lists + recovers (acceptable per K8s norms) |
| Backend restart with 100 agents | All reconnect within 10s, in-flight requests fail with retryable error |
| Network partition (agent unreachable) | All in-flight requests fail with timeout, registry marks as disconnected, restoration on reconnect |
| Slow agent (responses delayed 5s) | Backend handlers respect ctx timeout, no leaks |

`-race` for everything; concurrency under load is the highest-risk surface here.

### 5.6 Acceptance criteria mapping

| Criterion (from sprint plan) | Test |
|---|---|
| `kubectl get/apply` via proxy = direct | §5.3 client-go integration |
| Watch <200ms latency | §5.5 performance |
| handleRestart/Scale/Rollback/Delete via proxy → cluster cambia | §5.4 multi-cluster e2e |
| blast_radius via proxy ≡ directo | §5.4 multi-cluster e2e |
| Multi-cluster: 1 local + 2 agent-proxy | §5.4 multi-cluster e2e |
| Connection pool 100 agentes sin degradación | §5.5 performance |
| OSS single-cluster con `proxy.enabled=false`: directo | §5.4 implícito (kbdev-a) |
| Audit log distingue cluster | unit + multi-cluster e2e log assertion |

---

## 6. Branch + commit plan

Branch: `feat/agent-kube-proxy` (off `main` after Sprint A merge).

Sequenced commits, each compiling + green tests:

| # | Commit | Líneas (est.) | Notes |
|---|--------|---|---|
| 1 | `feat(proto)!: AgentChannel v2 — replaces AgentIngest v1` | 350 | proto v2 + generated bindings + delete v1 + update server.go to register the v2 service. **BREAKING**: v1 agents no longer connect after this commit. |
| 2 | `feat(agent-channel): backend AgentRegistry + Multiplexor` | 600 | registry, multiplexor, Hello/Welcome handshake, Heartbeat + Metrics handling on v2 |
| 3 | `feat(agent-channel): agent-side Channel client` | 500 | dispatcher, writer/reader loops, shipper rewrite against v2 |
| 4 | `feat(agent): KubeAPIProxy implementation` | 400 | HandleRequest + HandleWatch — agent side only, exercised via tests |
| 5 | ✅ `feat(api): AgentProxyTransport + watch adapter` | 600 | the http.RoundTripper that bridges client-go to the channel |
| 6 | ✅ `feat(cluster): ClusterAccess factory + Mode=local\|agent-proxy` | 400 | manager.go refactor |
| 7 | ✅ `feat(cluster): auto-register agent-proxy clusters (opt-in)` | 250 | Helm value gate |
| 7a | ✅ `feat(agent): forward cluster name to backend via Hello.Labels` | 65 | post-smoke: agent ships KUBEBOLT_AGENT_CLUSTER_NAME so backend uses friendly displayName |
| 7b | ✅ `fix(agent): suffix " (via agent)" para disambiguar dropdown` | 60 | post-smoke: avoid name collision when kubeconfig + agent reach same cluster |
| 7c | ✅ `feat(agent): operator-tier RBAC manifest` | 150 | dual ClusterRole, opt-in (apply via `make agent-rbac-operator`) |
| 8a | ✅ `docs(sprint-a5): include SPDY tunneling — design` | 200 | §0.7-§0.9 doc additions |
| 8b | ✅ `feat(proto)!: KubeStreamData + KubeStreamAck for upgrade tunnels` | 80 | proto + buf regen |
| 8c | ✅ `feat(agent-channel): tunnel slot mode in Multiplexor` | 350 | SlotTunnel with overflow→close (loss-intolerant) |
| 8d | ✅ `feat(api-channel): hijackable conn + Upgrade detection` | 400 | AgentProxyTransport detects upgrade, TunnelConn implements net.Conn |
| 8e | ✅ `feat(agent-proxy): SPDY upgrade handler dialing apiserver` | 300 | agent dial + bidi byte pump with credit window |
| 8e+ | ✅ `fix(api): wire AgentProxyTransport into exec/files/portforward SPDY` | 105 | mid-sprint: handlers use cluster.SPDYTransportsFor instead of standard spdy.RoundTripperFor |
| 8e+ | ✅ `fix(agent-proxy): preserve Connection/Upgrade headers on upgrade` | 60 | post-smoke: don't strip them as hop-by-hop when isUpgradeRequest |
| **8e+** | ✅ **`fix(agent-proxy): make SPDY tunnel actually work end-to-end`** | **400** | **6-bug fix chain — see §6.5 Smoke-test postmortem** |
| 8e+ | ✅ `chore(agent-proxy): CancelRequest no-op` | 12 | silence client-go warning on every cancellation |
| 8f | ⏳ `feat(api-proxy): tunnel limits + idle-timeout + audit logging` | 350 | §0.9 hardening — **required before SaaS productivo** |
| 8g | ⏳ `feat(api-proxy): tunnel Prometheus metrics` | 150 | observability — **required before SaaS productivo** |
| 8h | ⏳ `test(integration): pod exec via fake apiserver` | 400 | real SPDY round-trip; replaces removed legacy HandleUpgrade tests |
| 9 | ⏳ `feat(api-helm): proxy.enabled value + backend wiring` | 100 | helm template polish |
| 10 | ⏳ `feat(agent-helm): proxy.enabled + rbac.mode values` | 120 | helm template (incluye operator RBAC del 7c) |
| 11 | ⏳ `test(integration): client-go calls via proxy` | 300 | the smoking-gun test (REST path) |
| 12 | ⏳ `test(e2e): multi-cluster sprint A.5 (REST + SPDY)` | 500 | bash + manifests + exec scenario |

**Total real al cerrar core funcional**: 25 commits, ~6300 LOC + 2200 de tests.

**Estado**: el core funcional está cerrado en `feat/agent-kube-proxy`. Las operaciones
REST + watch + exec + portforward + files vía agent-proxy están **verificadas end-to-end
contra un cluster kind real** (smoke test, sesión exec con `v4.channel.k8s.io` negociado,
6 KiB+ output fluyendo). Lo que queda (8f-12) es hardening, helm packaging y test
automation — obligatorio antes de SaaS productivo, opcional para uso self-hosted
single-tenant en inner-dev loop.

### 6.5 Smoke-test postmortem (commits 8e+)

El último commit grande del core (`fix(agent-proxy): make SPDY tunnel actually work
end-to-end`, ~400 LOC) consolida una cadena de 6 bugs encontrados durante smoke testing
en vivo. Vale documentarlos porque cada uno es lección para futuros tunnel-style
features:

**1. Multi-value HTTP headers serializados como string única**.
Nuestro proto `KubeProxyRequest.headers` es `map<string,string>` — un slot por nombre.
K8s' SPDY exec manda `X-Stream-Protocol-Version` con N entradas (una por protocolo
soportado). El backend hacía `strings.Join(values, ", ")` para meter en el map. El
agent recibía la string joined, hacía `Header.Set` con un único valor, apiserver leía
`req.Header[X-Stream-Protocol-Version]` esperando `[]string` con cada protocolo
distinto y no encontraba match exacto → **400 Bad Request**.
*Fix*: agent splittea por `,` para upgrade requests y hace `Header.Add` por cada parte.
*Lesson*: HTTP semántico requiere multi-value para algunos headers — un proto
`map<string,string>` es lossy. Considerar `map<string, StringList>` en futura iteración
si aparecen más casos.

**2. Hop-by-hop strip eliminaba `Connection` y `Upgrade` siempre**.
`buildRequest` del agent stripeaba ambos headers como hop-by-hop por RFC 7230. Para
upgrade requests esos son **el contenido principal** que el apiserver inspecciona —
no son hop-by-hop en este contexto. Stripeados → apiserver veía `Upgrade: SPDY/3.1`
en aislamiento → 400.
*Fix*: excepción cuando `isUpgradeRequest(req)` es true.

**3. Backend's agentProxyUpgrader no inyectaba `Connection`/`Upgrade`**.
El SPDY library estándar de K8s (`spdy.SpdyRoundTripper.RoundTrip`) clona el request
internamente y `header.Add(Connection, Upgrade)` antes de dialear. Como nosotros
reemplazamos ese RoundTripper con nuestro `agentProxyUpgrader`, asumimos esa
responsabilidad. Sin esa adición, `AgentProxyTransport.RoundTrip` veía un POST sin
upgrade headers → ruteaba como **unary** → agent forwardeaba sin upgrade → apiserver 400.
*Fix*: clonar request y `Header.Set` ambos antes de delegar.

**4. HTTP/2 transport rechaza `Upgrade` headers**.
K8s apiservers modernos negocian HTTP/2 vía ALPN. La implementación HTTP/2 de Go
responde con `http2: invalid Upgrade request header: ["SPDY/3.1"]` — HTTP/2 no tiene
mecanismo de protocol upgrade. El SPDY library de K8s evita esto **no usando
`http.Transport`** — dialea TCP+TLS directamente y escribe el HTTP/1.1 manual.
*Fix*: agent's `HandleUpgrade` reemplaza `p.transport.RoundTrip` con `tls.Dial` +
`httpReq.Write(conn)` + `http.ReadResponse(bufio.NewReader(conn))` + `bufConn` wrapper
que mantiene los bytes post-headers que pueda haber piggy-back-eado el apiserver en el
packet del 101.
*Lesson*: HTTP/1.1 upgrade y HTTP/2 son incompatibles a nivel de wire. Si el server
soporta ambos, hay que forzar 1.1 explícito vía `tls.Config.NextProtos = ["http/1.1"]`
o (más robusto) bypass total del http.Transport para upgrades.

**5. `defer resp.Body.Close()` de Negotiate cerraba el túnel**.
K8s' `spdy.Negotiate` (que llama `executor.StreamWithContext` indirectamente) hace
`defer resp.Body.Close()` justo después del upgrade. Si nuestro `resp.Body` ES la
`TunnelConn` directamente, ese defer **terminaba el túnel ~microsegundos después del
101**, antes que SPDY framing comenzara su propio handshake. El agent recibía
`KubeStreamData{eof:true}` instantáneo y `pumpToApiserver` salía con `bytes_received=0`.
*Fix*: nuevo type `TunnelHandshakeBody` que envuelve `TunnelConn`. Tiene método
`Extract()` que el upgrader llama antes que la SPDY library wrap-ee la conn; tras
`Extract`, `Body.Close()` es no-op (la SPDY conn ahora es dueña del lifecycle del
`TunnelConn`).

**6. El backend no enrutaba `KubeStreamData`/`Ack` al Multiplexor**.
La switch case en `apps/api/internal/agent/server.go` solo manejaba `KubeResponse`,
`KubeEvent`, `StreamClosed` — heredada de antes que añadiéramos las dos variantes
nuevas en commit 8b. Los `KubeStreamData` del agent caían al default y se
descartaban silenciosamente. El `TunnelConn.Read` del backend bloqueaba para siempre
esperando bytes que sí llegaban a la gRPC stream pero nunca al slot del Multiplexor.
*Fix*: añadir las dos variantes a la switch case (literalmente 2 líneas).
*Lesson*: cuando se extiende un protocolo bidireccional, **TODOS los extremos** (no
solo el que origina los nuevos mensajes) tienen que reconocerlos. Tests unitarios por
componente no captan esto — necesita test integration que ejerza el ciclo completo
(commits 8h y 11 son justo para eso).

**Workflow takeaway**: cada bug agregaba ~3 minutos de iteración (rebuild image →
load into kind → rolling DaemonSet → re-test). Total ~1h de iteraciones serie para
encontrar los 6 bugs. Hubiéramos tardado la mitad si hubiéramos instrumentado los
puntos críticos del túnel (Read/Write/demuxLoop, headers in/out) **antes** de empezar
a iterar — lección guardada en memoria del usuario para futuros sprints de protocolo.

Eliminados respecto al borrador original:
- Migrar Heartbeat/Metrics como commit aparte → ya forma parte del commit 1+2 (flip duro).
- Deprecation warning de v1 → no aplica (no hay fleets externos a deprecar).

---

## 7. Riesgos + mitigaciones

| Riesgo | Mitigación |
|---|---|
| Watch latency > 1s degrada UX en SaaS | Bench § 5.5; gRPC frames sin re-serialización; el hot path sólo pasa bytes |
| 100 agentes × 50 watches = 5000 streams concurrentes — leaks de goroutines | Transport siempre llama `Multiplexor.Cancel(req_id)` en `defer`; tests con `-race` y goroutine count |
| Race en correlation por `request_id` | `sync.Map` + tests de concurrency 100x bajo `-race` |
| Migration window: viejo + nuevo coexisten una release | Backend acepta ambos; agentes flaggean su versión; auditoría tagged |
| Watch missed events tras reconnect | client-go reflector re-list es la spec — documentar como límite honesto, NO intentar replay |
| `cluster.Manager` ya tiene 632 líneas, refactor riesgoso | Sprint A.5 NO refactoriza el `Connector`; solo extiende el path de creación de `rest.Config` |

---

## 8. Lo que NO entra en Sprint A.5

- **Cluster discovery vía agent registration en SaaS** — Sprint A.5 deja `autoRegisterClusters` opt-in. El `agents` bucket persistente queda para Sprint B.
- **Métricas detalladas del proxy** (latencia per-call, error rate per-cluster) — quedan como `slog.Info` lines en A.5; Prometheus metrics en Sprint C.
- **Agent → Agent comunicación** (ej. flow data sharing) — fuera de scope.
- ~~**Proxy de exec / port-forward / WebSocket**~~ → **scope-in mid-sprint**, ver §0.7-§0.9 + commits 8a-8h. SaaS productivo no es viable sin estos paths interactivos vía agent-proxy.
- **Encriptación por-mensaje** sobre el canal ya autenticado — fuera de scope; mTLS a nivel transport ya existe (Sprint A).
- **Per-tenant rate limiting de tunnels** (max-tunnels-por-plan, max-bps-por-plan diferenciado free/team/enterprise) — el algoritmo OSS, las políticas SaaS. ENTERPRISE-CANDIDATE.
- **Sharding del AgentRegistry para horizontal scaling** — un solo backend instance maneja ~500 customers; más allá se shardea en Sprint C+.
- **Tunnel session resume** después de reconnect — sesión exec rota = sesión perdida, igual que kubectl exec con red caída. No intentamos replay.

---

## 9. Resumen de decisiones (para quien retome el sprint)

| # | Tema | Decisión |
|---|---|---|
| §0.1 | Migración v1→v2 | **Flip duro** — v2 reemplaza v1, no coexistencia, no flag |
| §0.2 | Backpressure en watch | **Close stream** — client-go reflector hace re-list |
| §0.3 | Cluster auto-discovery | **Helm value `autoRegisterClusters`, default false** |
| §0.4 | Capacity target | **100 agentes / ~5k watches concurrentes** |
| §0.5 | Arquitectura | **Monolito** — proxy dentro del binario actual |
| §0.6 | Pseudoendpoint REST | **`https://<cluster_id>.agent.local`** |
| §0.7 | SPDY/WebSocket tunneling | **opaque byte-tunnel** — `KubeStreamData{data, eof}` opcional en oneof |
| §0.8 | Backpressure en tunnels | **credit-based flow control** — `KubeStreamAck`, ventana 256 KiB |
| §0.9 | Tunnel hardening | **defaults conservadores** + audit logging always-on + Prom metrics |
| extra | Deprecation v1 | **No aplica** — agente no publicado externamente, flip duro alcanza |

**Estado actual** (core funcional cerrado): commits 1-8e + serie de fixes post-smoke
test ✅ en `feat/agent-kube-proxy` (25 commits ahead de `develop`). Pod terminal,
files, portforward, plus todas las operaciones REST del Sprint A.5 verificadas en
vivo contra kind-kubebolt-dev. Branch listo para merge a `develop` antes de seguir
con hardening.

**Próximos pasos** (post-merge, en este orden):
1. Commit 8f — hardening (idle timeout, max duration, max-per-agent, bps cap, audit log)
2. Commit 8g — Prometheus metrics (active_tunnels, bytes_total, window_saturated)
3. Commit 8h — integration test (pod exec via fake apiserver)
4. Commits 9, 10 — helm chart wiring para api + agent (proxy.enabled + rbac.mode)
5. Commits 11, 12 — integration tests (REST smoking gun) + e2e multi-cluster

8f + 8g son bloqueadores de SaaS productivo. Los demás son polish y test automation.
