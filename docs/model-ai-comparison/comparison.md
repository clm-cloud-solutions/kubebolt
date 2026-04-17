# Copilot — Comparación de modelos: Opus 4.6 vs Opus 4.7

Experimento controlado del AI Copilot de KubeBolt sobre un escenario real de diagnóstico
en el cluster `<cluster-name>`.

## Resumen ejecutivo

**Opus 4.6 superó a Opus 4.7 en profundidad diagnóstica y, críticamente, detectó un
problema que 4.7 descartó.** En el tercer prompt — donde el objetivo era diagnosticar un
error en la integración GitLab ↔ Keycloak — 4.7 filtró los logs del webservice de GitLab
como *"no relevantes para OIDC"* y omitió un `PersonalAccessToken` expirado que estaba
generando cientos de 401 contra la API de GitLab. 4.6 procesó los mismos logs, encontró
el token expirado (ID 59), lo atribuyó al consumidor probable (Backstage, vía el
user-agent `node-fetch`) y listó los endpoints específicos afectados.

En los dos prompts anteriores los dos modelos llegaron a conclusiones equivalentes, pero
4.6 fue consistentemente más profundo en causa raíz (p. ej. detectó la configuración de
Puma como origen de los OOMKill de GitLab, mientras 4.7 se quedó en "falta de memoria")
y conectó pistas temporales (migración Velero → CertificateRequests huérfanos) que 4.7
no correlacionó.

Este resultado es contraintuitivo: el modelo más reciente (4.7) no fue el mejor para
este caso de uso. La diferencia parece estar en que 4.7 optimiza para respuestas más
compactas, pero a costa de descartar información que en diagnóstico operativo suele
contener la señal relevante.

## Configuración del experimento

| Parámetro | Valor |
|---|---|
| Cluster | `<cluster-name>` |
| Harness | KubeBolt (`make dev` en local) |
| Provider | Anthropic |
| Modelo A | `claude-opus-4-6` |
| Modelo B | `claude-opus-4-7` |
| Idioma | Español |
| Fecha | 2026-04-17 |

Los tres prompts, en el mismo orden para ambos modelos, fueron:

1. **P1** — *¿Cuál es el estado de mi cluster?*
2. **P2** — *Investiga sobre los problemas que encontraste.*
3. **P3** — *Investiga por qué tengo error en la integración de GitLab con Keycloak.*

Transcripts completos:

- [`cluster-analyze-claude-opus-4-6.md`](./cluster-analyze-claude-opus-4-6.md)
- [`cluster-analyze-claude-opus-4-7.md`](./cluster-analyze-claude-opus-4-7.md)

## P1 — Estado del cluster

Ambos modelos pintaron el mismo panorama global (6/6 nodos, 75/75 pods, CPU
sobreprovisionada ~77% solicitada vs 4% real, memoria 43% usada) y ambos detectaron los
mismos focos de atención. La diferencia está en el nivel de estructura de la respuesta.

| Dimensión | Opus 4.6 | Opus 4.7 |
|---|---|---|
| Forma de presentar OOMKills | **4 pods OOMKilled listados explícitamente como "Insights Críticos"** | Mencionados como *"memoria al 95%"*, sin marcarlos como insight crítico |
| Warnings enumerados | 9 warnings (incluye restarts, HPAs topados, cert-manager) | 4 puntos de atención |
| Recomendaciones | 5 priorizadas | 2 |
| Verbosidad | Más densa y accionable | Más compacta |

**Veredicto P1:** empate funcional con ventaja estructural para 4.6. Ambos entregan
suficiente señal para actuar; 4.6 baja la fricción para el siguiente paso porque ya tiene
los OOMKills enmarcados como crítico.

## P2 — Investigación de problemas

Aquí la brecha se ensancha. Ambos modelos investigan los cuatro mismos focos
(Keycloak OOM, GitLab webservice OOM, Grafana sobre límite, cert-manager en loop, HPAs
topados), pero con profundidad muy distinta.

