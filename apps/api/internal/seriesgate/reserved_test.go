package seriesgate

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestIsReservedMetricName(t *testing.T) {
	for name, want := range map[string]bool{
		// The platform's own families.
		"kubebolt_kobi_copilot_tokens_total":          true,
		"kubebolt_kobi_autopilot_incidents_total":     true,
		"kubebolt_ai_tokens_total":                    true,
		"kubebolt_autopilot_runs_total":               true,
		"kubebolt_cluster_team_info":                  true,
		"kubebolt_http_requests_total":                true,
		"kubebolt_vm_requests_total":                  true,
		"kubebolt_ws_clients":                         true,
		"kubebolt_job_last_success_timestamp_seconds": true,
		"kubebolt_pg_pool_connections":                true,
		"kubebolt_platform_leader":                    true,
		"kubebolt_prom_write_requests_total":          true,
		"kubebolt_api_runtimes":                       true,
		"kubebolt_agent_grpc_streams_total":           true,
		"kubebolt_agent_kube_request_seconds_bucket":  true,
		"kubebolt_agent_channels":                     true,
		"kubebolt_build_info":                         true,
		// The agent writes its own self-metrics through its door.
		"kubebolt_agent_heap_alloc_bytes":        false,
		"kubebolt_agent_info":                    false,
		"kubebolt_agent_samples_collected_total": false,
		"kubebolt_promread_leader":               false,
		"kubebolt_flow_collector_leader":         false,
		// Everything else is the customer's.
		"kube_pod_info":                 false,
		"process_resident_memory_bytes": false,
		"vm_rows_inserted_total":        false,
		"":                              false,
	} {
		if got := IsReservedMetricName(name); got != want {
			t.Errorf("IsReservedMetricName(%q) = %v, want %v", name, got, want)
		}
	}
}

// A server-owned family escapes the cap and the billing count, so it must
// never be writable by a customer: every one of them is reserved.
func TestServerOwnedFamiliesAreReserved(t *testing.T) {
	for _, p := range serverOwnedPrefixes {
		if !IsReservedMetricName(p + "anything_total") {
			t.Errorf("server-owned prefix %q is not reserved", p)
		}
	}
	if !IsReservedMetricName("kubebolt_cluster_team_info") {
		t.Error("kubebolt_cluster_team_info is not reserved")
	}
}

func TestMayHoldReserved(t *testing.T) {
	for payload, want := range map[string]bool{
		`kube_pod_info namespace kubebolt pod kubebolt-agent-x`: false, // the word alone is not a family
		`kubebolt_agent_heap_alloc_bytes tenant_id t1`:          false,
		`kubebolt_http_requests_total code 5xx`:                 true,
		`kubebolt_build_info version v2`:                        true,
		``:                                                      false,
	} {
		if got := MayHoldReserved([]byte(payload)); got != want {
			t.Errorf("MayHoldReserved(%q) = %v, want %v", payload, got, want)
		}
	}
}

// Every kubebolt_* family the API registers (a Name: field or a NewDesc) is
// one only the platform writes, so it must be reserved — or a customer could
// write it through remote_write. A new family under a new prefix fails here
// until it is added to reservedPrefixes / reservedExact.
func TestEveryFamilyTheAPIRegistersIsReserved(t *testing.T) {
	declared := regexp.MustCompile(`(?:Name:\s*|NewDesc\(\s*)"(kubebolt_[a-z0-9_]+)"`)
	seen := 0
	for _, dir := range []string{"../../internal", "../../cmd"} {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, m := range declared.FindAllSubmatch(src, -1) {
				seen++
				if name := string(m[1]); !IsReservedMetricName(name) {
					t.Errorf("%s registers %q, which is not reserved (seriesgate/reserved.go)", path, name)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if seen < 30 {
		t.Fatalf("found only %d kubebolt_* families in the API's source; the scan is not seeing them", seen)
	}
}
