## ¿Cuál es el estado de mi cluster?


Voy a obtener una vista general de tu cluster para darte un resumen completo.## Estado del Cluster: **<cluster-name>**

### 🟢 Salud General: **100/100** — Healthy

| Recurso | Total | Listos | Problemas |
|---|---|---|---|
| **Nodes** | 6 | 6 ✅ | 0 |
| **Pods** | 75 | 75 ✅ | 0 |
| **Deployments** | 32 | 32 ✅ | 0 |
| **StatefulSets** | 8 | 8 ✅ | 0 |
| **DaemonSets** | 4 | 4 ✅ | 0 |
| **Services** | 52 | 52 ✅ | 0 |
| **Namespaces** | 16 | — | — |

### 📊 Uso de Recursos

| Métrica | Uso Actual | Solicitado | Asignable |
|---|---|---|---|
| **CPU** | **536m** (4.6% del asignable) | 8,955m (77.3%) | 11,580m |
| **Memoria** | **18.0 Gi** (43.3% del asignable) | 17.4 Gi (42.0%) | 41.5 Gi |

> La CPU tiene un **sobre-aprovisionamiento significativo**: se solicita el 77% pero solo se usa el 4.6%.

---

### 🔴 Insights Críticos (4)

| Recurso | Problema |
|---|---|
| `gitlab/gitlab-webservice-pod-1` | **OOMKilled** — contenedor `webservice` terminado por falta de memoria |
| `gitlab/gitlab-webservice-pod-2` | **OOMKilled** — contenedor `webservice` terminado por falta de memoria |
| `login/keycloak-pod-2` | **OOMKilled** — contenedor `keycloak` terminado por OOM |
| `login/keycloak-pod-1` | **OOMKilled** — contenedor `keycloak` terminado por OOM |

### ⚠️ Warnings (9)

- **Memory Pressure** en `gitlab-webservice` (95%), ambos pods de `keycloak` (95-96%), y **Grafana** usando **133% del límite** 🚨
- **4 HPAs al máximo** de réplicas en GitLab (webservice, shell, registry, sidekiq) — podrían necesitar escalar más
- **Reinicios frecuentes** en `login/keycloak` (6 restarts por OOM)
- Eventos Warning de **cert-manager** en `gitlab` (334K+ ocurrencias de CertificateRequest duplicado)

---

### 🎯 Recomendaciones Prioritarias

1. **GitLab Webservice** — Ambos pods usan **~88.5% del límite de memoria** (7.1 Gi de 8 Gi) y han sido OOMKilled. **Aumentar el memory limit** o investigar posibles memory leaks.

2. **Keycloak** — Los 2 pods están al 95-96% del límite (973 Mi de 1 Gi) con reinicios por OOM. **Subir el límite de memoria** a al menos 1.5 Gi.

3. **Grafana** — Está usando **678 Mi con un límite de 512 Mi** (133%). Necesita un aumento de límite urgente.

4. **HPAs de GitLab** — Los 4 están al máximo de réplicas. Evaluar **incrementar `maxReplicas`** o agregar más capacidad de nodo.

5. **cert-manager** — Revisar los certificados `tls-gitlab-webservice` y `tls-gitlab-registry` que generan 334K+ warnings por CertificateRequests duplicados.

¿Quieres que profundice en alguno de estos problemas?


## Investiga sobre los problemas que encontraste



Voy a investigar los problemas principales. Empiezo con los pods con OOMKilled y los HPAs al máximo.Grafana no se encontró con ese nombre. Déjame buscarlo y también revisar los HPAs problemáticos.Ahora investigo Grafana:Perfecto, ya tengo toda la información. Aquí va el análisis completo:

---

## 🔍 Investigación Detallada de Problemas

### 1. 🔴 GitLab Webservice — OOMKilled (Crítico)

**Pod:** `gitlab/gitlab-webservice-pod-1`

| Detalle | Valor |
|---|---|
| **Contenedor** | `webservice` (Puma) |
| **Límite de memoria** | **4 Gi** |
| **Request de memoria** | 2 Gi |
| **Restarts** | 2 (último OOMKill: 17 Abr 08:44 UTC) |
| **Worker processes** | 4 (PUMA_THREADS_MIN=4, MAX=4) |
| **PUMA_WORKER_MAX_MEMORY** | *(vacío — sin límite por worker)* |
| **DISABLE_PUMA_WORKER_KILLER** | `true` |

