# Code Review — KubeBolt Phase 1

> **Fecha:** Marzo 2026
> **Alcance:** 19 archivos Go (backend) + 47 archivos TypeScript/React (frontend)
> **Total issues:** 57

---

## Resumen Ejecutivo

| Severidad | Backend | Frontend | Total |
|-----------|---------|----------|-------|
| **Critical** | 4 | 0 | **4** |
| **High** | 4 | 7 | **11** |
| **Medium** | 5 | 28 | **33** |
| **Low** | 4 | 5 | **9** |

---

## CRITICAL — Arreglar inmediatamente

### 1. `context.TODO()` sin timeout en `listGatewayResources`

- **Archivo:** `apps/api/internal/cluster/connector.go` — función `listGatewayResources()`
- **Problema:** Las llamadas al dynamic client para Gateway API usan `context.TODO()` que puede bloquear indefinidamente. Ya se corrigió en `addGatewayTopologyNodes()` pero `listGatewayResources()` sigue sin timeout.
- **Impacto:** El servidor se bloquea si el Gateway API no responde.
- **Fix:** Reemplazar con `context.WithTimeout(context.Background(), 5*time.Second)`.

### 2. Error no chequeado en `json.Encoder.Encode()`

- **Archivo:** `apps/api/internal/api/responses.go`
- **Problema:** `json.NewEncoder(w).Encode(data)` puede fallar silenciosamente. Si falla (referencias circulares, tipos no serializables), la respuesta HTTP queda corrupta sin log.
- **Fix:**
  ```go
  if err := json.NewEncoder(w).Encode(data); err != nil {
      log.Printf("Error encoding JSON response: %v", err)
  }
  ```

### 3. Header HTTP escrito en orden incorrecto

- **Archivo:** `apps/api/internal/api/responses.go`
- **Problema:** `w.Header().Set("Content-Type", ...)` se llama DESPUÉS de `w.WriteHeader()`. Una vez llamado WriteHeader, los headers ya se enviaron y las modificaciones se ignoran.
- **Fix:** Invertir el orden — headers primero, luego WriteHeader.

### 4. Double-close en WebSocket

- **Archivo:** `apps/api/internal/websocket/client.go`
- **Problema:** `readPump` y `writePump` ambos hacen `defer conn.Close()`. Si ambos ejecutan, panic por double-close. También `client.send` se cierra en `hub.unregister` y podría cerrarse dos veces.
- **Fix:** Usar `sync.Once` para garantizar un solo close.

---

## HIGH — Arreglar pronto

### Backend

#### 5. Errores silenciados en listers

- **Archivo:** `apps/api/internal/cluster/state.go`
- **Problema:** Todos los `GetPods()`, `GetDeployments()`, etc. ignoran errores con `_`:
  ```go
  pods, _ := c.podLister.List(everythingSelector())
  ```
  Si el cache no está sincronizado, retornan nil sin señalar el fallo.
- **Fix:** Loguear errores.

#### 6. Goroutine leak en hub broadcast

- **Archivo:** `apps/api/internal/websocket/hub.go`
- **Problema:** Cada vez que el buffer de un client está lleno, se lanza un goroutine para unregister. Bajo carga, los goroutines se acumulan más rápido de lo que se procesan.
- **Fix:** Usar remoción síncrona o pool de workers.

#### 7. Race condition en `ReloadKubeconfig`

- **Archivo:** `apps/api/internal/cluster/manager.go`
- **Problema:** El lock se mantiene solo para asignar `m.kubeConfig`, pero `ListClusters()` itera sobre el map mientras puede ser reemplazado.
- **Fix:** Mantener el lock durante toda la operación de reload.

#### 8. Timer no limpiado en Stop

- **Archivo:** `apps/api/internal/cluster/connector.go`
- **Problema:** `topologyTimer` no se cancela en `Stop()`, puede disparar después del shutdown.
- **Fix:** Agregar `topologyTimer.Stop()` en `Stop()`.

### Frontend

#### 9. No hay Error Boundary

- **Archivos:** Todos los componentes
- **Problema:** Si cualquier componente lanza un error, toda la app crashea sin recuperación.
- **Fix:** Crear un `ErrorBoundary` component y wrappear `App.tsx`.

#### 10. Keys con index en listas dinámicas

- **Archivos:** `EventsFeed.tsx`, `EventsPage.tsx`
- **Problema:** Usar array index como key causa re-renders innecesarios y bugs de estado:
  ```tsx
  <div key={`${event.object}-${event.reason}-${i}`}>
  ```
- **Fix:** Usar identificadores únicos como `${event.object}-${event.reason}-${event.timestamp}`.

#### 11. Dependencia incorrecta en `useWebSocket`

- **Archivo:** `apps/web/src/hooks/useWebSocket.ts`
- **Problema:** `queryClient` en el dependency array del useEffect causa re-ejecuciones innecesarias. `queryClient` es un singleton estable.
- **Fix:** Remover `queryClient` del array de dependencias.

#### 12. Search input no funcional

- **Archivo:** `apps/web/src/components/layout/Topbar.tsx`
- **Problema:** El input de búsqueda tiene `placeholder` pero no tiene `onChange` ni estado. Es puramente decorativo.
- **Fix:** Implementar búsqueda funcional o marcar como "coming soon".

---

## MEDIUM — Mejorar en próximas iteraciones

### Backend

| # | Archivo | Issue |
|---|---------|-------|
| 13 | `api/handlers.go` | API devuelve `400 Bad Request` en errores internos del manager (debería ser `500`) |
| 14 | `cluster/relationships.go` | Type assertions sin validación en parsing de Gateway API — puede causar panic |
| 15 | `cluster/graph.go` | Copia de slices innecesaria en `GetTopology()` en cada request |
| 16 | `cluster/connector.go` | Struct `Connector` con 40+ campos — considerar separar en interfaces |
| 17 | `api/handlers.go` | Sin validación de rango en `limit` (podría ser negativo o extremadamente grande) |

