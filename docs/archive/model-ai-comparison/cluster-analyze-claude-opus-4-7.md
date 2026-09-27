## ¿Cuál es el estado de mi cluster?


Voy a revisar el estado general de tu cluster.# Estado del cluster `<cluster-name>` ✅

**Salud general:** `healthy` — puntuación **100/100**

## Resumen

| Recurso | Estado |
|---|---|
| Nodos | **6/6** Ready |
| Pods | **75/75** Ready |
| Deployments | **32/32** |
| StatefulSets | **8/8** |
| DaemonSets | **4/4** |
| Servicios | 52 |
| PVCs | 8/8 |

## Uso de recursos

- **CPU:** 473m usado / 8955m solicitado / 11580m asignable → **4%** uso real, **77%** solicitado
- **Memoria:** 17.9 GiB usado / 17.4 GiB solicitado / 41.5 GiB asignable → **43%** uso real
- **Versión Kubernetes:** v1.34.4-eks-f69f56f

## ⚠️ Puntos de atención

Aunque el cluster está healthy, hay algunas señales de advertencia dignas de revisar:

1. **`monitoring/prometheus-grafana`** — usando **711 MiB (132% del límite de 512 MiB)**. Está por encima del límite configurado; riesgo de OOMKill.
2. **`login/keycloak`** — memoria al **95%** del límite (1021/1073 MiB). Muy cerca del umbral.
3. **`gitlab/gitlab-webservice`** — memoria al **88%** del límite (7.6 GiB).
4. **Eventos de cert-manager en `gitlab`:** fallos repetidos creando `CertificateRequest` (conflicto: *"already exists"*). Contador altísimo (**334.769 repeticiones**) — lleva tiempo fallando en bucle:
   - `Certificate/tls-gitlab-registry`
   - `Certificate/tls-gitlab-webservice`

## Recomendaciones

- Revisar y limpiar los `CertificateRequest` huérfanos en `gitlab` para que cert-manager pueda reconciliar:
   ```bash
   kubectl get certificaterequests -n gitlab
   kubectl describe certificate tls-gitlab-registry -n gitlab
   ```
- Considerar subir límites de memoria de Grafana o investigar un posible leak.

¿Quieres que profundice en alguno de estos puntos (p. ej., los certificados de GitLab o el consumo de Grafana)?



## Investiga sobre los problemas que encontraste


Voy a investigar los tres problemas en paralelo: los certificados de GitLab, el consumo de Grafana y el de Keycloak.Tengo mucha información. Déjame obtener detalles adicionales del pod de Grafana (con nombre completo) y los certificados de GitLab.Tengo lo necesario para un diagnóstico completo.

# Investigación de los problemas detectados

## 🔴 1. Pods OOMKilled (CRÍTICO)

KubeBolt ha detectado **4 eventos OOMKilled activos**:

| Pod | Límite memoria | % uso actual | Restarts |
|---|---|---|---|
| `login/keycloak-pod-1` | 512 MiB | **95%** | 6 |
| `login/keycloak-pod-2` | 512 MiB | **96%** | — |
| `gitlab/gitlab-webservice-pod-2` | ~8 GiB | **94%** | — |
| `gitlab/gitlab-webservice-pod-1` | ~8 GiB | — | — |

**Keycloak** (`login` ns) está especialmente mal dimensionado:
- Request: 256 MiB / Limit: **512 MiB** (muy bajo para Keycloak con JVM)
- Último OOMKill: 7 de abril 2026, tras correr ~48 días
- El contenedor arrancó el 8 dic 2025 → se ha reiniciado 6 veces por OOM

**Recomendación:** subir límites de Keycloak a ~1 GiB request / 2 GiB limit y configurar `JAVA_OPTS_APPEND=-XX:MaxRAMPercentage=70`.

## 🟡 2. Grafana: 133% del límite de memoria

- Pod: `monitoring/grafana-pod`
- Contenedor `grafana`: request **256 MiB** / limit **512 MiB**
- Uso real: **~711 MiB (133%)**
- **Curiosamente NO ha sido OOMKilled** (0 restarts, corriendo desde 8 dic 2025) — Linux probablemente le da margen porque hay memoria libre a nivel de nodo, pero está en riesgo permanente.
- KubeBolt también nota: *"memory request is less than 40% of actual usage"* — los requests están muy mal calibrados, afecta al scheduling.