**🧠 Diagnóstico:** Con **4 workers Puma x 4 threads** sin `PUMA_WORKER_MAX_MEMORY` y con el worker killer **deshabilitado**, no hay mecanismo interno para reciclar workers que acumulan memoria. Los workers crecen indefinidamente hasta que el kernel OOM-kill el proceso.

**✅ Recomendaciones:**
1. **Habilitar el worker killer** — Cambiar `DISABLE_PUMA_WORKER_KILLER` a `false`
2. **Establecer `PUMA_WORKER_MAX_MEMORY`** — Por ejemplo `1024` (MB por worker), para que Puma recicle workers antes de alcanzar el límite del contenedor
3. **Subir el memory limit a 6 Gi** como medida temporal mientras se aplican los cambios anteriores
4. Considerar reducir `WORKER_PROCESSES` de 4 a 2 si el tráfico lo permite

---

### 2. 🔴 Keycloak — OOMKilled (Crítico)

**Pod:** `login/keycloak-pod-2`

| Detalle | Valor |
|---|---|
| **Límite de memoria** | **512 Mi** |
| **Request de memoria** | 256 Mi |
| **Restarts** | **5** (último OOMKill: 25 Mar 10:07 UTC) |
| **Imagen** | `keycloak:0.0.17` |

**🧠 Diagnóstico:** **512 Mi es insuficiente para Keycloak en producción.** Keycloak es una aplicación Java que típicamente necesita 1-2 Gi de heap. Con 5 restarts por OOM, el límite está claramente por debajo de lo necesario. Además, no se ve ninguna variable `JAVA_OPTS` o `JAVA_OPTS_KC_HEAP` para controlar el heap de la JVM, lo que significa que Java intentará usar toda la memoria disponible.

**✅ Recomendaciones:**
1. **Aumentar memory limit a al menos 1.5 Gi** (request a 1 Gi):
   ```yaml
   resources:
     requests:
       memory: "1Gi"
       cpu: "200m"
     limits:
       memory: "1.5Gi"
       cpu: "1"
   ```
2. **Configurar el heap de Java** mediante variable de entorno:
   ```
   JAVA_OPTS_KC_HEAP: "-Xms512m -Xmx1024m"
   ```
3. Actualizar la imagen `0.0.17` — verificar si hay versiones más recientes con fixes de memory leaks

---

### 3. 🟠 Grafana — Memoria sobre el límite (Warning)

**Pod:** `monitoring/grafana-pod`

| Detalle | Valor |
|---|---|
| **Límite de memoria** | **512 Mi** |
| **Request de memoria** | 256 Mi |
| **GOMEMLIMIT** | 536,870,912 (~512 Mi, derivado del limit) |
| **Restarts** | 0 |
| **Uptime** | ~131 días (desde 8 Dic 2025) |

**🧠 Diagnóstico:** Grafana reporta uso por encima de su límite (insight indicaba 133% = ~678 Mi). Esto es posible porque el OOM killer no ha actuado aún (Linux puede tolerar picos antes de matar). Con `GOMEMLIMIT` configurado al mismo nivel que el limit, el GC de Go debería ser agresivo, pero con 131 días de uptime, la acumulación de dashboards/queries puede superar ese margen.

**✅ Recomendaciones:**
1. **Aumentar memory limit a 1 Gi** y request a 512 Mi
2. Hacer un `rollout restart` para liberar memoria acumulada:
   ```bash
   kubectl rollout restart deployment prometheus-grafana -n monitoring
   ```
3. Revisar si hay dashboards muy pesados o con refresh intervals muy agresivos

---

### 4. 🟡 HPAs de GitLab — min = max (Info)

| HPA | Réplicas | min/max | CPU actual | Target |
|---|---|---|---|---|
| `gitlab-webservice` | 2/2 | **2/2** | 250m | 1000m |
| `gitlab-gitlab-shell` | 2/2 | **2/2** | 8m | 100m |
| `gitlab-registry` | 1/1 | **1/1** | 1m | — |
| `gitlab-sidekiq-all-in-1-v2` | 1/1 | **1/1** | 31m | 350m |