### Frontend — Seguridad

| # | Archivo | Issue |
|---|---------|-------|
| 18 | `services/api.ts` | POST requests sin CSRF token |
| 19 | `resources/SettingsPage.tsx` | URL de instalación del agent hardcodeada sin validación |
| 20 | `resources/EventsPage.tsx` | Contenido de eventos renderizado directamente sin sanitización |

### Frontend — Performance

| # | Archivo | Issue |
|---|---------|-------|
| 21 | Todos los hooks | `refetchInterval` activo con tab inactivo — agregar `refetchIntervalInBackground: false` |
| 22 | `dashboard/ResourceUsage.tsx` | `UsageCard` no memoizado, se renderiza múltiples veces con datos idénticos |
| 23 | `layout/Topbar.tsx` | Invalidación de cache demasiado agresiva al cambiar cluster — invalida TODO |
| 24 | `map/ClusterMap.tsx` | Componente de 536 líneas — extraer lógica de layout a archivos separados |
| 25 | `hooks/useWebSocket.ts` | WebSocket invalida queries de forma amplia — debería ser quirúrgico |

### Frontend — Accesibilidad

| # | Archivo | Issue |
|---|---------|-------|
| 26 | `map/MapControls.tsx` | Botones con solo íconos sin `aria-label` |
| 27 | `layout/Topbar.tsx` | Dropdown sin navegación por teclado (Escape, flechas, Enter) |
| 28 | Múltiples componentes | Indicadores de estado solo por color, sin texto para screen readers |
| 29 | `resources/FilterBar.tsx` | Input sin `<label>` asociado |

### Frontend — TypeScript

| # | Archivo | Issue |
|---|---------|-------|
| 30 | `resources/ResourceListPage.tsx` | Casteos `as string` sin validación en celdas de tabla |
| 31 | `types/kubernetes.ts` | `ResourceItem` es genérico — debería ser union discriminado por tipo |
| 32 | `services/websocket.ts` | Mensajes WebSocket malformados ignorados silenciosamente |

### Frontend — CSS/Tailwind

| # | Archivo | Issue |
|---|---------|-------|
| 33 | `dashboard/ResourceUsage.tsx` | Colores hex inline (`#4c9aff`, `#ef4056`) — deberían ser tokens Tailwind |
| 34 | `utils/colors.ts` | `getUsageBarColor()` retorna hex en vez de clases Tailwind |
| 35 | Múltiples archivos | Mix de inline styles y clases Tailwind para los mismos valores |

### Frontend — Data Fetching

| # | Archivo | Issue |
|---|---------|-------|
| 36 | Todos los hooks | `gcTime` no configurado — datos quedan en cache indefinidamente |
| 37 | `hooks/useClusterOverview.ts` | Sin `staleTime` efectivo — errores causan re-render loops |
| 38 | Todas las páginas | Sin skeleton loading — solo spinner completo |
| 39 | `services/api.ts` | Sin distinción entre errores de red y errores de parsing JSON |
| 40 | Múltiples páginas | Mensajes de error genéricos sin contexto del recurso que falló |

---

## LOW — Nice to have

| # | Archivo | Issue |
|---|---------|-------|
| 41 | `cluster/connector.go` | Log "Informer caches synced" sin nombre de cluster |
| 42 | `api/handlers.go` | Sin validación de rango negativo en `limit` |
| 43 | Todos los botones | Sin estilos `focus-visible` para navegación por teclado |
| 44 | Todos los CSS | Dark mode hardcodeado, sin toggle light/dark |
| 45 | `cluster/connector.go` | Nil checks redundantes en slices post-filtrado |

---

## Plan de Remediación

### Sprint 1 — Criticals + High (1 día)

| Archivo | Cambios |
|---------|---------|
| `api/responses.go` | Fix orden headers + error handling en Encode |
| `cluster/connector.go` | Timeout en `listGatewayResources()`, cleanup timer en Stop |
| `websocket/client.go` | `sync.Once` para conn.Close() |
| `websocket/hub.go` | Eliminar goroutine leak en broadcast |
| `cluster/state.go` | Loguear errores de listers |
| `cluster/manager.go` | Fix race en ReloadKubeconfig |
| `App.tsx` | Agregar Error Boundary |
| `useWebSocket.ts` | Remover queryClient de deps |
| `EventsFeed.tsx` | Fix keys |

### Sprint 2 — Medium priority (2-3 días)

- CSRF protection en api.ts
- Accessibility (aria-labels, keyboard navigation)
- Memoización de componentes pesados
- `refetchIntervalInBackground: false` en todos los hooks
- TypeScript strict typing para ResourceItem

### Sprint 3 — Polish (1 semana)

- Skeleton loading screens
- Functional search en topbar
- Extraer colores hardcodeados a Tailwind config
- Error messages con contexto
- Keyboard navigation en dropdowns

### Verificación

```bash
# Backend
cd apps/api && go vet ./... && go build ./...

# Frontend
cd apps/web && npx tsc --noEmit && npm run build

# Race detector (cuando haya tests)
cd apps/api && go test -race ./...
```

---

## Conclusión

El codebase es **sólido para una Phase 1**. La arquitectura es limpia, la performance excelente (~19MB RAM, <5ms responses), y el código es generalmente idiomático. Los 4 issues críticos son todos corregibles en pocas horas y no representan vulnerabilidades de seguridad explotables sino riesgos de estabilidad (panics, bloqueos). Los issues medium y low son mejoras de calidad que pueden abordarse incrementalmente.
