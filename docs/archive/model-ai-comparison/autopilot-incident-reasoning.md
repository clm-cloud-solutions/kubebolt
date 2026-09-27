# Autopilot — Razonamiento de incidentes en casos reales

Tres incidentes de Kubernetes resueltos (o deliberadamente NO resueltos) por
KubeBolt Autopilot, capturados en ejecución contra un cluster real. El foco de
este documento no es *cuántos* incidentes cierra el agente, sino **cómo
razona**: cuándo decide actuar, cuándo se contiene, y cómo distingue una
mitigación de una cura.

Todo el texto citado del agente (investigación, plan, postmortem) es **literal**
de cada ejecución — no está editado ni reescrito.

## Resumen ejecutivo

El problema central de la auto-remediación no es técnico, es de **confianza**.
Una herramienta que solo alerta deja todo el trabajo al operador a las 3am; una
que ejecuta acciones a ciegas puede alargar o empeorar el incidente. Por eso un
equipo duda en darle a un agente permiso de escritura sobre producción.

Los tres casos siguientes muestran el comportamiento intermedio que tomaría un
SRE senior: en cada uno, Autopilot llegó a una **decisión distinta** sobre el
mismo tipo de pregunta ("¿debo actuar?"), y las tres fueron correctas:

| Caso | Síntoma | Confianza | Decisión | ¿Ejecutó algo? |
|---|---|---|---|---|
| A | CrashLoopBackOff (bug de código) | 0.98 | **No actuar** → escalar con diagnóstico | No — ningún fix automático aplica |
| B | CrashLoopBackOff (dependencia borrada) | 0.97 | **Rollback** de mitigación + causa raíz | Sí (con aprobación) |
| C | HPA en su tope (saturación de CPU) | 0.97 | **Mitigar** + advertir que no es la cura | Sí (dos acciones) |

La constante en los tres: Autopilot **distingue alivio de cura y reconoce dónde
termina su autoridad**. Cuando puede recuperar el servicio, lo hace; cuando solo
puede aplicar un parche, lo dice; y cuando no hay remedio automático seguro, no
inventa uno.

## Configuración

| Parámetro | Valor |
|---|---|
| Cluster | kind local (`kind-kubebolt-dev`), Kubernetes v1.35 |
| Harness | KubeBolt Autopilot (servicio Node/TypeScript sobre el backend Go de KubeBolt) |
| Pipeline por incidente | triage → investigación → planificación → ejecución → postmortem |
| Modo de operación | `autonomous` (ejecuta acciones reversibles de bajo riesgo; las destructivas siempre piden aprobación) |
| Acceso del investigador | solo lectura: logs de pod, eventos, YAML, `describe`, historial de revisiones, métricas |

Cada incidente es un escenario controlado y reproducible (no datos de un cliente
real); lo que es real es el razonamiento del agente sobre cada uno.

---

## Caso A — CrashLoopBackOff por un bug en el código de la aplicación

### Escenario

Un Deployment cuyo contenedor falla de forma determinística: el comando del pod
ejecuta `exit 1` de forma incondicional en cada arranque. kubelet lo reinicia,
vuelve a fallar, y tras unos ciclos entra en `CrashLoopBackOff`. No es una fuga
de memoria (OOM), no es una regresión de una versión nueva, no es un probe de
liveness mal calibrado: el propio binario está roto y termina con código 1 cada
vez que arranca.

Es el caso que más fácilmente lleva a una herramienta automática a hacer daño:
el síntoma (`CrashLoopBackOff`) invita a reintentar — reiniciar el pod, o hacer
rollback — pero ninguna de esas acciones puede arreglar un `exit 1` cableado en
el código.

### Cómo razonó Autopilot

El investigador leyó el spec del pod y los logs, y concluyó (confianza **0.98**):

> "The container 'app' in Deployment crashy-app is crashing deterministically
> due to a hardcoded `exit 1` baked directly into the pod spec's shell command.
> […] This is not a transient fault, resource exhaustion, or misconfigured
> probe: the crash is unconditional and will repeat indefinitely until the
> Deployment spec itself is corrected. No rollback path exists, as this is the
> only revision ever deployed."

Dos observaciones técnicas que sostienen la decisión:

1. **El fallo es incondicional**, no dependiente de estado. Un reinicio produce
   exactamente el mismo resultado — distinto de un OOM, donde subir el límite de
   memoria sí cambia el desenlace.
2. **No existe una revisión anterior sana.** Autopilot consultó el historial del
   Deployment: solo hay una revisión, así que un `rollback` no tiene destino. La
   palanca de mitigación más común para un workload roto (volver a la versión
   previa) directamente no aplica aquí.

El plan resultante fue una sola acción, `inform_only` — escalar a un humano sin
mutar el cluster:

