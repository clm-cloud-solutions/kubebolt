package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// rightSizingVM answers the two P95 queries with one row each and the history
// window query with windowStatus / windowBody.
func rightSizingVM(t *testing.T, windowStatus int, windowBody string) {
	t.Helper()
	row := `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"namespace":"ns","workload_kind":"Deployment","workload_name":"api"},"value":[1700000000,"0.5"]}]}}`
	vm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Query().Get("query"), "count_over_time") {
			w.WriteHeader(windowStatus)
			_, _ = w.Write([]byte(windowBody))
			return
		}
		_, _ = w.Write([]byte(row))
	}))
	t.Cleanup(vm.Close)
	t.Setenv("KUBEBOLT_METRICS_STORAGE_URL", vm.URL)
}

func identityScope(q string) string { return q }

// A failed history-window read is an error, not "no history": read as nil it
// made the gate answer "still collecting" over a cluster with weeks of data,
// and the cache held that answer for the whole TTL.
func TestReadRightSizingInputs_WindowQueryFailureIsAnError(t *testing.T) {
	rightSizingVM(t, http.StatusServiceUnavailable, `{"status":"error","error":"timeout"}`)
	if _, err := readRightSizingInputs(context.Background(), identityScope); err == nil {
		t.Fatal("a failed history-window query must surface as an error, not as missing history")
	}
}

// An empty window answer is the one case that means "no history".
func TestReadRightSizingInputs_EmptyWindowMeansNoHistory(t *testing.T) {
	rightSizingVM(t, http.StatusOK, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
	in, err := readRightSizingInputs(context.Background(), identityScope)
	if err != nil {
		t.Fatal(err)
	}
	if in.windowDays != nil {
		t.Errorf("an empty window answer must leave windowDays nil, got %v", *in.windowDays)
	}
	if len(in.cpu) != 1 || len(in.mem) != 1 {
		t.Errorf("the P95 rows must still be read: cpu=%d mem=%d", len(in.cpu), len(in.mem))
	}
}

func TestReadRightSizingInputs_WindowInDays(t *testing.T) {
	rightSizingVM(t, http.StatusOK, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1700000000,"36"]}]}}`)
	in, err := readRightSizingInputs(context.Background(), identityScope)
	if err != nil {
		t.Fatal(err)
	}
	if in.windowDays == nil || *in.windowDays != 1.5 {
		t.Fatalf("36 hourly buckets are 1.5 days, got %v", in.windowDays)
	}
}