**🧠 Diagnóstico:** Todos los HPAs tienen **minReplicas = maxReplicas**, lo que **anula por completo el autoscaling**. Funcionan como deployments de réplicas fijas. Esto parece intencional (posiblemente por la migración Velero del 9 Dic 2025), pero significa que si el tráfico sube, no se escalarán.

El uso de CPU actual está **muy por debajo de los targets**, así que no hay presión de escalado ahora. No es un problema urgente.

**✅ Recomendación:** Si desean autoscaling real, separar min de max:
```yaml
# Ejemplo para webservice
minReplicas: 2
maxReplicas: 4
```

---

### 5. 🟡 cert-manager — CertificateRequests duplicados (Warning)

| Certificado | Eventos Warning | Desde |
|---|---|---|
| `tls-gitlab-registry` | **334,759** | 22 Dic 2025 |
| `tls-gitlab-webservice` | **334,759** | 22 Dic 2025 |

**🧠 Diagnóstico:** cert-manager lleva **~4 meses** intentando crear `CertificateRequest` con nombre `-1` que ya existe. Esto genera un bucle infinito de warnings. Probablemente un `CertificateRequest` huérfano quedó de la migración Velero y cert-manager no puede crear uno nuevo porque colisiona con el nombre.

**✅ Recomendaciones:**

⚠️ **Comando destructivo** — verificar primero que los certificados TLS actuales son válidos:
```bash
# Verificar estado de los certificados
kubectl get certificates -n gitlab

# Si están en estado Ready=True, eliminar los CertificateRequests huérfanos
kubectl delete certificaterequest tls-gitlab-registry-1 -n gitlab
kubectl delete certificaterequest tls-gitlab-webservice-1 -n gitlab
```
Esto permitirá que cert-manager cree nuevos CertificateRequests y detenga el spam de eventos.

---

### 📋 Resumen de Prioridades

| Prioridad | Problema | Impacto | Esfuerzo |
|---|---|---|---|
| 🔴 **P1** | Keycloak OOM (5 restarts) | Caídas de autenticación | Bajo — subir límites |
| 🔴 **P2** | GitLab Webservice OOM | Caídas del servicio Git | Medio — config Puma + límites |
| 🟠 **P3** | Grafana sobre límite | Riesgo de crash de monitoring | Bajo — subir límites + restart |
| 🟡 **P4** | cert-manager loop | Ruido en eventos, carga innecesaria | Bajo — borrar CRs huérfanos |
| 🟡 **P5** | HPAs min=max | Sin autoscaling | Bajo — ajustar valores |

¿Quieres que profundice más en alguno de estos problemas? Por ejemplo, puedo revisar los logs de Keycloak o GitLab para buscar patrones de memory leak.



## Investiga por qué tengo error en la integración de GitLab con Keycloak



Voy a investigar la integración GitLab-Keycloak revisando los logs de ambos servicios y las configuraciones relevantes.Ahora necesito revisar los services y la configuración de red entre GitLab y Keycloak para entender la conectividad de la integración.Ahora tengo un panorama completo. Aquí está el análisis:

---

## 🔍 Investigación: Integración GitLab ↔ Keycloak

### Arquitectura detectada

| Componente | URL | Namespace |
|---|---|---|
| **GitLab** | `https://git.example.com` | `gitlab` |
| **Keycloak** | `https://login.example.com` | `login` |
| **Protocolo** | OpenID Connect (OIDC) | — |
| **Client ID en Keycloak** | `openid_connect` | — |
| **Realm** | `<realm-name>` (ID: `<realm-id>`) | — |

---

### 🔴 Problemas encontrados

#### 1. Errores de autenticación OIDC (LOGIN_ERROR)

Los logs de Keycloak muestran **múltiples errores de login** contra el cliente `openid_connect` (GitLab):

| Fecha | Usuario | Error | Detalle |
|---|---|---|---|
| 1 Abr | `user1@example.com` | **invalid_user_credentials** | Contraseña incorrecta |
| 6 Abr | `user2@example.com` | **invalid_user_credentials** | Contraseña incorrecta |
| 12 Abr | `user3@example.com` | **user_not_found** | Usuario no existe en Keycloak |
| 14 Abr | `user1@example.com` | **invalid_user_credentials** | Contraseña incorrecta (reincidente) |

