package api

import (
	"net/http"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/insights"
)

// GET /insights/summary — el estado REAL de cada cluster, para toda la flota.
//
// Por qué existe: hasta ahora el único camino a los insights era `/insights`,
// que vive dentro de `requireConnector` y arranca con `if eng == nil → 503`.
// Así que Fleet y Home sólo podían hablar del ENLACE —si el agente reporta— y
// acababan diciendo «Healthy» de un cluster cuyo propio Overview decía
// «warning». Dos preguntas distintas con la misma palabra, que es la peor forma
// de contradicción porque parecen la misma.
//
// El engine necesita connector; el STORE no. Persiste por (tenant, cluster,
// fingerprint) y sobrevive a reinicios, así que un resumen por cluster se lee
// sin conectar a ninguno — que es justo la promesa de la vista de flota.
//
// Y lo que importa más que el endpoint: sale del MISMO sitio donde el Overview
// calcula su salud. No es una segunda forma de deducirla que pueda discrepar
// —derivarla de métricas de VM, por ejemplo— sino el mismo dato leído desde
// otra puerta. Dos definiciones de «sano» habrían reproducido el bug con más
// pasos.

// insightSummaryResponse es cluster_id → severidad → nº de insights ACTIVOS.
// Misma forma que `bySeverityCluster` de findings, a propósito: el frontend ya
// sabe consumirla y las dos superficies se leen igual.
type insightSummaryResponse struct {
	BySeverityCluster map[string]map[string]int `json:"bySeverityCluster"`
	// Total por severidad en el alcance del llamante — evita que cada cliente
	// tenga que sumar el mapa para pintar un KPI de flota.
	BySeverity map[string]int `json:"bySeverity"`
}

func (h *handlers) handleInsightsSummary(w http.ResponseWriter, r *http.Request) {
	store := h.manager.InsightStore()
	if store == nil {
		// Sin persistencia el resumen no existe. Se responde 200 con mapas
		// vacíos y NO 503: la flota se pinta igual, sólo sin insignia de salud.
		// Un 503 aquí tumbaría la página entera por un adorno.
		respondJSON(w, http.StatusOK, insightSummaryResponse{
			BySeverityCluster: map[string]map[string]int{},
			BySeverity:        map[string]int{},
		})
		return
	}

	// Alcance ya resuelto por el middleware (cluster_scope.go): org + equipo.
	scope := ClusterScopeFrom(r.Context())
	if scope.EntitledToNothing() {
		respondJSON(w, http.StatusOK, insightSummaryResponse{
			BySeverityCluster: map[string]map[string]int{},
			BySeverity:        map[string]int{},
		})
		return
	}

	records, err := store.List(insights.InsightQuery{
		TenantID: h.activeTenantID(r),
		// Sólo lo ACTIVO. Un insight resuelto es historia, y contarlo pintaría
		// de ámbar un cluster que ya se arregló — el error que la propia
		// ingeniería de resolución existe para evitar.
		Status:    "active",
		ClusterID: scope.Requested(),
	})
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to summarize insights")
		return
	}

	resp := insightSummaryResponse{
		BySeverityCluster: map[string]map[string]int{},
		BySeverity:        map[string]int{},
	}
	for _, rec := range records {
		if !scope.May(rec.ClusterID) {
			continue
		}
		per := resp.BySeverityCluster[rec.ClusterID]
		if per == nil {
			per = map[string]int{}
			resp.BySeverityCluster[rec.ClusterID] = per
		}
		per[rec.Severity]++
		resp.BySeverity[rec.Severity]++
	}
	respondJSON(w, http.StatusOK, resp)
}

// activeTenantIDOrEmpty documenta por qué el TenantID va tal cual: en OSS es
// vacío (un solo tenant, nada que filtrar) y en multi-tenant es el org del
// llamante, resuelto por ResolveTenant. Es el mismo discriminador que usan los
// handlers de métricas y de findings — una sola definición de «mi organización».
var _ = auth.ContextTenantID