> "Restarts cannot fix this crash loop because the container command is `exit 1`
> unconditionally and revision 1 is the only revision — no rollback target
> exists. A human operator must edit the Deployment spec […]."

### Resultado

Incidente escalado, con el diagnóstico y el comando concreto para corregirlo
(`kubectl edit deployment/crashy-app`). Autopilot **no ejecutó ninguna mutación**
— ni reinicios, ni un rollback hacia una revisión inexistente.

El punto contraintuitivo: Autopilot reportó **0.98 de confianza** *en que no
puede arreglar esto automáticamente*. Eso es lo opuesto a un falso positivo —
no es "no supe qué hacer", es "estoy seguro de que la causa está en el código y
ninguna acción de infraestructura la resuelve".

### Action items del postmortem

- Corregir el spec del Deployment con un entrypoint válido.
- Añadir una `ValidatingAdmissionPolicy` (o regla Kyverno/OPA) que rechace
  Deployments cuyo `command`/`args` sea un `exit 1` literal.
- Añadir un gate de CI que ejecute el contenedor con sus `args` de producción en
  una ventana de ~30s y falle el pipeline si termina con código ≠ 0.

---

## Caso B — CrashLoopBackOff por una dependencia de configuración eliminada

### Escenario

Un Deployment sano leía la variable `DB_PASSWORD` desde un `Secret`. Alguien
eliminó ese Secret. Mientras el pod existente siguió vivo no pasó nada — la
variable ya estaba cargada en su entorno. Pero al reiniciarse (por un nuevo
deploy, un drain de nodo, o un kill manual), el pod nuevo arrancó **sin** la
variable, su script de arranque trató el valor vacío como fatal y terminó con
`exit 1`, entrando en `CrashLoopBackOff`.

El detalle técnico clave: **el spec del Deployment nunca cambió.** Lo único que
desapareció fue una dependencia externa. Esto rompe la heurística ingenua de
"si está en CrashLoop tras un cambio, haz rollback" — porque no hubo cambio en
el workload.

### Cómo razonó Autopilot

El investigador usó cuatro herramientas de lectura (logs, YAML del recurso,
eventos y el historial de revisiones) y reconstruyó la cadena causal completa
(confianza **0.97**):

> "Revision 2 […] references the Secret 'app-db-credentials' […]. The Secret is
> not present in the namespace […], causing every container start to emit
> 'FATAL: DB_PASSWORD is not set' and exit with code 1. Revision 1 […] is still
> healthy with 1 ready replica, making it a safe rollback target."

El hallazgo que cambia la decisión vino del **historial de despliegue**: aunque
la causa raíz (el Secret borrado) es externa al spec, la revisión 1 seguía
corriendo con una réplica sana — su pod tenía la variable aún en memoria. Por lo
tanto un rollback a esa revisión **sí restaura el servicio de inmediato**, aun
cuando no toca la causa de fondo.

El plan tuvo tres pasos, separando explícitamente mitigación de cura:

1. **`rollback_deployment`** (riesgo alto → **requirió aprobación humana**) —
   "Rolls back to revision 1 […]; the Secret dependency that causes the fatal
   exit is no longer present in the older spec."
2. **`verify_pods_ready`** — confirma que el pod rolled-back alcanza estado
   Ready.
3. **`inform_only`** — la causa raíz, como follow-up para el operador:
   > "FOLLOW-UP REQUIRED: Before re-deploying revision 2, create the Secret
   > 'app-db-credentials' […]. Additionally, consider changing the Secret
   > reference from optional:true to optional:false so a missing Secret prevents
   > pod scheduling rather than triggering a runtime crash."

### Resultado

Tras la aprobación, las tres acciones terminaron con éxito y la verificación
pasó. Ventana de degradación: ~4 minutos.

Lo notable es que Autopilot no confundió las dos mitades del problema. Recuperó
el servicio con lo que tenía a mano (la revisión sana viva) **y por separado**
entregó la causa raíz real — recrear el Secret antes del próximo deploy, porque
de lo contrario el incidente reaparece en cuanto algo vuelva a reiniciar el pod.
La sugerencia adicional (`optional: true` → `optional: false`) convierte un fallo
silencioso en tiempo de ejecución en un fallo de admisión visible: kubelet
rechaza programar el pod si el Secret no existe, en lugar de dejarlo crashear.

### Action items del postmortem

- Crear el Secret `app-db-credentials` antes de cualquier redeploy.
- Cambiar `secretKeyRef` de `optional: true` a `optional: false` para que un
  Secret ausente impida el scheduling en vez de provocar un crash en runtime.
- Añadir un check de pre-deploy (p. ej. `kubectl diff` + un validador de
  existencia del Secret, o una política Kyverno) que falle el rollout cuando una
  dependencia referenciada no existe.
- Alerta de Prometheus sobre la tasa de `kube_pod_container_status_restarts_total`
  > 3 en 5 minutos.
