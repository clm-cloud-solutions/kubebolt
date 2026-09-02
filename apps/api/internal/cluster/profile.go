package cluster

import (
	"sync"
	"time"
)

// El perfil de un cluster: proveedor cloud, región y versión de Kubernetes.
//
// Existe porque la LISTA de clusters no lo tenía y la vista de flota lo necesita
// para cada cluster, no sólo para el activo.
//
// El dato ya se calculaba —`detectCloudProvider` sobre las etiquetas y el
// providerID de los nodos, más `serverVersionCached`— pero sólo al construir el
// overview, que se construye para UN cluster. La flota pinta a propósito sin
// conectar a ninguno, así que sin caché el icono aparecía o no según qué cluster
// hubieras visitado por última vez: peor que no tenerlo, porque un indicador
// intermitente enseña a desconfiar de él.
//
// Es una CACHÉ en memoria, no persistencia. La distinción importa y define lo
// que esta pieza promete:
//
//	· Se llena sola en cuanto el runtime de un cluster está caliente, que para
//	  un cluster agent-proxy ocurre al registrarse su agente.
//	· Sobrevive al desalojo del pool —que era la causa real del parpadeo—,
//	  porque este mapa no es el pool.
//	· NO sobrevive a un reinicio de la API. Tras arrancar se repuebla conforme
//	  los agentes reconectan, en segundos; hasta entonces la flota muestra los
//	  clusters sin su perfil, que es exactamente lo que muestra hoy.
//
// Persistirlo en la fila de membresía sería lo siguiente si ese hueco de
// arranque llega a molestar; cuesta una migración y no cambia nada de esto.
type ClusterProfile struct {
	// Provider es "aws" / "gcp" / "azure" / "kind" / "" cuando no se ha podido
	// determinar. Sale del providerID del nodo, no de una etiqueta que el
	// operador pueda poner a mano.
	Provider string
	Region   string
	// Version es la del servidor (v1.35.0), no la del kubelet: es la que
	// gobierna qué APIs existen, que es lo que el operador está preguntando.
	Version string
	// Platform refina el proveedor cuando la versión sola es ambigua (un AKS que
	// reporta una versión de Kubernetes vanilla).
	Platform string
	// At permite distinguir un perfil recién resuelto de uno viejo. No se usa
	// para caducar —el proveedor de un cluster no cambia— sino para diagnosticar
	// por qué una entrada no se refresca.
	At time.Time
}

// profileCache es el mapa cluster_id → perfil, con su propio candado.
//
// Candado propio y no el del Manager a propósito: se escribe desde la
// construcción del overview, que corre bajo el lock de lectura del Manager, y
// reusar aquel convertiría una lectura en una escritura y serializaría el
// camino caliente de todas las peticiones.
type profileCache struct {
	mu sync.RWMutex
	m  map[string]ClusterProfile
}

func newProfileCache() *profileCache {
	return &profileCache{m: map[string]ClusterProfile{}}
}

// remember guarda el perfil de un cluster. Ignora las llamadas sin cluster_id o
// completamente vacías: escribir un perfil en blanco borraría uno bueno de una
// resolución anterior, que es justo el parpadeo que esta caché evita.
func (p *profileCache) remember(clusterID string, prof ClusterProfile) {
	if clusterID == "" || (prof.Provider == "" && prof.Version == "") {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	prof.At = time.Now()
	// Fusiona en vez de sustituir: un cluster sin nodos visibles resuelve
	// versión pero no proveedor, y la resolución siguiente al revés. Quedarse
	// con la última entera perdería la mitad que ya se sabía.
	if old, ok := p.m[clusterID]; ok {
		if prof.Provider == "" {
			prof.Provider = old.Provider
			prof.Region = old.Region
		}
		if prof.Version == "" {
			prof.Version = old.Version
		}
		if prof.Platform == "" {
			prof.Platform = old.Platform
		}
	}
	p.m[clusterID] = prof
}

func (p *profileCache) get(clusterID string) (ClusterProfile, bool) {
	if clusterID == "" {
		return ClusterProfile{}, false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	prof, ok := p.m[clusterID]
	return prof, ok
}

// ProfileFor expone el perfil conocido de un cluster por su cluster_id (el UID
// de kube-system). Devuelve el cero cuando no se ha resuelto todavía — el
// llamante debe pintarlo como «no lo sabemos», nunca como un valor por defecto.
func (m *Manager) ProfileFor(clusterID string) ClusterProfile {
	if m == nil || m.profiles == nil {
		return ClusterProfile{}
	}
	prof, _ := m.profiles.get(clusterID)
	return prof
}

// RememberProfile registra el perfil resuelto de un cluster. La llama el camino
// que ya calcula estos valores (buildClusterOverview), de modo que no hay una
// segunda forma de deducir el proveedor que pueda discrepar de la primera.
func (m *Manager) RememberProfile(clusterID string, prof ClusterProfile) {
	if m == nil || m.profiles == nil {
		return
	}
	m.profiles.remember(clusterID, prof)
}
