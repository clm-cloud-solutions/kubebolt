package cluster

import "testing"

// Lo que se prueba no es el mapa: es que un icono de proveedor NUNCA parpadee.
// Un indicador que aparece y desaparece enseña a desconfiar de él, y entonces
// deja de servir aunque el dato sea correcto.

func TestProfileCache_RemembersAndServes(t *testing.T) {
	c := newProfileCache()
	c.remember("uid-1", ClusterProfile{Provider: "aws", Region: "us-east-1", Version: "v1.29.4"})

	got, ok := c.get("uid-1")
	if !ok {
		t.Fatal("el perfil recordado no se sirve")
	}
	if got.Provider != "aws" || got.Region != "us-east-1" || got.Version != "v1.29.4" {
		t.Errorf("perfil alterado al guardarlo: %+v", got)
	}
	if got.At.IsZero() {
		t.Error("At sin sellar: no se podría diagnosticar una entrada que no se refresca")
	}
}

func TestProfileCache_EmptyNeverErasesKnown(t *testing.T) {
	// El caso que causa el parpadeo. Un cluster cuyo agente se va deja de
	// resolver nodos, así que la siguiente construcción del overview trae
	// proveedor y versión vacíos. Si eso sobrescribiera, el icono desaparecería
	// justo cuando el operador está mirando por qué el cluster no responde.
	c := newProfileCache()
	c.remember("uid-1", ClusterProfile{Provider: "gcp", Region: "europe-west1", Version: "v1.28.0"})
	c.remember("uid-1", ClusterProfile{})

	got, _ := c.get("uid-1")
	if got.Provider != "gcp" || got.Version != "v1.28.0" {
		t.Errorf("un perfil vacío borró uno bueno: %+v", got)
	}
}

func TestProfileCache_MergesPartialResolutions(t *testing.T) {
	// Las dos mitades se resuelven por caminos distintos: la versión sale del
	// servidor y el proveedor de los nodos. Un cluster cuyos nodos aún no están
	// en la caché del informer resuelve una y no la otra, y la vez siguiente al
	// revés. Sustituir entero perdería la mitad ya sabida.
	c := newProfileCache()
	c.remember("uid-1", ClusterProfile{Version: "v1.30.1"})
	c.remember("uid-1", ClusterProfile{Provider: "azure", Region: "westeurope"})

	got, _ := c.get("uid-1")
	if got.Version != "v1.30.1" {
		t.Errorf("se perdió la versión al llegar el proveedor: %+v", got)
	}
	if got.Provider != "azure" || got.Region != "westeurope" {
		t.Errorf("no se aplicó el proveedor: %+v", got)
	}
}

func TestProfileCache_IgnoresMissingClusterID(t *testing.T) {
	// Un connector sin UID resuelto (kube-system aún no leído) llamaría con "".
	// Guardarlo crearía una entrada fantasma que ningún cluster reclama.
	c := newProfileCache()
	c.remember("", ClusterProfile{Provider: "aws"})
	if _, ok := c.get(""); ok {
		t.Error("se guardó un perfil sin cluster_id")
	}
}

func TestProfileCache_UnknownClusterIsAbsent(t *testing.T) {
	// La ausencia debe ser distinguible: el UI pinta «no lo sabemos», nunca un
	// proveedor por defecto.
	c := newProfileCache()
	if prof, ok := c.get("nunca-visto"); ok || prof.Provider != "" {
		t.Errorf("un cluster desconocido devolvió algo: %+v", prof)
	}
}

func TestManagerProfileFor_NilSafe(t *testing.T) {
	// La lista de clusters llama a esto para CADA fila. Un Manager a medio
	// construir —o el cero en un test— no puede tumbar la página entera de
	// flota por un icono.
	var m *Manager
	if got := m.ProfileFor("x"); got.Provider != "" {
		t.Errorf("un Manager nil devolvió perfil: %+v", got)
	}
	m2 := &Manager{}
	if got := m2.ProfileFor("x"); got.Provider != "" {
		t.Errorf("un Manager sin caché devolvió perfil: %+v", got)
	}
	m2.RememberProfile("x", ClusterProfile{Provider: "aws"}) // no debe entrar en pánico
}
