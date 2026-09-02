package insights

import (
	"context"
	"sort"
	"time"

	"github.com/google/uuid"
)

// Fase 3 (PR 3.2): el episodio OPERACIONAL — la capa que convierte «274
// hallazgos, 76 críticos» en «una rotación de nodos que se recuperó sola»
// (§3.3 del doc v2.1, el motor de #51).
//
// La agrupación es L1, CERO LLM, y eso es una promesa de producto: el parte
// de guardia tiene que poder decir «46 deployments, 45 recuperados en 49
// min, este fue el único que no» con aritmética reproducible, no con una
// inferencia que mañana cambia. Tres pasos deterministas:
//
//  1. Ventana de sesión: episodios cuyo first_seen cae a menos de SeedGap
//     del último del grupo pertenecen a la misma ráfaga. Una ráfaga con
//     menos de MinBurst episodios no es un evento operacional — se queda
//     como hallazgos sueltos.
//  2. Clasificación por semilla, en orden de precedencia: node-not-ready →
//     node_rotation; evicted-pods → node_pressure; ola de reglas de
//     despliegue sobre ≥ RolloutMinWorkloads workloads distintos →
//     mass_rollout; nada reconocible → unknown_burst (se admite no saber).
//  3. Blast radius por conteo sobre los miembros: afectados,
//     auto-recuperados, aún firing, expirados, y la duración del peor.
//
// Cross-cluster desde el día uno: la ráfaga se computa a nivel org (el caso
// Dipres cruzó dos clusters), y Clusters lista los UID involucrados.

const (
	OpKindNodeRotation = "node_rotation"
	OpKindMassRollout  = "mass_rollout"
	OpKindNodePressure = "node_pressure"
	OpKindUnknownBurst = "unknown_burst"
)

// Tunables — vars, no consts, para que los tests los encojan.
var (
	// SeedGap: separación máxima entre first_seen consecutivos dentro de la
	// misma ráfaga (ventana de sesión, no ventana fija: una rotación de
	// nodos gotea altas durante minutos).
	SeedGap = 5 * time.Minute
	// MinBurst: mínimo de episodios para que una ráfaga sea un evento
	// operacional. Por debajo, agrupar es inventar estructura.
	MinBurst = 5
	// RolloutMinWorkloads: workloads DISTINTOS con reglas de despliegue para
	// clasificar mass_rollout (muchas altas del mismo workload es un
	// crash-loop, no un rollout).
	RolloutMinWorkloads = 5
)

// seedRulesRotation / Pressure / Rollout: las reglas que delatan cada kind.
// La precedencia es rotación > presión > rollout: un drain masivo también
// produce evictions y reinicios — la causa más estructural gana.
var (
	seedRulesRotation = map[string]bool{"node-not-ready": true}
	seedRulesPressure = map[string]bool{"evicted-pods": true}
	seedRulesRollout  = map[string]bool{
		"progress-deadline-exceeded": true,
		"image-pull-backoff":         true,
		"readiness-probe-failing":    true,
		"crash-loop":                 true,
	}
)

// BlastStats: el radio de daño por aritmética. «Conteo sobre episodios, no
// razonamiento» (§3.3).
type BlastStats struct {
	Affected      int    `json:"affected"`      // recursos distintos
	AutoRecovered int    `json:"autoRecovered"` // resolved · auto_recovered
	Remediated    int    `json:"remediated"`    // resolved · remediated/manual/rule_changed
	StillFiring   int    `json:"stillFiring"`
	Expired       int    `json:"expired"`
	WorstSeconds  int64  `json:"worstSeconds"` // duración del episodio más largo
	WorstResource string `json:"worstResource"`
}

// OperationalEpisode es una ráfaga clasificada. El ID es determinista
// (uuid5 de org+kind+primer episodio semilla), así recomputar la misma
// ventana converge al mismo id en vez de acuñar uno nuevo por pasada.
type OperationalEpisode struct {
	ID         string     `json:"id"`
	TenantID   string     `json:"-"`
	Kind       string     `json:"kind"`
	Clusters   []string   `json:"clusters"`
	WindowFrom time.Time  `json:"windowFrom"`
	WindowTo   time.Time  `json:"windowTo"`
	SeedIDs    []string   `json:"seedIds"` // episodios que dispararon la clasificación
	MemberIDs  []string   `json:"memberIds"`
	Blast      BlastStats `json:"blast"`
}

// OperationalReader is what the API (and later the shift report) consumes.
// Implemented by the EE episode store; nil elsewhere.
type OperationalReader interface {
	// ClusterAndStore recomputes the org's operational episodes over the
	// window and persists them (delete-window + insert, deterministic ids →
	// convergent), returning the fresh set.
	ClusterAndStore(ctx context.Context, org string, from, to time.Time) ([]OperationalEpisode, error)
}

