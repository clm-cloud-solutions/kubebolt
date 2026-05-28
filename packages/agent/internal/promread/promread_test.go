package promread

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestConfig_ValidateSkippedWhenDisabled(t *testing.T) {
	c := Config{Enabled: false}
	if err := c.Validate(); err != nil {
		t.Errorf("Validate on disabled config should be nil, got %v", err)
	}
}

func TestConfig_ValidateRequiresURLWhenEnabled(t *testing.T) {
	c := Config{Enabled: true, Matchers: []string{`{__name__="up"}`}}
	if err := c.Validate(); err == nil {
		t.Error("expected error when URL empty")
	}
}

func TestConfig_ValidateRequiresMatchersWhenEnabled(t *testing.T) {
	c := Config{Enabled: true, URL: "http://x"}
	if err := c.Validate(); err == nil {
		t.Error("expected error when Matchers empty")
	}
}

func TestConfig_DefaultsApplied(t *testing.T) {
	c := Config{
		Enabled:  true,
		URL:      "http://x",
		Matchers: []string{`{__name__="up"}`},
	}
	r, err := NewReader(c)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if r.PollInterval() != DefaultPollInterval {
		t.Errorf("PollInterval default: got %v want %v", r.PollInterval(), DefaultPollInterval)
	}
}

func TestReader_CollectIteratesMatchers(t *testing.T) {
	var hits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.URL.Query().Get("query"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[
			{"metric":{"__name__":"up"},"values":[[1700000000,"1"]]}
		]}}`))
	}))
	t.Cleanup(srv.Close)

	r, err := NewReader(Config{
		Enabled:  true,
		URL:      srv.URL,
		Matchers: []string{`{__name__="up"}`, `{__name__="kube_pod_info"}`},
	})
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	samples, err := r.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(hits) != 2 {
		t.Errorf("expected 2 backend hits (one per matcher), got %d", len(hits))
	}
	// Each matcher returned 1 sample, so total = 2.
	if len(samples) != 2 {
		t.Errorf("expected 2 samples, got %d", len(samples))
	}
}

func TestReader_CollectReturnsPartialOnMatcherFailure(t *testing.T) {
	var call int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call++
		if call == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("boom"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[
			{"metric":{"__name__":"up"},"values":[[1700000000,"1"]]}
		]}}`))
	}))
	t.Cleanup(srv.Close)

	r, _ := NewReader(Config{
		Enabled:  true,
		URL:      srv.URL,
		Matchers: []string{`{__name__="up"}`, `{__name__="kube_pod_info"}`},
	})
	samples, err := r.Collect(context.Background())
	if err == nil {
		t.Fatal("expected error from first matcher failure")
	}
	if len(samples) != 1 {
		t.Errorf("expected 1 sample from the second matcher's success, got %d", len(samples))
	}
}

func TestReader_CollectRespectsContextDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block longer than the context deadline.
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	r, _ := NewReader(Config{
		Enabled:  true,
		URL:      srv.URL,
		Matchers: []string{`{__name__="up"}`},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := r.Collect(ctx)
	if err == nil {
		t.Fatal("expected context deadline error")
	}
}
