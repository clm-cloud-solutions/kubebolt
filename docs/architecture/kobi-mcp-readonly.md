# Diseño: Kobi MCP (read-only, multi-tenant)

**Estado:** Propuesta / feasibility
**Alcance v1:** Solo lectura. Sin mutaciones (`propose_*` excluidas).
**Objetivo:** Multi-tenant (SaaS/EE) — un backend sirve muchos `(tenant, cluster)`.
**Autor:** evaluación de factibilidad, branch `claude/kobi-mcp-feasibility-vPSZS`
**Base de código revisada:** rama `develop`

---

## 1. Motivación

Hoy las capacidades de Kobi (copilot) solo se consumen desde el panel de chat del
frontend de KubeBolt. La meta es exponer esas mismas capacidades —catálogo de
herramientas de diagnóstico/observabilidad sobre el cluster— como un **servidor
MCP (Model Context Protocol)** para que cualquier host MCP las use:

- **Claude Code / Cursor** — un operador investiga su cluster desde el IDE/CLI.
- **Pipelines CI/CD** — un step de pipeline pregunta "¿está sano el deploy?".
- **Otros agentes/orquestadores** que hablen MCP.

En todos esos canales **el LLM lo pone el cliente** (Claude Code trae su propio
modelo). Por eso el MCP de Kobi NO necesita provider, system prompt, loop de
chat, compactación ni usage tracking: solo necesita exponer **herramientas** y
**ejecutarlas** contra el cluster correcto.

## 2. Por qué es factible (lo que ya existe)

La implementación actual de Kobi ya separó —sin proponérselo— las dos mitades
que un MCP necesita distinguir:

| Mitad "host LLM" (NO se usa en MCP)        | Mitad "tools + ejecución" (REUTILIZAR)         |
|--------------------------------------------|------------------------------------------------|
| `provider.go`, `anthropic.go`, `openai.go` | `tools.go` — definiciones en JSON Schema       |
| `prompt.go` (system prompt, 31 KB)         | `executor.go` — dispatcher puro                |
| `compact.go`, `budget.go`, `usage.go`      | `workload_metrics_executor.go`, `describe.go`  |
| `conversations.go`, `title.go`             | `kubebolt_docs.go`                             |
| `api/copilot.go` (chat loop + SSE)         | `proposals.go` (solo relevante en fase 2)      |

Tres hechos clave del código actual:

1. **Las tools ya están en el formato de MCP.** `ToolDefinition{Name, Description,
   InputSchema}` (`internal/copilot/types.go`) es 1:1 con `tools/list` de MCP. Las
   descripciones son ricas (incluyen lógica de decisión) y se reutilizan tal cual.

2. **La ejecución ya está desacoplada del LLM y del HTTP.**
   `Executor.Execute(call ToolCall) ToolResult` (`internal/copilot/executor.go`)
   solo depende de `*cluster.Manager`. Cita del propio código: *"Tool execution
   is internal to the backend — no HTTP round-trip."*

3. **Ya existe gobernanza de tools.** `GovernedToolDefinitions(actionsEnabled,
   destructiveEnabled)` filtra el catálogo. Con `actionsEnabled=false` se omiten
   automáticamente las 9 herramientas `propose_*` → **el filtro read-only ya está
   escrito**.

## 3. El catálogo read-only (v1)

`GovernedToolDefinitions(false, false)` deja exactamente estas herramientas de
lectura (las `propose_*` se excluyen por el prefijo):

| Tool                    | Qué devuelve |
|-------------------------|--------------|
| `get_cluster_overview`  | Resumen del cluster: counts, CPU/mem, health, eventos |
| `list_resources`        | Lista paginada por tipo, con filtros |
| `get_resource_detail`   | Detalle + métricas en vivo |
| `get_resource_yaml`     | YAML crudo (secrets redactados) |
| `get_resource_describe` | `kubectl describe` |
| `get_pod_logs`          | Logs de contenedor (con grep/ventanas de tiempo/previous) |
| `get_workload_pods`     | Pods de un workload |
| `get_workload_history`  | Historial de revisiones |
| `get_cronjob_jobs`      | Jobs hijos de un CronJob |
| `get_topology`          | Grafo de topología |
| `get_insights`          | Insights activos con severidad |
| `get_events`            | Eventos de Kubernetes |
| `search_resources`      | Búsqueda global por nombre |
| `get_permissions`       | RBAC detectado para la conexión |
| `list_clusters`         | Contextos disponibles |
| `get_workload_metrics`  | Series CPU/mem/red (min/avg/max/p95 + sparkline) |
| `get_kubebolt_docs`     | Documentación del producto |