**Recomendación:** ajustar a request **512 MiB** / limit **1 GiB** como mínimo.

## 🟡 3. HPAs de GitLab al máximo (sin margen)

Cuatro HPAs están **topados**, lo que impide absorber picos de carga:

| HPA | Réplicas |
|---|---|
| `gitlab/gitlab-webservice` | 2/2 |
| `gitlab/gitlab-gitlab-shell` | 2/2 |
| `gitlab/gitlab-registry` | 1/1 |
| `gitlab/gitlab-sidekiq-all-in-1-v2` | 1/1 |

El hecho de que `gitlab-webservice` esté topado Y con OOMKills simultáneamente sugiere que el cluster no puede escalar la carga actual de GitLab. Subir `maxReplicas` a 3-4 ayudaría, aunque primero conviene entender por qué consume tanta memoria.

## 🔴 4. cert-manager en loop infinito (gitlab)

Este es el problema más llamativo por su volumen:

- **334.769 repeticiones** del mismo error desde el **22 de diciembre de 2025** (casi 4 meses fallando)
- Dos certificados afectados:
  - `Certificate/tls-gitlab-registry` → intenta crear `tls-gitlab-registry-1`
  - `Certificate/tls-gitlab-webservice` → intenta crear `tls-gitlab-webservice-1`
- Error: `CertificateRequest "xxx-1" already exists`

Esto significa que cert-manager quiere renovar el certificado pero existe un `CertificateRequest` huérfano con el mismo nombre que bloquea el proceso. Los certificados **probablemente están caducados o cerca de caducarlo**.

**Diagnóstico y solución sugerida:**

```bash
# 1. Ver el estado real de los certificados
kubectl get certificate -n gitlab
kubectl describe certificate tls-gitlab-registry -n gitlab
kubectl describe certificate tls-gitlab-webservice -n gitlab

# 2. Ver los CertificateRequest huérfanos
kubectl get certificaterequests -n gitlab
```

⚠️ **Acción correctiva (destructiva):** eliminar el `CertificateRequest` huérfano para que cert-manager pueda recrearlo. Esto es seguro porque cert-manager los regenera automáticamente, pero confirma el estado antes:

```bash
# Primero en modo dry-run
kubectl delete certificaterequest tls-gitlab-registry-1 -n gitlab --dry-run=server
kubectl delete certificaterequest tls-gitlab-webservice-1 -n gitlab --dry-run=server

# Si se ve bien, ejecutar real
kubectl delete certificaterequest tls-gitlab-registry-1 -n gitlab
kubectl delete certificaterequest tls-gitlab-webservice-1 -n gitlab
```

---

## Prioridad sugerida

1. **Certificados de GitLab** — riesgo de caducidad (4 meses fallando).
2. **Keycloak OOMKilled** — servicio de login, impacto directo en usuarios.
3. **Grafana memoria** — subir límites antes de que el nodo tenga presión.
4. **HPAs GitLab** — revisar `maxReplicas` para evitar problemas de capacidad.

¿Quieres que profundice en alguno en concreto o revise los logs de algún pod?



## Investiga por qué tengo error en la integración de GitLab con Keycloak


Voy a investigar los logs de Keycloak y de GitLab webservice para encontrar errores de integración.Detalle interesante: el webservice de GitLab monta un secret **`openid-connect`** — esa es la integración con Keycloak. Voy a revisar los logs de ambos lados buscando errores OIDC/Keycloak.Los logs de Keycloak dan información muy útil. Los de GitLab webservice fueron truncados (son logs de API, no relevantes para OIDC). Con esto ya tengo suficiente para un diagnóstico claro.

# Diagnóstico: Integración GitLab ↔ Keycloak

## ✅ La integración **está funcionando correctamente**

No hay un fallo de configuración de la integración. GitLab y Keycloak están comunicándose bien:

