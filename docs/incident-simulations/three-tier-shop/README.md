# KubeShop — app de 3 capas con fallos en cascada

Una tienda e-commerce de demostración partida en **tres capas**, cada una en su
propio namespace, con una base de datos que vive **fuera del cluster**. Está
diseñada para mostrar cómo un solo fallo (la BD) **se propaga en cascada** hacia
arriba a través de healthchecks que validan dependencias — y cómo Kobi razona
sobre la causa-raíz recorriendo la topología.

## Arquitectura

```
   ┌──────────────────────── cluster ────────────────────────┐
   │                                                          │
   │  ns: shop-frontend          ns: shop-backend             │
   │  ┌───────────┐    HTTP   ┌──────────────┐                │
   │  │ loadgen   │──────────▶│  shop-web    │                │
   │  └───────────┘           │  (storefront)│                │
   │                          └──────┬───────┘                │
   │                       HTTP /healthz │  GET /              │
   │                          ┌──────────┴─────────┐          │
   │                          ▼                    ▼          │
   │                   ┌────────────┐       ┌────────────┐    │
   │                   │ orders-api │       │ catalog-api│    │
   │                   └──────┬─────┘       └──────┬─────┘    │
   │                          │ TCP :5432          │          │
   │  ns: shop-data           ▼                    ▼          │
   │                   ┌──────────────────────────────┐       │
   │                   │ Service shop-db (sin selector)│       │
   │                   │ Endpoints → IP externa        │       │
   │                   └───────────────┬──────────────┘       │
   └───────────────────────────────────┼──────────────────────┘
                                        │ TCP :5432
                            ┌───────────▼────────────┐
                            │  postgres (docker run) │   ← FUERA del cluster
                            │  red docker `kind`      │
                            └────────────────────────┘
```

- **shop-frontend** — `shop-web` (storefront HTML que en cada request llama a las
  dos APIs) + `loadgen` (genera tráfico constante).
- **shop-backend** — `orders-api` y `catalog-api`, dos microservicios idénticos
  (mismo servidor, distinto `SERVICE_NAME`) que dependen de la BD.
- **shop-data** — un `Service` **sin selector** + `Endpoints` manuales que apuntan
  a la IP del contenedor de postgres externo. Así el cluster "ve" la BD como un
  Service normal aunque corra afuera.

Todo es `python:3.12-alpine` + un script embebido en un ConfigMap. Sin imágenes
custom.

## El modelo de healthchecks (lo importante)

Cada workload tiene **liveness Y readiness**, y cumplen roles distintos a
propósito:

| Capa | liveness `/livez` | readiness `/healthz` | Si la dependencia cae |
|------|-------------------|----------------------|------------------------|
| **backend** (`orders-api`, `catalog-api`) | proceso vivo (siempre 200) | **abre socket TCP a `shop-db:5432`** | `NotReady` → sale de los Endpoints (deja de recibir tráfico) |
| **frontend** (`shop-web`) | proceso vivo (siempre 200) | **self-contained (siempre 200)** | **sigue Ready**; la home `/` devuelve **503** al usuario |

Dos decisiones de diseño, ambas a propósito:

- **El backend SÍ sale de rotación** cuando pierde la BD: un backend sin base de
  datos genuinamente no puede servir, así que retirarlo del Service es lo correcto.
  Liveness no falla (no entra en loop de reinicios), solo readiness.
- **El frontend NO sale de rotación** cuando pierde el backend: una tienda con un
  backend degradado debería seguir respondiendo al usuario — con un **error 5xx**,
  no desaparecer. Por eso `shop-web` mantiene su readiness self-contained y su home
  devuelve `503` cuando `orders-api` no está disponible. Es lo más realista y, de
  paso, **lo que hace que el fallo se vea como tráfico de error (5xx) en el panel
  Reliability** en lugar de como pods que se esfuman.

## Logs

Los tres servicios emiten logs por request con nivel y latencia, pensados para
que Kobi (`get_pod_logs`) tenga material real de RCA y para verlos en el viewer de
logs de KubeBolt. Patrón: `<timestamp> [<servicio>] <NIVEL> <mensaje>`.

| Servicio | Sano | Durante el incidente |
|----------|------|----------------------|
| `orders-api` / `catalog-api` | `INFO GET / -> 200 db=ok 1ms` | `WARN readiness failed: cannot reach database shop-db…:5432 (timed out)` |
| `shop-web` | `INFO GET / -> 200 orders=up catalog=up 9ms` | `ERROR GET / -> 503 orders-api unavailable (…)` |

Decisiones para que el log cuente la historia sin ruido:
- Los probes sanos (`/livez`, `/healthz` OK) **no** se loguean.
- El backend loguea `/healthz` **solo cuando falla** → durante un corte de BD su
  log se llena de `WARN readiness failed … cannot reach database`, que es la señal
  de causa raíz que Kobi lee directo.
