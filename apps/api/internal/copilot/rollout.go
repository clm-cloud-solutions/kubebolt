package copilot

import (
	"fmt"
	"sort"
	"strconv"
)

// rolloutState is what a Deployment's revisions say about its rollout, worked
// out in code so the model reads it instead of deducing it: which revision is
// current, which are serving now, and where rollback_deployment would go.
//
// It exists because the deduction failed in practice: an investigator would
// see that the previous revision still had a ready pod — the stalled-rollout
// shape — and describe it, without concluding that a rollback restores it
// (the remedy misses the lab still showed).
type rolloutState struct {
	Current string `json:"current"`
	// Serving lists the revisions with ready pods right now, newest first.
	Serving        []string         `json:"serving"`
	RollbackTarget *rolloutRevision `json:"rollbackTarget,omitempty"`
	Summary        string           `json:"summary"`
}

type rolloutRevision struct {
	Revision string `json:"revision"`
	Image    string `json:"image,omitempty"`
	Serving  bool   `json:"serving"`
}

// summarizeRollout reads a Deployment's history rows (newest first or not).
// It answers only for Deployments: rollback_deployment exists only for them,
// and a StatefulSet's or DaemonSet's revisions carry no ready counts.
//
// The rollback target is the one rollback_deployment picks without
// toRevision: the most recent revision that is not the current one
// (cluster.Connector.RollbackDeployment, same as kubectl rollout undo).
func summarizeRollout(resourceType string, rows []map[string]interface{}) *rolloutState {
	if resourceType != "deployments" || len(rows) == 0 {
		return nil
	}
	type rev struct {
		n     int
		id    string
		image string
		ready int
	}
	revs := make([]rev, 0, len(rows))
	for _, r := range rows {
		id := fmt.Sprint(r["revision"])
		n, err := strconv.Atoi(id)
		if err != nil {
			continue
		}
		image, _ := r["image"].(string)
		revs = append(revs, rev{n: n, id: id, image: image, ready: intOf(r["readyReplicas"])})
	}
	if len(revs) == 0 {
		return nil
	}
	sort.Slice(revs, func(i, j int) bool { return revs[i].n > revs[j].n })

	cur := revs[0]
	st := &rolloutState{Current: cur.id, Serving: []string{}}
	for _, r := range revs {
		if r.ready > 0 {
			st.Serving = append(st.Serving, r.id)
		}
	}
	if len(revs) == 1 {
		st.Summary = fmt.Sprintf("Revision %s is the only one: there is nothing to roll back to.", cur.id)
		return st
	}
	tgt := revs[1]
	st.RollbackTarget = &rolloutRevision{Revision: tgt.id, Image: tgt.image, Serving: tgt.ready > 0}

	switch {
	case cur.ready == 0 && tgt.ready > 0:
		st.Summary = fmt.Sprintf("The rollout to revision %s has not finished: revision %s is still serving. "+
			"rollback_deployment returns to %s.", cur.id, tgt.id, tgt.id)
	case cur.ready == 0 && len(st.Serving) > 0:
		st.Summary = fmt.Sprintf("Revision %s is not serving, and neither is %s, where rollback_deployment returns "+
			"by default. Revision %s is still serving: rollback_deployment with toRevision=%s returns to it.",
			cur.id, tgt.id, st.Serving[0], st.Serving[0])
	case cur.ready == 0:
		st.Summary = fmt.Sprintf("No revision is serving. rollback_deployment returns to %s; whether it ran "+
			"healthy comes from the events and logs of its time, not from its counts.", tgt.id)
	case tgt.ready > 0:
		st.Summary = fmt.Sprintf("The rollout to revision %s is in progress: %s is serving too. "+
			"rollback_deployment returns to %s.", cur.id, tgt.id, tgt.id)
	default:
		st.Summary = fmt.Sprintf("Revision %s is serving. rollback_deployment returns to %s, which was replaced: "+
			"its 0 ready replicas are current, not how it ran.", cur.id, tgt.id)
	}
	return st
}

// intOf reads a count that is an int32 from the live connector and a float64
// once it has been through JSON (a recorded history).
func intOf(v interface{}) int {
	switch n := v.(type) {
	case int:
		return n
	case int32:
		return int(n)
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}