- Auditar otros Deployments con `secretKeyRef optional: true` cuyo código trate
  el valor como obligatorio.

---

## Caso C — HorizontalPodAutoscaler en su tope de réplicas

### Escenario

Un HPA llevaba más de **12 días** pineado en su máximo de réplicas, con la
condición `ScalingLimited: TooManyReplicas` continuamente activa. Cada pod corría
al **200% de su CPU request** y saturaba el límite duro de CPU (100% de
throttling). El escalado horizontal estaba agotado: el HPA quería más réplicas
pero no podía crearlas. La causa de fondo era un busy-loop en el contenedor que
consume CPU sin relación con su request declarado — es decir, el workload está
intrínsecamente mal dimensionado, no atravesando un pico transitorio.

Este caso pone a prueba algo distinto de los anteriores: aquí **sí** hay acciones
de mitigación disponibles (subir el tope de réplicas, subir el límite de CPU),
pero ninguna resuelve la causa raíz, que vive en el código de la aplicación.

### Cómo razonó Autopilot

El investigador confirmó el estado del HPA y del Deployment (confianza **0.97**),
y ejecutó un plan de cuatro pasos que combina **dos mitigaciones complementarias**
— marcando cada una, en su propio texto, como táctica:

1. **`patch_hpa`** (maxReplicas 3 → 5):
   > "this is a tactical band-aid only — the workload will still saturate CPU on
   > every replica."
2. **`patch_deployment_resources`** (CPU request/límite 100m/200m → 200m/400m):
   > "Reduces CPU throttle saturation from 100% toward ~50–75%, giving the HPA a
   > more realistic signal […]."
3. **`verify_pods_ready`** — confirma convergencia tras los cambios.
4. **`inform_only`** — la cura real, separada de las mitigaciones:
   > "PERMANENT FIX REQUIRED — the automated actions above are tactical only. The
   > root cause is a deliberately unbounded busy-loop shell script inside the
   > container. The team must (a) eliminate or throttle the busy-loop […] or (b)
   > accept that this workload is intentionally CPU-hungry […]. Without
   > addressing the loop itself, the HPA will continue to be pinned."

El diagnóstico tiene dos dimensiones que Autopilot atacó con palancas distintas:
no había headroom horizontal (lo resuelve subir maxReplicas) **y** cada pod
estaba al 100% de throttle (lo alivia subir el límite de CPU, que además le da al
HPA una señal de utilización más realista). Las dos mitigaciones se complementan,
y aun así el agente fue explícito en que ninguna es la solución definitiva.

### Resultado

Las cuatro acciones terminaron con éxito y la verificación pasó. Un HPA que
estuvo 12+ días en su tope quedó mitigado en una sola ejecución — con la
advertencia clara de que el problema reaparecerá si no se corrige el busy-loop en
el código.

El comportamiento que distingue este caso: Autopilot **ejecutó una acción y, en
la misma frase, declaró que esa acción no es la cura.** No reportó "resuelto" tras
aplicar el parche. Una herramienta que sube maxReplicas y cierra el incidente
dejaría al operador con el throttling de vuelta en horas y sin saber por qué.

En una variante del mismo escenario, Autopilot tomó un camino más conservador
(subió maxReplicas con un `targetCpuUtilization` ajustado) y advirtió que la cura
real — subir el límite de CPU sin tope — **podría ser peligrosa**: arriesga un
consumo descontrolado de recursos a nivel de nodo, por lo que no la automatiza.
Reconocer que una acción es demasiado arriesgada para tomarla sin un humano es
parte del mismo criterio.

### Action items del postmortem

- Eliminar o limitar el busy-loop en el código de la aplicación (la causa raíz).
- O bien aceptar que el workload es intencionalmente intensivo en CPU y mantener
  `maxReplicas` y los límites de CPU permanentemente elevados.
- El postmortem también dejó registrado que el HPA estuvo más de 12 días en su
  tope sin que nadie lo notara — un caso típico de alerta que existe pero que
  nadie escala.

---

## Bonus — Rollout atascado: cuando el síntoma no es la causa

Un caso adicional que merece atención aparte, porque muestra a Autopilot
haciendo algo más sutil que resolver: **corregir la propia hipótesis de
triage**.

### Escenario

Un Deployment quedó atascado con la condición `Progressing=False`,
`reason=ProgressDeadlineExceeded` — el rollout no avanzó dentro de su
`progressDeadlineSeconds` (fijado, además, en unos agresivos 30s). El pod usaba
una imagen inexistente (`nginx:does-not-exist-progress-2099`), así que el síntoma
visible apuntaba a un `ImagePullBackOff`. Pero había un segundo problema
escondido: el namespace tenía un `ResourceQuota` que exige `limits.cpu` y
`limits.memory` en cada contenedor, y el spec solo declaraba `requests`.