// Namespace fijo para los uuid5 de episodios operacionales — nunca cambiarlo:
// los ids derivados deben sobrevivir a recomputaciones y versiones.
var opEpisodeNS = uuid.MustParse("7b9e2f10-4c1d-4c60-9c30-8a2b6d1f0e42")

// opEpisodeID deriva el id determinista.
func opEpisodeID(org, kind, firstMemberID string) string {
	return uuid.NewSHA1(opEpisodeNS, []byte(org+"|"+kind+"|"+firstMemberID)).String()
}

// ClusterEpisodes agrupa y clasifica los episodios de UNA org (pura, sin
// I/O). Los episodios llegan como los da el store; se ordenan aquí.
func ClusterEpisodes(org string, eps []Episode) []OperationalEpisode {
	if len(eps) == 0 {
		return nil
	}
	sorted := make([]Episode, len(eps))
	copy(sorted, eps)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].FirstSeen.Before(sorted[j].FirstSeen) })

	var out []OperationalEpisode
	group := []Episode{sorted[0]}
	flush := func() {
		if op, ok := buildOperational(org, group); ok {
			out = append(out, op)
		}
	}
	for _, ep := range sorted[1:] {
		if ep.FirstSeen.Sub(group[len(group)-1].FirstSeen) <= SeedGap {
			group = append(group, ep)
			continue
		}
		flush()
		group = []Episode{ep}
	}
	flush()
	return out
}

func buildOperational(org string, group []Episode) (OperationalEpisode, bool) {
	if len(group) < MinBurst {
		return OperationalEpisode{}, false
	}
	kind, seeds := classifyBurst(group)
	if seeds == nil {
		// unknown_burst has no seeds — keep the slice non-nil so the
		// persistence layer encodes '{}' and not NULL (seed_ids is NOT NULL;
		// a nil here 500ed the endpoint on the first real noise burst).
		seeds = []string{}
	}
	op := OperationalEpisode{
		TenantID:   org,
		Kind:       kind,
		SeedIDs:    seeds,
		WindowFrom: group[0].FirstSeen,
		WindowTo:   group[0].LastSeen,
	}
	clusters := map[string]bool{}
	resources := map[string]bool{}
	for _, ep := range group {
		op.MemberIDs = append(op.MemberIDs, ep.ID)
		clusters[ep.ClusterID] = true
		resources[ep.ClusterID+"|"+ep.Resource] = true
		if ep.LastSeen.After(op.WindowTo) {
			op.WindowTo = ep.LastSeen
		}
		switch ep.Status {
		case EpisodeFiring:
			op.Blast.StillFiring++
		case EpisodeExpired:
			op.Blast.Expired++
		case EpisodeResolved:
			if ep.ResolutionKind == ResolutionAutoRecovered || ep.ResolutionKind == "" {
				op.Blast.AutoRecovered++
			} else {
				op.Blast.Remediated++
			}
		}
		if secs := episodeSeconds(ep); secs > op.Blast.WorstSeconds {
			op.Blast.WorstSeconds = secs
			op.Blast.WorstResource = ep.Resource
		}
	}
	op.Blast.Affected = len(resources)
	for c := range clusters {
		op.Clusters = append(op.Clusters, c)
	}
	sort.Strings(op.Clusters)
	op.ID = opEpisodeID(org, kind, op.MemberIDs[0])
	return op, true
}

// classifyBurst aplica la precedencia de semillas y devuelve el kind más los
// ids de los episodios que lo justifican (la evidencia del parte).
func classifyBurst(group []Episode) (string, []string) {
	var rotation, pressure, rollout []string
	rolloutWorkloads := map[string]bool{}
	for _, ep := range group {
		switch {
		case seedRulesRotation[ep.RuleID]:
			rotation = append(rotation, ep.ID)
		case seedRulesPressure[ep.RuleID]:
			pressure = append(pressure, ep.ID)
		case seedRulesRollout[ep.RuleID]:
			rollout = append(rollout, ep.ID)
			rolloutWorkloads[ep.ClusterID+"|"+ep.Resource] = true
		}
	}
	switch {
	case len(rotation) > 0:
		return OpKindNodeRotation, rotation
	case len(pressure) > 0:
		return OpKindNodePressure, pressure
	case len(rolloutWorkloads) >= RolloutMinWorkloads:
		return OpKindMassRollout, rollout
	default:
		return OpKindUnknownBurst, nil
	}
}

func episodeSeconds(ep Episode) int64 {
	end := ep.LastSeen
	if ep.ResolvedAt != nil {
		end = *ep.ResolvedAt
	}
	d := end.Sub(ep.FirstSeen)
	if d < 0 {
		return 0
	}
	return int64(d.Seconds())
}
