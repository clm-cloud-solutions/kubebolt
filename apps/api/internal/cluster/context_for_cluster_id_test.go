package cluster

import (
	"context"
	"testing"
)

// El bug: ContextNameForClusterID sólo miraba el mapa en memoria de agent-proxy,
// donde el nombre de contexto ES `agent:<uid>`. Un clúster DIRECTO —`in-cluster`,
// o una entrada de kubeconfig— no está ahí, y su UID vive únicamente en el mapa
// persistido, así que la función devolvía el UID tal cual.
//
// Río abajo eso no falla: miente. finding_detail lo usaba como clave de rutado,
// ningún runtime responde a ese nombre, y el panel concluía "cluster not
// connected" sobre un clúster que estaba vivo y sirviendo.
//
// Se ve justo en el clúster auto-monitorizado, porque el backend se salta a
// propósito el registro agent-proxy de su propio cluster_id para no duplicarlo
// (main.go, cluster-validation BUG-3). En SaaS eso es sólo la org del operador;
// en EE self-hosted es el clúster PRINCIPAL del cliente.
func TestContextNameForClusterID_ResolvesDirectContextFromPersistedUID(t *testing.T) {
	const uid = "bd891731-322e-4885-9cf6-feb804f70e00"

	m := newBareManager()
	m.storage = newBoltStorage(t)
	// Un clúster de agente, para que la primera vía tenga algo que NO coincida.
	m.agentProxyContexts = map[string]string{
		"agent:1dce102c-9c8b-4b22-a50d-706feb895c97": "1dce102c-9c8b-4b22-a50d-706feb895c97",
	}
	// Lo que el runtime persiste al resolver (manager.go, tras conectar).
	if err := m.storage.SetClusterUID(context.Background(), "in-cluster", uid); err != nil {
		t.Fatalf("SetClusterUID: %v", err)
	}

	got := m.ContextNameForClusterID("", uid)
	if got == uid {
		t.Fatalf("devolvió el UID como si fuera nombre de contexto (%q) — es la mentira "+
			"que acaba en \"cluster not connected\" sobre un clúster vivo", got)
	}
	if got != "in-cluster" {
		t.Fatalf("ContextNameForClusterID = %q, want \"in-cluster\"", got)
	}
}

// La primera vía no se rompe: un clúster de agente sigue resolviendo por el mapa
// en memoria, sin tocar el almacén.
func TestContextNameForClusterID_AgentProxyStillResolvesInMemory(t *testing.T) {
	const uid = "1dce102c-9c8b-4b22-a50d-706feb895c97"

	m := newBareManager()
	m.storage = nil // sin almacén: si lo necesitara, aquí reventaría
	m.agentProxyContexts = map[string]string{"agent:" + uid: uid}

	if got := m.ContextNameForClusterID("", uid); got != "agent:"+uid {
		t.Fatalf("ContextNameForClusterID = %q, want %q", got, "agent:"+uid)
	}
}

// Un UID que no conoce nadie sigue volviendo sin cambios. Se fija aquí a
// PROPÓSITO, aunque sea el comportamiento que queremos cambiar: devolver algo con
// forma de nombre de contexto convierte un fallo en una afirmación falsa, y
// account.go ya se defiende de ello con `!= clusterID` mientras finding_detail no
// lo hacía. Cambiarlo a "" toca a los tres llamadores y va en su propio commit —
// cuando ocurra, este test debe fallar y actualizarse, que es justo la señal.
func TestContextNameForClusterID_UnknownFallsBackToInput(t *testing.T) {
	m := newBareManager()
	m.storage = newBoltStorage(t)

	const unknown = "00000000-0000-0000-0000-000000000000"
	if got := m.ContextNameForClusterID("", unknown); got != unknown {
		t.Fatalf("ContextNameForClusterID = %q, want the input back (%q)", got, unknown)
	}
	if got := m.ContextNameForClusterID("", ""); got != "" {
		t.Fatalf("un cluster_id vacío debe devolver vacío, dio %q", got)
	}
}