Las `propose_*` (restart, scale, delete, rollback, set_image, set_env,
set_resources, patch_hpa, debug_pod) quedan **fuera de v1**. Ver §8 (fase 2).

## 4. Arquitectura propuesta

```
                       ┌───────────────────────────────────────────┐
   Claude Code /        │  KubeBolt API (proceso Go ya existente)    │
   Cursor / CI ──MCP──▶ │                                            │
   (trae su LLM)        │  internal/mcp/   (NUEVO, transporte)       │
                        │     ├─ server.go      tools/list, call     │
                        │     ├─ http.go        Streamable HTTP @/mcp │
                        │     ├─ stdio.go       subcomando `mcp`      │
                        │     └─ auth.go        token → RuntimeKey    │
                        │            │                                │
                        │            ▼  cluster.WithRuntimeKey(ctx)   │
                        │  internal/copilot/                          │
                        │     ├─ tools.go     ◀── REUTILIZADO          │
                        │     └─ executor.go  ◀── REUTILIZADO          │
                        │            │                                │
                        │            ▼  manager.Connector(ctx)        │
                        │  internal/cluster/  connector pool          │
                        │     poolKey{tenant, cluster} → runtime      │
                        └───────────────────────────────────────────┘
```

**Principio rector:** el paquete `internal/mcp/` es **solo transporte + auth +
routing de tenant**. Toda la lógica de cluster se delega al `Executor` existente.
No se duplica ni una llamada al connector.

### 4.1 Transportes

Dos transportes sobre el mismo `internal/mcp` core:

- **Streamable HTTP** montado en el router Chi como `/mcp` (o `/api/v1/mcp`).
  Es el transporte natural para SaaS multi-tenant: el backend ya es un servidor
  de larga vida y la auth ya es por-request. Reusa el middleware de auth.
- **stdio** vía subcomando `kubebolt mcp` para Claude Code/Cursor locales y CI.
  En SaaS este modo apunta al endpoint HTTP remoto (proxy stdio↔HTTP) o se usa
  solo en self-hosted single-tenant.

> Para el objetivo **multi-tenant SaaS**, el transporte primario es **Streamable
> HTTP**. El stdio se documenta pero es secundario.

### 4.2 SDK

No hay dependencia MCP en `go.mod` hoy. Usar el SDK oficial de Go
(`github.com/modelcontextprotocol/go-sdk`). Go 1.25 ya está en uso → sin fricción
de toolchain. Alternativa: `github.com/mark3labs/mcp-go` (más maduro a la fecha).
Decisión de implementación, no de diseño.

## 5. Multi-tenancy: el punto central de este diseño

Aquí está el trabajo real para el objetivo SaaS/EE. La buena noticia: **el
mecanismo de routing por-tenant ya existe en `develop`** y el MCP solo tiene que
alimentarlo correctamente.

### 5.1 El seam que ya existe

`develop` ya tiene un **connector pool** keyed por `poolKey{tenant, cluster}`
(`internal/cluster/manager.go`). El routing se hace por `context`:

```go
// internal/cluster/runtime_key.go (ya en develop)
type RuntimeKey struct {
    Tenant  string
    Cluster string // "" → active context (OSS)
}
func WithRuntimeKey(ctx, key) context.Context
func RuntimeKeyFromContext(ctx) RuntimeKey

// manager.go
func (m *Manager) Connector(ctx context.Context) *Connector {
    rt := m.resolveRuntime(ctx) // lee RuntimeKeyFromContext(ctx)
    ...
}
```

Es decir: **quien tenga un `ctx` con el `RuntimeKey` correcto obtiene el
connector del `(tenant, cluster)` correcto, sin tocar nada más.**

### 5.2 El gap a cerrar (único cambio de código fuera de `internal/mcp`)

