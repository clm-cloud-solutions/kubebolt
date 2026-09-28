package insights

import (
	"strconv"
	"testing"
	"time"
)

// Medido contra producción el 20-sep-2026: de 70 ráfagas, 67 eran
// unknown_burst y 56 no contenían NI UN malfuncionamiento — estaban hechas de
// policy-orphan (48 ráfagas), resource-underrequest (35), pdb-no-match (19) y
// policy-no-match (18). Su dispersión de inicio era 0,0 min, que es la firma
// de que no pasó nada: el tick de evaluación sella todas las expectativas con
// el mismo first_seen. Filtrar por clase las deja en 11.

func TestIsOperationalSignal(t *testing.T) {
	for _, rule := range []string{"node-not-ready", "evicted-pods", "oom-killed", "crash-loop"} {
		if !IsOperationalSignal(rule) {
			t.Errorf("%s es un malfuncionamiento y debe poder formar ráfaga", rule)
		}
	}
	for _, rule := range []string{"policy-orphan", "resource-underrequest", "pdb-no-match", "policy-no-match"} {
		if IsOperationalSignal(rule) {
			t.Errorf("%s es estado de configuración, no un suceso", rule)
		}
	}
	// Una regla sin ficha entra: el catálogo es la fuente de verdad de todo el
	// sistema de políticas, así que una que falte ya está rota en otros sitios.
	// Excluirla aquí la volvería invisible sin que nadie se entere.
	if !IsOperationalSignal("regla-que-alguien-anadio-y-no-catalogo") {
		t.Error("una regla desconocida debe incluirse, no desaparecer en silencio")
	}
}

// El caso que motivó separar las dos fechas: una rotación de nodos que rompió
// todo en 4 minutos y cuyos miembros seguían firing semanas después se
// mostraba como «ventana de 69 días».
func TestClusterEpisodes_OnsetIsSeparateFromLastActivity(t *testing.T) {
	t0 := time.Date(2026, 9, 16, 3, 5, 3, 0, time.UTC)
	var eps []Episode
	eps = append(eps, opEp("seed", "cl-1", "node-not-ready", "Node/_/n1", t0, EpisodeFiring, "", 69*24*time.Hour))
	for i := 0; i < 5; i++ {
		eps = append(eps, opEp("m"+strconv.Itoa(i), "cl-1", "evicted-pods", "Pod/ns/p"+strconv.Itoa(i),
			t0.Add(time.Duration(i+1)*time.Minute), EpisodeFiring, "", 69*24*time.Hour))
	}
	ops := ClusterEpisodes("org-1", eps)
	if len(ops) != 1 {
		t.Fatalf("ráfagas = %d, esperaba 1", len(ops))
	}
	op := ops[0]

	// El inicio: cuándo ROMPIÓ. Minutos.
	if onset := op.OnsetTo.Sub(op.WindowFrom); onset != 5*time.Minute {
		t.Errorf("inicio de %v, esperaba 5m — es la dispersión de los first_seen", onset)
	}
	// La última actividad: hasta cuándo se la vio. Semanas.
	if activity := op.WindowTo.Sub(op.WindowFrom); activity < 60*24*time.Hour {
		t.Errorf("última actividad a %v del inicio, esperaba ~69d", activity)
	}
	// Y la confusión que esto elimina: las dos no pueden ser el mismo número.
	if op.OnsetTo.Equal(op.WindowTo) {
		t.Error("inicio y última actividad colapsaron — es justo lo que hacía parecer la ráfaga de 69 días")
	}
	if op.Blast.StillFiring != 6 {
		t.Errorf("stillFiring = %d, esperaba 6", op.Blast.StillFiring)
	}
}

// Antes, las expectativas que caían en la misma ventana engordaban el blast
// radius: «48 afectados» cuando 20 de ellos eran namespaces sin NetworkPolicy
// que llevaban meses así.
func TestClusterEpisodes_ExpectationsDoNotInflateTheBlast(t *testing.T) {
	t0 := time.Now().Add(-3 * time.Hour).Truncate(time.Second)
	var eps []Episode
	eps = append(eps, opEp("seed", "cl-1", "node-not-ready", "Node/_/n1", t0, EpisodeResolved, ResolutionAutoRecovered, time.Hour))
	for i := 0; i < 5; i++ {
		eps = append(eps, opEp("m"+strconv.Itoa(i), "cl-1", "evicted-pods", "Pod/ns/p"+strconv.Itoa(i),
			t0.Add(time.Minute), EpisodeResolved, ResolutionAutoRecovered, time.Hour))
	}
	// Ruido de configuración que cae en la misma ventana por pura coincidencia
	// del tick de evaluación.
	for i := 0; i < 20; i++ {
		eps = append(eps, opEp("x"+strconv.Itoa(i), "cl-1", "policy-orphan", "Namespace/ns"+strconv.Itoa(i)+"/ns"+strconv.Itoa(i),
			t0.Add(2*time.Minute), EpisodeFiring, "", 40*24*time.Hour))
	}
	ops := ClusterEpisodes("org-1", eps)
	if len(ops) != 1 {
		t.Fatalf("ráfagas = %d, esperaba 1", len(ops))
	}
	op := ops[0]
	if op.Kind != OpKindNodeRotation {
		t.Errorf("kind = %s, esperaba node_rotation", op.Kind)
	}
	if op.Blast.Affected != 6 {
		t.Errorf("afectados = %d, esperaba 6 — las 20 expectativas no rompieron nada", op.Blast.Affected)
	}
	if op.Blast.StillFiring != 0 {
		t.Errorf("stillFiring = %d, esperaba 0 — lo que seguía firing era configuración", op.Blast.StillFiring)
	}
	// Y sin el filtro, el ruido crónico habría estirado la última actividad a 40 días.
	if op.WindowTo.Sub(op.WindowFrom) > 2*time.Hour {
		t.Errorf("última actividad a %v: una expectativa crónica se coló en la ventana", op.WindowTo.Sub(op.WindowFrom))
	}
}

// Por debajo de MinBurst tras filtrar, no hay ráfaga: agrupar dos cosas es
// inventar estructura, y antes el relleno de expectativas llegaba al mínimo.
func TestClusterEpisodes_ExpectationsCannotReachMinBurst(t *testing.T) {
	t0 := time.Now().Add(-time.Hour).Truncate(time.Second)
	var eps []Episode
	for i := 0; i < 2; i++ {
		eps = append(eps, opEp("m"+strconv.Itoa(i), "cl-1", "oom-killed", "Pod/ns/p"+strconv.Itoa(i),
			t0.Add(time.Duration(i)*time.Minute), EpisodeFiring, "", time.Hour))
	}
	for i := 0; i < 6; i++ {
		eps = append(eps, opEp("x"+strconv.Itoa(i), "cl-1", "pdb-no-match", "PDB/ns/b"+strconv.Itoa(i),
			t0.Add(time.Duration(i)*time.Minute), EpisodeFiring, "", time.Hour))
	}
	if ops := ClusterEpisodes("org-1", eps); len(ops) != 0 {
		t.Errorf("formó %d ráfaga(s) con 2 malfuncionamientos y 6 expectativas de relleno", len(ops))
	}
}
