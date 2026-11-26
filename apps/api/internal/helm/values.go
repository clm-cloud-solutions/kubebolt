package helm

import (
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
)

// EffectiveValues are the values a release renders with: the chart's
// defaults with what the person set merged over them, key by key, the way
// Helm merges them. A subchart's own defaults are not in the release
// record, so under a subchart's key only what the person set is here.
//
// RAW like DecodeReleaseDetail: never sent anywhere whole. Kobi Plans reads
// it to find where a container's resources live (kobiplan.FindResourcesPath).
type EffectiveValues struct {
	Name         string
	Namespace    string
	Chart        string
	ChartVersion string
	Revision     int
	Values       map[string]any
}

// DecodeEffectiveValues decodes the latest revision of (namespace, name)
// from its storage Secrets.
func DecodeEffectiveValues(namespace, name string, secrets []corev1.Secret) (*EffectiveValues, error) {
	var revisions []*helmReleaseJSON
	for i := range secrets {
		raw, ok := secrets[i].Data["release"]
		if !ok {
			continue
		}
		rls, err := decodeReleaseData(raw)
		if err != nil {
			continue
		}
		if rls.Namespace == namespace && rls.Name == name {
			revisions = append(revisions, rls)
		}
	}
	if len(revisions) == 0 {
		return nil, fmt.Errorf("release %s/%s not found", namespace, name)
	}
	sort.Slice(revisions, func(i, j int) bool { return revisions[i].Version > revisions[j].Version })
	latest := revisions[0]
	return &EffectiveValues{
		Name:         latest.Name,
		Namespace:    latest.Namespace,
		Chart:        latest.Chart.Metadata.Name,
		ChartVersion: latest.Chart.Metadata.Version,
		Revision:     latest.Version,
		Values:       MergeValues(latest.Chart.Values, latest.Config),
	}, nil
}

// MergeValues returns base with over merged on top: maps merge key by key,
// anything else in over replaces what base had, and a null in over deletes
// the key (Helm's rule for removing a default). Neither input is modified.
func MergeValues(base, over map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(over))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		if v == nil {
			delete(out, k)
			continue
		}
		bm, bok := out[k].(map[string]any)
		om, ook := v.(map[string]any)
		if bok && ook {
			out[k] = MergeValues(bm, om)
			continue
		}
		out[k] = v
	}
	return out
}