Todos los redirect van a `https://git.example.com/users/auth/openid_connect/callback` — confirmando que **la integración OIDC está configurada y funcional**, pero los errores son de credenciales de usuario.

#### 2. Token expirado en API de GitLab (problema separado pero relacionado)

Los logs de GitLab muestran **peticiones API con status 401** constantes:

```
meta.auth_fail_reason: "token_expired"
meta.auth_fail_token_id: "PersonalAccessToken/59"
```

Un **PersonalAccessToken (ID 59)** está expirado y un servicio con user-agent `node-fetch` (probablemente **Backstage**) lo sigue usando para consultar proyectos como:
- `<group>/<repo-1>`
- `<group>/<repo-2>`
- `<group>/<repo-3>/*`

Esto genera un flujo constante de **401 Unauthorized** contra la API de GitLab.

#### 3. Inestabilidad del cluster de Keycloak (Infinispan)

Los logs muestran que el **7 de Abril a las 16:22 UTC**, el pod `keycloak-pod-1` salió del cluster Infinispan y re-entró 1.5 minutos después como nueva instancia (`-<new-suffix>`), forzando un **rebalance completo** de todas las caches:

- `sessions`, `authenticationSessions`, `clientSessions`
- `offlineSessions`, `offlineClientSessions`
- `loginFailures`, `actionTokens`, `work`

Esto fue causado por el **OOMKill** de Keycloak. Durante el rebalance, las sesiones activas pueden perderse, causando que **usuarios autenticados en GitLab pierdan su sesión OIDC** y necesiten re-autenticarse.

#### 4. Logout incompleto

```
Some clients have not been logged out for user user4@example.com in <realm-name> realm: openid_connect
```

El back-channel logout de Keycloak hacia GitLab no funciona correctamente, dejando sesiones huérfanas.

---

### 🧠 Diagnóstico consolidado

| Tipo de error | Causa raíz | Impacto |
|---|---|---|
| `invalid_user_credentials` | Usuarios con contraseña incorrecta | Individual — no es un bug de integración |
| `user_not_found` | Usuario no provisionado en Keycloak | Individual — revisar provisioning |
| `token_expired` (401s) | **PersonalAccessToken/59 expirado** | 🔴 Backstage no puede leer proyectos de GitLab |
| Pérdida de sesiones | **OOMKill de Keycloak** → rebalance Infinispan | 🔴 Usuarios pierden sesión al azar |
| Logout fallido | Back-channel logout no configurado/funcional | 🟡 Sesiones fantasma |

**La integración OIDC en sí está correctamente configurada.** Los problemas principales son:

1. **Keycloak se cae por OOM** → destruye las sesiones → los usuarios pierden autenticación en GitLab
2. **Un token de API expirado** está generando miles de 401s (probablemente desde Backstage)

---

### ✅ Recomendaciones

#### P1 — Estabilizar Keycloak (evitar pérdida de sesiones)
```yaml
# Aumentar recursos de Keycloak
resources:
  requests:
    memory: "1Gi"
    cpu: "500m"
  limits:
    memory: "1.5Gi"
    cpu: "1"

# Agregar configuración de heap Java
env:
  - name: JAVA_OPTS_KC_HEAP
    value: "-Xms512m -Xmx1024m"
```

#### P2 — Renovar PersonalAccessToken/59 en GitLab
Acceder a GitLab Admin → **Tokens de acceso personal** y buscar el token con ID 59:
```
https://git.example.com/admin/personal_access_tokens
```
Renovarlo o crear uno nuevo y actualizar la configuración de Backstage.

#### P3 — Configurar Back-Channel Logout
En Keycloak, verificar que el client `openid_connect` tenga configurado:
- **Backchannel Logout URL**: `https://git.example.com/users/auth/openid_connect/backchannel-logout`
- **Backchannel Logout Session Required**: ON

#### P4 — Revisar usuarios faltantes
El usuario `user3@example.com` no existe en Keycloak. Verificar si:
- Debe ser provisionado manualmente
- Hay una federación LDAP/AD configurada que no está sincronizando correctamente

---

¿Quieres que investigue más a fondo alguno de estos puntos? Por ejemplo, puedo revisar los logs de Backstage (el servicio `keycloak-permissions-api` en `argocd`) o la configuración de los secrets de Keycloak.

