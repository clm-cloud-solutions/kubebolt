package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
)

// debt-20: an edit to a slice a controller rewrites is a 409 that says what to
// edit instead, so the YAML tab can open the Endpoints the slice mirrors.
func TestRespondManagedSlice(t *testing.T) {
	rec := httptest.NewRecorder()
	respondManagedSlice(rec, &cluster.ManagedSliceError{Source: "endpoints", Service: "shop-db"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409", rec.Code)
	}
	var body struct {
		Error        string            `json:"error"`
		ManagedSlice map[string]string `json:"managedSlice"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ManagedSlice["source"] != "endpoints" || body.ManagedSlice["service"] != "shop-db" || body.Error == "" {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}
