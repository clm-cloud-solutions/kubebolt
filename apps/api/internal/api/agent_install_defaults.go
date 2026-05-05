package api

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// AgentInstallDefaults seeds the AgentInstallWizard / AddClusterWizard
// with the values the user would otherwise have to type by hand. The
// shape distinguishes "install agent in this cluster" (use internal
// Service DNS, the Helm release wires the Service automatically) from
// "register a remote cluster" (requires the agent-ingest Service to be
// reachable from outside — externalEndpoint must be non-empty).
type AgentInstallDefaults struct {
	// "in-cluster" when the backend is running with a ServiceAccount
	// token, "external" when it's a desktop binary / docker-compose. The
	// frontend uses this to pick which wizard mode to show by default.
	DeploymentMode string `json:"deploymentMode"`

	// SelfNamespace is the namespace where the KubeBolt backend itself
	// runs (read from the SA token mount). Empty when external. Useful
	// for the wizard to display "deployed in <ns>" context.
	SelfNamespace string `json:"selfNamespace,omitempty"`

	// InternalBackendUrl is the cluster-DNS form of agent-ingest. Use
	// this when installing the agent in the SAME cluster as KubeBolt —
	// it stays inside the pod network, no external exposure needed.
	InternalBackendUrl string `json:"internalBackendUrl,omitempty"`

	// ExternalEndpoint is the externally-reachable address of agent-
	// ingest, derived from a LoadBalancer's status.loadBalancer.ingress
	// entry or a NodePort. Empty when the Service is ClusterIP only —
	// in that case the AddClusterWizard MUST warn the user that remote
	// agents won't reach it.
	ExternalEndpoint string `json:"externalEndpoint,omitempty"`

	// Suggested namespace for the agent install (kubebolt-system is the
	// chart default). Surfaced so the wizard pre-fills it without
	// hardcoding the constant in three places.
	AgentNamespace string `json:"agentNamespace"`

	// AgentIngestService surfaces the inspected Service so the wizard
	// can render an "expose this" hint with concrete fields. nil when
	// external mode (no inspection possible without kubeconfig).
	AgentIngestService *AgentIngestServiceInfo `json:"agentIngestService,omitempty"`
}

// AgentIngestServiceInfo is what we found inspecting the agent-ingest
// Service in the backend's own namespace. Drives the "is the Service
// exposed externally?" decision in the AddClusterWizard.
type AgentIngestServiceInfo struct {
	Namespace  string  `json:"namespace"`
	Name       string  `json:"name"`
	Type       string  `json:"type"`             // ClusterIP | LoadBalancer | NodePort
	Port       int32   `json:"port"`             // service port (typically 9090)
	NodePort   int32   `json:"nodePort,omitempty"`
	ExternalIP string  `json:"externalIp,omitempty"` // LoadBalancer ingress IP
	Hostname   string  `json:"hostname,omitempty"`   // LoadBalancer ingress hostname
}

