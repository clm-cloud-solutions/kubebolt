package cluster

import (
	"errors"
	"fmt"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/kubebolt/kubebolt/apps/api/internal/agent/channel"
)

// AccessMode names how the backend reaches a given Kubernetes cluster.
type AccessMode string

const (
	// AccessModeLocal uses a kubeconfig context — direct apiserver
	// reachability from wherever the backend runs. The historical
	// single-cluster mode.
	AccessModeLocal AccessMode = "local"

	// AccessModeInCluster uses the pod's mounted ServiceAccount token,
	// applicable only when the backend itself runs as a Pod.
	AccessModeInCluster AccessMode = "in-cluster"

	// AccessModeAgentProxy reaches a remote cluster's apiserver
	// through a kubebolt-agent's outbound bidi channel. The backend
	// has no direct network path to the cluster — every API call
	// rides over the agent's persistent gRPC stream.
	AccessModeAgentProxy AccessMode = "agent-proxy"
)

// ClusterAccess captures the wiring needed to obtain a *rest.Config
// for a single cluster. The downstream pieces (Connector, metrics
// Collector, dynamic client) stay mode-agnostic — they consume
// whatever Transport the *rest.Config carries.
//
// agent-proxy mode lets the backend serve clusters it has no direct
// network path to. The Host on the rest.Config is a synthetic
// `https://<cluster_id>.agent.local` so client-go has a valid URL to
// build its requests against; the real bytes never hit DNS — the
// AgentProxyTransport short-circuits RoundTrip and tunnels each call
// through the agent's gRPC stream.
type ClusterAccess struct {
	Mode AccessMode

	// Local-mode fields.
	KubeconfigPath    string
	KubeconfigContext string

	// Agent-proxy mode fields.
	ClusterID     string
	AgentRegistry *channel.AgentRegistry
}

// NewLocalAccess returns access for a kubeconfig context. An empty
// kubeconfigPath defers to client-go's default discovery (KUBECONFIG
// env, ~/.kube/config) — useful in tests, but production callers
// should always pass an explicit path.
func NewLocalAccess(kubeconfigPath, contextName string) *ClusterAccess {
	return &ClusterAccess{
		Mode:              AccessModeLocal,
		KubeconfigPath:    kubeconfigPath,
		KubeconfigContext: contextName,
	}
}

// NewInClusterAccess returns access using the pod's mounted SA token.
// Fails at RestConfig() if the backend isn't actually running as a Pod.
func NewInClusterAccess() *ClusterAccess {
	return &ClusterAccess{Mode: AccessModeInCluster}
}

// NewAgentProxyAccess returns access tunneled via the registered
// agent for the given cluster_id. The registry is consulted on every
// RoundTrip — a reconnect mid-flight just means the next call lands
// on the fresh Agent record, no transport reconfiguration needed.
func NewAgentProxyAccess(clusterID string, registry *channel.AgentRegistry) *ClusterAccess {
	return &ClusterAccess{
		Mode:          AccessModeAgentProxy,
		ClusterID:     clusterID,
		AgentRegistry: registry,
	}
}

// Name returns a human-friendly identifier for this access. For local
// access it is the kubeconfig context name; for agent-proxy access
// it is the cluster_id. Used as the cluster name field on Connector.
func (a *ClusterAccess) Name() string {
	if a == nil {
		return ""
	}
	switch a.Mode {
	case AccessModeAgentProxy:
		return a.ClusterID
	case AccessModeInCluster:
		if a.KubeconfigContext != "" {
			return a.KubeconfigContext
		}
		return "in-cluster"
	default:
		return a.KubeconfigContext
	}
}

// AgentProxyContextName is the contextName under which an agent-proxy
// cluster is advertised in the manager's clusters list. Lifted to a
// helper because both Manager.AddAgentProxyCluster (write side) and
// ListClusters (read side) need to agree on the format.
func AgentProxyContextName(clusterID string) string {
	return "agent:" + clusterID
}

// agentProxyAPIServerURL is the synthetic Host that goes into the
// rest.Config for an agent-proxy cluster. The hostname is never
// resolved — the Transport short-circuits RoundTrip — but client-go
// builds URLs from rest.Config.Host so we need something parseable
// and recognizable in logs.
func agentProxyAPIServerURL(clusterID string) string {
	return fmt.Sprintf("https://%s.agent.local", clusterID)
}

// agentProxyRestTimeout bounds calls that would otherwise be
// unbounded. We don't set rest.Config.Timeout for agent-proxy — the
// AgentProxyTransport already enforces its own DefaultTimeout, and
// stacking another Timeout here would just race against that. Kept
// as a constant so callers (e.g. tests) can mirror it if needed.
const agentProxyRestTimeout = 30 * time.Second

// RestConfig builds the *rest.Config for this access. Each mode owns
// its construction: local goes through clientcmd; in-cluster goes
// through rest.InClusterConfig; agent-proxy synthesizes a Host and
// plugs in our http.RoundTripper.
func (a *ClusterAccess) RestConfig() (*rest.Config, error) {
	if a == nil {
		return nil, errors.New("cluster access is nil")
	}
	switch a.Mode {
	case AccessModeLocal:
		rules := clientcmd.NewDefaultClientConfigLoadingRules()
		rules.ExplicitPath = a.KubeconfigPath
		overrides := &clientcmd.ConfigOverrides{CurrentContext: a.KubeconfigContext}
		cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("local access %q: %w", a.KubeconfigContext, err)
		}
		return cfg, nil
	case AccessModeInCluster:
		cfg, err := rest.InClusterConfig()
		if err != nil {
			return nil, fmt.Errorf("in-cluster access: %w", err)
		}
		return cfg, nil
	case AccessModeAgentProxy:
		if a.ClusterID == "" {
			return nil, errors.New("agent-proxy access: empty cluster_id")
		}
		if a.AgentRegistry == nil {
			return nil, fmt.Errorf("agent-proxy access %q: registry is nil", a.ClusterID)
		}
		return &rest.Config{
			Host:      agentProxyAPIServerURL(a.ClusterID),
			Transport: channel.NewAgentProxyTransport(a.ClusterID, a.AgentRegistry),
		}, nil
	}
	return nil, fmt.Errorf("unknown cluster access mode %q", a.Mode)
}
