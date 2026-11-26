package kobiplan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	corev1 "k8s.io/api/core/v1"
)

const hashPrefix = "sha256:"

// SpecHash is the step's precondition: SHA-256 of the canonical JSON of the
// image and the resources of every container of the pod template, taken when
// the step is created. When it changes and the change is not the step's own,
// the step expires: someone changed the workload and the evidence is stale.
//
// Canonical means it does not move with the order of the containers or the
// spelling of a quantity: "1000m" and "1" are the same CPU.
func SpecHash(spec corev1.PodSpec) string {
	type entry struct {
		Name     string            `json:"name"`
		Init     bool              `json:"init,omitempty"`
		Image    string            `json:"image"`
		Requests map[string]string `json:"requests,omitempty"`
		Limits   map[string]string `json:"limits,omitempty"`
	}
	entries := make([]entry, 0, len(spec.Containers)+len(spec.InitContainers))
	add := func(cs []corev1.Container, init bool) {
		for _, c := range cs {
			entries = append(entries, entry{
				Name: c.Name, Init: init, Image: c.Image,
				Requests: canonicalQuantities(c.Resources.Requests),
				Limits:   canonicalQuantities(c.Resources.Limits),
			})
		}
	}
	add(spec.Containers, false)
	add(spec.InitContainers, true)
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Init != entries[j].Init {
			return !entries[i].Init
		}
		return entries[i].Name < entries[j].Name
	})
	return hashJSON(entries)
}

func canonicalQuantities(rl corev1.ResourceList) map[string]string {
	if len(rl) == 0 {
		return nil
	}
	out := make(map[string]string, len(rl))
	for name, q := range rl {
		out[string(name)] = q.String() // Quantity.String is canonical
	}
	return out
}

// ContentHash is the hash of what the plan proposes, without its metadata, so
// two versions can be compared: the same steps on the same objects with the
// same values give the same hash whatever the status, version, dates,
// exports, notes, baseline or measured impact.
//
// Estimated savings are left out on purpose: they move with the node prices,
// not with what the plan proposes.
func ContentHash(p Plan) string {
	type targetView struct {
		Type      string `json:"type"`
		Namespace string `json:"namespace,omitempty"`
		Name      string `json:"name"`
		Owner     *Owner `json:"owner,omitempty"`
	}
	type stepView struct {
		Action           Action          `json:"action"`
		Target           targetView      `json:"target"`
		ExportOnly       bool            `json:"exportOnly"`
		ExportOnlyReason string          `json:"exportOnlyReason,omitempty"`
		Params           json.RawMessage `json:"params,omitempty"`
		SpecHash         string          `json:"specHash"`
		Risk             RiskLevel       `json:"risk"`
		Imposed          bool            `json:"imposed"`
	}
	type excludedView struct {
		Target targetView      `json:"target"`
		Reason ExclusionReason `json:"reason"`
	}
	type engineView struct {
		Name            string  `json:"name"`
		Percentile      float64 `json:"percentile,omitempty"`
		MinHistoryHours int     `json:"minHistoryHours,omitempty"`
		LowConfidence   bool    `json:"lowConfidence"`
	}
	view := struct {
		Kind      Kind           `json:"kind"`
		ClusterID string         `json:"clusterId"`
		Scope     Scope          `json:"scope"`
		Engine    engineView     `json:"engine"`
		Steps     []stepView     `json:"steps"`
		Excluded  []excludedView `json:"excluded"`
	}{
		Kind: p.Kind, ClusterID: p.ClusterID, Scope: canonicalScope(p.Scope),
		Engine:   engineView{p.Engine.Name, p.Engine.Percentile, p.Engine.MinHistoryHours, p.Engine.LowConfidence},
		Steps:    []stepView{},
		Excluded: []excludedView{},
	}
	steps := append([]Step(nil), p.Steps...)
	sort.SliceStable(steps, func(i, j int) bool { return steps[i].Order < steps[j].Order })
	for _, s := range steps {
		view.Steps = append(view.Steps, stepView{
			Action:     s.Action,
			Target:     targetView{s.Target.Type, s.Target.Namespace, s.Target.Name, s.Target.Owner},
			ExportOnly: s.ExportOnly, ExportOnlyReason: s.ExportOnlyReason,
			Params: canonicalJSON(s.Params), SpecHash: s.Precondition.SpecHash,
			Risk: s.Risk, Imposed: s.Imposed,
		})
	}
	for _, e := range p.Excluded {
		view.Excluded = append(view.Excluded, excludedView{
			Target: targetView{e.Target.Type, e.Target.Namespace, e.Target.Name, e.Target.Owner},
			Reason: e.Reason,
		})
	}
	sort.Slice(view.Excluded, func(i, j int) bool {
		a, b := view.Excluded[i], view.Excluded[j]
		if a.Target.Type != b.Target.Type {
			return a.Target.Type < b.Target.Type
		}
		if a.Target.Namespace != b.Target.Namespace {
			return a.Target.Namespace < b.Target.Namespace
		}
		if a.Target.Name != b.Target.Name {
			return a.Target.Name < b.Target.Name
		}
		return a.Reason < b.Reason
	})
	return hashJSON(view)
}

func canonicalScope(s Scope) Scope {
	out := Scope{Namespaces: append([]string{}, s.Namespaces...), Workloads: append([]string{}, s.Workloads...)}
	sort.Strings(out.Namespaces)
	sort.Strings(out.Workloads)
	return out
}

// canonicalJSON re-encodes raw JSON with sorted keys and no spacing, so the
// same params written two ways hash the same.
func canonicalJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw // Validate reports it; hash what is there
	}
	out, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return out
}

func hashJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		// Every input is built from plain types above; a failure is a bug.
		panic("kobiplan: hash input does not marshal: " + err.Error())
	}
	sum := sha256.Sum256(b)
	return hashPrefix + hex.EncodeToString(sum[:])
}
