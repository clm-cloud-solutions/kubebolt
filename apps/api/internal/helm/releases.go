// Package helm provides a READ-ONLY view of Helm 3 releases by decoding the
// Secrets Helm uses as its storage backend — no helm.sh/helm/v3 SDK import,
// so it adds zero dependency-tree / image-bloat / Trivy-gate risk. (Sprint 4;
// write actions + App Center are deferred — see internal/helm-applications-post-1.14.md.)
//
// Helm 3 stores each release revision as a Secret labeled owner=helm, named
// sh.helm.release.v1.<release>.v<revision>, type helm.sh/release.v1. The
// payload lives in data["release"] as base64(gzip(json)) (and client-go has
// already base64-decoded the Secret transport layer, so we see the inner
// base64 string).
package helm

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// Release is the read-only projection of a Helm release we surface in the UI.
type Release struct {
	Name         string         `json:"name"`
	Namespace    string         `json:"namespace"`
	Revision     int            `json:"revision"`
	Status       string         `json:"status"`
	Chart        string         `json:"chart"`
	ChartVersion string         `json:"chartVersion"`
	AppVersion   string         `json:"appVersion,omitempty"`
	Updated      time.Time      `json:"updated"`
	Description  string         `json:"description,omitempty"`
	FirstDeployed time.Time     `json:"firstDeployed,omitempty"`

	// Detail-only (populated by DecodeReleaseDetail, omitted in list view).
	Values   map[string]any `json:"values,omitempty"`
	Manifest string         `json:"manifest,omitempty"`
	Notes    string         `json:"notes,omitempty"`
	// History is the list of revisions for this release name (detail view).
	History []ReleaseRevision `json:"history,omitempty"`
	// Dependencies are the chart's declared sub-charts (detail view).
	Dependencies []ChartDependency `json:"dependencies,omitempty"`
}

// ReleaseRevision is one entry in a release's revision history.
type ReleaseRevision struct {
	Revision     int       `json:"revision"`
	Status       string    `json:"status"`
	ChartVersion string    `json:"chartVersion"`
	AppVersion   string    `json:"appVersion,omitempty"`
	Updated      time.Time `json:"updated"`
	Description  string    `json:"description,omitempty"`
}

// ChartDependency mirrors a Chart.yaml dependency entry.
type ChartDependency struct {
	Name       string `json:"name"`
	Version    string `json:"version,omitempty"`
	Repository string `json:"repository,omitempty"`
	Condition  string `json:"condition,omitempty"`
}

// helmReleaseJSON is the subset of helm.sh/helm/v3/pkg/release.Release we
// decode. Defined locally to avoid importing the SDK.
type helmReleaseJSON struct {
	Name      string         `json:"name"`
	Namespace string         `json:"namespace"`
	Version   int            `json:"version"`
	Manifest  string         `json:"manifest"`
	Config    map[string]any `json:"config"`
	Info      struct {
		FirstDeployed time.Time `json:"first_deployed"`
		LastDeployed  time.Time `json:"last_deployed"`
		Description   string    `json:"description"`
		Status        string    `json:"status"`
		Notes         string    `json:"notes"`
	} `json:"info"`
	Chart struct {
		Metadata struct {
			Name         string            `json:"name"`
			Version      string            `json:"version"`
			AppVersion   string            `json:"appVersion"`
			Dependencies []ChartDependency `json:"dependencies"`
		} `json:"metadata"`
	} `json:"chart"`
}

// decodeReleaseData decodes a Secret's data["release"] value into the Helm
// release JSON. Handles the base64(gzip(json)) encoding, tolerating an
// already-decompressed payload (defensive against driver changes).
func decodeReleaseData(raw []byte) (*helmReleaseJSON, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("empty release data")
	}
	// client-go already base64-decoded the Secret transport; raw is the
	// inner base64 string of gzipped JSON.
	b, err := base64.StdEncoding.DecodeString(string(raw))
	if err != nil {
		// Maybe it's already the gzip/json bytes (no inner base64).
		b = raw
	}
	// Gunzip if gzip-magic present.
	if len(b) >= 2 && b[0] == 0x1f && b[1] == 0x8b {
		gz, err := gzip.NewReader(bytes.NewReader(b))
		if err != nil {
			return nil, fmt.Errorf("gzip reader: %w", err)
		}
		defer gz.Close()
		b, err = io.ReadAll(gz)
		if err != nil {
			return nil, fmt.Errorf("gunzip: %w", err)
		}
	}
	var rls helmReleaseJSON
	if err := json.Unmarshal(b, &rls); err != nil {
		return nil, fmt.Errorf("unmarshal release: %w", err)
	}
	return &rls, nil
}

func toSummary(rls *helmReleaseJSON) Release {
	return Release{
		Name:          rls.Name,
		Namespace:     rls.Namespace,
		Revision:      rls.Version,
		Status:        rls.Info.Status,
		Chart:         rls.Chart.Metadata.Name,
		ChartVersion:  rls.Chart.Metadata.Version,
		AppVersion:    rls.Chart.Metadata.AppVersion,
		Updated:       rls.Info.LastDeployed,
		FirstDeployed: rls.Info.FirstDeployed,
		Description:   rls.Info.Description,
	}
}

// DecodeReleases decodes a set of Helm release storage Secrets into the
// LATEST revision per (namespace, name) — the list view. Secrets that fail
// to decode are skipped (a single bad value shouldn't blank the list).
func DecodeReleases(secrets []corev1.Secret) []Release {
	latest := make(map[string]Release) // key ns/name → highest-revision summary
	for i := range secrets {
		raw, ok := secrets[i].Data["release"]
		if !ok {
			continue
		}
		rls, err := decodeReleaseData(raw)
		if err != nil {
			continue
		}
		sum := toSummary(rls)
		key := sum.Namespace + "/" + sum.Name
		if cur, ok := latest[key]; !ok || sum.Revision > cur.Revision {
			latest[key] = sum
		}
	}
	out := make([]Release, 0, len(latest))
	for _, r := range latest {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// DecodeReleaseDetail decodes the detail view for one release: the latest
// revision's values/manifest/notes/dependencies plus the full revision
// history, from all storage Secrets that belong to (namespace, name).
func DecodeReleaseDetail(namespace, name string, secrets []corev1.Secret) (*Release, error) {
	var revisions []helmReleaseJSON
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
			revisions = append(revisions, *rls)
		}
	}
	if len(revisions) == 0 {
		return nil, fmt.Errorf("release %s/%s not found", namespace, name)
	}
	sort.Slice(revisions, func(i, j int) bool { return revisions[i].Version > revisions[j].Version })
	latest := &revisions[0]

	detail := toSummary(latest)
	detail.Values = latest.Config
	detail.Manifest = latest.Manifest
	detail.Notes = latest.Info.Notes
	detail.Dependencies = latest.Chart.Metadata.Dependencies
	for i := range revisions {
		r := &revisions[i]
		detail.History = append(detail.History, ReleaseRevision{
			Revision:     r.Version,
			Status:       r.Info.Status,
			ChartVersion: r.Chart.Metadata.Version,
			AppVersion:   r.Chart.Metadata.AppVersion,
			Updated:      r.Info.LastDeployed,
			Description:  r.Info.Description,
		})
	}
	return &detail, nil
}
