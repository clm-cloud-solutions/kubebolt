package integrations

import (
	"context"
	"testing"
)

// The compile-time assertion in opencost.go is the real contract
// check; these tests pin the semantics the E2 providers will rely on.

func TestOpenCostSignalProvider(t *testing.T) {
	p := NewOpenCost(nil, nil)

	sp, ok := p.(SignalProvider)
	if !ok {
		t.Fatal("openCostProvider must implement SignalProvider (framework's first impl)")
	}
	if sp.IngestMode() != IngestScrape {
		t.Errorf("IngestMode = %q, want %q", sp.IngestMode(), IngestScrape)
	}

	// Scrape-mode providers normalize agent-side; the backend hook is
	// a documented no-op that must never error or emit signals.
	sig, err := sp.Normalize(context.Background(), []byte("anything"))
	if err != nil {
		t.Fatalf("passthrough Normalize errored: %v", err)
	}
	if len(sig.Findings) != 0 || len(sig.Events) != 0 || len(sig.Samples) != 0 {
		t.Errorf("passthrough Normalize must return empty Signals, got %+v", sig)
	}
}

func TestBaseProvidersAreNotSignalProviders(t *testing.T) {
	// The interface is OPTIONAL: detection-only providers must keep
	// working without it. Guards against someone "helpfully" folding
	// SignalProvider into the base Provider interface later.
	if _, ok := NewAgent(nil, nil).(SignalProvider); ok {
		t.Error("agent provider should not implement SignalProvider (detection-only)")
	}
}