- El cliente OIDC `openid_connect` en Keycloak (realm `<realm-id>`) está activo.
- El `redirect_uri` configurado es correcto: `https://git.example.com/users/auth/openid_connect/callback`.
- El webservice de GitLab monta correctamente el secret `openid-connect` (confirmado en volúmenes del pod).
- Los dos pods de Keycloak están clusterizados vía JGroups/Infinispan (caché compartida OK).

## 🔴 Lo que sí son errores reales (pero **no de integración**)

Los errores que aparecen en los logs de Keycloak son **errores esperados** y se dividen en 3 categorías:

### 1. Credenciales incorrectas de usuarios (no es un bug)
```
LOGIN_ERROR ... clientId="openid_connect" ... error="invalid_user_credentials"
```
- `user6@example.com` — 10 abril
- `user1@example.com` — 14 abril
- `user5@example.com` — 14 abril

Los usuarios están tecleando mal la contraseña. Normal.

### 2. Usuario intentando resetear password con email inexistente
```
RESET_PASSWORD_ERROR ... error="user_not_found" ... username="user3@example.com"
```

### 3. ⚠️ Error de aplicación en Keycloak Admin API
Este **sí es un bug real**, pero no es de la integración GitLab–Keycloak, sino de la provisión de usuarios vía Admin API:

```
ERROR [KeycloakErrorHandler] Uncaught server error: 
java.lang.RuntimeException: Unable to find group specified by path: /<example-group>
    at org.keycloak.models.utils.RepresentationToModel.createGroups(RepresentationToModel.java:727)
    at org.keycloak.services.resources.admin.UsersResource.createUser(...)
```

**Qué significa:** alguien (probablemente un script o Terraform/Ansible) intentó crear un usuario asignándolo al grupo `/<example-group>` que no existe en Keycloak. Keycloak devolvió un 500 en lugar de un 400.

## 🟡 El problema real: **los OOMKills están afectando la experiencia**

Aunque la integración funcione, los reinicios de Keycloak por OOM (6 restarts) sí provocan síntomas visibles:

1. **Cuando un pod de Keycloak cae**, hay un breve momento en el que el otro asume la carga y reconstruye el clúster Infinispan → durante ese tiempo los logins pueden tardar o fallar con timeouts.
2. Las sesiones activas (`sessions`, `authenticationSessions`, `clientSessions`) se rebalancean → algunos usuarios pueden verse forzados a re-autenticarse.
3. Keycloak 24.0.4 con **512 MiB de heap es extremadamente justo** cuando tiene cluster replicado con Infinispan. Lo normal serían 1–2 GiB.

También hay evidencia en el log:
```
REFRESH_TOKEN_ERROR ... error="invalid_token", reason="Token is not active"
```
Esto puede ocurrir si una sesión estaba en un pod que se reinició y el otro todavía no había terminado de replicar.

## 🎯 Recomendaciones

### Prioritario: subir recursos de Keycloak

Actualmente:
```yaml
resources:
  requests: { cpu: 200m, memory: 256Mi }
  limits:   { cpu: 500m, memory: 512Mi }   # ← causa OOMKill
```

Sugerido:
```yaml
resources:
  requests: { cpu: 500m, memory: 1Gi }
  limits:   { cpu: 1000m, memory: 2Gi }
env:
  - name: JAVA_OPTS_APPEND
    value: "-XX:MaxRAMPercentage=70 -XX:InitialRAMPercentage=50"
```

### Secundario: investigar el script que llama a Admin API

Revisar qué automatización intenta crear usuarios con `groups: ["/<example-group>"]` y:
- O crear el grupo primero.
- O quitar la asignación a grupo inexistente.

### Pregunta clave para ti

¿Has observado algún síntoma concreto cuando los usuarios hacen login con Keycloak contra GitLab? Por ejemplo:
- ¿Login infinito / timeout?
- ¿Error "No se puede verificar identidad"?
- ¿Usuarios que inician sesión y son desconectados poco después?

Con esa info puedo afinar más el diagnóstico (por ejemplo, revisar logs de `workhorse` o el sidekiq de GitLab que es quien procesa ciertos callbacks).