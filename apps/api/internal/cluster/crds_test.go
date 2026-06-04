package cluster

import (
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// These exercise the real unstructured field-extraction that runs against
// live CRD objects (the riskiest part of the dynamic-client path) — using
// objects shaped exactly as cert-manager / ArgoCD / VPA produce them, so
// they verify the feature behavior short of a live cluster.

func TestOptionalCRDToMap_Certificate(t *testing.T) {
	notAfter := time.Now().Add(240 * time.Hour).UTC().Format(time.RFC3339) // ~10d out
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "cert-manager.io/v1",
		"kind":       "Certificate",
		"metadata":   map[string]interface{}{"name": "web-tls", "namespace": "prod"},
		"spec": map[string]interface{}{
			"secretName": "web-tls-secret",
			"commonName": "web.example.com",
			"dnsNames":   []interface{}{"web.example.com", "www.example.com"},
			"issuerRef":  map[string]interface{}{"name": "letsencrypt-prod"},
		},
		"status": map[string]interface{}{
			"notAfter": notAfter,
			"conditions": []interface{}{
				map[string]interface{}{"type": "Ready", "status": "True"},
			},
		},
	}}
	m := optionalCRDToMap("certificates", obj)
	if m["status"] != "Ready" {
		t.Fatalf("status = %v, want Ready", m["status"])
	}
	if m["issuer"] != "letsencrypt-prod" {
		t.Fatalf("issuer = %v", m["issuer"])
	}
	if m["secretName"] != "web-tls-secret" {
		t.Fatalf("secretName = %v", m["secretName"])
	}
	dns, _ := m["dnsNames"].([]string)
	if len(dns) != 2 || dns[0] != "web.example.com" {
		t.Fatalf("dnsNames = %v", m["dnsNames"])
	}
	days, ok := m["expiresInDays"].(int)
	if !ok || days < 9 || days > 10 {
		t.Fatalf("expiresInDays = %v (want ~9-10)", m["expiresInDays"])
	}
}

func TestOptionalCRDToMap_CertificateNotReady(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{"name": "broken", "namespace": "prod"},
		"status": map[string]interface{}{
			"conditions": []interface{}{
				map[string]interface{}{"type": "Ready", "status": "False"},
			},
		},
	}}
	if m := optionalCRDToMap("certificates", obj); m["status"] != "NotReady" {
		t.Fatalf("status = %v, want NotReady", m["status"])
	}
}

func TestOptionalCRDToMap_ArgoApplication(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{"name": "checkout", "namespace": "argocd"},
		"spec":     map[string]interface{}{"project": "default"},
		"status": map[string]interface{}{
			"sync":   map[string]interface{}{"status": "OutOfSync", "revision": "abc123"},
			"health": map[string]interface{}{"status": "Degraded"},
		},
	}}
	m := optionalCRDToMap("argocdapps", obj)
	if m["syncStatus"] != "OutOfSync" || m["healthStatus"] != "Degraded" {
		t.Fatalf("sync/health = %v/%v", m["syncStatus"], m["healthStatus"])
	}
	if m["status"] != "Degraded" { // health takes precedence in the one-word status
		t.Fatalf("status = %v, want Degraded", m["status"])
	}
	if m["project"] != "default" || m["revision"] != "abc123" {
		t.Fatalf("project/revision = %v/%v", m["project"], m["revision"])
	}
}

func TestOptionalCRDToMap_VPA(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{"name": "api-vpa", "namespace": "prod"},
		"spec": map[string]interface{}{
			"targetRef":    map[string]interface{}{"name": "api"},
			"updatePolicy": map[string]interface{}{"updateMode": "Auto"},
		},
		"status": map[string]interface{}{
			"recommendation": map[string]interface{}{
				"containerRecommendations": []interface{}{
					map[string]interface{}{
						"containerName": "app",
						"target":        map[string]interface{}{"cpu": "250m", "memory": "256Mi"},
					},
				},
			},
		},
	}}
	m := optionalCRDToMap("vpas", obj)
	if m["status"] != "Active" || m["targetRef"] != "api" || m["updateMode"] != "Auto" {
		t.Fatalf("status/targetRef/updateMode = %v/%v/%v", m["status"], m["targetRef"], m["updateMode"])
	}
	recs, ok := m["recommendations"].([]map[string]interface{})
	if !ok || len(recs) != 1 {
		t.Fatalf("recommendations = %v", m["recommendations"])
	}
	if recs[0]["container"] != "app" || recs[0]["targetCPU"] != "250m" || recs[0]["targetMem"] != "256Mi" {
		t.Fatalf("rec[0] = %v", recs[0])
	}
}
