package shipper

import (
	"testing"

	"github.com/kubebolt/kubebolt/packages/agent/internal/buffer"
)

func TestWithClusterIdent_PopulatesShipperFields(t *testing.T) {
	s := New("addr", "node-a", "v1", buffer.New(10),
		WithClusterIdent("c-prod-eks", "Prod EU"),
	)
	if s.clusterHint != "c-prod-eks" {
		t.Errorf("clusterHint = %q, want c-prod-eks", s.clusterHint)
	}
	if s.clusterName != "Prod EU" {
		t.Errorf("clusterName = %q, want Prod EU", s.clusterName)
	}
}

func TestWithClusterIdent_EmptyValuesAreOK(t *testing.T) {
	// Backend tolerates empty cluster_hint / labels and falls back to
	// the cluster_id derived from auth. Pin that no validation
	// silently drops the agent on its way to Hello.
	s := New("addr", "node-a", "v1", buffer.New(10),
		WithClusterIdent("", ""),
	)
	if s.clusterHint != "" || s.clusterName != "" {
		t.Errorf("empty values must be preserved: hint=%q name=%q", s.clusterHint, s.clusterName)
	}
}

func TestNew_DefaultsHaveNoClusterIdent(t *testing.T) {
	s := New("addr", "node-a", "v1", buffer.New(10))
	if s.clusterHint != "" || s.clusterName != "" {
		t.Errorf("defaults must leave cluster ident empty: hint=%q name=%q", s.clusterHint, s.clusterName)
	}
}
