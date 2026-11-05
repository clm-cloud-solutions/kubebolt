package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
)

func TestApplyYAMLStatus(t *testing.T) {
	secrets := schema.GroupResource{Resource: "secrets"}
	cases := []struct {
		name string
		err  error
		want int
	}{
		// What the API server answered on kind for plain text under a Secret's data.
		{"value that is not base64", apierrors.NewBadRequest(`Secret in version "v1" cannot be handled as a Secret: illegal base64 data at input byte 5`), http.StatusBadRequest},
		{"edit refused before sending", &cluster.EditRefusedError{Reason: "no value to keep"}, http.StatusBadRequest},
		{"YAML that does not parse", errors.New("invalid YAML: yaml: line 3: mapping values are not allowed"), http.StatusBadRequest},
		{"edited over a newer version", apierrors.NewConflict(secrets, "db", errors.New("the object has been modified")), http.StatusConflict},
		{"no right to read it first", fmt.Errorf("reading the current object: %w", apierrors.NewForbidden(secrets, "db", errors.New("denied"))), http.StatusForbidden},
		{"deleted meanwhile", fmt.Errorf("reading the current object: %w", apierrors.NewNotFound(secrets, "db")), http.StatusNotFound},
		{"anything else", errors.New("dial tcp: connection refused"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		if got := applyYAMLStatus(tc.err); got != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, got, tc.want)
		}
	}
}

// The editor shows "Keep as binary" only when binaryValues comes back, and
// sends ?keepBinary=true with the same text.
func TestRespondEditRefused_NamesTheBinaryValues(t *testing.T) {
	for _, tc := range []struct {
		refused *cluster.EditRefusedError
		want    []string
	}{
		{&cluster.EditRefusedError{Reason: "binary", BinaryValues: []string{"password"}}, []string{"password"}},
		{&cluster.EditRefusedError{Reason: "renamed"}, nil},
	} {
		rec := httptest.NewRecorder()
		respondEditRefused(rec, tc.refused)
		var body struct {
			Error        string   `json:"error"`
			BinaryValues []string `json:"binaryValues"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if rec.Code != http.StatusBadRequest || body.Error != tc.refused.Reason || fmt.Sprint(body.BinaryValues) != fmt.Sprint(tc.want) {
			t.Errorf("%d %+v, want 400 %q %v", rec.Code, body, tc.refused.Reason, tc.want)
		}
	}

	for url, want := range map[string]bool{"/x?keepBinary=true": true, "/x": false, "/x?keepBinary=1": false} {
		if got := writeOptionsOf(httptest.NewRequest(http.MethodPut, url, nil)).KeepBinary; got != want {
			t.Errorf("%s: keepBinary %v, want %v", url, got, want)
		}
	}
}