- El front loguea cada `/` con el resultado de sus upstreams (`orders=up/DOWN`,
  `catalog=up/DOWN`), así se distingue un fallo total (503) de uno degradado (200
  con catálogo caído).

```bash
kubectl logs -n shop-backend  -l app=orders-api --prefix -f
kubectl logs -n shop-frontend -l app=shop-web   --prefix -f
```

## Puesta en marcha

> Requiere un cluster **kind** (red docker `kind`) por defecto. Para otros
> entornos, ver [Portabilidad](#portabilidad).

```bash
cd docs/incident-simulations/three-tier-shop

# 1. Levanta la BD externa (postgres en docker, red kind)
database/run-db.sh

# 2. Despliega las 3 capas y conéctalas a la BD
./apply.sh

# 3. Verifica que todo está Ready
kubectl get pods -n shop-backend
kubectl get pods -n shop-frontend
```

El `loadgen` debería estar logueando `shop-web -> 200`:

```bash
kubectl logs -n shop-frontend deploy/loadgen -f
```

## La cascada 💥

```bash
database/stop-db.sh      # docker stop shop-db
```

En orden, en ~10–15s:

1. **BD ↓** — el contenedor de postgres se detiene.
2. **backend NotReady** — `orders-api`/`catalog-api` `/healthz` no conecta a
   `:5432` → 503 → readiness falla → los pods salen de sus Endpoints.
3. **Services backend sin endpoints** — el insight `service-no-endpoints` se
   dispara para `orders-api` y `catalog-api`.
4. **frontend sirve errores** — `shop-web` **sigue Ready** (no se cae). Su home
   `/` intenta llamar a `orders-api`, que ya no tiene endpoints, y devuelve **503**
   al usuario.
5. **loadgen ve 503** — la tienda sigue respondiendo pero con error; el usuario
   final ve "503 — no podemos cargar la tienda" en vez de una conexión muerta.

Esto es lo que hace la simulación útil para demos de **error rate**: como el front
no desaparece, el 503 viaja como tráfico HTTP real y se ve en Hubble / el panel
Reliability (bucket 5xx / server_err), no como pods ausentes.

Obsérvalo:

```bash
kubectl get pods -A -l app.kubernetes.io/part-of=kubeshop -w   # backend NotReady, shop-web sigue Ready
kubectl logs -n shop-frontend deploy/loadgen -f                 # 200 -> 503
# L7 del error (cluster Cilium):
kubectl exec -n kube-system ds/cilium -c cilium-agent -- \
  hubble observe --protocol http --namespace shop-frontend --last 10   # GET / -> 503
```

### Qué probar con Kobi

Con la cascada activa, pídele a Kobi (en la UI de KubeBolt):

- *"la tienda está devolviendo 503, ¿cuál es la causa raíz?"* — debería recorrer
  la topología hacia abajo (shop-web → orders-api → shop-db) y señalar la BD, no
  los síntomas de arriba.
- *"¿por qué orders-api está NotReady?"* — debería leer el `/healthz`/eventos y
  apuntar a la conexión a la BD.
- Revisa **Insights** y **Topology** en KubeBolt para ver los 2 services backend
  sin endpoints y el grafo de dependencias cross-namespace, y el panel
  **Reliability** para el pico de 5xx en `shop-web`.

## Recuperación

```bash
database/start-db.sh     # docker start shop-db
```

La recuperación sube por la misma cadena, también en ~10–15s, y **sin reiniciar
ningún pod** (la liveness nunca se disparó). El `loadgen` vuelve a `200`.

## Caídas de una sola API (fault injection)

La caída de BD tumba **las dos** APIs a la vez. Para simular que **una sola API
falla** —y distinguir fallos de app de fallos de infra— cada backend tiene un
`FAULT_MODE` que inyectas sin tocar la BD ni la otra API:

```bash
database/fault-api.sh <orders|catalog> <error|unready|slow>   # inyectar
database/heal-api.sh  <orders|catalog>                        # sanar
```

| Modo | Qué hace la API | Qué ve el front | Insight / señal | Lección de RCA |
|------|-----------------|-----------------|-----------------|----------------|
| `error` | sigue **Ready** (readiness verde, con endpoints) pero `GET /` → **500** | `503 orders-api returned 500` | NO hay insight de readiness; el pico de 5xx vive en Reliability + en los logs | El caso difícil: **readiness verde ≠ API sana**. Hay que mirar respuestas/logs, no solo readiness. |
| `unready` | readiness falla con la **BD sana** → sale de endpoints | `503 orders-api unreachable` | `service-no-endpoints` (orders-api), `zero-replicas` si caen todas | Se distingue de la cascada de BD porque **catalog-api sigue Ready** → la BD está bien; el problema es orders-api. |
| `slow` | `GET /` duerme `FAULT_LATENCY_MS` (3s) | timeout (2s) → `503 unreachable` + latencia alta | latencia en Reliability | Lento ≠ caído; la latencia es la pista. |

**Criticidad por capa (ya en el código del front):**
- **orders-api** es la dependencia **crítica** → si falla en cualquier modo, el front devuelve **503**.
- **catalog-api** es **no-crítica** → si falla, el front se queda en **200 degradado**
  (`WARN GET / -> 200 orders=up catalog=DOWN`), el sitio sigue arriba. Demo de
  *graceful degradation*.

Otras caídas con kubectl puro (sin `FAULT_MODE`):
- **API a 0 réplicas:** `kubectl -n shop-backend scale deploy/orders-api --replicas=0`
  → `service-no-endpoints` + `zero-replicas` → Kobi propone `scale_workload`.
- **Imagen rota:** `kubectl -n shop-backend set image deploy/orders-api app=python:nope`
  → `ImagePullBackOff` → Kobi propone `set_image` / `rollback`.

### Qué probar con Kobi (caídas de API)

- `fault-api.sh orders error` → *"orders-api está Ready pero la tienda da 503, ¿por
  qué?"* — Kobi no debería conformarse con "readiness OK"; debe leer logs/respuestas
  y ver los `500 fault-injected`.
- `fault-api.sh orders unready` → *"¿es la base de datos otra vez?"* — la respuesta
  correcta es **no**: catalog-api sigue Ready, así que la BD está bien; es orders-api.

## Aislamiento por capas (opcional)

```bash
kubectl apply -f 50-networkpolicies.yaml
```

Aplica `default-deny` + allows por capa (frontend→backend, backend→data). En un
cluster **Cilium + Hubble** esto además alimenta el panel **Reliability → Network
Drops** con tráfico `verdict=dropped` si algo intenta saltarse una capa.

## Visibilidad L7 / HTTP en Hubble (Cilium)

```bash
kubectl apply -f 55-l7-visibility.yaml
```

Sin esto, **Hubble solo captura L3/L4** (IP/puerto) de la tienda — los paneles
**Reliability** (error rate, latencia, tráfico) y las aristas L7 del mapa quedan
vacíos para los namespaces `shop-*`. Cilium solo parsea HTTP cuando una
`CiliumNetworkPolicy` con regla `http` redirige el tráfico a su proxy Envoy; una
`NetworkPolicy` estándar (la del paso anterior) **no** lo hace.

Este archivo agrega esa CNP de visibilidad (solo observabilidad, no filtra) en
las capas frontend y backend, sobre el puerto `8080`. Es el mismo patrón que la
policy `demo-web-http-visibility` de `deploy/test/demo-workload.yaml`. Solo
aplica en clusters **Cilium + Hubble**; en otro CNI estas CRDs no existen —
sáltate este archivo.

> Validado en kind: tras aplicarlo, `hubble observe --protocol http -n shop-backend`
> muestra los `GET /` entre capas, y los pods siguen `Ready` (los probes de
> kubelet y la cascada hacia la BD no se ven afectados — la policy es ingress-only).

## Limpieza

```bash
./teardown.sh            # borra los 3 namespaces + el contenedor de la BD
```

## Portabilidad

Los scripts asumen **kind** (red docker `kind`, pods enrutan a la IP del
contenedor). Para otros entornos:

| Entorno | Qué ajustar |
|---------|-------------|
| **kind** | Nada. Camino por defecto. |
| **Otra red docker** | `DB_NETWORK=<tu-red> database/run-db.sh && DB_NETWORK=<tu-red> ./apply.sh` |
| **Docker Desktop K8s** | La red puede no ser enrutable desde pods. Apunta el `Endpoints` de `10-data-tier.yaml` a la IP de `host.docker.internal`, o corre la BD con `--network host` y usa la IP del host. |
| **minikube** | Usa `minikube ssh` o la IP del host; ajusta el `Endpoints` a una IP alcanzable desde los pods. |
| **Postgres real / RDS / Cloud SQL** | No hace falta `run-db.sh`. Pon la IP/host real en el `Endpoints` (o cambia a un `Service` `ExternalName`) y aplica. La cascada se dispara igual cortando esa conectividad. |

La lógica de la app no depende de kind — solo el wiring del `Endpoints` a la IP
externa. Cualquier dirección que tus pods puedan alcanzar por TCP:5432 sirve.

> Estos manifiestos describen un estado de fallo a propósito. No los apliques en
> producción.
