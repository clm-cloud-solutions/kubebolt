package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

// The {context} segment is decoded exactly once, whichever form chi matched.
func TestContextURLParam_DecodesOnce(t *testing.T) {
	cases := map[string]string{
		"/clusters/agent%3Auid-1":                                      "agent:uid-1",
		"/clusters/arn%3Aaws%3Aeks%3Aus-east-1%3A123%3Acluster%2Fprod": "arn:aws:eks:us-east-1:123:cluster/prod",
		"/clusters/kind-dev":                                           "kind-dev",
		"/clusters/100%25":                                             "100%",
		"/clusters/a%2541b":                                            "a%41b",
	}
	for path, want := range cases {
		var got string
		var err error
		r := chi.NewRouter()
		r.Get("/clusters/{context}", func(w http.ResponseWriter, req *http.Request) {
			got, err = contextURLParam(req)
		})
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
		if err != nil || got != want {
			t.Errorf("%s: got %q (err %v), want %q", path, got, err, want)
		}
	}
}