// handleAgentInstallDefaults returns a sensible pre-fill for the agent
// install / add-cluster wizards. Detects in-cluster vs external; in-
// cluster, reads the agent-ingest Service to surface the right
// internal/external endpoint hints.
//
// Admin-only — the deployment topology and Service IPs are operator
// information.
func (h *handlers) handleAgentInstallDefaults(w http.ResponseWriter, r *http.Request) {
	defaults := AgentInstallDefaults{
		AgentNamespace: "kubebolt-system",
	}

	cfg, err := rest.InClusterConfig()
	if err != nil {
		// Desktop binary / docker-compose. There's no Service to
		// inspect; the agent ingest is the backend's own gRPC port
		// (default 9090). Best default we have is the host the user
		// reached us on — unless that's localhost, in which case the
		// remote agent would need a real address anyway.
		defaults.DeploymentMode = "external"
		if ep := inferExternalEndpointFromRequest(r); ep != "" {
			defaults.ExternalEndpoint = ep
		}
		respondJSON(w, http.StatusOK, defaults)
		return
	}

	defaults.DeploymentMode = "in-cluster"
	defaults.SelfNamespace = readSelfNamespace()

	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		// In-cluster but couldn't build a client. Surface what we know.
		defaults.InternalBackendUrl = fmt.Sprintf(
			"kubebolt-agent-ingest.%s.svc.cluster.local:9090",
			coalesce(defaults.SelfNamespace, "kubebolt"),
		)
		respondJSON(w, http.StatusOK, defaults)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// Look up by component label so the discovery survives non-default
	// Helm release names. The chart consistently labels its agent-ingest
	// Service with app.kubernetes.io/component=agent-ingest.
	svcList, err := client.CoreV1().Services(defaults.SelfNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/component=agent-ingest",
	})
	if err != nil || len(svcList.Items) == 0 {
		// Permission denied / Service not found / not yet provisioned.
		// Fall back to the standard chart-default DNS name.
		defaults.InternalBackendUrl = fmt.Sprintf(
			"kubebolt-agent-ingest.%s.svc.cluster.local:9090",
			coalesce(defaults.SelfNamespace, "kubebolt"),
		)
		respondJSON(w, http.StatusOK, defaults)
		return
	}

	svc := svcList.Items[0]
	info := inspectAgentIngestService(&svc)
	defaults.AgentIngestService = info
	defaults.InternalBackendUrl = fmt.Sprintf(
		"%s.%s.svc.cluster.local:%d",
		svc.Name, svc.Namespace, info.Port,
	)

	// External endpoint when the Service has a real address. NodePort is
	// surfaced as a "host:port" hint with the literal "<NODE_IP>" — the
	// operator picks a node IP because the API server doesn't know which
	// node a remote agent should dial.
	switch svc.Spec.Type {
	case corev1.ServiceTypeLoadBalancer:
		if info.ExternalIP != "" {
			defaults.ExternalEndpoint = fmt.Sprintf("%s:%d", info.ExternalIP, info.Port)
		} else if info.Hostname != "" {
			defaults.ExternalEndpoint = fmt.Sprintf("%s:%d", info.Hostname, info.Port)
		}
	case corev1.ServiceTypeNodePort:
		if info.NodePort > 0 {
			defaults.ExternalEndpoint = fmt.Sprintf("<NODE_IP>:%d", info.NodePort)
		}
	}

	respondJSON(w, http.StatusOK, defaults)
}

func inspectAgentIngestService(svc *corev1.Service) *AgentIngestServiceInfo {
	info := &AgentIngestServiceInfo{
		Namespace: svc.Namespace,
		Name:      svc.Name,
		Type:      string(svc.Spec.Type),
		Port:      9090,
	}
	for _, p := range svc.Spec.Ports {
		if p.Name == "grpc" || p.Port == 9090 {
			info.Port = p.Port
			info.NodePort = p.NodePort
			break
		}
	}
	for _, ing := range svc.Status.LoadBalancer.Ingress {
		if ing.IP != "" {
			info.ExternalIP = ing.IP
			break
		}
		if ing.Hostname != "" {
			info.Hostname = ing.Hostname
			break
		}
	}
	return info
}

func readSelfNamespace() string {
	const path = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func coalesce(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// inferExternalEndpointFromRequest derives a "host:9090" hint for the
// AddClusterWizard in external (non-in-cluster) deployments. The HTTP
// API and the agent gRPC port both bind to the same host but on
// different ports, so the hostname the user reached us on is a good
// guess for the gRPC endpoint too. Returns empty when the host is a
// loopback address — those don't help a remote agent.
func inferExternalEndpointFromRequest(r *http.Request) string {
	if r == nil || r.Host == "" {
		return ""
	}
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		// r.Host can be a bare hostname when the request didn't include
		// a port (rare for HTTP, but handle it).
		host = r.Host
	}
	if isLoopbackHost(host) {
		return ""
	}
	return fmt.Sprintf("%s:9090", host)
}

func isLoopbackHost(host string) bool {
	h := strings.ToLower(strings.Trim(host, "[]"))
	if h == "" || h == "localhost" {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback() || ip.IsUnspecified()
	}
	return false
}
