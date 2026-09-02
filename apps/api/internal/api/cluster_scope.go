package api

import (
	"context"
	"net/http"
	"sort"
)

// El alcance por cluster del que hace la petición, resuelto UNA vez por el
// middleware y guardado en el contexto.
//
// Por qué middleware y no un helper que cada handler llama.
//
// La versión anterior era `findingsClusterFilter`, una función que el handler
// tenía que acordarse de invocar. Eso es una convención, y las convenciones se
// olvidan: el MISMO fallo —una superficie nueva que scopea por ORG y se olvida
// del EQUIPO— se ha colado tres veces (S-2 en métricas, S-3 en el buscador,
// S-6 en el pilar de Security entero). Las tres compilaban, pasaban los tests y
// se veían bien en la UI, porque quien las probaba era un admin, que no está
// estrechado por nada.
//
// Un middleware invierte la carga: la ruta que se monta en el grupo scopeado
// llega con el alcance ya resuelto, y el handler tiene que hacer algo
// deliberado para IGNORARLO. No es infalible —nada obliga a llamar a May()—
// pero convierte «acordarse de añadir el filtro» en «acordarse de quitarlo»,
// que es un olvido mucho menos probable y mucho más visible en revisión.
type ClusterScope struct {
	// requested es el `?cluster=` que pidió el llamante, tal cual.
	requested string
	// allowed son los cluster_id que puede leer. nil = sin estrechar.
	allowed map[string]bool
	// narrowed distingue «no puede leer ninguno» (allowed vacío) de «no hay
	// estrechamiento» (OSS, sin org, admin). Sin este bool los dos casos son un
	// mapa vacío, y confundirlos abre la flota entera o la cierra del todo.
	narrowed bool
}

// May responde si una fila de este cluster puede mostrarse.
//
// Es la única pregunta que un handler necesita hacer, y la respuesta ya
// incorpora las tres reglas que se olvidan al escribirlas a mano: sin
// estrechamiento se lee todo; con estrechamiento sólo lo entitled; y un
// `?cluster=` explícito ajeno no devuelve 403 sino VACÍO, porque `/clusters`
// oculta ese cluster y un 403 confirmaría que existe.
func (s ClusterScope) May(clusterID string) bool {
	if !s.narrowed {
		return true
	}
	if s.requested != "" {
		return clusterID == s.requested && s.allowed[s.requested]
	}
	return s.allowed[clusterID]
}

// Requested es el cluster pedido, ya validado: cadena vacía cuando no se pidió
// ninguno. Se devuelve tal cual incluso si no está permitido — quien lo pase al
// store leerá cero filas, que es exactamente el resultado buscado.
func (s ClusterScope) Requested() string { return s.requested }

// AllowedIDs enumera los clusters legibles, para cuando el filtro no puede
// aplicarse fila a fila con May: un store remoto —Autopilot— que necesita la
// lista por adelantado para meterla en su WHERE.
//
// El segundo valor distingue las dos cosas que un slice vacío confunde: `false`
// = no hay estrechamiento, sirve todo; `true` con lista vacía = derecho a
// ninguno, no sirvas nada. Quien lo use tiene que propagar esa distinción hasta
// el store, porque «lista vacía» leída como «sin filtro» abre la flota entera.
//
// Se deriva de May y no de `allowed` directamente para que siga habiendo UNA
// definición de qué puedo leer, incluida la regla de que un `?cluster=` ajeno
// no abre nada. Ordenado para que la cabecera sea estable entre peticiones.
func (s ClusterScope) AllowedIDs() ([]string, bool) {
	if !s.narrowed {
		return nil, false
	}
	ids := make([]string, 0, len(s.allowed))
	for id := range s.allowed {
		if s.May(id) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids, true
}

// Narrowed indica si hay estrechamiento por equipo. Útil sólo para diagnóstico
// y para tests; un handler correcto no debería necesitarlo.
func (s ClusterScope) Narrowed() bool { return s.narrowed }

// EntitledToNothing es cierto cuando el llamante no puede leer ningún cluster
// —membresía irresoluble, o equipo sin clusters—. Permite cortocircuitar la
// lectura en vez de traer filas para descartarlas todas.
func (s ClusterScope) EntitledToNothing() bool {
	return s.narrowed && s.requested == "" && len(s.allowed) == 0
}

type clusterScopeKeyType struct{}

var clusterScopeKey clusterScopeKeyType

// WithClusterScope resuelve el alcance del llamante y lo deja en el contexto.
//
// Se monta sobre el grupo de rutas que sirven datos POR CLUSTER a nivel de
// organización. Resolverlo aquí y no en cada handler tiene un segundo efecto
// que importa: `allowedClusterIDs` consulta la tienda de propiedad y las
// membresías del usuario, así que hacerlo una vez por petición en vez de una
// vez por handler evita que un endpoint que lea dos fuentes pague el doble.
func (h *handlers) WithClusterScope(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ids, narrowed := h.allowedClusterIDs(r)
		scope := ClusterScope{
			requested: r.URL.Query().Get("cluster"),
			narrowed:  narrowed,
		}
		if narrowed {
			scope.allowed = make(map[string]bool, len(ids))
			for _, id := range ids {
				scope.allowed[id] = true
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), clusterScopeKey, scope)))
	})
}

// ClusterScopeFrom recupera el alcance resuelto.
//
// El cero —sin middleware montado— NO estrecha, y es deliberado: una ruta de
// OSS o una que no sirva datos por cluster debe seguir funcionando igual. La
// protección real la da montar el middleware en el grupo correcto, no este
// default; por eso hay un test que recorre las rutas de datos por cluster y
// exige que estén dentro del grupo.
func ClusterScopeFrom(ctx context.Context) ClusterScope {
	if s, ok := ctx.Value(clusterScopeKey).(ClusterScope); ok {
		return s
	}
	return ClusterScope{}
}
