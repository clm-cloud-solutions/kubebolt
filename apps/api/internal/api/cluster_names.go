package api

import (
	"net/http"

	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
)

// handleClusterNames sirve el mapa identificador → nombre legible de los
// clusters de la organización, INCLUIDOS los que ya no están dados de alta.
//
// Por qué hace falta un endpoint aparte de /clusters. Esa lista es la de los
// clusters VIVOS: alimenta el selector, y meter ahí los que ya no existen sería
// ofrecer entrar en algo que no está. Pero el consumo no desaparece cuando el
// cluster sí: las vistas de créditos son históricas y de organización, así que
// enseñan gasto de clusters dados de baja. Sin esto, esas filas salen con un
// guion — «alguien gastó 1.034 créditos en algún sitio», que es peor que no
// tener la columna, porque parece un fallo.
//
// El nombre sobrevive porque el registro es durable a propósito
// (cluster_display_names no se borra al desconectar). Esto sólo lo publica.
//
// Se sirve por AMBAS identidades. Los dos productos guardan cosas distintas:
// Kobi apunta el nombre de contexto de cuando ocurrió la sesión
// (`agent:<uid>`), y Autopilot el UID pelado, porque su almacén se indexa por
// lo que escribe su poller. Devolver una sola clave dejaría a uno de los dos
// sin resolver.
func (h *handlers) handleClusterNames(w http.ResponseWriter, r *http.Request) {
	out := map[string]string{}
	if h.manager == nil {
		respondJSON(w, http.StatusOK, out)
		return
	}
	store := h.manager.Storage()
	if store == nil {
		respondJSON(w, http.StatusOK, out)
		return
	}
	names, err := store.AllDisplayNames(r.Context())
	if err != nil {
		// No es fatal: sin nombres la vista pinta un guion, que es exactamente lo
		// que hacía antes. Tumbar la página por perder una etiqueta sería peor.
		respondJSON(w, http.StatusOK, out)
		return
	}
	scope := ClusterScopeFrom(r.Context())
	for contextName, display := range names {
		if display == "" {
			continue
		}
		// Mismo criterio que los datos: si no puedes ver el consumo de ese
		// cluster, tampoco su nombre. Un cluster dado de baja no tiene fila de
		// propiedad, así que para un usuario estrechado queda fuera por las dos
		// puertas a la vez — y eso es coherente, no una laguna: sus ejecuciones
		// tampoco le llegan.
		uid := cluster.RawClusterID(contextName)
		if !scope.May(uid) {
			continue
		}
		out[contextName] = display
		if uid != contextName {
			out[uid] = display
		}
	}
	respondJSON(w, http.StatusOK, out)
}