El `Executor` actual **descarta** el contexto del request y hardcodea
`context.Background()`:

```go
// internal/copilot/executor.go (HOY)
func (e *Executor) Execute(call ToolCall) ToolResult {
    // context.Background(): el executor no tiene request ctx, así que
    // resuelve a default-tenant + active-cluster (correcto en OSS).
    conn := e.manager.Connector(context.Background())
    ...
}
```

El propio comentario lo marca como deuda: *"EE (Fase B) threads the real
request ctx into Execute for per-tenant scoping."* **El MCP multi-tenant es
precisamente ese Fase B.**

**Cambio requerido (pequeño, retrocompatible):**

```go
// Nueva firma que acepta ctx; la vieja se mantiene como shim.
func (e *Executor) ExecuteCtx(ctx context.Context, call ToolCall) ToolResult {
    conn := e.manager.Connector(ctx) // ctx lleva el RuntimeKey
    ...
}
func (e *Executor) Execute(call ToolCall) ToolResult { // compat
    return e.ExecuteCtx(context.Background(), call)
}
```

El chat loop actual (`api/copilot.go`) sigue llamando `Execute` (sin cambios);
el MCP llama `ExecuteCtx` con un ctx enriquecido. **Cero regresión** para el
panel de chat existente.

> Nota: para que el routing per-tenant sea realista, el loop de chat HTTP
> existente también debería migrar a `ExecuteCtx` con el ctx del request en
> algún momento; pero eso es independiente del MCP y puede ir después.

### 5.3 De identidad MCP a `RuntimeKey`

El paso multi-tenant crítico: **cómo el MCP sabe qué `(tenant, cluster)` sirve
cada llamada.** Flujo propuesto para el transporte HTTP:

1. El cliente MCP autentica con un **token de servicio/PAT** (ver §6). El token
   resuelve a una identidad con `TenantID` (la auth de agentes ya modela
   `AgentIdentity{TenantID}`; reutilizar el mismo store de tenants).
2. El **cluster** objetivo se determina por:
   - un parámetro explícito en cada tool-call (`cluster` opcional añadido al
     schema), **o**
   - el header/sesión MCP (p. ej. un `cluster_id` en la inicialización de la
     sesión MCP), **o**
   - `list_clusters` + una tool `select_cluster` a nivel de sesión.
3. El middleware del MCP construye
   `ctx = cluster.WithRuntimeKey(reqCtx, RuntimeKey{Tenant: id.TenantID,
   Cluster: clusterID})` y lo pasa a `ExecuteCtx`.

**Recomendación v1:** cluster por-sesión MCP (se fija en `initialize`), no
por-llamada — más simple para el host y suficiente para el caso "un operador
investiga un cluster". Multi-cluster en una sola sesión → fase posterior.

### 5.4 Aislamiento entre tenants

El connector pool ya garantiza que cada `(tenant, cluster)` tiene su propio
runtime (informers, cache, RBAC del SA de ese cluster). Mientras el MCP
**siempre** derive el `Tenant` del token autenticado (nunca de un parámetro que
el cliente controle), un tenant no puede leer otro. **Regla de seguridad dura:
el `Tenant` del `RuntimeKey` viene SIEMPRE de la identidad del token, jamás de
un argumento de la tool-call.** El `Cluster` sí puede venir de parámetro, pero
se valida contra los clusters visibles para ese tenant.

## 6. Autenticación y autorización

- **Transporte HTTP:** token bearer por-request. Reutilizar el modelo de tenants
  existente (`internal/auth`, store de tenants en BoltDB). Opciones:
  - PAT/service-token emitido desde Admin (nuevo, pequeño): `kb_mcp_…` con scope
    de tenant + (opcional) lista de clusters.
  - Reusar JWT de usuario si el host puede portarlo (menos práctico para CI).
- **RBAC del cluster:** la lectura ya corre bajo el SA del connector de ese
  cluster (probing de permisos en `cluster/permissions.go`). `get_permissions`
  expone qué puede ver. El MCP **no** eleva privilegios: si el SA no lista
  secrets, la tool devuelve `forbidden` igual que en el panel.
- **Read-only por construcción:** v1 no expone `propose_*` ni endpoints de
  mutación. No hay ruta de escritura que proteger.