| Problema | Opus 4.6 | Opus 4.7 |
|---|---|---|
| **GitLab webservice OOMKilled** | **Identifica la causa raíz en la configuración de Puma**: `DISABLE_PUMA_WORKER_KILLER=true`, `PUMA_WORKER_MAX_MEMORY` vacío, 4 workers × 4 threads sin reciclaje. Recomienda ajustar la config de Puma antes de subir memoria. | Identifica que los pods están OOMKilled y al 94% de uso. Recomienda investigar capacidad pero no profundiza en configuración de aplicación. |
| **Keycloak OOMKilled** | Sube límites, sugiere `JAVA_OPTS_KC_HEAP="-Xms512m -Xmx1024m"` y **nota la imagen `keycloak:0.0.17` como desactualizada**. | Sube límites a 1 Gi/2 Gi y sugiere `JAVA_OPTS_APPEND=-XX:MaxRAMPercentage=70`. |
| **Grafana 133% del límite** | Idem recomendación + **nota `GOMEMLIMIT` y 131 días de uptime** como factores acumulativos. Sugiere `rollout restart`. | Sube límites a 512 Mi/1 Gi. Nota que Linux tolera picos si hay memoria libre en el nodo. |
| **HPAs de GitLab** | Detecta `minReplicas == maxReplicas` en los 4 y lo describe como *"autoscaling anulado"*. | Nota que los 4 están topados y sugiere subir `maxReplicas`. |
| **cert-manager loop** | 334 K repeticiones + **conecta temporalmente** con la migración Velero del 9 Dic 2025 como posible origen. | 334 K repeticiones desde 22 Dic 2025. Sugiere borrar CRs huérfanos. |
| Tabla final de priorización | Sí (impacto + esfuerzo por problema) | Prioridad ordenada sin matriz |
| Rondas de tool calls observadas | 3 | 2 |

**Veredicto P2:** ventaja clara para 4.6. La configuración de Puma y la correlación
Velero → certs huérfanos son observaciones que cambian la recomendación ejecutable
(no basta subir memoria; hay que arreglar la config que causa el leak). 4.7 entrega un
diagnóstico correcto pero plano.

## P3 — Integración GitLab ↔ Keycloak (el prompt decisivo)

Este era el prompt diseñado para evidenciar capacidad de razonamiento sobre logs ruidosos.
El síntoma buscado — un `PersonalAccessToken` expirado generando 401s — está en los logs
de GitLab, no en los de Keycloak. Detectarlo requiere no descartar los logs del
webservice como irrelevantes.

| Dimensión | Opus 4.6 | Opus 4.7 |
|---|---|---|
| Conclusión sobre la integración OIDC | *"Está correctamente configurada"* | *"Está funcionando correctamente"* |
| Errores de credenciales clasificados | Sí (invalid_user_credentials, user_not_found) | Sí |
| Bug de Admin API (grupo `/<example-group>`) | Detectado (mencionado en P2 como insight) | Detectado (en P3) |
| Rebalance de Infinispan por OOMKill | Detectado con timestamp específico (7 Abr 16:22 UTC), nombre del pod post-restart (`-<new-suffix>`) | Detectado, conectado con pérdida de sesiones |
| Logout incompleto / back-channel logout | Detectado; recomienda configurar *Backchannel Logout URL* | No mencionado |
| **`PersonalAccessToken/59` expirado (401s)** | ✅ **Detectado**. Identificó `meta.auth_fail_reason: token_expired` / `meta.auth_fail_token_id: PersonalAccessToken/59`. | ❌ **No detectado**. Descartó los logs del webservice de GitLab como *"truncados, no relevantes para OIDC"*. |
| Atribución del consumidor del token | Backstage, por user-agent `node-fetch`, con endpoints específicos (`<group>/<repo>/<project>`, etc.) | — |
| Rondas de tool calls observadas | 2 | 2 |

**Veredicto P3:** ventaja decisiva para 4.6. La diferencia no es de estilo: es que 4.7
filtró información relevante antes de procesarla. Es el tipo de fallo que degrada
severamente la utilidad del copilot en escenarios reales donde los logs ruidosos son la
norma.

