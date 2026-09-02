package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// El middleware existe porque el MISMO fallo se coló tres veces: una superficie
// nueva scopea por org y se olvida del equipo. Estos tests fijan la tabla de
// decisión completa, incluida la forma que tuvo la explotación real (nombrar
// explícitamente el cluster ajeno) y el caso que un arreglo de seguridad suele
// romper: sin estrechamiento, todo debe seguir leyéndose igual.

func scopeWith(requested string, allowed []string, narrowed bool) ClusterScope {
	s := ClusterScope{requested: requested, narrowed: narrowed}
	if narrowed {
		s.allowed = map[string]bool{}
		for _, id := range allowed {
			s.allowed[id] = true
		}
	}
	return s
}

func TestClusterScope_NoNarrowingReadsEverything(t *testing.T) {
	// OSS, sin org, o admin. Un fix que deja la página en blanco en cada
	// instalación de un solo tenant no es un fix.
	s := scopeWith("", nil, false)
	for _, id := range []string{"a", "b", "", "cualquiera"} {
		if !s.May(id) {
			t.Errorf("sin estrechamiento se negó %q", id)
		}
	}
	if s.EntitledToNothing() {
		t.Error("sin estrechamiento no puede ser 'sin derecho a nada'")
	}
}

func TestClusterScope_NarrowedReadsOnlyEntitled(t *testing.T) {
	s := scopeWith("", []string{"mine-1", "mine-2"}, true)
	if !s.May("mine-1") || !s.May("mine-2") {
		t.Error("se negó un cluster propio")
	}
	if s.May("otro-equipo") {
		t.Error("se sirvió el cluster de otro equipo")
	}
}

func TestClusterScope_ExplicitForeignClusterReadsNothing(t *testing.T) {
	// La forma exacta de la explotación de S-6: pedir el cluster del otro
	// equipo por su id. Devuelve VACÍO y no 403 — `/clusters` oculta ese
	// cluster, así que un 403 confirmaría que existe. La ausencia tiene que ser
	// consistente entre superficies o no es una ausencia.
	s := scopeWith("otro-equipo", []string{"mine-1"}, true)
	if s.May("otro-equipo") {
		t.Error("se sirvió el cluster ajeno pedido explícitamente")
	}
	if s.May("mine-1") {
		t.Error("pedir un cluster ajeno no debe abrir los propios")
	}
}

func TestClusterScope_ExplicitOwnClusterNarrowsToIt(t *testing.T) {
	s := scopeWith("mine-1", []string{"mine-1", "mine-2"}, true)
	if !s.May("mine-1") {
		t.Error("se negó el cluster propio pedido")
	}
	if s.May("mine-2") {
		t.Error("pedir uno concreto debe excluir los demás")
	}
}

func TestClusterScope_EntitledToNothingFailsClosed(t *testing.T) {
	// Membresía irresoluble o equipo sin clusters. Debe leer CERO, nunca todo:
	// fallar abierto aquí es la diferencia entre una página vacía y una fuga.
	s := scopeWith("", nil, true)
	if !s.EntitledToNothing() {
		t.Fatal("un conjunto vacío estrechado debe ser 'sin derecho a nada'")
	}
	if s.May("cualquiera") {
		t.Error("sin derecho a nada se sirvió algo")
	}
}

func TestClusterScope_MiddlewarePopulatesFromQuery(t *testing.T) {
	h := &handlers{}
	var got ClusterScope
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = ClusterScopeFrom(r.Context())
	})
	req := httptest.NewRequest(http.MethodGet, "/findings?cluster=abc", nil)
	h.WithClusterScope(next).ServeHTTP(httptest.NewRecorder(), req)

	if got.Requested() != "abc" {
		t.Errorf("el middleware no recogió ?cluster= : %q", got.Requested())
	}
}

// AllowedIDs alimenta a un store REMOTO —Autopilot— que necesita la lista por
// adelantado. Ahí la distinción «sin estrechar» vs «derecho a ninguno» deja de
// estar implícita en un predicado y viaja por la red, que es donde se pierde.

func TestClusterScope_AllowedIDs_NotNarrowedYieldsNoFilter(t *testing.T) {
	ids, narrowed := scopeWith("", nil, false).AllowedIDs()
	if narrowed {
		t.Fatal("sin estrechamiento no debe pedirse filtro")
	}
	if ids != nil {
		t.Errorf("se devolvió una lista (%v) donde no hay filtro; el receptor la aplicaría", ids)
	}
}

func TestClusterScope_AllowedIDs_EntitledToNothingIsNotNoFilter(t *testing.T) {
	// El caso que hay que no estropear: lista vacía CON narrowed=true. Si el
	// segundo valor se perdiera, el receptor leería «sin filtro» y serviría la
	// organización entera a quien no tiene derecho a nada.
	ids, narrowed := scopeWith("", nil, true).AllowedIDs()
	if !narrowed {
		t.Fatal("derecho a ninguno debe seguir pidiendo filtro")
	}
	if len(ids) != 0 {
		t.Errorf("AllowedIDs = %v, se esperaba vacío", ids)
	}
}

func TestClusterScope_AllowedIDs_SortedAndOnlyEntitled(t *testing.T) {
	ids, narrowed := scopeWith("", []string{"zeta", "alpha", "mid"}, true).AllowedIDs()
	if !narrowed {
		t.Fatal("debería estar estrechado")
	}
	// Ordenado para que la cabecera sea idéntica entre peticiones: si bailara,
	// cualquier caché intermedia trataría la misma consulta como distinta.
	if strings.Join(ids, ",") != "alpha,mid,zeta" {
		t.Errorf("AllowedIDs = %v, se esperaba orden estable alpha,mid,zeta", ids)
	}
}

func TestClusterScope_AllowedIDs_ForeignRequestCollapsesToEmpty(t *testing.T) {
	// Pedir explícitamente un cluster ajeno no puede colar ese id en la lista, y
	// tampoco debe abrir los propios. Se deriva de May justo para que esta regla
	// no tenga que reescribirse aquí y divergir.
	ids, narrowed := scopeWith("otro-equipo", []string{"mine-1", "mine-2"}, true).AllowedIDs()
	if !narrowed || len(ids) != 0 {
		t.Errorf("AllowedIDs = %v (narrowed=%v), se esperaba vacío", ids, narrowed)
	}
}

func TestClusterScope_AllowedIDs_ExplicitOwnClusterNarrowsToIt(t *testing.T) {
	ids, _ := scopeWith("mine-1", []string{"mine-1", "mine-2"}, true).AllowedIDs()
	if strings.Join(ids, ",") != "mine-1" {
		t.Errorf("AllowedIDs = %v, se esperaba sólo mine-1", ids)
	}
}

func TestClusterScopeFrom_ZeroValueDoesNotNarrow(t *testing.T) {
	// Una ruta sin el middleware montado —OSS, o una que no sirva datos por
	// cluster— debe seguir funcionando. La protección real la da montar el
	// middleware en el grupo correcto, no este default.
	s := ClusterScopeFrom(context.Background())
	if s.Narrowed() {
		t.Error("el cero estrecha; rompería toda ruta sin middleware")
	}
	if !s.May("lo-que-sea") {
		t.Error("el cero niega; dejaría páginas en blanco")
	}
}
