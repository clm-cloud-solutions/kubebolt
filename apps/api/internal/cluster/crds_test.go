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

func TestOptionalCRDToMap_CiliumNetworkPolicy_L7(t *testing.T) {
	// A CNP shaped like the 3-tier lab's visibility policy: selects shop-web,
	// allows ingress on :8080 with an HTTP L7 rule.
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "cilium.io/v2",
		"kind":       "CiliumNetworkPolicy",
		"metadata":   map[string]interface{}{"name": "frontend-http", "namespace": "shop-frontend"},
		"spec": map[string]interface{}{
			"endpointSelector": map[string]interface{}{
				"matchLabels": map[string]interface{}{"app": "shop-web"},
			},
			"ingress": []interface{}{
				map[string]interface{}{
					"fromEndpoints": []interface{}{
						map[string]interface{}{"matchLabels": map[string]interface{}{"app": "loadgen"}},
					},
					"toPorts": []interface{}{
						map[string]interface{}{
							"ports": []interface{}{
								map[string]interface{}{"port": "8080", "protocol": "TCP"},
							},
							"rules": map[string]interface{}{
								"http": []interface{}{
									map[string]interface{}{"method": "GET", "path": "/"},
								},
							},
						},
					},
				},
			},
			"egress": []interface{}{
				map[string]interface{}{
					"toEndpoints": []interface{}{
						map[string]interface{}{"matchLabels": map[string]interface{}{"app": "orders-api"}},
					},
				},
			},
		},
	}}
	m := optionalCRDToMap("ciliumnetworkpolicies", obj)
	if m["endpointSelector"] != "app=shop-web" {
		t.Fatalf("endpointSelector = %v", m["endpointSelector"])
	}
	if m["ingressRules"] != 1 || m["egressRules"] != 1 {
		t.Fatalf("ingress/egress = %v/%v", m["ingressRules"], m["egressRules"])
	}
	if m["hasL7"] != true {
		t.Fatalf("hasL7 = %v, want true", m["hasL7"])
	}
	l7, _ := m["l7Protocols"].([]string)
	if len(l7) != 1 || l7[0] != "http" {
		t.Fatalf("l7Protocols = %v, want [http]", m["l7Protocols"])
	}
	if m["status"] != "Enforcing" {
		t.Fatalf("status = %v, want Enforcing", m["status"])
	}
	rules, ok := m["policyRules"].([]map[string]interface{})
	if !ok || len(rules) != 2 {
		t.Fatalf("policyRules = %v (want 2)", m["policyRules"])
	}
	// Ingress rule should carry the from-peer and the HTTP L7 line.
	ing := rules[0]
	if ing["direction"] != "ingress" {
		t.Fatalf("rule[0].direction = %v", ing["direction"])
	}
	peers, _ := ing["peers"].([]string)
	if len(peers) != 1 || peers[0] != "endpoint: app=loadgen" {
		t.Fatalf("rule[0].peers = %v", ing["peers"])
	}
	l7lines, _ := ing["l7"].([]string)
	if len(l7lines) != 1 || l7lines[0] != "http: GET /" {
		t.Fatalf("rule[0].l7 = %v", ing["l7"])
	}
}

// CCNP with a `specs` array (multiple rules) + a cluster-entity egress —
// exercises the specs[] flattening and the entity peer rendering.
func TestOptionalCRDToMap_CiliumClusterwide_Specs(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "cilium.io/v2",
		"kind":       "CiliumClusterwideNetworkPolicy",
		"metadata":   map[string]interface{}{"name": "allow-dns"},
		"specs": []interface{}{
			map[string]interface{}{
				"endpointSelector": map[string]interface{}{}, // empty = all pods
				"egress": []interface{}{
					map[string]interface{}{
						"toEntities": []interface{}{"kube-apiserver"},
					},
				},
			},
		},
	}}
	m := optionalCRDToMap("ciliumclusterwidenetworkpolicies", obj)
	if m["endpointSelector"] != "all pods" {
		t.Fatalf("endpointSelector = %v, want 'all pods'", m["endpointSelector"])
	}
	if m["egressRules"] != 1 {
		t.Fatalf("egressRules = %v, want 1", m["egressRules"])
	}
	rules, _ := m["policyRules"].([]map[string]interface{})
	if len(rules) != 1 {
		t.Fatalf("policyRules len = %d, want 1", len(rules))
	}
	peers, _ := rules[0]["peers"].([]string)
	if len(peers) != 1 || peers[0] != "entity: kube-apiserver" {
		t.Fatalf("peers = %v", rules[0]["peers"])
	}
}