### Cómo razonó Autopilot

El triage propuso la hipótesis obvia (problema de imagen). El investigador la
**refutó parcialmente** con la evidencia (confianza **0.95**):

> "The triage hypothesis is **partially correct but missing the primary
> blocker**. The rollout deadline was exceeded […], but the root cause is NOT an
> ImagePullBackOff — zero pods were ever scheduled. Every pod creation attempt
> was immediately rejected by the `autopilot-generous` ResourceQuota, which
> requires `limits.cpu` and `limits.memory` to be set. The container spec only
> has `requests` […]. The bad image […] is a secondary concern that would
> surface only after fixing the quota violation."

El razonamiento clave es de **orden de causalidad**: el síntoma observable era la
imagen, pero los eventos contaban otra historia. El investigador leyó los eventos
del ReplicaSet (10 advertencias `FailedCreate` con `forbidden: failed quota`),
el YAML (bloque `limits` ausente) y el `describe` (0 pods programados, no 1 pod
fallando al arrancar). Esa distinción — *ningún pod llegó a crearse* vs. *un pod
arranca y falla* — es la que descarta `ImagePullBackOff` como causa primaria: no
se puede tener un error de pull de un pod que nunca se programó.

El plan tuvo tres pasos, secuenciados según ese orden de causalidad:

1. **`patch_deployment_resources`** (añade `limits` cpu 100m / memoria 64Mi) —
   "Pod admission is unblocked by the ResourceQuota; the ReplicaSet will attempt
   to schedule the pod." Desbloquea el bloqueador primario.
2. **`inform_only`** — predice el segundo problema antes de que aparezca:
   > "Once the quota patch lands the pod will be admitted but will immediately
   > enter ImagePullBackOff because the image tag […] does not exist. A human
   > operator must update […] the image to a valid tag […]. Autopilot cannot
   > mutate image tags — this is outside the action whitelist."
3. **`verify_pods_ready`** — y, de forma deliberada, anticipa que esta
   verificación **fallará**: "Expected to report ImagePullBackOff until the
   operator applies a valid image tag — the verify result will make that state
   visible."

### Resultado

El patch de quota se aplicó con éxito; la verificación reportó `verificationPassed:
False` — exactamente lo previsto, porque el pod ya admitido entró en
`ImagePullBackOff` por la imagen inexistente. Eso **no es un fallo del plan**: es
el agente desbloqueando lo que podía arreglar (la admisión por quota, dentro de
su whitelist de acciones) y dejando explícito lo que no puede (mutar el tag de
imagen, fuera de su whitelist), con la verificación haciendo visible ese estado
en lugar de ocultarlo.

Lo interesante de este caso no es el "fix" — es que Autopilot **no se dejó llevar
por el síntoma más llamativo.** Una herramienta que confía en la etiqueta del
triage habría perseguido la imagen y nunca habría desbloqueado el quota, que era
el verdadero primer dominó.

### Action items del postmortem

- Actualizar la imagen del Deployment a un tag válido (la causa secundaria que
  requiere intervención humana).
- Añadir un `LimitRange` al namespace que inyecte `limits` por defecto, para que
  los workloads escritos sin límites no choquen contra el ResourceQuota.
- Subir `progressDeadlineSeconds` de 30 hacia el default de 600 — 30s es
  demasiado ajustado para absorber un arranque normal.
- Documentar el contrato quota-vs-limits del namespace en el runbook.
- Añadir un lint de CI (kube-score / polaris) que bloquee PRs con Deployments sin
  `resources.limits`.
- Extender la propia regla de triage para que, cuando exista `ReplicaFailure=True
  (FailedCreate)`, lo presente como hipótesis principal — de modo que los fallos
  de quota/admisión no queden eclipsados por el síntoma más visible.

---

## Lectura transversal

Los tres incidentes comparten síntomas superficiales (dos son `CrashLoopBackOff`),
pero Autopilot tomó tres decisiones opuestas, cada una apoyada en evidencia
recogida en vivo — logs, eventos, historial de revisiones y métricas:

- En **A** no actuó, porque ningún fix de infraestructura puede reparar un bug de
  código, y lo dijo con alta confianza.
- En **B** actuó (rollback con aprobación) porque tenía una mitigación real a
  mano, y por separado entregó la causa raíz.
- En **C** actuó con dos mitigaciones, pero rechazó vender el parche como
  solución y marcó la cura definitiva como trabajo humano — además de declinar
  una acción que habría sido demasiado arriesgada sin supervisión.

La diferencia entre "automatización" y lo que estos casos muestran es esa: no
aplicar un patrón fijo al síntoma, sino razonar caso por caso, distinguir alivio
de cura, y ser explícito sobre el límite de lo que el agente debe hacer por sí
solo. Ese criterio es la condición para que un equipo le confíe acceso a
producción.
