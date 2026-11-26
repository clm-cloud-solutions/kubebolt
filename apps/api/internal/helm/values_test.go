package helm

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func storedRelease(t *testing.T, rls map[string]any) corev1.Secret {
	t.Helper()
	raw, err := json.Marshal(rls)
	if err != nil {
		t.Fatal(err)
	}
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	_, _ = w.Write(raw)
	_ = w.Close()
	return corev1.Secret{Data: map[string][]byte{"release": []byte(base64.StdEncoding.EncodeToString(gz.Bytes()))}}
}

func TestMergeValues(t *testing.T) {
	base := map[string]any{
		"admissionController": map[string]any{"replicas": 1.0, "container": map[string]any{
			"resources": map[string]any{"requests": map[string]any{"cpu": "100m", "memory": "128Mi"}},
		}},
		"debug": true,
	}
	over := map[string]any{
		"admissionController": map[string]any{"container": map[string]any{
			"resources": map[string]any{"requests": map[string]any{"cpu": "50m"}},
		}},
		"debug": nil,
	}
	got := MergeValues(base, over)
	want := map[string]any{
		"admissionController": map[string]any{"replicas": 1.0, "container": map[string]any{
			"resources": map[string]any{"requests": map[string]any{"cpu": "50m", "memory": "128Mi"}},
		}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merged = %v", got)
	}
	if _, ok := base["debug"]; !ok {
		t.Fatal("the base was modified")
	}
}

// The latest revision wins, and its values are the chart's defaults with
// what the person set merged over them.
func TestDecodeEffectiveValues(t *testing.T) {
	rev := func(version int, cpu string) corev1.Secret {
		return storedRelease(t, map[string]any{
			"name": "kyverno", "namespace": "kyverno", "version": version,
			"config": map[string]any{"cleanupController": map[string]any{"resources": map[string]any{"requests": map[string]any{"cpu": cpu}}}},
			"chart": map[string]any{
				"metadata": map[string]any{"name": "kyverno", "version": "3.8.2"},
				"values":   map[string]any{"cleanupController": map[string]any{"resources": map[string]any{"requests": map[string]any{"cpu": "100m", "memory": "64Mi"}}}},
			},
		})
	}
	ev, err := DecodeEffectiveValues("kyverno", "kyverno", []corev1.Secret{rev(1, "80m"), rev(2, "90m")})
	if err != nil {
		t.Fatal(err)
	}
	if ev.Revision != 2 || ev.Chart != "kyverno" || ev.ChartVersion != "3.8.2" {
		t.Fatalf("release = %+v", ev)
	}
	req := ev.Values["cleanupController"].(map[string]any)["resources"].(map[string]any)["requests"].(map[string]any)
	if req["cpu"] != "90m" || req["memory"] != "64Mi" {
		t.Fatalf("requests = %v", req)
	}
	if _, err := DecodeEffectiveValues("kyverno", "other", []corev1.Secret{rev(1, "80m")}); err == nil {
		t.Fatal("found a release that is not there")
	}
}