- **Rate limiting / cuotas:** recomendable por-tenant en el handler HTTP del MCP
  (las tools de logs/topology pueden ser pesadas). Reusar límites ya presentes
  en el executor (`maxToolResultBytes`, `maxLogBytes`, etc.) que acotan el
  tamaño de respuesta.

## 7. MCP prompts y resources (opcional, alto valor)

MCP no es solo `tools`. Dos primitivas extra que encajan:

- **`prompts/list`** — exponer la guía/persona de Kobi (vive en
  `internal/copilot/prompts/` y `prompt.go`) como un MCP *prompt* que Claude Code
  puede cargar. Así el host LLM hereda parte del "saber operar" de Kobi sin que
  Kobi tenga que ser el LLM. **Recomendado para v1** porque recupera el valor del
  system prompt que de otro modo se pierde.
- **`resources/list`** — opcional; podría exponer `get_kubebolt_docs` como
  resources navegables en vez de tool. Baja prioridad.

## 8. Fuera de alcance (fases siguientes)

- **Fase 2 — escritura human-in-the-loop.** Exponer `propose_*` + una tool
  `execute_proposal`, usando *elicitation* de MCP para la confirmación. Requiere
  **portar a Go** la traducción "propuesta → endpoint de mutación" que hoy vive
  **solo en el frontend** (`ActionProposalCard.tsx` → `runProposal`, que mapea
  cada acción a `api.scaleResource`, `api.restartResource`, etc.). Hoy no existe
  esa capa en el backend; el chat loop nunca ejecuta, solo devuelve la propuesta.
- **Fase 3 — multi-cluster en una sesión** (cluster por-llamada con validación).
- **Webhooks/streaming push** (que el MCP notifique insights nuevos).

## 9. Riesgos y mitigaciones

| Riesgo | Mitigación |
|--------|------------|
| El MCP filtra datos entre tenants | `Tenant` SIEMPRE del token, nunca de argumento (§5.4). Test de aislamiento obligatorio. |
| El loop de chat existente se rompe al refactorizar `Execute` | Mantener `Execute()` como shim sobre `ExecuteCtx()`; cero cambios en `api/copilot.go`. |
| Respuestas enormes (logs/topology) saturan el host LLM | Reusar caps del executor (`maxToolResultBytes`, `maxLogBytes`). |
| Drift entre descripciones de tools y comportamiento real | Las tools ya son la única fuente; el MCP las consume sin copiarlas. |
| Tenant sin connector conectado (agent-proxy) | Devolver el mismo 503/"waiting for agent" que el resto del API; documentar como estado normal. |
| Coste/abuso desde CI | Rate limit por-token + cuotas por-tenant en el handler MCP. |

## 10. Estimación de esfuerzo (v1 read-only, multi-tenant)

| Trabajo | Tamaño |
|---------|--------|
| Refactor `Executor.Execute` → `ExecuteCtx(ctx,…)` + shim | XS |
| Paquete `internal/mcp` (server core + tools/list + tools/call sobre el SDK) | M |
| Transporte HTTP `/mcp` + middleware auth → `RuntimeKey` | M |
| Emisión de service-tokens MCP (Admin) | S–M |
| Selección de cluster por-sesión + validación contra tenant | S |
| MCP prompts (guía de Kobi) | S |
| Subcomando stdio `kubebolt mcp` | S |
| Tests (aislamiento de tenant, tools/list paridad, golden de tool-call) | M |

**Ruta crítica:** el refactor `ExecuteCtx` + el wiring `token → RuntimeKey →
ctx`. Todo lo demás es transporte estándar del SDK MCP. Ninguna lógica de
cluster se reescribe.

## 11. Decisiones abiertas

1. SDK MCP: oficial `go-sdk` vs `mark3labs/mcp-go`.
2. Cluster: por-sesión (recomendado v1) vs por-llamada.
3. Token MCP: PAT nuevo dedicado vs reutilizar JWT de usuario.
4. Ruta: `/mcp` vs `/api/v1/mcp` (afecta al proxy nginx del web container).
5. ¿Exponer la guía de Kobi como MCP prompt en v1? (recomendado: sí).
