package cluster

import (
	"io"
	"net/http"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/rest"

	"github.com/kubebolt/kubebolt/apps/api/internal/agent/channel"
)

func refusal(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Status: http.StatusText(code), Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}
}

// An agent in reader mode has no pods/exec: the API server answers the upgrade
// with a Forbidden Status, which the agent forwards unchanged. It has to reach
// the terminal as a Forbidden error, not as "expected 101, got 403".
func TestAgentProxyUpgrader_RefusedUpgradeIsTheAPIServersStatus(t *testing.T) {
	body := `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure",` +
		`"message":"pods \"web-0\" is forbidden: User \"system:serviceaccount:kubebolt-system:kubebolt-agent\" cannot create resource \"pods/exec\" in API group \"\" in the namespace \"shop\"",` +
		`"reason":"Forbidden","details":{"name":"web-0","kind":"pods"},"code":403}`
	_, err := (&agentProxyUpgrader{}).NewConnection(refusal(http.StatusForbidden, body))
	if !apierrors.IsForbidden(err) {
		t.Fatalf("err = %v, want a Forbidden StatusError", err)
	}
	if !strings.Contains(err.Error(), `cannot create resource "pods/exec"`) {
		t.Errorf("message lost the API server's reason: %v", err)
	}
}

func TestUpgradeFailure_ClassifiesABodyThatIsNotAStatus(t *testing.T) {
	if err := upgradeFailure(refusal(http.StatusForbidden, "forbidden by policy")); !apierrors.IsForbidden(err) {
		t.Errorf("403 with a plain body: err = %v, want Forbidden", err)
	}
	if err := upgradeFailure(refusal(http.StatusNotFound, "")); !apierrors.IsNotFound(err) {
		t.Errorf("404 with no body: err = %v, want NotFound", err)
	}
}

func TestIsAgentProxyConfig(t *testing.T) {
	if !IsAgentProxyConfig(&rest.Config{Transport: &channel.AgentProxyTransport{}}) {
		t.Error("agent-proxy transport: want true")
	}
	if IsAgentProxyConfig(&rest.Config{Host: "https://example"}) || IsAgentProxyConfig(nil) {
		t.Error("kubeconfig or nil config: want false")
	}
}
