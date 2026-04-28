package cluster

import (
	"errors"
	"strings"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/agent/channel"
)

func TestClusterAccess_LocalRestConfig_BadPath(t *testing.T) {
	// A non-existent kubeconfig path should surface as an error from
	// clientcmd, not a panic. We don't validate the success path here
	// because that would require a real kubeconfig; the connector
	// integration test covers it end-to-end.
	a := NewLocalAccess("/no/such/kubeconfig", "ctx")
	_, err := a.RestConfig()
	if err == nil {
		t.Fatal("expected error from missing kubeconfig path")
	}
}

func TestClusterAccess_AgentProxyRestConfig(t *testing.T) {
	reg := channel.NewAgentRegistry()
	a := NewAgentProxyAccess("c-prod", reg)
	cfg, err := a.RestConfig()
	if err != nil {
		t.Fatalf("RestConfig err = %v", err)
	}
	if want := "https://c-prod.agent.local"; cfg.Host != want {
		t.Errorf("Host = %q, want %q", cfg.Host, want)
	}
	if cfg.Transport == nil {
		t.Fatal("Transport must be set for agent-proxy mode")
	}
	tr, ok := cfg.Transport.(*channel.AgentProxyTransport)
	if !ok {
		t.Fatalf("Transport = %T, want *channel.AgentProxyTransport", cfg.Transport)
	}
	if tr.ClusterID != "c-prod" {
		t.Errorf("transport.ClusterID = %q", tr.ClusterID)
	}
	if tr.Registry != reg {
		t.Error("transport.Registry must be the registry we passed in")
	}
}

func TestClusterAccess_AgentProxy_RejectsEmptyClusterID(t *testing.T) {
	reg := channel.NewAgentRegistry()
	_, err := NewAgentProxyAccess("", reg).RestConfig()
	if err == nil || !strings.Contains(err.Error(), "cluster_id") {
		t.Fatalf("err = %v, want empty-cluster_id failure", err)
	}
}

func TestClusterAccess_AgentProxy_RejectsNilRegistry(t *testing.T) {
	_, err := NewAgentProxyAccess("c1", nil).RestConfig()
	if err == nil || !strings.Contains(err.Error(), "registry") {
		t.Fatalf("err = %v, want nil-registry failure", err)
	}
}

func TestClusterAccess_RejectsNilReceiver(t *testing.T) {
	var a *ClusterAccess
	_, err := a.RestConfig()
	if err == nil {
		t.Fatal("nil access should error, not panic")
	}
}

func TestClusterAccess_RejectsUnknownMode(t *testing.T) {
	a := &ClusterAccess{Mode: "weird"}
	_, err := a.RestConfig()
	if err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("err = %v, want unknown-mode failure", err)
	}
}

func TestClusterAccess_Name(t *testing.T) {
	if got := NewLocalAccess("", "kind-foo").Name(); got != "kind-foo" {
		t.Errorf("local Name = %q", got)
	}
	if got := NewInClusterAccess().Name(); got != "in-cluster" {
		t.Errorf("in-cluster Name = %q", got)
	}
	if got := NewAgentProxyAccess("c-edge", channel.NewAgentRegistry()).Name(); got != "c-edge" {
		t.Errorf("agent-proxy Name = %q", got)
	}
}

func TestAgentProxyContextName(t *testing.T) {
	if got := AgentProxyContextName("c-prod"); got != "agent:c-prod" {
		t.Errorf("got %q, want agent:c-prod", got)
	}
}

func TestClusterAccess_InClusterError(t *testing.T) {
	// rest.InClusterConfig() looks for /var/run/secrets/.../token. On
	// dev machines that file isn't there; the call returns an error
	// instead of panicking. Pin that contract — we want the manager
	// to surface it cleanly.
	_, err := NewInClusterAccess().RestConfig()
	if err == nil {
		t.Skip("running inside a pod — skipping negative case")
	}
	if !strings.Contains(err.Error(), "in-cluster") && !errors.Is(err, err) {
		t.Errorf("err = %v, want in-cluster context", err)
	}
}
