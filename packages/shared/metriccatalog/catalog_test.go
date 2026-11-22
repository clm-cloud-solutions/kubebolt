package metriccatalog

import (
	"bufio"
	"os"
	"strings"
	"testing"

	"github.com/kubebolt/kubebolt/packages/shared/costmetrics"
)

// What the clusters really send must all be core: a name missing here would be
// counted as unknown and, once the doors check the catalog, dropped from an
// install that did nothing wrong.
func TestEveryNameObservedInProductionIsCore(t *testing.T) {
	f, err := os.Open("testdata/observed-2026-11-21.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		name := strings.TrimSpace(sc.Text())
		if name == "" || strings.HasPrefix(name, "#") {
			continue
		}
		n++
		if c, _ := Classify(name); c != Core {
			t.Errorf("%s is observed in production but classifies as %s", name, c)
		}
	}
	if n < 100 {
		t.Fatalf("read only %d names from testdata: the file was truncated", n)
	}
}

func TestTheCostFamiliesAreCore(t *testing.T) {
	for _, n := range costmetrics.Families {
		if !IsCore(n) {
			t.Errorf("cost family %s is not core: the Cost page reads it", n)
		}
	}
}

// The prefix is not the test. A customer's metric under a core prefix, a
// custom-resource-state metric (named by each customer's own config) and a
// made-up name from a naming guide are all unknown.
func TestACorePrefixAloneIsNotCore(t *testing.T) {
	for _, n := range []string{
		"kube_myapp_requests_total",
		"container_myapp_requests_total",
		"node_myapp_temperature",
		"kube_customresource_ready_count",
		"kube_pod_total",
		"myapp_requests_total",
		"",
	} {
		if c, _ := Classify(n); c != Unknown {
			t.Errorf("Classify(%q) = %s, want unknown", n, c)
		}
	}
}

func TestOpenCostsKSMCompatibleNamesAreCore(t *testing.T) {
	// OpenCost still publishes kube-state-metrics v1 names; Kobi and the
	// metrics-only overview read some of them on clusters without KSM.
	for _, n := range []string{
		"kube_node_status_allocatable_cpu_cores",
		"kube_node_status_capacity_memory_bytes",
		"kube_pod_container_resource_requests",
		"kube_pod_owner",
		"kube_namespace_labels",
	} {
		if !IsCore(n) {
			t.Errorf("%s is published by OpenCost and read by the product, but is not core", n)
		}
	}
}

func TestKSMListLooksComplete(t *testing.T) {
	if len(ksmNames) < 350 {
		t.Fatalf("only %d kube-state-metrics names: regenerate with gen_ksm_names.sh", len(ksmNames))
	}
	for _, n := range ksmNames {
		if strings.HasPrefix(n, "kube_customresource_") {
			t.Errorf("%s is a custom-resource-state metric, named by a customer's config: it cannot be core", n)
		}
	}
}

func TestFamiliesDoNotOverlapCore(t *testing.T) {
	seen := map[string]string{}
	for _, f := range Families {
		if f.ID == "" || f.Integration == "" || f.Use == "" {
			t.Errorf("family %+v needs an ID, an integration and a use", f)
		}
		for _, n := range f.Names {
			if IsCore(n) {
				t.Errorf("%s is in family %s and also core", n, f.ID)
			}
			if other, ok := seen[n]; ok {
				t.Errorf("%s is in families %s and %s", n, other, f.ID)
			}
			seen[n] = f.ID
		}
	}
}

func TestCoreNamesIsSortedAndComplete(t *testing.T) {
	names := CoreNames()
	if len(names) != len(core) {
		t.Fatalf("CoreNames returned %d names, the set holds %d", len(names), len(core))
	}
	for i := 1; i < len(names); i++ {
		if names[i-1] >= names[i] {
			t.Fatalf("CoreNames is not sorted at %q, %q", names[i-1], names[i])
		}
	}
}