## Diferencias sistemáticas observadas

Más allá de los resultados por prompt, hay patrones que aparecen en ambos modelos a lo
largo del experimento:

1. **Triage de información.** 4.7 tiende a descartar bloques de información que *parecen*
   fuera de tema (ej.: "logs de API, no relevantes para OIDC"). 4.6 los procesa y extrae
   señal. En diagnóstico operativo esto es diferencial: los problemas suelen esconderse
   justo en los logs que no parecían relevantes.

2. **Profundidad de causa raíz.** Ante un OOMKill, 4.7 se detiene en "falta memoria,
   sube el límite". 4.6 busca la configuración que causa el consumo anormal (Puma worker
   killer, heap JVM, uptime acumulado). Esto cambia la recomendación ejecutable.

3. **Correlación temporal.** 4.6 enlaza eventos separados en el tiempo ("migración Velero
   9 Dic → CRs huérfanos 22 Dic"; "OOMKill de Keycloak 7 Abr 16:22 UTC → rebalance
   Infinispan → REFRESH_TOKEN_ERROR"). 4.7 trata los eventos como aislados.

4. **Atribución específica.** 4.6 llega hasta el consumidor probable (Backstage vía
   `node-fetch`). 4.7 se queda en diagnósticos genéricos.

5. **Estructura de respuesta.** 4.6 usa más consistentemente tablas de priorización
   (impacto + esfuerzo) al cierre. 4.7 lista recomendaciones pero sin matriz.

6. **Rondas de tool calls.** 4.6 hizo más rondas de investigación en P2 (3 vs 2), lo que
   es consistente con la mayor profundidad obtenida. En P1 y P3 ambos hicieron el mismo
   número de rondas.

## Conclusiones

Para el caso de uso diagnóstico del Copilot de KubeBolt — donde el valor está en
identificar la causa raíz real, no en reportar síntomas — **Opus 4.6 es notablemente
superior a Opus 4.7 en este experimento**. La brecha se amplifica cuando el problema
requiere:

- Procesar logs ruidosos sin descartar lo que parece fuera de tema.
- Correlacionar eventos separados en el tiempo.
- Ir más allá del "sube los límites" y atacar la configuración de aplicación.

Opus 4.7 sigue siendo adecuado para un panorama general rápido (P1), pero en
investigación profunda y diagnóstico multi-causa (P2, P3) deja puntos clave fuera del
análisis.

El resultado es contraintuitivo: la intuición sugiere que el modelo más nuevo debería
ganar. Una hipótesis es que 4.7 optimiza para concisión y velocidad, lo que en tareas
conversacionales puede ser una ventaja, pero en diagnóstico operativo se traduce en
descartar información que resulta ser la clave del problema. Un único experimento no es
prueba suficiente — pero el patrón (menos rondas de investigación, menor profundidad,
filtrado agresivo de logs) es consistente a lo largo de los tres prompts.

### Implicación para el producto

Considerar **Opus 4.6 como modelo por defecto** para el Copilot cuando el usuario esté
configurado con Anthropic, al menos hasta que se valide el comportamiento de 4.7 en más
escenarios. Conviene repetir el experimento con otros casos de uso (escalado, red,
storage) antes de tomar una decisión definitiva.

## Reproducir el experimento

```bash
# Modelo A
echo 'KUBEBOLT_AI_MODEL=claude-opus-4-6' >> .env
make dev
# → ejecutar P1, P2, P3 en la UI del Copilot y capturar respuestas

# Modelo B
# editar .env → KUBEBOLT_AI_MODEL=claude-opus-4-7
make dev
# → repetir los tres prompts
```

Los prompts deben ejecutarse en la misma sesión de chat, en el orden P1 → P2 → P3, contra
el mismo cluster y con el menor intervalo de tiempo posible entre corridas para minimizar
el ruido de cambios en el estado del cluster (pods reiniciándose, nuevos OOMKills, etc.).
