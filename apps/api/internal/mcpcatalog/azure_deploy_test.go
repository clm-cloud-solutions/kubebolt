package mcpcatalog

import (
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
)

// azureDeployFile reads a deployment file from the repository root, or skips
// when this checkout does not ship it.
func azureDeployFile(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile("../../../../" + rel)
	if err != nil {
		t.Skipf("%s not present in this checkout", rel)
	}
	return string(raw)
}

// azureListAfter returns the "- item" lines that follow the first line equal
// to header once start has been seen, up to the first line that is not a list
// item.
func azureListAfter(text, start, header string) []string {
	var out []string
	seen, in := false, false
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case !seen:
			seen = strings.Contains(line, start)
		case !in:
			in = trimmed == header
		case strings.HasPrefix(trimmed, "- "):
			out = append(out, strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")))
		default:
			return out
		}
	}
	return out
}

// azureEnvValue returns the value given to name after start: the compose
// "NAME=value" form or the chart's "- name: NAME" / "value: ..." pair.
func azureEnvValue(text, start, name string) string {
	seen, next := false, false
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case !seen:
			seen = strings.Contains(line, start)
		case next:
			return strings.TrimSpace(strings.TrimPrefix(trimmed, "value:"))
		case trimmed == "- name: "+name:
			next = true
		case strings.HasPrefix(trimmed, "- "+name+"="):
			return strings.TrimPrefix(trimmed, "- "+name+"=")
		}
	}
	return ""
}

// The chart and the compose file start the sidecar the API was reviewed
// against: the same options and namespaces as the setup guide, the image of
// azmcpVersion, and the loopback address of DefaultAzmcpURL. A bumped image
// or a dropped option would otherwise reach installs with nothing failing.
func TestAzureDeploymentsStartTheReviewedSidecar(t *testing.T) {
	values := azureDeployFile(t, "deploy/helm/kubebolt/values.yaml")
	chart := azureDeployFile(t, "deploy/helm/kubebolt/templates/deployment.yaml")
	compose := azureDeployFile(t, "deploy/docker-compose.yml")

	want := append([]string{}, azureSidecarFlags...)
	for _, ns := range azureSidecarNamespaces() {
		want = append(want, "--namespace", ns)
	}
	if got := azureListAfter(chart, "- name: azure-mcp", "args:"); !reflect.DeepEqual(got, want) {
		t.Errorf("the chart starts the sidecar with %v, want %v", got, want)
	}
	if got := azureListAfter(compose, "azure-mcp:", "command:"); !reflect.DeepEqual(got, want) {
		t.Errorf("docker-compose starts the sidecar with %v, want %v", got, want)
	}

	var tag, digest string
	seen := false
	for _, line := range strings.Split(values, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "repository: mcr.microsoft.com/azure-sdk/azure-mcp":
			seen = true
		case seen && strings.HasPrefix(trimmed, "tag:"):
			tag = strings.TrimSpace(strings.TrimPrefix(trimmed, "tag:"))
		case seen && strings.HasPrefix(trimmed, "digest:"):
			digest = strings.TrimSpace(strings.TrimPrefix(trimmed, "digest:"))
			seen = false
		}
	}
	if tag != azmcpVersion {
		t.Errorf("values.yaml pins azure-mcp %q, the allowlist was reviewed against %q", tag, azmcpVersion)
	}
	if !strings.HasPrefix(digest, "sha256:") {
		t.Errorf("values.yaml pins azure-mcp without a digest: %q", digest)
	}
	image := "mcr.microsoft.com/azure-sdk/azure-mcp:" + tag + "@" + digest
	if !strings.Contains(compose, "image: "+image+"\n") {
		t.Errorf("docker-compose does not run %s", image)
	}

	u, err := url.Parse(DefaultAzmcpURL)
	if err != nil {
		t.Fatal(err)
	}
	listen := u.Scheme + "://" + u.Host
	if got := azureEnvValue(chart, "- name: azure-mcp", "ASPNETCORE_URLS"); got != listen {
		t.Errorf("the chart's sidecar listens on %q, the API calls %q", got, listen)
	}
	if got := azureEnvValue(compose, "azure-mcp:", "ASPNETCORE_URLS"); got != listen {
		t.Errorf("docker-compose's sidecar listens on %q, the API calls %q", got, listen)
	}
}
