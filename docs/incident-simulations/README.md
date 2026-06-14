# Laboratorio de simulación de incidentes para Kobi

Un set de escenarios de Kubernetes intencionalmente rotos para probar, demostrar
y validar las capacidades de **Kobi** (el copilot de KubeBolt): detección de
incidentes, diagnóstico, y **acciones propuestas** (`propose_*`).

Todo usa imágenes estándar (`alpine`, `python`, `nginx`, `postgres`, `pause`) —
sin imágenes custom — para que cualquiera lo aplique en su propio cluster.

Hay dos partes:

| Carpeta | Qué es | Para qué |
|---------|--------|----------|
| [`scenarios/`](./scenarios/) | 13 incidentes de **un solo workload**, uno por capacidad | Probar cada acción de Kobi de forma aislada y limpia |
| [`three-tier-shop/`](./three-tier-shop/) | App e-commerce de **3 capas** (front → APIs → BD externa) en namespaces separados | Probar **fallos en cascada** y el razonamiento de causa-raíz a través de la topología |

---

## Las 9 acciones que Kobi puede proponer

Kobi nunca ejecuta cambios: **propone** una acción que la UI renderiza como una
tarjeta de confirmación con botón *Execute* (la ejecución corre bajo el rol RBAC
del operador, no el de Kobi). Estos escenarios cubren las 9:

| Acción `propose_*` | Qué hace | Escenario que la dispara |
|--------------------|----------|--------------------------|
| `set_resources` | Sube/baja CPU·memoria (requests/limits) | `01-oomkilled`, `06-cpu-throttle`, `07-under-requested` |
| `restart_workload` | Rollout restart (Deploy/STS/DS) | `02-crashloop-exit` |
| `set_env` | Agrega/cambia variables de entorno | `03-crashloop-missing-env` |
| `set_image` | Cambia la imagen de un contenedor | `04-imagepullbackoff` |
| `rollback_deployment` | `kubectl rollout undo` a una revisión sana | `05-bad-rollout` |
| `patch_hpa` | Ajusta min/maxReplicas de un HPA | `08-hpa-maxed-out` |
| `scale_workload` | Escala a N réplicas (incl. 0) | `09-zero-replicas` |
| `debug_pod` | Adjunta un contenedor efímero de debug | `10-distroless-debug` |
| `delete_resource` | Borra un recurso (destructivo, pide confirmación) | `13-orphaned-resources` |

Además, dos escenarios disparan insights que Kobi **diagnostica** (sin acción
auto-remediable): `11-orphan-service` (service-no-endpoints) y
`12-orphan-networkpolicy` (policy-no-match).

---

## Parte 1 — Escenarios de un solo workload (`scenarios/`)

Todos viven en el namespace `kobi-incident-lab`.

### Aplicar todo

```bash
kubectl apply -f scenarios/
```

Espera 1–2 minutos a que los incidentes maduren (los crash-loops necesitan
acumular reinicios; el HPA necesita métricas). Luego ábre KubeBolt → Insights, o
pregúntale a Kobi.

### Tabla de escenarios

| # | Archivo | Síntoma | Insight | Acción Kobi |
|---|---------|---------|---------|-------------|
| 01 | `01-oomkilled.yaml` | OOMKilled (aloca 200Mi bajo límite de 128Mi) | OOMKilled | `set_resources` (mem limit) |
| 02 | `02-crashloop-exit.yaml` | Sale con exit 1 en loop | frequent-restarts | diagnóstico + `restart_workload` |
| 03 | `03-crashloop-missing-env.yaml` | Falta `DATABASE_URL` | crash-loop | `set_env` |
| 04 | `04-imagepullbackoff.yaml` | Tag de imagen inexistente | image-pull-backoff | `set_image` |
| 05 | `05-bad-rollout.yaml` | Deploy malo con revisión previa sana | image-pull/crash con rev. sana | `rollback_deployment` |
| 06 | `06-cpu-throttle.yaml` | Busy-loop con límite de 50m CPU | cpuThrottleRisk | `set_resources` (CPU) |
| 07 | `07-under-requested.yaml` | Usa mucho más de lo que pide | resource-underrequest | `set_resources` (requests) |
| 08 | `08-hpa-maxed-out.yaml` | HPA pineado en maxReplicas | hpaMaxedOut | `patch_hpa` |
| 09 | `09-zero-replicas.yaml` | `replicas: 0` | zeroReplicas | `scale_workload` |
| 10 | `10-distroless-debug.yaml` | Pod sin shell que hay que depurar | — (triage) | `debug_pod` |
| 11 | `11-orphan-service.yaml` | Service sin endpoints | service-no-endpoints | diagnóstico |
| 12 | `12-orphan-networkpolicy.yaml` | NetworkPolicy sin matches | policy-no-match | diagnóstico |
| 13 | `13-orphaned-resources.yaml` | Recursos zombie | (orphaned) | `delete_resource` |

### Dos escenarios necesitan un paso extra

- **05 (rollback):** un rollback necesita ≥2 revisiones. Tras aplicar, arma la
  segunda (rota) con:
  ```bash
  cd scenarios && ./break.sh
  ```
- **08 (HPA):** el HPA lee CPU de `metrics.k8s.io`, así que necesita
  **metrics-server**. Viene de fábrica en la mayoría de clusters gestionados; en
  kind instálalo si falta.

### Limpiar

```bash
kubectl delete ns kobi-incident-lab
```

---

## Parte 2 — App de 3 capas en cascada (`three-tier-shop/`)

El escenario estrella: una tienda e-commerce de 3 capas donde **tumbar la base
de datos** dispara una cascada de readiness que sube capa por capa. Incluye su
propio README detallado con el diagrama de arquitectura, el walkthrough de la
cascada y la inyección de fallos:

➡️ **[three-tier-shop/README.md](./three-tier-shop/README.md)**

Resumen rápido:

```bash
cd three-tier-shop
database/run-db.sh     # levanta postgres FUERA del cluster (docker)
./apply.sh             # despliega front + APIs y los conecta a la BD
database/stop-db.sh    # 💥 dispara la cascada: BD ↓ → backend NotReady → front sirve 503
database/start-db.sh   # recuperación end-to-end
database/fault-api.sh orders error   # 💥 tumba UNA sola API (error|unready|slow)
database/heal-api.sh  orders         # sana esa API
./teardown.sh          # limpia todo
```

---

## Portabilidad

- **kind** (probado): la BD corre como contenedor docker en la red `kind` y los
  pods la alcanzan por IP. Es el camino por defecto de los scripts.
- **Docker Desktop / minikube / otros:** la red docker puede llamarse distinto o
  no ser enrutable desde los pods. Pasa `DB_NETWORK=<tu-red>` a los scripts, o
  apunta el `Endpoints` de `three-tier-shop/10-data-tier.yaml` a una IP que tus
  pods sí alcancen (p. ej. la IP de `host.docker.internal`, o un postgres real).
  Detalles en el README de la app de 3 capas.
- Los escenarios de la **Parte 1** no dependen de nada externo y corren igual en
  cualquier cluster (solo el 08 pide metrics-server).

> Nota: estos manifiestos describen estados de fallo **a propósito**. No los
> apliques en un cluster de producción.
